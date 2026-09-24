<!-- BEGIN:nextjs-agent-rules -->

# This is NOT the Next.js you know

This version has breaking changes — APIs, conventions, and file structure may all differ from your training data. Read the relevant guide in `node_modules/next/dist/docs/` (resolved from this file's directory; in monorepos the `next` package may not be visible from the repo root) before writing any code. Heed deprecation notices.

This block is written and re-added by `next dev` — verify at `node_modules/next/dist/server/lib/generate-agent-files.js`. Removing it from a diff only re-creates the uncommitted change; committing it with your work keeps the tree clean.

<!-- END:nextjs-agent-rules -->

## NERVE project guidance

- Start with the requested change and its adjacent code/tests. Use `README.md` for orientation only when needed; open the relevant `docs/` page for the subsystem being changed, not the entire documentation tree.
- Keep the Next.js UI, Go API, PostgreSQL persistence, native discovery/traffic agents, and Compose/proxy boundaries intact. Check relevant contracts when changing behavior across boundaries.
- Inventory and traffic views must reflect observed data, with provenance and honest uncertainty. Do not introduce fake devices, topology, locations, or live traffic.
- For authentication, network scanning, sensors, and ingestion changes, preserve existing exposure and credential boundaries. Never put secrets in browser code or logs, or publish loopback-only ingestion through the proxy.
- Add new ordered database migrations rather than editing migrations that may already have been applied; their checksums are verified.
- Prefer small, focused diffs. Avoid unrelated rewrites, formatting churn, or new dependencies unless the task requires them.
- Validate affected behavior with relevant checks and tests. Use broader integration checks for cross-service work when feasible; mention any checks that could not run and why.
- Keep the final report brief: change, checks, and outstanding risks or blockers.
