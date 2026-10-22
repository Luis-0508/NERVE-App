# Releasing

NERVE releases are SemVer tags `vMAJOR.MINOR.PATCH` on `master`. A tag runs
`.github/workflows/release.yml`, which builds the agent for windows/amd64, linux/amd64 and
linux/arm64, adds the installers and an SPDX SBOM, writes and signs `manifest.json`, and
opens a **draft** GitHub release. Why and how the signing works: decision
[0003](DECISIONS/0003-release-signing.md).

## One-time setup (maintainer)

1. Create the release key on a trusted machine:

   ```sh
   go run ./cmd/nerve-release keygen -out release-signing.key
   ```

   It prints the public key and its key ID.
2. In GitHub, create the environment `release` (Settings → Environments), restrict it to
   tags `v*` and, if wanted, require a reviewer. Add the content of
   `release-signing.key` as its secret `NERVE_RELEASE_SIGNING_KEY`.
3. Keep an offline backup of the key file, then delete the working copy.
4. Add the public key to `pinnedKeys` in `internal/release/keys.go` in a pull request.
   Releases built before this change cannot be verified by agents, by design.

## Cutting a release

1. In `CHANGELOG.md`, rename `[Unreleased]` to the new version and date, for example
   `## [0.1.0] - 2026-11-02`, and merge that change.
2. Tag the merge commit and push the tag:

   ```sh
   git tag -a v0.1.0 -m "NERVE v0.1.0"
   git push origin v0.1.0
   ```

3. The workflow fails if the changelog section is missing, the build is not reproducible,
   or the signature does not match a pinned key. Otherwise it creates a draft release.
4. Check the draft, then publish it.

To check the pipeline without the real key, run the workflow manually (Actions → Release →
Run workflow). The dry run signs with a throwaway key and only uploads the files as a
workflow artifact.

## Verifying a release

```sh
nerve-agent verify-release ./downloaded-release
```

It checks the signature against the keys compiled into that agent, every file's SHA-256 and
size, and that the release contains a build for the current platform.
`go run ./cmd/nerve-release verify -dir DIR -key BASE64` does the same with an explicit key.
