#!/bin/sh
# Build the agent release files into DIR: one binary per platform plus the
# installers. Builds are reproducible: -trimpath, no VCS stamp and an empty
# build ID make the same commit and toolchain produce identical bytes.
#
#   scripts/release/build-agents.sh v0.1.0 dist
set -eu

version="$1"
dir="$2"
mkdir -p "$dir"
for platform in windows/amd64 linux/amd64 linux/arm64; do
	goos="${platform%/*}"
	goarch="${platform#*/}"
	suffix=""
	[ "$goos" = windows ] && suffix=".exe"
	CGO_ENABLED=0 GOOS="$goos" GOARCH="$goarch" go build -trimpath -buildvcs=false \
		-ldflags="-s -w -buildid= -X main.version=$version" \
		-o "$dir/nerve-agent_${goos}_${goarch}${suffix}" ./cmd/nerve-agent
done
cp scripts/agent/install-windows.ps1 scripts/agent/uninstall-windows.ps1 "$dir/"
install -m 0755 scripts/agent/install-linux.sh scripts/agent/uninstall-linux.sh "$dir/"
