package jotaro_test

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"time"

	"github.com/RIRUa/jotaro"
)

// A scenario is plain Go code. Run it with a Plan, here against a local test server.
func Example() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login":
			fmt.Fprint(w, `{"token":"secret"}`)
		default:
			w.WriteHeader(http.StatusCreated)
		}
	}))
	defer srv.Close()

	checkout := jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
		var login struct {
			Token string `json:"token"`
		}
		if err := u.Step("login", func(s *jotaro.Step) error {
			return s.Post("/login").JSON(map[string]string{"user": "alice"}).Do().Decode(&login)
		}); err != nil {
			return err // return err to stop; ignore it to continue
		}

		u.Think(10 * time.Millisecond) // waiting time; not counted in latency

		return u.Step("order", func(s *jotaro.Step) error {
			return s.Post("/orders").Bearer(login.Token).
				JSON(map[string]int{"item": 1}).
				Do().ExpectStatus(http.StatusCreated)
		})
	})

	rep, err := jotaro.Run(context.Background(), jotaro.Plan{
		Scenario: checkout,
		BaseURL:  srv.URL,
		Duration: 200 * time.Millisecond,
		Users:    2,
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("ran:", rep.Requests.Count > 0)
	fmt.Println("errors:", len(rep.Requests.Errors))
	// Output:
	// ran: true
	// errors: 0
}

// In the open model, scenarios start at a fixed rate whatever the server does.
// Stages switches the arrival stage by stage: ramp up, hold, ramp down.
func ExamplePlan_open() {
	var scenario *jotaro.Scenario // see the Example above

	plan := jotaro.Plan{
		Scenario: scenario,
		BaseURL:  "http://localhost:8080",
		Duration: 100 * time.Second,
		Arrival: jotaro.Stages(
			jotaro.Stage{Arrival: jotaro.Ramp(0, 50, time.Second, 30*time.Second), For: 30 * time.Second},
			jotaro.Stage{Arrival: jotaro.Rate(50, time.Second), For: 60 * time.Second},
			jotaro.Stage{Arrival: jotaro.Ramp(50, 0, time.Second, 10*time.Second), For: 10 * time.Second},
		),
		MaxUsers: 500, // arrivals beyond this are counted as dropped
	}
	_ = plan // jotaro.Run(ctx, plan)
}

// UserStages changes the number of users over time in the closed model, here from 2 users up to 4 and down to 0.
// StartUsers is how many users are already running when the first stage begins.
func ExamplePlan_userStages() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	ping := jotaro.NewScenario("ping", func(ctx context.Context, u *jotaro.User) error {
		return u.Step("ping", func(s *jotaro.Step) error {
			return s.Get("/").Do().ExpectStatus(http.StatusOK)
		})
	})

	rep, err := jotaro.Run(context.Background(), jotaro.Plan{
		Scenario:   ping,
		BaseURL:    srv.URL,
		StartUsers: 2,
		UserStages: []jotaro.UserStage{
			{Target: 4, For: 100 * time.Millisecond},
			{Target: 4, For: 100 * time.Millisecond},
			{Target: 0, For: 100 * time.Millisecond},
		},
		Duration: 300 * time.Millisecond,
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("ran:", rep.Requests.Count > 0)
	fmt.Println("open model:", rep.Open)
	// Output:
	// ran: true
	// open model: false
}

// RepeatStages repeats a sequence of stages, for a load shape that goes up and down several times.
// Join it with other stages using append.
func ExampleRepeatStages() {
	stages := append(
		[]jotaro.UserStage{{Target: 10, For: 10 * time.Second}}, // warm up
		jotaro.RepeatStages(2,
			jotaro.UserStage{Target: 100, For: 20 * time.Second},
			jotaro.UserStage{Target: 100, For: 30 * time.Second},
			jotaro.UserStage{Target: 10, For: 10 * time.Second},
		)...,
	)
	for _, st := range stages {
		fmt.Println(st.Target, st.For)
	}
	// Output:
	// 10 10s
	// 100 20s
	// 100 30s
	// 10 10s
	// 100 20s
	// 100 30s
	// 10 10s
}

// Scenarios runs a weighted mix of flows in one plan. Every iteration picks one scenario in proportion to its
// weight, and the report keeps the results apart by scenario name, so the names must differ.
func ExamplePlan_scenarios() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()

	visit := func(name, path string) *jotaro.Scenario {
		return jotaro.NewScenario(name, func(ctx context.Context, u *jotaro.User) error {
			return u.Step("visit", func(s *jotaro.Step) error {
				return s.Get(path).Do().ExpectStatus(http.StatusOK)
			})
		})
	}

	rep, err := jotaro.Run(context.Background(), jotaro.Plan{
		Scenarios: []jotaro.Weighted{
			{Scenario: visit("browse", "/products"), Weight: 1}, // 1 : 1 is 50% each; 70 : 30 would be 70% and 30%
			{Scenario: visit("buy", "/orders"), Weight: 1},
		},
		BaseURL:  srv.URL,
		Duration: 200 * time.Millisecond,
		Users:    2,
	})
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	var names []string
	for _, sc := range rep.Scenarios {
		if sc.Iterations.Count > 0 {
			names = append(names, sc.Name)
		}
	}
	sort.Strings(names)
	fmt.Println("scenarios that ran:", names)
	// Output: scenarios that ran: [browse buy]
}

// AbortOn stops a run early when a condition holds, so a broken server is not hit for the whole Duration.
// Run then returns the report up to that point together with an error that wraps ErrAborted.
func ExamplePlan_abortOn() {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable) // a broken server
	}))
	defer srv.Close()

	ping := jotaro.NewScenario("ping", func(ctx context.Context, u *jotaro.User) error {
		return u.Step("ping", func(s *jotaro.Step) error {
			return s.Get("/").Do().ExpectStatus(http.StatusOK)
		})
	})

	rep, err := jotaro.Run(context.Background(), jotaro.Plan{
		Scenario: ping,
		BaseURL:  srv.URL,
		Duration: time.Minute,
		Users:    2,
		AbortOn:  []jotaro.AbortRule{{When: "http_5xx_rate>50%"}}, // For: 0 aborts at the first evaluation that holds
	})
	fmt.Println("aborted:", errors.Is(err, jotaro.ErrAborted))
	fmt.Println("report returned:", rep != nil && rep.Requests.Count > 0)
	// Output:
	// aborted: true
	// report returned: true
}

// JSON reads one value from a JSON body by a dot path, without declaring a struct.
// Object keys are looked up by name and array elements by index. Numbers come back as float64.
func ExampleResponse_JSON() {
	resp := &jotaro.Response{Code: 200, Body: []byte(`{"data":{"items":[{"id":7,"name":"tea"}]}}`)}

	id, _ := resp.JSON("data.items.0.id")
	name, _ := resp.JSON("data.items.0.name")
	_, err := resp.JSON("data.items.1.id")
	fmt.Println(id, name)
	fmt.Println(err)
	// Output:
	// 7 tea
	// jotaro: JSON path "data.items.1.id": bad index "1" (len 1)
}

// ExpectJSON checks the value at a path. A mismatch is an *ExpectError (expect_mismatch), which fails the step.
func ExampleResponse_ExpectJSON() {
	resp := &jotaro.Response{Code: 200, Body: []byte(`{"status":"accepted","count":2}`)}

	fmt.Println(resp.ExpectJSON("status", "accepted"))
	fmt.Println(resp.ExpectJSON("count", 3))
	// Output:
	// <nil>
	// jotaro: unexpected JSON at "count" (want 3, got 2)
}

// A Feeder hands out different data to each user.
func ExampleFeeder() {
	accounts := jotaro.NewFeeder([]map[string]string{
		{"user": "alice"}, {"user": "bob"},
	}, jotaro.Circular)

	for range 3 {
		row, _ := accounts.Next()
		fmt.Println(row["user"])
	}
	// Output:
	// alice
	// bob
	// alice
}

// With Once, rows are handed out once and then Next returns false.
func ExampleFeeder_once() {
	tokens := jotaro.NewFeeder([]string{"t1", "t2"}, jotaro.Once)

	for {
		tok, ok := tokens.Next()
		if !ok {
			break
		}
		fmt.Println(tok)
	}
	// Output:
	// t1
	// t2
}

// LoadCSV reads rows keyed by the column names of the header line.
func ExampleLoadCSV() {
	rows, err := jotaro.LoadCSV(strings.NewReader("user,password\nalice,a1\nbob,b2\n"))
	if err != nil {
		fmt.Println(err)
		return
	}
	feeder := jotaro.NewFeeder(rows, jotaro.Once)
	row, _ := feeder.Next()
	fmt.Println(feeder.Len(), row["user"], row["password"])
	// Output: 2 alice a1
}
