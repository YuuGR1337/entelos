# Storage port specification

This file specifies the storage port that the Host uses. [storage-port.md](storage-port.md) gives the obligations for a future database adapter. The JSON vectors in [conformance/storage/](conformance/storage/) test this specification. The Go harness `TestStoragePortVectors` in [vectors_test.go](../lifecycle-core/vectors_test.go) runs them against two adapters:

- `GitStore` in [gitstore.go](../lifecycle-core/gitstore.go), one local bare Git repository.
- `MemoryStore` in [memstore.go](../lifecycle-core/memstore.go), process memory only.

The vectors are language-neutral. A harness in a different language can run the same files.

## Terms

- **Cursor**: an opaque string that identifies one accepted revision. The empty string `""` is the cursor of the empty store. Each adapter selects its own cursor format. A vector does not contain an adapter cursor; it binds a name to a cursor that a step returns.
- **State**: a JSON object. The empty state is `{"schema":"local-lifecycle/v1","heads":{},"sources":[],"plans":[],"events":[],"requests":{},"workers":{},"receipts":[],"tasks":{},"outbox":{}}`.
- **Canonical JSON**: UTF-8 JSON with sorted object keys, no insignificant whitespace, non-ASCII characters not escaped, no non-finite numbers and one final newline (`\n`). Duplicate object keys are not permitted.
- **Media type of a kind**: `application/vnd.cel-lifecycle.<kind>+json`.
- **Content reference**: `sha256:` and the lowercase hex SHA-256 digest of the envelope. The envelope is the media type bytes, one NUL byte and the content bytes.
- **Payload reference**: the content reference of the canonical JSON payload with the media type of the kind `request`.
- **Fault**: an error with a stable `code`. Adapters return faults by code. The message text is not part of the contract.

## Operations

### `read(cursor)`

- Input: an optional cursor. If the cursor is absent, the store uses the current cursor.
- Output: a read view with `cursor` and `state`.
- If the cursor is `""`, the view has the empty state. This is true also after later publications.
- If the cursor is not a string, the operation fails with `unknown_cursor`.
- If the cursor is not `""` and not in the accepted history of the current cursor, the operation fails with `unknown_cursor`.
- Each call returns a new view. A change to a view does not change the store or a different view.

### Read view operations

A view has these operations:

- `put_raw(mime, raw)`: record the bytes as pending content in this view. Return the content reference. Pending content is visible only in this view.
- `put(kind, value)`: `put_raw` with the media type of the kind and the canonical JSON of the value.
- `get_raw(ref, mime)`: return the content bytes. The `mime` input is optional.
- `get(ref, kind)`: `get_raw` with the media type of the kind. Decode the bytes as JSON. The value must be a JSON object.

`get_raw` and `get` look first in the pending content of the view, then in the content of the view cursor. They fail with these codes:

| Code | Condition |
|---|---|
| `invalid_reference` | The reference is not `sha256:` and 64 lowercase hex digits. |
| `artifact_unavailable` | The view and its cursor do not have the content. |
| `artifact_mismatch` | The stored bytes do not have the given reference. |
| `invalid_record` | The media type is not the requested type, the JSON is not valid, or `get` found a value that is not an object. |

### `transact(expected, key, payload, apply)`

- Input: the expected cursor, a request key, a JSON payload and an `apply` callback.
- Output: `{"cursor", "result", "reused"}`.

The store does these steps in this sequence:

1. If the key is not a string of 1 to 128 characters, fail with `invalid_request`.
2. Read a view at the current cursor. Calculate the payload reference.
3. If the current state has a request record for the key, compare the payload references:
   - If they are different, fail with `idempotency_conflict`.
   - If they are equal, return the stored result, `reused: true` and the first accepted cursor that has the record. Do not call `apply`. Do not publish.
4. If `expected` is not the current cursor, fail with `stale_revision`. Do not call `apply`.
5. Call `apply(view)`. If it fails, return its fault unchanged. Do not publish.
6. Set `state.requests[key]` to `{"payload_ref": <payload reference>, "result": <result>}`.
7. Publish the new state and the pending content of the view as one new revision, with `expected` as its parent. Do this only if the current cursor is still `expected`. Else fail with `stale_revision`.
8. Return the new cursor, the result and `reused: false`.

Step 3 occurs before step 4. Thus a retry with an old cursor still gets its original success.

### `history(cursor)`

- Input: a cursor that `read` or `transact` returned.
- Output: the list of accepted cursors from this cursor back to the first revision, newest first. The list does not contain `""`. For `""`, the list is empty.

## Error codes

| Code | Operation | Meaning |
|---|---|---|
| `invalid_request` | `transact` | The request key is not valid. |
| `idempotency_conflict` | `transact` | The key has a record for a different payload. |
| `stale_revision` | `transact` | The expected cursor is not current, or a different writer published before the final compare-and-swap. |
| `unknown_cursor` | `read` | The cursor is not in accepted history. |
| `invalid_reference`, `artifact_unavailable`, `artifact_mismatch`, `invalid_record` | view reads | See the table in the read view section. |
| `invalid_store`, `store_error` | all | The adapter or its storage failed. The vectors do not cause these codes or `artifact_mismatch`. |

## Invariants

Each vector names the invariants that it tests. The harness makes sure that the vector set tests all six.

| Invariant | Statement |
|---|---|
| `atomic_publication` | One successful `transact` makes its state, its request record and its content visible together, at one new cursor. A view outside `transact` publishes nothing. |
| `stale_cas_rejection` | A writer whose expected cursor is not current publishes nothing. This applies before `apply` and at the final compare-and-swap. |
| `idempotent_success` | A retry with the same key and an equal canonical payload returns the original cursor and result and publishes nothing. The same key with a different payload fails. |
| `stable_historical_read` | A read at a cursor always returns the same state and content. Later publications do not change it. |
| `no_partial_state_after_failure` | A failed `transact` adds no state, no request record and no content that a read can get. The key stays available. |
| `immutable_content_identity` | A content reference identifies exact bytes and one media type. Equal content gives an equal reference in all transactions. |

An adapter can keep content that no revision refers to after a failure, for example Git objects. A read must not return that content.

## Vector format

Each file in `conformance/storage/` is one JSON object:

- `format`: `storage-port-vector/v1`.
- `id`, `description` and `invariants`.
- `steps`: a list of steps. The harness runs them in sequence on a new empty store.

A string that starts with `$` names a cursor that an earlier step bound. Other strings are literal values. A step has an `op` and an `expect` object:

| `op` | Fields | `expect` |
|---|---|---|
| `read` | optional `cursor`, optional `actions` | `cursor`, `state` (exact), `state_includes` (exact for each named top-level key), or `error` |
| `transact` | `expected`, `key`, `payload`, `apply: {actions, result}` | `bind` (a name for a new nonempty cursor), `cursor`, `result`, `reused`, `apply_calls`, or `error` and `apply_calls` |
| `history` | `cursor` | `cursors`, a list in order |

An action operates on the view of its step. Actions run in sequence:

- `{"put": kind, "value": v, "expect_ref": r}` and `{"put_raw": mime, "text": s, "expect_ref": r}`. The text is UTF-8.
- `{"get": r, "kind": k, "expect": {"value": v} or {"error": c}}` and `{"get_raw": r, "mime": m, "expect": {"text": s} or {"error": c}}`. The `mime` field is optional.
- `{"set": [key, ...], "value": v}`: set a value in the view state at this path of object keys.
- `{"cursor_is": cursor}`: the view cursor must be equal to this cursor.
- `{"transact": step}`: run a nested `transact` step on the same store while `apply` runs. This simulates a concurrent writer.
- `{"raise": code}`: make `apply` fail with this fault code.

Each expected value is a literal in the vector. A script that uses only SHA-256 and the canonical JSON rule calculated the expected references. The harness does not calculate an expected value from adapter output. The only exception is a cursor, which the harness binds by name and then compares for equality.

## Limits

Passing the vectors does not show durability, crash recovery, performance or behavior under real concurrent processes. `MemoryStore` keeps no data after the process stops. The vectors test the port contract, not the Host commands on top of it. Only `GitStore` runs the Host conformance in [host_test.go](../lifecycle-core/host_test.go).
