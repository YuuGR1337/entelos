# Implemented contract map

This map points to executable reference contracts. It is not a separately versioned standard.

| Surface | Source | Behavior |
|---|---|---|
| Typed host | [handoff.go](../src/cel-handoff/handoff.go) | Exact bundle/build, declared caller fields, disjoint fixed values, string/bool inputs and bool result. |
| Authoring | [prepare.py](../src/cel-handoff/tools/prepare.py) and pinned upstream CEL CLI | One environment describes the author/host declarations; tests remain host-owned. |
| Calling | [caller.py](../src/lifecycle-core/client/caller.py) | Discover a typed contract, then send named values and expected bundle identity without source or tests. |
| Lifecycle | [host.go](../src/lifecycle-core/host.go) and the JSONL host [lifecycle-core](../src/lifecycle-core/cmd/lifecycle-core/main.go) | Qualification/report binding, separate admission, interval selection, preparation, receipt and correction operations. |
| Wire protocol 1.0 | [PROTOCOL.md](../src/protocol/PROTOCOL.md) | Versioned, length-prefixed JSON frames; one schema for each operation; golden vectors that the Go sidecar passes. Unstable. |
| Local sidecar | [entelos-sidecar](../src/lifecycle-core/README.md#sidecar) | Wire protocol 1.0 on a Unix domain socket with mode 0600 in front of the Go Host; stale-socket replacement and socket removal on shutdown; no authentication. |
| Local storage | [gitstore.go](../src/lifecycle-core/gitstore.go) | Immutable objects, captured historical roots, request-key recovery and single-root compare-and-swap. |
| Application boundary | [adapter.py](../examples/ci-trigger/adapter.py) | Project a nested event, retain raw/projection audit links; no atomic webhook ingress or pipeline effect is implemented. |

The JSON CLI schemas and typed faults are defined in source. Qualification success does not activate a proposal. Caller discovery must be checked again after source/profile/bundle changes. Invalid input and a successful false result remain different outcomes.

## Callable cases

`Bundle.Check` compares each case with a call to the selected bundle. A case has these fields:

| Field | Meaning |
|---|---|
| `case` | A nonempty, unique name for this case. |
| `inputs` | The input values passed to the bundle. |
| `status` | The exact status expected from the call. |
| `value` | Optional expected boolean result. Required when `status` is `ok`; omitted for every other status. |
| `field` | Optional diagnostic field expected from the call. When present, it must equal the actual diagnostic's field. |

The case list must contain at least one entry. Each actual status must equal the expected status. An answer case with status `ok` passes only when both expected and actual values are present and equal. A refusal or other non-`ok` case passes only when neither the expectation nor the actual response has a value. If `field` is supplied, the response must include a diagnostic with that exact field. These checks do not compare other diagnostic details.

For example, these two entries expect a true answer and a missing-input refusal:

```json
[
  {"case": "paid-eligible", "inputs": {"tier": "gold", "trial": false}, "status": "ok", "value": true},
  {"case": "missing-trial", "inputs": {"tier": "gold"}, "status": "needs_input", "field": "/trial"}
]
```

The case structure and comparison rules are implemented by [`Case` and `Bundle.Check`](../src/cel-handoff/handoff.go).

Replay retains the original input and execution closure. Restatement names a later accepted root and computes a separate answer for the retained original input. A correction to application facts therefore requires a new input/call and an explicit relationship supplied by the application; restatement does not reread a live fact source.

Applications supply source authority, independent expectations, admission/learning policy, authentication, retention and side effects. See [INTEGRATION.md](INTEGRATION.md), [SECURITY.md](../SECURITY.md) and [SUPPORT.md](SUPPORT.md) before treating the local reference as an application component.

## Lifecycle request records

The CLI reads one JSON object per line, in a loop, so one process can take many requests. Each object has `op` plus that operation's fields. The host installs the roles in the context file, so a request never names its own role. Every write operation also requires `expected_revision` and `request_key`; read operations reject them. An unknown field is `invalid_request`.

| Operation | Host role | Accepted fields |
|---|---|---|
| `source` | reviewer | `function`, `from`, `until`, `text`, `supersedes`, `reason` |
| `plan` | reviewer | `source_ref`, `cases`, `origin`, `supersedes` |
| `propose` | author | `function`, `from`, `until`, `base_snapshot`, `source_ref`, `bundle`, `reason`, `stale_mode` |
| `qualify` | runner | `snapshot_ref`, `plan_ref` |
| `decide` | reviewer | `report_ref`, `reason` |
| `activate` | reviewer | `decision_ref`, `targets` |
| `suspend` | reviewer | `function`, `from`, `until`, `targets`, `reason` |
| `load` | worker | `function`, `date` |
| `invoke` | caller | `function`, `date`, `expected_bundle_ref`, `inputs` |
| `replay` | reader | `receipt_ref`, `inputs_json`, `reason` |
| `restate` | reader | `receipt_ref`, `cursor`, `reason` |
| `observe` | observer | `receipt_ref`, `route`, `message` |
| `claim` | maintainer | `task_id`, `checkpoint` |
| `checkpoint` | maintainer | `task_id`, `checkpoint`, `token`, `status`, `note`, `correction_snapshot` |
| `takeover` | reviewer | `task_id`, `checkpoint`, `owner`, `reason` |
| `ack` | worker | `event_ref` |
| `describe` | caller | `function`, `date` |
| `resolve` | reader | `function`, `date`, `cursor` |
| `inspect` | reviewer | `cursor` |
| `record` | reviewer | `ref`, `kind`, `cursor` |
| `receipt` | reader | `receipt_ref` |
| `ingest` | worker | `entries` |

`source.supersedes` and `plan.supersedes` default to an empty list. `activate.targets` defaults to an empty list. `propose.stale_mode` defaults to `fail_closed`. `checkpoint.correction_snapshot` defaults to an empty string. A log entry in `ingest.entries` has no default fields except `stale_reason`, which defaults to an empty string. `describe`, `resolve`, `inspect`, `record` and `receipt` are read operations; the other operations are writes.

## Proposal base rule

`propose` targets one function, scope and date interval. `base_snapshot` must equal the current proposal head for that function and scope. The head advances on every stored proposal, including a proposal that did not pass qualification or was never activated, so a different value is `stale_proposal`. The first proposal for the key uses an empty `base_snapshot`.

The bundle must name all four components. The manifest's `function_id` must equal the proposal's `function`, or the call is `contract_changed`. When `base_snapshot` is not empty, the new manifest must keep the base manifest's `environment_ref`, `caller_variables`, `profile` and `limits`; a difference is `contract_changed`. A later proposal may change the rule and the fixed data.

## Expiry and revocation

An activation is an `enable` event with a half-open interval from `from` to `until`. The lifecycle uses the existing records for expiry and revocation:

- An activation is expired at a date that is at or after its `until` date.
- An activation is revoked at a date in its interval if a later event masks it. A `suspend` event is the usual mask. An `enable` event with `targets` is also a mask.

The proposal declares the stale mode of a revision in `stale_mode`. The two values are `fail_closed` (the default) and `continue_stale`. Any other value is `invalid_request`. The snapshot records the mode. Qualification, the decision and the activation bind that snapshot, so the reviewer approves the mode with the revision. `invoke` has no `stale_mode` field, and a caller cannot propose. Thus a caller cannot select the mode. To change the mode, propose a new revision and qualify, decide and activate it.

If an activation is live at the invocation date, `invoke` uses it, and the receipt is not stale. If no activation is live, `invoke` examines the activation that the worker loaded:

1. If the loaded activation is not an `enable` event of this function and scope, or the date is before its `from` date, the result is `out_of_scope`.
2. If its revision has `fail_closed`, the result is `activation_expired` or `activation_revoked`. The Host publishes no state.
3. If its revision has `continue_stale`, the loaded activation gives the answer. The receipt has `stale` `true` and `stale_reason` `expired` or `revoked`.

Each receipt records `stale_mode` (the mode of the selected revision), `stale` (a boolean) and `stale_reason` (`expired`, `revoked` or an empty string). A replay keeps the stale reason of the old receipt, because it runs the old selection again. A restatement always selects a live activation, so it is never stale. The `describe`, `resolve` and `load` operations do not continue a stale activation. They give `out_of_scope`.

To find the stale receipts, use the existing reads. `inspect` gives `state.receipts`, the list of receipt references. Read each receipt with `receipt` and keep the receipts with `stale` `true`. There is no stale index. The scan reads each receipt once.

The Host compares the invocation `date` with the activation interval. It reads no clock. An executor that sets `date` from its local clock uses only that clock. There is no clock synchronization policy.

## Receipt provenance and ingest

An operator traces each receipt to the executor that wrote it, its position in the local log of that executor and its exact inputs. An executor is a process that runs a pinned snapshot and writes receipts. Each receipt has these provenance fields:

| Field | Meaning |
|---|---|
| `executor_id` | The executor that wrote the receipt. It is the `worker` value of the host context. A request cannot set it. |
| `log_cursor` | The position of the receipt in the log of that executor: an integer from 1. |
| `inputs_ref` | The input digest: the content reference of the canonical input bytes with the `application/json` media type. `InputDigest` computes it. |
| `selection.snapshot_ref` | The pinned snapshot. |

The state field `executors` holds one log for each executor ID. The log is the list of receipt references in cursor order, so the receipt at position n has `log_cursor` n. The Host makes the field when it writes the first receipt. A store without the field has empty logs.

The Host is the executor for `invoke`, `replay` and `restate`. Each of these receipts takes the next cursor in the log of the context `worker`. These receipts are also in `state.receipts`.

`ingest` records the local log of an executor that ran away from the Host. The host role is `worker`, and the executor ID is the context `worker`. `entries` is a nonempty list of log entries in log order. Each entry has these fields:

| Field | Meaning |
|---|---|
| `cursor` | The local log cursor: an integer from 1 to 2^53-1. On a 32-bit platform, the cursor must also be in the range of an `int`. |
| `snapshot_ref` | The pinned snapshot. It must be a snapshot in the store, in the trusted scope, for a function in the context `functions`. Else the result is `out_of_scope`, as for `resolve`. |
| `date` | The invocation date, as an ISO date. |
| `inputs_ref` | The input digest. |
| `response` | The answer object of the executor. Its `bundle_ref` must equal the `bundle_ref` of the snapshot, or the result is `artifact_mismatch`. |
| `stale_reason` | Optional: `expired`, `revoked` or an empty string. |
| `inputs` | Optional: the raw inputs. Only the upload policy permits this field. |

An `enable` event of the registry for this function and scope must name the pinned snapshot and have `from` at or before the entry date. Else the result is `out_of_scope` (stage `resolve`). The Host always keeps the answer of a known activation, because a user saw it. It does not refuse an entry because the stale reason of the executor differs from its own view. This rule also applies to a `fail_closed` snapshot.

The Host computes its own view with the masks that `invoke` uses (see [Expiry and revocation](#expiry-and-revocation)):

1. If an `enable` event of the snapshot is live at the date, the view is an empty string.
2. Else, if the date is in the interval of an `enable` event of the snapshot, a mask removed it. The view is `revoked`.
3. Else, the view is `expired`.

An entry has no `executor_id` field, and an unknown field is `invalid_request`. Thus a request cannot name another executor. To ingest for a different executor, the embedding host installs a context with that `worker` value.

The Host makes one receipt record for each entry. The record has two parts:

- The executor claim: `mode` `ingest`, the provenance fields, a `selection` with `function`, `scope`, `date`, `snapshot_ref`, `bundle_ref` and `engine_build` from the snapshot, `response_ref`, `stale_mode` from the snapshot, `stale` and `stale_reason`. `stale` and `stale_reason` are the statement of the executor.
- The host fields: `host_stale_reason`, the view of the Host at ingest, and `host_checked_events`, the number of registry events that the Host examined. A later event can change the view of the Host, but not a stored receipt.

The record has no `lifecycle_build`, so a new Host build makes the same claim from the same entry.

Ingest is idempotent on the pair (executor ID, cursor). The Host reads the receipt in the log at that cursor and compares its executor claim with the claim of the new entry. The comparison ignores the host fields, so a later event does not make a re-ingest of the same log a conflict:

1. If the cursor is in the log and the claims are equal, the entry is a duplicate. It adds no receipt, and the result names the stored receipt.
2. If the cursor is in the log and the claims differ, the result is `ingest_conflict`. A different response, input digest, snapshot, date or stale reason is a different payload.
3. If the cursor is the next cursor after the log, the Host appends the receipt.
4. If the cursor is after the next cursor, the result is `ingest_gap`. The Host does not fill a gap.

The Host examines the entries in the list order. Thus a reorder in the list is a gap, for example cursor 2 before cursor 1. A repeated cursor in one list is a duplicate. To continue after a gap, send the log again from the first cursor that is not in the store. A list can start before the end of the log, and the earlier entries are duplicates. A fault stops the full ingest, so a failed ingest publishes no part of its entries.

The result is `executor_id`, `log_cursor` (the last cursor of the log), `added` (the number of new receipts) and `receipts` (the receipt reference of each entry, in the list order). A second ingest of the same log with a new `request_key` gives `added` 0 and the same `receipts`. It publishes a new root that has only the new request record. A retry with the same `request_key` gives the first result, as for each write.

The Host and an ingest share one log for each executor ID. If an ingest uses the executor ID of a Host worker at a cursor that the Host wrote, the result is `ingest_conflict`. Use a different executor ID for each executor.

The raw inputs stay with the executor by default. The host context field `upload_inputs` is the upload policy. It is optional, and it must be a boolean. If it is not `true`, an entry with `inputs` is `upload_not_permitted`, and the Host stores only the digest. If it is `true`, the canonical bytes of `inputs` must have the digest `inputs_ref`, or the result is `input_mismatch`. The Host then stores the bytes at that reference. The receipt is the same with and without the upload. A duplicate entry with `inputs` also stores them, so an executor can upload inputs after the first ingest.

To audit the ingested receipts, use `inspect` for `state.executors`, then read each receipt with `receipt`:

- A receipt with `stale` `true` is stale in the claim of the executor.
- A receipt with a `host_stale_reason` that is not empty is stale in the view of the Host.
- A receipt with `stale_reason` different from `host_stale_reason` is a mismatch.
- A stale receipt with `stale_mode` `fail_closed` is an answer that `invoke` refuses. The Host keeps it to record what the user saw.

These are the limits of ingest:

- The Host does not run the rule again. An ingested receipt is the statement of the executor, bound to its executor ID and the pinned snapshot. A content reference is not a signature.
- A mask can revoke an activation after the executor wrote its entry, and the executor did not know about it at that time. Thus a difference between `stale_reason` and `host_stale_reason` is not an error. It is a fact for an auditor.
- An ingested receipt is not in `state.receipts`. It has no registry selection, so `replay` and `restate` give `artifact_unavailable`. Use `inspect` for `state.executors` and `receipt` to read each ingested receipt.
- There is no network transport for ingest. The executor sends its log through the JSONL host or the local socket.

The vectors in [conformance/ingest/](../src/cel-lifecycle/conformance/ingest/) state the expected results. Each vector writes a seed snapshot and its `enable` event to a new store, sends Host requests (a string in `entries` names an entry in the `entries` object of the vector) and checks the result, the number of stored objects, the executor logs and the stored raw inputs. The expected receipt references come from the record shape above. `ingest_test.go` runs each vector on a new Git store and a new in-memory store.

## Content references for a stage bundle

A content reference is `sha256:` plus the lowercase hex SHA-256 of `media type + NUL + exact bytes`. Lifecycle records use `application/vnd.cel-lifecycle.<kind>+json`. The four bundle components use:

| Component | Media type |
|---|---|
| `manifest.json` | `application/vnd.cel-handoff.bundle+json` |
| `env.json` | `application/json` |
| `rule.cel` | `text/plain` |
| `fixed.json` | `application/json` |

To change a fixed value for a stage, replace the fixed bytes, compute the new reference with the `application/json` media type, write it into the manifest's `fixed_ref`, encode the manifest, and compute the manifest reference over the exact manifest bytes with the bundle manifest media type. The examples use the canonical `encode()` from `git_store.py` (sorted keys, UTF-8, no insignificant whitespace, trailing newline), but the host does not require that form and any byte change gives a new reference. The manifest's `environment_ref` and `source_ref` follow the same rule when their bytes change. `describe` reports the assembled bundle identity as `bundle_ref` and the host build as `implementation_ref`; the lifecycle stores them as the snapshot's `bundle_ref` and `engine_build`.

## Author fixtures

`prepare.py --fixture` reads the following fields from a JSON fixture:

| Field | Meaning |
|---|---|
| `function_id` | Identifier written to the generated manifest. |
| `environment` | Object written to `env.json`. It contains a `name` and `variables`; each variable has a `name`, `type` and `description`. The host accepts `string` and `bool` variable types. |
| `caller_variables` | Names of the variables supplied by each caller. The remaining declared variables must have fixed bindings. |
| `fixed_bindings` | Object written to `fixed.json` and combined with each successful case's inputs when preparing upstream tests. Its keys must be declared variables that are not caller variables. |
| `expression` | CEL source written to `rule.cel`. |
| `expected_calls` | Case records written to `cases.json`. Each record supplies `case`, `inputs` and `status`; successful (`ok`) cases also supply `value`, while refusal cases may supply `field` to check the diagnostic location. Only `ok` cases are included in `upstream-tests.json`. |
| `proposed_profile.resource_budgets` | Resource limits copied to the generated manifest as integers, except the descriptive `status` and `cost_model` entries. |

`prepare.py` does not read the fixture's descriptive metadata such as `kind`, `requirement`, `workflow_source` or `expected_cel_output_type`. Those fields do not affect the generated bundle.

Fixed values must use the JSON type declared for their variable: a `bool` needs a JSON boolean and a `string` needs a JSON string. The host rejects null and does not coerce values, so `"false"` is not a boolean. Fixed strings are subject to the manifest's `max_string_utf8_bytes` limit, measured in UTF-8 bytes. Caller and fixed names must be disjoint, and every declared variable must appear in one of those two sets.
