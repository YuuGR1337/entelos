# Wire protocol 1.0

This file defines the versioned wire protocol between an application module and the lifecycle. Issue [#83](https://github.com/mohanraj00/entelos/issues/83) owns it. The [technology decision](../../docs/decisions/0001-technology.md) selected JSON with a JSON Schema, length-prefixed frames on a Unix socket, and RFC 8785 for canonical bytes.

The protocol is unstable. A later milestone states its compatibility.

## Files

| Path | Content |
|---|---|
| `schema/envelope.schema.json` | The request envelope, the success and failure responses, and the diagnostic object. |
| `schema/<operation>.schema.json` | One file for each lifecycle operation. `$defs/request_body` is the request body. `$defs/result` is the `result` of a success response. |
| `vectors/session-*.json` | Golden request and response vectors. |
| `vectors/jcs.json` | RFC 8785 canonical form vectors. |
| `reference_adapter.py` | The reference adapter in front of the local Host. |
| `test_protocol.py` | The vector runner. `verify.py` runs it as the `protocol` check. |

## Frame

A frame is a 4-byte unsigned big-endian length `N`, then `N` bytes of UTF-8 JSON.

- `N` must be from 1 to 1048576. If `N` is 0 or more than 1048576, the receiver sends one `invalid_frame` response and closes the connection.
- If the stream ends inside a header or inside a payload, the receiver sends one `invalid_frame` response and closes the connection.
- One request frame gets one response frame, in the same order.
- The receiver encodes each response in the RFC 8785 canonical form.

## Request

```json
{"protocol":"1.0","request_id":"r-1","operation":"describe","body":{"function":"record.visible","date":"2026-11-15"}}
```

| Field | Rule |
|---|---|
| `protocol` | `"MAJOR.MINOR"`. |
| `request_id` | A string of 1 to 128 characters. The caller chooses it. The response echoes it. |
| `operation` | The name of one lifecycle operation. |
| `body` | An object. The schema of the operation defines its fields. |

The receiver rejects these payloads with `invalid_request`:

- JSON that is not valid UTF-8, or that is not one object;
- a duplicate key;
- a number that is not an integer, or an integer outside ±(2^53 − 1);
- an unknown top-level field, a missing field or a field of the wrong type.

A write operation also needs `expected_revision` and `request_key` in its body. A read operation must not have them. The `x-entelos-access` value in each schema file shows the access type. The host context (actor, roles, scope) never comes from a request.

## Version rule

The receiver checks the version first, before all other fields.

- If `protocol` is missing or does not agree with the pattern, the response is `invalid_request`.
- If the major version is not 1, the response is `unsupported_version`.
- If the minor version is more than 0, the response is `unsupported_version`. A later minor version can add fields, and this receiver rejects unknown fields.

Each response has `"protocol":"1.0"`, which is the version of the receiver.

## Response

A success:

```json
{"protocol":"1.0","request_id":"r-1","status":"ok","result":{}}
```

A failure:

```json
{"protocol":"1.0","request_id":"r-1","status":"out_of_scope","diagnostic":{"code":"out_of_scope","stage":"resolve","message":"no enabled revision for the exact function/scope/date"}}
```

- `status` is `ok` or the diagnostic code.
- If the receiver cannot read a valid `request_id`, the response has `"request_id":""`.
- A write success has `result` = `{"cursor":…,"result":…,"reused":…}`. `reused` is `true` for an idempotent retry with the same `request_key` and the same payload.

## Diagnostics

`stage` tells where the failure occurred:

| Stage | Source |
|---|---|
| `protocol` | The frame, the envelope or the body schema. The Host did not get the request. |
| `authority` | The host roles do not permit the operation. |
| `lifecycle`, `admission`, `resolve`, `runtime`, `store`, `codec` | The Host and the store. The codes are the existing `Fault` codes in `src/cel-lifecycle`. |

The protocol adds two codes: `unsupported_version` and `invalid_frame`. All other codes are the existing `Fault` codes, for example `invalid_request`, `forbidden`, `out_of_scope`, `invalid_interval`, `stale_revision`, `idempotency_conflict` and `artifact_unavailable`.

`message` is text for a person. A program must use `code` and `stage`. The vectors do not compare `message`.

## Vectors

Each `session-*.json` file has a host `context` and ordered `steps`. The runner makes a fresh empty store for each file and runs the steps in order. A step gives one of these inputs:

- `request`: an object. The runner encodes it in the canonical form.
- `payload_text` or `payload_hex`: the exact payload bytes of one frame.
- `stream_hex`: the exact bytes of a stream with zero or more frames. The expected value is the list of responses.

The expected response must be equal to the actual response, with these markers:

| Marker | Where | Meaning |
|---|---|---|
| `{"$capture":"name"}` | expected | A nonempty string. The first use binds the name. A later use must be equal. |
| `{"$use":"name"}` | request | The bound value. |
| `{"$type":"string"}` | expected | Any string. |

The runner also checks each response against the envelope schema and each success result against the result schema of its operation. It checks that each Host operation has one schema file with the same fields as the Host table.

The Go sidecar (#84) must pass the same vector files.

## Limits

- The vectors do not run a rule. `describe` with an activation, `qualify`, `load`, `invoke`, `replay` and `restate` are checked only by their schemas. The sidecar work (#84) adds vectors with a fixture runtime.
- Record objects inside a result (`report`, `receipt`, `state`) have the type `object` only. The record format version (#78) owns their schemas.
- The validator supports only the JSON Schema keywords that the files in `schema/` use.
- The adapter does not open a socket. `Adapter.serve` reads frames from a stream, and the sidecar (#84) owns the socket.
