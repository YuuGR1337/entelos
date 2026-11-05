package protocol

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	lifecycle "example.org/lifecycle-core"
)

// stubHost gives a fixed result, a fixed error or a panic.
type stubHost struct {
	result any
	err    error
	panics bool
}

func (h stubHost) Handle(any) (any, error) {
	if h.panics {
		panic("/private/store/secret.git: broken")
	}
	return h.result, h.err
}

var inspect = []byte(`{"body":{},"operation":"inspect","protocol":"1.0","request_id":"stub"}`)

// serveOne sends one frame through Serve and gives each response frame.
func serveOne(t *testing.T, host Handler) ([]any, [][]byte) {
	t.Helper()
	var out bytes.Buffer
	if err := NewAdapter(host, loadTestSchemas(t)).Serve(bytes.NewReader(frameOf(inspect)), &out); err != nil {
		t.Fatal(err)
	}
	got, payloads, err := frames(out.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	return got, payloads
}

func wantFailure(t *testing.T, got []any, code, stage string) {
	t.Helper()
	if len(got) != 1 {
		t.Fatalf("got %d frames, want 1", len(got))
	}
	r := got[0].(lifecycle.Object)
	d, _ := r["diagnostic"].(lifecycle.Object)
	if r["protocol"] != "1.0" || r["request_id"] != "stub" || r["status"] != code || d["code"] != code || d["stage"] != stage || len(r) != 4 {
		t.Fatalf("got %s, want %s at stage %s", lifecycle.Encode(r), code, stage)
	}
	if strings.Contains(string(lifecycle.Encode(r)), "secret") {
		t.Fatalf("the response shows the error text: %s", lifecycle.Encode(r))
	}
}

// An unexpected Host failure, or a result outside the value domain, gives one
// internal_error frame with no error text.
func TestInternalErrors(t *testing.T) {
	cases := map[string]stubHost{
		"panic":       {panics: true},
		"plain-error": {err: errors.New("/private/store/secret.git: broken")},
		"float":       {result: lifecycle.Object{"cursor": "", "state": lifecycle.Object{"ratio": 0.5}}},
		"unsafe-int":  {result: lifecycle.Object{"n": lifecycle.Int{Text: "9007199254740992"}}},
		"go-type":     {result: lifecycle.Object{"n": struct{}{}}},
		"invalid-key": {result: lifecycle.Object{"\xff": "x"}},
	}
	for name, host := range cases {
		t.Run(name, func(t *testing.T) {
			got, _ := serveOne(t, host)
			wantFailure(t, got, "internal_error", "host")
		})
	}
}

// A Fault from the Host keeps its code and stage.
func TestFaultMapping(t *testing.T) {
	got, _ := serveOne(t, stubHost{err: lifecycle.NewFault("stale_revision", "m", "")})
	wantFailure(t, got, "stale_revision", "lifecycle")
}

// A success result over the frame bound gives response_too_large, and the
// frame stays in the bound.
func TestResponseTooLarge(t *testing.T) {
	big := stubHost{result: lifecycle.Object{"cursor": "", "state": lifecycle.Object{"blob": strings.Repeat("x", MaxFrame)}}}
	got, payloads := serveOne(t, big)
	wantFailure(t, got, "response_too_large", "protocol")
	if len(payloads[0]) > MaxFrame {
		t.Fatal("the frame exceeds the bound")
	}
	// A result of exactly the bound passes.
	empty := `{"protocol":"1.0","request_id":"stub","result":{"cursor":"","state":{"blob":""}},"status":"ok"}`
	fits := stubHost{result: lifecycle.Object{"cursor": "", "state": lifecycle.Object{"blob": strings.Repeat("x", MaxFrame-len(empty))}}}
	got, payloads = serveOne(t, fits)
	if len(payloads[0]) != MaxFrame || got[0].(lifecycle.Object)["status"] != "ok" {
		t.Fatalf("got %d bytes, status %v; want %d bytes and ok", len(payloads[0]), got[0].(lifecycle.Object)["status"], MaxFrame)
	}
}

// A message has a maximum of 1024 code points and always encodes as UTF-8.
func TestFailureMessage(t *testing.T) {
	long := failure("r", "c", "s", strings.Repeat("\U0001F600", 2000))["diagnostic"].(lifecycle.Object)["message"].(string)
	if utf8.RuneCountInString(long) != 1024 || !strings.HasSuffix(long, "...") || !strings.HasPrefix(long, "\U0001F600") {
		t.Fatalf("got %d code points", utf8.RuneCountInString(long))
	}
	exact := strings.Repeat("\U0001F600", 1024)
	if failure("r", "c", "s", exact)["diagnostic"].(lifecycle.Object)["message"] != exact {
		t.Fatal("a message of 1024 code points changed")
	}
	bad := failure("r", "c", "s", "a\xffb")["diagnostic"].(lifecycle.Object)["message"]
	if bad != `a\xffb` {
		t.Fatalf("got %q, want a\\xffb", bad)
	}
}

// The canonical form refuses each value outside the protocol value domain.
func TestCanonicalDomain(t *testing.T) {
	for name, value := range map[string]any{
		"float": 1.5, "too-large": lifecycle.Int{Text: "9007199254740992"}, "too-small": int64(-1 << 53),
		"invalid-utf8": "\xff", "go-type": struct{}{},
	} {
		if _, err := Canonical(value); err == nil {
			t.Errorf("%s: no error", name)
		}
	}
	got, err := Canonical(lifecycle.Object{"b": []string{"x"}, "a": int64(-(1<<53 - 1))})
	if err != nil || string(got) != `{"a":-9007199254740991,"b":["x"]}` {
		t.Fatalf("got %s, %v", got, err)
	}
}

func copySchemas(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	paths, _ := filepath.Glob(filepath.Join(protocolDir, "schema", "*.schema.json"))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(path)), raw, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// The sidecar refuses a schema directory that does not agree with the Host.
func TestLoadSchemasRefusesMismatch(t *testing.T) {
	dir := copySchemas(t)
	path := filepath.Join(dir, "inspect.schema.json")
	raw, _ := os.ReadFile(path)
	changed := bytes.Replace(raw, []byte(`"x-entelos-access": "read"`), []byte(`"x-entelos-access": "write"`), 1)
	if bytes.Equal(raw, changed) {
		t.Fatal("inspect.schema.json has no read access field")
	}
	os.WriteFile(path, changed, 0o600)
	if _, err := LoadSchemas(dir); err == nil {
		t.Fatal("a read operation with write access loaded")
	}
	dir = copySchemas(t)
	os.Remove(filepath.Join(dir, "envelope.schema.json"))
	if _, err := LoadSchemas(dir); err == nil {
		t.Fatal("a directory without the envelope loaded")
	}
	if _, err := LoadSchemas(t.TempDir()); err == nil {
		t.Fatal("an empty directory loaded")
	}
}

// recordHost keeps the operation of each Host request.
type recordHost struct{ ops *[]any }

func (h recordHost) Handle(q any) (any, error) {
	*h.ops = append(*h.ops, q.(lifecycle.Object)["op"])
	return lifecycle.Object{"cursor": "", "state": lifecycle.Object{}}, nil
}

// A body key "op" does not change the operation that the Host gets. The
// schemas reject the key now; this test also covers a schema that allows it.
func TestBodyCannotReplaceOperation(t *testing.T) {
	var ops []any
	a := NewAdapter(recordHost{&ops}, loadTestSchemas(t))
	a.Message(lifecycle.Object{"body": lifecycle.Object{"op": "activate"}, "operation": "inspect", "protocol": "1.0", "request_id": "stub"})
	for _, op := range ops {
		if op != "inspect" {
			t.Fatalf("the Host got operation %v, want inspect", op)
		}
	}
}
