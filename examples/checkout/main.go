// Command checkout is a sample that shows how to use jotaro.
// It starts its own mock server and applies load to the flow login → product list → order.
//
//	go run ./examples/checkout            # 20 fixed users (closed)
//	go run ./examples/checkout -rate 50   # 50 scenarios per second (open)
//	go run ./examples/checkout -rate 100 -ramp 8s -duration 12s  # ramp from 0 to 100 over 8 seconds
//	go run ./examples/checkout -rate 100 -duration 12s -stages    # ramp up → hold → ramp down
//	go run ./examples/checkout -raw raw.jsonl                      # also write raw data (JSON Lines)
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"os/signal"
	"strings"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

// item is a product in the mock shop. Price is in the smallest currency unit.
type item struct {
	ID    int `json:"id"`
	Price int `json:"price"`
}

// accounts hands out a different account to each user.
// To read from a CSV, pass the result of jotaro.LoadCSV(f) as is.
var accounts = jotaro.NewFeeder([]map[string]string{
	{"user": "alice"}, {"user": "bob"}, {"user": "carol"},
}, jotaro.Circular)

// checkout is the flow every virtual user runs: log in, list the items, think for a moment, then order one at random.
// Each u.Step shows up under its own name in the report, and a step that returns an error counts as failed.
var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
	var login struct {
		Token string `json:"token"`
	}
	if err := u.Step("login", func(s *jotaro.Step) error {
		creds, ok := accounts.Next()
		if !ok {
			return fmt.Errorf("not enough accounts")
		}
		if err := s.Post("/login").JSON(creds).Do().Decode(&login); err != nil {
			return err
		}
		return nil
	}); err != nil {
		return err
	}

	var items []item
	if err := u.Step("list", func(s *jotaro.Step) error {
		return s.Get("/items").Bearer(login.Token).Do().Decode(&items)
	}); err != nil {
		return err
	}

	u.Think(50 * time.Millisecond)

	return u.Step("order", func(s *jotaro.Step) error {
		pick := items[rand.IntN(len(items))]
		return s.Post("/orders").Bearer(login.Token).
			JSON(map[string]int{"item": pick.ID}).
			Do().ExpectStatus(http.StatusCreated)
	})
})

// main builds a Plan from the flags, runs it against a local mock server, and writes the report.
// Without -rate it uses the closed model (20 fixed users); with -rate it uses the open model
// (constant, -ramp, or -stages). Ctrl-C stops the run early and still prints the results so far.
func main() {
	rate := flag.Int("rate", 0, "scenarios started per second; 0 means the Users model")
	ramp := flag.Duration("ramp", 0, "time to reach -rate; 0 means constant from the start")
	stages := flag.Bool("stages", false, "use -rate, -ramp and -duration to make three stages: ramp up → hold → ramp down")
	duration := flag.Duration("duration", 3*time.Second, "run time")
	format := flag.String("format", "text", "result format (text, json, html)")
	out := flag.String("o", "", "where to write the result; standard output if empty")
	rawOut := flag.String("raw", "", "where to write raw data (JSON Lines); not written if empty")
	flag.Parse()

	f, err := report.ParseFormat(*format)
	if err != nil {
		log.Fatal(err)
	}

	srv := httptest.NewServer(mockShop())
	defer srv.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	plan := jotaro.Plan{
		Scenario: checkout,
		BaseURL:  srv.URL,
		Duration: *duration,
	}
	if *rate > 0 {
		plan.Arrival = jotaro.Rate(*rate, time.Second)
		if *ramp > 0 {
			plan.Arrival = jotaro.Ramp(0, *rate, time.Second, *ramp)
		}
	} else {
		plan.Users = 20
	}
	if *rate > 0 && *stages {
		// ramp up and down in the first and last quarter, and hold steady in the middle half.
		q := *duration / 4
		plan.Arrival = jotaro.Stages(
			jotaro.Stage{Arrival: jotaro.Ramp(0, *rate, time.Second, q), For: q},
			jotaro.Stage{Arrival: jotaro.Rate(*rate, time.Second), For: 2 * q},
			jotaro.Stage{Arrival: jotaro.Ramp(*rate, 0, time.Second, q), For: q},
		)
	}
	if *rawOut != "" {
		file, err := os.Create(*rawOut)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		plan.Raw = file
	}
	rep, err := jotaro.Run(ctx, plan)
	if err != nil {
		log.Fatal(err)
	}

	w := io.Writer(os.Stdout)
	if *out != "" {
		file, err := os.Create(*out)
		if err != nil {
			log.Fatal(err)
		}
		defer file.Close()
		w = file
	}
	if err := rep.Write(w, f); err != nil {
		log.Fatal(err)
	}
}

// mockShop is an API that is a bit slow and sometimes fails an order.
func mockShop() http.Handler {
	delay := func(min, max time.Duration) {
		time.Sleep(min + rand.N(max-min))
	}
	authed := func(w http.ResponseWriter, r *http.Request) bool {
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer t-") {
			w.WriteHeader(http.StatusUnauthorized)
			return false
		}
		return true
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /login", func(w http.ResponseWriter, r *http.Request) {
		delay(5*time.Millisecond, 20*time.Millisecond)
		json.NewEncoder(w).Encode(map[string]string{"token": fmt.Sprintf("t-%d", rand.Int())})
	})
	mux.HandleFunc("GET /items", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		delay(2*time.Millisecond, 10*time.Millisecond)
		json.NewEncoder(w).Encode([]item{{1, 100}, {2, 250}, {3, 980}})
	})
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if !authed(w, r) {
			return
		}
		delay(10*time.Millisecond, 60*time.Millisecond)
		if rand.IntN(100) < 5 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusCreated)
	})
	return mux
}
