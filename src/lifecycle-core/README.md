# Go lifecycle core

This module is a Go port of the reference lifecycle Host in `src/cel-lifecycle/`. It is part of milestone M6 ([#88](https://github.com/mohanraj00/entelos/issues/88)). The Python core stays the reference.

## Contents

- `host.go`: the Host. It has the same operations, roles, faults and records as `lifecycle.py`.
- `store.go`: the storage port (`Read`, `Transact`, `History`) and the read view.
- `gitstore.go`: the local Git adapter. It publishes state only through a compare-and-swap of `refs/lifecycle/state`.
- `memstore.go`: an in-memory adapter with the same publication contract. It is not durable.
- `canon.go`: the record codec. It gives the same bytes as the Python codec.
- `runtime.go`: the CEL adapter. It calls the pinned `cel-handoff` executable, the same as the Python core.
- `runner.go`: the qualification runner contract. [RUNNER.md](RUNNER.md) is the spec. `CELRunner` is the first runner.
- `cmd/lifecycle-core`: the JSONL host. It has the same arguments and wire form as `cli.py`.
- `tools/parity.py`: runs the lifecycle scenario on both cores and compares the traces.

## Dependencies

The module uses only the Go standard library. It does not import `cel-go`, because CEL evaluation stays in the pinned `cel-handoff` executable. Thus this module adds no dependency and no licence. It needs Go 1.27.2 and `git` on the `PATH`.

## Verification

The `go-lifecycle-core` check in `verify.py` does these steps:

1. Build `cmd/lifecycle-core`.
2. Run `tools/parity.py` on `src/cel-lifecycle/fixtures/lifecycle-scenario.json`.
3. Run `go test` and `go vet`, also if parity failed. The Host tests in `host_test.go` start from a copy of the Git repository that step 2 made with the Go host. `LIFECYCLE_GO_BASELINE` names that run's `parity.json`. The Host tests use the repository only if parity passed, which shows that the Go trace equals the Python trace. If parity failed or did not run, the Host tests fail.

`vectors_test.go` runs each storage port vector in `src/cel-lifecycle/conformance/storage/` on a new Git store and a new in-memory store.

The parity check aliases each content reference and each cursor by its first appearance. Then it compares the two traces and the scenario checks. The snapshot references must also be byte-identical.

## Build identity

The lifecycle build identity is a content reference over the core `*.go` sources, the Go version and the `git --version` text. It does not include the `*_test.go` files. The Python identity also includes only its core sources.

## Limits

- Records that hold the lifecycle build (reports, receipts and decisions) have different content references, because the two builds are different.
- A fault code is the same as in Python. A message that comes from a Python exception text can be different.
- The scenario parity runs only on the Git adapter. The in-memory adapter has the storage vectors, store tests and one in-process Host test.
- Windows is not tested.
