# Decision records

Short records of significant technical decisions: what was decided, why, and
what it costs. They are written when a decision is made and are not rewritten
later; a changed decision gets a new record that supersedes the old one.

Earlier decisions (component boundaries, data model, privilege boundary, delivery order) are
described in [IMPLEMENTATION_PLAN.md](../../IMPLEMENTATION_PLAN.md) and the subsystem pages
in `docs/`.

Name files `NNNN-short-title.md`, numbered in order, and add each record to
the table.

| # | Decision | Status |
|---|---|---|
| [0001](0001-local-kanban-board-and-project-memory.md) | Local Kanban board and project memory for development | Superseded by 0005 |
| [0002](0002-ui-design-system-and-plex-fonts.md) | UI design system with self-hosted IBM Plex | Accepted |
| [0003](0003-release-signing.md) | Ed25519-signed release manifests | Accepted |
| [0004](0004-ieee-oui-registry.md) | Embedded IEEE registry for MAC vendor lookup | Accepted |
| [0005](0005-github-issues-for-planned-work.md) | GitHub issues for planned work | Accepted |
| [0006](0006-meshcentral-for-interactive-sessions.md) | MeshCentral for interactive remote sessions | Accepted |

## Template

```markdown
# NNNN. Title

- Status: Proposed | Accepted | Superseded by NNNN
- Date: YYYY-MM-DD

## Context
What problem or force made a decision necessary.

## Decision
What we do.

## Alternatives considered
The real options that were rejected, and why.

## Consequences
What becomes easier, what becomes harder, what to watch.
```
