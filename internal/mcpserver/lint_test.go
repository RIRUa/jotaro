package mcpserver

import (
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
	const head = "package main\nimport (\n\"context\"\n\"time\"\n\"github.com/RIRUa/jotaro\"\n)\n"
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
