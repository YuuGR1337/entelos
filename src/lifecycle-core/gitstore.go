package lifecycle

// The local Git adapter: one dedicated bare repository. Accepted state is
// published only through a compare-and-swap of one root ref.

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// RootRef is the only ref that publishes accepted state.
const RootRef = "refs/lifecycle/state"

var cursorPattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// GitResult is the result of one git command.
type GitResult struct {
	Code           int
	Stdout, Stderr []byte
}

// GitStore is the local Git adapter.
type GitStore struct {
	Path string
	env  []string
	// Hook replaces the git command runner. Tests use it for fault injection.
	Hook func(store *GitStore, args []string, data []byte) (GitResult, error)
}

// gitEnvironment is the process environment without GIT_* variables, plus a
// fixed Git configuration and identity.
func gitEnvironment() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "GIT_") {
			env = append(env, kv)
		}
	}
	return append(env, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL="+os.DevNull, "GIT_TERMINAL_PROMPT=0",
		"GIT_AUTHOR_NAME=Local lifecycle", "GIT_AUTHOR_EMAIL=lifecycle@example.invalid",
		"GIT_COMMITTER_NAME=Local lifecycle", "GIT_COMMITTER_EMAIL=lifecycle@example.invalid")
}

// OpenGitStore opens or makes a dedicated bare repository.
func OpenGitStore(path string) (*GitStore, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	s := &GitStore{Path: abs, env: gitEnvironment()}
	if _, err := os.Stat(abs); errors.Is(err, os.ErrNotExist) {
		if err := os.MkdirAll(filepath.Dir(abs), 0o777); err != nil {
			return nil, err
		}
		cmd := exec.Command("git", "init", "--bare", "--object-format=sha1", abs)
		cmd.Env = s.env
		if out, err := cmd.CombinedOutput(); err != nil {
			return nil, NewFault("store_error", string(out), "store")
		}
	}
	out, err := s.Git(nil, "rev-parse", "--is-bare-repository")
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(string(out)) != "true" {
		return nil, NewFault("invalid_store", "use a dedicated bare local repository", "store")
	}
	return s, nil
}

// RunGit runs one git command against the repository without hooks.
func (s *GitStore) RunGit(args []string, data []byte) (GitResult, error) {
	full := append([]string{"--git-dir=" + s.Path, "-c", "core.hooksPath=" + os.DevNull, "-c", "core.fsmonitor=false"}, args...)
	cmd := exec.Command("git", full...)
	cmd.Env = s.env
	if data != nil {
		cmd.Stdin = bytes.NewReader(data)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	var exit *exec.ExitError
	if err != nil && !errors.As(err, &exit) {
		return GitResult{}, NewFault("store_error", err.Error(), "store")
	}
	return GitResult{Code: cmd.ProcessState.ExitCode(), Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}, nil
}

func (s *GitStore) run(args []string, data []byte) (GitResult, error) {
	if s.Hook != nil {
		return s.Hook(s, args, data)
	}
	return s.RunGit(args, data)
}

// Git runs a command and gives a store_error on a nonzero exit.
func (s *GitStore) Git(data []byte, args ...string) ([]byte, error) {
	r, err := s.run(args, data)
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		return nil, NewFault("store_error", string(r.Stderr), "store")
	}
	return r.Stdout, nil
}

// Root gives the current accepted root, or "" before the first transaction.
func (s *GitStore) Root() (string, error) {
	r, err := s.run([]string{"rev-parse", "--verify", RootRef}, nil)
	if err != nil {
		return "", err
	}
	if r.Code != 0 {
		return "", nil
	}
	return strings.TrimSpace(string(r.Stdout)), nil
}

func (s *GitStore) History(cursor string) ([]string, error) {
	if cursor == "" {
		return nil, nil
	}
	out, err := s.Git(nil, "rev-list", "--first-parent", cursor)
	if err != nil {
		return nil, err
	}
	return lines(out), nil
}

func lines(out []byte) []string {
	var result []string
	for _, line := range strings.Split(string(out), "\n") {
		if line = strings.TrimSuffix(line, "\r"); line != "" {
			result = append(result, line)
		}
	}
	return result
}

func (s *GitStore) object(handle string) ([]byte, error) {
	return s.Git(nil, "cat-file", "blob", handle)
}

func (s *GitStore) Read(cursor any) (*View, error) {
	root, err := s.Root()
	if err != nil {
		return nil, err
	}
	at, err := cursorAt(cursor, root)
	if err != nil {
		return nil, err
	}
	if at == "" {
		return newView(s, "", EmptyState(), map[string]string{}), nil
	}
	history, err := s.History(root)
	if err != nil {
		return nil, err
	}
	if !cursorPattern.MatchString(at) || !contains(history, at) {
		return nil, NewFault("unknown_cursor", "cursor is not in accepted history", "store")
	}
	out, err := s.Git(nil, "ls-tree", "-z", at)
	if err != nil {
		return nil, err
	}
	entries := map[string]string{}
	for _, row := range bytes.Split(out, []byte{0}) {
		if len(row) == 0 {
			continue
		}
		meta, name, _ := bytes.Cut(row, []byte{'\t'})
		fields := strings.Fields(string(meta))
		if len(fields) != 3 || fields[0] != "100644" || fields[1] != "blob" {
			return nil, NewFault("invalid_store", "unexpected tree entry", "store")
		}
		entries[string(name)] = fields[2]
	}
	oid, ok := entries["state.json"]
	if !ok {
		return nil, NewFault("invalid_store", "missing state", "store")
	}
	raw, err := s.object(oid)
	if err != nil {
		return nil, err
	}
	value, err := Decode(raw)
	if err != nil {
		return nil, err
	}
	state, ok := value.(Object)
	if !ok || !equal(state["schema"], "local-lifecycle/v1") {
		return nil, NewFault("invalid_store", "unknown state schema", "store")
	}
	return newView(s, at, state, entries), nil
}

func contains(list []string, value string) bool {
	for _, x := range list {
		if x == value {
			return true
		}
	}
	return false
}

func (s *GitStore) Transact(expected, key, payload any, apply func(*View) (any, error)) (Object, error) {
	k, err := checkKey(key)
	if err != nil {
		return nil, err
	}
	view, err := s.Read(nil)
	if err != nil {
		return nil, err
	}
	digest := requestDigest(payload)
	if result, found, err := reuse(s, view, k, digest); found || err != nil {
		return result, err
	}
	if !equal(expected, view.Cursor) {
		return nil, NewFault("stale_revision", "capture the current root and re-evaluate", "")
	}
	result, err := apply(view)
	if err != nil {
		return nil, err
	}
	view.State["requests"].(Object)[k] = Object{"payload_ref": digest, "result": result}
	entries := make(map[string]string, len(view.Entries)+len(view.pending)+1)
	for name, oid := range view.Entries {
		entries[name] = oid
	}
	hash := func(data []byte) (string, error) {
		out, err := s.Git(data, "hash-object", "-w", "--stdin")
		return strings.TrimSpace(string(out)), err
	}
	if entries["state.json"], err = hash(Encode(view.State)); err != nil {
		return nil, err
	}
	for ref, body := range view.pending {
		if entries["o-"+ref[7:]], err = hash(body); err != nil {
			return nil, err
		}
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	var rows strings.Builder
	for _, name := range names {
		rows.WriteString("100644 blob " + entries[name] + "\t" + name + "\n")
	}
	tree, err := s.Git([]byte(rows.String()), "mktree")
	if err != nil {
		return nil, err
	}
	base := view.Cursor
	args := []string{"commit-tree", strings.TrimSpace(string(tree))}
	if base != "" {
		args = append(args, "-p", base)
	}
	out, err := s.Git([]byte("local lifecycle "+k+"\n"), args...)
	if err != nil {
		return nil, err
	}
	commit := strings.TrimSpace(string(out))
	old := base
	if old == "" {
		old = strings.Repeat("0", 40)
	}
	// This is the sole state publication point. No automatic retry after a lost CAS.
	r, err := s.run([]string{"update-ref", RootRef, commit, old}, nil)
	if err != nil {
		return nil, err
	}
	if r.Code != 0 {
		root, err := s.Root()
		if err != nil {
			return nil, err
		}
		if root != base {
			return nil, NewFault("stale_revision", "root CAS lost; no state/outbox was published", "")
		}
		return nil, NewFault("store_error", string(r.Stderr), "store")
	}
	return Object{"cursor": commit, "result": result, "reused": false}, nil
}
