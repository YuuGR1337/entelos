<p align="center">
  <picture>
    <source media="(prefers-color-scheme: dark)" srcset="docs/brand/entelos-logo-dark.svg">
    <img alt="Entelos" src="docs/brand/entelos-logo-light.svg" width="360">
  </picture>
</p>

# Entelos

A service often asks a rule for an answer, and people change that rule over time. Later, someone asks why the service gave a specific answer on a specific date. Often nobody can show the exact rule, inputs and approval behind that answer. If a correction changes the old rule in place, the earlier answer has no explanation. I want each answer tied to its exact rule version, inputs and approval, and each correction recorded as a new, separate answer.

> **Limits:** This is local reference code with unstable APIs and records. It is not a hosted service, an authentication boundary or a production database. Rules accept only string and bool inputs and give a bool result. See [Limits](#limits) and [SUPPORT.md](docs/SUPPORT.md).

## Run the demo

Entelos does this with [CEL](https://cel.dev) rules and a local Git history. The demo runs one invented rule through an answer, a rule change, a replay and a restatement. One run on a Mac took about 1.5 minutes.

### 1. Install the tools

| Tool | Version | Check | Install |
|---|---|---|---|
| Go | 1.27.2 or later | `go version` | [go.dev/dl](https://go.dev/dl/) |
| Python | 3 (tested with 3.14.6) | `python3 --version` | [python.org/downloads](https://www.python.org/downloads/) |
| Git | tested with 2.54.0 | `git --version` | [git-scm.com/downloads](https://git-scm.com/downloads) |

You also need network access. The demo downloads the pinned Go modules on each run. macOS and Linux are tested. Windows is not tested. Before it starts, the demo checks that Go and Git are installed and that Go is 1.27.2 or later. It does not check the Git or Python version.

### 2. Get the code

```sh
git clone https://github.com/mohanraj00/entelos.git
cd entelos
```

### 3. Run the demo

The demo writes to a new directory outside this checkout. The directory must not exist. The demo does not overwrite an earlier run.

```sh
export TASK_WORK="$(mktemp -d)/run"
python3 -B examples/quickstart/demo.py --work "$TASK_WORK"
```

### 4. Read the output

This is the output of one run. The hashes in your run can differ.

```text
Entelos demo: each answer stays explainable after its rule changes.
The rule record.visible answers one question: can a user see this record?
  Rule:   state == allowed_state && !archived
  Values: allowed_state is "ready" from 2026-10-01, then "reviewed" from 2026-11-01.

  1. Answer       Is a "reviewed", not archived record visible on 2026-11-15? -> true (expected true)
                  A receipt in the Git store saves the exact rule version, input and answer.
  2. Rule change  A reviewer changes allowed_state from "reviewed" to "approved".
                  This makes a new rule version. The old version and the receipt stay.
  3. Replay       Why was the answer true? Run the receipt on its old rule version. -> true (expected true)
  4. Restatement  Under the new rule, what is the answer for the same record and date? -> false (expected false)
                  This is a new, separate answer. The first answer stays in the history.

  The expected values come from the fixture file, not from this run.
  Input:   {"state":"reviewed","archived":false}
  Records: receipt sha256:47121ec901a7..., old rule sha256:66e12e967a63...,
           new rule sha256:9e30a2cd3dac..., change event sha256:78730c528391...
  Build:   sha256:94ac3b7b4d34... (cel-go v0.32.0)
Result: passed. Details: <TASK_WORK>/summary.json
Next: read "After the demo" in README.md.
```

- **Answer:** the service asks the rule a question. The receipt keeps the exact rule version and input.
- **Rule change:** a reviewer changes the rule value. The change is a new version. Nothing old is edited. [CORRECTION.md](docs/CORRECTION.md) calls this path a rule restatement, and it also explains a fact correction.
- **Replay:** you can still show why the first answer was `true`, because its rule version still exists.
- **Restatement:** the same question under the new rule gives `false`. You now have two answers, and each one has its own record.

The `expected` values come from a fixture file, not from the demo. `Result: passed` means that each answer agrees with the fixture. [examples/quickstart/README.md](examples/quickstart/README.md) explains `summary.json` and the failure output.

## Call a rule from your service

A service calls a rule through the Go lifecycle host [cmd/lifecycle-core](src/lifecycle-core/cmd/lifecycle-core/main.go). Build it with `go build -o <dir>/lifecycle-core ./cmd/lifecycle-core` in `src/lifecycle-core`. The service writes one JSON request to stdin and reads one JSON response from stdout. A call has two requests:

1. `describe` gives the input schema, the exact bundle reference and the store cursor for a function and a date.
2. `invoke` sends the inputs, the bundle reference and the cursor from `describe`, and a retry key.

These are the two requests for the record filter. The service copies the two values in angle brackets from the `describe` response:

```text
{"op":"describe","function":"record.visible","date":"2026-11-15"}
{"op":"invoke","function":"record.visible","date":"2026-11-15","inputs":{"state":"reviewed","archived":false},"expected_bundle_ref":"<data.callable.bundle_ref>","expected_revision":"<data.cursor>","request_key":"walk-november"}
```

Check two status fields before you read the answer:

1. The outer `status`. If the lifecycle refuses the call, this field has a refusal status, for example `revision_not_loaded`, and the response has no result.
2. `data.result.actual.status`. The rule host can reject the inputs after the lifecycle accepts the call. Then the outer status is `ok`, but this field is `invalid_input`, and `data.result.actual` has no `value`.

The answer is in `data.result.actual.value` only if both fields are `ok`. The store records a receipt for each call that the lifecycle accepts, also for a rejected input. Its identity is in `data.result.receipt_ref`.

[caller.py](src/lifecycle-core/client/caller.py) is a short caller that sends these two requests. It starts the Go host once for each request. The service does not send the rule text, the fixed values or the test cases. Step 6 of [WALKTHROUGH.md](docs/WALKTHROUGH.md#6-call-as-a-caller) gives the full command. The `unchanged-caller-client` check in [examples/ci-trigger/run.py](examples/ci-trigger/run.py) runs `caller.py` in each `verify.py` run.

## After the demo

| If you want to | Read |
|---|---|
| Know how a service calls a rule, why not a plain function and log, and what a correction date means | [COMPARISON.md](docs/COMPARISON.md) |
| See the same steps with the commands and records | [WALKTHROUGH.md](docs/WALKTHROUGH.md) |
| See the receipt of the first answer | The command that the demo prints, or [Print a receipt](examples/quickstart/README.md#print-a-receipt) |
| Know the difference between a fact correction and a rule change | [CORRECTION.md](docs/CORRECTION.md) |
| Call or author a rule from your own service | [Call a rule from your service](#call-a-rule-from-your-service), [CONTRACTS.md](docs/CONTRACTS.md), then [INTEGRATION.md](docs/INTEGRATION.md) |
| Compare Entelos with a tool you know | [OPA](docs/compare/opa.md), [Cedar](docs/compare/cedar.md), [GoRules ZEN](docs/compare/zen.md), [feature flags](docs/compare/flags.md), [ordinary code and Git](docs/compare/code-git.md) |
| Know what Entelos does not do | [Limits](#limits) and [SUPPORT.md](docs/SUPPORT.md) |
| Run all the checks | [Run all checks](#run-all-checks) |
| Ask a question or report a problem | [GitHub issues](https://github.com/mohanraj00/entelos/issues) |

## How a decision moves

![Decision path from source to replay or restatement](docs/diagrams/decision-path.svg)

- [Caller and correction walkthrough](docs/WALKTHROUGH.md): the same record filter, step by step, with the commands and records.
- [Fact correction versus rule restatement](docs/CORRECTION.md): the two reasons that a recorded answer can be wrong, and how the lifecycle handles each.
- [Contract map](docs/CONTRACTS.md): the executable interfaces for authors and callers.
- [Diagram notes](docs/diagrams/README.md): what each part of the diagram shows and does not show.
- [Limits](#limits) and [SUPPORT.md](docs/SUPPORT.md): what the reference does not do.

## What the examples show

Entelos uses [CEL](https://cel.dev) for the rule text and Git for the history. It does not add a new expression language. The repository has two examples:

- The record filter, `record.visible`, decides if a user can see a record. Its correction changes a fixed value of the rule. [WALKTHROUGH.md](docs/WALKTHROUGH.md) shows each step.
- The CI example, `ci.pr_should_run`, decides if a pull request starts CI. Its correction changes the rule formula. It changes nested event data into string and bool inputs. It also compares the CEL rule with an ordinary typed function. The source is in [examples/ci-trigger](examples/ci-trigger/).

Both examples use these terms:

- **Caller contract:** the named inputs and the bool output that a caller can see. A caller cannot send a **fixed value**. A fixed value is a value that the author sets in the rule, for example `allowed_state`. See [CONTRACTS.md](docs/CONTRACTS.md).
- **Snapshot:** one exact set of rule files. Its content hash identifies it. A hash is an identity. It is not a signature.
- **Qualification:** a test of one snapshot against cases that the host owns, not the author. A passed test does not let the rule give answers to callers.
- **Activation:** a reviewer approves a qualified snapshot for one scope and one date interval. A call for a date uses the snapshot that is active on that date.
- **Store:** a local Git repository that holds the approved state. Each change is one update of one Git ref. The update fails if another writer changed the ref first. A **cursor** is the Git commit of one state. A read at an earlier cursor shows the earlier state.
- **Receipt:** the record of one answer: the snapshot, the input, the host build and the result.
- **Correction:** a new snapshot replaces an incorrect snapshot for later calls. The old snapshot, its cases and its receipts stay in the store. **Replay** runs an old receipt again on its own snapshot and build. **Restatement** gives a new, separate answer for the old input at a later cursor. See [CORRECTION.md](docs/CORRECTION.md).
- **Correction task:** a recorded work item for a correction. If a second maintainer takes the task, the store refuses a write from the first owner.

The ordinary-code comparison uses the same lifecycle. It does not show that CEL or this lifecycle is always the better choice.

## Limits

The callable profile accepts named string/bool variables and a boolean result. Numbers, structured wire types and custom extensions are unsupported. Internal expressions use the pinned CEL standard language. Resource ceilings are configured example bounds, not measured production-safe defaults.

This is local reference code with unstable APIs and records. Host context and repository writers are trusted. It is not a network authentication boundary, distributed database, resident-worker coordinator or hosted service. Preparation acknowledgement records a successful ephemeral preparation; each call prepares and checks its exact closure again.

CI ingress verification is simulated. No webhook is authenticated and no pipeline is launched. Its raw-event audit sidecar is not atomic with the Git receipt. The application supplies source truth, independent expectations, admission/learning policy, authentication, retention and side effects. See [INTEGRATION.md](docs/INTEGRATION.md), [SUPPORT.md](docs/SUPPORT.md) and [SECURITY.md](SECURITY.md).

The M5 security review found two open limits. The `inspect` and `record` reads do not check the scope, so a reviewer can read all records in a shared store. Use one store for each scope. A `request_key` that contains a NUL byte gives `store_error` and leaves unused Git objects, but it does not change the state.

The complete source-to-correction workflow is reproducible here. Production adapters and operations remain separate work. Ordinary code plus Git is appropriate when these explicit contracts and histories are unnecessary.

## Run all checks

Use Python, Go and Git. See [SUPPORT.md](docs/SUPPORT.md) for the tested toolchain and limits. Use a new work directory, not the demo directory. One run on a Mac took about 10 minutes, and the runner prints its result only at the end.

```sh
export TASK_WORK="$(mktemp -d)/run"
python3 -B verify.py --work "$TASK_WORK"
```

The runner fetches the pinned public CEL author tools, builds the host and executes the examples and tests. Network access is needed for public source/module downloads. No model account, database, Kubernetes cluster or private repository is needed. Builds, logs, bare Git stores and generated inputs stay under the supplied work directory. Do not use a directory containing real data.

If the author-tools checkout is already available at the documented revision, pass `--skills /path/to/skills`. The runner requires a clean checkout. It never pushes, creates a remote repository or publishes a package.

The work directory contains `results.json`, command logs and the example stores. To discover the CI function after a successful run:

```sh
printf '%s\n' '{"op":"describe","function":"ci.pr_should_run","date":"2026-11-15"}' | "$TASK_WORK/bin/lifecycle-core" --repo "$TASK_WORK/reuse/cel/state.git" --context "$TASK_WORK/reuse/cel/caller.json" --runtime "$TASK_WORK/bin/cel-handoff" --work "$TASK_WORK/reuse/cel"
```

To call a function from your own code, see [Call a rule from your service](#call-a-rule-from-your-service). Authors can use the existing CEL prompt/compile/eval tools and submit an exact proposal/base through the same local command boundary.

## Work in this project

Start with [CONSTITUTION.md](CONSTITUTION.md), [AGENTS.md](AGENTS.md) and [current work](docs/STATUS.md). Codex and Claude Code share those rules and the same skill source. [Tracked issues](docs/BACKLOG.md) define the bounded next work. [Contract map](docs/CONTRACTS.md) points to the executable interfaces.

## Name, source and license

The tagline is "Entelos: grow toward the goal." Entelos (from Greek *en* + *telos*, "the goal within") is the name of this versioned decision reference. The repository is `mohanraj00/entelos`; its earlier name was `versioned-decision-reference`. [Brand assets](docs/brand/README.md) holds the mark, the logo and the colours.

The project source is licensed under [Apache-2.0](LICENSE). Keep the [project attribution](NOTICE) and required notices. Dependencies retain their own licenses; see [THIRD_PARTY.md](licenses/THIRD_PARTY.md). The [licence decision](docs/LICENSING.md) records the change from MIT. The workflow examples are neutral invented policies informed by the public sources listed in [PROVENANCE.md](docs/PROVENANCE.md).
