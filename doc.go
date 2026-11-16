// Package jotaro is a library for load-testing HTTP servers with scenarios:
// a flow such as login → list → order, written as plain Go code.
//
// This package is the entry point. It handles scenarios (Scenario / User / Step)
// and how load is applied (Plan / Run). Load is applied either with a fixed number of users (Plan.Users),
// with a number of users that changes over time (Plan.UserStages), or by arrival rate (Plan.Arrival with
// Rate / Ramp / Stages). Plan.Scenarios runs a weighted mix of scenarios instead of a single one.
// The result is returned as a [report.Report]. Writing it to the console, JSON or HTML,
// and checking it against thresholds, is done by the report package;
// raw per-request records are handled by the record package.
//
//	sc := jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
//		return u.Step("login", func(s *jotaro.Step) error {
//			return s.Post("/login").JSON(creds).Do().ExpectStatus(200)
//		})
//	})
//	rep, err := jotaro.Run(ctx, jotaro.Plan{Scenario: sc, BaseURL: url, Users: 20, Duration: time.Minute})
//	rep.Write(os.Stdout, report.FormatText)
package jotaro
