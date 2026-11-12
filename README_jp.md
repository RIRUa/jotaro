# jotaro

[English](README.md) | 日本語

[![CI](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml/badge.svg)](https://github.com/RIRUa/jotaro/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/RIRUa/jotaro.svg)](https://pkg.go.dev/github.com/RIRUa/jotaro)
[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)

シナリオ（ログイン → 一覧 → 注文 のような一連の流れ）を Go で書いて、HTTP サーバーに負荷をかけるライブラリです。結果はコンソール・JSON・HTML で出せて、CLI でしきい値判定もできます。

- シナリオは ふつうの Go のコードで書く（`if` や変数がそのまま使える）
- 負荷のかけ方は 2 通り
  - **クローズド**: 固定数のユーザーがシナリオを繰り返す（`Users`）
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
  - ヘッダー: `Header(k, v)` / `Headers(map)` / `Bearer(token)` / `Basic(user, pass)`
  - クエリ: `Query(k, v)` / `Queries(url.Values)`（値はエスケープされる）
  - ボディ: `JSON(v)` / `Form(url.Values)` / `Body(v)`（`[]byte` / `string` / `io.Reader` はそのまま、`url.Values` はフォーム、それ以外は JSON。`Content-Type` が要るときは `Header` で付ける）
- `Do()` のあとに `Decode(&v)`（JSON を読む）か `ExpectStatus(code)`（ステータスを検証する）をつなげる
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
```

`Users`・`UserStages`・`Arrival` のうちちょうど 1 つを指定します。

| 指定 | 内容 |
|---|---|
| `Users: 20` | 20 ユーザーが `Duration` の間シナリオを繰り返す |
| `UserStages: []jotaro.UserStage{{Target: 100, For: 30*time.Second}, {Target: 100, For: time.Minute}, {Target: 0, For: 10*time.Second}}` | ユーザー数を段階ごとに変える。30 秒で 100 人まで増やし、1 分保ち、10 秒で 0 人まで減らす（最初の段階は `StartUsers` 人から始まる。既定は 0） |
| `Arrival: jotaro.Rate(50, time.Second)` | 毎秒 50 回シナリオを開始する |
| `Arrival: jotaro.Ramp(0, 100, time.Second, 10*time.Second)` | 10 秒かけて毎秒 0 回から 100 回へ増やす |
| `Arrival: jotaro.Stages(...)` | 段階ごとに `Arrival` を切り替える（上げる → 保つ → 下げる、など） |

`UserStages` で目標が下がると、止まるユーザーは実行中のイテレーションを終えてから待機します（中断はしません）。ユーザーは再開しても `User.ID` とイテレーション番号を引き継ぎます。同時に動いているイテレーション数の推移はレポートの `Series.Concurrency` で確認でき、負荷の形を検証できます。

そのほか、`MaxUsers`（オープンのときの同時実行数の上限。超えた分はドロップとして数える）、`Timeout`（1 リクエストの上限）、`Grace`（`Duration` 後に実行中のイテレーションを待つ時間）、`Raw`（生データの書き出し先）を指定できます。

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
```

- 入力に `-` を渡すと標準入力から読む（`report` / `check`）
- `-raw` を付けると、入力を JSON Lines の生データ（`Plan.Raw` の出力）として読み、集計し直す
- `-format text|json|html` で出力の形式を選ぶ

### しきい値の書き方

`指標[{scenario=名前,step=名前}] 演算子 値`。演算子は `<` `<=` `>` `>=`。

| 指標 | 意味 |
|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | リクエストのレイテンシ（`200ms`、`1.5s`） |
| 上の頭に `corrected_` `ttfb_` `dns_` `connect_` `tls_` | CO 補正後 / TTFB / DNS / TCP / TLS の時間 |
| `error_rate` | 通信エラーと `ExpectStatus` 不一致の割合（`1%` または `0.01`） |
| `http_4xx_rate` `http_5xx_rate` | HTTP 4xx / 5xx の割合 |
| `iteration_fail_rate` | 失敗したイテレーションの割合 |
| `requests` `rps` `dropped` | リクエスト数 / 毎秒のリクエスト数 / ドロップされた到着の数 |

対象が無い、またはデータが無いときは「満たさなかった」扱いになります。ステップ名の打ち間違いで通ってしまわないようにするためです。詳しくは [`docs/result_jp.md`](docs/result_jp.md) の 5 章を見てください。

## MCP サーバー（シナリオ作成の補助）

`jotaro mcp` は、AI アシスタントがシナリオを書くのを助ける [MCP](https://modelcontextprotocol.io) サーバーを起動します。stdio で通信し、チェック対象のシナリオを実行することはありません。

```sh
claude mcp add jotaro -- jotaro mcp     # Claude Code の場合。他のクライアントも同じコマンドを登録する
```

| 種類 | 名前 | 用途 |
|---|---|---|
| Resource | `jotaro://guide` | シナリオの書き方（雛形・API の概要・負荷モデル・チェックリスト） |
| Resource | `jotaro://thresholds` | しきい値の文法と指標 |
| Prompt | `write_scenario` | 指定したフローのシナリオを書き、チェックまで行う |
| Tool | `check_scenario` | シナリオを検査する。Go の構文、jotaro 固有の lint（`Users` と `Arrival` はどちらか一方、レスポンス未検証、Step 名の重複など）、コンパイルによる型チェック。`dir`（jotaro を require している module）を渡すとその jotaro のバージョンで検査し、省略すると最新の jotaro を使う一時 module を作る（ネットワークが必要） |
| Tool | `validate_threshold` | `p99{step=login}<100ms` のようなしきい値をパースする |

## パッケージ

| パッケージ | 内容 |
|---|---|
| `jotaro` | シナリオ（`Scenario` / `User` / `Step`）と負荷のかけ方（`Plan` / `Run` / `Rate` / `Ramp` / `Stages`）。使う人の入口 |
| `report` | 結果（`Report`）の集計・出力（text / JSON / HTML）・結合・しきい値判定 |
| `record` | リクエスト単位の記録と、生データ（JSON Lines）の読み書き |
| `cmd/jotaro` | CLI（`report` / `merge` / `check` / `mcp`） |

## 注意

- 計測できるのは HTTP のみです
- `Plan.Raw` を指定しないとき、p50 などは相対誤差 1% 以内の近似値です（記録は届くたびにヒストグラムへ畳むので、実行時間が長くてもメモリは増えません）。件数・最小・最大・平均は正確です。`Plan.Raw` を指定すると全記録をメモリに持ち、パーセンタイルは正確になります
- 統計的な比較（2 つの結果に有意差があるか）をする機能はありません。`check` は基準を決めて合否を見る方式です

## ライセンス

[MIT](LICENSE)
