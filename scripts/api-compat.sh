#!/bin/sh
# Compare the exported API of the working tree with the last release and fail on an incompatible
# change that is not acknowledged in .github/api-breaks.txt.
#
# Usage: scripts/api-compat.sh [base-tag]
#
# Without an argument the base is the highest stable tag (vX.Y.Z, no pre-release suffix) of the
# repository. Needs git (for the tags), tar and the Go toolchain; golang.org/x/exp/cmd/apidiff is
# fetched at the version pinned below (set GOPROXY if your environment cannot reach the default).
#
# What is compared: every package of the module except internal/ packages (apidiff skips them),
# examples/ and cmd/ (programs, not API). Additions never fail. Everything apidiff reports as
# incompatible fails unless .github/api-breaks.txt has a line for it, see that file.
set -eu

# Pinned so that a new apidiff cannot change the result without a change here. golang.org/x/exp has
# no tags, so this is a pseudo-version; bump it on purpose.
APIDIFF=golang.org/x/exp/cmd/apidiff@v0.0.0-20261121134650-e1094eeca1b5
ACK_FILE=.github/api-breaks.txt

cd "$(dirname "$0")/.."

base=${1:-}
if [ -z "$base" ]; then
	base=$(git tag --list 'v*' --sort=-v:refname | grep -E '^v[0-9]+\.[0-9]+\.[0-9]+$' | head -n 1 || true)
fi
if [ -z "$base" ]; then
	echo "api-compat: no release tag (vX.Y.Z) found, nothing to compare with" >&2
	echo "api-compat: in CI, check out with fetch-depth: 0 so that the tags are there" >&2
	exit 1
fi
if ! git rev-parse --verify --quiet "refs/tags/$base" >/dev/null; then
	echo "api-compat: tag $base does not exist" >&2
	exit 2
fi

module=$(go list -m)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT INT TERM

# Export data of the last release, written from a clean copy of its tree.
mkdir "$work/base"
git archive "$base" | tar -x -C "$work/base"
(cd "$work/base" && go run "$APIDIFF" -m -w "$work/base.export" "$module") 2>"$work/base.log" || {
	cat "$work/base.log" >&2
	echo "api-compat: could not read the API of $base" >&2
	exit 2
}

if ! go run "$APIDIFF" -m "$work/base.export" "$module" >"$work/diff.txt" 2>"$work/diff.log"; then
	cat "$work/diff.log" >&2
	echo "api-compat: could not read the API of the working tree (does it compile?)" >&2
	exit 2
fi

# apidiff prints "Incompatible changes:" and "Compatible changes:" sections of "- <what>: <change>"
# lines (always with exit code 0). Sub-packages are prefixed with "./", for example "./record.Raw.X".
section() {
	awk -v want="$1" '
		/^[A-Za-z ]+:$/ { on = ($0 == want ":"); next }
		on && /^- / { print substr($0, 3) }
	' "$work/diff.txt" | grep -v -E '^\./(examples|cmd)/' || true
}
section "Compatible changes" >"$work/compatible.txt"
section "Incompatible changes" >"$work/incompatible.txt"

echo "API of $module compared with the last release $base"
echo
if [ -s "$work/compatible.txt" ]; then
	echo "Compatible changes (fine):"
	sed 's/^/  + /' "$work/compatible.txt"
else
	echo "Compatible changes: none"
fi
echo

# Acknowledgements: "<tag> <identifier> -- <reason>", the identifier being what apidiff prints before
# the first ": " (for example "LoadCSV" or "./record.Raw.Truncated"). Only lines for the current
# base tag count; lines for older tags are history. A line without a reason is an error.
if [ -f "$ACK_FILE" ]; then
	bad=$(grep -v -E '^[[:space:]]*(#|$)' "$ACK_FILE" | grep -v -E '^v[0-9]+\.[0-9]+\.[0-9]+ [^ ].* -- [^ ].*$' || true)
	if [ -n "$bad" ]; then
		echo "api-compat: malformed line(s) in $ACK_FILE (want \"<tag> <identifier> -- <reason>\"):" >&2
		echo "$bad" | sed 's/^/  /' >&2
		exit 2
	fi
	grep -v -E '^[[:space:]]*(#|$)' "$ACK_FILE" | awk -v base="$base" '
		$1 == base { rest = substr($0, length($1) + 2); i = index(rest, " -- "); print substr(rest, 1, i - 1) "\t" substr(rest, i + 4) }
	' >"$work/acks.txt"
else
	: >"$work/acks.txt"
fi

fail=0
: >"$work/used.txt"
if [ -s "$work/incompatible.txt" ]; then
	echo "Incompatible changes:"
	while IFS= read -r line; do
		subject=${line%%: *}
		reason=$(awk -F '\t' -v s="$subject" '$1 == s { print $2; exit }' "$work/acks.txt")
		if [ -n "$reason" ]; then
			echo "  ! $line"
			echo "      acknowledged in $ACK_FILE: $reason"
			echo "$subject" >>"$work/used.txt"
		else
			echo "  ! $line"
			echo "      NOT acknowledged"
			echo "::error title=Incompatible API change::$line (not acknowledged in $ACK_FILE; compared with $base)"
			fail=1
		fi
	done <"$work/incompatible.txt"
	echo
else
	echo "Incompatible changes: none"
	echo
fi

# An acknowledgement that matches nothing is stale (the break was reverted or the identifier was
# misspelled). Warn only, so that reverting a break does not turn the check red.
while IFS="$(printf '\t')" read -r subject _; do
	[ -n "$subject" ] || continue
	if ! grep -qxF "$subject" "$work/used.txt"; then
		echo "::warning title=Stale API acknowledgement::$ACK_FILE has an entry for $base \"$subject\" that matches no incompatible change"
	fi
done <"$work/acks.txt"

if [ "$fail" -ne 0 ]; then
	cat >&2 <<EOF
api-compat: the exported API is not compatible with $base.
If the break is intended, add one line per identifier to $ACK_FILE:
  $base <identifier as printed above, before the first ": "> -- <why it cannot be avoided>
and say so in CHANGELOG.md under "Changed" (see CONTRIBUTING.md).
EOF
	exit 1
fi
echo "api-compat: ok"
