package lifecycle

// The lifecycle scenario: the record.visible loop of
// src/cel-lifecycle/fixtures/lifecycle-scenario.json, from the first
// proposal to the correction, the pinned replay and the restatement.
//
// Each step opens a new Git store and a new Host, as a new JSONL process
// does. The expected statuses are stated in this file. The expected answers
// come from the "expected" field of the fixture, never from a run.
//
// LIFECYCLE_CEL_BINARY names the pinned CEL executable. LIFECYCLE_CEL_BUNDLE
// names the bundle that src/cel-handoff/tools/prepare.py wrote. If one of
// them is not set, the scenario and the Host tests skip. If
// LIFECYCLE_REQUIRE_SCENARIO is set, a skip is a failure. If
// LIFECYCLE_SCENARIO_RESULTS is set, the scenario writes its checks, pins
// and trace to that file.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
)

type scenarioDriver struct {
	work, repo, binary string
	fixture            Object
	contexts           map[string]Object
	trace, checks      Array
	counter            int
}

// scenarioFailure stops the scenario at a failed step or check.
type scenarioFailure struct{ message string }

func newScenarioDriver(work, binary string, fixture Object) (*scenarioDriver, error) {
	if err := os.MkdirAll(work, 0o777); err != nil {
		return nil, err
	}
	d := &scenarioDriver{work: work, repo: filepath.Join(work, "state.git"), binary: binary, fixture: fixture, contexts: map[string]Object{}}
	roles := map[string]Array{"reviewer": {"reviewer", "reader"}, "author": {"author"}, "runner": {"runner"}, "caller": {"caller"},
		"worker": {"worker"}, "reader": {"reader"}, "observer": {"observer"}, "maintainer": {"maintainer"},
		"other-runner": {"runner"}, "other-maintainer": {"maintainer"}}
	for name, permissions := range roles {
		actor := name
		if name == "runner" {
			actor = "fixture-runner"
		}
		context := Object{"actor": actor, "roles": permissions, "scope": fixture["scope"], "runner_actor": "fixture-runner",
			"worker": "local-worker", "functions": Array{fixture["function"]}}
		raw := Encode(context)
		if err := os.WriteFile(filepath.Join(work, name+".json"), raw, 0o666); err != nil {
			return nil, err
		}
		value, _ := Decode(raw)
		d.contexts[name] = value.(Object)
	}
	return d, nil
}

func (d *scenarioDriver) fail(format string, args ...any) {
	panic(scenarioFailure{fmt.Sprintf(format, args...)})
}

// cursor gives the current root, or "" before the first write.
func (d *scenarioDriver) cursor() string {
	if _, err := os.Stat(d.repo); err != nil {
		return ""
	}
	store, err := OpenGitStore(d.repo)
	if err != nil {
		d.fail("open store: %v", err)
	}
	root, err := store.Root()
	if err != nil {
		d.fail("root: %v", err)
	}
	return root
}

// call sends one request as the actor and checks the response status.
// It gives the response data for "ok", or the full response for a fault.
func (d *scenarioDriver) call(actor, op, name, expected string, values Object) Object {
	d.counter++
	q := Object{"op": op}
	for k, v := range values {
		q[k] = v
	}
	if !Reads[op] {
		if _, ok := q["expected_revision"]; !ok {
			q["expected_revision"] = d.cursor()
		}
		if _, ok := q["request_key"]; !ok {
			if name != "" {
				q["request_key"] = name
			} else {
				q["request_key"] = op + "-" + strconv.Itoa(d.counter)
			}
		}
	}
	if name == "" {
		name = op
	}
	request, err := Decode(Encode(q))
	if err != nil {
		d.fail("%s: request does not encode: %v", name, err)
	}
	var response Object
	store, err := OpenGitStore(d.repo)
	if err == nil {
		var rt *CELRuntime
		if rt, err = NewCELRuntime(d.binary, filepath.Join(d.work, "tmp")); err == nil {
			var host *Host
			if host, err = NewHost(store, rt, d.contexts[actor]); err == nil {
				var result any
				if result, err = host.Handle(request); err == nil {
					response = Object{"status": "ok", "data": result}
				}
			}
		}
	}
	if fault, ok := err.(*Fault); ok {
		response = fault.Record()
	} else if err != nil {
		response = Object{"status": "process_error", "message": err.Error()}
	}
	decoded, derr := Decode(Encode(response))
	if derr != nil {
		d.fail("%s: response does not encode: %v", name, derr)
	}
	response = decoded.(Object)
	d.trace = append(d.trace, Object{"name": name, "actor": actor, "request": request, "response": response, "expected_status": expected})
	if response["status"] != expected {
		d.fail("%s: %s expected %s", name, Encode(response), expected)
	}
	if data, ok := response["data"].(Object); ok {
		return data
	}
	return response
}

func (d *scenarioDriver) check(name, question string, passed bool, actual any) {
	d.checks = append(d.checks, Object{"name": name, "question": question, "passed": passed, "actual": actual})
	if !passed {
		d.fail("check %s failed: %s", name, Encode(actual))
	}
}

func result(value Object) Object { return value["result"].(Object) }

// at gives value[k1][k2]..., or nil if a part is missing.
func at(value any, keys ...string) any {
	for _, k := range keys {
		m, ok := value.(Object)
		if !ok {
			return nil
		}
		value = m[k]
	}
	return value
}

// bundle reads the prepared bundle and replaces the fixed values. The
// manifest gets the new content references.
func (d *scenarioDriver) bundle(dir, allowed string) Object {
	data := map[string][]byte{}
	for key, file := range FileNames {
		raw, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			d.fail("bundle: %v", err)
		}
		data[key] = raw
	}
	data["fixed"] = Encode(Object{"allowed_state": allowed})
	value, err := Decode(data["manifest"])
	if err != nil {
		d.fail("bundle manifest: %v", err)
	}
	manifest := value.(Object)
	for key, field := range map[string]string{"environment": "environment_ref", "source": "source_ref", "fixed": "fixed_ref"} {
		manifest[field] = ContentRef(Envelope(Mimes[key], data[key]))
	}
	data["manifest"] = Encode(manifest)
	files := Object{}
	for key, raw := range data {
		files[key] = string(raw)
	}
	return files
}

// cases are stated by the approved source table, not evaluated from a candidate.
func cases(allowed string) Array {
	return Array{
		Object{"case": "visible-current", "inputs": Object{"state": allowed, "archived": false}, "status": "ok", "value": true},
		Object{"case": "different-state", "inputs": Object{"state": "closed", "archived": false}, "status": "ok", "value": false},
		Object{"case": "archived-current", "inputs": Object{"state": allowed, "archived": true}, "status": "ok", "value": false},
	}
}

func (d *scenarioDriver) table(stage string) Object { return d.fixture[stage].(Object) }

func (d *scenarioDriver) sourcePlan(stage string, sourceSupersedes, planSupersedes Array) (string, string) {
	table := d.table(stage)
	allowed := table["allowed_state"].(string)
	source := result(d.call("reviewer", "source", stage+"-source", "ok", Object{"function": d.fixture["function"],
		"from": table["from"], "until": table["until"],
		"text":       "Invented approved source table: visible state " + allowed + "; archived records are invisible.",
		"supersedes": sourceSupersedes, "reason": stage + " requirement"}))["source_ref"].(string)
	plan := result(d.call("reviewer", "plan", stage+"-plan", "ok", Object{"source_ref": source, "cases": cases(allowed),
		"origin": "Independent host fixture from the invented source table", "supersedes": planSupersedes}))["plan_ref"].(string)
	return source, plan
}

func (d *scenarioDriver) propose(stage string, table Object, source, base string, bundle Object, name, expected string) Object {
	if name == "" {
		name = stage + "-proposal"
	}
	return d.call("author", "propose", name, expected, Object{"function": d.fixture["function"], "from": table["from"], "until": table["until"],
		"base_snapshot": base, "source_ref": source, "bundle": bundle, "reason": stage + " proposal"})
}

func (d *scenarioDriver) qualifyDecide(snapshot, plan, prefix string) (Object, string) {
	q := result(d.call("runner", "qualify", prefix+"-qualify", "ok", Object{"snapshot_ref": snapshot, "plan_ref": plan}))
	decision := result(d.call("reviewer", "decide", prefix+"-decide", "ok", Object{"report_ref": q["report_ref"], "reason": "manual fixture approval"}))
	return q, decision["decision_ref"].(string)
}

// demonstrate runs the scenario. It gives the pins that the Host tests use.
func (d *scenarioDriver) demonstrate(bundleDir string) (pins Object, err error) {
	defer func() {
		if r := recover(); r != nil {
			failure, ok := r.(scenarioFailure)
			if !ok {
				panic(r)
			}
			pins, err = nil, fmt.Errorf("%s", failure.message)
		}
	}()
	f := d.fixture
	fun := f["function"]
	exp := f["expected"].(Object)
	original := f["original_input"]
	invoke := func(name, expected, date string, bundle any, inputs any) Object {
		return d.call("caller", "invoke", name, expected, Object{"function": fun, "date": date, "expected_bundle_ref": bundle, "inputs": inputs})
	}
	s1, p1 := d.sourcePlan("initial", Array{}, Array{})
	b1 := d.bundle(bundleDir, d.table("initial")["allowed_state"].(string))
	candidate := result(d.propose("initial", d.table("initial"), s1, "", b1, "", "ok"))
	snap1 := candidate["snapshot_ref"].(string)
	d.call("caller", "describe", "", "out_of_scope", Object{"function": fun, "date": "2026-10-15"})
	d.call("author", "activate", "", "forbidden", Object{"decision_ref": "forged", "targets": Array{}})
	d.call("author", "qualify", "", "forbidden", Object{"snapshot_ref": snap1, "plan_ref": p1})
	d.call("author", "decide", "", "forbidden", Object{"report_ref": "forged", "reason": "self-certify"})
	q1, dec1 := d.qualifyDecide(snap1, p1, "initial")
	initial := result(d.call("reviewer", "activate", "initial-activate", "ok", Object{"decision_ref": dec1, "targets": Array{}}))["event_ref"].(string)
	d.call("caller", "describe", "", "ok", Object{"function": fun, "date": "2026-10-15"})
	ready := Object{"state": "ready", "archived": false}
	invoke("before-load", "revision_not_loaded", "2026-10-15", candidate["bundle_ref"], ready)
	d.call("worker", "load", "initial-load", "ok", Object{"function": fun, "date": "2026-10-15"})
	october := result(invoke("october-call", "ok", "2026-10-15", candidate["bundle_ref"], ready))
	invalid := result(invoke("fixed-binding-override", "ok", "2026-10-15", candidate["bundle_ref"], Object{"state": "ready", "archived": false, "allowed_state": "closed"}))
	actual, _ := at(invalid, "actual").(Object)
	_, hasValue := actual["value"]
	d.check("typed-caller-boundary", "Does lifecycle invocation retain the tested rejection of a fixed-parameter override?",
		at(invalid, "actual", "status") == "invalid_input" && !hasValue, invalid)
	historical := d.cursor()
	d.check("initial-qualified-loaded-call", "Can the approved bundle serve a typed call only after the selected revision is loaded?",
		at(october, "actual", "value") == exp["initial_october_ready"] && at(q1, "report", "status") == "passed", october)
	// A valid CEL bundle with the wrong host parameter must fail independent cases.
	wrong := result(d.propose("initial", d.table("initial"), s1, snap1, d.bundle(bundleDir, "closed"), "wrong-data-proposal", "ok"))["snapshot_ref"].(string)
	bad := result(d.call("runner", "qualify", "wrong-data-qualify", "ok", Object{"snapshot_ref": wrong, "plan_ref": p1}))
	d.call("reviewer", "decide", "wrong-data-admission", "qualification_failed", Object{"report_ref": bad["report_ref"], "reason": "try bad data"})
	d.check("independent-cases-block-bad-data", "Does a wrong fixed parameter remain a failed inactive proposal?", at(bad, "report", "status") == "failed", bad)
	// A report from a role-allowed but uninstalled runner is not admissible.
	fake := result(d.call("other-runner", "qualify", "other-runner", "ok", Object{"snapshot_ref": snap1, "plan_ref": p1}))
	d.call("reviewer", "decide", "", "untrusted_runner", Object{"report_ref": fake["report_ref"], "reason": "reject other runner"})
	s2, p2 := d.sourcePlan("amendment", Array{s1}, Array{p1})
	b2 := d.bundle(bundleDir, d.table("amendment")["allowed_state"].(string))
	amendment := result(d.propose("amendment", d.table("amendment"), s2, wrong, b2, "", "ok"))
	snap2 := amendment["snapshot_ref"].(string)
	d.propose("amendment", d.table("amendment"), s2, wrong, b2, "stale-competing-proposal", "stale_proposal")
	q2, dec2 := d.qualifyDecide(snap2, p2, "amendment")
	d.call("reviewer", "activate", "ambiguous-write", "ambiguous_activation", Object{"decision_ref": dec2, "targets": Array{}})
	oldRoot := d.cursor()
	futureWrite := d.call("reviewer", "activate", "future-activate", "ok", Object{"decision_ref": dec2, "targets": Array{initial}})
	future := result(futureWrite)["event_ref"].(string)
	futureCursor := futureWrite["cursor"]
	resolve := func(date string, extra Object) Object {
		q := Object{"function": fun, "date": date}
		for k, v := range extra {
			q[k] = v
		}
		return d.call("reader", "resolve", "", "ok", q)
	}
	octSel, novSel, oldNov := resolve("2026-10-15", nil), resolve("2026-11-15", nil), resolve("2026-11-15", Object{"cursor": historical})
	d.check("future-and-historical-selection", "Do interval masks retain the earlier period and the captured historical selection?",
		octSel["snapshot_ref"] == snap1 && novSel["snapshot_ref"] == snap2 && oldNov["snapshot_ref"] == snap1,
		Object{"october": octSel, "november": novSel, "historical_november": oldNov})
	invoke("", "revision_not_loaded", "2026-11-15", amendment["bundle_ref"], original)
	d.call("worker", "load", "future-load", "ok", Object{"function": fun, "date": "2026-11-15"})
	d.call("worker", "ack", "future-ack", "ok", Object{"event_ref": future})
	retry := d.call("reviewer", "activate", "future-activate", "ok", Object{"expected_revision": oldRoot, "decision_ref": dec2, "targets": Array{initial}})
	d.check("successful-key-after-later-writes", "Does retry after load/ack return the original activation cursor and result?",
		retry["reused"] == true && retry["cursor"] == futureCursor && equal(retry["result"], futureWrite["result"]), retry)
	d.call("reviewer", "activate", "future-activate", "idempotency_conflict", Object{"decision_ref": dec2, "targets": Array{}})
	d.call("worker", "ack", "future-ack", "ok", Object{"expected_revision": oldRoot, "event_ref": future})
	before := result(invoke("november-before-correction", "ok", "2026-11-15", amendment["bundle_ref"], original))
	d.check("future-answer", "Is the old answer explicit before correction?", at(before, "actual", "value") == exp["future_november_reviewed"], before)
	observation := result(d.call("observer", "observe", "report-discrepancy", "ok", Object{"receipt_ref": before["receipt_ref"], "route": "source",
		"message": "The approved future state was transcribed incorrectly. Need a source correction."}))
	task := result(d.call("maintainer", "claim", "claim-correction", "ok", Object{"task_id": observation["task_id"], "checkpoint": "0"}))
	saved := result(d.call("maintainer", "checkpoint", "save-correction-work", "ok", Object{"task_id": observation["task_id"], "checkpoint": task["checkpoint"],
		"token": task["token"], "status": "working", "note": "Correction source requested; no rule change yet."}))
	d.call("other-maintainer", "claim", "", "task_claimed", Object{"task_id": observation["task_id"], "checkpoint": saved["checkpoint"]})
	taken := result(d.call("reviewer", "takeover", "manual-takeover", "ok", Object{"task_id": observation["task_id"], "checkpoint": saved["checkpoint"],
		"owner": "other-maintainer", "reason": "Manually resume the saved correction task."}))
	d.call("maintainer", "checkpoint", "", "stale_fence", Object{"task_id": observation["task_id"], "checkpoint": taken["checkpoint"], "token": saved["token"],
		"status": "complete", "note": "stale owner"})
	s3, p3 := d.sourcePlan("correction", Array{s2}, Array{p2})
	stale := result(d.call("runner", "qualify", "superseded-cases", "ok", Object{"snapshot_ref": snap2, "plan_ref": p2}))
	d.call("reviewer", "decide", "", "stale_qualification", Object{"report_ref": q2["report_ref"], "reason": "old source no longer valid here"})
	// The initial evidence is still applicable to a nonoverlapping October snapshot.
	octTable := copyObject(d.table("initial"))
	octTable["until"] = "2026-11-01"
	octCandidate := result(d.propose("initial", octTable, s1, snap2, b1, "october-retained-source", "ok"))["snapshot_ref"].(string)
	octReport := result(d.call("runner", "qualify", "october-retained-cases", "ok", Object{"snapshot_ref": octCandidate, "plan_ref": p1}))
	d.check("scoped-source-and-case-supersession", "Does correction retain old evidence/cases and leave unrelated dates qualified?",
		at(stale, "report", "status") == "unresolved" && at(octReport, "report", "status") == "passed", Object{"superseded": stale, "nonoverlap": octReport})
	corrected := result(d.propose("correction", d.table("correction"), s3, octCandidate, d.bundle(bundleDir, d.table("correction")["allowed_state"].(string)), "", "ok"))
	snap3 := corrected["snapshot_ref"].(string)
	_, dec3 := d.qualifyDecide(snap3, p3, "correction")
	correction := d.table("correction")
	suspension := result(d.call("reviewer", "suspend", "suspend-wrong-future", "ok", Object{"function": fun, "from": correction["from"], "until": correction["until"],
		"targets": Array{future}, "reason": "stop serving incorrect source data"}))["event_ref"].(string)
	d.call("caller", "describe", "suspension-no-fallback", "out_of_scope", Object{"function": fun, "date": "2026-11-15"})
	currentOct := resolve("2026-10-15", nil)
	enabled := result(d.call("reviewer", "activate", "activate-correction", "ok", Object{"decision_ref": dec3, "targets": Array{future, suspension}}))["event_ref"].(string)
	correctionCursor := d.cursor()
	invoke("old-discovery", "artifact_mismatch", "2026-11-15", amendment["bundle_ref"], original)
	invoke("correction-before-load", "revision_not_loaded", "2026-11-15", corrected["bundle_ref"], original)
	d.call("worker", "load", "load-correction", "ok", Object{"function": fun, "date": "2026-11-15"})
	d.call("worker", "ack", "ack-correction", "ok", Object{"event_ref": enabled})
	after := result(invoke("november-corrected", "ok", "2026-11-15", corrected["bundle_ref"], original))
	replay := result(d.call("reader", "replay", "pinned-replay", "ok", Object{"receipt_ref": before["receipt_ref"], "reason": "Reproduce the earlier answer without resolving latest."}))
	restated := result(d.call("reader", "restate", "later-restatement", "ok", Object{"receipt_ref": before["receipt_ref"], "cursor": correctionCursor,
		"reason": "Use the approved source correction for this original input/date."}))
	d.call("reader", "replay", "changed-replay-inputs", "input_mismatch", Object{"receipt_ref": before["receipt_ref"], "inputs_json": `{"state":"approved","archived":false}`,
		"reason": "must not change old inputs"})
	retained := d.call("reader", "receipt", "", "ok", Object{"receipt_ref": before["receipt_ref"]})
	d.check("correction-pinned-replay-restatement", "Can an operator preserve the old answer and compute a separate corrected answer?",
		at(after, "actual", "value") == exp["corrected_november_reviewed"] && at(replay, "actual", "value") == exp["pinned_replay"] &&
			at(replay, "receipt", "matches_original") == true && at(restated, "actual", "value") == exp["later_restatement"] &&
			equal(retained, before["receipt"]) && at(replay, "receipt", "selection", "snapshot_ref") == snap2 &&
			at(restated, "receipt", "selection", "snapshot_ref") == snap3 && currentOct["snapshot_ref"] == snap1,
		Object{"before": before, "after": after, "replay": replay, "restatement": restated, "retained_old_receipt": retained})
	complete := result(d.call("other-maintainer", "checkpoint", "complete-correction", "ok", Object{"task_id": observation["task_id"], "checkpoint": taken["checkpoint"],
		"token": taken["token"], "status": "complete", "note": "Source correction activated; original answer replayed and correction restated.", "correction_snapshot": snap3}))
	d.check("restart-and-fenced-task", "Can a fresh process resume a saved task while rejecting the old owner?",
		complete["status"] == "complete" && complete["correction_snapshot"] == snap3, complete)
	state := d.call("reviewer", "inspect", "", "ok", Object{})["state"].(Object)
	outbox, _ := at(state, "outbox", enabled, "status").(string)
	d.check("retained-history-and-outbox", "Are earlier evidence/plans/receipts retained with acknowledged load events?",
		len(state["sources"].(Array)) == 3 && len(state["plans"].(Array)) == 3 && outbox == "acknowledged" && within(before["receipt_ref"], state["receipts"]),
		Object{"sources": state["sources"], "plans": state["plans"], "outbox": state["outbox"], "receipts": state["receipts"]})
	return Object{"initial_snapshot": snap1, "future_snapshot": snap2, "correction_snapshot": snap3, "future_event": future, "correction_event": enabled,
		"decision": dec3, "old_receipt": before["receipt_ref"], "new_receipt": restated["receipt_ref"], "historical_cursor": historical,
		"correction_cursor": correctionCursor}, nil
}

// scenarioRun is the one scenario run of this test process. The Host tests
// copy its Git repository.
type scenarioRun struct {
	dir, binary string
	driver      *scenarioDriver
	pins        Object
	err         error
	skip        string
}

var (
	scenarioOnce   sync.Once
	sharedScenario scenarioRun
)

func runScenario() scenarioRun {
	scenarioOnce.Do(func() {
		binary, bundle := os.Getenv("LIFECYCLE_CEL_BINARY"), os.Getenv("LIFECYCLE_CEL_BUNDLE")
		if binary == "" || bundle == "" {
			sharedScenario.skip = "LIFECYCLE_CEL_BINARY and LIFECYCLE_CEL_BUNDLE are not set"
			return
		}
		raw, err := os.ReadFile(filepath.Join("..", "cel-lifecycle", "fixtures", "lifecycle-scenario.json"))
		if err != nil {
			sharedScenario.err = err
			return
		}
		fixture, err := Decode(raw)
		if err != nil {
			sharedScenario.err = err
			return
		}
		// The store goes next to the results file, if there is one, so that
		// the work directory keeps it after a failure.
		parent := ""
		if out := os.Getenv("LIFECYCLE_SCENARIO_RESULTS"); out != "" {
			parent = filepath.Dir(out)
		}
		dir, err := os.MkdirTemp(parent, "lifecycle-scenario-")
		if err != nil {
			sharedScenario.err = err
			return
		}
		d, err := newScenarioDriver(filepath.Join(dir, "baseline"), binary, fixture.(Object))
		if err != nil {
			sharedScenario = scenarioRun{dir: dir, err: err}
			return
		}
		pins, err := d.demonstrate(bundle)
		sharedScenario = scenarioRun{dir: dir, binary: binary, driver: d, pins: pins, err: err}
		if out := os.Getenv("LIFECYCLE_SCENARIO_RESULTS"); out != "" {
			status := "passed"
			var message any
			if err != nil {
				status, message = "failed", err.Error()
			}
			record := Object{"kind": "NeutralLifecycleDemonstration", "status": status, "error": message, "checks": d.checks, "pins": pins,
				"steps": len(d.trace), "trace": d.trace}
			if werr := os.WriteFile(out, Encode(record), 0o666); werr != nil && sharedScenario.err == nil {
				sharedScenario.err = werr
			}
		}
	})
	return sharedScenario
}

func TestMain(m *testing.M) {
	code := m.Run()
	// Remove the scenario store only after a run with no failure. After a
	// failure, keep the store, the objects and the contexts as evidence.
	if sharedScenario.dir != "" {
		if code == 0 && sharedScenario.err == nil {
			os.RemoveAll(sharedScenario.dir)
		} else {
			fmt.Fprintf(os.Stderr, "lifecycle scenario: kept the store at %s\n", sharedScenario.dir)
		}
	}
	os.Exit(code)
}

// requireScenario gives the scenario run, or skips the test without it.
func requireScenario(t *testing.T) scenarioRun {
	t.Helper()
	run := runScenario()
	if run.skip != "" {
		if os.Getenv("LIFECYCLE_REQUIRE_SCENARIO") != "" {
			t.Fatal(run.skip)
		}
		t.Skip(run.skip)
	}
	if run.err != nil {
		t.Fatalf("the lifecycle scenario failed: %v", run.err)
	}
	return run
}

// The scenario has 68 steps and 10 checks. Each check passed, or the
// scenario stopped with an error.
func TestLifecycleScenario(t *testing.T) {
	run := requireScenario(t)
	if len(run.driver.trace) != 68 || len(run.driver.checks) != 10 {
		t.Fatalf("%d steps and %d checks, want 68 and 10", len(run.driver.trace), len(run.driver.checks))
	}
	for _, x := range run.driver.trace {
		step := x.(Object)
		if at(step, "response", "status") != step["expected_status"] {
			t.Errorf("%s: %s", step["name"], Encode(step["response"]))
		}
	}
}
