#!/bin/sh
# Print the CHANGELOG.md section for VERSION (without the leading v), for
# release notes. Fails when the section is missing, so a tag cannot be released
# without its changelog entry.
#
#   scripts/release/changelog-section.sh 0.1.0
set -eu

version="$1"
section=$(awk -v heading="## [$version]" '
	index($0, heading) == 1 { found = 1; next }
	found && /^## \[/ { exit }
	found { print }
' CHANGELOG.md)
if [ -z "$(printf '%s' "$section" | tr -d '[:space:]')" ]; then
	echo "CHANGELOG.md has no section for $version" >&2
	exit 1
fi
printf '%s\n' "$section"
