# Comparison: feature-flag services

This page compares the Entelos decision path with two feature-flag services. It uses the `record.visible` record filter from the [quickstart demo](../../examples/quickstart/README.md). It writes the same rule, the correction and the replay question in each service where the service can.

The page does not measure speed, effort or cost. It makes no claim about which tool is better in general.

## Sources and versions

I read these sources on 2026-10-30.

| Service | Version | Release date | Primary docs |
|---|---|---|---|
| flagd | `flagd/v0.18.2` | 2026-10-28 | [Flag definitions](https://flagd.dev/reference/flag-definitions/), [Syncs](https://flagd.dev/concepts/syncs/), [Schema](https://flagd.dev/reference/schema/), [CLI](https://flagd.dev/reference/flagd-cli/flagd/), [releases](https://github.com/open-feature/flagd/releases) |
| OpenFeature specification | `v0.9.0` | 2026-07-29 | [Flag evaluation](https://openfeature.dev/specification/sections/flag-evaluation), [Hooks](https://openfeature.dev/specification/sections/hooks), [releases](https://github.com/open-feature/spec/releases) |
| Unleash | `v8.3.1` | 2026-10-29 | [Activation strategies](https://docs.getunleash.io/concepts/activation-strategies), [Unleash context](https://docs.getunleash.io/concepts/unleash-context), [Events](https://docs.getunleash.io/concepts/events), [Change requests](https://docs.getunleash.io/concepts/change-requests), [Impression data](https://docs.getunleash.io/concepts/impression-data), [Playground](https://docs.getunleash.io/concepts/playground), [Import and export](https://docs.getunleash.io/concepts/import-export), [releases](https://github.com/Unleash/unleash/releases) |

The Entelos column refers to `main` at commit `784e0dc`. Its facts come from [CORRECTION.md](../CORRECTION.md), [WALKTHROUGH.md](../WALKTHROUGH.md) and [scenario.py](../../src/lifecycle-core/client/scenario.py).

Evidence level for each service:

- flagd: I ran the image `ghcr.io/open-feature/flagd:v0.18.2` (digest `sha256:a1b6de22ef4f8a4f10af2f9d0009ccef946d707d4211db342f0f2422778b764a`) on 2026-10-30. The commands and the output are in a scratch directory outside the checkout. The results are below.
- Unleash: I did not run Unleash. The Unleash rule below comes from the docs only. Change requests and scheduled change requests are Enterprise features.

## The rule

The function `record.visible` takes `state` (string) and `archived` (bool). It returns `true` only if `state` is equal to `allowed_state` and `archived` is `false`. The [fixture](../../src/cel-lifecycle/fixtures/lifecycle-scenario.json) gives three source tables:

| Stage | Interval | `allowed_state` |
|---|---|---|
| `initial` | 2026-10-01 to 2026-12-01 | `ready` |
| `amendment` | 2026-11-01 to 2026-12-01 | `reviewed` |
| `correction` | 2026-11-01 to 2026-12-01 | `approved` |

The `amendment` table had a transcription error. The `correction` table replaces it. The replay question is: "On 2026-11-15, before the correction, what was the answer for `{"state":"reviewed","archived":false}`, and on which rule?" The fixture expects `true` for that answer, `true` for the replay and `false` for the restatement under the correction.

In Entelos, the rule is the CEL expression `state == allowed_state && !archived`. The value of `allowed_state` is a fixed parameter in the bundle. The caller can not send it.

### flagd

flagd uses a modified JsonLogic for targeting. One flag definition holds the amendment rule. The `asOf` context field is the evaluation date in Unix seconds, from the caller. I used `asOf` and not `$flagd.timestamp` because `$flagd.timestamp` is the server clock. With the server clock, a caller can not ask about 2026-11-15 on 2026-10-30.

```json
{
  "$schema": "https://flagd.dev/schema/v0/flags.json",
  "metadata": {"flagSetId": "demo"},
  "flags": {
    "record-visible": {
      "state": "ENABLED",
      "variants": {"true": true, "false": false},
      "defaultVariant": "false",
      "targeting": {
        "if": [
          {"var": "archived"}, "false",
          {"and": [{">=": [{"var": "asOf"}, 1793491200]}, {"<": [{"var": "asOf"}, 1796083200]}]},
          {"if": [{"==": [{"var": "state"}, "reviewed"]}, "true", "false"]},
          {"and": [{">=": [{"var": "asOf"}, 1790812800]}, {"<": [{"var": "asOf"}, 1796083200]}]},
          {"if": [{"==": [{"var": "state"}, "ready"]}, "true", "false"]},
          null
        ]
      },
      "metadata": {"stage": "amendment"}
    }
  }
}
```

The timestamps are 2026-11-01, 2026-12-01 and 2026-10-01 at 00:00 UTC. The correction is a second file. It changes `"reviewed"` to `"approved"` and the metadata `stage` to `"correction"`. flagd has no source table, so the three stages are in one targeting tree. The amendment branch comes first, so it takes precedence in November.

To use the server clock for a dated activation, replace `{"var": "asOf"}` with `{"var": "$flagd.timestamp"}`. I checked this syntax with a separate probe flag, `{">=": [{"var": "$flagd.timestamp"}, 1790812800]}`. It returned `true` on 2026-10-30.

### Unleash (not run)

Unleash has no boolean constraint operator, and SDKs send context values as strings. Thus the rule compares `archived` as the string `"false"`. Constraints in one strategy are joined with AND. Strategies on one flag are joined with OR. Thus the October window must stop at 2026-11-01, or the `ready` strategy also matches in November.

| Strategy | Constraints |
|---|---|
| November (amendment) | `currentTime` `DATE_AFTER` `2026-11-01T00:00:00Z`; `currentTime` `DATE_BEFORE` `2026-12-01T00:00:00Z`; `state` `IN` `reviewed`; `archived` `IN` `false` |
| October (initial) | `currentTime` `DATE_AFTER` `2026-10-01T00:00:00Z`; `currentTime` `DATE_BEFORE` `2026-11-01T00:00:00Z`; `state` `IN` `ready`; `archived` `IN` `false` |

The docs say "after" for `DATE_AFTER`. They do not say if the boundary instant is included. I did not test it. The caller can set `currentTime` to 2026-11-15 to ask about that date. The correction edits the November strategy from `reviewed` to `approved`.

## flagd run results

The expected values come from the fixture. The observed values come from two flagd processes, one for each file.

| File | Context | Expected | Observed `value` | `reason` |
|---|---|---|---|---|
| amendment | `ready`, `archived` false, 2026-10-15 | `true` | `true` | `TARGETING_MATCH` |
| amendment | `reviewed`, `archived` false, 2026-11-15 | `true` | `true` | `TARGETING_MATCH` |
| amendment | `reviewed`, `archived` true, 2026-11-15 | `false` | `false` | `TARGETING_MATCH` |
| amendment | `closed`, `archived` false, 2026-11-15 | `false` | `false` | `TARGETING_MATCH` |
| correction | `reviewed`, `archived` false, 2026-11-15 | `false` | `false` | `TARGETING_MATCH` |
| correction | `approved`, `archived` false, 2026-11-15 | `true` | `true` | `TARGETING_MATCH` |

Each response also returned the variant and the metadata `{"flagSetId":"demo","stage":"amendment"}` or `"correction"`. The metadata is text that I wrote in the file. flagd does not compute it from the rule.

These probes show the input boundary. Entelos refuses each of these inputs ([WALKTHROUGH.md](../WALKTHROUGH.md), section 4):

| Context | Entelos | flagd observed |
|---|---|---|
| `archived` absent | `needs_input` | `true`, `TARGETING_MATCH` |
| `archived` is the string `"false"` | `invalid_input` | `false`, `TARGETING_MATCH` |
| `archived` is the number `0` | not tested; the schema is bool | `true`, `TARGETING_MATCH` |
| `asOf` absent | not applicable; `date` is a call argument | `false`, `DEFAULT` |

In flagd, a missing or wrong-type field gives an ordinary value, not an error. A JsonLogic `var` that is absent is falsy, so an absent `archived` gives "visible". The application must validate the context before it evaluates the flag. The flag definition has no input schema.

## The decision path

Each row is one step of the path in the [README diagram](../diagrams/decision-path.svg). The values are:

- **Built in**: the service does this step without other tools.
- **Possible with other tools**: the application or another tool (for example Git or CI) must do it.
- **Not covered**: the docs that I read do not describe it, and I found no direct way.

| Step | Entelos (local reference) | flagd with OpenFeature | Unleash v8.3.1 |
|---|---|---|---|
| Source | Built in. `source` records the approved source table, its interval and what it supersedes. | Possible with other tools. flagd reads flag files from file, HTTP, gRPC, Kubernetes or object storage syncs. A Git repository can hold the files and the source text. | Possible with other tools. A flag has a description and tags. There is no record of an approved source table separate from the rule. |
| Proposal | Built in. `propose` makes a candidate snapshot from a base snapshot and a bundle. | Possible with other tools, for example a Git pull request. | Built in for Enterprise: a change request in Draft state. Open source: a direct edit. |
| Qualification | Built in. `qualify` runs independent cases from a separate `plan`. The author can not qualify. A wrong `allowed_state` fails. | Possible with other tools. The JSON schema checks the file shape. Cases must run in CI against a flagd process. The flagd CLI has no `validate` or `test` command. | Possible with other tools. The Playground evaluates a context and can preview a change request. It does not keep a pass/fail record against independent cases. |
| Approval | Built in. `decide` needs a passed report. A failed report gives `qualification_failed`. | Possible with other tools, for example Git branch protection. Approval is not tied to a test result unless CI does that. | Built in for Enterprise: up to 10 approvals for each change request. Approval does not need a test result. |
| Dated activation | Built in. `activate` records an event. Each snapshot has a `from` and `until` interval. | Possible with other tools. A targeting rule can compare `$flagd.timestamp`. The new file must reach flagd before that date. flagd has no scheduler. | Built in. A `DATE_AFTER` constraint gives a dated rule. Enterprise has scheduled change requests. |
| Preparation | Built in. `load` prepares the selected revision. A call before `load` gives `revision_not_loaded`. | Built in. flagd watches or polls its sync sources and loads the change. | Built in. SDKs fetch and cache the flag configuration. |
| Execution | Built in. `invoke` takes a date, the expected bundle reference and typed inputs. It writes a receipt. | Built in for evaluation. The response has `value`, `variant`, `reason` and metadata. No receipt is written. An OpenFeature `after` or `finally` hook can record each evaluation. | Built in for evaluation. Impression data is an SDK event for each evaluation, opt-in per flag. The application must store it. |
| Feedback and correction | Built in. `observe` attaches a message to a receipt. A correction is a new source, proposal, qualification, decision and activation event. | Possible with other tools. The correction is a new file version, for example a Git commit. | Built in. The event log records who, when, `preData` and `data` for each change. |
| Replay or restatement | Built in. `replay` runs the receipt again on its pinned snapshot. `restate` uses a later cursor and links the new receipt to the old one. | Possible with other tools. I ran the amendment file in a second flagd process to get the old answer. The application must store the context and know which file version was live. | Not covered. The event log keeps `preData`, but the docs describe no evaluation against an earlier configuration. An export can restore a configuration. |

## The replay question in each service

**Entelos.** The old receipt pins the snapshot, the bundle and the engine build. `replay` with the receipt reference gives `true`. `restate` with a later cursor gives `false` and a new receipt that links to the old one. The quickstart demo runs both.

**flagd.** flagd keeps no record of an evaluation. To answer the question, the application must:

1. Store the evaluation context, the time and the flag set version with each answer, for example in an OpenFeature `finally` hook.
2. Keep each flag file version, for example in Git.
3. Find the file version that was live on 2026-11-15. flagd does not record when it loaded a file.
4. Start a flagd process on that file version and send the stored context again.

I did steps 2 and 4 by hand. The amendment process gave `true`, and the correction process gave `false`. The flagd image digest identifies the engine. Nothing in flagd checks that the replay used the same engine as the original answer.

**Unleash.** The event log shows that the November strategy changed from `reviewed` to `approved`, who changed it and when. To answer the question, the application must keep the impression event with its context and time. Then it must rebuild the earlier configuration from `preData` or from an export, and evaluate it again. I found no documented replay or revert action.

## Where a feature-flag service is the better choice

- **Gradual rollout and experiments.** flagd has a `fractional` operator, and Unleash has gradual rollout strategies and variants. Entelos has no percentage rollout.
- **A fast kill switch.** A flag service changes behavior for all callers when its configuration changes. It does not need a proposal, a qualification and a decision first. If the change is a release toggle with no record obligation, the Entelos path adds steps that give no value.
- **Many languages and services.** OpenFeature has SDKs for many languages and a provider model. Entelos has one Go host and a Go lifecycle core, with a string/bool profile only.
- **People who do not edit code.** Unleash has a web UI, an event log with user identity and, in Enterprise, approvals. Entelos has no authentication and no UI.
- **Richer context.** Unleash has numeric, date, semver, regex and IP operators. The Entelos callable profile accepts string and bool inputs only.

## Where Entelos covers a step that the services leave to the application

- A typed input contract. flagd returned ordinary answers for an absent field and for wrong types. Entelos refuses them with a structured diagnostic.
- Qualification against independent cases before approval, with the author kept out of qualification.
- A receipt for each answer that names the snapshot, the bundle and the engine build.
- Replay of an earlier answer on its pinned rule, and restatement under a later rule with a link to the old receipt.

These are differences in scope. They do not show that Entelos is faster, cheaper or easier to operate. I did not measure those.

## Limits

- I ran flagd only. I did not run the OpenFeature SDK, an OpenFeature hook, the flagd JSON schema validation or Unleash.
- The flagd run used six cases and four probes on one machine. It does not test performance, concurrency or sync behavior.
- The Unleash rule and all Unleash rows come from the docs. I did not check the `DATE_AFTER` boundary or the Enterprise features.
- A flag service can do more with its paid plans, plugins or integrations than this page shows. The rows describe only the docs that I read.
- Entelos is a local source reference. Its APIs and records are unstable. See [SUPPORT.md](../SUPPORT.md).
