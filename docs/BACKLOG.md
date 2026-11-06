# Tracked work

GitHub issues own live status. This map identifies bounded work for this source reference.

| Issue | Scope |
|---|---|
| [Switch project licensing to Apache-2.0](https://github.com/mohanraj00/entelos/issues/5) | Licence, attribution, documentation and verification; prior history and dependency terms retained. |
| [Bootstrap the runnable reference and shared agent workflow](https://github.com/mohanraj00/entelos/issues/1) | Initial publication, shared project workflow and verification. |
| [Run the full standalone reference suite in GitHub Actions](https://github.com/mohanraj00/entelos/issues/2) | Done: the `Full suite` workflow runs `verify.py` on `ubuntu-24.04`. |
| [Add a clean-run typed caller and correction walkthrough](https://github.com/mohanraj00/entelos/issues/3) | Done: [WALKTHROUGH.md](WALKTHROUGH.md), merged in #23. |
| [Explain supplied-fact correction versus rule restatement](https://github.com/mohanraj00/entelos/issues/4) | Done: [CORRECTION.md](CORRECTION.md), merged in #20. |
| [Tell the user how to choose a new work directory](https://github.com/mohanraj00/entelos/issues/44) | Done: merged in #45. |
| [Make the first run clear](https://github.com/mohanraj00/entelos/issues/46) | Done: tools table, demo output and next steps, merged in #47. |
| [Answer the first three questions](https://github.com/mohanraj00/entelos/issues/38) | Done: [COMPARISON.md](COMPARISON.md), merged in #48. |

## Milestones to a public announcement

On 2026-10-29, the maintainer chose to announce Entelos as a source reference to read and copy. It is not a library to depend on. A milestone is complete only when its exit test passes. The announcement itself needs specific maintainer authorization.

| Milestone | Work | Exit test |
|---|---|---|
| M0 Bootstrap | Done: runnable reference, two examples, full-suite CI, Apache-2.0, cross-harness review, rename. | Met on 2026-10-29. |
| [M1 First contact](https://github.com/mohanraj00/entelos/issues/10) | Done in `main` on 2026-10-30: issues 3 and 4, one demo command that shows an answer, a correction, a replay and a restatement, a README that states the problem first, one diagram of the complete path. Then a clear first run: issues 44 and 46. | Changed on 2026-10-30. The observed sessions are dropped (see #10). A maintainer run of the demo and an adversarial critic pass replace them. No outside engineer ran the demo. |
| [M2 Comparison](https://github.com/mohanraj00/entelos/issues/11) | Done in `main` on 2026-10-30: compare OPA/Rego, Cedar, GoRules ZEN, feature-flag services and ordinary code plus Git, and state where each one is the better choice. [COMPARISON.md](COMPARISON.md) answers three first questions. | Met on 2026-10-30, with the three questions of the critic pass in place of the questions of the M1 engineers. The Cedar blocks were not run. |
| [M3 Copy proof](https://github.com/mohanraj00/entelos/issues/12) | Done in `main` on 2026-10-31: the tier-eligibility example (PR #59) reuses `src/` and needed 0 `src/` changes. It found 9 docs gaps (issue #60). | Met on 2026-10-31. An agent wrote the example from the docs, not an outside engineer. |
| [M4 Contributor path](https://github.com/mohanraj00/entelos/issues/13) | Done in `main` on 2026-11-03: `CONTRIBUTING.md`, issue templates, three to five starter issues, a review path for a human contributor without an agent harness, CI on Linux and macOS. | Met on 2026-11-03: PR #69 from an outside contributor merged through the documented path. |
| [M5 Snapshot and announcement](https://github.com/mohanraj00/entelos/issues/14) | A compatibility statement, a changelog, a `v0.1.0` tag, a security review of the approval and record path. A quiet share with five to ten engineers before the public post. On 2026-11-04, the maintainer moved the record format version field to the design work after v0.1.0. On 2026-11-06, the `v0.2.0` tag marked the M6 snapshot for the quiet share. | The tag passes on both operating systems. Known limits are at the top of the README. |
| [M6 Store-neutral core and versioned protocol](https://github.com/mohanraj00/entelos/issues/77) | Done in `main` on 2026-11-06: a technology decision (#85), a record format version (#78), storage port conformance vectors and an in-memory adapter (#79), a qualification runner interface (#80), receipt provenance and idempotent ingest (#81), a fail-closed expiry mode (#82), a versioned wire protocol (#83), a local sidecar (#84), a Go core (#88), the removal of the Python core (#95) and a browser executor (#93). | Met on 2026-11-06 at `bf51d0e`: `verify.py` passes on both operating systems. Both adapters pass the storage vectors. The sidecar passes the protocol vectors. An example runs the full path through the sidecar only. A repeated ingest adds no records. An expired activation fails closed. The browser executor passes the executor vectors in a headless browser. See #77 for the full test. |

A reference to copy needs no package release. A tag gives a stable snapshot that a reader can cite and copy.

No issue here authorizes an unscoped model benchmark, a new domain policy, a public package release or production deployment. Choose the smallest change that completes the issue's stated workflow and preserve its failures/limits.
