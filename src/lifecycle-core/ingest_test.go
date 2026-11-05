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

// seedStore writes seed records directly to the store and gives their
// references. If a seed has expect_ref, the harness checks the reference.
// If a seed has list, the harness appends the reference to that state list.
func seedStore(store Store, key string, seed Array) ([]string, error) {
	var refs []string
	view, _ := store.Read(nil)
	_, err := store.Transact(view.Cursor, key, Object{"op": "seed", "seed": seed}, func(v *View) (any, error) {
		for _, raw := range seed {
			s := raw.(Object)
			got := v.Put(s["put"].(string), copyObject(s["value"].(Object)))
			if want, has := s["expect_ref"]; has && got != want {
				return nil, fmt.Errorf("seed reference %s, want %s", got, want)
			}
			if list, ok := s["list"].(string); ok {
				v.State[list] = append(stringList(v.State[list]), got)
			}
			refs = append(refs, got)
		}
		return nil, nil
	})
	return refs, err
}

func runIngestVector(t *testing.T, store Store, vector Object) {
	if _, err := seedStore(store, "seed", vector["seed"].(Array)); err != nil {
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
		// A string in entries names an entry of the vector. An object with
		// "like" is that named entry with the other fields of the object.
		if list, ok := q["entries"].(Array); ok {
			entries := Array{}
			named := vector["entries"].(Object)
			for _, x := range list {
				if name, isName := x.(string); isName {
					x = named[name]
				} else if changed, isObject := x.(Object); isObject && changed["like"] != nil {
					x = merge(named[changed["like"].(string)].(Object), changed)
					delete(x.(Object), "like")
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
	if _, err := seedStore(store, "seed", vector["seed"].(Array)); err != nil {
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
// the store, a date before each activation of the snapshot, or a response
// for another bundle, also stops the ingest.
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
		"cursor-too-large": {Array{merge(entry, Object{"cursor": Int{"9007199254740992"}})}, "invalid_request"},
		"before-from":      {Array{merge(entry, Object{"date": "2026-10-15"})}, "out_of_scope"},
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

// An ingested receipt has a selection from the pinned snapshot, but no
// lifecycle_build, report or decision. It is not in state.receipts, so
// replay and restatement refuse it.
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

// The Host keeps each answer and records its own stale view beside the
// claim of the executor. A later mask does not make a re-ingest of the same
// log a conflict. A fail_closed snapshot also keeps a stale answer. A
// snapshot for another function is out_of_scope.
func TestIngestKeepsTheAnswerAndMarksTheHostView(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	other := Object{"function": "other.function", "scope": "tenant-example", "from": "2026-11-01", "until": "2026-12-01",
		"bundle_ref": at(entry, "response", "bundle_ref"), "engine_build": "sha256:" + strings.Repeat("e", 64), "stale_mode": "continue_stale"}
	closed := merge(other, Object{"function": "record.visible", "from": "2026-10-01", "until": "2026-11-01", "stale_mode": "fail_closed"})
	refs, err := seedStore(store, "seed-2", Array{Object{"put": "snapshot", "value": other}, Object{"put": "snapshot", "value": closed}})
	if err != nil {
		t.Fatal(err)
	}
	view, _ := store.Read(nil)
	pinned := stringList(view.State["events"])[0]
	suspend := Object{"kind": "suspend", "function": "record.visible", "scope": "tenant-example", "from": "2026-11-20", "until": "2026-12-01",
		"targets": Array{pinned}, "reason": "test"}
	enable := merge(closed, Object{"kind": "enable", "snapshot_ref": refs[1], "targets": Array{}})
	if _, err := seedStore(store, "seed-3", Array{Object{"put": "event", "value": enable, "list": "events"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Handle(ingestRequest(store, "other", merge(entry, Object{"snapshot_ref": refs[0]}))); codeOf(err) != "out_of_scope" {
		t.Fatalf("other-function: got %v", err)
	}
	ingest := func(key string, added string, entries ...any) Array {
		t.Helper()
		out, err := host.Handle(ingestRequest(store, key, entries...))
		if err != nil || out.(Object)["result"].(Object)["added"] != (Int{added}) {
			t.Fatalf("%s: got %v %v", key, out, err)
		}
		return out.(Object)["result"].(Object)["receipts"].(Array)
	}
	claimRevoked := merge(entry, Object{"cursor": Int{"2"}, "date": "2026-11-16", "stale_reason": "revoked"})
	first := ingest("k-1", "2", entry, claimRevoked)
	if _, err := seedStore(store, "suspend", Array{Object{"put": "event", "value": suspend, "list": "events"}}); err != nil {
		t.Fatal(err)
	}
	if again := ingest("k-2", "0", entry, claimRevoked); !equal(again, first) {
		t.Fatalf("re-ingest after a mask gave %v, want %v", again, first)
	}
	later := ingest("k-3", "3",
		merge(entry, Object{"cursor": Int{"3"}, "date": "2026-11-25"}),
		merge(entry, Object{"cursor": Int{"4"}, "snapshot_ref": refs[1], "date": "2026-10-15", "stale_reason": "revoked"}),
		merge(entry, Object{"cursor": Int{"5"}, "snapshot_ref": refs[1]}))
	view, _ = store.Read(nil)
	for i, c := range []struct {
		ref               any
		claim, host, mode string
	}{
		{first[0], "", "", "continue_stale"},
		{first[1], "revoked", "", "continue_stale"},
		{later[0], "", "revoked", "continue_stale"},
		{later[1], "revoked", "", "fail_closed"},
		{later[2], "", "expired", "fail_closed"},
	} {
		r := get(view, c.ref, "receipt")
		if r["stale_reason"] != c.claim || r["host_stale_reason"] != c.host || r["stale_mode"] != c.mode || r["stale"] != (c.claim != "") {
			t.Errorf("receipt %d: %s", i, Encode(r))
		}
	}
}

// If the policy permits upload, a new entry and a duplicate entry both
// store their inputs. GetRaw gives the canonical bytes. Inputs that differ
// from inputs_ref give input_mismatch.
func TestIngestUploadStoresTheInputs(t *testing.T) {
	host, store, entry := ingestHost(t, func(c Object) { c["upload_inputs"] = true })
	first := Object{"state": "approved", "archived": false}
	second := Object{"state": "closed", "archived": false}
	next := merge(entry, Object{"cursor": Int{"2"}, "inputs_ref": InputDigest(second), "inputs": second})
	for i, entries := range []Array{{entry}, {merge(entry, Object{"inputs": first}), next}} {
		if _, err := host.Handle(ingestRequest(store, fmt.Sprint("k-", i), entries...)); err != nil {
			t.Fatal(err)
		}
	}
	view, _ := store.Read(nil)
	for _, inputs := range []Object{first, second} {
		if raw, err := view.GetRaw(InputDigest(inputs), "application/json"); err != nil || string(raw) != string(Encode(inputs)) {
			t.Errorf("stored inputs %q %v", raw, err)
		}
	}
	bad := merge(entry, Object{"cursor": Int{"3"}, "inputs": second})
	if _, err := host.Handle(ingestRequest(store, "k-bad", bad)); codeOf(err) != "input_mismatch" {
		t.Fatalf("got %v, want input_mismatch", err)
	}
}

// A corrupt executors field fails closed. The Host does not replace it.
func TestIngestRefusesCorruptExecutorState(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	view, _ := store.Read(nil)
	if _, err := store.Transact(view.Cursor, "corrupt", Object{"op": "corrupt"}, func(v *View) (any, error) {
		v.State["executors"] = "corrupt"
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := host.Handle(ingestRequest(store, "k-1", entry)); codeOf(err) != "invalid_record" {
		t.Fatalf("got %v, want invalid_record", err)
	}
	defer func() {
		if f, ok := recover().(*Fault); !ok || f.Code != "invalid_record" {
			t.Fatalf("setExecutorLog replaced a corrupt field: %v", f)
		}
	}()
	setExecutorLog(&View{State: Object{"executors": Array{}}}, "executor-a", Array{})
}

// The request record binds the context worker and upload policy. The same
// request key from a Host with another worker, or with another upload
// policy, is idempotency_conflict, and the state does not change.
func TestIngestRequestKeyBindsTheExecutor(t *testing.T) {
	host, store, entry := ingestHost(t, nil)
	q := ingestRequest(store, "shared-key", entry)
	if _, err := host.Handle(copyObject(q)); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Read(nil)
	for _, change := range []Object{{"worker": "executor-b"}, {"upload_inputs": true}} {
		other, err := NewHost(store, nil, merge(host.Context, change))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := other.Handle(copyObject(q)); codeOf(err) != "idempotency_conflict" {
			t.Fatalf("%v: got %v, want idempotency_conflict", change, err)
		}
	}
	after, _ := store.Read(nil)
	if after.Cursor != before.Cursor || len(after.State["executors"].(Object)) != 1 {
		t.Fatalf("a conflict changed the state: %s", Encode(after.State["executors"]))
	}
}

// The executor vectors state inputs_ref for each entry. The executor
// computes it with its own codec. This test computes it with the Host codec
// from the same input text, so the two codecs agree on each vector.
func TestExecutorVectorDigestsAgreeWithTheHostCodec(t *testing.T) {
	paths, _ := filepath.Glob(filepath.Join("..", "cel-lifecycle", "conformance", "executor", "*.json"))
	if len(paths) == 0 {
		t.Fatal("no executor vectors")
	}
	checked := 0
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		vector := must(Decode(raw)).(Object)
		for _, s := range vector["steps"].(Array) {
			step := s.(Object)
			entry, ok := step["expect"].(Object)["entry"].(Object)
			if !ok {
				continue
			}
			inputs, err := Decode([]byte(step["inputs_json"].(string)))
			if err != nil {
				t.Fatalf("%s/%s: %v", vector["id"], step["name"], err)
			}
			if got := InputDigest(inputs); got != entry["inputs_ref"] {
				t.Errorf("%s/%s: Host digest %s, vector %s", vector["id"], step["name"], got, entry["inputs_ref"])
			}
			checked++
		}
	}
	if checked < 10 {
		t.Fatalf("checked only %d entries", checked)
	}
}
