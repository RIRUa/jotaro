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

tmp=$(mktemp)
trap 'rm -f "$tmp"' EXIT
# Keep an empty [Unreleased] on top and put the version heading right under it.
awk -v ver="$version" -v date="$date" '
	{ print }
	index($0, "## [Unreleased]") == 1 { printf "\n## [%s] - %s\n", ver, date }
' CHANGELOG.md >"$tmp"
cat "$tmp" >CHANGELOG.md

echo "CHANGELOG.md: [Unreleased] -> [$version] - $date"
