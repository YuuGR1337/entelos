# Quickstart: the record-filter loop

This demo runs the complete record-filter loop with one command. It needs Go, Python 3, Git and network access (see [SUPPORT.md](../../docs/SUPPORT.md)). Each run downloads the pinned Go modules into a new module cache in the work directory. Thus, a run without network access fails at `build-host`.

```sh
python3 -B examples/quickstart/demo.py --work "$TASK_WORK"
```

Use a new path outside this checkout for `--work`. The directory must not exist. The demo makes it, and it does not overwrite an earlier run. The demo does not write to the checkout. It writes the build, the Go caches and the Git lifecycle store to the work directory.

## What the demo does

1. It builds the `cel-handoff` host with the same pinned flags and isolated settings as `verify.py`.
2. It prepares the record bundle with `src/cel-handoff/tools/prepare.py`.
3. It builds the Go lifecycle host `src/lifecycle-core/cmd/lifecycle-core` to `<work>/bin/lifecycle-core`.
4. It runs the unchanged lifecycle scenario `src/lifecycle-core/client/scenario.py` on the fixture `src/cel-lifecycle/fixtures/lifecycle-scenario.json`. The scenario sends each request to the Go host.
5. It prints a short summary and writes `summary.json` to the work directory.

## What the summary shows

- The build identity of the host.
- The answer: on 2026-11-15, the record `{"state":"reviewed","archived":false}` is visible. The answer has a receipt.
- The rule change: the allowed state changes from `reviewed` to `approved`. A reviewer activates it as a new event. The `correction` field of `summary.json` holds this change.
- The replay: the earlier receipt, on its pinned snapshot, gives the same answer again.
- The restatement: the same input under the rule change gives a new, separate answer.

The expected values come from the `expected` field of the fixture, not from the demo output. The full trace is in `<work>/loop/scenario-results.json`.

## Print a receipt

If the demo passes, it prints one command that prints the receipt of the first answer. The `receipt_command` field of `summary.json` holds the same command. The command has this form:

```sh
printf '%s\n' '{"op":"receipt","receipt_ref":"<receipt_ref>"}' | <work>/bin/lifecycle-core --repo <work>/loop/state.git --context <work>/loop/reader.json --runtime <work>/bin/cel-handoff --work <work>/loop | python3 -m json.tool
```

The demo fills in the full receipt reference and the absolute paths. The command sends the `receipt` read operation to the Go lifecycle host, with the reader context of the demo. It reads the Git store and does not change it.

The receipt contains the function, the scope, the date, the engine build and the references of the input, the response, the rule version (`selection.snapshot_ref`) and the approval. The input and the response are separate records in the store. [WALKTHROUGH.md](../../docs/WALKTHROUGH.md) shows how to replay this receipt.

## If the demo fails

The demo prints `Result: failed` and exits with status 1. If a step before the loop fails, the demo also writes the step output to stderr. It always writes `<work>/summary.json` with the reason:

- `failed_step`, `stdout` and `stderr`: a step before the loop failed, for example `build-host`.
- `loop_error` and `loop_output`: the loop failed. `loop_output` keeps the exit code, stdout and stderr of `scenario.py`.
- `errors`: the results file is missing or not valid JSON, or a step or field is missing from the trace.
- `failed_checks`: a value or snapshot does not agree with the fixture. The demo prints each name as `Failed comparison`.

Keep the work directory of a failed run. The next run needs a new work directory.

## Limits

The demo shows one invented example on a local Git store. It does not show authentication, a production database or performance. The `quickstart-failures` check in `verify.py` runs `test_demo.py`, which gives invented failed results to the summary path. `test_demo.py` also runs the demo one time in its own work directory, runs the printed receipt command and compares the output with the receipt in the trace. The `quickstart` check runs this demo and compares its build identity and answers with the verified build and the fixture.
