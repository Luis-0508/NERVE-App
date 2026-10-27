# Contributing to NERVE

Read [AGENTS.md](AGENTS.md) for the project rules; they apply to human contributors too.
Setup and the subsystem guides are in the [README](README.md) and [`docs/`](docs/).

Repository-facing collaboration is in English: documentation, filenames, branches, commits,
issues and pull requests. Report suspected vulnerabilities privately as described in
[SECURITY.md](SECURITY.md), and follow the [Code of Conduct](CODE_OF_CONDUCT.md).

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

## Releases

Maintainers cut releases from `master`; see [docs/RELEASING.md](docs/RELEASING.md).

## Optional tooling

The maintainer works with Claude Code (`CLAUDE.md`, `.claude/`) and the project memory in
`docs/`. Planned work is tracked in GitHub issues and milestones; pick an open issue or open
one before larger changes. None of the tooling is required to contribute, and it does not
affect building or testing the project. See [docs/WORKFLOW.md](docs/WORKFLOW.md) for how the
pieces fit together.
