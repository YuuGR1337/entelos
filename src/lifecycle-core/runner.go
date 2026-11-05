package lifecycle

// The qualification runner contract. RUNNER.md is the spec. The Host gives a
// runner the exact candidate and the cases. The runner gives a verdict or a
// structured diagnostic. The Host, not the runner, sets the record status.

// Candidate is the identity of the exact candidate that a runner qualifies.
// The values come from the snapshot record.
type Candidate struct {
	// SnapshotRef is the content reference of the snapshot record.
	SnapshotRef string
	// BundleRef is the bundle identity that the proposal recorded.
	BundleRef string
	// EngineBuild is the engine build that the proposal recorded.
	EngineBuild string
}

// QualificationInput is the input of one qualification.
type QualificationInput struct {
	Candidate Candidate
	// Files holds the exact candidate bytes by component name. The Host
	// reads them from the store by content reference.
	Files map[string][]byte
	// Cases is the case list of the installed plan, as the plan records it.
	Cases any
}

// Verdict is the structured result of a runner. The Host keeps Outcome as
// the "outcome" field of the qualification record. Outcome must be an object
// with these fields:
//
//   - "status": "passed" if all cases passed.
//   - "bundle_ref": the bundle identity that the runner qualified.
//   - "implementation_ref": the runner identity that gave the verdict.
//
// Other fields are runner data. The Host does not read them.
type Verdict struct {
	Outcome any
}

// Runner is a qualification runner.
//
// Identity gives the build identity of the installed runner. It is a content
// reference. Admission refuses a report whose runner identity differs.
//
// Expected gives the runner identity that a report for the candidate binds.
// The Host writes it as the "engine_build" field of the report. A passed
// verdict must name this identity.
//
// Qualify runs the cases on the candidate. If the runner cannot give a
// verdict, it gives a *Fault. The Host records the fault as the structured
// diagnostic of an unresolved report. Another error stops the request.
type Runner interface {
	Identity() (string, error)
	Expected(c Candidate) string
	Qualify(in QualificationInput) (Verdict, error)
}

// CELRunner is the qualification runner for the pinned CEL executable. It
// runs the "check" command of the Runtime.
type CELRunner struct {
	Runtime Runtime
}

// Identity is the build identity of the CEL executable.
func (r *CELRunner) Identity() (string, error) { return r.Runtime.Build() }

// Expected is the engine build of the candidate. A CEL qualification is a
// statement about the engine that described the candidate. If the installed
// engine differs, the check names the other engine, and the report fails.
func (r *CELRunner) Expected(c Candidate) string { return c.EngineBuild }

// Qualify runs the CEL check. A check report with exit 1 is a verdict, not a
// fault. Runtime.Execute owns that rule.
func (r *CELRunner) Qualify(in QualificationInput) (Verdict, error) {
	outcome, err := r.Runtime.Execute(in.Files, "check", nil, in.Cases)
	if err != nil {
		return Verdict{}, err
	}
	return Verdict{Outcome: outcome}, nil
}

// candidate gives the candidate identity of a snapshot record. A value that
// is not a string gives an empty identity.
func candidate(snapshotRef any, snapshot Object) Candidate {
	asString := func(v any) string {
		s, _ := v.(string)
		return s
	}
	return Candidate{SnapshotRef: asString(snapshotRef), BundleRef: asString(lookupAny(snapshot, "bundle_ref")),
		EngineBuild: asString(item(snapshot, "engine_build"))}
}
