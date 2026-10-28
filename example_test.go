package jotaro_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
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
