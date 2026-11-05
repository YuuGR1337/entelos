package lifecycle

// Host regressions ported from the removed Python Host tests. Each test
// starts from a copy of the Git repository that the lifecycle scenario in
// scenario_test.go made. The pins name the records of that scenario.

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

type baseline struct {
	repo, contexts, binary string
	pins                   Object
}

func loadBaseline(t *testing.T) baseline {
	t.Helper()
	run := requireScenario(t)
	return baseline{repo: run.driver.repo, contexts: run.driver.work, binary: run.binary, pins: run.pins}
}

type fixture struct {
	t     *testing.T
	b     baseline
	dir   string
	store *GitStore
}

func newFixture(t *testing.T) *fixture {
	b := loadBaseline(t)
	dir := t.TempDir()
	repo := filepath.Join(dir, "state.git")
	if err := os.CopyFS(repo, os.DirFS(b.repo)); err != nil {
		t.Fatal(err)
	}
	store, err := OpenGitStore(repo)
	if err != nil {
		t.Fatal(err)
	}
	return &fixture{t: t, b: b, dir: dir, store: store}
}

func (f *fixture) pin(name string) string { return f.b.pins[name].(string) }

func (f *fixture) context(actor string) Object {
	raw, err := os.ReadFile(filepath.Join(f.b.contexts, actor+".json"))
	if err != nil {
		f.t.Fatal(err)
	}
	value, err := Decode(raw)
	if err != nil {
		f.t.Fatal(err)
	}
	return value.(Object)
}

func (f *fixture) runtime(binary string) Runtime {
	rt, err := NewCELRuntime(binary, filepath.Join(f.dir, "tmp"))
	if err != nil {
		f.t.Fatal(err)
	}
	return rt
}

func (f *fixture) host(actor string, change func(Object)) *Host {
	context := f.context(actor)
	if change != nil {
		change(context)
	}
	h, err := NewHost(f.store, f.runtime(f.b.binary), context)
	if err != nil {
		f.t.Fatal(err)
	}
	return h
}

func (f *fixture) root() string {
	r, err := f.store.Root()
	if err != nil {
		f.t.Fatal(err)
	}
	return r
}

func (f *fixture) request(op string, fields Object) Object {
	q := Object{"op": op, "expected_revision": f.root(), "request_key": f.t.Name()}
	for k, v := range fields {
		q[k] = v
	}
	return q
}

// expect gives a check that the result is a fault with the code.
func (f *fixture) expect(want string) func(any, error) {
	return func(result any, err error) {
		f.t.Helper()
		if fault, ok := err.(*Fault); !ok || fault.Code != want {
			f.t.Fatalf("got %v %v, want %s", result, err, want)
		}
	}
}

func (f *fixture) ok(result any, err error) Object {
	f.t.Helper()
	if err != nil {
		f.t.Fatal(err)
	}
	return result.(Object)
}

func resolveRequest(extra Object) Object {
	q := Object{"op": "resolve", "function": "record.visible", "date": "2026-11-15"}
	for k, v := range extra {
		q[k] = v
	}
	return q
}

// barrierStore holds each prepared write until both writers have prepared one.
type barrierStore struct {
	*GitStore
	wait func()
}

func (s barrierStore) Transact(expected, key, payload any, apply func(*View) (any, error)) (Object, error) {
	return s.GitStore.Transact(expected, key, payload, func(v *View) (any, error) {
		result, err := apply(v)
		s.wait()
		return result, err
	})
}

func TestTwoActivationWritersPublishOnlyOneRoot(t *testing.T) {
	f := newFixture(t)
	before, _ := f.store.Read(nil)
	var ready, done sync.WaitGroup
	ready.Add(2)
	done.Add(2)
	errs := make([]error, 2)
	for i := range errs {
		store, err := OpenGitStore(f.store.Path)
		if err != nil {
			t.Fatal(err)
		}
		h, err := NewHost(barrierStore{store, func() { ready.Done(); ready.Wait() }}, f.runtime(f.b.binary), f.context("reviewer"))
		if err != nil {
			t.Fatal(err)
		}
		go func(i int) {
			defer done.Done()
			_, errs[i] = h.Handle(Object{"op": "activate", "decision_ref": f.pin("decision"), "targets": Array{f.pin("correction_event")},
				"expected_revision": before.Cursor, "request_key": "racer-" + string(rune('0'+i))})
		}(i)
	}
	done.Wait()
	codes := []string{code(errs[0]), code(errs[1])}
	if !(codes[0] == "" && codes[1] == "stale_revision" || codes[0] == "stale_revision" && codes[1] == "") {
		t.Fatalf("got %v", errs)
	}
	for _, err := range errs {
		if err != nil && !strings.Contains(err.(*Fault).Message, "root CAS lost") {
			t.Fatal(err)
		}
	}
	after, _ := f.store.Read(nil)
	if len(after.State["events"].(Array)) != len(before.State["events"].(Array))+1 ||
		len(after.State["outbox"].(Object)) != len(before.State["outbox"].(Object))+1 {
		t.Fatal("not exactly one accepted activation")
	}
	requests := after.State["requests"].(Object)
	_, first := requests["racer-0"]
	_, second := requests["racer-1"]
	if first == second {
		t.Fatal("not exactly one accepted request key")
	}
	old, _ := f.store.Read(before.Cursor)
	if !equal(old.State, before.State) {
		t.Fatal("the old root changed")
	}
}

func TestReplayRefusesAMissingRuntimeBuild(t *testing.T) {
	f := newFixture(t)
	before := f.root()
	context := f.context("reader")
	h, err := NewHost(f.store, f.runtime(filepath.Join(f.dir, "absent-binary")), context)
	if err != nil {
		t.Fatal(err)
	}
	f.expect("implementation_unavailable")(h.Handle(f.request("replay", Object{"receipt_ref": f.pin("old_receipt"), "reason": "missing implementation test"})))
	if f.root() != before {
		t.Fatal("state changed")
	}
}

func TestMissingRetainedBlobIsNotReplacedByLatest(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	receipt, err := view.Get(f.pin("old_receipt"), "receipt")
	if err != nil {
		t.Fatal(err)
	}
	input := receipt["inputs_ref"].(string)
	// Deliberate trusted-store corruption in an isolated copy.
	if _, err := f.store.Transact(view.Cursor, "simulate-retention-loss", Object{"op": "test-corruption"}, func(v *View) (any, error) {
		delete(v.Entries, "o-"+input[7:])
		return Object{"removed": input}, nil
	}); err != nil {
		t.Fatal(err)
	}
	f.expect("artifact_unavailable")(f.host("reader", nil).Handle(f.request("replay", Object{"receipt_ref": f.pin("old_receipt"), "reason": "missing input test"})))
	raw, err := view.GetRaw(input, "application/json")
	if err != nil || string(raw) != string(Encode(Object{"archived": false, "state": "reviewed"})) {
		t.Fatalf("%q %v", raw, err)
	}
}

func TestImportedAmbiguousRegistryFailsClosed(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	event, err := view.Get(f.pin("correction_event"), "event")
	if err != nil {
		t.Fatal(err)
	}
	event = copyObject(event)
	event["targets"], event["event_id"] = Array{}, "deliberately-invalid-import"
	if _, err := f.store.Transact(view.Cursor, "invalid-import", Object{"op": "test-corruption"}, func(v *View) (any, error) {
		ref := v.Put("event", event)
		v.State["events"] = append(v.State["events"].(Array), ref)
		return Object{"event_ref": ref}, nil
	}); err != nil {
		t.Fatal(err)
	}
	reader := f.host("reader", nil)
	f.expect("ambiguous_activation")(reader.Handle(resolveRequest(nil)))
	old := f.ok(reader.Handle(resolveRequest(Object{"cursor": view.Cursor})))
	if old["event_ref"] != f.pin("correction_event") {
		t.Fatal(old)
	}
}

func TestScopeRolesAndReportFieldsAreHostOwned(t *testing.T) {
	f := newFixture(t)
	f.expect("out_of_scope")(f.host("reader", func(c Object) { c["scope"] = "other" }).Handle(resolveRequest(nil)))
	f.expect("invalid_request")(f.host("caller", nil).Handle(Object{"op": "describe", "function": "record.visible", "date": "2026-11-15", "scope": "demo"}))
	f.expect("invalid_request")(f.host("runner", nil).Handle(f.request("qualify", Object{"snapshot_ref": f.pin("correction_snapshot"), "plan_ref": "forged", "status": "passed"})))
	f.expect("forbidden")(f.host("author", nil).Handle(f.request("activate", Object{"decision_ref": f.pin("decision"), "targets": Array{f.pin("correction_event")}})))
	f.expect("unknown_cursor")(f.host("reader", nil).Handle(resolveRequest(Object{"cursor": strings.Repeat("f", 40)})))
}

func TestRestatementRequiresLaterCursorAndReplayExactBytes(t *testing.T) {
	f := newFixture(t)
	reader := f.host("reader", nil)
	old := f.ok(reader.Handle(Object{"op": "receipt", "receipt_ref": f.pin("old_receipt")}))
	cursor := old["selection"].(Object)["registry_cursor"]
	f.expect("invalid_cursor")(reader.Handle(f.request("restate", Object{"receipt_ref": f.pin("old_receipt"), "cursor": cursor, "reason": "not a later root"})))
	// The same values with different bytes are also a resupplied-input mismatch.
	f.expect("input_mismatch")(reader.Handle(f.request("replay", Object{"receipt_ref": f.pin("old_receipt"),
		"inputs_json": `{"state":"reviewed", "archived":false}`, "reason": "byte identity test"})))
	f.expect("invalid_cursor")(reader.Handle(f.request("restate", Object{"receipt_ref": f.pin("old_receipt"), "cursor": nil, "reason": "must be explicit"})))
}

func TestIntervalErrorsPublishNoState(t *testing.T) {
	f := newFixture(t)
	before := f.root()
	f.expect("invalid_interval")(f.host("reviewer", nil).Handle(f.request("source", Object{"function": "record.visible",
		"from": "2026-13-01", "until": "2027-01-01", "text": "bad", "reason": "bad", "supersedes": Array{}})))
	if f.root() != before {
		t.Fatal("state changed")
	}
}

func TestHostContextRejectsARoleString(t *testing.T) {
	f := newFixture(t)
	context := f.context("reviewer")
	context["roles"] = "reviewer"
	if _, err := NewHost(f.store, f.runtime(f.b.binary), context); code(err) != "configuration_error" {
		t.Fatal(err)
	}
}

func TestMalformedBundleIsAnExplicitError(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	snapshot, err := view.Get(f.pin("correction_snapshot"), "snapshot")
	if err != nil {
		t.Fatal(err)
	}
	f.expect("invalid_bundle")(f.host("author", nil).Handle(f.request("propose", Object{"function": "record.visible",
		"from": snapshot["from"], "until": snapshot["until"], "base_snapshot": f.pin("correction_snapshot"),
		"source_ref": snapshot["source_ref"], "bundle": Array{"manifest", "environment", "source", "fixed"},
		"reason": "malformed author request"})))
}

func TestAnswersRequireTheQualifiedLifecycleBuild(t *testing.T) {
	f := newFixture(t)
	other := "sha256:" + strings.Repeat("0", 64)
	worker := f.host("worker", nil)
	worker.BuildRef = other
	f.expect("implementation_unavailable")(worker.Handle(f.request("load", Object{"function": "record.visible", "date": "2026-11-15"})))
	caller := f.host("caller", nil)
	caller.BuildRef = other
	selected := f.ok(f.host("reader", nil).Handle(resolveRequest(nil)))
	before := f.root()
	f.expect("implementation_unavailable")(caller.Handle(f.request("invoke", Object{"function": "record.visible", "date": "2026-11-15",
		"expected_bundle_ref": selected["bundle_ref"], "inputs": Object{"state": "ready", "archived": false}})))
	if f.root() != before {
		t.Fatal("state changed")
	}
}

func TestReviewerMustDifferFromProducerAndRunner(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	decision, err := view.Get(f.pin("decision"), "decision")
	if err != nil {
		t.Fatal(err)
	}
	for _, actor := range []string{"author", "fixture-runner"} {
		h := f.host("reviewer", func(c Object) { c["actor"] = actor })
		f.expect("separation_required")(h.Handle(f.request("decide", Object{"report_ref": decision["report_ref"], "reason": "self approval"})))
	}
}

func TestReplayRecordsWhetherTheAnswerMatches(t *testing.T) {
	f := newFixture(t)
	reader := f.host("reader", nil)
	old := f.ok(reader.Handle(Object{"op": "receipt", "receipt_ref": f.pin("old_receipt")}))
	value := f.ok(reader.Handle(f.request("replay", Object{"receipt_ref": f.pin("old_receipt"), "reason": "match test"})))
	receipt := value["result"].(Object)["receipt"].(Object)
	if receipt["matches_original"] != true || receipt["response_ref"] != old["response_ref"] {
		t.Fatal(receipt)
	}
}

func TestWrongJSONTypesAreFaults(t *testing.T) {
	f := newFixture(t)
	before := f.root()
	for _, raw := range []any{Int{"5"}, nil, Object{}} {
		f.expect("invalid_request")(f.host("reader", nil).Handle(f.request("replay", Object{"receipt_ref": f.pin("old_receipt"), "inputs_json": raw, "reason": "type test"})))
	}
	if f.root() != before {
		t.Fatal("state changed")
	}
}

func TestGitObjectIntegrityAndRestart(t *testing.T) {
	f := newFixture(t)
	if _, err := f.store.Git(nil, "fsck", "--full", "--no-dangling"); err != nil {
		t.Fatal(err)
	}
	fresh, err := OpenGitStore(f.store.Path)
	if err != nil {
		t.Fatal(err)
	}
	f.store = fresh
	reader := f.host("reader", nil)
	if old := f.ok(reader.Handle(Object{"op": "receipt", "receipt_ref": f.pin("old_receipt")})); old["mode"] != "invoke" {
		t.Fatal(old)
	}
	if selected := f.ok(reader.Handle(resolveRequest(nil))); selected["snapshot_ref"] != f.pin("correction_snapshot") {
		t.Fatal(selected)
	}
}

// The same Host runs on the in-memory adapter: the baseline state and objects
// are copied into a MemoryStore, and reads and a replay give the Git answers.
func TestHostOnTheMemoryStoreGivesTheGitAnswers(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	memory := NewMemoryStore()
	if _, err := memory.Transact("", "import", Object{"op": "import"}, func(v *View) (any, error) {
		for name, oid := range view.Entries {
			if strings.HasPrefix(name, "o-") {
				body, err := f.store.object(oid)
				if err != nil {
					return nil, err
				}
				v.pending["sha256:"+name[2:]] = body
			}
		}
		state, err := Decode(Encode(view.State))
		if err != nil {
			return nil, err
		}
		v.State = state.(Object)
		return nil, nil
	}); err != nil {
		t.Fatal(err)
	}
	gitReader := f.host("reader", nil)
	memoryReader, err := NewHost(memory, f.runtime(f.b.binary), f.context("reader"))
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range []Object{resolveRequest(nil), {"op": "receipt", "receipt_ref": f.pin("old_receipt")}} {
		want := f.ok(gitReader.Handle(q))
		got := f.ok(memoryReader.Handle(q))
		delete(want, "registry_cursor")
		delete(got, "registry_cursor")
		if q["op"] == "resolve" && !equal(want, got) {
			t.Fatalf("resolve: %v != %v", got, want)
		}
		if q["op"] == "receipt" && got["response_ref"] != want["response_ref"] {
			t.Fatalf("receipt: %v != %v", got, want)
		}
	}
	root, _ := memory.Read(nil)
	replay := f.ok(memoryReader.Handle(Object{"op": "replay", "receipt_ref": f.pin("old_receipt"), "reason": "memory replay",
		"expected_revision": root.Cursor, "request_key": "memory-replay"}))
	if replay["result"].(Object)["receipt"].(Object)["matches_original"] != true {
		t.Fatal(replay)
	}
	// The memory writes must not reach the Git view that gave the import.
	fresh, _ := f.store.Read(nil)
	if !equal(view.State, fresh.State) {
		t.Fatal("the memory store shares state with the Git view")
	}
}

// A record of an unknown major version, a record without a format and a
// record with a malformed format each stop the Host read with a structured
// diagnostic. The failed replay publishes no state.
func TestUnknownRecordFormatFailsClosed(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	receipt, err := view.Get(f.pin("old_receipt"), "receipt")
	if err != nil {
		t.Fatal(err)
	}
	if receipt["format"] != RecordFormat {
		t.Fatalf("stored receipt format %v", receipt["format"])
	}
	cases := []struct {
		name   string
		format any
		want   Object
	}{
		{"unknown-major", "entelos.record/2", Object{"code": "unsupported_record_format", "stage": "store"}},
		{"missing", nil, Object{"code": "unsupported_record_format", "stage": "store"}},
		{"malformed", "entelos.record/one", Object{"code": "invalid_record", "stage": "store"}},
		{"not-a-string", Int{"1"}, Object{"code": "invalid_record", "stage": "store"}},
	}
	for _, c := range cases {
		planted := copyObject(receipt)
		delete(planted, "format")
		if c.format != nil {
			planted["format"] = c.format
		}
		var ref string
		if _, err := f.store.Transact(f.root(), "plant-"+c.name, Object{"op": "test-import", "case": c.name}, func(v *View) (any, error) {
			// PutRaw keeps the exact bytes. Put would set the current format.
			ref = v.PutRaw(Media("receipt"), Encode(planted))
			v.State["receipts"] = append(v.State["receipts"].(Array), ref)
			return Object{"receipt_ref": ref}, nil
		}); err != nil {
			t.Fatal(err)
		}
		before := f.root()
		// The reviewer context also has the reader role.
		reader := f.host("reviewer", nil)
		for _, q := range []Object{
			{"op": "receipt", "receipt_ref": ref},
			{"op": "record", "ref": ref, "kind": "receipt"},
			f.request("replay", Object{"receipt_ref": ref, "reason": "format test " + c.name}),
		} {
			result, err := reader.Handle(q)
			fault, ok := err.(*Fault)
			if !ok {
				t.Fatalf("%s %v: got %v %v, want a fault", c.name, q["op"], result, err)
			}
			got := fault.Record()["diagnostic"].(Object)
			delete(got, "message")
			if !equal(got, c.want) {
				t.Fatalf("%s %v: diagnostic %v, want %v", c.name, q["op"], got, c.want)
			}
		}
		if f.root() != before {
			t.Fatalf("%s: a failed read published state", c.name)
		}
	}
}

// A proposal without a stale mode records fail_closed. The scenario steps
// revoked-fails-closed and expired-fails-closed show that such a revision
// stops on revocation and on expiry.
func TestStaleModeIsFailClosedByDefault(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	for name, want := range map[string]string{"initial_snapshot": "fail_closed", "future_snapshot": "fail_closed", "correction_snapshot": "continue_stale"} {
		snapshot, err := view.Get(f.pin(name), "snapshot")
		if err != nil {
			t.Fatal(err)
		}
		if snapshot["stale_mode"] != want {
			t.Fatalf("%s: stale_mode %v, want %s", name, snapshot["stale_mode"], want)
		}
	}
	steps := map[string]string{}
	for _, x := range requireScenario(t).driver.trace {
		step := x.(Object)
		steps[step["name"].(string)] = at(step, "response", "status").(string)
	}
	if steps["revoked-fails-closed"] != "activation_revoked" || steps["expired-fails-closed"] != "activation_expired" {
		t.Fatalf("fail-closed steps: %v %v", steps["revoked-fails-closed"], steps["expired-fails-closed"])
	}
}

// The Host refuses a stale mode that is not fail_closed or continue_stale.
// The refused proposal publishes no state.
func TestInvalidStaleModeIsRefused(t *testing.T) {
	f := newFixture(t)
	before := f.root()
	author := f.host("author", nil)
	for _, mode := range []any{"continue", "", true, Array{"continue_stale"}} {
		_, err := author.Handle(f.request("propose", Object{"function": "record.visible", "from": "2026-11-01", "until": "2026-12-01",
			"base_snapshot": f.pin("correction_snapshot"), "source_ref": "forged", "bundle": Object{}, "reason": "invalid mode", "stale_mode": mode}))
		if fault, ok := err.(*Fault); !ok || fault.Code != "invalid_request" || !strings.Contains(fault.Message, "stale_mode") {
			t.Fatalf("stale_mode %v: got %v", mode, err)
		}
	}
	if f.root() != before {
		t.Fatal("a refused proposal published state")
	}
}

// A caller cannot select the stale mode. The invoke, describe and load
// requests have no stale_mode field, and a caller cannot propose.
func TestCallerCannotSelectStaleMode(t *testing.T) {
	f := newFixture(t)
	before := f.root()
	view, _ := f.store.Read(nil)
	snapshot, _ := view.Get(f.pin("future_snapshot"), "snapshot")
	caller := f.host("caller", nil)
	invoke := f.request("invoke", Object{"function": "record.visible", "date": "2026-12-15", "expected_bundle_ref": snapshot["bundle_ref"],
		"inputs": Object{"state": "reviewed", "archived": false}, "stale_mode": "continue_stale"})
	for _, q := range []Object{invoke, {"op": "describe", "function": "record.visible", "date": "2026-12-15", "stale_mode": "continue_stale"}} {
		_, err := caller.Handle(q)
		if fault, ok := err.(*Fault); !ok || fault.Code != "invalid_request" || !strings.Contains(fault.Message, "unexpected fields: stale_mode") {
			t.Fatalf("%v: got %v", q["op"], err)
		}
	}
	f.expect("invalid_request")(f.host("worker", nil).Handle(f.request("load", Object{"function": "record.visible", "date": "2026-12-15", "stale_mode": "continue_stale"})))
	f.expect("forbidden")(caller.Handle(f.request("propose", Object{"function": "record.visible", "from": "2026-11-01", "until": "2026-12-01",
		"base_snapshot": f.pin("correction_snapshot"), "source_ref": "forged", "bundle": Object{}, "reason": "caller mode", "stale_mode": "continue_stale"})))
	if f.root() != before {
		t.Fatal("a refused request published state")
	}
}

// A continue_stale revision that a reviewer suspends still gives an answer
// from the loaded activation, and the receipt is stale with the reason
// revoked. Before the from date of the loaded activation, the invocation
// stays out_of_scope. The reads do not continue.
func TestContinueStaleOnRevocation(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	snapshot, _ := view.Get(f.pin("correction_snapshot"), "snapshot")
	f.ok(f.host("reviewer", nil).Handle(f.request("suspend", Object{"function": "record.visible", "from": "2026-11-01", "until": "2026-12-01",
		"targets": Array{f.pin("correction_event")}, "reason": "revoke the correction"})))
	caller := f.host("caller", nil)
	call := func(date, key string) (any, error) {
		return caller.Handle(Object{"op": "invoke", "function": "record.visible", "date": date, "expected_bundle_ref": snapshot["bundle_ref"],
			"inputs": Object{"state": "reviewed", "archived": false}, "expected_revision": f.root(), "request_key": key})
	}
	out := f.ok(call("2026-11-15", "revoked-continue"))
	receipt := out["result"].(Object)["receipt"].(Object)
	if receipt["stale"] != true || receipt["stale_reason"] != "revoked" || receipt["stale_mode"] != "continue_stale" ||
		at(receipt, "selection", "event_ref") != f.pin("correction_event") {
		t.Fatalf("receipt %v", receipt)
	}
	f.expect("out_of_scope")(f.host("reader", nil).Handle(resolveRequest(nil)))
	before := f.root()
	f.expect("out_of_scope")(call("2026-09-15", "before-from"))
	if f.root() != before {
		t.Fatal("a refused invocation published state")
	}
}

// A reader finds each stale receipt with the existing reads: inspect lists
// the receipt references, and the receipt read gives the stale field. A
// replay of a stale receipt is also stale. A restatement is never stale.
func TestStaleReceiptsCanBeFound(t *testing.T) {
	f := newFixture(t)
	reader := f.host("reviewer", nil)
	stale := func() []any {
		state := f.ok(reader.Handle(Object{"op": "inspect"}))["state"].(Object)
		var found []any
		for _, ref := range state["receipts"].(Array) {
			receipt := f.ok(reader.Handle(Object{"op": "receipt", "receipt_ref": ref}))
			if _, ok := receipt["stale"].(bool); !ok {
				t.Fatalf("receipt %v has no stale field", ref)
			}
			if receipt["stale"] == true {
				found = append(found, ref)
			}
		}
		return found
	}
	if got := stale(); !equal(Array(got), Array{f.pin("stale_receipt")}) {
		t.Fatalf("stale receipts %v, want %v", got, f.pin("stale_receipt"))
	}
	replay := f.ok(reader.Handle(f.request("replay", Object{"receipt_ref": f.pin("stale_receipt"), "reason": "replay a stale answer"})))
	receipt := replay["result"].(Object)["receipt"].(Object)
	if receipt["stale"] != true || receipt["stale_reason"] != "expired" || receipt["matches_original"] != true {
		t.Fatalf("replay receipt %v", receipt)
	}
	if got := stale(); len(got) != 2 || !within(replay["result"].(Object)["receipt_ref"], Array(got)) {
		t.Fatalf("stale receipts after replay %v", got)
	}
	restated := f.ok(reader.Handle(Object{"op": "receipt", "receipt_ref": f.pin("new_receipt")}))
	if restated["stale"] != false || restated["stale_reason"] != "" {
		t.Fatalf("restated receipt %v", restated)
	}
}

// The Host and an ingest share one log for each executor ID. An ingest
// under the ID of the Host worker cannot replace a receipt that the Host
// wrote at a cursor.
func TestIngestCannotReplaceAHostReceipt(t *testing.T) {
	f := newFixture(t)
	snapshot := f.ok(f.host("reviewer", nil).Handle(Object{"op": "record", "ref": f.pin("correction_snapshot"), "kind": "snapshot"}))
	entry := Object{"cursor": Int{"1"}, "snapshot_ref": f.pin("correction_snapshot"), "date": "2026-11-15",
		"inputs_ref": InputDigest(Object{"state": "approved"}), "response": Object{"bundle_ref": snapshot["bundle_ref"]}}
	f.expect("ingest_conflict")(f.host("worker", nil).Handle(f.request("ingest", Object{"entries": Array{entry}})))
}

// An invoke receipt takes its executor ID and log cursor from the context
// worker. Thus the same request key from a caller with another worker is
// idempotency_conflict, not a reuse of the receipt of the first worker.
func TestInvokeRequestKeyBindsTheWorker(t *testing.T) {
	f := newFixture(t)
	view, _ := f.store.Read(nil)
	snapshot, _ := view.Get(f.pin("correction_snapshot"), "snapshot")
	q := f.request("invoke", Object{"function": "record.visible", "date": "2026-11-15", "expected_bundle_ref": snapshot["bundle_ref"],
		"inputs": Object{"state": "reviewed", "archived": false}})
	f.ok(f.host("caller", nil).Handle(copyObject(q)))
	before := f.root()
	other := f.host("caller", func(c Object) { c["worker"] = "other-worker" })
	f.expect("idempotency_conflict")(other.Handle(copyObject(q)))
	view, _ = f.store.Read(nil)
	if f.root() != before || lookup(view.State["executors"], "other-worker", nil) != nil {
		t.Fatal("a conflict changed the state")
	}
}
