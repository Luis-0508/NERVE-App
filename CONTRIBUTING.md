# Contributing to NERVE

Read [AGENTS.md](AGENTS.md) for the project rules; they apply to human contributors too.
Setup and the subsystem guides are in the [README](README.md) and [`docs/`](docs/).

## Workflow

1. Branch from `master` with a focused change.
2. Add or update tests for changed behaviour. New database changes are new ordered
   migrations; never edit an applied one.
3. Run the checks below.
4. Open a pull request against `master` and fill in the template. Merging is a maintainer
   decision.

## Checks

```sh
go test ./...
go vet ./...
# Disposable PostgreSQL 17; set NERVE_TEST_DATABASE_URL first.
go test -tags=integration -count=1 -p 1 ./...
npm ci
npm run typecheck
npm run test:map
npm run build
```

CI runs these, an API startup smoke test and the image and agent builds.

## Optional tooling

The maintainer's default workflow uses Claude Code with a local Kanban board (`CLAUDE.md`,
`.claude/`, `.kanban/`) and the project memory in `docs/`.
None of it is required to contribute: the board is plain JSON you can read, and these files
do not affect building or testing the project. Please do not edit `.kanban/` in your pull
requests. See [docs/WORKFLOW.md](docs/WORKFLOW.md) for how the pieces fit together.
