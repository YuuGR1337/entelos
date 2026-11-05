# Caller and correction walkthrough

This walkthrough uses the existing neutral record filter, `record.visible`. It goes from the author bundle to a typed call, qualification, activation, correction and pinned replay. It uses only the commands and records that are in this repository. It adds no API.

To run this scenario with one command, use the [quickstart demo](../examples/quickstart/README.md).

The rule is invented: a record is visible when its `state` equals the host's fixed `allowed_state` and `archived` is false. The source is [scope-fixture.json](../src/cel-handoff/testdata/scope-fixture.json). The dated lifecycle values are in [lifecycle-scenario.json](../src/cel-lifecycle/fixtures/lifecycle-scenario.json).

## Who supplies what

| Party | Supplies | Does not supply |
|---|---|---|
| Author | The CEL environment, `rule.cel`, the fixed bindings and a proposal for a scope and a date interval. | Test results, approval or activation. |
| Reviewer | The approved source text, the independent cases, the admission decision, activation and suspension. | The CEL expression. |
| Runner | The qualification report for one exact snapshot and one case plan. | A decision. |
| Worker | The load of the selected revision and the acknowledgement of that load. | A selection. |
| Caller | The function name, a date, the named inputs, the expected bundle reference and a retry key. | The expression, the fixed values or the test expectations. |
| Reader | Replay and restatement of a retained receipt. | New inputs for an old receipt. |
| Host | The context file for each actor: actor, roles, scope and functions. | Nothing from the request. A request cannot grant itself authority. |

In this walkthrough, the files under `$TASK_WORK/walk/*.json` are the host context. A real application must supply authentication and authorization. See [INTEGRATION.md](INTEGRATION.md).

## 1. Build the tools

Use Python, Go and Git. [SUPPORT.md](SUPPORT.md) gives the tested versions. Network access is necessary to get the pinned public modules and the CEL author tools.

Use a clean checkout. Select a new local scratch directory outside the checkout and outside cloud-synced folders. Do not use a directory that contains real data.

```sh
export TASK_WORK="$HOME/scratch/entelos-walk-1"
python3 -B verify.py --work "$TASK_WORK"
```

The last line gives the number of passed and failed checks. A complete run has `"failed": 0`. The runner builds the CEL host at `$TASK_WORK/bin/cel-handoff`, the Go lifecycle host at `$TASK_WORK/bin/lifecycle-core` and the record bundle at `$TASK_WORK/record-bundle`. If the runner fails, read `$TASK_WORK/results.json` and the logs in `$TASK_WORK/runs/`. The runner does not reuse a work directory. For each new run, use a new directory.

Set these names for the subsequent steps:

```sh
RT="$TASK_WORK/bin/cel-handoff"
HOST="$TASK_WORK/bin/lifecycle-core"
BUNDLE="$TASK_WORK/record-bundle"
```

## 2. Read the author bundle

[prepare.py](../src/cel-handoff/tools/prepare.py) writes the bundle from the fixture. The author owns these files:

- `env.json`: the declared variables `state`, `archived` and `allowed_state`.
- `rule.cel`: `state == allowed_state && !archived`.
- `fixed.json`: `{"allowed_state": "ready"}`.
- `manifest.json`: the function, the profile `cel-string-bool/v1`, the caller variables `state` and `archived`, the limits and the content identity of each file.

The host also writes `cases.json` from the fixture `expected_calls`. These cases are host-owned. The caller never reads them.

## 3. Discover the caller schema

```sh
"$RT" -bundle "$BUNDLE" describe
```

The result is the caller contract. It does not contain the expression or the fixed value:

```json
{"function_id":"record.visible","bundle_ref":"sha256:d036…","implementation_ref":"sha256:94ac…","profile":"cel-string-bool/v1",
 "inputs":{"additionalProperties":false,"required":["state","archived"],"type":"object",
  "properties":{"archived":{"type":"boolean"},"state":{"type":"string","x-max-utf8-bytes":256}}},
 "output":{"type":"boolean"},"limits":{"max_request_bytes":4096,"max_string_utf8_bytes":256,"evaluation_cost_limit":1000}}
```

The example output is shortened. `bundle_ref` identifies the exact bundle bytes and the host build. `implementation_ref` identifies the host build. Your hashes can be different, because a different toolchain or platform gives different build bytes. Both values are identities. They are not signatures and they are not authority.

## 4. Make typed calls

Each call sends the named inputs and the exact bundle reference from discovery:

```sh
REF=$("$RT" -bundle "$BUNDLE" describe | python3 -c 'import json,sys; print(json.load(sys.stdin)["bundle_ref"])')
call() { printf '{"bundle_ref":"%s","inputs":%s}\n' "${2:-$REF}" "$1" | "$RT" -bundle "$BUNDLE" run; }
call '{"state":"ready","archived":false}'
call '{"state":"closed","archived":false}'
call '{"state":"ready"}'
call '{"state":"ready","archived":"false"}'
call '{"state":"ready","archived":false,"allowed_state":"closed"}'
call '{"state":"ready","archived":false}' 'sha256:00'
```

The stated expectation for each input comes from the fixture `expected_calls`, not from this output. The observed results agree with it:

| Inputs | Expected | Observed `status` | Value or `diagnostic.field` |
|---|---|---|---|
| `ready`, not archived | visible | `ok` | `true` |
| `closed`, not archived | not visible | `ok` | `false` |
| `archived` absent | refusal | `needs_input` | `/archived` |
| `archived` is the string `"false"` | refusal, no coercion | `invalid_input` | `/archived` |
| caller sends `allowed_state` | refusal, fixed value | `invalid_input` | `/allowed_state` |
| wrong bundle reference | refusal | `artifact_mismatch` | `/bundle_ref` |

A refusal is a structured diagnostic with a `code`, a `stage`, a `field` and a `message`. A refusal is different from a successful `false` result.

The host can also run its own cases. This check does not activate the bundle:

```sh
"$RT" -bundle "$BUNDLE" -cases "$BUNDLE/cases.json" check
```

The result starts with `{"status":"passed"` and lists each case with its actual response.

A Go service can also load a bundle in its own process. It calls `handoff.Load` with the four bundle files, then `Bundle.Run` with the same request bytes. `TestCallerCanDiscoverAndInvokeFixedRule` in [handoff_test.go](../src/cel-handoff/handoff_test.go) covers this path. `Load` refuses a manifest whose `implementation_ref` is not the build identity of the running binary. The refusal is `implementation_unavailable`. Thus a bundle that `prepare.py` wrote for `cel-handoff` does not load in a different binary.

## 5. Run the dated lifecycle

[scenario.py](../src/lifecycle-core/client/scenario.py) sends each operation through the JSONL boundary of the Go lifecycle host [cmd/lifecycle-core](../src/lifecycle-core/cmd/lifecycle-core/main.go). It writes a new local Git store and one context file for each actor:

```sh
LIFE="$TASK_WORK/walk"
python3 -B src/lifecycle-core/client/scenario.py --work "$LIFE" --runtime "$RT" --bundle "$BUNDLE" \
  --fixture src/cel-lifecycle/fixtures/lifecycle-scenario.json --host "$HOST"
```

The output is `{"status": "passed", "checks": 10, "error": null}`. The full trace is in `$LIFE/scenario-results.json`. Print one line for each operation:

```sh
python3 -B -c '
import json, sys
for t in json.load(open(sys.argv[1]))["trace"]:
    d = t["response"].get("data", {})
    r = d.get("result", d) if isinstance(d, dict) else {}
    v = r.get("actual", {}).get("value", "") if isinstance(r, dict) else ""
    print(t["actor"], t["request"]["op"], t["name"], t["response"]["status"], v, sep="\t")
' "$LIFE/scenario-results.json"
```

The fixture states three source tables for the scope `demo`:

| Stage | Interval | `allowed_state` |
|---|---|---|
| initial | 2026-10-01 to 2026-12-01 | `ready` |
| amendment | 2026-11-01 to 2026-12-01 | `reviewed` |
| correction | 2026-11-01 to 2026-12-01 | `approved` |

The amendment transcribed the November state incorrectly. The correction replaces it. The intervals are half-open.

The trace shows these steps. The values come from one run on 2026-10-29.

1. The reviewer records the initial source and its case plan. The author proposes the initial snapshot.
2. Before activation, `describe` for 2026-10-15 gives `out_of_scope`. The author cannot qualify, decide or activate: each attempt gives `forbidden`.
3. The installed runner qualifies the snapshot. The reviewer decides and activates it.
4. A call before the worker load gives `revision_not_loaded`. After the load, the October call for `ready` gives `true`. A fixed-value override in the call gives `invalid_input`.
5. A proposal with the wrong fixed value `closed` fails the independent cases. The reviewer decision gives `qualification_failed`. A report from a runner that the host did not install gives `untrusted_runner`.
6. The amendment is proposed, qualified, decided and activated for November. October still selects the initial snapshot. A read at the captured earlier cursor also selects the initial snapshot for November.
7. The November call for the original input `{"state":"reviewed","archived":false}` gives `true`. This is the receipt that the correction must preserve.
8. An observer reports the discrepancy against that receipt. This opens a correction task. A maintainer claims it. A reviewer gives it to another maintainer, and the earlier owner gets `stale_fence`.
9. The reviewer records the corrected source and plan. The old amendment report becomes `stale_qualification`. October keeps its evidence.
10. The reviewer suspends the incorrect November activation. `describe` for November then gives `out_of_scope`. There is no fallback to an earlier version. The worker still has the amendment loaded. The amendment declares no stale mode, so a November call gives `activation_revoked` and a call for 2026-12-15 gives `activation_expired`.
11. The reviewer activates the correction. A call with the old bundle reference gives `artifact_mismatch`. A call before the new load gives `revision_not_loaded`.
12. After the load, the same November input gives `false`.
13. Replay of the old receipt gives `true` again, with the amendment snapshot. Restatement at the correction cursor gives `false`, with the correction snapshot. A replay with changed inputs gives `input_mismatch`.
14. The correction declares `stale_mode` `continue_stale`. A call for 2026-12-15 is after its `until` date. The loaded correction gives `false`, and the receipt has `stale` `true` and `stale_reason` `expired`.

The fixture `expected` block states the six answers before the run: `true`, `true`, `false`, `true`, `false` and `false`. The scenario checks the observed values against that block.

## 6. Call as a caller

[caller.py](../src/lifecycle-core/client/caller.py) discovers the selected callable, then invokes it. It reads no CEL source, fixed value or case:

```sh
python3 -B src/lifecycle-core/client/caller.py --host "$HOST" --repo "$LIFE/state.git" --context "$LIFE/caller.json" \
  --runtime "$RT" --work "$LIFE" --key walk-november
```

`caller.py` sends two JSON requests to the Go host: `describe`, then `invoke`. [Call a rule from your service](../README.md#call-a-rule-from-your-service) shows the two requests. The `unchanged-caller-client` check in [run.py](../examples/ci-trigger/run.py) runs `caller.py` in each `verify.py` run.

The default date is 2026-11-15 and the default input is `{"state":"reviewed","archived":false}`. The result has `"status":"ok"` and the value `false`, because the correction is active and loaded. The response also contains the new receipt.

The worker in this example has one preparation slot. The last load was for November. An October call therefore gives a refusal, not an answer:

```sh
python3 -B src/lifecycle-core/client/caller.py --host "$HOST" --repo "$LIFE/state.git" --context "$LIFE/caller.json" \
  --runtime "$RT" --work "$LIFE" --key walk-october --date 2026-10-15 --inputs '{"state":"ready","archived":false}'
```

The result is `revision_not_loaded`. A host that serves more than one date at the same time must configure separate preparation slots.

## 7. Replay the original receipt

Read the retained receipt of the November call before the correction:

```sh
cli() { "$HOST" --repo "$LIFE/state.git" --context "$LIFE/$1.json" --runtime "$RT" --work "$LIFE"; }
OLD=$(python3 -B -c 'import json,sys; print(json.load(open(sys.argv[1]))["pins"]["old_receipt"])' "$LIFE/scenario-results.json")
printf '{"op":"receipt","receipt_ref":"%s"}\n' "$OLD" | cli reader
```

The receipt records the function, the scope, the input identity, the engine build and the full selection: date, event, decision, report, snapshot and bundle. Its `selection.snapshot_ref` is the amendment snapshot.

Replay is a write, because it records a new receipt. It needs the current cursor and a retry key:

```sh
REV=$(git --git-dir="$LIFE/state.git" rev-parse refs/lifecycle/state)
printf '{"op":"replay","receipt_ref":"%s","reason":"walkthrough replay","expected_revision":"%s","request_key":"walk-replay"}\n' "$OLD" "$REV" | cli reader
```

The result has `"status":"ok"`, the value `true`, `"mode":"replay"` and `previous_receipt` equal to `$OLD`. The selection is the same amendment snapshot. Replay uses the old input, snapshot and build. It does not resolve the latest selection.

The correction changed the later selection. It did not change the earlier answer. Restatement is the separate operation that computes a new answer for the old input at a named later cursor. The scenario records it as `later-restatement`, with the value `false`. [CONTRACTS.md](CONTRACTS.md) describes replay and restatement.

## Limits

- The callable profile is string and bool inputs with a bool result. Numbers and structured types are not supported.
- The host context files are trusted local files. Another process of the same OS user can change them. This is not an authentication boundary.
- The hashes in this document come from one macOS arm64 run. Compare the structure and the statuses, not the hash values.
- The lifecycle store is a local Git repository for modest histories. APIs and record formats can change. A new version of the code can fail to load an old store.
- This correction is a rule correction: the approved source value changed. A correction to an application fact needs a new input and a new call. See [CONTRACTS.md](CONTRACTS.md).
- The resource limits are configured example values. They are not measured production values.

See [CONTRACTS.md](CONTRACTS.md), [INTEGRATION.md](INTEGRATION.md), [SUPPORT.md](SUPPORT.md) and [SECURITY.md](../SECURITY.md) before you use this code in an application.
