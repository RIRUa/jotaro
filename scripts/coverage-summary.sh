#!/bin/sh
# Print a Markdown table of statement coverage per package from a Go coverage profile.
#
# Usage: scripts/coverage-summary.sh [coverage.out]   (default: coverage.out)
# CI appends the output to the job summary; run `go test -coverprofile=coverage.out ./...` first to use it locally.
set -eu

profile=${1:-coverage.out}
if [ ! -f "$profile" ]; then
	echo "no coverage profile at $profile" >&2
	exit 1
fi

# A profile with only the "mode:" line has no statements (for example when no package was tested).
if [ "$(wc -l <"$profile")" -lt 2 ]; then
	echo "No coverage data in $profile."
	exit 0
fi

# Profile lines look like "module/pkg/file.go:12.3,15.2 <statements> <count>"; the first line is "mode: ...".
awk '
	NR == 1 { next }
	{
		file = $1
		sub(/:[^:]*$/, "", file)
		pkg = file
		sub(/\/[^\/]*$/, "", pkg)
		total[pkg] += $2
		total_all += $2
		if ($3 > 0) { covered[pkg] += $2; covered_all += $2 }
	}
	END {
		print "| Package | Statements | Coverage |"
		print "| --- | ---: | ---: |"
		n = 0
		for (p in total) names[++n] = p
		# Insertion sort keeps this portable across awk implementations.
		for (i = 2; i <= n; i++) {
			v = names[i]
			for (j = i - 1; j >= 1 && names[j] > v; j--) names[j + 1] = names[j]
			names[j + 1] = v
		}
		for (i = 1; i <= n; i++) {
			p = names[i]
			printf "| `%s` | %d | %.1f%% |\n", p, total[p], 100 * covered[p] / total[p]
		}
		if (total_all > 0) printf "| **total** | %d | **%.1f%%** |\n", total_all, 100 * covered_all / total_all
	}
' "$profile"
