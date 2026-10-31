package main

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/RIRUa/jotaro"
)

// This file is the scenario. The idea is that one scenario function plays several kinds of
// visitors, because real traffic is a mix and the report should show that mix, not one happy path.
//
//	window     browses anonymously and leaves
//	member     logs in with a CSV account and buys
//	newcomer   signs up (one fresh account each, via a Once feeder), uses the welcome coupon and buys
//	regret     buys, then cancels the order
//	abandoner  fills a cart and walks away
//
// Rules this scenario follows, which are worth copying:
//
//   - A step fails only when the system misbehaved. Sold out, an invalid coupon or a declined card
//     are normal business outcomes: the step returns nil and the outcome goes into the tally.
//   - An iteration fails only when a visitor could not finish for a reason that is not business as usual
//     (a failed login, checkout still failing after the retries, a payment that never settles).
//   - Step names are fixed strings, never built from IDs, so each step has one row in the report.

// Limits that shape how patient a simulated visitor is.
const (
	maxCheckoutAttempts = 4
	maxBackoff          = 1500 * time.Millisecond
	maxPaymentPolls     = 12
	maxCartAttempts     = 3
)

// Sentinel errors travel out of u.Step unchanged, so the caller can tell them apart with errors.Is.
var (
	errSessionExpired = errors.New("session expired")
	errPaymentTimeout = errors.New("payment did not settle in time")
)

// config is everything the scenario needs besides the User.
type config struct {
	members   *jotaro.Feeder[map[string]string] // existing accounts, reused round-robin
	newcomers *jotaro.Feeder[map[string]string] // accounts to sign up with, each usable once
	pace      float64                           // multiplies every think time (1 = realistic, 0.05 = fast test)
	tally     *tally
}

// newScenario builds the scenario. It closes over cfg instead of using globals so tests can run it at any speed.
func newScenario(cfg *config) *jotaro.Scenario {
	type persona struct {
		name   string
		weight int
		run    func(sh *shopper) error
	}
	personas := []persona{
		{"window", 40, (*shopper).window},
		{"member", 25, func(sh *shopper) error { return sh.member(intentBuy) }},
		{"newcomer", 15, (*shopper).newcomer},
		{"regret", 10, func(sh *shopper) error { return sh.member(intentBuyThenCancel) }},
		{"abandoner", 10, func(sh *shopper) error { return sh.member(intentAbandon) }},
	}
	total := 0
	for _, p := range personas {
		total += p.weight
	}

	return jotaro.NewScenario("marketplace", func(ctx context.Context, u *jotaro.User) error {
		n := rand.IntN(total)
		for _, p := range personas {
			if n < p.weight {
				cfg.tally.add("persona", p.name)
				return p.run(&shopper{cfg: cfg, u: u})
			}
			n -= p.weight
		}
		panic("unreachable: weights are exhausted")
	})
}

// intent is what a logged-in visitor does once the cart is ready.
type intent int

const (
	intentBuy intent = iota
	intentBuyThenCancel
	intentAbandon
)

// shopper is the state of one visitor during one iteration.
type shopper struct {
	cfg *config
	u   *jotaro.User

	email, password string
	card            string
	csrf            string
	signedUp        bool // signed up in this iteration
}

// think waits like a person: d scaled by the pace, with ±50% jitter. Think time is not part of any latency.
func (sh *shopper) think(d time.Duration) {
	sh.u.Think(time.Duration(float64(d) * sh.cfg.pace * (0.5 + rand.Float64())))
}

// window browses without an account and leaves.
func (sh *shopper) window() error {
	items, err := sh.search()
	if err != nil || len(items) == 0 {
		return err
	}
	sh.think(400 * time.Millisecond)
	for _, p := range items[:min(2, len(items))] {
		if err := sh.view(p.ID); err != nil {
			return err
		}
		sh.think(600 * time.Millisecond)
	}
	return nil
}

// member logs in with an existing account and does what the intent says.
func (sh *shopper) member(in intent) error {
	acc, ok := sh.cfg.members.Next()
	if !ok {
		return errors.New("no member accounts")
	}
	sh.email, sh.password, sh.card = acc["email"], acc["password"], acc["card"]
	if err := sh.signIn("login"); err != nil {
		return err
	}
	return sh.shop(in)
}

// newcomer signs up with an account nobody else uses. Once the accounts run out, the visitor
// has nothing to sign up with and turns into a window shopper, so the load does not stop.
func (sh *shopper) newcomer() error {
	acc, ok := sh.cfg.newcomers.Next()
	if !ok {
		sh.cfg.tally.add("outcome", "newcomers_exhausted")
		return sh.window()
	}
	sh.email, sh.password, sh.card, sh.signedUp = acc["email"], acc["password"], cardOK, true
	if err := sh.u.Step("signup", func(s *jotaro.Step) error {
		return sh.openSession(s.Post("/signup"), http.StatusCreated)
	}); err != nil {
		return err
	}
	return sh.shop(intentBuy)
}

// signIn opens a session with the visitor's credentials. step names the step so that a
// re-login after expiry is reported apart from the first login.
func (sh *shopper) signIn(step string) error {
	return sh.u.Step(step, func(s *jotaro.Step) error {
		return sh.openSession(s.Post("/session"), http.StatusOK)
	})
}

// openSession sends the credentials and keeps the CSRF token. The session cookie itself is stored by
// u.Client's jar, which is new for every iteration: that is how a visitor stays logged in.
func (sh *shopper) openSession(req *jotaro.Request, want int) error {
	resp := req.JSON(map[string]string{"email": sh.email, "password": sh.password}).Do()
	if err := resp.ExpectStatus(want); err != nil {
		return err
	}
	var out struct {
		CSRF string `json:"csrf"`
	}
	if err := resp.Decode(&out); err != nil {
		return err
	}
	sh.csrf = out.CSRF
	return nil
}

// call runs a step that needs a session. If the server says the session has gone,
// the visitor logs in again (its own "relogin" step) and repeats the step once.
// fn is called again as is, so it must read sh.csrf when it runs, not before.
func (sh *shopper) call(name string, fn func(s *jotaro.Step) error) error {
	err := sh.u.Step(name, fn)
	if !errors.Is(err, errSessionExpired) {
		return err
	}
	sh.cfg.tally.add("outcome", "relogin")
	if err := sh.signIn("relogin"); err != nil {
		return err
	}
	return sh.u.Step(name, fn)
}

// write builds a request that changes state: it carries the CSRF token.
func (sh *shopper) write(s *jotaro.Step, method, path string, body any) *jotaro.Request {
	r := s.Request(method, path).Header("X-CSRF-Token", sh.csrf)
	if body != nil {
		r = r.JSON(body)
	}
	return r
}

// expect is ExpectStatus that also recognises an expired session.
func expect(r *jotaro.Response, want int) error {
	if r.Code == http.StatusUnauthorized {
		return errSessionExpired
	}
	return r.ExpectStatus(want)
}

// listing is the part of a product that the visitor looks at.
type listing struct {
	ID    int `json:"id"`
	Price int `json:"price"`
	Stock int `json:"stock"`
}

// search looks for something and reads one to three pages by following next_cursor.
// All pages share the step name "search", so the report shows them as one row.
func (sh *shopper) search() ([]listing, error) {
	terms := []string{"novel", "puzzle", "pan", "camp", "deluxe", ""}
	q := url.Values{"q": {terms[rand.IntN(len(terms))]}, "limit": {"6"}}
	if rand.IntN(4) == 0 {
		q.Set("category", categories[rand.IntN(len(categories))].name)
	}

	var seen []listing
	for page := 0; page < 1+rand.IntN(3); page++ {
		var out struct {
			Items      []listing `json:"items"`
			NextCursor string    `json:"next_cursor"`
		}
		if err := sh.u.Step("search", func(s *jotaro.Step) error {
			return s.Get("/products").Queries(q).Do().Decode(&out)
		}); err != nil {
			return nil, err
		}
		seen = append(seen, out.Items...)
		if out.NextCursor == "" {
			break
		}
		q.Set("cursor", out.NextCursor)
		sh.think(500 * time.Millisecond)
	}
	return seen, nil
}

// view opens a product page and, now and then, comes back to it with If-None-Match.
// 200 and 304 are both fine: which one comes back depends on whether the stock changed meanwhile.
func (sh *shopper) view(id int) error {
	var etag string
	path := "/products/" + strconv.Itoa(id)
	if err := sh.u.Step("product", func(s *jotaro.Step) error {
		r := s.Get(path).Do()
		if err := r.ExpectStatus(http.StatusOK); err != nil {
			return err
		}
		etag = r.Header.Get("ETag")
		return nil
	}); err != nil || rand.IntN(100) >= 40 {
		return err
	}

	sh.think(300 * time.Millisecond)
	return sh.u.Step("product_revalidate", func(s *jotaro.Step) error {
		r := s.Get(path).Header("If-None-Match", etag).Do()
		if r.Code == http.StatusNotModified {
			return nil
		}
		return r.ExpectStatus(http.StatusOK)
	})
}

// shop is the shared body of every buying visitor: find something, fill the cart, then follow the intent.
func (sh *shopper) shop(in intent) error {
	items, err := sh.search()
	if err != nil || len(items) == 0 {
		return err
	}
	// Prefer what is in stock, and shuffle so that visitors do not all fight for the same item.
	rand.Shuffle(len(items), func(i, j int) { items[i], items[j] = items[j], items[i] })
	sort.SliceStable(items, func(i, j int) bool { return items[i].Stock > 0 && items[j].Stock == 0 })

	if err := sh.view(items[0].ID); err != nil {
		return err
	}
	sh.think(800 * time.Millisecond)

	added, err := sh.fillCart(items)
	if err != nil || !added {
		return err
	}
	if err := sh.coupon(); err != nil {
		return err
	}
	if err := sh.reviewCart(); err != nil {
		return err
	}

	if in == intentAbandon {
		sh.cfg.tally.add("outcome", "abandoned")
		sh.think(1500 * time.Millisecond)
		return nil
	}
	sh.think(700 * time.Millisecond)

	orderID, soldOut, err := sh.checkout()
	if err != nil || soldOut {
		return err
	}
	paid, err := sh.payWithRetry(orderID)
	if err != nil || !paid {
		return err
	}

	if err := sh.verifyOrder(orderID, "paid"); err != nil {
		return err
	}
	if in == intentBuyThenCancel {
		sh.think(1000 * time.Millisecond)
		return sh.cancel(orderID)
	}
	if rand.IntN(100) < 30 {
		return sh.history()
	}
	return nil
}

// fillCart tries up to maxCartAttempts products. A 409 means someone else got the last piece:
// that is a business outcome, so the step succeeds and the visitor simply picks the next product.
func (sh *shopper) fillCart(items []listing) (added bool, err error) {
	for _, p := range items[:min(maxCartAttempts, len(items))] {
		err := sh.call("cart_add", func(s *jotaro.Step) error {
			added = false
			r := sh.write(s, http.MethodPost, "/cart/items",
				map[string]int{"product_id": p.ID, "qty": 1 + rand.IntN(2)}).Do()
			if r.Code == http.StatusConflict {
				sh.cfg.tally.add("outcome", "cart_out_of_stock")
				return nil
			}
			if err := expect(r, http.StatusCreated); err != nil {
				return err
			}
			added = true
			return nil
		})
		if err != nil {
			return false, err
		}
		if added {
			return true, nil
		}
		sh.think(200 * time.Millisecond)
	}
	return false, nil
}

// coupon is tried by newcomers always and by others sometimes. A rejected code (422) is normal.
func (sh *shopper) coupon() error {
	if !sh.signedUp && rand.IntN(100) >= 30 {
		return nil
	}
	code := couponWelcome
	if !sh.signedUp {
		code = []string{couponSummer, couponWelcome, "EXPIRED2020"}[rand.IntN(3)]
	}
	return sh.call("coupon", func(s *jotaro.Step) error {
		r := sh.write(s, http.MethodPost, "/cart/coupon", map[string]string{"code": code}).Do()
		if r.Code == http.StatusUnprocessableEntity {
			sh.cfg.tally.add("outcome", "coupon_rejected")
			return nil
		}
		if err := expect(r, http.StatusOK); err != nil {
			return err
		}
		sh.cfg.tally.add("outcome", "coupon_applied")
		return nil
	})
}

// reviewCart reads the cart back and checks the arithmetic, so this step can fail on wrong data and
// not only on wrong status codes.
func (sh *shopper) reviewCart() error {
	return sh.call("cart_view", func(s *jotaro.Step) error {
		var cart struct {
			Subtotal int `json:"subtotal"`
			Discount int `json:"discount"`
			Total    int `json:"total"`
		}
		r := s.Get("/cart").Do()
		if err := expect(r, http.StatusOK); err != nil {
			return err
		}
		if err := r.Decode(&cart); err != nil {
			return err
		}
		if want := cart.Subtotal * (100 - cart.Discount) / 100; cart.Total != want || cart.Total <= 0 {
			return fmt.Errorf("cart total is %d, want %d (subtotal %d, discount %d%%)",
				cart.Total, want, cart.Subtotal, cart.Discount)
		}
		return nil
	})
}

// checkout places the order. It is the request that the server rate limits and sometimes answers with
// a failure after it has already created the order, so the visitor
//
//   - sends one Idempotency-Key for the whole purchase, which makes every retry safe, and
//   - retries 429 and 503 with exponential backoff, at least as long as Retry-After says.
//
// Each attempt is its own "checkout" step, so failed attempts are visible in the report. The wait
// between attempts is Think time, which keeps backoff out of the latencies.
func (sh *shopper) checkout() (orderID string, soldOut bool, err error) {
	key := fmt.Sprintf("%d-%d-%x", sh.u.ID, sh.u.Iteration, rand.Uint64())
	for attempt := 0; attempt < maxCheckoutAttempts; attempt++ {
		var (
			order struct {
				ID string `json:"id"`
			}
			retryAfter time.Duration
		)
		err = sh.call("checkout", func(s *jotaro.Step) error {
			soldOut = false
			r := sh.write(s, http.MethodPost, "/checkout", nil).Header("Idempotency-Key", key).Do()
			switch r.Code {
			case http.StatusConflict:
				soldOut = true // someone bought the last piece between the cart and the checkout
				return nil
			case http.StatusOK:
				sh.cfg.tally.add("outcome", "checkout_replayed") // the first answer was lost; this is the saved one
			default:
				if secs, perr := strconv.Atoi(r.Header.Get("Retry-After")); perr == nil {
					retryAfter = time.Duration(secs) * time.Second
				}
				if err := expect(r, http.StatusCreated); err != nil {
					return err
				}
			}
			return r.Decode(&order)
		})
		var unexpected *jotaro.ExpectError
		switch {
		case err == nil && soldOut:
			sh.cfg.tally.add("outcome", "checkout_sold_out")
			return "", true, nil
		case err == nil:
			return order.ID, false, nil
		case errors.As(err, &unexpected) && (unexpected.Got == http.StatusTooManyRequests || unexpected.Got == http.StatusServiceUnavailable):
			backoff := min(max(retryAfter, 100*time.Millisecond<<attempt), maxBackoff)
			sh.cfg.tally.add("outcome", "checkout_retry")
			sh.think(backoff)
		default:
			return "", false, err
		}
	}
	return "", false, fmt.Errorf("checkout still failing after %d attempts: %w", maxCheckoutAttempts, err)
}

// payWithRetry pays with the visitor's card. Some visitors mistype and use a card that is declined;
// they try once more with the right one, and cancel the order if that fails too.
func (sh *shopper) payWithRetry(orderID string) (paid bool, err error) {
	card := sh.card
	if rand.IntN(100) < 10 {
		card = cardDeclined
	}
	for attempt := 0; attempt < 2; attempt++ {
		paid, err = sh.pay(orderID, card)
		if err != nil || paid {
			return paid, err
		}
		sh.cfg.tally.add("outcome", "payment_declined")
		sh.think(600 * time.Millisecond)
		card = sh.card
	}
	return false, sh.cancel(orderID)
}

// pay starts a payment and polls it. The server settles it asynchronously, so the visitor asks again
// with growing pauses (150ms, 225ms, ...) until it is paid or declined or the patience runs out.
func (sh *shopper) pay(orderID, card string) (paid bool, err error) {
	var paymentID string
	if err := sh.call("payment", func(s *jotaro.Step) error {
		var out struct {
			PaymentID string `json:"payment_id"`
		}
		r := sh.write(s, http.MethodPost, "/payments",
			map[string]string{"order_id": orderID, "card": card}).Do()
		if err := expect(r, http.StatusAccepted); err != nil {
			return err
		}
		if err := r.Decode(&out); err != nil {
			return err
		}
		paymentID = out.PaymentID
		return nil
	}); err != nil {
		return false, err
	}

	wait := 150 * time.Millisecond
	for poll := 0; poll < maxPaymentPolls; poll++ {
		sh.think(wait)
		wait = wait * 3 / 2

		var status string
		if err := sh.call("payment_poll", func(s *jotaro.Step) error {
			var out struct {
				Status string `json:"status"`
			}
			r := s.Get("/payments/" + paymentID).Do()
			if err := expect(r, http.StatusOK); err != nil {
				return err
			}
			if err := r.Decode(&out); err != nil {
				return err
			}
			status = out.Status
			return nil
		}); err != nil {
			return false, err
		}
		switch status {
		case "paid":
			sh.cfg.tally.add("outcome", "paid")
			return true, nil
		case "declined":
			return false, nil
		}
	}
	return false, errPaymentTimeout
}

// verifyOrder reads the order back and checks its status, a read-after-write check.
func (sh *shopper) verifyOrder(orderID, want string) error {
	return sh.call("order_view", func(s *jotaro.Step) error {
		var o struct {
			Status string `json:"status"`
		}
		r := s.Get("/orders/" + orderID).Do()
		if err := expect(r, http.StatusOK); err != nil {
			return err
		}
		if err := r.Decode(&o); err != nil {
			return err
		}
		if o.Status != want {
			return fmt.Errorf("order %s is %q, want %q", orderID, o.Status, want)
		}
		return nil
	})
}

// cancel cancels an order and checks that the cancellation stuck.
func (sh *shopper) cancel(orderID string) error {
	if err := sh.call("order_cancel", func(s *jotaro.Step) error {
		return expect(sh.write(s, http.MethodDelete, "/orders/"+orderID, nil).Do(), http.StatusNoContent)
	}); err != nil {
		return err
	}
	sh.cfg.tally.add("outcome", "cancelled")
	return sh.verifyOrder(orderID, "cancelled")
}

// history opens the list of the visitor's latest orders.
func (sh *shopper) history() error {
	return sh.call("orders", func(s *jotaro.Step) error {
		var orders []struct {
			ID string `json:"id"`
		}
		r := s.Get("/orders").Query("limit", "5").Do()
		if err := expect(r, http.StatusOK); err != nil {
			return err
		}
		return r.Decode(&orders)
	})
}

// tally counts what visitors did. A load test report says how the server behaved; this says
// what the traffic was made of, which is needed to read the report.
type tally struct {
	mu sync.Mutex
	m  map[string]int
}

func newTally() *tally { return &tally{m: map[string]int{}} }

func (t *tally) add(kind, name string) {
	t.mu.Lock()
	t.m[kind+"/"+name]++
	t.mu.Unlock()
}

// get returns the count for kind/name.
func (t *tally) get(kind, name string) int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.m[kind+"/"+name]
}

// lines returns "kind/name: count" lines in a stable order.
func (t *tally) lines() []string {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]string, 0, len(t.m))
	for k, n := range t.m {
		out = append(out, fmt.Sprintf("%-32s %d", k, n))
	}
	sort.Strings(out)
	return out
}

// accountsCSV is the member accounts. In a real project this would be a file passed to jotaro.LoadCSV.
const accountsCSV = `email,password,card
alice@example.com,pw-alice,tok_visa
bob@example.com,pw-bob,tok_visa
carol@example.com,pw-carol,tok_visa
dave@example.com,pw-dave,tok_visa
erin@example.com,pw-erin,tok_visa
frank@example.com,pw-frank,tok_visa
`

// loadMembers reads accountsCSV.
func loadMembers() ([]map[string]string, error) {
	return jotaro.LoadCSV(strings.NewReader(accountsCSV))
}

// newcomerAccounts makes n accounts that do not exist yet.
func newcomerAccounts(n int) []map[string]string {
	rows := make([]map[string]string, n)
	for i := range rows {
		rows[i] = map[string]string{
			"email":    fmt.Sprintf("newcomer-%03d@example.com", i),
			"password": "pw-newcomer",
		}
	}
	return rows
}
