#!/bin/sh
# Install or upgrade the NERVE agent as a systemd service.
#
#   sudo ./install-linux.sh --api-url https://nerve.example/api/v1 --token-file ./token
#   sudo ./install-linux.sh                       # upgrade: keeps configuration and identity
#
# The binary is copied to /usr/local/bin (root-owned, so the service account
# cannot replace it). Other options are passed to `nerve-agent service install`:
# --api-url, --token-file, --ca-file, --force (re-enrollment), --no-start.
set -eu

binary="$(dirname "$0")/nerve-agent"
if [ "${1:-}" = "--binary" ]; then
	binary="$2"
	shift 2
fi

if [ "$(id -u)" -ne 0 ]; then
	echo "Run this script as root (sudo)." >&2
	exit 1
fi
if [ ! -f "$binary" ]; then
	echo "Agent binary not found: $binary (use --binary PATH)." >&2
	exit 1
fi
if ! command -v systemctl >/dev/null 2>&1; then
	echo "systemd was not found. See docs/AGENT_SETUP.md for running the agent manually." >&2
	exit 1
fi

"$binary" version >/dev/null
install -d -o root -g root -m 0755 /usr/local/bin
install -o root -g root -m 0755 "$binary" /usr/local/bin/nerve-agent.new
mv -f /usr/local/bin/nerve-agent.new /usr/local/bin/nerve-agent
echo "Installed /usr/local/bin/nerve-agent ($(/usr/local/bin/nerve-agent version))."

exec /usr/local/bin/nerve-agent service install "$@"
