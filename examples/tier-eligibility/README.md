# Tier eligibility: a third consumer

This example is the third neutral consumer of `src/`. It answers one invented question: may a customer use the bulk export feature on a given date? An engineer who did not write the reference wrote it from the docs, for [issue #12](https://github.com/mohanraj00/entelos/issues/12) (milestone M3). [FINDINGS.md](FINDINGS.md) lists each place where the docs were not sufficient.

The example imports the Python callers in `src/lifecycle-core/client` and calls `src/cel-handoff/tools/prepare.py`. It does not copy them. It changes no file in `src/`.

## The rule

The function is `feature.bulk_export`. The callable profile is `cel-string-bool/v1`: string and bool inputs, and a bool result.

| Name | Type | Supplied by |
|---|---|---|
| `tier` | string | the caller |
| `trial` | bool | the caller |
| `silver_allowed` | bool | the host (fixed value) |
| `trial_allowed` | bool | the host (fixed value) |

```cel
(tier == "platinum" || tier == "gold" || (tier == "silver" && silver_allowed)) && (trial_allowed || !trial)
```

The caller cannot send `silver_allowed` or `trial_allowed`. If the caller sends one, the host gives `invalid_input`.

## The dated tables

The scope is `tier-demo`. The intervals are half-open.

| Stage | Interval | `silver_allowed` | `trial_allowed` |
|---|---|---|---|
| initial | 2026-10-01 to 2026-12-01 | false | false |
| amendment | 2026-11-01 to 2026-12-01 | true | true |
| correction | 2026-11-01 to 2026-12-01 | true | false |

The amendment opens the feature to silver customers. It also allows trial accounts by mistake. The correction keeps silver and removes the trial access.

## Run it

`verify.py` runs this example as the `tier-eligibility` check. To run it alone, use the binaries from a complete `verify.py` run:

```sh
python3 -B examples/tier-eligibility/run.py --work "$(mktemp -d)/tier" \
  --cel-binary "$TASK_WORK/bin/cel-handoff" --author-binary "$TASK_WORK/bin/cel-expr" \
  --host "$TASK_WORK/bin/lifecycle-core"
```

The `--work` directory must not exist. The example writes the bundle, the Git store, `results.json` and `commands.json` to it. The last line gives `"status": "passed"` or `"status": "failed"`.

## What the example does

1. It writes the author bundle with the unchanged `prepare.py` and [fixtures/callable.json](fixtures/callable.json).
2. It runs the pinned upstream `cel-expr eval` on the rule and the stated `ok` cases.
3. It runs the host `describe` and `check` commands. The cases include three refusals. A call with another bundle reference gives `artifact_mismatch`.
4. It runs the dated lifecycle from [fixtures/lifecycle.json](fixtures/lifecycle.json) through the unchanged JSONL boundary of the Go lifecycle host `src/lifecycle-core/cmd/lifecycle-core`. `run.py` subclasses `Driver` from `src/lifecycle-core/client/lifecycle_client.py`.
5. It calls the new function with the unchanged `src/lifecycle-core/client/caller.py`.

The lifecycle steps:

1. The reviewer records the initial table and its cases. The author cannot qualify its own proposal (`forbidden`).
2. The runner qualifies the initial snapshot. The reviewer decides and activates it.
3. A call before the worker load gives `revision_not_loaded`. After the load, a paid gold customer in October gives `true`. A call that sends `trial_allowed` gives `invalid_input`.
4. A stale artifact keeps the initial fixed values for November. It fails the November cases, and the decision gives `qualification_failed`.
5. The amendment is qualified and activated for November. October and the earlier cursor still select the initial snapshot.
6. A silver trial account on 2026-11-15 gives `true`. This receipt is the answer that the correction must keep.
7. An observer reports the error. The reviewer records the corrected table. The old amendment report gives `stale_qualification`.
8. The reviewer suspends the amendment. Discovery for November gives `out_of_scope`, with no fallback.
9. The reviewer activates the correction. A call with the old bundle reference gives `artifact_mismatch`.
10. After the load, the same input gives `false`.
11. Replay of the old receipt gives `true` on the amendment snapshot. Restatement at the correction cursor gives `false` on the correction snapshot. A replay with changed inputs gives `input_mismatch`.

## Expected values

The cases and the `expected` block in the fixtures are written by hand from each table. They do not come from a run. The example compares each observed value with them.

| Question | Expected |
|---|---|
| Gold, paid, 2026-10-15 | `true` |
| Silver, trial, 2026-11-15, before the correction | `true` |
| Silver, trial, 2026-11-15, after the correction | `false` |
| Replay of the earlier receipt | `true` |
| Restatement at the correction cursor | `false` |

## If it fails

The example exits with status 1 and writes `results.json` with `"status": "failed"` and the `errors` list. `results.json` keeps the partial lifecycle trace. `commands.json` keeps each command, with its exit code, stdout and stderr. Keep the work directory of a failed run.

## Limits

- The tiers, the feature and the tables are invented. There is no customer data.
- Tiers are compared as strings. The profile has no numbers or ordered types, so "gold or better" is a list of names in the rule.
- The host context files are trusted local files. This is not an authentication boundary.
- The store is a local Git repository for a small history. The worker has one preparation slot.
