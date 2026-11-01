package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormatVersion(t *testing.T) {
	cases := []struct {
		name                         string
		injected, commit, moduleInfo string
		want                         string
	}{
		{"release build", "0.1.1", "0123456789abcdef", "(devel)", "jotaro v0.1.1 (0123456)"},
		{"injected with v prefix", "v0.1.1", "", "", "jotaro v0.1.1"},
		{"go install", "", "", "v0.1.1", "jotaro v0.1.1"},
		{"go build", "", "", "(devel)", "jotaro dev"},
		{"no build info", "", "", "", "jotaro dev"},
		{"short commit", "0.1.1", "abc", "", "jotaro v0.1.1 (abc)"},
	}
	for _, c := range cases {
		if got := formatVersion(c.injected, c.commit, c.moduleInfo); got != c.want {
			t.Errorf("%s: formatVersion = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestVersionCommand(t *testing.T) {
	for _, arg := range []string{"version", "-version", "--version"} {
		var stdout, stderr bytes.Buffer
		if code := run([]string{arg}, nil, &stdout, &stderr); code != 0 {
			t.Fatalf("%s: code = %d, stderr = %s", arg, code, stderr.String())
		}
		if !strings.HasPrefix(stdout.String(), "jotaro ") || strings.Count(stdout.String(), "\n") != 1 {
			t.Errorf("%s: stdout = %q, want one line starting with %q", arg, stdout.String(), "jotaro ")
		}
	}
}
