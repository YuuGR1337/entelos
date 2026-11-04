# Fact correction versus rule restatement

A recorded answer can be wrong for two different reasons:

- The application supplied a wrong fact. The rule is correct, but an input value is wrong.
- The approved rule is wrong. The input is correct, but the rule gave the wrong answer.

The lifecycle handles these two cases differently. This page uses the neutral record-filter scenario to show the boundary. The scenario is [lifecycle-scenario.json](../src/cel-lifecycle/fixtures/lifecycle-scenario.json), and [scenario.py](../src/cel-lifecycle/scenario.py) runs it.

To run this scenario with one command, use the [quickstart demo](../examples/quickstart/README.md).

## The rule and the inputs

The function `record.visible` in scope `demo` takes two caller inputs, `state` (string) and `archived` (bool). It returns `true` only if `state` is equal to the fixed value `allowed_state` and `archived` is `false`. The approved source tables give these values:

| Stage | Interval | `allowed_state` |
|---|---|---|
| `initial` | 2026-10-01 to 2026-12-01 | `ready` |
| `amendment` | 2026-11-01 to 2026-12-01 | `reviewed` |
| `correction` | 2026-11-01 to 2026-12-01 | `approved` |

Each interval includes its start date and excludes its end date. In the scenario, the `amendment` source table had a transcription error. The `correction` stage replaces it.

## The pinned receipt

On 2026-11-15, with the `amendment` snapshot active and loaded, the caller sends this input:

```json
{"archived":false,"state":"reviewed"}
```

The scenario names this call `november-before-correction`. The answer is `true`. The receipt records these items:

- `inputs_ref`: the content identity of the normalized input bytes. The host's fixed JSON encoding sorts the keys.
- `selection`: the function, scope, date, event, snapshot, bundle, engine build and `registry_cursor` that the call used.
- `response_ref`, `engine_build`, `lifecycle_build`, `invocation_id`, `previous_receipt` and `reason`.

The receipt does not refer to an application record, a database row or a fact source. It stores the input values themselves. The lifecycle does not know where those values came from.

## Three operations

All three operations start from the same old receipt. They give three different answers.

| Operation | Command | Input used | Rule used | Expected `value` | Status |
|---|---|---|---|---|---|
| Pinned replay | `replay` with the old `receipt_ref` | Retained `reviewed` input | Old `selection` (`amendment` snapshot) | `true` | Tested (`pinned-replay`) |
| Rule restatement | `restate` with the old `receipt_ref` and a later `cursor` | Retained `reviewed` input | Rule selected at the later cursor for 2026-11-15 (`correction` snapshot) | `false` | Tested (`later-restatement`) |
| Corrected-fact invocation | New `invoke` with corrected inputs | New `approved` input | Rule active and loaded now for 2026-11-15 | See below | Not exercised by the scenario |

### Pinned replay

Replay runs the old selection again with the retained input bytes. It does not resolve the latest rule. It needs the same engine build and lifecycle build. If either build is not available, replay fails with `implementation_unavailable`. The replay receipt records `matches_original`. This value is `true` only if the new response bytes are the same as the recorded response bytes. A replay with a different answer is still recorded, with `matches_original` `false`.

Replay does not accept changed inputs. The scenario sends `inputs_json` `{"state":"approved","archived":false}` with the old receipt. The result is `input_mismatch` (`changed-replay-inputs`). Thus replay can not correct a fact.

### Rule restatement

Restatement keeps the retained input. It selects the rule again at an explicit later accepted cursor, for the old date. The later cursor must have the old `registry_cursor` in its history. It must not be equal to that cursor. Else the result is `invalid_cursor`. Restatement also needs a reason.

Restatement writes a new receipt with `mode` `restate` and `previous_receipt` set to the old receipt. The old receipt does not change. The scenario checks this with the `receipt` read after the restatement.

Restatement does not read a corrected fact from the application. If the record's real state was `approved`, restatement still evaluates `reviewed`. Restatement also does not refund, launch or revert an effect.

### Corrected-fact invocation

Assume that the application finds that the record's state on 2026-11-15 was `approved`, not `reviewed`. The application must send a new `invoke` with the corrected input:

```json
{"archived":false,"state":"approved"}
```

This call gets a new `invocation_id` and a new receipt with `mode` `invoke`. The lifecycle does not set `previous_receipt` for an `invoke`. Thus the lifecycle records no link to the old receipt.

`invoke` always uses the snapshot that is active and loaded now. It has no `cursor` argument. The answer therefore depends on when the application sends the corrected input. The approved source tables give these expected values:

| When the application invokes | Selected `allowed_state` | Expected `value` for `approved` |
|---|---|---|
| Before the rule correction, with `amendment` active | `reviewed` | `false` |
| After the rule correction is active and loaded | `approved` | `true` |

These values come from the source tables, not from a run. The scenario does not send this corrected-fact call. The current CLI can send it with the same `invoke` command that `november-corrected` uses.

The lifecycle can not evaluate a corrected fact against the old rule at the old cursor. Replay rejects changed inputs, and `invoke` selects only the current rule. If an application needs that answer, it must record that need as a new requirement.

## Ownership

The application owns these items. The lifecycle does not implement them.

- **Input revision identity.** The application gives each fact revision its own identity, for example a record ID and a revision number. The receipt identity `inputs_ref` is a content hash of the values only. Two different records with the same values have the same `inputs_ref`.
- **The link between answers.** The application stores the relation "receipt B answers corrected revision 2 of the record that receipt A answered with revision 1". The lifecycle links a restatement to its old receipt only.
- **The decision to correct.** The application decides which fact source has authority and when a fact revision is accepted. A content hash is an identity, not a signature or an authority.
- **Effects.** The application decides whether the new answer reverses, repeats or keeps an earlier effect. The lifecycle performs no effect.

The `observe` command can attach a message to an old receipt and open a task. It does not identify the corrected fact or link a new receipt.

## Storage and transaction obligations

The local reference does not implement these obligations. An application that corrects supplied facts must supply them. See [storage-port.md](../src/cel-lifecycle/storage-port.md) for the generic store obligations.

1. Store each fact revision durably with its own identity before the corrected invocation.
2. Store the link from the old fact revision, the old receipt, the new fact revision and the new receipt. Make the link and the new receipt reference visible together, or record a recovery state.
3. Use a request key for the corrected invocation that names the fact revision. A retry then returns the original result. The same key with a changed input is an `idempotency_conflict`.
4. Keep the old fact revision and the old receipt. Do not overwrite or delete them.
5. Apply retention and access policy to the fact revisions and the link records.
6. Make each effect of the new answer idempotent, and reconcile it with the effects of the old answer.

The [CI trigger example](../examples/ci-trigger/adapter.py) stores raw-event, projection, input and receipt links in a sidecar. That sidecar is outside the generic Git transaction. It is not an atomic correction record.

## How to see the tested part

The full runner exercises replay and restatement:

```sh
python3 -B verify.py --work "$TASK_WORK"
```

The runner writes the trace to `$TASK_WORK/conformance/baseline-results.json`. Find the calls by name: `november-before-correction`, `pinned-replay`, `later-restatement`, `changed-replay-inputs` and `november-corrected`. The check `correction-pinned-replay-restatement` compares the replay, the restatement and the retained receipt.

## Limits

- The scenario does not send a corrected-fact `invoke`. The expected values in the corrected-fact table are not tested.
- The current profile accepts string and bool inputs only. This page adds no structured fact transport.
- The lifecycle has no fact revision store and no link record between corrected answers.
- `november-corrected` sends the original `reviewed` input after the rule correction. It is a new call with the same input, not a fact correction.
