package main

import (
	crand "crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/rand/v2"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// This file is a stateful mock of a small online marketplace. It is deliberately not a stub:
// the behaviours below are what make the scenario in scenario.go worth writing.
//
//   - Sessions are cookies plus a CSRF token, and they expire.
//   - Stock is shared, so popular items sell out under load (409).
//   - POST /checkout is rate limited (429 + Retry-After) and sometimes loses its response
//     after the order was already created (503). Retrying with the same Idempotency-Key is safe.
//   - Payment is asynchronous: it has to be polled until it settles.
//   - Latency grows with the number of requests in flight.

// Names the scenario sends and the mock reacts to.
const (
	cookieName    = "sid"  // login session, expires
	cartCookie    = "cart" // cart ID, survives a re-login like an anonymous cart in a real shop
	cardOK        = "tok_visa"
	cardDeclined  = "tok_declined"
	couponWelcome = "WELCOME10" // only for accounts that signed up and have not used it yet
	couponSummer  = "SUMMER5"
)

type product struct {
	ID       int    `json:"id"`
	Name     string `json:"name"`
	Category string `json:"category"`
	Price    int    `json:"price"`
	Stock    int    `json:"stock"`

	version int // bumped whenever the stock changes; it is the ETag
}

type user struct {
	password string
	welcome  bool // may still use the welcome coupon
}

type cart struct {
	items    map[int]int // product ID → quantity
	discount int         // percent
}

type session struct {
	email   string
	csrf    string
	expires time.Time
	cart    *cart // set on every request from the cart cookie
}

type order struct {
	ID     string      `json:"id"`
	Status string      `json:"status"`
	Total  int         `json:"total"`
	Items  map[int]int `json:"items"`

	owner   string
	payment *payment
}

type payment struct {
	ID      string
	order   *order
	ready   time.Time
	approve bool
	settled bool
}

// serverStats is what the mock saw, which the client-side report cannot tell.
type serverStats struct {
	OrdersCreated atomic.Int64
	Replays       atomic.Int64 // checkouts answered from the Idempotency-Key table
	RateLimited   atomic.Int64
	LostResponses atomic.Int64
	SoldOut       atomic.Int64
	Expired       atomic.Int64 // requests rejected because the session had expired
	Cancelled     atomic.Int64
}

type market struct {
	sessionTTL time.Duration

	mu       sync.Mutex
	products []*product
	initial  int // total stock at the start, for checkInventory
	users    map[string]*user
	carts    map[string]*cart
	sessions map[string]*session
	orders   map[string]*order
	payments map[string]*payment
	byKey    map[string]*order // Idempotency-Key → order

	// token bucket for POST /checkout
	tokens float64
	refill time.Time

	inflight atomic.Int64
	stats    serverStats
}

var (
	adjectives = []string{"Classic", "Compact", "Deluxe", "Eco", "Pro"}
	categories = []struct{ name, noun string }{
		{"books", "novel"}, {"games", "puzzle"}, {"kitchen", "pan"}, {"outdoor", "camp stove"},
	}
)

// newMarket creates a shop with 40 products and the given accounts. Every 7th product is a
// limited edition with 3 pieces, which is what makes sold-out conflicts happen.
func newMarket(sessionTTL time.Duration, accounts []map[string]string) *market {
	m := &market{
		sessionTTL: sessionTTL,
		users:      map[string]*user{},
		carts:      map[string]*cart{},
		sessions:   map[string]*session{},
		orders:     map[string]*order{},
		payments:   map[string]*payment{},
		byKey:      map[string]*order{},
		tokens:     checkoutBurst,
		refill:     time.Now(),
	}
	for i := 0; i < 40; i++ {
		c := categories[i%len(categories)]
		p := &product{
			ID:       i + 1,
			Name:     fmt.Sprintf("%s %s #%d", adjectives[i%len(adjectives)], c.noun, i+1),
			Category: c.name,
			Price:    500 + (i*137)%3000,
			Stock:    25,
		}
		if i%7 == 6 {
			p.Stock = 3
		}
		m.initial += p.Stock
		m.products = append(m.products, p)
	}
	for _, a := range accounts {
		m.users[a["email"]] = &user{password: a["password"]}
	}
	return m
}

// Checkout is limited to checkoutRate requests per second with a burst of checkoutBurst.
const (
	checkoutRate  = 12.0
	checkoutBurst = 8.0
)

// handler wires the routes. Auth and the latency model wrap everything.
func (m *market) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /products", m.listProducts)
	mux.HandleFunc("GET /products/{id}", m.getProduct)
	mux.HandleFunc("POST /signup", m.signup)
	mux.HandleFunc("POST /session", m.login)

	mux.HandleFunc("GET /cart", m.auth(m.getCart))
	mux.HandleFunc("POST /cart/items", m.auth(m.addToCart))
	mux.HandleFunc("POST /cart/coupon", m.auth(m.applyCoupon))
	mux.HandleFunc("POST /checkout", m.auth(m.checkout))
	mux.HandleFunc("POST /payments", m.auth(m.pay))
	mux.HandleFunc("GET /payments/{id}", m.auth(m.getPayment))
	mux.HandleFunc("GET /orders", m.auth(m.listOrders))
	mux.HandleFunc("GET /orders/{id}", m.auth(m.getOrder))
	mux.HandleFunc("DELETE /orders/{id}", m.auth(m.cancelOrder))
	return m.slow(mux)
}

// slow models a server that queues work: every request costs a few milliseconds
// plus a little more for each request already in flight.
func (m *market) slow(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := m.inflight.Add(1)
		defer m.inflight.Add(-1)
		time.Sleep(3*time.Millisecond + rand.N(9*time.Millisecond) + time.Duration(n)*300*time.Microsecond)
		next.ServeHTTP(w, r)
	})
}

// auth resolves the session from the cookie. Writes also need the CSRF token.
// An expired session is removed and answered with 401, which the scenario handles by logging in again.
func (m *market) auth(h func(http.ResponseWriter, *http.Request, *session)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(cookieName)
		if err != nil {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "no_session"})
			return
		}
		m.mu.Lock()
		s, ok := m.sessions[c.Value]
		if ok && time.Now().After(s.expires) {
			delete(m.sessions, c.Value)
			ok = false
			m.stats.Expired.Add(1)
		}
		m.mu.Unlock()
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "session_expired"})
			return
		}
		if r.Method != http.MethodGet && r.Header.Get("X-CSRF-Token") != s.csrf {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "bad_csrf"})
			return
		}
		m.mu.Lock()
		s.cart = m.cartFor(w, r)
		m.mu.Unlock()
		h(w, r, s)
	}
}

// cartFor returns the cart named by the cart cookie, and starts a new one (with its cookie) if there is none.
// The caller holds m.mu.
func (m *market) cartFor(w http.ResponseWriter, r *http.Request) *cart {
	if c, err := r.Cookie(cartCookie); err == nil {
		if cart, ok := m.carts[c.Value]; ok {
			return cart
		}
	}
	id := token(8)
	cart := &cart{items: map[int]int{}}
	m.carts[id] = cart
	http.SetCookie(w, &http.Cookie{Name: cartCookie, Value: id, Path: "/"})
	return cart
}

// newSession stores a session and sets its cookie. The caller holds m.mu.
func (m *market) newSession(w http.ResponseWriter, email string) *session {
	s := &session{
		email:   email,
		csrf:    token(8),
		expires: time.Now().Add(m.sessionTTL/2 + rand.N(m.sessionTTL)),
	}
	sid := token(16)
	m.sessions[sid] = s
	http.SetCookie(w, &http.Cookie{Name: cookieName, Value: sid, Path: "/", HttpOnly: true})
	return s
}

type credentials struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

func (m *market) signup(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if json.NewDecoder(r.Body).Decode(&c) != nil || c.Email == "" || c.Password == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.users[c.Email]; taken {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "email_taken"})
		return
	}
	m.users[c.Email] = &user{password: c.Password, welcome: true}
	s := m.newSession(w, c.Email)
	writeJSON(w, http.StatusCreated, map[string]string{"csrf": s.csrf})
}

func (m *market) login(w http.ResponseWriter, r *http.Request) {
	var c credentials
	if json.NewDecoder(r.Body).Decode(&c) != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.users[c.Email]; !ok || u.password != c.Password {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "bad_credentials"})
		return
	}
	s := m.newSession(w, c.Email)
	writeJSON(w, http.StatusOK, map[string]string{"csrf": s.csrf})
}

// listProducts supports q (substring of the name), category, limit and cursor (the last ID of the previous page).
func (m *market) listProducts(w http.ResponseWriter, r *http.Request) {
	q := strings.ToLower(r.URL.Query().Get("q"))
	category := r.URL.Query().Get("category")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 20 {
		limit = 6
	}
	after, _ := strconv.Atoi(r.URL.Query().Get("cursor"))

	m.mu.Lock()
	var items []product
	next := ""
	for _, p := range m.products {
		if p.ID <= after || !strings.Contains(strings.ToLower(p.Name), q) ||
			(category != "" && p.Category != category) {
			continue
		}
		if len(items) == limit {
			next = strconv.Itoa(items[len(items)-1].ID)
			break
		}
		items = append(items, *p)
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{"items": items, "next_cursor": next})
}

// getProduct answers 304 when If-None-Match still matches, like a CDN-friendly detail page.
func (m *market) getProduct(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(r.PathValue("id"))
	m.mu.Lock()
	var p product
	found := id >= 1 && id <= len(m.products)
	if found {
		p = *m.products[id-1]
	}
	m.mu.Unlock()
	if !found {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_product"})
		return
	}
	etag := fmt.Sprintf(`"%d-%d"`, p.ID, p.version)
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}
	writeJSON(w, http.StatusOK, p)
}

func (m *market) addToCart(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		ProductID int `json:"product_id"`
		Qty       int `json:"qty"`
	}
	if json.NewDecoder(r.Body).Decode(&in) != nil || in.Qty < 1 || in.Qty > 3 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "bad_request"})
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if in.ProductID < 1 || in.ProductID > len(m.products) {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_product"})
		return
	}
	// Adding does not reserve stock; it only refuses what is already gone.
	if m.products[in.ProductID-1].Stock < s.cart.items[in.ProductID]+in.Qty {
		m.stats.SoldOut.Add(1)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "out_of_stock"})
		return
	}
	s.cart.items[in.ProductID] += in.Qty
	writeJSON(w, http.StatusCreated, map[string]int{"lines": len(s.cart.items)})
}

func (m *market) applyCoupon(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		Code string `json:"code"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	m.mu.Lock()
	defer m.mu.Unlock()
	switch {
	case in.Code == couponWelcome && m.users[s.email].welcome:
		s.cart.discount = 10
	case in.Code == couponWelcome:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "not_eligible"})
		return
	case in.Code == couponSummer:
		s.cart.discount = 5
	default:
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "invalid_coupon"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]int{"discount": s.cart.discount})
}

// cartView prices the cart. The caller holds m.mu.
func (m *market) cartView(c *cart) (lines []map[string]int, subtotal, total int) {
	for _, id := range sortedKeys(c.items) {
		p := m.products[id-1]
		lines = append(lines, map[string]int{"product_id": id, "qty": c.items[id], "price": p.Price})
		subtotal += p.Price * c.items[id]
	}
	return lines, subtotal, subtotal * (100 - c.discount) / 100
}

func (m *market) getCart(w http.ResponseWriter, r *http.Request, s *session) {
	m.mu.Lock()
	lines, subtotal, total := m.cartView(s.cart)
	discount := s.cart.discount
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]any{
		"items": lines, "subtotal": subtotal, "discount": discount, "total": total,
	})
}

// checkout is the interesting endpoint: rate limit, idempotency, stock check, and a lost response.
func (m *market) checkout(w http.ResponseWriter, r *http.Request, s *session) {
	key := r.Header.Get("Idempotency-Key")
	if key == "" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "idempotency_key_required"})
		return
	}
	time.Sleep(20*time.Millisecond + rand.N(30*time.Millisecond))

	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	m.tokens = min(checkoutBurst, m.tokens+now.Sub(m.refill).Seconds()*checkoutRate)
	m.refill = now
	if m.tokens < 1 {
		m.stats.RateLimited.Add(1)
		w.Header().Set("Retry-After", "1")
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "slow_down"})
		return
	}
	m.tokens--

	if o, ok := m.byKey[key]; ok {
		m.stats.Replays.Add(1)
		writeJSON(w, http.StatusOK, o)
		return
	}
	if len(s.cart.items) == 0 {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "empty_cart"})
		return
	}
	for id, qty := range s.cart.items {
		if m.products[id-1].Stock < qty {
			m.stats.SoldOut.Add(1)
			writeJSON(w, http.StatusConflict, map[string]any{"error": "sold_out", "product_id": id})
			return
		}
	}

	_, _, total := m.cartView(s.cart)
	o := &order{ID: "o-" + token(6), Status: "pending_payment", Total: total, Items: s.cart.items, owner: s.email}
	if s.cart.discount > 0 {
		m.users[s.email].welcome = false
	}
	for id, qty := range s.cart.items {
		p := m.products[id-1]
		p.Stock -= qty
		p.version++
	}
	s.cart.items, s.cart.discount = map[int]int{}, 0
	m.orders[o.ID] = o
	m.byKey[key] = o
	m.stats.OrdersCreated.Add(1)

	// The order exists, but the answer never reaches the client.
	if rand.IntN(100) < 4 {
		m.stats.LostResponses.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	writeJSON(w, http.StatusCreated, o)
}

// pay starts an asynchronous payment. It settles 150-700ms later; the card decides how.
func (m *market) pay(w http.ResponseWriter, r *http.Request, s *session) {
	var in struct {
		OrderID string `json:"order_id"`
		Card    string `json:"card"`
	}
	_ = json.NewDecoder(r.Body).Decode(&in)
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[in.OrderID]
	if !ok || o.owner != s.email {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_order"})
		return
	}
	if o.Status != "pending_payment" || o.payment != nil {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "not_payable"})
		return
	}
	p := &payment{
		ID:      "p-" + token(6),
		order:   o,
		ready:   time.Now().Add(150*time.Millisecond + rand.N(550*time.Millisecond)),
		approve: in.Card != cardDeclined,
	}
	o.payment = p
	m.payments[p.ID] = p
	writeJSON(w, http.StatusAccepted, map[string]string{"payment_id": p.ID})
}

// settle moves a payment from pending to its final state once its time has come. The caller holds m.mu.
func (m *market) settle(p *payment) string {
	if p.settled {
		return map[bool]string{true: "paid", false: "declined"}[p.approve]
	}
	if time.Now().Before(p.ready) {
		return "pending"
	}
	p.settled = true
	if p.order.Status == "pending_payment" {
		if p.approve {
			p.order.Status = "paid"
		} else {
			p.order.payment = nil // a new attempt is allowed
		}
	}
	return m.settle(p)
}

func (m *market) getPayment(w http.ResponseWriter, r *http.Request, s *session) {
	m.mu.Lock()
	p, ok := m.payments[r.PathValue("id")]
	if !ok || p.order.owner != s.email {
		m.mu.Unlock()
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_payment"})
		return
	}
	status := m.settle(p)
	m.mu.Unlock()
	writeJSON(w, http.StatusOK, map[string]string{"status": status})
}

func (m *market) getOrder(w http.ResponseWriter, r *http.Request, s *session) {
	m.mu.Lock()
	o, ok := m.orders[r.PathValue("id")]
	if ok && o.owner == s.email {
		if o.payment != nil {
			m.settle(o.payment)
		}
		writeJSON(w, http.StatusOK, o)
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()
	writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_order"})
}

func (m *market) listOrders(w http.ResponseWriter, r *http.Request, s *session) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	m.mu.Lock()
	mine := []*order{}
	for _, o := range m.orders {
		if o.owner == s.email {
			mine = append(mine, o)
		}
	}
	m.mu.Unlock()
	slices.SortFunc(mine, func(a, b *order) int { return strings.Compare(b.ID, a.ID) })
	if limit > 0 && len(mine) > limit {
		mine = mine[:limit]
	}
	writeJSON(w, http.StatusOK, mine)
}

// cancelOrder returns the stock. Cancelling twice is a conflict.
func (m *market) cancelOrder(w http.ResponseWriter, r *http.Request, s *session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	o, ok := m.orders[r.PathValue("id")]
	if !ok || o.owner != s.email {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no_such_order"})
		return
	}
	if o.Status == "cancelled" {
		writeJSON(w, http.StatusConflict, map[string]string{"error": "already_cancelled"})
		return
	}
	for id, qty := range o.Items {
		p := m.products[id-1]
		p.Stock += qty
		p.version++
	}
	o.Status = "cancelled"
	m.stats.Cancelled.Add(1)
	w.WriteHeader(http.StatusNoContent)
}

// checkInventory verifies that no piece of stock was created or lost:
// what started on the shelves is either still there or in an order that was not cancelled.
func (m *market) checkInventory() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	have := 0
	for _, p := range m.products {
		if p.Stock < 0 {
			return fmt.Errorf("product %d has negative stock %d", p.ID, p.Stock)
		}
		have += p.Stock
	}
	for _, o := range m.orders {
		if o.Status == "cancelled" {
			continue
		}
		for _, qty := range o.Items {
			have += qty
		}
	}
	if have != m.initial {
		return fmt.Errorf("inventory drifted: started with %d, now accounted for %d", m.initial, have)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// token returns n random bytes as hex.
func token(n int) string {
	b := make([]byte, n)
	_, _ = crand.Read(b)
	return hex.EncodeToString(b)
}

func sortedKeys(m map[int]int) []int {
	keys := make([]int, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
