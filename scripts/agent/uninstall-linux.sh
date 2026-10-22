#!/bin/sh
# Remove the NERVE agent service and binary.
#
#   sudo ./uninstall-linux.sh           # keeps /var/lib/nerve-agent (identity) for a reinstall
#   sudo ./uninstall-linux.sh --purge   # also deletes the identity, configuration and logs
#
# Revoke or remove the agent in NERVE as well.
set -eu

if [ "$(id -u)" -ne 0 ]; then
	echo "Run this script as root (sudo)." >&2
	exit 1
fi
if [ -x /usr/local/bin/nerve-agent ]; then
	/usr/local/bin/nerve-agent service uninstall "$@"
	rm -f /usr/local/bin/nerve-agent
	echo "Removed /usr/local/bin/nerve-agent."
else
	echo "/usr/local/bin/nerve-agent is not installed."
fi
