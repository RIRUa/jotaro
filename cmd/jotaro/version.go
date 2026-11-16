package main

import (
	"runtime/debug"
	"strings"
)

// version and commit are set at release time through -ldflags ("-X main.version=..."), which
// GoReleaser does by default. They stay empty for "go build" and "go install".
var (
	version string
	commit  string
)

// versionString returns the line printed by "jotaro version". It prefers the value injected at
// release time, then the module version recorded by "go install ...@vX.Y.Z", and falls back to "dev".
func versionString() string {
	return formatVersion(version, commit, moduleVersion())
}

// versionNumber returns the version alone, without the commit: "v0.1.3", or "dev" when it is unknown.
// The MCP server reports it as its own version, so it is always the one "jotaro version" prints.
func versionNumber() string {
	return resolveVersion(version, moduleVersion())
}

// moduleVersion is the main module version recorded in the build info. It is empty without build info.
func moduleVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return ""
}

// resolveVersion picks the version by the order described on versionString. A "(devel)" module
// version, which "go build" records, counts as unknown.
func resolveVersion(injected, moduleVersion string) string {
	v := injected
	if v == "" && moduleVersion != "(devel)" {
		v = moduleVersion
	}
	if v == "" {
		return "dev"
	}
	if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	return v
}

// formatVersion builds the line printed by "jotaro version" from the resolved version and the commit.
func formatVersion(injected, commit, moduleVersion string) string {
	v := resolveVersion(injected, moduleVersion)
	if commit != "" {
		v += " (" + commit[:min(len(commit), 7)] + ")"
	}
	return "jotaro " + v
}
