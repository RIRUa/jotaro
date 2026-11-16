package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIRUa/jotaro"
)

// The checkout flow runs against its mock shop. The mock fails some orders on purpose and the counts depend
// on timing, so the test only asks that every step ran and that no request failed at the network level.
func TestCheckoutRuns(t *testing.T) {
	srv := httptest.NewServer(mockShop())
	t.Cleanup(srv.Close)

	rep, err := jotaro.Run(context.Background(), jotaro.Plan{
		Scenario: checkout,
		BaseURL:  srv.URL,
		Users:    3,
		Duration: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(rep.Scenarios) != 1 || rep.Scenarios[0].Name != "checkout" {
		t.Fatalf("scenarios = %+v, want just checkout", rep.Scenarios)
	}
	var steps []string
	for _, st := range rep.Scenarios[0].Steps {
		steps = append(steps, st.Name)
	}
	if want := []string{"login", "list", "order"}; len(steps) != len(want) || steps[0] != want[0] || steps[1] != want[1] || steps[2] != want[2] {
		t.Errorf("steps = %v, want %v", steps, want)
	}
	if len(rep.Requests.Errors) != 0 {
		t.Errorf("transport errors: %v", rep.Requests.Errors)
	}
}
