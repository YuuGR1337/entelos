# Three questions before you compare

An engineer who reads this project for the first time often asks three questions. This page gives a short answer to each, with a link to the evidence. The full comparisons with other tools are in [docs/compare/](compare/):

- [Ordinary code plus Git](compare/code-git.md)
- [OPA](compare/opa.md)
- [Cedar](compare/cedar.md)
- [GoRules ZEN](compare/zen.md)
- [Feature-flag services](compare/flags.md)

## 1. How does a service call a rule?

The reference has three local call paths. It has no network server and no sidecar.

| Path | How the service calls it | What it records | Source |
|---|---|---|---|
| Go library | Call `handoff.Load` with the bundle files, then `Bundle.Run` with one JSON request. | Nothing. It gives the answer only. | [handoff.go](../src/cel-handoff/handoff.go) |
| Rule process | Start `cel-handoff -bundle DIR run`. Write one JSON request on each stdin line, and read one response on each stdout line. | Nothing. It gives the answer only. | [main.go](../src/cel-handoff/cmd/cel-handoff/main.go) |
| Lifecycle process | Start the Go host [lifecycle-core](../src/lifecycle-core/cmd/lifecycle-core/main.go) and write JSON lines. Send `describe`, then `invoke` with the expected bundle, the expected revision and a request key. | A receipt in the Git store. | [caller.py](../src/lifecycle-core/client/caller.py), [WALKTHROUGH.md](WALKTHROUGH.md) |

Only the lifecycle path gives replay, restatement and a receipt. The first two paths evaluate a rule, but they do not keep a history.

The cost of each call is not measured. This page makes no speed claim. The source shows these facts about the work that each call does:

- The Go library runs in the service process. It has no process start and no store write.
- The rule process runs one bundle for its full life. A new bundle needs a new process.
- For each `invoke`, the lifecycle starts a new `cel-handoff` process, with a 15 second timeout ([runtime.go](../src/lifecycle-core/runtime.go)). It also writes the receipt to the Git store with a compare-and-swap on one root ref ([gitstore.go](../src/lifecycle-core/gitstore.go)).

If you need a network service, the application must supply it. It must also supply authentication. See [INTEGRATION.md](INTEGRATION.md) and [SECURITY.md](../SECURITY.md).

## 2. Why not a plain function, a decision log table and Git history?

For many services, that is the better choice. [compare/code-git.md](compare/code-git.md) lists when to use it. It also lists the properties of a plain function that Entelos does not have, for example compiler type checks and more input types.

Here is a case where that log fails. A service logs one row for each answer: date, input, answer and the deployed commit.

1. On 2026-11-15, the service answers `true` for `{"state":"reviewed","archived":false}`. The row records commit A.
2. A reviewer finds an error in the rule. Commit C corrects it.
3. An auditor asks: "What is the corrected answer for that call, and what was the earlier answer?"

The log cannot answer the second part safely, for these reasons:

- **The old row can change.** A script that writes the corrected answer can `UPDATE` the old row. The table has no rule that stops it. Then the earlier answer is lost.
- **Nothing links the new answer to the old one.** If the script inserts a new row, the link to the old row is application code.
- **The commit is not the activation.** Commit A shows what was deployed. It does not show the approved interval, so you cannot tell which rule was meant to apply on 2026-11-15.

Application code can solve each of these. In Entelos, the lifecycle does it:

- A receipt is an immutable object with a content identity.
- A restatement writes a new receipt with `previous_receipt` set to the old receipt.
- The `selection` in the receipt names the snapshot that applied on that date.

The demo shows this sequence. [compare/code-git.md](compare/code-git.md) runs the same case with a plain Go function and two commits.

## 3. What does the effective date of a correction mean?

The effective date is the interval in the approved source. It is not the time of the commit, the deploy or the call. Each interval includes its start date and excludes its end date. The caller sends the date of the question in the `date` field.

In the demo, the correction has the interval 2026-11-01 to 2026-12-01. The scenario does these steps ([scenario.py](../src/lifecycle-core/client/scenario.py)):

1. A reviewer suspends the wrong amendment on its interval. While the suspension applies, a call for 2026-11-15 gets `out_of_scope`. There is no fallback to the initial rule (`suspension-no-fallback`).
2. A reviewer activates the correction. The activation names the amendment and the suspension as its targets.

The correction replaces the amendment on that interval only. A call for 2026-10-15 still uses the initial rule. No version sort or timestamp picks the winner. See [CORRECTION.md](CORRECTION.md) and [INTEGRATION.md](INTEGRATION.md).

These people see a new answer after a correction:

- **A caller that sends a new `invoke`.** If the correction is active and loaded, the caller gets the corrected answer. The scenario tests two stale cases:
  - The caller sends the old bundle. It gets `artifact_mismatch` and must send `describe` again (`old-discovery`).
  - The correction is active, but no worker loaded it. The caller gets `revision_not_loaded` (`correction-before-load`).
- **A reader who restates an old receipt.** `restate` needs a later accepted cursor and a reason. It writes a new receipt and does not change the old one (`later-restatement`).

The lifecycle tells no caller automatically about an earlier answer. It sends no message and performs no effect. The `observe` command can attach a message to an old receipt and open a task. The application decides which earlier answers to restate, and whom to tell.

A correction to a fact, not to the rule, is a different operation. It needs a new `invoke` with the corrected input. [CORRECTION.md](CORRECTION.md) explains this boundary and what the application owns.

## Limits

- I did not measure call cost, speed or effort. The answers above describe the source and the tested scenario only.
- The scenario tests the calls named on this page. It does not test a corrected-fact `invoke`.
- The current profile accepts string and bool inputs and gives a bool output.
