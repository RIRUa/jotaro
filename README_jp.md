# jotaro

[English](README.md) | 日本語

[![CI](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml/badge.svg)](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/RIRUa/jotaro.svg)](https://pkg.go.dev/github.com/RIRUa/jotaro)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

シナリオ（ログイン → 一覧 → 注文 のような一連の流れ）を Go で書いて、HTTP サーバーに負荷をかけるライブラリです。結果はコンソール・JSON・HTML で出せて、CLI でしきい値判定もできます。

- シナリオは ふつうの Go のコードで書く（`if` や変数がそのまま使える）
- 負荷のかけ方は 2 通り
  - **クローズド**: 固定数のユーザーがシナリオを繰り返す（`Users`）、またはユーザー数を段階ごとに変える（`UserStages`）
  - **オープン**: 一定の割合でシナリオを開始する（`Rate` / `Ramp` / `Stages`）。サーバーが遅くなっても到着のペースを落とさず、遅延は予定開始時刻から測る（Coordinated Omission の補正）
- ステップ単位のレイテンシ（p50 / p90 / p95 / p99 / max）、エラーの内訳、スループット、TTFB、DNS / TCP / TLS 時間、接続の再利用率を計測する
- 1 秒ごとの時系列と、負荷生成側の健全性（CPU・ゴルーチン数）も記録する

設計の考え方は [`DESIGN_jp.md`](DESIGN_jp.md)、結果の形式としきい値の仕様は [`docs/result_jp.md`](docs/result_jp.md) にあります。

> **状態: v0.x。** むやみに API を変えず、[Go 1 の互換性の約束](https://go.dev/doc/go1compat)のように、既存のコードが動き続けることを大事にします。例外は既存のバグの修正で、これまでの動作に頼っていたコードに影響が出ることがあります。そうした変更は [`CHANGELOG.md`](CHANGELOG.md)（英語）にまとめます。

## 試してみる

自前のモックサーバーを立てるサンプルを [`examples/`](examples/README_jp.md) に用意しています。

```sh
go run ./examples/checkout       # 短いフロー: ログイン → 商品一覧 → 注文
go run ./examples/marketplace    # 5 種類の訪問者を演じる、より深いシナリオ
```

## ライブラリとして使う

```sh
go get github.com/RIRUa/jotaro
```

Go 1.25 以上が必要です。

### 1. シナリオを書く

```go
var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
	var login struct {
		Token string `json:"token"`
	}
	if err := u.Step("login", func(s *jotaro.Step) error {
		return s.Post("/login").JSON(creds).Do().Decode(&login)
	}); err != nil {
		return err // 止めるなら return err。続けるなら無視する
	}

	u.Think(50 * time.Millisecond) // 待ち時間。レイテンシには含めない

	return u.Step("order", func(s *jotaro.Step) error {
		return s.Post("/orders").Bearer(login.Token).
			JSON(map[string]int{"item": 1}).
			Do().ExpectStatus(http.StatusCreated)
	})
})
```

- `Step` は計測の単位。結果はステップごとに集計される
- `Get` / `Post` / `Put` / `Patch` / `Delete` でリクエストを組み立て、`Do()` で送る
- リクエストには次を足せる（`Do()` の前につなげる）
  - ヘッダー: `Header(k, v)` / `Headers(map)` / `Bearer(token)` / `Basic(user, pass)`。`Header("Host", v)`（または `Host(v)`）は送る Host を設定する。`BaseURL` のアドレスに接続したまま仮想ホストやテナントを選ぶために使う。TLS のサーバー名は変わらない
  - クエリ: `Query(k, v)` / `Queries(url.Values)`（値はエスケープされる）。`Path` に直接書いたクエリはそのまま送られるので、空白や制御文字があると `Do()` はエラーを返して何も送らない（パス部分は `net/http` がエスケープする）
  - ボディ: `JSON(v)` / `Form(url.Values)` / `Body(v)`（`[]byte` / `string` / `io.Reader` はそのまま、`url.Values` はフォーム、それ以外は JSON。`Content-Type` が要るときは `Header` で付ける）
- `Do()` のあとに `Decode(&v)`（JSON を読む）か、次の `Expect*` のどれかをつなげる
  - `ExpectStatus(code)` / `ExpectStatusIn(codes...)`: ステータスコード
  - `ExpectHeader(key, want)` / `ExpectBodyContains(substr)`: ヘッダーの値 / ボディに含まれる文字列
  - `ExpectJSON(path, want)`: `data.items.0.id` のようなドット区切りのパスにある値。`Response.JSON(path)` はその値を返すので、構造体を定義せずに次のリクエストへ使える
  - 検証に失敗するとエラーを返し、ステップは `expect_mismatch` として記録される。リクエストのエラーとしては数えない（[しきい値の書き方](#しきい値の書き方)の `error_rate` を参照）
- `Think` は思考時間。シナリオ時間は、`Think` を除いたものと含めたものの両方が出る
- ステップが失敗しても、シナリオの関数が `nil` を返せばそのイテレーションは成功として数える。ステップの失敗はステップ単位の集計で見る
- Cookie はイテレーションごとに新しくなる。接続プールは全ユーザーで共有する

ユーザーごとに別のデータ（アカウントなど）を配るには `Feeder` を使う。CSV からは `LoadCSV` で読める。

```go
var accounts = jotaro.NewFeeder([]map[string]string{
	{"user": "alice"}, {"user": "bob"},
}, jotaro.Circular)

creds, ok := accounts.Next()
```

### 2. 負荷のかけ方を決めて実行する

```go
rep, err := jotaro.Run(ctx, jotaro.Plan{
	Scenario: checkout,
	BaseURL:  "http://localhost:8080",
	Duration: time.Minute,
	Users:    20, // クローズド。オープンにするなら Users の代わりに Arrival
})
if err != nil && !errors.Is(err, jotaro.ErrAborted) {
	log.Fatal(err) // Plan が不正（rep は nil）、または Raw の書き込みに失敗（rep は返る）
}
```

下の `AbortOn` が実行を止めたときは、`Run` は途中までのレポートと `jotaro.ErrAborted` を包んだエラーの**両方**を返します。`err` が nil でなくても、レポートは使えます。

`Users`・`UserStages`・`Arrival` のうちちょうど 1 つを指定します。

| 指定 | 内容 |
|---|---|
| `Users: 20` | 20 ユーザーが `Duration` の間シナリオを繰り返す |
| `UserStages: []jotaro.UserStage{{Target: 100, For: 30*time.Second}, {Target: 100, For: time.Minute}, {Target: 0, For: 10*time.Second}}` | ユーザー数を段階ごとに変える。30 秒で 100 人まで増やし、1 分保ち、10 秒で 0 人まで減らす（最初の段階は `StartUsers` 人から始まる。既定は 0） |
| `Arrival: jotaro.Rate(50, time.Second)` | 毎秒 50 回シナリオを開始する |
| `Arrival: jotaro.Ramp(0, 100, time.Second, 10*time.Second)` | 10 秒かけて毎秒 0 回から 100 回へ増やす |
| `Arrival: jotaro.Stages(...)` | 段階ごとに `Arrival` を切り替える（上げる → 保つ → 下げる、など） |

複数のフローを 1 つの Plan で流すときは、`Scenario` の代わりに `Scenarios` を指定します: `Scenarios: []jotaro.Weighted{{Scenario: browse, Weight: 7}, {Scenario: checkout, Weight: 3}}`。イテレーション（クローズド）または到着（オープン）ごとに、重みに比例した確率で 1 つが選ばれます。レポートはシナリオ名ごとに結果を分けるので、名前は重ならないようにします。

`UserStages` で目標が下がると、止まるユーザーは実行中のイテレーションを終えてから待機します（中断はしません）。ユーザーは再開しても `User.ID` とイテレーション番号を引き継ぎます。同時に動いているイテレーション数の推移はレポートの `Series.Concurrency` で確認でき、負荷の形を検証できます。

段階の並びを繰り返したいとき（たとえば波を 3 回）は `jotaro.RepeatUserStages(3, stage1, stage2, ...)` を使います。並びを 3 回くり返した段階を返し、`append` で他の段階と連結できます。各回は直前の回が終わった Target から始まります。

そのほか、`MaxUsers`（オープンのときの同時実行数の上限。上限に達しているときに来た到着は、待たされず実行もされずにドロップとして数える。ドロップがあるとテキストのレポートに `warning:` の行が出る）、`Timeout`（1 リクエストの上限）、`Grace`（`Duration` 後に実行中のイテレーションを待つ時間）、`Raw`（生データの書き出し先）、`Progress`（`os.Stderr` などの `io.Writer`。実行中、1 秒ごとに `12s/60s rps=480 p95=38ms err=0.2%` のような 1 行を書き出す）、`MetricsAddr`（たとえば `":9090"`。実行中の状態を Prometheus のテキスト形式で `/metrics` に公開し、Grafana などで実行中に見られる。依存は増えず、`Run` が返ると止まる）を指定できます。

サーバーが明らかに壊れたら早めに止めたいときは `AbortOn` を指定します。例: `AbortOn: []jotaro.AbortRule{{When: "http_5xx_rate>50%", For: 5 * time.Second}}`。`When` は閾値と同じ書き方ですが、「止める状態」を表します。1 秒ごとに、ここまでの実行全体に対して判定し、`For` はその状態が途切れず続くべき時間です。実行中のイテレーションは `Grace` まで待たれ、その後 `Run` は途中までのレポートと、`errors.Is(err, jotaro.ErrAborted)` を満たすエラーを一緒に返します。

### 3. 結果を出す

```go
rep.Write(os.Stdout, report.FormatText) // FormatText / FormatJSON / FormatHTML
```

しきい値で判定することもできます。

```go
th, _ := report.ParseThreshold("p95<200ms")
checks := rep.Check(th)
checks.Print(os.Stdout)
if !checks.Passed() {
	os.Exit(1)
}
```

### 4. フォルダを構成する

テスト対象のサービスと同じリポジトリに負荷試験を置くときの、おすすめの構成です。

```
myservice/
├─ go.mod
└─ loadtest/
   ├─ main.go          # フラグ、Plan、Run、結果の保存、Check
   ├─ checkout.go      # シナリオ（package main）
   ├─ thresholds.go    # var thresholds = []string{"p95<200ms", ...}
   ├─ testdata/        # Feeder 用の CSV。秘密情報は置かない
   └─ results/         # git 管理外: result.json, report.html, raw.jsonl
```

シナリオが増えたり、複数の環境に流すようになったら、独立した module に分けます。

## CLI

保存した結果を、あとから加工・判定するための CLI です。

```sh
go install github.com/RIRUa/jotaro/cmd/jotaro@latest
```

ビルド済みのバイナリ（Linux / macOS / Windows）は、[GitHub Release](https://github.com/RIRUa/jotaro/releases) ごとに添付します。`jotaro version` でバージョンを表示します（バグ報告に添えてください）。

オプションは、ファイル名より**前**に書きます。

```sh
# JSON の結果から HTML レポートを作る（既定は html）
jotaro report -o result.html result.json

# 複数の結果（別マシンで走らせた分など）を 1 つにまとめる（既定は json）
jotaro merge -o merged.json a.json b.json

# しきい値を満たすか確かめる。満たさないものが 1 つでもあれば終了コード 1
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json

# 前回の結果と比べる。どれかの p95 が 10% より悪化したら終了コード 1
jotaro compare -max-p95-regression 10 base.json new.json

# 生データ（Plan.Raw の出力）を他のツール向けに変換する
jotaro export -format vegeta -o results.json raw.jsonl   # そのあと: vegeta report -type=json results.json
jotaro export -format csv -o series.csv raw.jsonl        # 毎秒の件数とパーセンタイル
```

- 入力に `-` を渡すと標準入力から読む（`report` / `merge` / `check` / `compare` / `export`）
- `-raw` を付けると、入力を JSON Lines の生データ（`Plan.Raw` の出力）として読み、集計し直す（`report` / `merge` / `check` / `compare`）。`export` は常に生データを読む。生データでない入力を `-raw` や `export` に渡すとエラーになる
- `report` / `merge` の出力の形式は `-format text|json|html`、`export` は `-format vegeta|csv` で選ぶ
- `compare` は rps・p50 / p95 / p99・エラー率の差を、全体とステップごとに表示する。`-max-p95-regression <パーセント>` を付けると、p95 がそれより悪化したときに終了コード 1 になる

### しきい値の書き方

`指標[{scenario=名前,step=名前}] 演算子 値`。演算子は `<` `<=` `>` `>=`。

| 指標 | 意味 |
|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | リクエストのレイテンシ（`200ms`、`1.5s`） |
| 上の頭に `corrected_` `ttfb_` `dns_` `connect_` `tls_` | CO 補正後 / TTFB / DNS / TCP / TLS の時間 |
| `error_rate` | 通信レベルのエラー（タイムアウト、接続拒否、DNS 失敗など）で終わったリクエストの割合（`1%` または `0.01`）。`Expect*` の失敗は含まない |
| `http_4xx_rate` `http_5xx_rate` | HTTP 4xx / 5xx の割合 |
| `iteration_fail_rate` | 失敗したイテレーションの割合 |
| `iteration_min` 〜 `iteration_max`、`iteration_active_min` 〜 `iteration_active_max` | シナリオ 1 周ぶんのレイテンシ（Think を含む / 含まない。`{scenario=...}` で絞り込み） |
| `requests` `rps` `dropped` | リクエスト数 / 毎秒のリクエスト数 / ドロップされた到着の数 |

カンマ、釣り合わない波括弧、`<` `>`、前後の空白を含むステップ名・シナリオ名は、Go の二重引用符付き文字列で書きます: `p95{step="list, then order"}<1s`（`\"` と `\\` はエスケープ）。

対象が無い、またはデータが無いときは「満たさなかった」扱いになります。ステップ名の打ち間違いで通ってしまわないようにするためです。詳しくは [`docs/result_jp.md`](docs/result_jp.md) の 5 章を見てください。

しきい値を選ぶときに知っておきたいことが 2 つあります。

- `error_rate` は、通信レベルのエラー（タイムアウト、接続拒否、DNS 失敗など）で終わったリクエストの割合です。`Expect*` の失敗はリクエストのエラーとしては数えません。ステップが `expect_mismatch` で失敗し（ステップのエラーの一覧に出ます）、シナリオがそのエラーを返すと `iteration_fail_rate` に反映されます。誤ったレスポンスで不合格にしたいときは、ステップのエラーをシナリオから返し（上の例のとおり）、`iteration_fail_rate<1%` を指定します。HTTP のエラーステータスは `http_5xx_rate` / `http_4xx_rate` で見ます。
- オープンモデルで `MaxUsers` が満杯のとき、到着は待たされずにドロップされ、ドロップされた到着はレイテンシの標本に入りません。`corrected_*` をしきい値に使うときは `dropped<1` を併記してください。そうしないと、ドロップが出た走行でも、実行できた到着のレイテンシだけで合格してしまいます。

## MCP サーバー（シナリオ作成と結果確認の補助）

`jotaro mcp` は、AI アシスタントがシナリオを書き、保存した結果を読むのを助ける [MCP](https://modelcontextprotocol.io) サーバーを起動します。stdio で通信し、シナリオを実行することはありません（コードの検査と結果ファイルの読み取りだけを行います）。

```sh
claude mcp add jotaro -- jotaro mcp     # Claude Code の場合。他のクライアントも同じコマンドを登録する
```

| 種類 | 名前 | 用途 |
|---|---|---|
| Resource | `jotaro://guide` | シナリオの書き方（雛形・API の概要・負荷モデル・チェックリスト） |
| Resource | `jotaro://thresholds` | しきい値の文法と指標 |
| Resource | `jotaro://result` | 結果の保存方法と読み方（構造、各数値とエラー種別の意味、最初に確認すること） |
| Prompt | `write_scenario` | 指定したフローのシナリオを書き、チェックまで行う |
| Tool | `check_scenario` | シナリオを検査する。Go の構文、jotaro 固有の lint（`Users` / `UserStages` / `Arrival` のうちちょうど 1 つ、レスポンス未検証、Step 名の重複・データから組み立てた Step 名など）、コンパイルによる型チェック。lint は jotaro の型とメソッドで判定するので、別名の import や変数を経由していても検出し、自前の `Plan` 型や `Do()` メソッドは無視する（コンパイルが通った場合に型情報つきで実行し、それまでは名前ベースの代替が警告だけを返す）。`dir`（jotaro を require している module）を渡すとその jotaro のバージョンで検査し、省略すると、実行中の `jotaro` バイナリと同じバージョンの jotaro（開発ビルドでは最新版）を使う一時 module を作る（ネットワークが必要） |
| Tool | `validate_threshold` | `p99{step=login}<100ms` のようなしきい値をパースする |
| Tool | `summarize_result` | 保存した結果ファイルを要約する。スループット、エラー率、ステップごとのレイテンシ、dropped、最も遅かった 1 秒、負荷生成側の CPU 飽和などの警告を返す。ヒストグラムと毎秒の時系列を含む結果ファイルそのものよりずっと小さい |
| Tool | `check_result` | 保存した結果ファイルをしきい値で判定する（`jotaro check` と同じ）。しきい値ごとの合否と実測値を返す。`raw: true` で生データ（JSON Lines）を読む |

## パッケージ

| パッケージ | 内容 |
|---|---|
| `jotaro` | シナリオ（`Scenario` / `User` / `Step`）と負荷のかけ方（`Plan` / `Run` / `Rate` / `Ramp` / `Stages` / `UserStages`）。使う人の入口 |
| `report` | 結果（`Report`）の集計・出力（text / JSON / HTML）・結合・比較・他ツール向けの変換・しきい値判定 |
| `record` | リクエスト単位の記録と、生データ（JSON Lines）の読み書き |
| `cmd/jotaro` | CLI（`report` / `merge` / `check` / `compare` / `export` / `mcp` / `version`） |

## 注意

- 計測できるのは HTTP のみです
- `Plan.Raw` を指定しないとき、p50 などは相対誤差 1% 以内の近似値です（記録は届くたびにヒストグラムへ畳むので、実行時間が長くてもメモリは増えません）。件数・最小・最大・平均は正確です。`Plan.Raw` を指定しても記録は届くたびに書き出すだけでメモリは一定です。正確なパーセンタイルが必要なときは、そのファイルを `jotaro report -raw` で集計し直してください
- 統計的な比較（2 つの結果に有意差があるか）をする機能はありません。`check` は基準を決めて合否を見る方式で、`compare` は 2 つの結果の差を表示します（合否を決めるのは p95 の悪化だけです）

## ライセンス

[MIT](LICENSE)
