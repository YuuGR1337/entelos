# Current work

## Name

The project name is Entelos, with the tagline "Entelos: grow toward the goal." On 2026-10-29, the GitHub repository changed from `mohanraj00/versioned-decision-reference` to `mohanraj00/entelos`. GitHub redirects the old URL. The Go module path and the NOTICE attribution are unchanged. On 2026-10-29, `entelos` was free on PyPI, npm and crates.io, and the .dev and .io domains were free. The GitHub user and the .com, .ai and .org domains were taken. No trademark database was searched.

## Source reference

The source reference contains a primitive typed CEL callable, a local qualified Git lifecycle in Go, record-filter and PR CI examples, and an ordinary-function comparison. APIs and record formats are unstable. See [SUPPORT.md](SUPPORT.md) for the tested environment and operating limits.

## Licence

Project source uses [Apache-2.0](../LICENSE); [NOTICE](../NOTICE) retains project attribution. The [licence decision](LICENSING.md) records the maintainer choice and earlier MIT publication. Runtime/example/dependency bytes are unchanged by that change.

## Shared workflow

Purpose is in [CONSTITUTION.md](../CONSTITUTION.md), workflow in [AGENTS.md](../AGENTS.md). Codex (`AGENTS.md`), Claude Code (`CLAUDE.md`) and agy (`GEMINI.md`) share those files and the same skill source. A different harness reviews each change; see [REVIEW.md](REVIEW.md). `tools/check_project.py` checks that layout, links and source checksums.

The `CI` workflow checks the project structure and source inventory on every change. For code changes only, its `standalone` job runs `verify.py` on two runners, `ubuntu-24.04` and `macos-15`. Each run uploads its compact results and command logs for 14 days. See [SUPPORT.md](SUPPORT.md) for the observed toolchain. Two passing runners do not establish broad platform support.

## Layout

On 2026-10-29, the code moved to `src/` and `examples/`, the boundary test moved to `tests/`, and the loose reference docs moved to `docs/` and `licenses/`. `GEMINI.md` replaced `Agent.md`. Behavior did not change; the full suite must pass on the new paths.

## Next work

Use [BACKLOG.md](BACKLOG.md) and the linked GitHub issues. The next milestone is [M6 Store-neutral core and versioned protocol](https://github.com/mohanraj00/entelos/issues/77). [M5](https://github.com/mohanraj00/entelos/issues/14) stays open only for the quiet share and the public post. Both need specific maintainer authorization.

### M1 First contact

On 2026-10-30, the M1 work was complete in `main` (merge commit `e844246`):

- [WALKTHROUGH.md](WALKTHROUGH.md) gives the typed caller and the correction (issue #3).
- [CORRECTION.md](CORRECTION.md) explains a fact correction and a rule restatement (issue #4).
- `examples/quickstart/demo.py` runs the answer, correction, replay and restatement in one command. `verify.py` compares its results with the fixture and checks the failure evidence (issues #15 and #27).
- The README states the problem first and shows the [decision path diagram](diagrams/decision-path.svg) (issues #16 and #17).

On 2026-10-30, the maintainer dropped the observed first-contact sessions as the M1 exit test. The scope of that test was not clear, and most of it measured whether the docs were clear enough to run the demo. The maintainer ran the demo and found those problems in one session. The reason and the limits are on [issue #10](https://github.com/mohanraj00/entelos/issues/10). These changes replace the test:

- PR #45 (merge `0375158`, issue #44): a clear error and a README line for the work directory.
- PR #47 (merge `c3a2ece`, issue #46): a tools table, numbered steps, a demo output that explains each step, a Go and Git check before the build, and an "After the demo" table.
- A fresh-context adversarial critic read the new README. Its findings are on PR #47.

No engineer who does not know the project ran the demo. Nobody recorded whether the problem matters to an engineer. [FIRST_CONTACT.md](FIRST_CONTACT.md) and the first-contact issue form stay in the repo, but nobody used them.

### M2 Comparison

On 2026-10-30, the M2 work was complete in `main`:

- Five pages in [compare/](compare/): OPA/Rego, Cedar, GoRules ZEN, feature-flag services and ordinary code plus Git (issues #33 to #37). Each page states where the other tool is the better choice.
- [COMPARISON.md](COMPARISON.md) answers the three questions of the critic pass (PR #48, merge `49d5a7e`, issue #38).

The M2 exit test was "the first three questions from the M1 engineers". The three questions of the critic replace them, because the first-contact sessions did not run.

These parts of M2 were not run:

- The Cedar blocks in [compare/cedar.md](compare/cedar.md) are proposed text from the Cedar docs. Nobody parsed, validated or authorized them.
- Unleash, the GoRules BRMS and the OpenFeature SDK are described from their docs only.
- No call cost, speed or effort was measured.

On 2026-10-31, the five critic findings from PR #47 were complete in `main`:

- An example of how a service calls a rule from its own code (issue #50, PR #56).
- A command that prints a receipt from the demo (issue #51, PR #58).
- The first-screen order: the problem now comes before the Limits note (issue #52, PR #56).
- Progress output from `verify.py` on stderr (issue #53, PR #57).
- Plain definitions in "What the examples show" (issue #54, PR #56).

### M3 Copy proof

On 2026-10-31, the M3 work was complete in `main` (PR #59, merge `f14e2f8`):

- [examples/tier-eligibility/](../examples/tier-eligibility/) adds a third neutral example, `feature.bulk_export`. It reuses `src/` without a copy.
- The example needed 0 `src/` changes. `verify.py` runs it as the `tier-eligibility` check, and the full run had 20 passed checks and 0 failed checks.
- [FINDINGS.md](../examples/tier-eligibility/FINDINGS.md) records 9 docs gaps. For each gap, the author read source to continue. Issue #60 owns the docs for these gaps.

These are the limits of the M3 result:

- An agent wrote the example from the docs, not an engineer from outside the project.
- The example uses only the current profile: string/bool inputs and a bool output.

### M4 Contributor path

On 2026-11-03, the M4 exit test was met in `main`:

- PR #67 (merge `1339d71`) adds `CONTRIBUTING.md`, issue templates and a review path for a contributor without an agent harness.
- PR #68 (merge `6043da4`) runs `verify.py` on `macos-15` in CI, beside `ubuntu-24.04`.
- PR #69 (merge `718ca52`, issue #64) is the first pull request from an outside contributor. It merged through the documented path.
- PR #70 (merge `9ee54ed`, issue #63) is a second outside pull request.
- PR #72 (merge `5616680`) adds a manual security review for when Codex and agy are both not available.

These are the limits of the M4 result:

- The outside pull requests changed only docs. No outside contributor changed code.
- Starter issues #62, #65 and #66 are open. PR #71 for issue #65 is open.

### M6 Store-neutral core and versioned protocol

On 2026-11-05, the maintainer approved the M6 scope in issue #77. Decision [0002](decisions/0002-browser-executor.md) adds a browser executor (#93): the Go executor compiled to WASM, for web delivery with no install. The specs and vectors for #78 to #81 are language-neutral.

On 2026-11-05, the maintainer accepted the [technology decision](decisions/0001-technology.md): Go for the core and the sidecar, JSON with RFC 8785 digests on a Unix socket, and a gomobile executor package for native Swift and Kotlin apps.

Issue #79 adds a storage port spec, 11 JSON storage vectors and an in-memory adapter. The Git adapter and the in-memory adapter pass all vectors. The Go tests run them in the `go-lifecycle-core` check of `verify.py`. The vectors do not show durability or performance.

Issue #78 adds the record format version. Each stored record has `format: "entelos.record/1"`. `View.Get` reads only this major version. A record without a format or with an unknown major version fails closed with `unsupported_record_format`. The `record-format` storage vector and a Host test check this rule. The vector set now has 12 files. Stores from v0.1.0 are not migrated, and there is no migration tool.

Issue #80 adds the [qualification runner contract](../src/lifecycle-core/RUNNER.md) to the Go core. CEL is the first runner, and a test-only fixture runner shows that runners plug in.

Issue #84 adds the Go sidecar `entelos-sidecar`: wire protocol 1.0 on a Unix socket with mode 0600. It passes all session vectors through the socket (`sidecar-protocol`), and [examples/sidecar-client](../examples/sidecar-client/README.md) completes propose, qualify, decide, activate and invoke through the socket only (`sidecar-client`). There is no authentication beyond the file mode.

Issue #95 removes the Python lifecycle core: the Python Host, its Git and in-memory stores, its JSONL CLI, the protocol reference adapter and the parity tool. The Go core in `src/lifecycle-core` is now the only Host. `scenario_test.go` runs the lifecycle scenario with the fixture values. Each removed Python test has a Go test or a vector with the same expected values. The examples call the Go JSONL host through the Python callers in `src/lifecycle-core/client/`. `src/cel-lifecycle` keeps the specs, the vectors and the fixtures.

Keep caller walkthrough and input-correction documentation bounded to the existing examples. Numeric/structured profiles, production DBs, authentication gateways and resident-worker coordination are outside the current implemented scope.

## Handoff rule

Record the selected issue, final commit, behavior/checks and unresolved limits. A future worker must be able to identify the next task without reconstructing a conversation. Preserve meaningful failures and mark unmeasured claims as unmeasured.
