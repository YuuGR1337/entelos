# Compare: ordinary code plus Git

This page compares the Entelos decision path with a plain function in a service, its Git history and its tests. It uses the `record.visible` record filter from the [quickstart demo](../../examples/quickstart/README.md). The facts on this page are from 2026-10-30.

## Sources and versions

| Source | Version or date | Used for |
|---|---|---|
| Go | go1.27.2 darwin/arm64 | The plain function and its tests |
| Git | 2.54.0 (Apple Git-157) | History, `git worktree` for the replay question |
| [Go `testing` package](https://pkg.go.dev/testing) | Go 1.27.2 documentation, read on 2026-10-30 | Tests |
| [Go `runtime/debug.BuildInfo`](https://pkg.go.dev/runtime/debug#BuildInfo) | Go 1.27.2 documentation, read on 2026-10-30 | The `vcs.revision` build setting |
| [git-worktree](https://git-scm.com/docs/git-worktree) and [git-log](https://git-scm.com/docs/git-log) | Git 2.54.0 documentation, read on 2026-10-30 | Old revisions and history |
| [GitHub protected branches](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/about-protected-branches) | Read on 2026-10-30 | Required reviews and status checks |
| [lifecycle-scenario.json](../../src/cel-lifecycle/fixtures/lifecycle-scenario.json) | Commit `cadf024`, 2026-10-29 | The rule, the dates and the expected answers |
| [ordinary.py](../../examples/ci-trigger/ordinary.py) | Commit `cadf024`, 2026-10-29 | The frozen ordinary functions |
| [INTEGRATION.md](../INTEGRATION.md) and [CORRECTION.md](../CORRECTION.md) | Commit `959b2a0`, 2026-10-30 | The Entelos boundary and the correction cases |

## The same rule as a plain Go function

The fixture gives the rule `state == allowed_state && !archived`. The approved `allowed_state` changes by date. The ordinary version puts the dates and the values in the code:

```go
// Each period includes its start date and excludes its end date.
// A later period in the table replaces an earlier one on its interval.
var periods = []period{
	{"2026-10-01", "2026-12-01", "ready"},
	{"2026-11-01", "2026-12-01", "reviewed"}, // the amendment
}

func Visible(day time.Time, state string, archived bool) bool {
	allowed, ok := allowedState(day)
	return ok && state == allowed && !archived
}
```

`allowedState` returns the value of the last period that contains the date. The author wrote that rule for overlaps. Go and Git do not supply it.

## The correction and the replay question

I ran these steps in a scratch Git repository outside this checkout, with Go 1.27.2 and Git 2.54.0:

1. Commit A: the amendment (`reviewed`) and a test for October (`ready` is visible).
2. Commit C: the correction (`approved`) and a new test (`reviewed` is not visible on 2026-11-15).
3. For each commit, `git worktree add` the commit, add a test file that calls `Visible(2026-11-15, "reviewed", false)`, and run `go test -run TestAsk -v`.

| Commit | Question | Result | Fixture value |
|---|---|---|---|
| A (amendment) | 2026-11-15, `{"state":"reviewed","archived":false}` | `true` | `pinned_replay`: `true` |
| C (correction) | The same input and date | `false` | `later_restatement`: `false` |

The two `go test` runs passed. The results agree with the `expected` field of the fixture. The commit hashes and the run output stay in the scratch directory. They are not in the repository.

This check shows one thing: an old commit gives the old answer for an input that I supply. It does not show which commit served the 2026-11-15 call, or which input the caller sent. Git has no record of either.

## The decision path

Status values: **built in** (the tool does it), **other tools** (possible with another tool or with application code), **not covered**.

| Step | Ordinary code plus Git | Status | Entelos |
|---|---|---|---|
| Source | The function and its period table are files in a commit. | Built in | Built in. The application supplies the approved source. Entelos stores the source bytes with a content identity. |
| Proposal | A branch and a commit. A PR host adds the review page. | Built in (Git); other tools for review | Built in. A proposal is a lifecycle record. |
| Qualification | `go test` runs the tests. A CI service binds a check result to a commit SHA. The tests are in the same commit as the change, so the author can change both. | Built in (tests); other tools (CI binds the result) | Built in. Qualification binds the snapshot, source, case plan and build. The application supplies independent cases. A producer cannot submit a passed flag. |
| Approval | Git has no approval. GitHub protected branches can require a review and status checks before a merge. | Other tools | Built in. A separate reviewer decision. |
| Dated activation | The author writes the dates and the overlap rule in the code. The commit date and the deploy date are not the activation date. | Other tools (application code) | Built in. Explicit half-open intervals, exact scope, explicit replacement. No version sort and no timestamp tie-breaker. |
| Preparation | `go build`. A default build records `vcs.revision` in the binary. The toolchain is not stored in Git. | Built in (build); other tools (keep the toolchain) | Built in. A prepared bundle with content identities and a build identity. Entelos also does not archive the toolchain. |
| Execution | A direct call. The Go compiler checks the argument types. | Built in | Built in. A JSON call with structured diagnostics for wrong or missing inputs. |
| Answer record | The service must log the input, the date, the commit and the answer. | Not covered (application code) | Built in. A receipt with the normalized input, the selection and the builds. |
| Feedback and correction | A new commit. The old commit stays in history if nobody rewrites history. Protected branches can block a force push. No earlier answer is linked to the correction. | Built in (history); other tools (protection) | Built in. A correction event and a new snapshot. The earlier receipt stays. |
| Replay | Check out the old commit and call the function with the old input. This needs the logged input and a record of the served commit. It also needs a compatible Go toolchain. | Other tools (log and deploy record) | Built in. Replay uses the receipt's input, snapshot and builds. If a build is not available, replay fails with `implementation_unavailable`. |
| Restatement | Call the current commit with the old input. Nothing links the new answer to the old answer, unless the application writes that link. | Other tools (application code) | Built in. Restatement needs a later accepted cursor and a reason. It writes a new receipt with `previous_receipt`. |

## Ordinary functions under the Entelos lifecycle

[ordinary.py](../../examples/ci-trigger/ordinary.py) has three frozen Python functions for the PR CI example: `initial`, `amendment` and `correction`. The CI example runs them through the same lifecycle port as the CEL bundles: a small executable gives them the CLI protocol of the CEL runtime. Thus the lifecycle rows above do not need CEL. They need a runtime that the lifecycle can identify and call.

These functions have a different profile and build from CEL. They do not implement the CEL parser or cost limits. As [INTEGRATION.md](../INTEGRATION.md) says, a common lifecycle in both paths cannot show that it is better than other lifecycle systems.

## When ordinary code plus Git is the better choice

Use ordinary code plus Git when all of these are true:

- The rule changes only with a service release, and the team that owns the rule also owns the service.
- Nobody must later answer "what did the service answer on that date, and why".
- The service already logs the inputs and the deployed commit, if an audit needs them.

In that case, the plain function has these properties that Entelos does not have:

- The compiler checks the types of the inputs. A wrong type does not compile.
- The function can take numbers, structures and other types. The current Entelos profile takes only string and bool inputs and gives a bool output.
- The service has no second language, no bundle and no lifecycle store to operate.
- The usual editor, debugger and test tools work on the rule.

Also use ordinary code when the rule is not a separate decision, for example a value that only controls the service's own code path.

## Limits

- I did not measure speed, effort or maintenance cost. This page makes no claim about them.
- The ordinary version is one invented Go function. Other code can log answers, store dates as data, or link a restatement to an earlier answer. Those are application work, so the table marks them as other tools.
- The scratch run used one machine (macOS arm64). It did not use a CI service, a PR host or a deploy.
