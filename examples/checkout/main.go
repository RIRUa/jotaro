// checkout は jotaro の使い方を示すサンプル。
// 自前のモックサーバーを立てて、ログイン → 商品一覧 → 注文 の流れに負荷をかける。
//
//	go run ./examples/checkout            # 20 ユーザー固定（クローズド）
//	go run ./examples/checkout -rate 50   # 毎秒 50 シナリオ（オープン）
//	go run ./examples/checkout -rate 100 -ramp 8s -duration 12s  # 0 から 100 へ 8 秒かけて増やす
//	go run ./examples/checkout -rate 100 -duration 12s -stages    # 上げる → 保つ → 下げる
//	go run ./examples/checkout -raw raw.jsonl                      # 生データ（JSON Lines）も書き出す
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

type item struct {
	ID    int `json:"id"`
	Price int `json:"price"`
}

// accounts はユーザーごとに別のアカウントを配る。
// CSV から読むなら jotaro.LoadCSV(f) の結果をそのまま渡せる。
var accounts = jotaro.NewFeeder([]map[string]string{
	{"user": "alice"}, {"user": "bob"}, {"user": "carol"},
}, jotaro.Circular)

var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
	var login struct {
		Token string `json:"token"`
	}
	if err := u.Step("login", func(s *jotaro.Step) error {
		creds, ok := accounts.Next()
		if !ok {
			return fmt.Errorf("アカウントが足りない")
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

func main() {
	rate := flag.Int("rate", 0, "毎秒のシナリオ開始数。0 なら Users モデル")
	ramp := flag.Duration("ramp", 0, "-rate に達するまでの時間。0 なら最初から一定")
	stages := flag.Bool("stages", false, "-rate・-ramp・-duration を使い、上げる → 保つ → 下げるの 3 段階にする")
	duration := flag.Duration("duration", 3*time.Second, "実行時間")
	format := flag.String("format", "text", "結果の形式 (text, json, html)")
	out := flag.String("o", "", "結果の書き出し先。空なら標準出力")
	rawOut := flag.String("raw", "", "生データ（JSON Lines）の書き出し先。空なら出さない")
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
		// 前後の 1/4 ずつで上げ下げし、中間の 1/2 は一定にする。
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

// mockShop は少し遅くて、ときどき注文に失敗する API。
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
