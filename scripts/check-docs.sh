#!/bin/sh
# Run the documentation checks (internal/doccheck): English/Japanese parity, Go snippets that compile and
# relative links. The same script runs in CI (.github/workflows/docs.yml); see "Documentation checks" in
# CONTRIBUTING.md.
#
# Usage: scripts/check-docs.sh [go test flags]   e.g. scripts/check-docs.sh -run Parity -v
set -eu

cd "$(dirname "$0")/.."
# The snippet check builds a temporary module, so plain `go test ./...` skips it unless this is set.
DOCCHECK_COMPILE=1 exec go test -count=1 "$@" ./internal/doccheck
