#!/bin/sh
# Turn the [Unreleased] section of CHANGELOG.md into a dated version section.
#
# Usage: scripts/prepare-release.sh 0.1.0 [YYYY-MM-DD]
# Run it on a release/vX.Y.Z branch, commit the result and open a pull request to main.
# Merging that pull request tags vX.Y.Z and publishes the release (.github/workflows/release.yml).
set -eu

if [ $# -lt 1 ] || [ $# -gt 2 ]; then
	echo "usage: $0 <X.Y.Z> [YYYY-MM-DD]" >&2
	exit 2
fi
version=${1#v}
date=${2:-$(date +%Y-%m-%d)}

case $version in
*[!0-9.]* | *..* | .* | *. | *.*.*.*) bad=1 ;;
*.*.*) bad=0 ;;
*) bad=1 ;;
esac
if [ "$bad" -eq 1 ]; then
	echo "version must look like X.Y.Z, got: $1" >&2
	exit 2
fi

cd "$(dirname "$0")/.."

if grep -q "^## \[$version\]" CHANGELOG.md; then
	echo "CHANGELOG.md already has a section for [$version]" >&2
	exit 1
fi
if ! grep -q '^## \[Unreleased\]' CHANGELOG.md; then
	echo "CHANGELOG.md has no [Unreleased] section" >&2
	exit 1
fi

# An empty [Unreleased] would produce a version heading with no body, which the release workflow rejects.
if [ -z "$(awk '
	index($0, "## [Unreleased]") == 1 { found = 1; next }
	found && index($0, "## [") == 1 { exit }
	found && NF && $0 !~ /^###/ { print; exit }
' CHANGELOG.md)" ]; then
	echo "CHANGELOG.md [Unreleased] has no entries; nothing to release" >&2
	exit 1
fi

tmp=$(mktemp)
trap 'rm -f "$tmp" "$tmp.refs"' EXIT
# Keep an empty [Unreleased] on top and put the version heading right under it.
awk -v ver="$version" -v date="$date" '
	{ print }
	index($0, "## [Unreleased]") == 1 { printf "\n## [%s] - %s\n", ver, date }
' CHANGELOG.md >"$tmp"
# Keep the link references at the bottom (Keep a Changelog) in step: [Unreleased] now compares with
# the new version, and the new version compares with the one that was "Unreleased" before.
# Files without an [Unreleased] reference are left as they are.
awk -v ver="$version" '
	/^\[Unreleased\]: .*\/compare\/v[0-9.]+\.\.\.HEAD$/ {
		base = $0
		sub(/\/compare\/.*$/, "", base)
		sub(/^\[Unreleased\]: /, "", base)
		prev = $0
		sub(/^.*\/compare\/v/, "", prev)
		sub(/\.\.\.HEAD$/, "", prev)
		printf "[Unreleased]: %s/compare/v%s...HEAD\n", base, ver
		printf "[%s]: %s/compare/v%s...v%s\n", ver, base, prev, ver
		next
	}
	{ print }
' "$tmp" >"$tmp.refs"
mv "$tmp.refs" "$tmp"
cat "$tmp" >CHANGELOG.md

echo "CHANGELOG.md: [Unreleased] -> [$version] - $date"
