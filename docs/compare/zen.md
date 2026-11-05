# Comparison with GoRules ZEN

This page compares the [GoRules ZEN Engine](https://github.com/gorules/zen) and the commercial GoRules BRMS with the Entelos decision path. It uses the `record.visible` record filter from the [quickstart demo](../../examples/quickstart/README.md). It writes the same rule, the correction and the replay question as ZEN decision graphs. It covers decision graphs, versions and the simulator.

## Version and sources

- ZEN Engine: the Python binding `zen-engine` 2.1.3 from PyPI, uploaded on 2026-10-09. I ran this version. The Rust crate release [zen-engine-v2.1.6](https://github.com/gorules/zen/releases/tag/zen-engine-v2.1.6) is from 2026-10-21. The engine is MIT-licensed.
- GoRules BRMS: I did not run it. The BRMS is self-hosted and needs a licence key. The rows for versions, releases, requests and the simulator come from the docs only.
- I read these primary docs on 2026-10-30. The pages show no version number and no date. Thus, they can change after that date.
  - [ZEN repository README](https://github.com/gorules/zen)
  - [JDM format](https://docs.gorules.io/learn/jdm-format/standard)
  - [Open source and BRMS](https://docs.gorules.io/developers/overview/bre-vs-brms)
  - [Decision graphs](https://docs.gorules.io/brms/build/graphs)
  - [Simulator](https://docs.gorules.io/docs/simulator) and the [simulate API](https://docs.gorules.io/api-reference/brms/decision/simulate-decision.md)
  - [Testing](https://docs.gorules.io/brms/build/testing.md)
  - [Branches and history](https://docs.gorules.io/brms/review/branches.md)
  - [Requests](https://docs.gorules.io/brms/review/requests.md) and [project settings](https://docs.gorules.io/brms/administration/project-settings.md)
  - [Releases](https://docs.gorules.io/brms/deploy/releases.md) and [environments](https://docs.gorules.io/reference/environments)
  - [Audit logs](https://docs.gorules.io/brms/administration/audit-logs.md)
  - [BRMS deployment](https://docs.gorules.io/developers/deployment/brms/overview)
- Entelos: the `main` branch at merge commit `784e0dc`. The scenario is [lifecycle-scenario.json](../../src/cel-lifecycle/fixtures/lifecycle-scenario.json). [CORRECTION.md](../CORRECTION.md) explains replay and restatement.

## The rule as a ZEN decision graph

A ZEN decision is a JSON Decision Model (JDM) graph. The graph for `record.visible` has three nodes: an input node, one decision table and an output node. The table uses the `first` hit policy. Each row has three input columns, `date`, `state` and `archived`, and one output column, `visible`. The last row has empty cells, so it matches all inputs and gives `false`.

The two rows for the amendment revision:

| `date` | `state` | `archived` | `visible` |
|---|---|---|---|
| `d($) >= d("2026-10-01") and d($) < d("2026-11-01")` | `"ready"` | `false` | `true` |
| `d($) >= d("2026-11-01") and d($) < d("2026-12-01")` | `"reviewed"` | `false` | `true` |
| (any) | (any) | (any) | `false` |

The correction revision is the same graph with `"approved"` in place of `"reviewed"` in the second row. ZEN has no activation date for a graph. Thus, the dated intervals are rows in the table, as in the Entelos source tables. The rows split the fixture intervals so that they do not overlap. The `date` is a caller input, so the caller selects the date.

I kept each revision as a separate graph file:

| File | Second row `state` | SHA-256 of the file |
|---|---|---|
| `r1-initial.json` | none; one row, 2026-10-01 to 2026-12-01, `"ready"` | `8488207a...` |
| `r2-amendment.json` | `"reviewed"` | `7e40d18c...` |
| `r3-correction.json` | `"approved"` | `673fe8c5...` |

## What I ran

I ran `zen-engine` 2.1.3 with Python 3.14.6 on macOS arm64 on 2026-10-30. The package was in a virtual environment in a scratch directory outside this checkout. The script, the graph files and the output stayed there. This page adds no dependency.

The script wrote the three graph files. It then called `ZenEngine.evaluate` with `{"trace": true}` for each case. The expected values come from the `expected` field of the fixture, not from the ZEN output.

| Graph | Input | ZEN result | Fixture expects |
|---|---|---|---|
| `r1-initial` | `2026-10-15`, `ready`, not archived | `true` | `true` (`initial_october_ready`) |
| `r2-amendment` | `2026-11-15`, `reviewed`, not archived | `true` | `true` (`future_november_reviewed`) |
| `r2-amendment`, again | the same input | `true` | `true` (`pinned_replay`) |
| `r3-correction` | `2026-11-15`, `reviewed`, not archived | `false` | `false` (`later_restatement`) |
| `r3-correction` | `2026-11-15`, `approved`, not archived | `true` | not in the fixture; [CORRECTION.md](../CORRECTION.md) gives `true` |
| `r2-amendment` | `2026-11-15`, `reviewed`, archived | `false` | `false` (the `archived-current` case of `scenario.py`) |

The trace of the table node had the node name, the output, the matched row `_id` (`r1`), the cell expressions of that row and the input values. It did not name the graph file or a content identity of the graph.

**A failed first attempt.** My first script wrote the date cells as string intervals, for example `["2026-11-01".."2026-12-01")`. All six calls gave `false`, and `evaluate` raised no error. A direct test with `evaluate_unary_expression` showed the cause: `Opcode Interval: Unsupported type`. In the table, the cell error made the row not match, and the default row gave `false`. I then used `d($)` date functions. I kept the output of the failed attempt in the scratch directory. In Entelos, the `qualify` step runs a reviewer's case plan before approval. A case plan with a `true` case finds this type of error. In ZEN, the graph tests in the BRMS can find it. The engine alone does not.

These runs show the rule and the answers. They do not test the BRMS, the simulator, the editor, releases, requests or the Agent.

## The replay question in ZEN

The question: on 2026-11-15, the caller got `true` for the `reviewed` record. After the correction, what was the earlier answer, and what is the answer now?

- **Replay.** The engine keeps no record of an evaluation. To get the earlier answer again, the application must keep the input, the result and the graph revision it used. It must also keep the old graph bytes. Then it can call `evaluate` again on the `r2-amendment` graph, as the third row above does. The ZEN trace does not name the graph revision, so the application must record it. In the BRMS, an earlier release does not change, and the docs list evaluate endpoints for a release and for a commit. I did not run them. The docs describe no stored record of each evaluation. The audit log records user actions, for example a deploy, not evaluations.
- **Restatement.** The fourth row runs the same input on the `r3-correction` graph. The answer is `false`. ZEN records no link from this answer to the earlier answer.
- **Fact correction.** The fifth row sends the corrected input `approved`. This is a new evaluation, with no link to the earlier one. Entelos also records no link for a corrected-fact `invoke`. See [CORRECTION.md](../CORRECTION.md).
- **Identity.** A BRMS release has a semantic version, and a commit has an ID. The docs do not state that a release version is a content hash of the graph bytes. In Entelos, a receipt names the snapshot, bundle and engine build by content identity. A content identity is not a signature or an authority.

## Versions and the simulator

These features are in the GoRules BRMS, not in the MIT-licensed engine. I read them in the docs and did not run them.

- **Versions.** A BRMS v2 project keeps graphs on branches with commits. The History page can compare two commits, branches or releases with a visual diff. "Revert to this commit" makes a new branch from the latest main and does not overwrite history. A release is a snapshot of a branch. Its contents do not change after it is made. Publishing a release gives it a semantic version.
- **Simulator.** The simulator runs a JSON input through the graph in the editor. It shows the output and a trace for each node, with the evaluated and matched table rows. It does not save the result unless the author saves the input as an event. The simulate API also does not save changes. A saved event runs with the graph tests.
- **Tests.** A `<graph>.test` file holds cases with an input and an expected output. A case passes when the actual output contains each expected field with the expected value. Tests run from the graph's Tests tab, and the review and release dialogs run them as pre-flight checks.
- **Requests and approval.** A project can protect its main branch. A request can cover a merge, a deploy, an undeploy, a publish or an unpublish. The completion modes are no approval, an open request, a single approval and a group approval. An author can not approve their own request. Each environment can also require approval for a deploy.

## The decision path

"Built in" means the ZEN Engine or the GoRules BRMS supplies the step. "BRMS" names steps that only the commercial BRMS supplies.

| Step | GoRules ZEN | Notes | Entelos |
|---|---|---|---|
| Source | Built in | JDM graph files, with decision tables, expression nodes, function nodes and switch nodes, or policy documents that compile to the same engine. The BRMS keeps them on branches with commits. The engine reads a graph from a loader that the application supplies. | CEL source and source tables in a Git store. The application supplies source authority. |
| Proposal | Built in (BRMS) | A branch and a merge request in the BRMS. The engine alone has no proposal record; a Git pull request can supply one. | `propose` records a candidate snapshot with its source, interval and bundle. |
| Qualification | Built in (BRMS) | Graph tests with expected outputs, and pre-flight test runs on review and release. The docs do not state that a test pass is bound to the exact release bytes. | `qualify` runs a reviewer's case plan on the exact snapshot and records a report. |
| Approval | Built in (BRMS) | Single or group approval on a request to merge, publish or deploy. An author can not approve their own request. The audit log records the actions. | A reviewer's `decide` records the approval of a qualification report. A content hash is an identity, not a signature. |
| Dated activation | Possible with other tools | A deploy is immediate. The docs describe no scheduled activation date. Dated rows in the graph, as above, give a dated answer, but the caller supplies the date. | A reviewer's `activate` event selects the snapshot for its interval. |
| Preparation | Built in | A release is a fixed snapshot. The BRMS deploys it to an environment or to object storage for the Agent. With the engine alone, the application loads the graph file. | `prepare.py` makes the bundle for the pinned host build. A worker's `load` loads the active snapshot. |
| Execution | Built in | The engine in Rust, with bindings for Node.js, Python, Go, Java, Kotlin and .NET, the BRMS API and the Agent. Input and output are any JSON value. | `invoke` on the host. The current profile accepts string and bool inputs and gives a bool output. |
| Feedback and correction | Possible with other tools | A correction is a new commit and release. The trace is returned to the caller, not stored. The docs describe no record of each evaluation, so the application must log the input, result and release. | A receipt for each answer. `observe` attaches feedback. A correction is a new proposal and activation. |
| Replay or restatement | Possible with other tools | An earlier release does not change, so the application can evaluate a logged input on it again. ZEN writes no record that links the new answer to the earlier one. | `replay` uses the pinned snapshot. `restate` writes a new receipt that links to the earlier receipt. |

## Where GoRules ZEN is the better choice

- **Business users edit the rules.** The BRMS has a visual graph editor, decision tables and a simulator with a trace for each node. Entelos has no editor. Its authors write CEL and JSON source tables.
- **Structured input and output.** A ZEN graph reads nested JSON and can return any JSON value. The current Entelos profile accepts only string and bool inputs and gives a bool output.
- **A managed review and release flow.** The BRMS has branches, visual diffs, merge requests with group approval, immutable releases and per-environment deploy approval. Entelos is a local reference on a Git store and has no user interface.
- **Embedding in many languages.** The engine has a Rust core with bindings for Node.js, Python, Go, Java, Kotlin and .NET. The Entelos host is a Go command, and the lifecycle core is Go.

If the main need is a record of each answer, a dated activation, and replay or restatement that links to the earlier answer, ZEN supplies parts. The application must then supply the evaluation log, the activation date and the link records.

## Limits

- I did not measure speed, effort or code size. This page makes no claim about them.
- I ran only the Python binding 2.1.3 of the engine, on one machine. I did not run the BRMS, the editor, the simulator or the Agent. The rows for those features come from the docs, not from a run.
- The docs do not state whether requests and approvals need a specific BRMS plan. I did not check the prices.
- The ZEN graph splits the fixture intervals into rows that do not overlap. It does not model the Entelos stage order.
- The docs list evaluate endpoints for a release and for a commit. I read only their index entries.
