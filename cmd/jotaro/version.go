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
	var moduleVersion string
	if info, ok := debug.ReadBuildInfo(); ok {
		moduleVersion = info.Main.Version
	}
	return formatVersion(version, commit, moduleVersion)
}

// formatVersion picks the version by the order described on versionString. A "(devel)" module
// version, which "go build" records, counts as unknown.
func formatVersion(injected, commit, moduleVersion string) string {
	v := injected
	if v == "" && moduleVersion != "(devel)" {
		v = moduleVersion
	}
	if v == "" {
		v = "dev"
	} else if !strings.HasPrefix(v, "v") {
		v = "v" + v
	}
	if commit != "" {
		v += " (" + commit[:min(len(commit), 7)] + ")"
	}
	return "jotaro " + v
}
