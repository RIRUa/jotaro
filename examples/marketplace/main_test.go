package main

import (
	"context"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/RIRUa/jotaro"
)

// runFast runs the scenario at 5% of the real think times against a mock with short-lived sessions.
func runFast(t *testing.T, plan jotaro.Plan) (*config, *market) {
	t.Helper()
	accounts, err := loadMembers()
	if err != nil {
		t.Fatal(err)
	}
	shop := newMarket(300*time.Millisecond, accounts)
	srv := httptest.NewServer(shop.handler())
	t.Cleanup(srv.Close)

	cfg := &config{
		members:   jotaro.NewFeeder(accounts, jotaro.Circular),
		newcomers: jotaro.NewFeeder(newcomerAccounts(10), jotaro.Once),
		pace:      0.05,
		tally:     newTally(),
	}
	plan.Scenario = newScenario(cfg)
	plan.BaseURL = srv.URL
	rep, err := jotaro.Run(context.Background(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if rep.Scenarios[0].Iterations.Failed != 0 {
		t.Errorf("%d of %d iterations failed: %v", rep.Scenarios[0].Iterations.Failed,
			rep.Scenarios[0].Iterations.Count, rep.Scenarios[0].Iterations.Errors)
	}
	return cfg, shop
}

func TestMarketplaceClosed(t *testing.T) {
	cfg, shop := runFast(t, jotaro.Plan{Users: 8, Duration: 2 * time.Second})

	if err := shop.checkInventory(); err != nil {
		t.Error(err)
	}
	if got := cfg.tally.get("outcome", "paid"); got == 0 || int64(got) > shop.stats.OrdersCreated.Load() {
		t.Errorf("paid = %d with %d orders created", got, shop.stats.OrdersCreated.Load())
	}
	// Sessions live 150-450ms and a purchase takes longer, so visitors must have logged in again.
	if cfg.tally.get("outcome", "relogin") == 0 {
		t.Error("no visitor logged in again although sessions expire")
	}
	if int64(cfg.tally.get("outcome", "relogin")) != shop.stats.Expired.Load() {
		t.Errorf("relogin = %d, sessions expired on the server = %d",
			cfg.tally.get("outcome", "relogin"), shop.stats.Expired.Load())
	}
}

func TestMarketplaceOpen(t *testing.T) {
	cfg, shop := runFast(t, jotaro.Plan{
		Arrival:  profile(10, 2*time.Second),
		Duration: 2 * time.Second,
	})

	if err := shop.checkInventory(); err != nil {
		t.Error(err)
	}
	if cfg.tally.get("persona", "window") == 0 || cfg.tally.get("persona", "member") == 0 {
		t.Errorf("personas were not mixed: %v", cfg.tally.lines())
	}
	// Only 10 sign-up accounts exist, and each can be used once.
	accounts, _ := loadMembers()
	shop.mu.Lock()
	defer shop.mu.Unlock()
	if signedUp := len(shop.users) - len(accounts); signedUp > 10 {
		t.Errorf("%d accounts were signed up, only 10 exist", signedUp)
	}
}
