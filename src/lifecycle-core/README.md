# Go lifecycle core

This module is the lifecycle core. It is the only lifecycle Host. The specs, the conformance vectors and the fixtures are in `src/cel-lifecycle/`. Milestone M6 ([#88](https://github.com/mohanraj00/entelos/issues/88)) moved the core from Python to Go. Issue [#95](https://github.com/mohanraj00/entelos/issues/95) removed the Python core.

## Contents

- `host.go`: the Host: the operations, roles, faults and records.
- `store.go`: the storage port (`Read`, `Transact`, `History`) and the read view. `View.Put` writes the record format `entelos.record/1`. `View.Get` is the decode path of each record read, and it fails closed on an unknown record format.
- `gitstore.go`: the local Git adapter. It publishes state only through a compare-and-swap of `refs/lifecycle/state`.
- `memstore.go`: an in-memory adapter with the same publication contract. It is not durable.
- `canon.go`: the record codec. Content references depend on its bytes.
- `runtime.go`: the CEL adapter. It calls the pinned `cel-handoff` executable.
- `runner.go`: the qualification runner contract. [RUNNER.md](RUNNER.md) is the spec. `CELRunner` is the first runner.
- `cmd/lifecycle-core`: the JSONL host. It reads one request record on each stdin line and writes one response record on each stdout line. The arguments are `--repo`, `--context`, `--runtime` and `--work`.
- `protocol/`: the wire protocol 1.0 of [PROTOCOL.md](../protocol/PROTOCOL.md) in front of the Host: frames, the RFC 8785 encoder, the schema validator and the Unix socket listener.
- `cmd/entelos-sidecar`: the local sidecar ([#84](https://github.com/mohanraj00/entelos/issues/84)). It serves the protocol on a Unix domain socket.
- `client/`: Python callers of the JSONL host. They contain no Host and no store. `lifecycle_client.py` encodes the requests and starts the host for each request. `scenario.py` runs the neutral lifecycle scenario. `caller.py` discovers a callable, then invokes it. The examples use these files.

## Dependencies

The module uses only the Go standard library. It does not import `cel-go`, because CEL evaluation stays in the pinned `cel-handoff` executable. Thus this module adds no dependency and no licence. It needs Go 1.27.2 and `git` on the `PATH`.

## Verification

The `go-lifecycle-core` check in `verify.py` does these steps:

1. Build `cmd/lifecycle-core`.
2. Run `go test` and `go vet`.

`scenario_test.go` runs the neutral lifecycle scenario on a new Git store, with the pinned CEL executable. It compares each answer, replay and restatement with the `expected` field of `src/cel-lifecycle/fixtures/lifecycle-scenario.json`. `LIFECYCLE_CEL_BINARY` names the CEL executable and `LIFECYCLE_CEL_BUNDLE` names the prepared record bundle. If these are not set, the scenario and the Host tests skip. `verify.py` also sets `LIFECYCLE_REQUIRE_SCENARIO`, so that a skip is a failure. `LIFECYCLE_SCENARIO_RESULTS` names a file for the checks, the pins and the trace of the scenario.

The Host tests in `host_test.go` start from a copy of the Git repository that the scenario made. The pins name the records of that scenario. `TestUnknownRecordFormatFailsClosed` plants receipts with an unknown, a missing and a malformed format. The `receipt`, `record` and `replay` operations must each give a structured diagnostic and publish no state. `schema_test.go` checks that each schema file in `src/protocol/schema/` agrees with the Host table.

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
- While the sidecar starts, it holds an exclusive lock on `<path>.lock` for the stale check, the removal and the bind. It releases the lock when the socket listens. Thus two sidecars that start at the same time at one stale socket cannot both remove it. The lock file stays, with mode 0600. If the lock path is not a regular file that the current user owns and can read and write, or another process holds the lock for 10 seconds, the sidecar does not start.
- The socket path has a maximum of 103 bytes, the `sun_path` limit of macOS. For a longer directory, start the sidecar in the socket directory and give a relative path.
- When the socket listens, the sidecar writes one line to stdout: `{"socket":"<path>","status":"listening"}`.
- Each connection has its own goroutine. The Host gets one request at a time.
- On SIGINT or SIGTERM, the sidecar closes the open connections, removes the socket file and exits with status 0.

## Build identity

The lifecycle build identity is a content reference over the core `*.go` sources, the Go version and the `git --version` text. It does not include the `*_test.go` files. It does not include `protocol/` or `cmd/`, so the sidecar adapter is not part of the lifecycle build identity.

## Limits

- Records that hold the lifecycle build (reports, receipts and decisions) get new content references when a core source changes.
- The scenario runs only on the Git adapter. The in-memory adapter has the storage vectors, store tests and one in-process Host test.
- Windows is not tested. The sidecar is POSIX only.
- The sidecar has no authentication. The file mode of the socket is the only access control. Some BSD systems do not check the mode of a socket file, so put the socket in a directory with mode 0700.
- The socket vectors cannot make the sidecar do short writes. The in-process Go tests cover short reads and short writes.
