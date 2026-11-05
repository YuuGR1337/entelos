# Agent workflow

Read [CONSTITUTION.md](CONSTITUTION.md), this file, then [docs/STATUS.md](docs/STATUS.md). Use [docs/BACKLOG.md](docs/BACKLOG.md) and GitHub issues for current work. Read only the code/evidence needed for the selected issue.

## Shared harness setup

Codex reads this entry point. `CLAUDE.md` and `GEMINI.md` import this file and the constitution, for Claude Code and agy (Antigravity). Shared skills live in `.agents/skills/`; `.claude/skills` is a relative link to that directory. Edit shared instructions only at their owning path.

For contract, lifecycle, integration or reproduction work, read `.agents/skills/decision-workflow/SKILL.md`. No model account is needed for the deterministic checks. This structure does not claim equivalent model performance across harnesses.

## Layout

- `src/`: the reusable reference (`cel-handoff` Go host, `cel-lifecycle` lifecycle specs, conformance vectors and fixtures, `lifecycle-core` Go lifecycle core with its Python callers in `client/`, `protocol` wire protocol).
- `examples/`: consumer examples that reuse `src/` without copies.
- `tests/`: cross-component boundary tests.
- `tools/`: project checks, change classification, PR gates and cross-harness review.
- `docs/`: status, backlog, contracts, support, integration, provenance, review rules and brand.
- `licenses/`: third-party notices and dependency licence texts.

## Work and verification

Start from a GitHub issue with a concrete workflow and completion criteria. Inspect the working tree and preserve unrelated edits. Use a task branch for subsequent engineering work; Codex defaults to `codex/<topic>`. Initial repository bootstrap may create `main` directly.

Map the change to its question before adding machinery. Keep authoring and calling usable through explicit contracts and structured diagnostics. Separate domain projection/authority/policy from reusable mechanics. Use independently stated expectations; never label a candidate's own output as its oracle.

Run the checks relevant to the change. For project docs/skills/layout:

```sh
python3 -B tools/check_project.py
```

For runtime, profile, lifecycle, fixture or dependency changes, use the full runner with a fresh external work directory:

```sh
python3 -B verify.py --work "$TASK_WORK"
```

The runner builds pinned public dependencies, runs Go tests/vet, Python checks, lifecycle conformance, regressions and both consumer examples. It retains generated results under the supplied work directory. Keep caches, binaries, logs and generated stores outside the source checkout. Use the host's configured local scratch directory; avoid cloud-synced folders for temporary work. Use `python3 -B` to avoid source-side bytecode.

Preserve failed runs. Record a changed method and its reason; call a repaired same-fixture repeat what it is. A dependency/profile change requires fresh build identity, qualification and relevant checks. Never carry a passed flag onto changed bytes.

## Review

A harness does not review its own change. Ask the other harness for a review in a PR comment on GitHub: Claude Code writes `@codex review`; Codex and agy write `@claude review this PR. Use the Review guidelines in AGENTS.md.` If that reviewer is not available, run the local agy fallback, `python3 -B tools/cross_review.py`. A security-relevant change also needs a security review. [docs/REVIEW.md](docs/REVIEW.md) owns the request rules, the fallback, fork PR acceptance and the CI scope.

## Review guidelines

Flag these as high priority in a PR review:

- A change that weakens a decision contract, an approval, an activation date or the record of an earlier answer.
- A check that labels a candidate's own output as its oracle, or a passed flag carried onto changed bytes.
- A lost failed run or a removed failure case.
- A content hash used as a signature or as an authority.
- Private application material, customer data, credentials or generated binaries in the repo.
- An unpinned action or dependency, or a dependency without a recorded licence.

End a code review with one line. Write `Security review: needed`, with the reason, if `tools/classify_change.py` marks a changed path as security-relevant. Else write `Security review: not needed`. A security review does not end with this line.

## Records and publication

GitHub issues own live status. At completion, comment with the commit, checks, limits and exact next task. Update `docs/STATUS.md` and `docs/BACKLOG.md` when state changes. Do not store raw run trees in the repository.

`checksums.json` inventories regular source files except itself. After an intended source edit, regenerate it with `python3 -B tools/check_project.py --write-checksums`, review the diff, then run the project check. That command records bytes; it does not validate their behavior or authorize publication. The only source symlink is `.claude/skills` to the internal shared skill directory.

Obtain specific maintainer authorization for public pushes, tags/releases, package publication and outreach. An engineering task does not authorize promotion or a change to any other repository. Never publish private application material or claim unsupported production guarantees. Report security concerns through [SECURITY.md](SECURITY.md).

Write short, concrete English. Explain the behavior, checks and limits. Public writing uses first person where appropriate. Avoid em dashes, filler and invented quality claims.
