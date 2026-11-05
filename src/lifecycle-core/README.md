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
- `protocol/`: the wire protocol 1.0 of [PROTOCOL.md](../protocol/PROTOCOL.md) in front of the Host: frames, the RFC 8785 encoder, the schema validator and the Unix socket listener.
- `cmd/entelos-sidecar`: the local sidecar ([#84](https://github.com/mohanraj00/entelos/issues/84)). It serves the protocol on a Unix domain socket.
- `tools/parity.py`: runs the lifecycle scenario on both cores and compares the traces.

## Dependencies

The module uses only the Go standard library. It does not import `cel-go`, because CEL evaluation stays in the pinned `cel-handoff` executable. Thus this module adds no dependency and no licence. It needs Go 1.27.2 and `git` on the `PATH`.

## Verification

The `go-lifecycle-core` check in `verify.py` does these steps:

1. Build `cmd/lifecycle-core`.
2. Run `tools/parity.py` on `src/cel-lifecycle/fixtures/lifecycle-scenario.json`.
3. Run `go test` and `go vet`, also if parity failed. The Host tests in `host_test.go` start from a copy of the Git repository that step 2 made with the Go host. `LIFECYCLE_GO_BASELINE` names that run's `parity.json`. The Host tests use the repository only if parity passed, which shows that the Go trace equals the Python trace. If parity failed or did not run, the Host tests fail.

`vectors_test.go` runs each storage port vector in `src/cel-lifecycle/conformance/storage/` on a new Git store and a new in-memory store.

The `go test` step also runs the `protocol/` tests:

- `protocol/vectors_test.go` runs each file in `src/protocol/vectors/` in the process: the session steps on a new Git store, with short reads and short writes, the RFC 8785 cases and the envelope schema cases.
- `protocol/adapter_test.go` uses stub Hosts. A panic, an error that is not a `Fault`, a float, an integer outside ±(2^53 − 1) and a Go type outside the value domain each give one `internal_error` frame with no error text. A result over the frame bound gives `response_too_large`.
- `protocol/socket_test.go` tests the socket rules below.

The `sidecar-protocol` check in `verify.py` builds `cmd/entelos-sidecar`. Then it runs `src/protocol/test_protocol.py --sidecar`, which runs each session vector file through the socket of a new sidecar process. The `sidecar-client` check runs [examples/sidecar-client](../../examples/sidecar-client/README.md).

## Sidecar

```sh
entelos-sidecar --socket <path> --repo <store.git> --context <context.json> --runtime <cel-handoff> --work <dir> --schema src/protocol/schema
```

- The host context comes from the `--context` file. A request cannot supply the actor, the roles or the scope.
- The sidecar reads the schemas from `--schema` at start. If an operation schema does not agree with the Host read operations, the sidecar does not start.
- The socket file has mode 0600. The umask is 0177 while the sidecar makes the socket, so the mode is 0600 from the start.
- If the socket path exists, the sidecar removes it only if it is a stale socket: a socket file with no listener. If the path is another file type or a socket with a listener, the sidecar does not start.
- The sidecar holds an exclusive lock on `<path>.lock` (mode 0600) during the stale check, the removal and the bind. Two sidecars that start at the same path cannot remove each other's live socket. The lock file stays.
- The socket path has a maximum of 103 bytes, the `sun_path` limit of macOS. For a longer directory, start the sidecar in the socket directory and give a relative path.
- When the socket listens, the sidecar writes one line to stdout: `{"socket":"<path>","status":"listening"}`.
- Each connection has its own goroutine. The Host gets one request at a time.
- On SIGINT or SIGTERM, the sidecar closes the open connections, removes the socket file and exits with status 0.

The parity check aliases each content reference and each cursor by its first appearance. Then it compares the two traces and the scenario checks. The snapshot references must also be byte-identical.

## Build identity

The lifecycle build identity is a content reference over the core `*.go` sources, the Go version and the `git --version` text. It does not include the `*_test.go` files. The Python identity also includes only its core sources. It does not include `protocol/` or `cmd/`, so the sidecar adapter is not part of the lifecycle build identity.

## Limits

- Records that hold the lifecycle build (reports, receipts and decisions) have different content references, because the two builds are different.
- A fault code is the same as in Python. A message that comes from a Python exception text can be different.
- The scenario parity runs only on the Git adapter. The in-memory adapter has the storage vectors, store tests and one in-process Host test.
- Windows is not tested. The sidecar is POSIX only.
- The sidecar has no authentication. The file mode of the socket is the only access control. Some BSD systems do not check the mode of a socket file, so put the socket in a directory with mode 0700.
- The socket vectors cannot make the sidecar do short writes. The in-process Go tests cover short reads and short writes.
