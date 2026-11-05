package lifecycle

// The trusted lifecycle Host. The specs in src/cel-lifecycle define its
// operations, roles, checks and diagnostics. Internal helpers
// stop with a panic that carries a *Fault or a *requestError; Handle and the
// Transact callback turn that panic back into an error.

import (
	"embed"
	"errors"
	"io/fs"
	"math"
	"os/exec"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Roles are installed by the embedding host, never read from a request.
var schema = map[string]struct {
	role   string
	fields []string
}{
	"source":     {"reviewer", []string{"function", "from", "until", "text", "supersedes", "reason"}},
	"plan":       {"reviewer", []string{"source_ref", "cases", "origin", "supersedes"}},
	"propose":    {"author", []string{"function", "from", "until", "base_snapshot", "source_ref", "bundle", "reason", "stale_mode"}},
	"qualify":    {"runner", []string{"snapshot_ref", "plan_ref"}},
	"decide":     {"reviewer", []string{"report_ref", "reason"}},
	"activate":   {"reviewer", []string{"decision_ref", "targets"}},
	"suspend":    {"reviewer", []string{"function", "from", "until", "targets", "reason"}},
	"load":       {"worker", []string{"function", "date"}},
	"invoke":     {"caller", []string{"function", "date", "expected_bundle_ref", "inputs"}},
	"replay":     {"reader", []string{"receipt_ref", "inputs_json", "reason"}},
	"restate":    {"reader", []string{"receipt_ref", "cursor", "reason"}},
	"observe":    {"observer", []string{"receipt_ref", "route", "message"}},
	"claim":      {"maintainer", []string{"task_id", "checkpoint"}},
	"checkpoint": {"maintainer", []string{"task_id", "checkpoint", "token", "status", "note", "correction_snapshot"}},
	"takeover":   {"reviewer", []string{"task_id", "checkpoint", "owner", "reason"}},
	"ack":        {"worker", []string{"event_ref"}},
	"describe":   {"caller", []string{"function", "date"}},
	"resolve":    {"reader", []string{"function", "date", "cursor"}},
	"inspect":    {"reviewer", []string{"cursor"}},
	"record":     {"reviewer", []string{"ref", "kind", "cursor"}},
	"receipt":    {"reader", []string{"receipt_ref"}},
	"ingest":     {"worker", []string{"entries"}},
}

// Reads are the operations that publish no state.
var Reads = map[string]bool{"describe": true, "resolve": true, "inspect": true, "record": true, "receipt": true}

//go:embed *.go
var sources embed.FS

// Implementation is the content identity of this lifecycle build: the core
// sources, the Go version and the Git version.
func Implementation() (string, error) {
	names, err := fs.Glob(sources, "*.go")
	if err != nil {
		return "", err
	}
	sort.Strings(names)
	var body []byte
	for _, name := range names {
		// The identity holds only the core sources, not the tests.
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		raw, err := sources.ReadFile(name)
		if err != nil {
			return "", err
		}
		body = append(append(append(append(body, name...), 0), raw...), 0)
	}
	body = append(body, runtime.Version()...)
	cmd := exec.Command("git", "--version")
	cmd.Env = gitEnvironment()
	git, err := cmd.Output()
	if err != nil {
		return "", NewFault("configuration_error", "git is unavailable: "+err.Error(), "")
	}
	body = append(body, git...)
	return ContentRef(Envelope(Media("implementation"), body)), nil
}

// Host is the trusted lifecycle host for one context.
type Host struct {
	Store    Store
	Runtime  Runtime
	Runner   Runner
	Context  Object
	BuildRef string
}

// NewHost checks the trusted host context. The CEL runner of rt qualifies
// the candidates.
func NewHost(store Store, rt Runtime, context any) (*Host, error) {
	return NewHostWithRunner(store, rt, &CELRunner{Runtime: rt}, context)
}

// NewHostWithRunner checks the trusted host context. The runner qualifies
// the candidates. The embedding host installs the runner, the same as the
// roles. A request never selects it.
func NewHostWithRunner(store Store, rt Runtime, runner Runner, context any) (*Host, error) {
	if runner == nil {
		return nil, NewFault("configuration_error", "host runner is required", "")
	}
	ctx, ok := context.(Object)
	if !ok {
		return nil, NewFault("configuration_error", "host context must be an object", "")
	}
	for _, name := range []string{"actor", "roles", "scope", "runner_actor", "worker", "functions"} {
		if _, ok := ctx[name]; !ok {
			return nil, NewFault("configuration_error", "missing host context "+name, "")
		}
	}
	for _, k := range []string{"actor", "scope", "runner_actor", "worker"} {
		if s, ok := ctx[k].(string); !ok || s == "" {
			return nil, NewFault("configuration_error", "host actor/scope/runner/worker must be nonempty strings", "")
		}
	}
	for _, name := range []string{"roles", "functions"} {
		list, ok := ctx[name].(Array)
		valid := ok && len(list) > 0
		for _, x := range list {
			if s, ok := x.(string); !ok || s == "" {
				valid = false
			}
		}
		if !valid {
			return nil, NewFault("configuration_error", "host "+name+" must be a nonempty string list", "")
		}
	}
	// upload_inputs is the policy for raw inputs in an ingest. If it is not
	// set, the raw inputs stay with the executor.
	if policy, has := ctx["upload_inputs"]; has {
		if _, ok := policy.(bool); !ok {
			return nil, NewFault("configuration_error", "host upload_inputs must be a boolean", "")
		}
	}
	build, err := Implementation()
	if err != nil {
		return nil, err
	}
	return &Host{Store: store, Runtime: rt, Runner: runner, Context: ctx, BuildRef: build}, nil
}

func (h *Host) ctx(name string) string { return h.Context[name].(string) }

func (h *Host) ctxList(name string) Array { return h.Context[name].(Array) }

// recovered turns a helper panic into an error.
func recovered(r any) error {
	switch e := r.(type) {
	case *Fault:
		return e
	case *requestError:
		return NewFault("invalid_request", "invalid or missing field: "+e.message, "")
	case error:
		return e
	}
	panic(r)
}

// Handle runs one request. The error is a *Fault, or another error for a
// host failure.
func (h *Host) Handle(request any) (result any, err error) {
	q, ok := request.(Object)
	if !ok {
		return nil, NewFault("invalid_request", "unknown operation", "")
	}
	op, _ := q["op"].(string)
	spec, known := schema[op]
	if !known {
		return nil, NewFault("invalid_request", "unknown operation", "")
	}
	if !withinStrings(spec.role, toStrings(h.ctxList("roles"))) {
		return nil, NewFault("forbidden", "host role does not permit "+op, "authority")
	}
	allowed := map[string]bool{"op": true}
	for _, f := range spec.fields {
		allowed[f] = true
	}
	if !Reads[op] {
		allowed["expected_revision"], allowed["request_key"] = true, true
	}
	var extra []string
	for k := range q {
		if !allowed[k] {
			extra = append(extra, k)
		}
	}
	if len(extra) > 0 {
		sort.Strings(extra)
		return nil, NewFault("invalid_request", "unexpected fields: "+strings.Join(extra, ","), "")
	}
	defer func() {
		if r := recover(); r != nil {
			result, err = nil, recovered(r)
		}
	}()
	if Reads[op] {
		return h.readOperation(op, q), nil
	}
	_, hasRevision := q["expected_revision"]
	_, hasKey := q["request_key"]
	if !hasRevision || !hasKey {
		return nil, NewFault("invalid_request", "revision and request key required", "")
	}
	request2 := Object{}
	for k, v := range q {
		if k != "expected_revision" && k != "request_key" {
			request2[k] = v
		}
	}
	payload := Object{"request": request2, "actor": h.ctx("actor"), "scope": h.ctx("scope"), "policy_runner": h.ctx("runner_actor")}
	out, err := h.Store.Transact(q["expected_revision"], q["request_key"], payload, func(v *View) (value any, err error) {
		defer func() {
			if r := recover(); r != nil {
				value, err = nil, recovered(r)
			}
		}()
		return h.apply(v, op, q), nil
	})
	if err != nil {
		return nil, mapError(err)
	}
	return out, nil
}

// mapError turns a requestError from a store into an invalid_request Fault.
func mapError(err error) error {
	var re *requestError
	if errors.As(err, &re) {
		return recovered(re)
	}
	return err
}

func toStrings(list Array) []string {
	out := make([]string, len(list))
	for i, x := range list {
		out[i] = x.(string)
	}
	return out
}

// get and getRaw are View reads that stop on a fault.
func get(v *View, ref any, kind string) Object { return must(v.Get(ref, kind)) }

func getRaw(v *View, ref any, mime string) []byte { return must(v.GetRaw(ref, mime)) }

func checkDate(value any) string {
	s, ok := value.(string)
	if !ok {
		fail("invalid_interval", "date must be an ISO string", "")
	}
	parsed, err := time.Parse("2006-01-02", s)
	if err != nil {
		fail("invalid_interval", err.Error(), "")
	}
	if parsed.Year() < 1 || parsed.Format("2006-01-02") != s {
		fail("invalid_interval", "date must be canonical ISO date", "")
	}
	return s
}

func interval(record any) {
	checkDate(item(record, "from"))
	checkDate(item(record, "until"))
	if lessEqual(item(record, "until"), item(record, "from")) {
		fail("invalid_interval", "empty or reversed interval", "")
	}
}

func overlaps(a, b any) bool {
	return less(item(a, "from"), item(b, "until")) && less(item(b, "from"), item(a, "until"))
}

func covers(a, b any) bool {
	return lessEqual(item(a, "from"), item(b, "from")) && lessEqual(item(b, "until"), item(a, "until"))
}

func same(a, b any) bool {
	return equal(item(a, "function"), item(b, "function")) && equal(item(a, "scope"), item(b, "scope"))
}

func (h *Host) owned(record Object) Object {
	if !equal(item(record, "scope"), h.ctx("scope")) {
		fail("out_of_scope", "exact trusted scope required", "")
	}
	if !within(item(record, "function"), h.ctxList("functions")) {
		fail("out_of_scope", "function is not installed", "")
	}
	return record
}

func (h *Host) target(q Object) Object {
	value := Object{"function": item(q, "function"), "scope": h.ctx("scope"), "from": item(q, "from"), "until": item(q, "until")}
	h.owned(value)
	interval(value)
	return value
}

func (h *Host) scoped(v *View, ref any, kind string) Object { return h.owned(get(v, ref, kind)) }

func (h *Host) files(v *View, snapshot Object) map[string][]byte {
	refs := item(snapshot, "files")
	names := map[any]bool{}
	for _, x := range elements(refs) {
		names[hashable(x)] = true
	}
	valid := len(names) == len(FileNames)
	for k := range FileNames {
		if !names[k] {
			valid = false
		}
	}
	if !valid {
		fail("artifact_unavailable", "snapshot dependency closure is incomplete", "store")
	}
	out := map[string][]byte{}
	for key, ref := range object(refs) {
		out[key] = getRaw(v, ref, Mimes[key])
	}
	return out
}

func (h *Host) sourceValid(v *View, sourceRef any, target Object) bool {
	source := h.scoped(v, sourceRef, "source")
	if !same(source, target) || !covers(source, target) {
		return false
	}
	for _, ref := range elements(item(v.State, "sources")) {
		other := get(v, ref, "source")
		if within(sourceRef, item(other, "supersedes")) && same(other, target) && overlaps(other, target) {
			return false
		}
	}
	return true
}

func (h *Host) planValid(v *View, planRef any, snapshot Object) bool {
	plan := h.scoped(v, planRef, "plan")
	if !(equal(item(plan, "source_ref"), item(snapshot, "source_ref")) && covers(plan, snapshot)) {
		return false
	}
	for _, ref := range elements(item(v.State, "plans")) {
		newer := get(v, ref, "plan")
		if within(planRef, item(newer, "supersedes")) && same(newer, snapshot) && overlaps(newer, snapshot) {
			return false
		}
	}
	return true
}

func (h *Host) reportValid(v *View, ref any) (Object, Object) {
	report := h.scoped(v, ref, "qualification")
	snapshot := h.scoped(v, item(report, "snapshot_ref"), "snapshot")
	if !equal(item(report, "status"), "passed") {
		fail("qualification_failed", "the exact report did not pass", "admission")
	}
	if !equal(item(report, "runner"), h.ctx("runner_actor")) {
		fail("untrusted_runner", "report is not from the installed runner", "admission")
	}
	if !equal(item(report, "lifecycle_build"), h.BuildRef) || !equal(item(report, "engine_build"), must(h.Runner.Identity())) {
		fail("implementation_unavailable", "qualification build is not installed", "admission")
	}
	// Check the bindings again. The verdict must name the bytes of this
	// snapshot and the runner identity of this report. Thus a passed flag
	// never moves to other bytes or to another runner.
	outcome := item(report, "outcome")
	if !equal(lookupAny(outcome, "bundle_ref"), item(snapshot, "bundle_ref")) ||
		!equal(lookupAny(outcome, "implementation_ref"), item(report, "engine_build")) ||
		!equal(item(report, "engine_build"), h.Runner.Expected(candidate(item(report, "snapshot_ref"), snapshot))) {
		fail("qualification_failed", "the exact report did not pass", "admission")
	}
	if !h.sourceValid(v, item(snapshot, "source_ref"), snapshot) || !h.planValid(v, item(report, "plan_ref"), snapshot) {
		fail("stale_qualification", "source or cases have been superseded on this interval", "admission")
	}
	return report, snapshot
}

func (h *Host) qualifiedBuild(v *View, selection Object) {
	report := h.scoped(v, item(selection, "report_ref"), "qualification")
	if !equal(item(report, "lifecycle_build"), h.BuildRef) {
		fail("implementation_unavailable", "installed lifecycle build differs from the qualified build", "runtime")
	}
}

func (h *Host) events(v *View) []Object {
	var values []Object
	for _, ref := range elements(item(v.State, "events")) {
		event := get(v, ref, "event")
		interval(event)
		if !within(lookup(event, "kind", nil), Array{"enable", "suspend"}) {
			fail("invalid_record", "unknown registry event", "")
		}
		event = merge(event, Object{"ref": ref})
		values = append(values, event)
	}
	known := map[any]Object{}
	for _, event := range values {
		for _, target := range elements(item(event, "targets")) {
			old, ok := known[hashable(target)]
			if !ok || !same(old, event) || !overlaps(old, event) {
				fail("invalid_record", "invalid event mask target", "")
			}
		}
		known[hashable(event["ref"])] = event
	}
	return values
}

// live gives the live enable events at a date, in insertion order.
func (h *Host) live(v *View, function any, at any) ([]any, map[any]Object) {
	checkDate(at)
	probe := Object{"function": function, "scope": h.ctx("scope")}
	h.owned(probe)
	var order []any
	active := map[any]Object{}
	remove := func(ref any) {
		if _, ok := active[ref]; !ok {
			return
		}
		delete(active, ref)
		for i, x := range order {
			if x == ref {
				order = append(order[:i], order[i+1:]...)
				break
			}
		}
	}
	for _, event := range h.events(v) {
		if same(event, probe) && lessEqual(item(event, "from"), at) && less(at, item(event, "until")) {
			for _, ref := range elements(item(event, "targets")) {
				remove(hashable(ref))
			}
			if equal(item(event, "kind"), "enable") {
				ref := hashable(event["ref"])
				if _, ok := active[ref]; !ok {
					order = append(order, ref)
				}
				active[ref] = event
			}
		}
	}
	if len(active) > 1 {
		fail("ambiguous_activation", "multiple live revisions; refusing selection", "resolve")
	}
	return order, active
}

func (h *Host) resolve(v *View, function any, at any) Object {
	order, active := h.live(v, function, at)
	if len(active) == 0 {
		fail("out_of_scope", "no enabled revision for the exact function/scope/date", "resolve")
	}
	return h.selection(v, function, at, active[order[0]])
}

// Stale modes. The proposal declares the mode of a revision. The snapshot
// records it, and qualification, the decision and the activation bind that
// snapshot. A caller cannot select a mode.
const (
	failClosed    = "fail_closed"
	continueStale = "continue_stale"
)

// serving gives the selection for an invocation and the stale reason. The
// reason is "" if an activation is live at the date. If no activation is
// live, serving examines the activation that the worker loaded. If the date
// is at or after its until date, the activation expired. If the date is in
// its interval, a later registry event masked it, and it is revoked. In the
// fail_closed mode, the invocation stops with activation_expired or
// activation_revoked. In the continue_stale mode, the loaded activation
// gives the answer, and the receipt is stale. The Host uses only the
// invocation date. It does not read or synchronize a clock.
func (h *Host) serving(v *View, function any, at any) (Object, string) {
	order, active := h.live(v, function, at)
	if len(active) > 0 {
		return h.selection(v, function, at, active[order[0]]), ""
	}
	loaded := lookup(item(v.State, "workers"), h.ctx("worker"), Object{})
	var event Object
	for _, x := range h.events(v) {
		if equal(x["ref"], lookup(loaded, "event_ref", nil)) {
			event = x
		}
	}
	probe := Object{"function": function, "scope": h.ctx("scope")}
	if event == nil || !equal(item(event, "kind"), "enable") || !same(event, probe) || less(at, item(event, "from")) {
		fail("out_of_scope", "no enabled revision for the exact function/scope/date", "resolve")
	}
	reason, code := "revoked", "activation_revoked"
	if lessEqual(item(event, "until"), at) {
		reason, code = "expired", "activation_expired"
	}
	snapshot := h.scoped(v, item(event, "snapshot_ref"), "snapshot")
	if !equal(lookup(snapshot, "stale_mode", nil), continueStale) {
		fail(code, "the loaded activation is "+reason+" at this date; the revision fails closed", "resolve")
	}
	return h.selection(v, function, at, event), reason
}

// selection is the resolved selection of one enable event.
func (h *Host) selection(v *View, function any, at any, event Object) Object {
	snapshot := h.scoped(v, item(event, "snapshot_ref"), "snapshot")
	return Object{"function": function, "scope": h.ctx("scope"), "date": at, "registry_cursor": v.Cursor,
		"event_ref": item(event, "ref"), "snapshot_ref": item(event, "snapshot_ref"), "bundle_ref": item(snapshot, "bundle_ref"),
		"engine_build": item(snapshot, "engine_build"), "report_ref": item(event, "report_ref"), "decision_ref": item(event, "decision_ref")}
}

func (h *Host) readOperation(op string, q Object) any {
	v := must(h.Store.Read(lookup(q, "cursor", nil)))
	switch op {
	case "inspect":
		return Object{"cursor": v.Cursor, "state": v.State}
	case "record":
		return get(v, item(q, "ref"), text(item(q, "kind")))
	case "receipt":
		return h.scoped(v, item(q, "receipt_ref"), "receipt")
	}
	selected := h.resolve(v, item(q, "function"), item(q, "date"))
	if op == "resolve" {
		return selected
	}
	snapshot := h.scoped(v, selected["snapshot_ref"], "snapshot")
	desc := must(h.Runtime.Execute(h.files(v, snapshot), "describe", nil, nil))
	return Object{"cursor": v.Cursor, "selection": selected, "callable": desc}
}

// receipt runs the selection and records a receipt. staleReason is "" for
// a receipt that is not stale. Each receipt records the stale mode of the
// selected snapshot, and stale is true only for a continue_stale answer.
func (h *Host) receipt(v *View, selection Object, inputs []byte, key any, mode string, previous, reason, original any, staleReason string) Object {
	snapshot := h.scoped(v, item(selection, "snapshot_ref"), "snapshot")
	if !equal(must(h.Runtime.Build()), item(selection, "engine_build")) {
		fail("implementation_unavailable", "exact replay/execution build unavailable", "runtime")
	}
	h.qualifiedBuild(v, selection)
	files := h.files(v, snapshot)
	must(h.Runtime.Execute(files, "describe", nil, nil))
	actual := must(h.Runtime.Execute(files, "run", inputs, nil))
	if !equal(lookup(actual, "bundle_ref", nil), item(selection, "bundle_ref")) || !equal(lookup(actual, "implementation_ref", nil), item(selection, "engine_build")) {
		fail("artifact_mismatch", "actual execution identity differs from the selection", "runtime")
	}
	inputRef := v.PutRaw("application/json", inputs)
	responseRef := v.PutRaw("application/json", Encode(actual))
	record := Object{"function": item(selection, "function"), "scope": item(selection, "scope"), "mode": mode, "selection": selection,
		"inputs_ref": inputRef, "response_ref": responseRef, "actual_bundle": item(actual, "bundle_ref"),
		"engine_build": item(actual, "implementation_ref"), "lifecycle_build": h.BuildRef, "invocation_id": key,
		"previous_receipt": previous, "reason": reason, "stale_mode": lookup(snapshot, "stale_mode", failClosed),
		"stale": staleReason != "", "stale_reason": staleReason}
	if truthy(original) {
		record["matches_original"] = equal(responseRef, original)
	}
	// The Host is the executor of the worker in its context. The receipt
	// takes the next position in the log of that executor.
	executor := h.ctx("worker")
	log := executorLog(v, executor)
	record["executor_id"], record["log_cursor"] = executor, Int{strconv.Itoa(len(log) + 1)}
	ref := v.Put("receipt", record)
	setExecutorLog(v, executor, append(log, ref))
	v.State["receipts"] = append(stringList(v.State["receipts"]), ref)
	return Object{"receipt_ref": ref, "receipt": record, "actual": actual}
}

// InputDigest is the content reference of the canonical bytes of inputs. It
// is the inputs_ref of a receipt.
func InputDigest(inputs any) string {
	return ContentRef(Envelope("application/json", Encode(inputs)))
}

// executorLog gives the receipt references of an executor, in log order.
// The receipt at position n, from 1, has log_cursor n. A state without
// executors has empty logs.
func executorLog(v *View, executor string) Array {
	logs, ok := lookup(v.State, "executors", Object{}).(Object)
	if !ok {
		fail("invalid_record", "state executors must be an object", "store")
	}
	return stringList(lookup(logs, executor, Array{}))
}

// setExecutorLog makes the executors field only if it is absent. A field
// of another type is a corrupt state, so the write fails closed.
func setExecutorLog(v *View, executor string, log Array) {
	raw, has := v.State["executors"]
	logs, ok := raw.(Object)
	if has && !ok {
		fail("invalid_record", "state executors must be an object", "store")
	}
	if !has {
		logs = Object{}
		v.State["executors"] = logs
	}
	logs[executor] = log
}

// entryFields are the fields of one entry in the local log of an executor.
// An entry has no executor_id. The Host takes the executor ID from its
// context, so an entry cannot name another executor.
var entryFields = map[string]bool{"cursor": true, "snapshot_ref": true, "date": true, "inputs_ref": true, "response": true,
	"inputs": true, "stale_reason": true}

// maxCursor is the largest log cursor: the largest exact JSON integer.
const maxCursor int64 = 1<<53 - 1

// logCursor reads the cursor of a log entry: an integer from 1 to 2^53-1.
// The parse uses 64 bits on each platform. On a 32-bit platform, a cursor
// that is larger than the largest int is refused, because no log on that
// platform has that position.
func logCursor(value any) int {
	n, ok := value.(Int)
	c, err := strconv.ParseInt(n.Text, 10, 64)
	if !ok || err != nil || c < 1 || c > maxCursor || c > int64(math.MaxInt) {
		fail("invalid_request", "log entry cursor must be an integer from 1 to 2^53-1", "")
	}
	return int(c)
}

// pinnedActivation checks that an enable event of the pinned snapshot
// covers the date of a log entry. For a stale reason of "" or "revoked",
// the date must be in the half-open interval of the event. For "expired",
// the date must be at or after the until date of the event. A stale
// reason for a fail_closed snapshot is refused with the code that invoke
// gives in that mode.
func (h *Host) pinnedActivation(v *View, snapshotRef any, snapshot Object, date, staleReason string) {
	if staleReason != "" && !equal(lookup(snapshot, "stale_mode", nil), continueStale) {
		fail("activation_"+staleReason, "the pinned snapshot fails closed, so it has no stale receipt", "resolve")
	}
	for _, event := range h.events(v) {
		if !equal(item(event, "kind"), "enable") || !same(event, snapshot) || !equal(item(event, "snapshot_ref"), snapshotRef) || less(date, item(event, "from")) {
			continue
		}
		if lessEqual(item(event, "until"), date) == (staleReason == "expired") {
			return
		}
	}
	fail("out_of_scope", "no activation of the pinned snapshot covers the entry date and stale reason", "resolve")
}

// ingest records the entries of the local log of the executor in the Host
// context. The entries are in log order. Ingest is idempotent on the pair
// (executor ID, cursor):
//
//   - A cursor in the log with the same receipt is a duplicate. It adds
//     no record.
//   - A cursor in the log with a different receipt is ingest_conflict.
//   - The next cursor after the log appends the receipt.
//   - A cursor after the next cursor is ingest_gap.
//
// A fault stops the full ingest, so a failed ingest publishes no state.
// The Host does not run the rule again. An ingested receipt is the
// statement of the executor, bound to the pinned snapshot.
func (h *Host) ingest(v *View, entries any) Object {
	list, ok := entries.(Array)
	if !ok || len(list) == 0 {
		fail("invalid_request", "ingest needs a nonempty list of log entries", "")
	}
	executor := h.ctx("worker")
	upload, _ := h.Context["upload_inputs"].(bool)
	log := executorLog(v, executor)
	refs := Array{}
	added := 0
	for _, raw := range list {
		entry, ok := raw.(Object)
		if !ok {
			fail("invalid_request", "each log entry must be an object", "")
		}
		var extra []string
		for k := range entry {
			if !entryFields[k] {
				extra = append(extra, k)
			}
		}
		if len(extra) > 0 {
			sort.Strings(extra)
			fail("invalid_request", "unexpected log entry fields: "+strings.Join(extra, ","), "")
		}
		cursor := logCursor(item(entry, "cursor"))
		inputsRef, isRef := item(entry, "inputs_ref").(string)
		response, isObject := item(entry, "response").(Object)
		staleReason := lookup(entry, "stale_reason", "")
		if !isRef || !refPattern.MatchString(inputsRef) || !isObject || !within(staleReason, Array{"", "expired", "revoked"}) {
			fail("invalid_request", "log entry needs an inputs_ref, a response object and a known stale_reason", "")
		}
		inputs, hasInputs := entry["inputs"]
		if hasInputs && !upload {
			fail("upload_not_permitted", "the host policy keeps raw inputs with the executor; send inputs_ref only", "")
		}
		if hasInputs && InputDigest(inputs) != inputsRef {
			fail("input_mismatch", "uploaded inputs differ from inputs_ref", "")
		}
		date := checkDate(item(entry, "date"))
		// scoped refuses a snapshot outside the trusted scope or for a
		// function that the context does not install, as resolve does.
		snapshot := h.scoped(v, item(entry, "snapshot_ref"), "snapshot")
		h.pinnedActivation(v, entry["snapshot_ref"], snapshot, date, staleReason.(string))
		if !equal(lookup(response, "bundle_ref", nil), item(snapshot, "bundle_ref")) {
			fail("artifact_mismatch", "the response names a bundle that differs from the pinned snapshot", "")
		}
		responseBytes := Encode(response)
		selection := Object{"function": item(snapshot, "function"), "scope": item(snapshot, "scope"), "date": date,
			"snapshot_ref": entry["snapshot_ref"], "bundle_ref": item(snapshot, "bundle_ref"), "engine_build": item(snapshot, "engine_build")}
		record := Object{"format": RecordFormat, "function": selection["function"], "scope": selection["scope"], "mode": "ingest",
			"selection": selection, "inputs_ref": inputsRef, "response_ref": ContentRef(Envelope("application/json", responseBytes)),
			"executor_id": executor, "log_cursor": Int{strconv.Itoa(cursor)}, "stale_mode": lookup(snapshot, "stale_mode", failClosed),
			"stale": staleReason != "", "stale_reason": staleReason}
		ref := ContentRef(Envelope(Media("receipt"), Encode(record)))
		switch {
		case cursor <= len(log):
			if !equal(log[cursor-1], ref) {
				fail("ingest_conflict", "executor "+executor+" has a different receipt at cursor "+strconv.Itoa(cursor), "")
			}
		case cursor == len(log)+1:
			if hasInputs {
				v.PutRaw("application/json", Encode(inputs))
			}
			v.PutRaw("application/json", responseBytes)
			v.Put("receipt", record)
			log = append(log, ref)
			added++
		default:
			fail("ingest_gap", "cursor "+strconv.Itoa(cursor)+" does not follow cursor "+strconv.Itoa(len(log))+" of executor "+executor, "")
		}
		refs = append(refs, ref)
	}
	setExecutorLog(v, executor, log)
	return Object{"executor_id": executor, "log_cursor": Int{strconv.Itoa(len(log))}, "added": Int{strconv.Itoa(added)}, "receipts": refs}
}

func (h *Host) appendEvent(v *View, event Object, key any) Object {
	interval(event)
	targets := item(event, "targets")
	unique := map[any]bool{}
	for _, t := range elements(targets) {
		unique[hashable(t)] = true
	}
	if pyLen(targets) != len(unique) {
		fail("invalid_targets", "duplicate mask target", "")
	}
	known := map[any]Object{}
	var knownOrder []Object
	for _, x := range h.events(v) {
		ref := hashable(x["ref"])
		if _, ok := known[ref]; !ok {
			knownOrder = append(knownOrder, x)
		}
		known[ref] = x
	}
	for _, ref := range elements(targets) {
		old, ok := known[hashable(ref)]
		if !ok || !same(old, event) || !overlaps(old, event) {
			fail("invalid_targets", "target must exist in this scope and overlap", "")
		}
	}
	boundaries := map[string]bool{text(item(event, "from")): true}
	for _, x := range knownOrder {
		old := known[hashable(x["ref"])]
		if same(old, event) && overlaps(old, event) {
			from, until := text(item(event, "from")), text(item(event, "until"))
			if f := text(item(old, "from")); f > from {
				from = f
			}
			if u := text(item(old, "until")); u < until {
				until = u
			}
			boundaries[from], boundaries[until] = true, true
		}
	}
	sorted := make([]string, 0, len(boundaries))
	for b := range boundaries {
		sorted = append(sorted, b)
	}
	sort.Strings(sorted)
	conflicts := map[any]bool{}
	for _, at := range sorted {
		if less(at, item(event, "until")) {
			_, active := h.live(v, item(event, "function"), at)
			for ref := range active {
				conflicts[ref] = true
			}
		}
	}
	for ref := range conflicts {
		if !unique[ref] {
			fail("ambiguous_activation", "explicitly mask all overlapping live events", "")
		}
	}
	if equal(item(event, "kind"), "suspend") && !truthy(targets) {
		fail("invalid_targets", "suspension must name an existing overlapping event", "")
	}
	event["event_id"] = key
	ref := v.Put("event", event)
	v.State["events"] = append(stringList(v.State["events"]), ref)
	object(item(v.State, "outbox"))[ref] = Object{"event_ref": ref, "status": "pending"}
	return Object{"event_ref": ref}
}

func (h *Host) supersedes(v *View, record Object, kind, code, message string) {
	for _, ref := range elements(item(record, "supersedes")) {
		old := h.scoped(v, ref, kind)
		if !same(old, record) || !overlaps(old, record) {
			fail(code, message, "")
		}
	}
}

func appendOnce(v *View, field, ref string) {
	list := stringList(v.State[field])
	if !within(ref, list) {
		v.State[field] = append(list, ref)
	}
}

func pick(record Object, keys ...string) Object {
	out := Object{}
	for _, k := range keys {
		out[k] = item(record, k)
	}
	return out
}

var interval4 = []string{"function", "scope", "from", "until"}

func (h *Host) apply(v *View, op string, q Object) any {
	actor := h.ctx("actor")
	key := q["request_key"]
	state := v.State
	switch op {
	case "source":
		target := h.target(q)
		record := merge(target, Object{"text": item(q, "text"), "supersedes": lookup(q, "supersedes", Array{}), "reason": item(q, "reason"), "attester": actor})
		if !truthy(record["text"]) || !truthy(record["reason"]) {
			fail("invalid_request", "source text/reason required", "")
		}
		h.supersedes(v, record, "source", "invalid_supersession", "source replacement must overlap the same scope/function")
		ref := v.Put("source", record)
		appendOnce(v, "sources", ref)
		return Object{"source_ref": ref}
	case "plan":
		source := h.scoped(v, item(q, "source_ref"), "source")
		cases := item(q, "cases")
		if list, ok := cases.(Array); !ok || len(list) == 0 || !truthy(item(q, "origin")) {
			fail("invalid_plan", "independent nonempty cases and origin required", "")
		}
		record := merge(pick(source, interval4...), Object{"source_ref": item(q, "source_ref"), "cases": cases, "origin": item(q, "origin"),
			"supersedes": lookup(q, "supersedes", Array{}), "installer": actor})
		h.supersedes(v, record, "plan", "invalid_supersession", "case replacement must overlap the same scope/function")
		ref := v.Put("plan", record)
		appendOnce(v, "plans", ref)
		return Object{"plan_ref": ref}
	case "propose":
		target := h.target(q)
		staleMode := lookup(q, "stale_mode", failClosed)
		if !within(staleMode, Array{failClosed, continueStale}) {
			fail("invalid_request", "stale_mode must be fail_closed or continue_stale", "")
		}
		headKey := strings.TrimSpace(string(Encode(Array{target["function"], target["scope"]})))
		heads := object(item(state, "heads"))
		head, ok := heads[headKey]
		if !ok {
			head = ""
		}
		if !equal(head, item(q, "base_snapshot")) {
			fail("stale_proposal", "explicit proposal base is stale", "")
		}
		source := h.scoped(v, item(q, "source_ref"), "source")
		if !same(source, target) || !covers(source, target) {
			fail("source_not_applicable", "source does not cover the candidate", "")
		}
		bundle, isObject := item(q, "bundle").(Object)
		valid := isObject && len(bundle) == len(FileNames)
		for k, raw := range bundle {
			if _, isString := raw.(string); !isString {
				valid = false
			}
			if _, named := FileNames[k]; !named {
				valid = false
			}
		}
		if !valid {
			fail("invalid_bundle", "supply all exact bundle components", "")
		}
		files := map[string][]byte{}
		for k, raw := range bundle {
			files[k] = []byte(raw.(string))
		}
		desc := must(h.Runtime.Execute(files, "describe", nil, nil))
		if !equal(item(desc, "function_id"), target["function"]) {
			fail("contract_changed", "logical function differs", "")
		}
		if truthy(q["base_snapshot"]) {
			base := h.scoped(v, q["base_snapshot"], "snapshot")
			old := must(Decode(h.files(v, base)["manifest"]))
			fresh := must(Decode(files["manifest"]))
			for _, field := range []string{"environment_ref", "caller_variables", "profile", "limits"} {
				if !equal(item(old, field), item(fresh, field)) {
					fail("contract_changed", "this slice permits rule/fixed-data changes only", "")
				}
			}
		}
		refs := Object{}
		for k, raw := range files {
			refs[k] = v.PutRaw(Mimes[k], raw)
		}
		snapshot := merge(target, Object{"base_snapshot": q["base_snapshot"], "source_ref": q["source_ref"], "files": refs,
			"bundle_ref": item(desc, "bundle_ref"), "engine_build": item(desc, "implementation_ref"), "reason": item(q, "reason"), "producer": actor,
			"stale_mode": staleMode})
		ref := v.Put("snapshot", snapshot)
		heads[headKey] = ref
		return Object{"snapshot_ref": ref, "bundle_ref": item(desc, "bundle_ref")}
	case "qualify":
		snapshot := h.scoped(v, item(q, "snapshot_ref"), "snapshot")
		plan := h.scoped(v, item(q, "plan_ref"), "plan")
		applicable := h.sourceValid(v, item(snapshot, "source_ref"), snapshot) && h.planValid(v, q["plan_ref"], snapshot)
		c := candidate(q["snapshot_ref"], snapshot)
		expected := h.Runner.Expected(c)
		var outcome, diagnostic any
		if applicable {
			outcome, diagnostic = h.check(v, snapshot, c, item(plan, "cases"))
		}
		// The Host sets the status from the verdict and the bindings. The
		// runner cannot pass a candidate or a runner that it did not name.
		status := "failed"
		switch {
		case !applicable || truthy(diagnostic):
			status = "unresolved"
		case equal(lookup(outcome, "status", nil), "passed") && equal(lookup(outcome, "bundle_ref", nil), item(snapshot, "bundle_ref")) &&
			equal(lookup(outcome, "implementation_ref", nil), expected):
			status = "passed"
		}
		report := merge(pick(snapshot, interval4...), Object{"snapshot_ref": q["snapshot_ref"], "source_ref": snapshot["source_ref"], "plan_ref": q["plan_ref"],
			"runner": actor, "engine_build": expected, "lifecycle_build": h.BuildRef,
			"status": status, "source_and_plan_applicable": applicable, "outcome": outcome, "diagnostic": diagnostic})
		return Object{"report_ref": v.Put("qualification", report), "report": report}
	case "decide":
		report, snapshot := h.reportValid(v, item(q, "report_ref"))
		if !truthy(item(q, "reason")) {
			fail("invalid_request", "admission reason required", "")
		}
		if equal(actor, item(snapshot, "producer")) || equal(actor, item(report, "runner")) {
			fail("separation_required", "reviewer must differ from producer and runner", "admission")
		}
		record := merge(pick(snapshot, interval4...), Object{"snapshot_ref": item(report, "snapshot_ref"), "report_ref": q["report_ref"], "actor": actor,
			"reason": q["reason"], "disposition": "accept", "runner_policy": h.ctx("runner_actor")})
		return Object{"decision_ref": v.Put("decision", record)}
	case "activate":
		decision := h.scoped(v, item(q, "decision_ref"), "decision")
		report, snapshot := h.reportValid(v, item(decision, "report_ref"))
		if !equal(item(decision, "snapshot_ref"), item(report, "snapshot_ref")) || !equal(item(decision, "disposition"), "accept") ||
			!equal(item(decision, "runner_policy"), h.ctx("runner_actor")) {
			fail("invalid_decision", "decision bindings differ from qualification", "")
		}
		event := merge(pick(snapshot, interval4...), Object{"kind": "enable", "snapshot_ref": report["snapshot_ref"], "report_ref": decision["report_ref"],
			"decision_ref": q["decision_ref"], "targets": lookup(q, "targets", Array{}), "actor": actor})
		return h.appendEvent(v, event, key)
	case "suspend":
		target := h.target(q)
		event := merge(target, Object{"kind": "suspend", "targets": item(q, "targets"), "actor": actor, "reason": item(q, "reason")})
		return h.appendEvent(v, event, key)
	case "load":
		selection := h.resolve(v, item(q, "function"), item(q, "date"))
		snapshot := h.scoped(v, selection["snapshot_ref"], "snapshot")
		h.qualifiedBuild(v, selection)
		actual := must(h.Runtime.Execute(h.files(v, snapshot), "describe", nil, nil))
		if !equal(item(actual, "bundle_ref"), selection["bundle_ref"]) || !equal(item(actual, "implementation_ref"), selection["engine_build"]) {
			fail("artifact_mismatch", "worker loaded a different revision", "runtime")
		}
		loaded := Object{"event_ref": selection["event_ref"], "snapshot_ref": selection["snapshot_ref"], "bundle_ref": item(actual, "bundle_ref"),
			"engine_build": item(actual, "implementation_ref"), "observed_cursor": v.Cursor, "worker": h.ctx("worker")}
		object(item(state, "workers"))[h.ctx("worker")] = loaded
		return loaded
	case "invoke":
		selection, staleReason := h.serving(v, item(q, "function"), item(q, "date"))
		if !equal(selection["bundle_ref"], item(q, "expected_bundle_ref")) {
			fail("artifact_mismatch", "discover the selected bundle before invocation", "")
		}
		loaded := lookup(item(state, "workers"), h.ctx("worker"), Object{})
		if !equal(lookup(loaded, "event_ref", nil), selection["event_ref"]) || !equal(lookup(loaded, "snapshot_ref", nil), selection["snapshot_ref"]) ||
			!equal(lookup(loaded, "engine_build", nil), selection["engine_build"]) {
			fail("revision_not_loaded", "desired revision differs from observed worker load", "runtime")
		}
		return h.receipt(v, selection, Encode(item(q, "inputs")), key, "invoke", "", "", "", staleReason)
	case "replay", "restate":
		old := h.scoped(v, item(q, "receipt_ref"), "receipt")
		if !within(q["receipt_ref"], item(state, "receipts")) {
			fail("artifact_unavailable", "receipt is not retained", "")
		}
		inputs := getRaw(v, item(old, "inputs_ref"), "application/json")
		var selection Object
		if op == "replay" {
			if !equal(item(old, "lifecycle_build"), h.BuildRef) {
				fail("implementation_unavailable", "exact lifecycle build is unavailable", "")
			}
			if raw, ok := q["inputs_json"]; ok {
				s, isString := raw.(string)
				if !isString {
					fail("invalid_request", "inputs_json must be a JSON string", "")
				}
				if s != string(inputs) {
					fail("input_mismatch", "replay requires byte-identical retained inputs", "")
				}
			}
			selection = object(item(old, "selection"))
		} else {
			cursor, ok := item(q, "cursor").(string)
			if !ok || cursor == "" {
				fail("invalid_cursor", "restatement requires a concrete later cursor", "")
			}
			later := must(h.Store.Read(cursor))
			prior := item(item(old, "selection"), "registry_cursor")
			history := must(h.Store.History(later.Cursor))
			if equal(later.Cursor, prior) || !withinStrings(prior, history) {
				fail("invalid_cursor", "restatement needs an explicit later accepted root", "")
			}
			selection = h.resolve(later, item(old, "function"), item(item(old, "selection"), "date"))
		}
		if !truthy(lookup(q, "reason", nil)) {
			fail("invalid_request", "forensic execution reason required", "")
		}
		var original any = ""
		staleReason := ""
		if op == "replay" {
			// A replay runs the old selection again. Thus it keeps the stale
			// reason of the old receipt. A restatement selects a live
			// activation, so it is never stale.
			original = item(old, "response_ref")
			staleReason, _ = lookup(old, "stale_reason", "").(string)
		}
		return h.receipt(v, selection, inputs, key, op, q["receipt_ref"], q["reason"], original, staleReason)
	case "observe":
		receipt := h.scoped(v, item(q, "receipt_ref"), "receipt")
		if !within(item(q, "route"), Array{"source", "artifact", "contract", "test", "runtime"}) {
			fail("invalid_request", "route must name an owner", "")
		}
		record := Object{"function": item(receipt, "function"), "scope": item(receipt, "scope"), "receipt_ref": q["receipt_ref"], "route": q["route"],
			"message": item(q, "message"), "observer": actor, "observation_id": key}
		ref := v.Put("observation", record)
		object(item(state, "tasks"))[ref] = Object{"observation_ref": ref, "function": receipt["function"], "scope": receipt["scope"],
			"status": "open", "checkpoint": "0", "owner": "", "token": "", "note": ""}
		return Object{"observation_ref": ref, "task_id": ref}
	case "claim", "checkpoint", "takeover":
		taskID := hashable(item(q, "task_id"))
		id, isString := taskID.(string)
		var task Object
		if isString {
			task, _ = object(item(state, "tasks"))[id].(Object)
		}
		if !truthy(task) {
			fail("task_unavailable", "unknown correction task", "")
		}
		h.owned(task)
		if !equal(item(task, "checkpoint"), item(q, "checkpoint")) || equal(item(task, "status"), "complete") {
			fail("stale_checkpoint", "task checkpoint is stale or complete", "")
		}
		fence := func(owner any) string {
			return ContentRef(Envelope(Media("fence"), Encode(Array{key, owner, task["checkpoint"]})))
		}
		switch op {
		case "claim":
			if truthy(item(task, "owner")) && !equal(task["owner"], actor) {
				fail("task_claimed", "another owner holds the task", "")
			}
			token := fence(actor)
			task["owner"], task["status"], task["token"] = actor, "working", token
		case "takeover":
			if !truthy(item(q, "reason")) || !truthy(item(q, "owner")) {
				fail("invalid_request", "new owner and reason required", "")
			}
			token := fence(q["owner"])
			task["owner"], task["status"], task["token"], task["note"] = q["owner"], "working", token, q["reason"]
		default:
			if !equal(item(task, "owner"), actor) || !equal(item(task, "token"), item(q, "token")) {
				fail("stale_fence", "task owner/fence changed", "")
			}
			if !within(item(q, "status"), Array{"working", "complete"}) {
				fail("invalid_request", "unknown task status", "")
			}
			if truthy(lookup(q, "correction_snapshot", nil)) {
				h.scoped(v, q["correction_snapshot"], "snapshot")
			}
			task["status"], task["note"], task["correction_snapshot"] = q["status"], item(q, "note"), lookup(q, "correction_snapshot", "")
		}
		checkpoint, err := strconv.Atoi(text(item(task, "checkpoint")))
		if err != nil {
			typeError("invalid literal for int() with base 10: %q", task["checkpoint"])
		}
		task["checkpoint"] = strconv.Itoa(checkpoint + 1)
		return merge(task, Object{"task_id": q["task_id"]})
	case "ack":
		h.scoped(v, item(q, "event_ref"), "event")
		var record Object
		if ref, ok := hashable(q["event_ref"]).(string); ok {
			record, _ = object(item(state, "outbox"))[ref].(Object)
		}
		loaded := lookup(item(state, "workers"), h.ctx("worker"), Object{})
		if !truthy(record) || !equal(lookup(loaded, "event_ref", nil), q["event_ref"]) {
			fail("revision_not_loaded", "ack requires an observed load of this event", "")
		}
		record["status"], record["worker"] = "acknowledged", h.ctx("worker")
		return copyObject(record)
	case "ingest":
		return h.ingest(v, item(q, "entries"))
	}
	fail("invalid_request", "unimplemented operation", "")
	return nil
}

// check runs the qualification cases on the installed runner. A runner fault
// is a diagnostic, not an error of the request.
func (h *Host) check(v *View, snapshot Object, c Candidate, cases any) (outcome, diagnostic any) {
	defer func() {
		if r := recover(); r != nil {
			fault, ok := r.(*Fault)
			if !ok {
				panic(r)
			}
			outcome, diagnostic = nil, fault.Record()
		}
	}()
	verdict := must(h.Runner.Qualify(QualificationInput{Candidate: c, Files: h.files(v, snapshot), Cases: cases}))
	return verdict.Outcome, nil
}
