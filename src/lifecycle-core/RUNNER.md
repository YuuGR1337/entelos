# Qualification runner contract

A qualification runner runs the installed cases on one exact candidate. CEL is the first runner. Issue [#80](https://github.com/mohanraj00/entelos/issues/80) adds this contract. `runner.go` holds the Go types.

## Installation

The embedding host installs the runner with `NewHostWithRunner`, the same as the roles. A request cannot select a runner. `NewHost` installs `CELRunner`, which runs the `check` command of the pinned CEL executable.

## Input

`QualificationInput` has these fields:

- `Candidate`: the snapshot reference, the bundle identity and the engine build that the proposal recorded.
- `Files`: the exact candidate bytes. The Host reads them from the store by content reference.
- `Cases`: the case list of the installed plan.

The runner gets only this input. It does not get the store.

## Verdict

`Verdict.Outcome` is an object with these fields:

- `status`: `passed` if all cases passed.
- `bundle_ref`: the bundle identity that the runner qualified.
- `implementation_ref`: the runner identity that gave the verdict.

Other fields are runner data. The Host keeps the outcome as the `outcome` field of the qualification record.

The Host sets the record status, not the runner. The status is `passed` only if all of these are true:

1. The outcome status is `passed`.
2. The outcome `bundle_ref` equals the snapshot bundle identity.
3. The outcome `implementation_ref` equals the expected runner identity.

Else the status is `failed`.

## Structured diagnostics

If the runner cannot give a verdict, it gives a `*Fault` with a code, a stage and a message. The Host records the fault as the `diagnostic` field and sets the status to `unresolved`. A missing candidate file is also a diagnostic. Another error stops the request, and the Host publishes no state.

## Runner identity

- `Identity()` gives the build identity of the installed runner. It is a content reference.
- `Expected(candidate)` gives the runner identity that a report for the candidate binds. The Host writes it as the `engine_build` field of the report.

For CEL, `Identity()` is the `-build` output of the executable, and `Expected` is the engine build of the snapshot. Thus the CEL records are the same as before this contract. A different runner identity gives a different report record, and thus a different report reference.

## Admission

`decide` and `activate` check the report again. The checks are in this order:

1. The report status must be `passed`. Else the fault is `qualification_failed`.
2. The report runner must be the installed runner actor. Else the fault is `untrusted_runner`.
3. The report lifecycle build must be the installed build, and the report `engine_build` must equal `Identity()`. Else the fault is `implementation_unavailable`.
4. The outcome `bundle_ref` must equal the snapshot bundle identity. The outcome `implementation_ref` and `Expected(candidate)` must equal the report `engine_build`. Else the fault is `qualification_failed`.
5. The source and the plan must not be superseded. Else the fault is `stale_qualification`.

Step 4 makes sure that a passed flag never moves to other bytes or to another runner. A changed candidate is a new snapshot, and it needs a new qualification.

## Evidence

`runner_test.go` has a fixture runner. The fixture runner reads its verdicts from a table and runs no rule. The tests show these results:

- The fixture runner completes the path from `source` to `invoke`.
- A fixture verdict `failed` gives a failed report. A missing verdict gives an unresolved report with a diagnostic. Admission refuses both.
- A runner status `passed` for another bundle gives a failed report.
- Two runner identities give two report references. Each report is admissible only with its own runner.
- If the installed runner identity changes, `decide` and `activate` refuse the old report with `implementation_unavailable`.
- If a passed report is copied onto a new snapshot with other bytes, `decide` refuses it with `qualification_failed`.

The `go-lifecycle-core` parity check shows that the CEL path gives the same trace as the Python core.

## Limits

- The fixture runner is a test runner. This contract adds no new production runner.
- The candidate is still a CEL bundle. `propose`, `load` and `invoke` use the CEL runtime.
- The candidate identity fields must be strings. A snapshot that holds another JSON type gives an empty identity.
- The Python core has no runner interface. It stays a reference until issue #95 removes it.
