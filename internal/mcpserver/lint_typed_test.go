package mcpserver

import (
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// typedCheck runs check_scenario on src against this repository's jotaro, so the lint runs with type information.
func typedCheck(t *testing.T, src string) CheckOutput {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go command is not available")
	}
	_, out, err := checkScenario("dev")(context.Background(), nil, CheckInput{Code: src, Dir: repoRoot})
	if err != nil {
		t.Fatal(err)
	}
	if !out.TypeChecked || out.Note != "" {
		t.Fatalf("want a type-checked run with type information, got %+v", out)
	}
	return out
}

// lintRules returns "severity rule" of each lint diagnostic, and any build diagnostics as they are.
func lintRules(out CheckOutput) []string {
	var got []string
	for _, d := range out.Diagnostics {
		if d.Source == "lint" {
			got = append(got, d.Severity+" "+d.Rule)
		} else {
			got = append(got, d.Source+": "+d.Message)
		}
	}
	return got
}

// The cases of issue #132: the rules match jotaro's types, so they follow the code through aliases and variables and
// ignore look-alikes.
func TestLintTyped(t *testing.T) {
	const imports = "package main\nimport (\n\"context\"\n\"fmt\"\n\"time\"\n\"github.com/RIRUa/jotaro\"\n)\nvar (\n_ = fmt.Sprint\n_ = time.Second\n_ context.Context\n_ *jotaro.Plan\n)\nfunc main() {}\n"
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "aliased import",
			src:  "package main\nimport j \"github.com/RIRUa/jotaro\"\nvar _ = j.Plan{}\nfunc main() {}\n",
			want: []string{"error plan-load-model", "error plan-duration", "error plan-scenario"},
		},
		{
			name: "dot import",
			src:  "package main\nimport . \"github.com/RIRUa/jotaro\"\nvar _ = Plan{}\nfunc main() {}\n",
			want: []string{"error plan-load-model", "error plan-duration", "error plan-scenario"},
		},
		{
			name: "type alias of Plan",
			src:  imports + "type P = jotaro.Plan\nvar _ = P{}\n",
			want: []string{"error plan-load-model", "error plan-duration", "error plan-scenario"},
		},
		{
			name: "Plan elided in a slice literal",
			src:  imports + "var _ = []jotaro.Plan{{}}\n",
			want: []string{"error plan-load-model", "error plan-duration", "error plan-scenario"},
		},
		{
			name: "the user's own Plan type",
			src:  "package main\ntype Plan struct{ Name string }\nvar _ = Plan{Name: \"x\"}\nfunc main() {}\n",
		},
		{
			name: "two variables of the same name keep their own assignments",
			src: imports + `func a() {
	plan := jotaro.Plan{Scenario: nil, Duration: time.Second}
	_ = plan
}
func b() {
	plan := jotaro.Plan{Scenario: nil, Duration: time.Second}
	plan.Users = 1
	_ = plan
}
`,
			want: []string{"error plan-load-model"},
		},
		{
			name: "literal setting two models",
			src:  imports + "var _ = jotaro.Plan{Scenario: nil, Users: 1, Arrival: jotaro.Rate(1, time.Second), Duration: time.Second}\n",
			want: []string{"error plan-load-model"},
		},
		{
			name: "literal setting two models, assigned again later",
			src:  imports + "func f() {\n\tplan := jotaro.Plan{Scenario: nil, Users: 1, Arrival: jotaro.Rate(1, time.Second), Duration: time.Second}\n\tplan.Users = 0\n}\n",
			want: []string{"warning plan-load-model"},
		},
		{
			name: "response assigned and never checked",
			src:  imports + "func f(s *jotaro.Step) {\n\tresp := s.Get(\"/x\").Do()\n\t_ = resp\n}\n",
			want: []string{"warning unchecked-response"},
		},
		{
			name: "response discarded with the blank identifier",
			src:  imports + "func f(s *jotaro.Step) {\n\t_ = s.Get(\"/x\").Do()\n}\n",
			want: []string{"warning unchecked-response"},
		},
		{
			name: "response only read with JSON",
			src:  imports + "func f(s *jotaro.Step) {\n\tresp := s.Get(\"/x\").Do()\n\t_, _ = resp.JSON(\"a\")\n}\n",
			want: []string{"warning unchecked-response"},
		},
		{
			name: "request built in a variable",
			src:  imports + "func f(s *jotaro.Step) {\n\treq := s.Get(\"/x\")\n\treq.Do()\n}\n",
			want: []string{"warning unchecked-response"},
		},
		{
			name: "response checked later",
			src:  imports + "func f(s *jotaro.Step) error {\n\tresp := s.Get(\"/x\").Do()\n\treturn resp.ExpectStatus(200)\n}\n",
		},
		{
			name: "response status read through a field",
			src:  imports + "func f(s *jotaro.Step) bool {\n\tresp := s.Get(\"/x\").Do()\n\treturn resp.Code == 200\n}\n",
		},
		{
			name: "the user's own Do",
			src:  imports + "type job struct{}\nfunc (job) Do() int { return 1 }\nfunc f(j job) {\n\tj.Do()\n\t_ = j.Do()\n}\n",
		},
		{
			name: "Do of another jotaro type is not a request",
			src:  imports + "type doer interface{ Do() *jotaro.Response }\nfunc f(d doer) {\n\td.Do()\n}\n",
		},
		{
			name: "raw client of a user not called u",
			src:  imports + "func f(user *jotaro.User) {\n\t_, _ = user.Client.Get(\"http://x\")\n}\n",
			want: []string{"warning unrecorded-request"},
		},
		{
			name: "step name built from data",
			src:  imports + "func f(u *jotaro.User, id int) error {\n\treturn u.Step(fmt.Sprintf(\"item-%d\", id), func(s *jotaro.Step) error { return nil })\n}\n",
			want: []string{"warning dynamic-step-name"},
		},
		{
			name: "step name from a constant",
			src:  imports + "const name = \"item\"\nfunc f(u *jotaro.User) error {\n\treturn u.Step(name, func(s *jotaro.Step) error { return nil })\n}\n",
		},
		{
			name: "duplicate step name through a constant",
			src: imports + `const name = "item"
func f(u *jotaro.User) error {
	_ = u.Step(name, func(s *jotaro.Step) error { return nil })
	return u.Step("item", func(s *jotaro.Step) error { return nil })
}
`,
			want: []string{"warning duplicate-step-name"},
		},
		{
			name: "Step of something else",
			src:  imports + "type flow struct{}\nfunc (flow) Step(name string, fn func()) {}\nfunc f(w flow, id int) { w.Step(fmt.Sprint(id), func() {}) }\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := lintRules(typedCheck(t, tt.src))
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("diagnostics = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLintTypedFallsBack pins that code that does not compile is linted syntactically, with warnings only, next to
// the build errors.
func TestLintTypedFallsBack(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go command is not available")
	}
	src := "package main\nimport \"github.com/RIRUa/jotaro\"\nvar _ = jotaro.Plan{}\nvar _ int = \"x\"\nfunc main() {}\n"
	_, out, err := checkScenario("dev")(context.Background(), nil, CheckInput{Code: src, Dir: repoRoot})
	if err != nil {
		t.Fatal(err)
	}
	var builds, lints int
	for _, d := range out.Diagnostics {
		switch d.Source {
		case "build":
			builds++
		case "lint":
			lints++
			if d.Severity != "warning" {
				t.Errorf("a lint finding without type information must be a warning, got %+v", d)
			}
		}
	}
	if out.OK || builds == 0 || lints != 3 {
		t.Errorf("want build errors and three lint warnings, got %+v", out)
	}
}

// TestTypeInfoSurvivesNewerExportData pins that export data written by a newer Go than the one this binary was built
// with (which makes the importer panic) is reported as an error, not a crash, so check_scenario falls back to the
// syntactic lint.
func TestTypeInfoSurvivesNewerExportData(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("the go command is not available")
	}
	exports, err := listExports(context.Background(), repoRoot, []string{"list", "-export", "-deps", "-json=ImportPath,Export", "."})
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(exports[jotaroModule])
	if err != nil {
		t.Fatal(err)
	}
	// The unified export format starts with "\n$$B\nu" and a little-endian uint32 version.
	const magic = "\n$$B\nu"
	i := strings.Index(string(data), magic)
	if i < 0 {
		t.Skip("the export data is not in the unified format")
	}
	i += len(magic)
	data[i], data[i+1], data[i+2], data[i+3] = 99, 0, 0, 0
	bumped := t.TempDir() + "/jotaro.a"
	if err := os.WriteFile(bumped, data, 0o600); err != nil {
		t.Fatal(err)
	}
	exports[jotaroModule] = bumped

	fset, f, diags := parseScenario("package main\nimport \"github.com/RIRUa/jotaro\"\nvar _ = jotaro.Plan{}\n")
	if f == nil {
		t.Fatal(diags)
	}
	info, err := typeInfo(fset, f, exports, nil)
	if err == nil || info != nil || !strings.Contains(err.Error(), "type information is unavailable") {
		t.Errorf("want an error, got info=%v err=%v", info != nil, err)
	}
}
