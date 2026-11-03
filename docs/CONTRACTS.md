# Implemented contract map

This map points to executable reference contracts. It is not a separately versioned standard.

| Surface | Source | Behavior |
|---|---|---|
| Typed host | [handoff.go](../src/cel-handoff/handoff.go) | Exact bundle/build, declared caller fields, disjoint fixed values, string/bool inputs and bool result. |
| Authoring | [prepare.py](../src/cel-handoff/tools/prepare.py) and pinned upstream CEL CLI | One environment describes the author/host declarations; tests remain host-owned. |
| Calling | [client.py](../src/cel-lifecycle/client.py) | Discover a typed contract, then send named values and expected bundle identity without source or tests. |
| Lifecycle | [lifecycle.py](../src/cel-lifecycle/lifecycle.py) | Qualification/report binding, separate admission, interval selection, preparation, receipt and correction operations. |
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
