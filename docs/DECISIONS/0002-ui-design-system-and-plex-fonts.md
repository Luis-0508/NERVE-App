# 0002. UI design system with self-hosted IBM Plex

- Status: Accepted
- Date: 2026-10-22

## Context

The web UI used a generic dark-green dashboard look with a sidebar listing modules that did not
exist yet. The Overview led with system health and left the operator's question, "what needs me
and what changed", below the fold. A design review of a local prototype chose an
attention-first Overview and a stricter visual system (see `DESIGN.md`).

The proxy's content security policy allows fonts only from the app's own origin
(`font-src 'self'`), so a hosted font CDN is not an option at runtime.

## Decision

- `DESIGN.md` describes the design system; `app/globals.css` holds its tokens.
- Colour is reserved for state; actions and chrome are neutral.
- IBM Plex Sans and IBM Plex Mono load through `next/font/google`. Next.js downloads the files at
  build time and serves them from `/_next/static/media`, so the runtime policy is unchanged.

## Alternatives considered

- `@fontsource` packages: offline builds, but two new npm dependencies for files Next.js can
  already self-host.
- Committing the font files with `next/font/local`: offline builds, but binary files and licence
  upkeep in the repository.
- Keeping the system font stack: no download at all, but no dedicated monospace for addresses
  and a less distinct product.

## Consequences

- `npm run build` (and the web Docker image build) needs network access to Google Fonts, in
  addition to the npm registry. Browsers never contact Google.
- Switching to `next/font/local` later only changes `app/layout.tsx`.
