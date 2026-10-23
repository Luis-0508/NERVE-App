# 0003. Ed25519-signed release manifests

- Status: Accepted
- Date: 2026-10-22

## Context

Agents will soon download releases from NERVE Core (K-21) and update themselves (K-19). An
agent must be able to tell, offline and without trusting Core or the network, that a file is
an official build. The maintainer chose GitHub tags and GitHub Releases for publishing and no
paid code-signing certificate (Authenticode).

## Decision

- Releases are SemVer tags `vMAJOR.MINOR.PATCH`. `.github/workflows/release.yml` builds the
  agent for windows/amd64, linux/amd64 and linux/arm64, adds the installers and an SPDX SBOM,
  and writes `manifest.json` with each file's SHA-256 and size (`cmd/nerve-release`).
- `manifest.json.sig` is an Ed25519 signature over the exact manifest bytes, with the key ID
  (base64url SHA-256 of the public key).
- The private key is a 32-byte seed created once with `nerve-release keygen`. It lives only as
  the secret `NERVE_RELEASE_SIGNING_KEY` of the protected GitHub environment `release`, which
  only the publish job uses. The maintainer keeps an offline copy.
- The public key is compiled into the agent (`internal/release/keys.go`). The publish job
  verifies the signed manifest against those pinned keys, so a wrong secret fails before
  anything is published. Releases are drafts until the maintainer publishes them.
- Builds are reproducible (`-trimpath`, no VCS stamp, empty build ID, `SOURCE_DATE_EPOCH`); the
  workflow rebuilds and compares the binaries.

## Alternatives considered

- Sigstore/cosign keyless signing: no long-lived key, but the agent would need a Sigstore
  client and the transparency log to verify, which does not work offline.
- Authenticode or GPG only: Authenticode needs a paid certificate (declined) and covers Windows
  only; GPG needs a separate verifier in every agent.
- Checksums without a signature: anyone who can change the files can change the checksums.

## Consequences

- Rotation: add the new public key next to the old one in `keys.go`, ship agents with both,
  then switch the secret. Remove the old key once no agent depends on it. A leaked key is
  handled the same way, plus revoking it in the next agent release.
- Until the maintainer creates the key and pins it, real releases fail at verification by
  design; dry runs sign with a throwaway key.
- Windows still shows "unknown publisher" for a hand-started binary or MSI; integrity comes
  from the signed manifest, not from Authenticode.
