# Changelog

This file lists the changes for each snapshot tag. A tag is a stable snapshot to cite and copy. It is not a package release, and it does not promise a stable API or record format.

## Unreleased

### Reference code

- The Go lifecycle core in `src/lifecycle-core` is now the only lifecycle Host (#95). The Python Host, its stores, its JSONL CLI and the protocol reference adapter are removed.
- `src/cel-lifecycle` keeps the specs, the storage port vectors and the scenario fixture.
- `scenario_test.go` runs the lifecycle scenario in the Go tests. Its expected values come from the fixture.
- `src/lifecycle-core/client/` holds the Python callers of the Go JSONL host: the scenario driver and the generic caller. The examples use them.
- `src/protocol/test_protocol.py` runs the session vectors only through the Go sidecar socket.
- `verify.py` has no parity stage. The `go-lifecycle-core` check runs the Go scenario, the storage port vectors and the Host tests.
- Each stored record has the field `format: "entelos.record/1"` (#78). A read of a record without this field, or with an unknown major version, fails closed with `unsupported_record_format`. The new `record-format` storage vector tests this rule. Stores from v0.1.0 are not migrated. See [SUPPORT.md](docs/SUPPORT.md).
- An expired or revoked activation fails closed by default (#82). The new diagnostics are `activation_expired` and `activation_revoked`. A proposal can declare `stale_mode` `continue_stale`. Then the loaded activation gives the answer, and the receipt has `stale` `true`. Each receipt records `stale_mode`, `stale` and `stale_reason`. See [CONTRACTS.md](docs/CONTRACTS.md#expiry-and-revocation).

## v0.1.0 (2026-11-04)

The first snapshot. It contains the runnable reference and its docs.

### Reference code

- `src/cel-handoff`: a Go host that prepares and evaluates typed CEL rules. Rules accept named string and bool inputs and give a bool result.
- `src/cel-lifecycle`: a Python lifecycle that records each proposal, qualification, approval, dated activation, answer, replay and restatement in a local Git store.
- Three consumer examples that reuse `src/` without copies: `record-filter`, `ci-trigger` and `tier-eligibility`.
- `verify.py`: the full check runner. CI runs it on `ubuntu-24.04` and `macos-15`.

### Docs

- The README states the problem first, gives one demo command and puts the limits at the top.
- [WALKTHROUGH.md](docs/WALKTHROUGH.md), [CORRECTION.md](docs/CORRECTION.md) and the [decision path diagram](docs/diagrams/decision-path.svg).
- [COMPARISON.md](docs/COMPARISON.md) compares OPA/Rego, Cedar, GoRules ZEN, feature-flag services and ordinary code plus Git.
- [CONTRACTS.md](docs/CONTRACTS.md) gives the callable cases and the author fixture fields.
- `CONTRIBUTING.md`, issue templates and a review path for a contributor without an agent harness.

### Security fixes

The M5 security review of the approval and record path found five issues. #74 fixed them.

- An answer now requires the lifecycle build that the qualification report names. Before, only the report check compared the build.
- The reviewer who approves must be different from the producer and the policy runner.
- A replay receipt records `matches_original`, which shows if the new answer is the same as the recorded answer.
- A wrong JSON type in `inputs_json` and very deep JSON nesting now give a fault. Before, they stopped the CLI stream.

### Limits

- The record format has no version field. A later snapshot can change the records, and new code may not load an old store. See [SUPPORT.md](docs/SUPPORT.md).
- APIs are unstable. The only callable profile is string/bool input and bool output.
- Linux and macOS are tested. Windows is not tested.
- The `inspect` and `record` reads do not check the scope. Use one store for each scope.
- A `request_key` with a NUL byte gives `store_error`. The state does not change.
