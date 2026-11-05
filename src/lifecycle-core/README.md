# Go lifecycle core

This module is a Go port of the reference lifecycle Host in `src/cel-lifecycle/`. It is part of milestone M6 ([#88](https://github.com/mohanraj00/entelos/issues/88)). The Python core stays the reference.

## Contents

- `host.go`: the Host. It has the same operations, roles, faults and records as `lifecycle.py`.
- `store.go`: the storage port (`Read`, `Transact`, `History`) and the read view.
- `gitstore.go`: the local Git adapter. It publishes state only through a compare-and-swap of `refs/lifecycle/state`.
- `memstore.go`: an in-memory adapter with the same publication contract. It is not durable.
- `canon.go`: the record codec. It gives the same bytes as the Python codec.
- `runtime.go`: the CEL adapter. It calls the pinned `cel-handoff` executable, the same as the Python core.
- `cmd/lifecycle-core`: the JSONL host. It has the same arguments and wire form as `cli.py`.
- `tools/parity.py`: runs the lifecycle scenario on both cores and compares the traces.

## Dependencies

The module uses only the Go standard library. It does not import `cel-go`, because CEL evaluation stays in the pinned `cel-handoff` executable. Thus this module adds no dependency and no licence. It needs Go 1.27.2 and `git` on the `PATH`.

## Verification

The `go-lifecycle-core` check in `verify.py` does these steps:

1. Build `cmd/lifecycle-core`.
2. Run `tools/parity.py` on `src/cel-lifecycle/fixtures/lifecycle-scenario.json`.
3. Run `go test` with the Git repository that the parity run made.
4. Run `go vet`.

`vectors_test.go` runs each storage port vector in `src/cel-lifecycle/conformance/storage/` on a new Git store and a new in-memory store.

The parity check aliases each content reference and each cursor by its first appearance. Then it compares the two traces and the scenario checks. The snapshot references must also be byte-identical.

## Limits

- Records that hold the lifecycle build (reports, receipts and decisions) have different content references, because the two builds are different.
- A fault code is the same as in Python. A message that comes from a Python exception text can be different.
- The scenario parity runs only on the Git adapter. The in-memory adapter has the storage vectors, store tests and one in-process Host test.
- The build identity includes the test sources of the package.
- Windows is not tested.
