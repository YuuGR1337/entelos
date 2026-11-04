# Storage port and DB-adapter obligations

This record defines follow-on obligations. Only the local Git adapter is implemented and checked. It is not a tested production DB abstraction or a promise that this callback API needs no change for another adapter.

The Host uses store `read(cursor)`, `transact(expected,key,payload,apply)` and accepted `history(cursor)`. A read view exposes its captured cursor/state and immutable `put`, `get`, `put_raw` and `get_raw` operations. Mutations run through validated Host commands; a candidate must not receive the raw trusted store callback. Git-specific plumbing stays in `git_store.py`.

| Obligation | Local reference | Required DB behavior / conformance |
|---|---|---|
| Immutable content | Media/NUL/body content refs, verified Git blobs | Durable exact-byte blob identity, integrity checks and immutable rows/objects; no latest alias during reads |
| Atomic accepted state | One tree and root CAS | One transaction or equivalent atomic revision publication for heads/events/requests/tasks/outbox; reject stale writers |
| Historical/batch read | Capture accepted first-parent root, use its tree | Stable versioned view at an explicit cursor; ordinary current-row reads are insufficient |
| Idempotent success | Key/payload/result in root; origin cursor found in history | Unique key namespace with actor/scope/policy-bound payload and original result/cursor; check existing success before stale revision |
| Qualification/admission | Snapshot/source/plan/build/runner bindings verified before CAS | Recheck those bindings and source/test supersession against the committed revision; a precomputed passed flag is insufficient |
| Applicability | Exact scope, half-open dates, explicit interval masks | Same overlap/partial-mask/suspension and fail-closed ambiguity semantics; no implicit SQL iteration priority |
| Runtime observation | Loaded event/snapshot/build stored separately | Preserve desired versus observed loaded state, including unavailable revision behavior |
| Receipt retention | Immutable inputs/closure/outcome and append-only receipt refs | Transactional append/uniqueness with retained dependencies and access policy; report actual, not desired, identities |
| Correction/replay | Old inputs/build/snapshot preserved; restatement links prior receipt | Byte-identical replay inputs, a recorded `matches_original` response comparison, or an explicit unavailable result; later-cursor restatement must remain a separate record |
| Task recovery | Checkpoint CAS, owner and fencing token; manual takeover | Transactional claim/checkpoint/complete with fencing; document lease/clock policy if added |
| Outbox and recovery | Commit entry with activation; ack after observed load | Commit activation and outbox atomically, retry delivery idempotently, preserve original success after reply loss |
| Failed publication | No accepted event/outbox; orphan blobs may remain | No partial committed business state; explicitly handle unreferenced blob cleanup and transaction rollback |
| Trust/durability | Local repo writers and host contexts trusted | Application authentication/authorization, tenant boundaries, backup/retention and DB/object-store durability are separate requirements |

The Git adapter evaluates deterministic CEL outside a held write lock, then publishes only if the captured root still matches. A DB adapter may prepare work from an immutable snapshot and validate its exact dependencies inside the final transaction. It must not run a slow external process under an unbounded row lock or silently drop the final version check.

Before portability is claimed, run the same Host/command conformance for concurrent activation, partial masks, historical reads, source/test supersession, request retries/conflicts, load observations, receipt replay/restatement, task fences and outbox recovery on the DB adapter. Add realistic transaction failure/restart/durability checks for that database. The present fault injections exercise the local publication contract only. A database implementation, schema/index choice, scheduler and high-volume receipt architecture remain unselected.
