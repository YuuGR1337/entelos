# Browser executor: answers away from the Host, then ingest

This example is part of [issue #93](https://github.com/mohanraj00/entelos/issues/93) and [decision 0002](../../docs/decisions/0002-browser-executor.md). A headless Chromium browser runs the WebAssembly executor module. The browser answers from a pinned activation and keeps a receipt log in IndexedDB. Then the Host ingests that log.

```sh
python3 -B examples/browser-executor/run.py --work "$TASK_WORK" --sidecar <entelos-sidecar> \
  --cel-binary <wasm wrapper> --record-bundle <wasm bundle> --module <cel-handoff.wasm> --wasm-exec <GOROOT>/lib/wasm/wasm_exec.js
```

`verify.py` builds these inputs and runs this command as the `browser-executor-ingest` check:

- `entelos-sidecar` from `src/lifecycle-core/cmd/entelos-sidecar`;
- the module `cel-handoff.wasm`, built with `GOOS=js GOARCH=wasm`;
- a wrapper that runs that module under Node as the Host engine;
- the record bundle that `src/cel-handoff/tools/prepare.py` bound to the WebAssembly build;
- `wasm_exec.js` of the Go release that built the module.

Use a new path outside this checkout for `--work`. The directory must not exist. The browser comes from `--browser`, else `ENTELOS_BROWSER`, else a known Chrome or Chromium name. If `ENTELOS_BROWSER_NO_SANDBOX=1`, the browser starts with `--no-sandbox`. Use this only on a runner that refuses the sandbox.

## What the example does

1. It starts four sidecars on one Git store: `author`, `runner`, `reviewer` and `browser`. The `browser` context has the worker `browser-1`. The engine of each sidecar is the WebAssembly module under Node. Thus qualification binds the module build.
2. It runs the sidecar-client loop: propose, qualify, decide and activate.
3. It reads the activation event and the snapshot, and makes the pin from these records.
4. It serves a local page on 127.0.0.1 and starts the browser with a new profile. The page loads the module only if its content reference is the expected one. A module with one changed byte must fail with `artifact_mismatch`. A change to the caller's buffer after the call must have no effect.
5. The page runs the executor vectors, loads the pin and invokes the function:
   - two answers, compared with the fixture and the source table;
   - a date before `from`, which must give `out_of_scope`;
   - the date `until`, which must give `activation_expired`, because the pin is `fail_closed`;
   - two invokes at the same time, which must get cursors 3 and 4;
   - a `load` while one more invocation is pending, which must fail with `invalid_request`. The pending invocation gets cursor 5.
6. The page closes the log, opens it again and compares the entries. The export has no raw inputs.
7. The example computes each `inputs_ref` with its own codec and compares it with the entries.
8. It sends the entries to `ingest` on the `browser` socket two times. The first ingest adds 5 receipts. The second ingest adds 0 and gives the same receipts. Each receipt must name `browser-1`, its cursor, the snapshot and the engine build of the pin.
9. It stops each sidecar with SIGTERM.

The example writes `results.json` to the work directory. The file has each check, the browser record, the page result and the timing.

## Limits

- The receipt log lives in one browser profile. The example does not test storage eviction, private windows or more than one tab.
- A content reference is an identity, not a signature. The application must supply the expected module reference and the pin from a source that it trusts.
- The timing is for one machine and one run. It is not a general speed claim.
