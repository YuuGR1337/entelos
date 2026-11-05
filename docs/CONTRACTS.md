# Implemented contract map

This map points to executable reference contracts. It is not a separately versioned standard.

| Surface | Source | Behavior |
|---|---|---|
| Typed host | [handoff.go](../src/cel-handoff/handoff.go) | Exact bundle/build, declared caller fields, disjoint fixed values, string/bool inputs and bool result. |
| Authoring | [prepare.py](../src/cel-handoff/tools/prepare.py) and pinned upstream CEL CLI | One environment describes the author/host declarations; tests remain host-owned. |
| Calling | [client.py](../src/cel-lifecycle/client.py) | Discover a typed contract, then send named values and expected bundle identity without source or tests. |
| Lifecycle | [lifecycle.py](../src/cel-lifecycle/lifecycle.py) | Qualification/report binding, separate admission, interval selection, preparation, receipt and correction operations. |
| Wire protocol 1.0 | [PROTOCOL.md](../src/protocol/PROTOCOL.md) | Versioned, length-prefixed JSON frames; one schema for each operation; golden vectors that the reference adapter and the Go sidecar pass. Unstable. |
| Local sidecar | [entelos-sidecar](../src/lifecycle-core/README.md#sidecar) | Wire protocol 1.0 on a Unix domain socket with mode 0600 in front of the Go Host; stale-socket replacement and socket removal on shutdown; no authentication. |
| Local storage | [git_store.py](../src/cel-lifecycle/git_store.py) | Immutable objects, captured historical roots, request-key recovery and single-root compare-and-swap. |
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
| `propose` | author | `function`, `from`, `until`, `base_snapshot`, `source_ref`, `bundle`, `reason` |
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

`source.supersedes` and `plan.supersedes` default to an empty list. `activate.targets` defaults to an empty list. `checkpoint.correction_snapshot` defaults to an empty string. `describe`, `resolve`, `inspect`, `record` and `receipt` are read operations; the other operations are writes.

## Proposal base rule

`propose` targets one function, scope and date interval. `base_snapshot` must equal the current proposal head for that function and scope. The head advances on every stored proposal, including a proposal that did not pass qualification or was never activated, so a different value is `stale_proposal`. The first proposal for the key uses an empty `base_snapshot`.

The bundle must name all four components. The manifest's `function_id` must equal the proposal's `function`, or the call is `contract_changed`. When `base_snapshot` is not empty, the new manifest must keep the base manifest's `environment_ref`, `caller_variables`, `profile` and `limits`; a difference is `contract_changed`. A later proposal may change the rule and the fixed data.

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
