package mcpserver

import (
	"os"
	"strings"
	"testing"
)

// rules returns the rule names (or sources, for syntax errors) reported for src.
func rules(src string) []string {
	var got []string
	for _, d := range lintScenario(src) {
		if d.Rule != "" {
			got = append(got, d.Rule)
		} else {
			got = append(got, d.Source)
		}
	}
	return got
}

func TestLintScenario(t *testing.T) {
	const head = "package main\nimport (\n\"context\"\n\"fmt\"\n\"time\"\n\"github.com/RIRUa/jotaro\"\n)\n"
	tests := []struct {
		name string
		src  string
		want []string
	}{
		{
			name: "syntax error",
			src:  "package main\nfunc {",
			want: []string{"syntax"},
		},
		{
			name: "valid plan",
			src: head + `var _ = jotaro.Plan{Scenario: nil, Users: 1, Duration: time.Second}
`,
		},
		{
			name: "both Users and Arrival",
			src: head + `var _ = jotaro.Plan{Scenario: nil, Users: 1, Arrival: jotaro.Rate(1, time.Second), Duration: time.Second}
`,
			want: []string{"plan-load-model"},
		},
		{
			name: "valid plan with UserStages",
			src: head + `var _ = jotaro.Plan{Scenario: nil, UserStages: []jotaro.UserStage{{Target: 5, For: time.Second}}, Duration: time.Second}
`,
		},
		{
			name: "both Users and UserStages",
			src: head + `var _ = jotaro.Plan{Scenario: nil, Users: 1, UserStages: []jotaro.UserStage{{Target: 5, For: time.Second}}, Duration: time.Second}
`,
			want: []string{"plan-load-model"},
		},
		{
			name: "neither Users nor Arrival",
			src: head + `var _ = jotaro.Plan{Scenario: nil, Duration: time.Second}
`,
			want: []string{"plan-load-model"},
		},
		{
			name: "missing Duration and Scenario",
			src: head + `var _ = jotaro.Plan{Users: 1}
`,
			want: []string{"plan-duration", "plan-scenario"},
		},
		{
			name: "model assigned after the literal",
			src: head + `func f() {
	plan := jotaro.Plan{Scenario: nil, Duration: time.Second}
	plan.Users = 20
	_ = plan
}
`,
		},
		{
			name: "model assigned in branches, as in examples/checkout",
			src: head + `func f(rate int) {
	plan := jotaro.Plan{Scenario: nil, Duration: time.Second}
	if rate > 0 {
		plan.Arrival = jotaro.Rate(rate, time.Second)
	} else {
		plan.Users = 20
	}
	_ = plan
}
`,
		},
		{
			name: "duration and scenario assigned after a pointer literal",
			src: head + `func f() {
	plan := &jotaro.Plan{Users: 1}
	plan.Duration = time.Second
	plan.Scenarios = []jotaro.Weighted{}
	_ = plan
}
`,
		},
		{
			name: "model missing though another variable is assigned",
			src: head + `func f() {
	plan := jotaro.Plan{Scenario: nil, Duration: time.Second}
	other := jotaro.Plan{Scenario: nil, Duration: time.Second}
	other.Users = 20
	_, _ = plan, other
}
`,
			want: []string{"plan-load-model"},
		},
		{
			name: "duplicate step name",
			src: head + `var _ = jotaro.NewScenario("a", func(ctx context.Context, u *jotaro.User) error {
	u.Step("x", func(s *jotaro.Step) error { return nil })
	return u.Step("x", func(s *jotaro.Step) error { return nil })
})
`,
			want: []string{"duplicate-step-name"},
		},
		{
			name: "same step name in separate scenarios",
			src: head + `var _ = jotaro.NewScenario("a", func(ctx context.Context, u *jotaro.User) error {
	return u.Step("x", func(s *jotaro.Step) error { return nil })
})
var _ = jotaro.NewScenario("b", func(ctx context.Context, u *jotaro.User) error {
	return u.Step("x", func(s *jotaro.Step) error { return nil })
})
`,
		},
		{
			name: "unchecked response",
			src: head + `var _ = jotaro.NewScenario("a", func(ctx context.Context, u *jotaro.User) error {
	return u.Step("x", func(s *jotaro.Step) error {
		s.Get("/x").Do()
		return s.Get("/y").Do().ExpectStatus(200)
	})
})
`,
			want: []string{"unchecked-response"},
		},
		{
			name: "raw client",
			src: head + `var _ = jotaro.NewScenario("a", func(ctx context.Context, u *jotaro.User) error {
	_, err := u.Client.Get("http://x")
	return err
})
`,
			want: []string{"unrecorded-request"},
		},
		{
			name: "aliased import",
			src:  "package main\nimport j \"github.com/RIRUa/jotaro\"\nvar _ = j.Plan{}\n",
			want: []string{"plan-load-model", "plan-duration", "plan-scenario"},
		},
		{
			name: "dot import",
			src:  "package main\nimport . \"github.com/RIRUa/jotaro\"\nvar _ = Plan{}\n",
			want: []string{"plan-load-model", "plan-duration", "plan-scenario"},
		},
		{
			name: "the user's own Plan type",
			src:  "package main\ntype Plan struct{ Name string }\nvar _ = Plan{Name: \"x\"}\n",
		},
		{
			name: "Plan of another package",
			src:  "package main\nimport \"example.com/other\"\nvar _ = other.Plan{}\n",
		},
		{
			name: "the user's own Do",
			src:  "package main\ntype job struct{}\nfunc (job) Do() int { return 1 }\nfunc f(j job) { j.Do() }\n",
		},
		{
			name: "unchecked response through an aliased import",
			src:  "package main\nimport j \"github.com/RIRUa/jotaro\"\nfunc f(s *j.Step) {\n\ts.Get(\"/x\").Do()\n}\n",
			want: []string{"unchecked-response"},
		},
		{
			name: "response discarded with the blank identifier",
			src:  head + "func f(s *jotaro.Step) {\n\t_ = s.Get(\"/x\").Do()\n}\n",
			want: []string{"unchecked-response"},
		},
		{
			name: "response assigned and never checked",
			src:  head + "func f(s *jotaro.Step) {\n\tresp := s.Get(\"/x\").Do()\n\t_ = resp\n}\n",
			want: []string{"unchecked-response"},
		},
		{
			name: "response assigned and checked",
			src:  head + "func f(s *jotaro.Step) error {\n\tresp := s.Get(\"/x\").Do()\n\treturn resp.ExpectStatus(200)\n}\n",
		},
		{
			name: "response assigned and decoded",
			src:  head + "func f(s *jotaro.Step) error {\n\tresp := s.Get(\"/x\").Header(\"a\", \"b\").Do()\n\tvar v any\n\treturn resp.Decode(&v)\n}\n",
		},
		{
			name: "response handed to another function",
			src:  head + "func f(s *jotaro.Step) {\n\tresp := s.Get(\"/x\").Do()\n\tcheck(resp)\n}\nfunc check(*jotaro.Response) {}\n",
		},
		{
			name: "raw client of a user not called u",
			src:  head + "func f(user *jotaro.User) {\n\t_, _ = user.Client.Get(\"http://x\")\n}\n",
			want: []string{"unrecorded-request"},
		},
		{
			name: "Client of something else",
			src:  head + "type holder struct{ Client int }\nfunc f(u holder) int { return u.Client }\n",
		},
		{
			name: "step name built from data",
			src:  head + "func f(u *jotaro.User, id int) error {\n\treturn u.Step(fmt.Sprintf(\"item-%d\", id), func(s *jotaro.Step) error { return nil })\n}\n",
			want: []string{"dynamic-step-name"},
		},
		{
			name: "Step of something else",
			src:  head + "type flow struct{}\nfunc (flow) Step(name string, fn func()) {}\nfunc f(w flow, id int) { w.Step(fmt.Sprint(id), func() {}) }\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := rules(tt.src)
			if strings.Join(got, ",") != strings.Join(tt.want, ",") {
				t.Errorf("rules = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestLintCheckoutExample pins that the way examples/checkout builds its Plan (a literal, then the load model
// assigned in branches) is accepted, since the guide points readers at that example.
func TestLintCheckoutExample(t *testing.T) {
	src, err := os.ReadFile("../../examples/checkout/main.go")
	if err != nil {
		t.Skip("examples/checkout is not available:", err)
	}
	for _, d := range lintScenario(string(src)) {
		if d.Severity == "error" || d.Rule == "plan-load-model" {
			t.Errorf("examples/checkout should lint clean of load-model problems, got %+v", d)
		}
	}
	// With type information the whole example passes without a finding.
	if out := typedCheck(t, string(src)); !out.OK || len(out.Diagnostics) != 0 {
		t.Errorf("examples/checkout should pass check_scenario cleanly, got %+v", out)
	}
}

// TestLintFallbackWarnsOnly pins that without type information nothing is certain, so every finding is a warning.
func TestLintFallbackWarnsOnly(t *testing.T) {
	diags := lintScenario("package main\nimport \"github.com/RIRUa/jotaro\"\nvar _ = jotaro.Plan{}\n")
	if len(diags) != 3 {
		t.Fatalf("want 3 diagnostics, got %+v", diags)
	}
	for _, d := range diags {
		if d.Severity != "warning" {
			t.Errorf("the syntactic fallback must not report errors, got %+v", d)
		}
	}
}
