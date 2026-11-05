# Sidecar client: the lifecycle through a local socket

This example is a consumer that talks to the Go sidecar only. It is part of [issue #84](https://github.com/mohanraj00/entelos/issues/84). The client imports no lifecycle code. It sends wire protocol 1.0 frames ([PROTOCOL.md](../../src/protocol/PROTOCOL.md)) on Unix domain sockets.

```sh
python3 -B examples/sidecar-client/run.py --work "$TASK_WORK" --sidecar <entelos-sidecar> --cel-binary <cel-handoff> --record-bundle <bundle>
```

`verify.py` builds the three inputs and runs this command as the `sidecar-client` check:

- `entelos-sidecar` from `src/lifecycle-core/cmd/entelos-sidecar`;
- the pinned `cel-handoff` from `src/cel-handoff`;
- the record bundle from `src/cel-handoff/tools/prepare.py`.

Use a new path outside this checkout for `--work`. The directory must not exist.

## What the example does

1. It starts three sidecars on one Git store. Each sidecar has one trusted host context: `author`, `runner` and `reviewer`. The sidecar reads the context from a file. A request cannot change it.
2. It puts the sockets in a directory with mode 0700. It checks that each socket file has mode 0600.
3. It sends `source` and `plan` to the reviewer socket. The plan has hand-written cases from the invented source table.
4. It sends `propose` to the author socket. The bundle is the prepared record bundle with the fixed `allowed_state` of the `initial` table of `src/cel-lifecycle/fixtures/lifecycle-scenario.json`.
5. It sends `qualify` to the runner socket. The report must have the status `passed`.
6. It sends `decide` to the author socket. The response must be `forbidden` at the `authority` stage, because the author context has no reviewer role.
7. It sends `decide`, `activate`, `describe`, `load` and `invoke` to the reviewer socket.
8. It compares the answer with `expected.initial_october_ready` of the fixture. The receipt must name the proposed snapshot and the activation event.
9. It stops each sidecar with SIGTERM. Each sidecar must exit with status 0 and remove its socket file.

The example writes `results.json` to the work directory. The file has each check, each request and each response.

## Limits

- Local sockets only. The only access control is the file mode of the socket and of its directory. There is no authentication.
- The example runs one path with one invented rule. It does not show a correction, a replay or concurrent clients.
- The socket path is relative to the socket directory, because a full path can be longer than the `sun_path` limit (104 bytes on macOS).
- POSIX only. Windows is not tested.
