#!/bin/sh
# Print the body of one version's section in CHANGELOG.md (used as the GitHub Release notes).
#
# Usage: scripts/changelog-section.sh 0.1.0
# Fails when the section is missing or empty, so a release cannot go out without notes.
set -eu

if [ $# -ne 1 ]; then
	echo "usage: $0 <X.Y.Z>" >&2
	exit 2
fi
version=$1

cd "$(dirname "$0")/.."

section=$(awk -v ver="$version" '
	index($0, "## [" ver "]") == 1 { found = 1; next }
	found && index($0, "## [") == 1 { exit }
	found { print }
' CHANGELOG.md)

# Drop the blank lines around the body; a heading with nothing under it counts as empty.
section=$(printf '%s\n' "$section" | sed -e '/./,$!d')
if [ -z "$section" ]; then
	echo "CHANGELOG.md has no (or an empty) section for [$version]" >&2
	exit 1
fi
printf '%s\n' "$section"
