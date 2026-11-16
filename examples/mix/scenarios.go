package main

import (
	"context"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strconv"
	"time"

	"github.com/RIRUa/jotaro"
)

// The two flows of the mix. They are independent: a visitor either browses or buys, so each is its own
// scenario and Plan.Scenarios picks one per iteration. (To branch on what an earlier step returned, keep one
// scenario and branch inside it, as examples/marketplace does.)
//
// A step returns the error of its Expect* checks, and the scenario returns the step's error. That is what makes
// iteration_fail_rate see a response that is not what was expected.

// browse looks at the product list and opens one product.
var browse = jotaro.NewScenario("browse", func(ctx context.Context, u *jotaro.User) error {
	var id int
	if err := u.Step("list", func(s *jotaro.Step) error {
		resp := s.Get("/products").Do()
		if err := resp.ExpectStatusIn(http.StatusOK, http.StatusNotModified); err != nil {
			return err
		}
		if err := resp.ExpectJSON("currency", "JPY"); err != nil {
			return err
		}
		// Response.JSON reads one value by a dot path, without declaring a struct. Numbers come back as float64.
		v, err := resp.JSON("items." + strconv.Itoa(rand.IntN(productCount)) + ".id")
		if err != nil {
			return err
		}
		n, ok := v.(float64)
		if !ok {
			return fmt.Errorf("product id is %T, want a number", v)
		}
		id = int(n)
		return nil
	}); err != nil {
		return err
	}

	u.Think(100 * time.Millisecond)

	return u.Step("detail", func(s *jotaro.Step) error {
		return s.Get("/products/" + strconv.Itoa(id)).Do().ExpectStatusIn(http.StatusOK)
	})
})

// buy logs in and orders one product. "Sold out" (409) is a normal business outcome, so the step accepts it.
var buy = jotaro.NewScenario("buy", func(ctx context.Context, u *jotaro.User) error {
	var token string
	if err := u.Step("login", func(s *jotaro.Step) error {
		resp := s.Post("/login").JSON(map[string]string{"user": "alice"}).Do()
		if err := resp.ExpectStatusIn(http.StatusOK); err != nil {
			return err
		}
		v, err := resp.JSON("token")
		if err != nil {
			return err
		}
		token, _ = v.(string)
		return nil
	}); err != nil {
		return err
	}

	u.Think(200 * time.Millisecond)

	return u.Step("order", func(s *jotaro.Step) error {
		resp := s.Post("/orders").Bearer(token).JSON(map[string]int{"item": 1 + rand.IntN(productCount)}).Do()
		if err := resp.ExpectStatusIn(http.StatusCreated, http.StatusConflict); err != nil {
			return err
		}
		if resp.Code == http.StatusCreated {
			return resp.ExpectJSON("status", "accepted")
		}
		return nil
	})
})
