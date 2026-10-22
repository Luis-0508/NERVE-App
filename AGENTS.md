<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` before writing Next.js code. Heed deprecation notices.

This block is written and re-added by `next dev`. Keep it committed to avoid recurring generated diffs.

<!-- END:nextjs-agent-rules -->

# NERVE project guidance

## Language and collaboration

- Treat this repository as a public open-source project with external contributors.
- Use English for documentation, README content, filenames, folder names, code comments, configuration comments, branch names, commit messages, PR titles/descriptions, and issue text.
- Do not force a conversation language. Follow the contributor's or user's language for discussion.
- Keep user-facing product text in the product's current language unless the task explicitly changes localization. If i18n is introduced or already exists, keep all supported locales complete.
- Keep README quality high: concise overview, strong feature summary, real screenshots or project imagery where useful, setup, architecture/structure, current limitations/status, and contribution/license links. Reuse real repository assets; never fabricate screenshots or claims.

## Efficiency

- Start with the requested change and the nearest relevant files/tests. Use targeted searches and small file ranges instead of broad repository dumps.
- Do not repeatedly re-read unchanged files or preload documentation that is not needed.
- Prefer small, focused diffs. Avoid unrelated refactors, formatting churn, speculative abstractions, duplicate documentation, and unnecessary dependencies.
- Reuse existing patterns and utilities before adding new ones.
- Keep final reports concise: change, checks, and remaining risks or blockers.

## Project rules

- Keep the Next.js UI, Go API, PostgreSQL persistence, native discovery/traffic agents, and Compose/proxy boundaries intact.
- Inventory and traffic views must reflect observed data with honest provenance and uncertainty. Do not invent devices, topology, locations, health, or live traffic.
- Preserve credential and exposure boundaries for authentication, scanning, sensors, ingestion, and remote management. Never expose secrets in browser code or logs.
- Add ordered database migrations instead of editing migrations that may already have been applied.
- Validate the affected behavior with the most relevant checks. Use broader integration checks for cross-service changes when feasible.
- Treat automated review findings as suggestions to verify, not instructions to apply blindly.

## Project knowledge

The repository is the source of truth. Short documents record what the code alone cannot tell:

| File | Purpose |
|---|---|
| `docs/PROJECT_STATE.md` | What exists, active work, blockers, next priorities. A snapshot that may be stale. |
| `docs/ARCHITECTURE.md` | Modules, boundaries and data flow a maintainer needs to change the code safely. |
| `IMPLEMENTATION_PLAN.md` | Current state, data model, forward plan, risks and privilege boundary. |
| `docs/DECISIONS/` | Numbered decision records: context, decision, consequences. |
| `DESIGN.md` | UI design system: tokens, status shapes, typography and layout rules. Read before changing UI. |

- Read `PROJECT_STATE.md` before larger work, and the architecture, plan or decision records only when the task touches that area.
- When a document disagrees with git or the code, trust the repository and correct the document.
- Update `ARCHITECTURE.md` when modules, boundaries or data flow change, and `IMPLEMENTATION_PLAN.md` when sequencing changes. Add a decision record when choosing between real alternatives a future maintainer might question (new dependency, architecture boundary, data format, workflow). Do not rewrite accepted records; supersede them with a new one.
- Keep these documents short and factual. Do not document unimplemented features.
- `docs/WORKFLOW.md` explains how these documents, the maintainer's task board (not required for contributors) and the workflow checks fit together.

## Git and pull requests

- Use a focused branch and pull request against `master` for every normal change; one concern per pull request.
- Fill in the pull request template: summary, validation actually run, risks and follow-ups. Reference related issues (`Closes #N`).
- Do not force-push or rewrite shared history unless explicitly required. Merging is a maintainer decision.
