# jotaro 設計メモ

[English](DESIGN.md) | 日本語

シナリオ表現力に重点を置いた、Go 製の負荷試験ツール（ライブラリ＋薄い CLI）。
壁打ちで決めた内容をまとめたもの。未決の項目は末尾に記載。

- モジュール: `github.com/RIRUa/jotaro`
- Go: 1.25 以上（`go.mod`）。開発では 1.27.3 を使う（`.mise.toml` で固定）。CI は 1.25 と最新の安定版で試す

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
| 実行モデル | 1 ユーザー（シナリオの 1 回の実行）= 1 goroutine。`u.Step(name, fn)` でステップを区切る。`User` はその goroutine からだけ使う。1 つのステップの中のリクエストは並行に送ってよく（`Step.Parallel`）、予定到着時刻はそのうちちょうど 1 つが取る |
| プロトコル | HTTP のみ。他のプロトコルを後で差し込めるようインターフェースを切る予定だったが、まだ無い（`exec.go` が `net/http` を直接呼ぶ）。2 つ目のプロトコルが実際に必要になったときに切る |
| YAML | 後回し。実現するなら YAML → B（データ構造）→ A（実行）の片方向で、実行時に読み込む方式 |
| リクエスト記述 | 内部でデータ構造として持つ（`RequestSpec`: メソッド・パス・クエリ・ヘッダー・ボディ）。YAML の道を残すため。値の抽出と期待値も最初はここに入れる予定だったが、実装では `Response` のメソッド（`Decode`、`JSON(path)`、`Expect*`）になった |

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

- `Do()` は最初、通信エラーのときだけ `error` を返す予定だった。実装では `*Response` を返し、呼び出しをつなげられるようにした（`Do().Decode(&v)`）。通信エラーはレスポンスの中に持たれ、`Err()`、`Decode`、`JSON`、`Expect*` が返す。HTTP 4xx/5xx はステータスとして記録し、Go のエラーにはしない（404 を期待するテストを書けるように）。
- 期待値の検証は明示的に行い、外れたら `error`（`*ExpectError`）を返す: `ExpectStatus(201)`、`ExpectStatusIn(...)`、`ExpectHeader`、`ExpectBodyContains`、`ExpectJSON(path, want)`。不一致は応答が届いたあとで決まるので、リクエストではなくステップに `expect_mismatch` として記録する（8 章の「`error_rate` が数えるもの」を参照）。
- レスポンスからの値の取り出しは、`Decode(&v)`（構造体へ）か、`Response.JSON(path)`（`data.items.0.id` のようなドット区切りのパスの値を 1 つ。構造体は要らない）。
- `fn` が `error` を返すとそのステップは失敗。シナリオを止めるかどうかは、呼び出し側が `return err` するかどうかで決める。

### `User` が持つもの

ユーザー ID、何回目の実行か、Cookie jar 付き `http.Client`（イテレーションごとに新しくなる）、`Think()`。

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
- `UserStages`（クローズド、ユーザー数が変化）: `UserStage{Target, For}` の段階で、有効ユーザー数を直前の Target（開始時は `StartUsers`、既定は 0）から Target まで直線的に動かす。**実装済み**。
- `Rate`（オープン）: 到着レート固定。vegeta の強みをシナリオ単位で再現する。**実装済み**。可変レートは `Ramp`（レートを直線的に上げる）と `Stages`（レートを段階ごとに変える）で、どちらも同じ `Arrival` インターフェースで実装済み。
- `Scenarios`（重みつきの混合。`Scenario` の代わりに指定）: `[]Weighted{Scenario, Weight}`。イテレーション（クローズド）または到着（オープン）ごとに、`Weight / 重みの合計` の確率で 1 つのシナリオをランダムに選ぶ。`Scenario` とは排他で、レポートとしきい値はシナリオ名で結果を分けるので、名前は重ならないようにする。負荷のモデルとは独立している。**実装済み**。

`UserStages` モデルの動き:

- 全ユーザー（`StartUsers` と各 Target の最大値ぶん）を先に確保し、そのうち `StartUsers` 人は最初から動く。ユーザー `id` は有効人数が `id` を超えている間だけ動くので、ID の大きいユーザーから先に止まり、小さいユーザーから先に始まる。
- コントローラが事前に計算した時刻で人数を動かす。`a` 人から `b` 人への段階は `For / |b - a|` ごとに 1 人ずつ動かすので、段階の終わりにちょうど Target に達する。最後の段階のあとは、`Duration` まで Target の人数を保つ。
- 停止は協調的: 人数を超えたユーザーは、実行中のイテレーションを終えてから待機する（ユーザーごとの容量 1 のチャネルで起こすので、全員が一斉に起きることはない）。何も中断しないので、イテレーションが長いと `Series.Concurrency` が目標を上回る時間が出る。
- `User.ID` とイテレーション番号は実行全体で安定している。再開したユーザーは次のイテレーション番号から続ける。Cookie jar はイテレーションごとに新しくする。
- 負荷の形は `Series.Concurrency`（毎秒の実行中イテレーション数のピーク）から読む。専用の系列は持たない。
- `RepeatUserStages(n, stages...)` は、段階の並びを n 回くり返した `[]UserStage` を返すだけのヘルパー。コントローラと検証は繰り返しを知らないので、くり返した形は同じ段階を手で書き並べた場合と完全に同じ動きになる。

`Rate` モデルの動き:

- 到着時刻は開始時刻からの累積（`i × 間隔`）で決める。ディスパッチャが遅れても間隔はずれず、遅れた分はまとめて起動する。
- 同時実行数が `MaxUsers`（既定 1000）に達しているときの到着は、実行せず**ドロップ**として数える（`Report.Dropped`）。待たせたり、並べたりはしないので、ドロップされた到着はレイテンシの標本を残さない。`corrected_*` のしきい値は `dropped<1` と併記するのがよい。
- イテレーションの最初のリクエストの `Scheduled` に予定到着時刻を入れる。`Record.Corrected()`（予定時刻から応答完了まで）が Coordinated Omission の補正後の値で、`RequestStats.Corrected` に集計する。2 つ目以降のリクエストは `Scheduled = Start`（前のリクエストの遅れは実際の遅延なので補正しない）。
- 到着ごとに別のユーザーとして扱う（`User.ID` は到着の通し番号、`Iteration` は 0）。終了の仕方はクローズドモデルと同じ（`Duration` / `Grace`）。

実行の終わり方（全モデル共通）:

- `Duration` を過ぎたら、新しいイテレーションを始めない。
- `Plan.AbortOn` は実行を早めに終える。1 秒ごとに集計器のスナップショットを各ルール（「これが成り立ったら止める」と読む閾値）で判定し、`For` の間途切れず成り立ったルールが発動する。発動すると `Duration` の終わりと同じ扱い（新規イテレーションを始めない）になるが、`Grace` は中断した時点から数える。`Run` は途中までのレポートと `ErrAborted` を包んだエラーを返すので、CI では既定で失敗になる。条件は直近の窓ではなく「ここまでの実行全体」に対するものなので、実行の後半で起きた障害には反応が遅い。`http_5xx_rate>50%` のようなルールは、最初から壊れているサーバー向き。
- 実行中のイテレーションは `Grace`（既定 30 秒）まで完了を待つ。超えたものは中断して `canceled` として記録する。リクエスト自体のタイムアウト（`Timeout`、既定 30 秒）は `timeout` で、区別される。
- 親の `ctx` が終わったら、実行中のものも含めてすぐ止まる。
- そのため `Report.Elapsed` は `Duration` より少し長くなり、RPS はわずかに低めに出る。

実行中の状態を見る手段（どれも任意で、同じ記録の流れを受け取る）:

- `Plan.Progress`（`io.Writer`）: 1 秒ごとに `12s/60s rps=480 p95=38ms err=0.2% 5xx=31.0% fail=31.2%` のような 1 行を書く（`5xx`・`fail`・`dropped` は 0 でない間だけ）。軽いカウンタと専用のヒストグラムで数えるので（`progress.go`）、`Plan.Raw` の有無でコストは変わらない。
- `Plan.MetricsAddr`（たとえば `":9090"`）: 集計器のスナップショットを Prometheus のテキスト形式で `/metrics` に公開する（`metrics.go`）。テキストは自前で書くので `client_golang` の依存は増えない。ラベルはステータスクラスとエラーの種類だけで、レイテンシは固定バケットのヒストグラムではなく分位点の summary。サーバーは `Run` が返ると止まり、アドレスが不正なら、リクエストを送る前に失敗する。
- `Plan.AbortOn`: 上を参照。

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

実行時間が長くてもメモリが増えないよう、`Run` は記録を持たずに、届くたびにヒストグラムへ畳み込む（`report.Aggregator`。`record.NewStreamingCollector` 経由で受け取る）。代わりにパーセンタイルは近似（相対誤差 1% 以内）になる。件数・最小・最大・平均は正確なまま。これは `Plan.Raw` を指定しても変わらない。`Run` は記録を届くたびに `Plan.Raw` へ書き出し（`record.RawWriter`）、やはり何も持たないので、そのとき返す `Report` も近似になる（当初は `Plan.Raw` のときだけ記録を持ち、`BuildReport` で正確に集計する予定だったが、それでは実行に比例してメモリが増える）。正確な値は 1 手で得られる: 書き出したデータを `report.FromRaw`（`jotaro report -raw`）で集計し直す。

### フェーズ

| フェーズ | 内容 |
|---|---|
| 1 (MVP, `Users`) | パーセンタイル（p50/p90/p95/p99/max）、エラー内訳（タイムアウト／接続拒否／DNS／TLS／HTTP 4xx・5xx／`Expect` 不一致、ステップ別）、スループット（実 RPS・イテレーション/秒）、TTFB、送受信バイト数、思考時間を除いたシナリオ時間 |
| 2 (`Rate`) | ドロップされたイテレーション数、Coordinated Omission の補正（予定開始時刻からの遅延で測る）。**実装済み** |
| 3 | 時系列（1 秒ごと）・同時実行数の推移、接続の再利用率と DNS/TCP/TLS 時間（時系列での推移を含む）、負荷生成側の健全性（CPU・ゴルーチン数）。**実装済み**（集計・JSON・結合・HTML のグラフ） |

## 6. MVP の範囲

**入れる**: `Scenario` / `User` / `Step` / `Do()` / `ExpectStatus` / `Think` / `Plan`（`Users` のみ）/ ステップ単位のレイテンシとエラー数のコンソール出力（フェーズ1の計測）

**後回し**（MVP の時点）: `Rate`（フェーズ 2 で実装済み）、レスポンスからの値の抽出ヘルパー（まずは `Decode`。あとから `Response.JSON(path)` が入った）、vegeta 互換出力（8 章で当面作らないと決めた。あとから `jotaro export` として入った）、案B（ビルダー）、YAML

## 7. 実装状況

フェーズ 1（MVP）・フェーズ 2（`Rate`）・フェーズ 3（時系列・接続内訳・負荷生成側の健全性）は実装済み。`go run ./examples/checkout` で動作を確認できる。

ライブラリのパッケージは 4 つ。使う人の入口は `jotaro`（負荷をかける側）で、結果を扱う側は `report`、記録は `record` に分けてある。依存は `jotaro` → `report` → `record`（`jotaro` は `record` にも依存する）の一方向で、`internal/histogram` は `report` と `jotaro`（進捗の表示。`progress.go`）が使う。ライブラリの上に、CLI（`cmd/jotaro`）と `jotaro mcp` の MCP サーバー（`internal/mcpserver`）があり、下の表にも載せている。

| パッケージ | ファイル | 内容 |
|---|---|---|
| `jotaro` | `scenario.go` `user.go` `step.go` | `Scenario` / `User` / `Step`（`Think`、panic の回復を含む） |
| | `request.go` `response.go` `exec.go` | リクエスト記述とビルダー、`httptrace` つきの実行、エラー分類 |
| | `run.go` | `Plan` / `Run` / `Weighted`（`Users` / `UserStages` / `Arrival` の各モデル。重みつきの混合は `Scenarios`。`Plan.Raw` で生データを逐次書く） |
| | `abort.go` | `AbortRule` / `ErrAborted` と、`Plan.AbortOn` を支える 1 秒ごとの監視 |
| | `progress.go` | 1 秒ごとの進捗の 1 行（`Plan.Progress`）と、記録を複数のオブザーバーへ配る処理 |
| | `metrics.go` | 自前で書いた `/metrics`（`Plan.MetricsAddr`） |
| | `user_stages.go` | `UserStage` と有効ユーザー数のスケジュール |
| | `arrival.go` | `Arrival` / `Rate` / `Ramp` / `Stages` |
| | `feeder.go` | `Feeder` / `LoadCSV` |
| | `health.go` | 負荷生成側の健全性（ゴルーチン数・CPU）を 1 秒ごとにサンプリング |
| `record` | `record.go` | `Record` / `StepRecord` / `IterationRecord` / `Collector` / `Observer` / `NewStreamingCollector` / `ErrKind` / `ConnKind` / `HealthPoint` |
| | `raw.go` | `Raw` / `WriteRaw` / `ReadRaw`（生データの JSON Lines） |
| | `raw_writer.go` | `RawWriter`: 実行中に生データを 1 記録ずつ書く（`Plan.Raw`） |
| `report` | `report.go` `series.go` `aggregate.go` | `Report` / `Latency` と集計、時系列（1 秒ごと）、記録を持たない逐次集計（`Aggregator`） |
| | `print.go` `json.go` `html.go` `html_chart.go` `write.go` | テキスト・JSON・HTML の出力と、JSON の読み込み |
| | `merge.go` | `MergeReports` |
| | `compare.go` | `Compare` / `Comparison`: 2 つの結果の差と p95 の悪化の判定（`jotaro compare`） |
| | `export.go` | `WriteVegeta` / `Report.WriteSeriesCSV`（`jotaro export`） |
| | `health.go` | 負荷生成側の健全性の要約・結合 |
| | `threshold.go` | `Threshold` / `ParseThreshold` / `Report.Check`（しきい値判定。[`docs/result_jp.md`](docs/result_jp.md) 5 章） |
| `internal/histogram` | `histogram.go` | 結合のための対数ヒストグラム |
| `cmd/jotaro` | `main.go` | CLI（`report` / `merge` / `check` / `compare` / `export` / `mcp` / `version`） |
| `internal/mcpserver` | `server.go` `resources.go` `tools.go` `check.go` `lint.go` `result.go` `summary.go` `prompts.go` | `jotaro mcp` の MCP サーバー（シナリオ作成の補助: ガイドの resource、`check_scenario`、`validate_threshold`。保存した結果の読み取り: `summarize_result`、`check_result`） |

実装中に決めたこと:

- Cookie jar はイテレーションごとに新しくする（k6 の既定と同じ。毎回ログインし直す）。接続プールは全ユーザーで共有する。
- `BuildReport`（`FromRaw` とライブラリ利用者が使う）は、パーセンタイルを全記録のソートで求める（nearest-rank）。集計中のコピーは避けてある（記録をコピーせず位置で持つ、一時スライスをその場でソートする、`Collector.Take` は記録を持つ `Collector` から、コピーせずに渡す）。`Run` がまだ `BuildReport` を使っていたころは、100 万リクエストで集計中の確保が 1.4GB → 0.15GB になった。記録そのもの（100 万件で約 240MB）は、`Plan.Raw` の有無にかかわらず `Run` は持たない。`Aggregator` がヒストグラムに畳み込む（`RawWriter` は届くたびに書き出す）。500 万リクエストで、収集側が保持するヒープは約 1165MB から約 9MB になった（1 台での測定）。時系列の同時実行数は、保存したイベントを走査するのではなく、イテレーションの開始・終了のたびに数える（1 秒ごとに現在値と最大値を 1 つずつ持つ）。
- リクエストのレイテンシには、通信エラー（タイムアウトなど）になったものも含める。TTFB は応答を受け取れたものだけ。
- `BytesIn` / `BytesOut` はボディのバイト数（ヘッダーは含まない）。

## 8. 決めたことと未決

### 決めたこと

- **`Pacer` に近い抽象:** 自前の `Arrival` を持つ（vegeta の `lib` は import しない）。依存を増やさず、シナリオ単位で制御するため。可変レートは `Arrival` の実装を増やして対応する（`Ramp` で直線的なランプアップ、`Stages` で段階的な負荷を実装済み）。
- **思考時間を含むシナリオ時間:** 併記する（実装済み）。`IterationStats.Active`（`Think` を除く）と `Total`（含む）を両方持ち、コンソール・JSON・HTML のどれにも両方出す。
- **ステップが失敗したときの挙動:** ヘルパーは提供しない。`u.Step` は `fn` のエラーをそのまま返し、失敗は `StepRecord` に残る。止めるなら `return err`、続けるなら無視する。ふつうの Go の `if` で書けるのが案 A の利点で、ヘルパーを足すと「既定はどちらか」という暗黙の規則が増えるため。
  - イテレーションの成否は、シナリオの関数が返したエラーで決まる。ステップが失敗しても続けて最後まで `nil` を返せば、そのイテレーションは成功として数える（ステップの失敗は、ステップ単位の集計 `StepStats.Failed` で見る）。
  - 案 B（ビルダー）を作るときは、ステップの並びでの既定（続ける／止める）を決める必要がある。そのときに見直す。
- **vegeta 互換:** 最初は当面作らないと決めた。vegeta の `Result` にするとシナリオ・ステップの情報が落ちる。生データ（JSON Lines。[`docs/result_jp.md`](docs/result_jp.md)）を出せるようになってからは、生データを vegeta 形式に変換するサブコマンドで対応し、`lib` の型は直接使わないと決めた（`Pacer` と同じく依存を増やさないため）。その後、決めたとおりに実装した: `jotaro export -format vegeta`（毎秒の系列には `-format csv`）と `report.WriteVegeta`。この形式はそれ以上の情報を持てないので、attack 名は `<シナリオ>/<ステップ>`。
- **`error_rate` が数えるもの:** 通信レベルのエラー（タイムアウト、接続拒否、DNS 失敗など）で終わったリクエストだけ。`Expect*` の失敗は応答が届いたあとで決まるので、ステップのものとして扱う。ステップを `expect_mismatch` で失敗させ、シナリオがそのエラーを返したときは `iteration_fail_rate` に反映される。文書は「`error_rate` は Expect の不一致を含む」と書いていたが、実装はそうなっていなかった。v0.1.3 では実装のほうを残し、文書を直した。誤ったレスポンスで不合格にしたいときは、ステップのエラーをシナリオから返し、`iteration_fail_rate` にしきい値を置く。

### 未決

- **値の受け渡しを型安全に保つ範囲:** A（Go の変数と `Decode`）だけなら問題ない。B・YAML では文字列キーになるので、案 B か YAML を作るときに決める。
