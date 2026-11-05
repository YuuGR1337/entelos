# M3 copy proof: findings

This file records what I needed to write the tier-eligibility example from the docs, for [issue #12](https://github.com/mohanraj00/entelos/issues/12). Each finding gives the need, the gap in the docs and the source that I read. The line numbers are for the base commit `4d8d3bb`. Issue #95 later removed the Python lifecycle files that this record names, for example `lifecycle.py`, `cli.py` and `client.py`. The paths and line numbers refer to the base commit.

## Summary

- `src/` changes: **0**. The example reuses `src/` without a copy and without an edit.
- Docs gaps: **9**. For each gap, I read source to continue.
- The example passes `verify.py`. See [Time, reading and runs](#time-reading-and-runs).

## Method

I started from `CONSTITUTION.md`, `AGENTS.md`, `README.md`, `docs/STATUS.md`, `docs/BACKLOG.md`, `docs/CONTRACTS.md`, `docs/INTEGRATION.md`, `docs/SUPPORT.md`, `docs/WALKTHROUGH.md`, `.agents/skills/decision-workflow/SKILL.md` and `examples/quickstart/README.md`. I read source only when the docs sent me there or when the docs did not answer a question.

The record-filter example has no directory in `examples/`. Its files are `src/cel-handoff/testdata/scope-fixture.json`, `src/cel-lifecycle/fixtures/lifecycle-scenario.json` and `src/cel-lifecycle/scenario.py`. The walkthrough links them, so I read them.

## Docs gaps

1. **No guide to write a new consumer.** I needed the steps to add a new function: author fixture, bundle, lifecycle and caller. The README, `CONTRACTS.md` and `WALKTHROUGH.md` explain the record filter only. No doc says which files a new consumer writes and which files it reuses. I read `examples/ci-trigger/demo.py:11-49` and `examples/ci-trigger/support.py:1-13` to find the pattern: subclass `Driver` and import `src/cel-lifecycle` by path.

2. **The CI example has no README.** The README section "What the examples show" describes it. But `examples/ci-trigger/` has no README that names its entry point, its arguments or its files. I read `examples/ci-trigger/run.py:18-128` to find the entry point and the result format.

3. **`scenario.py` accepts `--fixture`, but it works only for `record.visible`.** `WALKTHROUGH.md` step 5 runs `scenario.py --fixture`. This suggests that another fixture can drive it, but it cannot. `Driver.bundle` writes `allowed_state` (`src/cel-lifecycle/scenario.py:47-53`). `Driver.cases` writes `state` and `archived` cases (`scenario.py:54-58`). `demonstrate` sends record inputs (`scenario.py:77-162`). Only `Driver.call`, `Driver.cursor` and `Driver.check` are generic. I subclassed `Driver` and replaced `bundle`, `source_plan` and `propose`.

4. **The author fixture format is not documented.** `WALKTHROUGH.md` step 2 lists the bundle files, and `CONTRACTS.md` links `prepare.py`. No doc says that `prepare.py --fixture` accepts a new fixture, or which fields it needs. I read `src/cel-handoff/tools/prepare.py:18-49` and `src/cel-handoff/testdata/scope-fixture.json`. The fields that `prepare.py` reads are `function_id`, `environment`, `caller_variables`, `fixed_bindings`, `expression`, `proposed_profile.resource_budgets` and `expected_calls`.

5. **The docs do not say if a fixed value can be a bool.** The docs say "string/bool inputs", and the only example fixed value is a string. I read `src/cel-handoff/handoff.go:151-175`. The host checks each fixed value against its declared type, so a bool is permitted.

6. **The case format and its pass rule are not documented.** I needed to know how to write a refusal case. `WALKTHROUGH.md` step 4 shows statuses and fields, but not the case schema. I read `src/cel-handoff/handoff.go:307-350`. A refusal case must not have `value`. If a case has `field`, the diagnostic field must be the same. Each case name must be unique.

7. **The lifecycle request fields are only in source.** `CONTRACTS.md` says that "the JSON CLI schemas and typed faults are defined in source". I needed the fields of `source`, `plan`, `propose`, `suspend`, `observe`, `claim` and `checkpoint`. I read `src/cel-lifecycle/lifecycle.py:11-33`.

8. **The proposal base rule is not documented.** The docs do not say that `base_snapshot` must be the latest proposal for the function and scope, including a proposal that failed. They also do not say that a later proposal can change only the rule and the fixed values. If you do not obey these rules, the result is `stale_proposal` or `contract_changed`. I read `src/cel-lifecycle/lifecycle.py:208-220` and `scenario.py:97-107`.

9. **A stage bundle needs new content references.** To change a fixed value for a stage, the manifest must name the new `fixed_ref`. No doc says how to calculate it or which media type to use. `INTEGRATION.md` says that the identity hashes the media type, a NUL byte and the exact bytes, but it does not name the media types. I read `src/cel-lifecycle/scenario.py:47-53` and `src/cel-lifecycle/cel_runtime.py:8-9`.

## Other notes

- `verify.py` has no documented place for a new example. I added one `run` call and one `check` call after the existing consumer check. I read `verify.py:82-84` to copy the pattern.
- The author tool `cel-expr eval` takes `-env`, `-expr` and `-tests`. I found the arguments in `examples/ci-trigger/run.py:99-106`. `prepare.py` already writes `upstream-tests.json` in the format that this tool reads.
- `src/cel-lifecycle/client.py` worked unchanged with `--function` and `--inputs`. The README gives the command for the CI function, which was sufficient.
- The profile has no ordered or numeric type. A rule such as "gold tier or better" must list the tier names. This is a limit of the profile, not a docs gap.

## `src/` changes

None. The example passes with the `src/` bytes of the base commit. `verify.py` checks that the source inventory does not change during the run.

## Time, reading and runs

- Start: 2026-10-31 00:50 IST. Reading of the docs and the source above: about 10 minutes.
- The fixtures, `run.py` and the `verify.py` edit took about 5 minutes. They passed on the first run. I did not repair a failed run, and no run failed.
- Development run 1, 00:51 to 01:02 IST: `{"passed": 20, "failed": 0}`. The `tier-eligibility` check had 14 passed checks of its own. This run did not include `README.md` or this file. Those bytes changed after the run, so I did not carry its result onto the final bytes.
- The final run uses the committed bytes. The pull request gives its result.
- I did not build the host and the author tools by hand outside `verify.py`, because the local tool permission blocked the clone of the author tools. `verify.py` builds both.
