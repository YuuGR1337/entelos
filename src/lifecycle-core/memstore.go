package lifecycle

// An in-memory adapter of the storage port. It keeps the same publication
// contract as the Git adapter: apply runs outside the lock, and the result is
// published only if the captured root is still current. It is not durable.

import (
	"crypto/sha1"
	"encoding/hex"
	"sort"
	"sync"
)

type memoryCommit struct {
	parent  string
	entries map[string]string
}

// MemoryStore is the in-memory adapter.
type MemoryStore struct {
	mu      sync.Mutex
	root    string
	commits map[string]memoryCommit
	objects map[string][]byte // handle (content reference) to envelope bytes
	// BeforePublish runs after apply and before the compare-and-swap. Tests
	// use it for fault injection. An error stops the publication.
	BeforePublish func() error
}

// NewMemoryStore makes an empty in-memory store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{commits: map[string]memoryCommit{}, objects: map[string][]byte{}}
}

func (s *MemoryStore) object(handle string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	body, ok := s.objects[handle]
	if !ok {
		return nil, NewFault("store_error", "missing object "+handle, "store")
	}
	return body, nil
}

func (s *MemoryStore) History(cursor string) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.history(cursor)
}

func (s *MemoryStore) history(cursor string) ([]string, error) {
	var result []string
	for at := cursor; at != ""; {
		c, ok := s.commits[at]
		if !ok {
			return nil, NewFault("store_error", "unknown revision "+at, "store")
		}
		result = append(result, at)
		at = c.parent
	}
	return result, nil
}

func (s *MemoryStore) Read(cursor any) (*View, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	at, err := cursorAt(cursor, s.root)
	if err != nil {
		return nil, err
	}
	if at == "" {
		return newView(s, "", EmptyState(), map[string]string{}), nil
	}
	history, _ := s.history(s.root)
	if !contains(history, at) {
		return nil, NewFault("unknown_cursor", "cursor is not in accepted history", "store")
	}
	commit := s.commits[at]
	entries := make(map[string]string, len(commit.entries))
	for k, v := range commit.entries {
		entries[k] = v
	}
	value, err := Decode(s.objects[commit.entries["state.json"]][len(Media("state"))+1:])
	if err != nil {
		return nil, err
	}
	return newView(s, at, value.(Object), entries), nil
}

func (s *MemoryStore) Transact(expected, key, payload any, apply func(*View) (any, error)) (Object, error) {
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
	if s.BeforePublish != nil {
		if err := s.BeforePublish(); err != nil {
			return nil, err
		}
	}
	state := Envelope(Media("state"), Encode(view.State))
	entries := make(map[string]string, len(view.Entries)+len(view.pending)+1)
	for name, handle := range view.Entries {
		entries[name] = handle
	}
	entries["state.json"] = ContentRef(state)
	for ref := range view.pending {
		entries["o-"+ref[7:]] = ref
	}
	names := make([]string, 0, len(entries))
	for name := range entries {
		names = append(names, name)
	}
	sort.Strings(names)
	h := sha1.New()
	h.Write([]byte(view.Cursor + "\n" + k + "\n"))
	for _, name := range names {
		h.Write([]byte(name + "\x00" + entries[name] + "\n"))
	}
	id := hex.EncodeToString(h.Sum(nil))

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.root != view.Cursor {
		return nil, NewFault("stale_revision", "root CAS lost; no state/outbox was published", "")
	}
	s.objects[entries["state.json"]] = state
	for ref, body := range view.pending {
		s.objects[ref] = body
	}
	s.commits[id] = memoryCommit{parent: view.Cursor, entries: entries}
	s.root = id
	return Object{"cursor": id, "result": result, "reused": false}, nil
}
