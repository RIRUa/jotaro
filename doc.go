// Package jotaro は、シナリオ（ログイン → 一覧 → 注文 のような一連の流れ）を書いて HTTP サーバーに負荷をかけるライブラリ。
//
// 使う人の入口はこのパッケージで、シナリオ（Scenario / User / Step）と負荷のかけ方（Plan / Run / Rate / Ramp / Stages）を扱う。
// 結果は [report.Report] で返る。コンソール・JSON・HTML に書き出したり、しきい値で判定したりするのは
// report パッケージ、生データ（リクエスト単位の記録）は record パッケージが扱う。
//
//	sc := jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
//		return u.Step("login", func(s *jotaro.Step) error {
//			return s.Post("/login").JSON(creds).Do().ExpectStatus(200)
//		})
//	})
//	rep, err := jotaro.Run(ctx, jotaro.Plan{Scenario: sc, BaseURL: url, Users: 20, Duration: time.Minute})
//	rep.Write(os.Stdout, report.FormatText)
package jotaro
