package lifecycle

// A Go harness for the ingest vectors in src/cel-lifecycle/conformance/ingest,
// and the negative ingest tests. Each vector runs on a fresh Git store and a
// fresh in-memory store. Ingest does not run a rule, so these tests need no
// CEL runtime.

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// seedStore writes the seed records of a vector directly to the store. The
// harness checks each content reference against the vector.
func seedStore(store Store, seed Array) error {
	_, err := store.Transact("", "seed", Object{"op": "seed"}, func(v *View) (any, error) {
		for _, raw := range seed {
			s := raw.(Object)
			if got := v.Put(s["put"].(string), copyObject(s["value"].(Object))); got != s["expect_ref"] {
				return nil, fmt.Errorf("seed reference %s, want %s", got, s["expect_ref"])
			}
		}
		return nil, nil
	})
	return err
}

func runIngestVector(t *testing.T, store Store, vector Object) {
	if err := seedStore(store, vector["seed"].(Array)); err != nil {
		t.Fatal(err)
	}
	host, err := NewHost(store, nil, vector["context"])
	if err != nil {
		t.Fatal(err)
	}
	for i, raw := range vector["steps"].(Array) {
		step := raw.(Object)
		where := fmt.Sprintf("steps[%d] %v", i, step["name"])
		q := copyObject(step["request"].(Object))
		// A string in entries names an entry of the vector.
		if list, ok := q["entries"].(Array); ok {
			entries := Array{}
			for _, x := range list {
				if name, isName := x.(string); isName {
					x = vector["entries"].(Object)[name]
				}
				entries = append(entries, x)
			}
			q["entries"] = entries
		}
		if !Reads[q["op"].(string)] {
			view, _ := store.Read(nil)
			q["expected_revision"] = view.Cursor
		}
		out, err := host.Handle(q)
		expect := step["expect"].(Object)
		var f *Fault
		switch {
		case expect["error"] != nil:
			if !errors.As(err, &f) || f.Code != expect["error"] {
				t.Errorf("%s: got %v %v, want %v", where, Encode(out), err, expect["error"])
			}
		case err != nil:
			t.Errorf("%s: %v", where, err)
		case Reads[q["op"].(string)]:
			if !equal(out, expect["result"]) {
				t.Errorf("%s: result %s, want %s", where, Encode(out), Encode(expect["result"]))
			}
		default:
			got := out.(Object)
			if !equal(got["result"], expect["result"]) || !equal(got["reused"], expect["reused"]) {
				t.Errorf("%s: result %s, want %s", where, Encode(got), Encode(expect))
			}
		}
		view, err := store.Read(nil)
		if err != nil {
			t.Fatal(err)
		}
		if n, has := step["objects"]; has {
			count := 0
			for name := range view.Entries {
				if strings.HasPrefix(name, "o-") {
					count++
				}
			}
			if !equal(Int{fmt.Sprint(count)}, n) {
				t.Errorf("%s: %d stored objects, want %v", where, count, n)
			}
		}
		if logs, has := step["executors"]; has && !equal(lookup(view.State, "executors", Object{}), logs) {
			t.Errorf("%s: executors %s, want %s", where, Encode(view.State["executors"]), Encode(logs))
		}
		if stored, ok := step["stored"].(Object); ok {
			for ref, want := range stored {
				_, err := view.GetRaw(ref, "")
				if (err == nil) != want {
					t.Errorf("%s: object %s stored is %v, want %v", where, ref, err == nil, want)
				}
			}
		}
	}
}

func loadIngestVectors(t *testing.T) []Object {
	t.Helper()
	paths, _ := filepath.Glob(filepath.Join("..", "cel-lifecycle", "conformance", "ingest", "*.json"))
	if len(paths) == 0 {
		t.Fatal("no ingest vectors found")
	}
	var vectors []Object
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		value, err := Decode(raw)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		vector := value.(Object)
		if vector["format"] != "ingest-vector/v1" {
			t.Fatalf("%s: unknown vector format", path)
		}
		vectors = append(vectors, vector)
	}
	return vectors
}

func TestIngestVectors(t *testing.T) {
	for _, vector := range loadIngestVectors(t) {
		id := vector["id"].(string)
		t.Run("git/"+id, func(t *testing.T) {
			store, err := OpenGitStore(filepath.Join(t.TempDir(), id+".git"))
			if err != nil {
				t.Fatal(err)
			}
			runIngestVector(t, store, vector)
		})
		t.Run("memory/"+id, func(t *testing.T) { runIngestVector(t, NewMemoryStore(), vector) })
	}
}

// ingestHost gives a Host on a memory store with the seed of the
// repeat-ingest vector, and the entry e1 of that vector.
func ingestHost(t *testing.T, change func(Object)) (*Host, *MemoryStore, Object) {
	t.Helper()
	var vector Object
	for _, x := range loadIngestVectors(t) {
		if x["id"] == "repeat-ingest" {
			vector = x
		}
	}
	store := NewMemoryStore()
	if err := seedStore(store, vector["seed"].(Array)); err != nil {
		t.Fatal(err)
	}
	context := copyObject(vector["context"].(Object))
	if change != nil {
		change(context)
	}
	host, err := NewHost(store, nil, context)
	if err != nil {
		t.Fatal(err)
	}
	return host, store, copyObject(vector["entries"].(Object)["e1"].(Object))
}

func ingestRequest(store Store, key string, entries ...any) Object {
	view, _ := store.Read(nil)
	return Object{"op": "ingest", "expected_revision": view.Cursor, "request_key": key, "entries": Array(entries)}
}

func codeOf(err error) string {
	var f *Fault
	if errors.As(err, &f) {
		return f.Code
	}
	return fmt.Sprint(err)
}

// The Host takes the executor ID from its context. Two hosts with different
// workers keep two logs, and the same cursor in each log is not a conflict.
// A host without the worker role cannot ingest.
func TestIngestExecutorIDComesFromTheHostContext(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	if _, err := host.Handle(ingestRequest(store, "a-1", entry)); err != nil {
		t.Fatal(err)
	}
	other, err := NewHost(store, nil, merge(host.Context, Object{"actor": "executor-b", "worker": "executor-b"}))
	if err != nil {
		t.Fatal(err)
	}
	out, err := other.Handle(ingestRequest(store, "b-1", entry))
	if err != nil {
		t.Fatal(err)
	}
	result := out.(Object)["result"].(Object)
	receipt, err := other.Handle(Object{"op": "receipt", "receipt_ref": result["receipts"].(Array)[0]})
	if err != nil || result["added"] != (Int{"1"}) || receipt.(Object)["executor_id"] != "executor-b" {
		t.Fatalf("got %v %v %v", result, receipt, err)
	}
	view, _ := store.Read(nil)
	if logs := view.State["executors"].(Object); len(logs) != 2 || equal(logs["executor-a"], logs["executor-b"]) {
		t.Fatalf("executors %v", logs)
	}
	caller, err := NewHost(store, nil, merge(host.Context, Object{"roles": Array{"caller", "reader"}}))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := caller.Handle(ingestRequest(store, "c-1", entry)); codeOf(err) != "forbidden" {
		t.Fatalf("got %v, want forbidden", err)
	}
}

// Raw inputs stay with the executor by default: the request fails, and the
// failed request leaves no trace of the inputs. A malformed policy value
// is a configuration error.
func TestIngestKeepsRawInputsWithTheExecutorByDefault(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	inputs := Object{"state": "approved", "archived": false}
	if InputDigest(inputs) != entry["inputs_ref"] {
		t.Fatal("the vector inputs_ref is not the digest of the inputs")
	}
	before, _ := store.Read(nil)
	if _, err := host.Handle(ingestRequest(store, "k-1", merge(entry, Object{"inputs": inputs}))); codeOf(err) != "upload_not_permitted" {
		t.Fatalf("got %v", err)
	}
	after, _ := store.Read(nil)
	if after.Cursor != before.Cursor {
		t.Fatal("a refused upload published state")
	}
	if _, err := host.Handle(ingestRequest(store, "k-2", entry)); err != nil {
		t.Fatal(err)
	}
	view, _ := store.Read(nil)
	if _, err := view.GetRaw(entry["inputs_ref"], ""); codeOf(err) != "artifact_unavailable" {
		t.Fatalf("the raw inputs are in the store: %v", err)
	}
	if _, err := NewHost(store, nil, merge(host.Context, Object{"upload_inputs": "yes"})); codeOf(err) != "configuration_error" {
		t.Fatalf("got %v", err)
	}
}

// Malformed entries are invalid requests. A pinned snapshot that is not in
// the store, or a response for another bundle, also stops the ingest.
func TestIngestRefusesMalformedEntries(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	response := entry["response"].(Object)
	for name, c := range map[string]struct {
		entries Array
		code    string
	}{
		"no-entries":       {Array{}, "invalid_request"},
		"not-an-object":    {Array{"entry"}, "invalid_request"},
		"string-cursor":    {Array{merge(entry, Object{"cursor": "1"})}, "invalid_request"},
		"no-snapshot":      {Array{merge(entry, Object{"snapshot_ref": nil})}, "invalid_reference"},
		"unknown-snapshot": {Array{merge(entry, Object{"snapshot_ref": "sha256:" + strings.Repeat("0", 64)})}, "artifact_unavailable"},
		"bad-digest":       {Array{merge(entry, Object{"inputs_ref": "approved"})}, "invalid_request"},
		"bad-date":         {Array{merge(entry, Object{"date": "2026-13-01"})}, "invalid_interval"},
		"bad-stale-reason": {Array{merge(entry, Object{"stale_reason": "late"})}, "invalid_request"},
		"other-bundle":     {Array{merge(entry, Object{"response": merge(response, Object{"bundle_ref": "sha256:" + strings.Repeat("c", 64)})})}, "artifact_mismatch"},
	} {
		if _, err := host.Handle(ingestRequest(store, name, c.entries...)); codeOf(err) != c.code {
			t.Errorf("%s: got %v, want %s", name, err, c.code)
		}
	}
	view, _ := store.Read(nil)
	if _, has := view.State["executors"]; has {
		t.Fatal("a refused ingest published an executor log")
	}
}

// An ingested receipt has no Host selection. It is not in state.receipts,
// so replay and restatement refuse it.
func TestIngestedReceiptIsNotReplayed(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	out, err := host.Handle(ingestRequest(store, "k-1", entry))
	if err != nil {
		t.Fatal(err)
	}
	ref := out.(Object)["result"].(Object)["receipts"].(Array)[0]
	view, _ := store.Read(nil)
	if within(ref, view.State["receipts"]) {
		t.Fatal("an ingested receipt is in state.receipts")
	}
	for _, q := range []Object{{"op": "replay", "receipt_ref": ref, "reason": "replay"}, {"op": "restate", "receipt_ref": ref, "cursor": view.Cursor, "reason": "restate"}} {
		q["expected_revision"], q["request_key"] = view.Cursor, q["op"]
		if _, err := host.Handle(q); codeOf(err) != "artifact_unavailable" {
			t.Errorf("%s: got %v", q["op"], err)
		}
	}
}
