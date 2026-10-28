# jotaro 設計メモ

シナリオ表現力に重点を置いた、Go 製の負荷試験ツール（ライブラリ＋薄い CLI）。
壁打ちで決めた内容をまとめたもの。未決の項目は末尾に記載。

- モジュール: `github.com/RIRUa/jotaro`
- Go: 1.22 以上（`go.mod`）。開発では 1.27.3 を使う（`.mise.toml` で固定）。CI は 1.22 と最新の安定版で試す

## 1. 動機

[vegeta](https://github.com/tsenart/vegeta) は固定レートで単発リクエストを撃つことに特化しており、状態を持つ流れ（ログイン → トークン取得 → 注文 → 確認）を表現できない。

README と `pkg.go.dev`（v12.13.0）で確認した事実:

- ターゲットは独立しており、連鎖・値の抽出・スクリプトの記述は README に無い。
- `type Targeter func(*Target) error` は直前のレスポンスを受け取れない。`Target` にユーザーやコンテキストを表す欄も無い。
- `Pacer.Pace(elapsed, hits)` の単位は 1 リクエスト（ヒット）で、「シナリオ/秒」でペースを決められない。
- `Result` は "a single Target hit" 単位で、シナリオ単位の成否・所要時間を持たない。

→ vegeta の上には載せず、**自前の実行エンジン**を作る。

## 2. 方針

| 項目 | 決定 |
|---|---|
| 提供形態 | ライブラリ中心＋薄い CLI |
| シナリオの書き方 | **案A: 素の Go 関数**で始める。案B（ビルダー）は後から A の糖衣として追加 |
| 実行モデル | 1 ユーザー（シナリオの 1 回の実行）= 1 goroutine。`u.Step(name, fn)` でステップを区切る |
| プロトコル | HTTP から。後で差し込めるようインターフェースは切る |
| YAML | 後回し。実現するなら YAML → B（データ構造）→ A（実行）の片方向で、実行時に読み込む方式 |
| リクエスト記述 | 内部でデータ構造として持つ（メソッド・パス・ヘッダー・ボディ・抽出・期待値）。YAML の道を残すため |

## 3. シナリオの書き心地（案A）

```go
var checkout = jotaro.NewScenario("checkout", func(ctx context.Context, u *jotaro.User) error {
    var login struct{ Token string `json:"token"` }
    if err := u.Step("login", func(s *jotaro.Step) error {
        return s.Post("/login").JSON(creds(u.ID)).Do().Decode(&login)
    }); err != nil {
        return err
    }

    var items []Item
    u.Step("list", func(s *jotaro.Step) error {
        return s.Get("/items").Bearer(login.Token).Do().Decode(&items)
    })

    u.Think(1 * time.Second)
    return u.Step("order", func(s *jotaro.Step) error {
        return s.Post("/orders").Bearer(login.Token).JSON(order(items[0])).Do().ExpectStatus(201)
    })
})
```

実装では `Scenario` を型名として使うため、コンストラクタは `NewScenario` にした。

### エラーの扱い

- `Do()` が `error` を返すのは通信エラーのときだけ。HTTP 4xx/5xx はステータスとして記録し、Go のエラーにはしない（404 を期待するテストを書けるように）。
- 期待値の検証は `ExpectStatus(201)` のように明示し、外れたら `error` を返す。
- `fn` が `error` を返すとそのステップは失敗。シナリオを止めるかどうかは、呼び出し側が `return err` するかどうかで決める。

### `User` が持つもの

ユーザー ID、何回目の実行か、ユーザーごとの Cookie jar 付き `http.Client`、`Think()`。

## 4. 負荷モデル

```go
jotaro.Run(ctx, jotaro.Plan{
    Scenario: checkout,
    Users:    50,                          // クローズド: 固定同時ユーザー数
    // Arrival: jotaro.Rate(10, time.Second) // オープン: 毎秒10シナリオ（Users とは排他）
    Duration: time.Minute,
})
```

- `Users`（クローズド）: N 個の goroutine が逐次実行する。**実装済み**。
- `Rate`（オープン）: 到着レート固定。vegeta の強みをシナリオ単位で再現する。**実装済み**。

`Rate` モデルの動き:

- 到着時刻は開始時刻からの累積（`i × 間隔`）で決める。ディスパッチャが遅れても間隔はずれず、遅れた分はまとめて起動する。
- 同時実行数が `MaxUsers`（既定 1000）に達しているときの到着は、実行せず**ドロップ**として数える（`Report.Dropped`）。
- イテレーションの最初のリクエストの `Scheduled` に予定到着時刻を入れる。`Record.Corrected()`（予定時刻から応答完了まで）が Coordinated Omission の補正後の値で、`RequestStats.Corrected` に集計する。2 つ目以降のリクエストは `Scheduled = Start`（前のリクエストの遅れは実際の遅延なので補正しない）。
- 到着ごとに別のユーザーとして扱う（`User.ID` は到着の通し番号、`Iteration` は 0）。終了の仕方は `Users` と同じ（`Duration` / `Grace`）。

`Users` モデルの終了の仕方:

- `Duration` を過ぎたら、新しいイテレーションを始めない。
- 実行中のイテレーションは `Grace`（既定 30 秒）まで完了を待つ。超えたものは中断して `canceled` として記録する。リクエスト自体のタイムアウト（`Timeout`、既定 30 秒）は `timeout` で、区別される。
- 親の `ctx` が終わったら、実行中のものも含めてすぐ止まる。
- そのため `Report.Elapsed` は `Duration` より少し長くなり、RPS はわずかに低めに出る。

### ユーザーごとのデータ（Feeder）

`Feeder[T]` が、ユーザーごとに別のデータ（アカウント、入力値）を配る。全ユーザーから並行に呼べる。

```go
rows, _ := jotaro.LoadCSV(f) // 1 行目がヘッダー。列名をキーとする map の列
accounts := jotaro.NewFeeder(rows, jotaro.Once)

// シナリオの中で
acc, ok := accounts.Next()
```

| モード | 動き | 向いているデータ |
|---|---|---|
| `Circular` | 順に配り、末尾で先頭へ戻る | 同じアカウントを繰り返し使ってよい |
| `Random` | 毎回ランダム | 商品 ID など |
| `Once` | 使い切ったら `false` | 1 アカウント 1 回きり（新規登録など） |

Feeder はシナリオの外で作り、クロージャで共有する（`User` には持たせない。シナリオごとに必要なデータが違うため）。

## 5. 計測

### 構造

**リクエスト単位の生データを記録し、そこから集計する。** 3 階層で集計する: リクエスト / ステップ（ステップ名が集計キー）/ シナリオ。

記録の形は最初から全部入りにして、後から値を足しても構造を変えずに済むようにする。

- `httptrace` の各時間（DNS / TCP / TLS / TTFB）
- 送受信バイト数
- **予定開始時刻と実際の開始時刻を分けて持つ**（Coordinated Omission の補正のため）

メモリが問題になる場合は、ヒストグラム（HdrHistogram 系）に畳み込む層を挟む。

### フェーズ

| フェーズ | 内容 |
|---|---|
| 1 (MVP, `Users`) | パーセンタイル（p50/p90/p95/p99/max）、エラー内訳（タイムアウト／接続拒否／DNS／TLS／HTTP 4xx・5xx／`Expect` 不一致、ステップ別）、スループット（実 RPS・イテレーション/秒）、TTFB、送受信バイト数、思考時間を除いたシナリオ時間 |
| 2 (`Rate`) | ドロップされたイテレーション数、Coordinated Omission の補正（予定開始時刻からの遅延で測る）。**実装済み** |
| 3 | 時系列（1 秒ごと）・同時実行数の推移、接続の再利用率と DNS/TCP/TLS 時間（時系列での推移を含む）、負荷生成側の健全性（CPU・ゴルーチン数）。**実装済み**（集計・JSON・結合・HTML のグラフ） |

## 6. MVP の範囲

**入れる**: `Scenario` / `User` / `Step` / `Do()` / `ExpectStatus` / `Think` / `Plan`（`Users` のみ）/ ステップ単位のレイテンシとエラー数のコンソール出力（フェーズ1の計測）

**後回し**（MVP の時点）: `Rate`（フェーズ 2 で実装済み）、レスポンスからの値の抽出ヘルパー（まずは `Decode`）、vegeta 互換出力（8 章で当面作らないと決めた）、案B（ビルダー）、YAML

## 7. 実装状況

フェーズ 1（MVP）・フェーズ 2（`Rate`）・フェーズ 3（時系列・接続内訳・負荷生成側の健全性）は実装済み。`go run ./examples/checkout` で動作を確認できる。

パッケージは 4 つ。使う人の入口は `jotaro`（負荷をかける側）で、結果を扱う側は `report`、記録は `record` に分けてある。依存は `jotaro` → `report` → `record`（`jotaro` は `record` にも依存する）の一方向で、`internal/histogram` は `report` だけが使う。

| パッケージ | ファイル | 内容 |
|---|---|---|
| `jotaro` | `scenario.go` `user.go` `step.go` | `Scenario` / `User` / `Step`（`Think`、panic の回復を含む） |
| | `request.go` `response.go` `exec.go` | リクエスト記述とビルダー、`httptrace` つきの実行、エラー分類 |
| | `run.go` | `Plan` / `Run`（`Users` / `Arrival` の両モデル。`Plan.Raw` で生データを書く） |
| | `arrival.go` | `Arrival` / `Rate` / `Ramp` / `Stages` |
| | `feeder.go` | `Feeder` / `LoadCSV` |
| | `health.go` | 負荷生成側の健全性（ゴルーチン数・CPU）を 1 秒ごとにサンプリング |
| `record` | `record.go` | `Record` / `StepRecord` / `IterationRecord` / `Collector` / `ErrKind` / `ConnKind` / `HealthPoint` |
| | `raw.go` | `Raw` / `WriteRaw` / `ReadRaw`（生データの JSON Lines） |
| `report` | `report.go` `series.go` | `Report` / `Latency` と集計、時系列（1 秒ごと） |
| | `print.go` `json.go` `html.go` `html_chart.go` `write.go` | テキスト・JSON・HTML の出力と、JSON の読み込み |
| | `merge.go` | `MergeReports` |
| | `health.go` | 負荷生成側の健全性の要約・結合 |
| | `threshold.go` | `Threshold` / `ParseThreshold` / `Report.Check`（しきい値判定。[`docs/result.md`](docs/result.md) 5 章） |
| `internal/histogram` | `histogram.go` | 結合のための対数ヒストグラム |
| `cmd/jotaro` | `main.go` | CLI（`report` / `merge` / `check`） |

実装中に決めたこと:

- Cookie jar はイテレーションごとに新しくする（k6 の既定と同じ。毎回ログインし直す）。接続プールは全ユーザーで共有する。
- パーセンタイルは、現状は全記録をソートして求める（nearest-rank）。集計中のコピーは避けてある（記録をコピーせず位置で持つ、一時スライスをその場でソートする、`Run` は `Collector.Take` で記録を受け取る）。100 万リクエストで集計中の確保が 1.4GB → 0.15GB。ただし記録そのもの（100 万件で約 240MB）は実行が終わるまで持つので、それが問題になったら、記録を逐次ヒストグラムに畳む形に変える。
- リクエストのレイテンシには、通信エラー（タイムアウトなど）になったものも含める。TTFB は応答を受け取れたものだけ。
- `BytesIn` / `BytesOut` はボディのバイト数（ヘッダーは含まない）。

## 8. 決めたことと未決

### 決めたこと

- **`Pacer` に近い抽象:** 自前の `Arrival` を持つ（vegeta の `lib` は import しない）。依存を増やさず、シナリオ単位で制御するため。可変レートは `Arrival` の実装を増やして対応する（`Ramp` で直線的なランプアップ、`Stages` で段階的な負荷を実装済み）。
- **思考時間を含むシナリオ時間:** 併記する（実装済み）。`IterationStats.Active`（`Think` を除く）と `Total`（含む）を両方持ち、コンソール・JSON・HTML のどれにも両方出す。
- **ステップが失敗したときの挙動:** ヘルパーは提供しない。`u.Step` は `fn` のエラーをそのまま返し、失敗は `StepRecord` に残る。止めるなら `return err`、続けるなら無視する。ふつうの Go の `if` で書けるのが案 A の利点で、ヘルパーを足すと「既定はどちらか」という暗黙の規則が増えるため。
  - イテレーションの成否は、シナリオの関数が返したエラーで決まる。ステップが失敗しても続けて最後まで `nil` を返せば、そのイテレーションは成功として数える（ステップの失敗は、ステップ単位の集計 `StepStats.Failed` で見る）。
  - 案 B（ビルダー）を作るときは、ステップの並びでの既定（続ける／止める）を決める必要がある。そのときに見直す。
- **vegeta 互換:** 当面は作らない。vegeta の `Result` にするとシナリオ・ステップの情報が落ちる。生データ（JSON Lines。[`docs/result.md`](docs/result.md)）を出せるようになったので、変換は後から足せる。需要が出たら、生データを vegeta 形式に変換する `jotaro export` のようなサブコマンドで対応する（`lib` の型を直接使う案は採らない。`Pacer` と同じく依存を増やさないため）。

### 未決

- **値の受け渡しを型安全に保つ範囲:** A（Go の変数と `Decode`）だけなら問題ない。B・YAML では文字列キーになるので、案 B か YAML を作るときに決める。
