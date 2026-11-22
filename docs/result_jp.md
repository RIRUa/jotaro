# 結果（リザルト）の仕様

[English](result.md) | 日本語

jotaro の実行結果に関する仕様メモ。ここに書いてあることはすべて実装済み。2・3 章は、出力形式と閲覧手段をどう決めたかという経緯を残したもので、1 章と 4〜6 章が今の動作を説明する。

## 1. 現状（実装済み）

結果は **メモリ上の `Report` 構造体**で、コンソール表示・JSON・HTML に書き出せる（4 章）。生データは JSON Lines に書き出せる（下の「生データ」）。

```
                       ┌─▶ report.Aggregator ─▶ *report.Report ─▶ Write ─▶ コンソール / JSON / HTML
Run() ─▶ Collector ────┤     （AbortOn と /metrics も読む）
 （記録は届くたびに）   ├─▶ record.RawWriter ─▶ Plan.Raw（JSON Lines）
                       └─▶ 進捗カウンタ ─▶ Plan.Progress
```

`Run` は記録を保持しない。`Collector` は記録が届くたびにオブザーバー（`record.Observer`）へ渡すので、実行が長くなってもメモリは増えない。

| 層 | 型 | ファイル | 役割 |
|---|---|---|---|
| 生データ | `record.Record` / `StepRecord` / `IterationRecord` | `record/record.go` | リクエスト・ステップ・シナリオ1回ごとの記録 |
| 収集 | `record.Collector` | `record/record.go` | 並行に記録を受け取る。`NewStreamingCollector`（`Run` が使う）は記録を `record.Observer` へ渡して何も保持しない。`NewCollector` は保持する（`Snapshot()` でコピー取得） |
| 集計 | `report.Report` | `report/report.go` | 3階層（リクエスト / ステップ / シナリオ）の集計結果。`report.Aggregator`（`report/aggregate.go`。`Run` が使う）が逐次作るか、`report.BuildReport` / `FromRaw` が全記録から作る |
| 出力 | — | `report/print.go` / `json.go` / `html.go` | コンソール・JSON・HTML への出力 |
| 生データの書き出し | `record.RawWriter` | `record/raw_writer.go` | 記録を届くたびに `Plan.Raw` へ書く |

### `Report` の構造

```
Report
├─ Open        オープンモデル（Arrival: Rate / Ramp / Stages）で実行したか
├─ Dropped     MaxUsers に達していたため実行しなかった到着の数
├─ StartedAt   実行の開始時刻（UTC。不明ならゼロ値）
├─ Elapsed     実行時間（Duration より少し長くなる）
├─ Requests    RequestStats（全リクエスト合計）
├─ Scenarios[] ScenarioReport
│  ├─ Name
│  ├─ Iterations  IterationStats（Active = Think 除く / Total = Think 含む）
│  └─ Steps[]     StepStats（最初に実行された順）: Count、Failed、Duration、Errors（ステップが失敗した理由。`ErrKind` ごと）
│     └─ Requests RequestStats
├─ Series[]    SeriesPoint: StartedAt からの 1 秒ごとの集計（「レポートの結合」を参照）
└─ Health[]    record.HealthPoint: 負荷生成側の 1 秒ごとの健全性（`Run` で実行したときだけ）
```

### `Latency`（時間の分布）

| フィールド | 内容 |
|---|---|
| `Count` | 件数 |
| `Min` / `Mean` / `Max` | 最小 / 平均 / 最大 |
| `P50` `P90` `P95` `P99` | パーセンタイル（nearest-rank 法） |

### `RequestStats`

| フィールド | 内容 |
|---|---|
| `Count` | リクエスト数 |
| `Latency` | レイテンシ。通信エラーのものも含む |
| `TTFB` | 応答を受け取れたものだけ |
| `DNS` / `Connect` / `TLS` | 接続を張る各区間の時間。その区間が実際に起きたリクエストだけ |
| `NewConns` / `ReusedConns` | 接続を新しく張った / 再利用したリクエストの数 |
| `Corrected` | 予定開始時刻からの時間（Coordinated Omission 補正後）。クローズドモデル（`Users` / `UserStages`）では `Latency` と同じ |
| `Status` | `"2xx"` などステータスクラスごとの件数 |
| `Errors` | 通信レベルのエラーの内訳（`ErrKind` ごと）。`Expect*` の失敗は入らない（ステップの `Errors` に入る） |
| `BytesIn` / `BytesOut` | ボディのバイト数（ヘッダーは含まない） |

### `ErrKind`（失敗の種類）

| 値 | 意味 |
|---|---|
| `""`（`ErrNone`） | 成功 |
| `timeout` | リクエストのタイムアウト |
| `connection_refused` | 接続拒否 |
| `dns` | 名前解決の失敗 |
| `tls` | TLS の失敗 |
| `canceled` | `Grace` 超過などによる中断 |
| `expect_mismatch` | `Expect*`（`ExpectStatus`、`ExpectJSON` など）の不一致。ステップとイテレーションにだけ現れる |
| `panic` | シナリオ関数が panic した（テストコードのバグで、対象の性質ではない）。ステップとイテレーションにだけ現れる |
| `other` | 上記以外 |

HTTP 4xx/5xx は `ErrKind` ではなくステータス（`Status`）として記録される。

どこに記録されるか: 通信レベルの種類（`timeout`、`connection_refused`、`dns`、`tls`、`canceled`、`other`）はリクエストに記録され、エラーがステップやイテレーションから返されたときは、それらにも記録される。`panic` は、実行中だったステップとイテレーションに記録される。ステップは panic までの時間つきで失敗（`Failed`）として一覧に載り、panic 前に送ったリクエストもそのステップに残り、実行は他のユーザーで続く。実行中の最初の panic は、実行が終わったあとにスタックつきで一度だけ `Plan.Progress`（`Progress` が nil のときは標準エラー）に書かれ、以降のものは数えるだけ。`expect_mismatch` は、応答が届いたあとの `Expect*` の呼び出しで決まるので、リクエストには**記録されない**。ステップの `Errors`（シナリオがそのエラーを返したときはイテレーションの `Errors`）に現れ、ステップを失敗（`Failed`）にする。`error_rate`（5 章）に映らないのはこのため。

ステップの `Errors` は、失敗した実行 1 回につき `ErrKind` 1 つの理由で、合計は `Failed` と一致する。

理由の表示先: テキストレポートは「steps (duration)」の `failures` 列（`expect_mismatch=500`）に、HTML レポートはステップの表の同じ列に出す。「steps (requests)」の `errors` 列は、リクエストの通信レベルのエラーだけを数えるので、どちらも `net errors` という名前にしてある。チェックが失敗したステップは、`fail` が 0 より大きく、`failures` に `expect_mismatch` が出て、`net errors` は `-` になる。`jotaro mcp` の `summarize_result` は同じ内訳を各ステップの `errors` として返し、`failed_checks` 警告には種類を添える。失敗のないステップの `failures` は `-`。

### 補足

- `RPS()` は `Requests.Count / Elapsed`。
- `Run` は `Plan.Raw` の有無にかかわらず逐次集計する（`report.Aggregator`）。記録は届くたびにカウンタと対数ヒストグラムへ畳み込み、保持しないので、メモリはリクエスト数に比例して増えない。件数・最小・最大・平均は正確で、`P50`〜`P99` は相対誤差 1% 以内の近似値（`[最小, 最大]` に収める）。
- `FromRaw`、`BuildReport` は全記録を受け取ってソートするので、`P50`〜`P99` は nearest-rank の正確な値になる。ある実行の正確なパーセンタイルが欲しいときは、`Plan.Raw` に書き出してから集計し直す（`jotaro report -raw`）。

### JSON 出力（実装済み）

`Report.WriteJSON(w io.Writer) error` で、集計結果を JSON として書き出せる（`report/json.go`）。

```go
f, _ := os.Create("result.json")
defer f.Close()
if err := rep.WriteJSON(f); err != nil {
    log.Fatal(err)
}
```

- 時間はすべて**ナノ秒の整数**（フィールド名は `_ns` で終わる）。
- トップレベルに `version`（現在 `3`、定数 `JSONVersion`）を持つ。互換を壊す変更のたびに上げる。`ReadJSON` は 1 以降を読める。
  - `3`: `series`（1 秒ごとの時系列）と、`requests` の `dns` / `connect` / `tls` / `conn_new` / `conn_reused` を追加。`series` の接続まわりの項目と `health`（負荷生成側の健全性）は、あとから version 3 に足した（無い JSON は 0・空として読むので版は上げていない）
  - `2`: `started_at`（実行の開始時刻、RFC 3339）と、各 `latency` の `buckets`（ヒストグラム）を追加
  - `1`: 初版
- 内部の `Report` とは別の出力用の型を持つので、`Report` を変えても JSON の形は変わらない。
- 空の集合は `null` ではなく `[]` / `{}` で出す（読む側が分岐しなくて済む）。
- 追加で `rps` を出す（`Requests.Count / Elapsed`）。リクエストの集計は、分布を `latency` / `ttfb` / `corrected` / `dns` / `connect` / `tls` の 6 つ、件数を `conn_new` / `conn_reused` で持つ。

### HTML レポート（実装済み）

`Report.WriteHTML(w io.Writer) error` で、外部に依存しない 1 枚の HTML を書き出せる（`report/html.go` / `report/report.html.tmpl`）。

```go
f, _ := os.Create("report.html")
defer f.Close()
if err := rep.WriteHTML(f); err != nil {
    log.Fatal(err)
}
```

- `html/template` で生成するので、シナリオ名やステップ名は自動でエスケープされる。
- CSS は埋め込み、JS・グラフのライブラリ・外部リソースは使わない（グラフはインラインの SVG）。ライト／ダークは OS の設定に従う。
- ページは `<html lang="en">` を宣言する（表示は英語）。開始時刻（UTC）をヘッダーと `<title>` に出すので、複数のレポートを見分けられる。開始時刻が不明なときは、どちらにも出さない。
- 内容: サマリー（負荷のモデル `closed` / `open` を含む）、ステータス・エラーの内訳、レイテンシ（p50〜max の棒）、`corrected`（オープンモデル、つまり `Arrival` のときのみ）、TTFB、シナリオ別のイテレーション時間とステップ別の表（`failures` 列に各ステップの失敗理由）。レポートに時系列があるときは、続けて 1 秒ごとのタイムラインとして、インラインの SVG 折れ線グラフを出す: リクエスト数・エラー・5xx、レイテンシ p50 / p95 / p99、同時実行数、接続、DNS / connect / TLS の時間、負荷生成側のゴルーチン数と CPU。
- 表示用の値は `Print` と同じ書式（`fmtDur`）。ナノ秒の無損失な値は JSON を使う。
- サンプル: `go run ./examples/checkout -format html -o report.html`（`-rate 30` で `Rate` モデル）。

### レポートの結合（実装済み）

負荷生成機を分けて同時に実行した結果などを、1 つにまとめる。

```
jotaro merge -o merged.json a.json b.json c.json
jotaro merge -format html -o report.html a.json b.json
```

ライブラリでは `report.MergeReports(reps ...*report.Report) (*report.Report, error)`（`report/merge.go`）。`-format` の既定は `json`（結合結果を再び入力にできる）。

| 項目 | 結合の仕方 |
|---|---|
| 件数・バイト数・`Dropped`・ステータス・エラー | 合計 |
| `StartedAt` / `Elapsed` | 最初の開始〜最後の終了（`StartedAt + Elapsed` の最大）。同時でも順次でも RPS が正しくなる |
| `Min` / `Max` / `Mean` | 正確 |
| `P50`〜`P99` | ヒストグラムを足して求め直す（**相対誤差 1% 以内**） |
| シナリオ・ステップ | 名前で突き合わせ。並びは最初に現れた順 |
| `Open` が揃っていない | エラー（モデルが違う結果は混ぜられない） |

- **ヒストグラム**: 各 `latency` に対数バケット（DDSketch 方式、相対誤差 1%）を持たせた（JSON の `buckets`: バケット番号 → 件数）。バケットは足し算で結合できるので、何台分でも順序に関係なく同じ結果になる。全記録から作ったレポート（`BuildReport`、`FromRaw`）の `P50`〜`P99` は nearest-rank の正確な値。逐次集計したもの（`Run`。`Plan.Raw` の有無は問わない）と、結合したものは近似になる。
- **サイズ**: バケットの数は分布の広がりで決まる（件数には比例しない）。10 万件・裾の重い分布で 1 つの `latency` あたり数百バケット。
- **接続の内訳 (`requests` と各ステップの `requests`、version 3)**: `dns` / `connect`（TCP）/ `tls` は、その区間が実際に起きたリクエストだけのレイテンシ分布（`buckets` つき）。接続を再利用したリクエストや、http の TLS は含まない。`conn_new` / `conn_reused` は接続を新しく張った / 再利用したリクエストの数で、再利用率は `conn_reused / (conn_new + conn_reused)`。接続を得る前に失敗したリクエストはどちらにも数えない。version 1・2 の JSON には無い（0・空として読める）。
- **時系列 (`series`、version 3)**: `started_at` からの 1 秒ごとの配列。要素は `second` / `requests` / `errors`（通信エラー。HTTP 4xx/5xx は含まない）/ `http_5xx`（HTTP 5xx の数。`errors` とは重ならない。無ければ 0）/ `latency`（`buckets` つき）/ `conn_new` / `conn_reused`（その秒に開始したリクエストのうち、接続を新しく張った / 再利用した数）/ `dns` / `connect` / `tls`（その区間が起きたリクエストだけの分布。`buckets` つき。`requests` の同名のフィールドと同じ範囲）/ `concurrency`（その秒の実行中イテレーション数の最大）。`started_at` が無いと空。結合では `started_at` の差だけ秒をずらして足し、`concurrency` は合計（上限の見積り）。version 1・2 の JSON には無い。`conn_new` 以降は version 3 に後から足した項目で、無い JSON は 0・空として読む（互換を壊さないので版は上げていない）。
- **負荷生成側の健全性 (`health`、version 3)**: `jotaro.Run` を動かしているプロセスの、`started_at` からの 1 秒ごとの配列。要素は `second` / `goroutines`（その秒の終わりのゴルーチン数）/ `cpu`（その秒に使った CPU 時間の割合。GOMAXPROCS 個の CPU 全体に対して 0〜1。取得できない環境では -1）。`BuildReport` だけで作った結果や、古い JSON には無い（空）。結合では秒をずらして重ね、`goroutines` は合計、`cpu` は最大（負荷生成側が複数あるときは、一番詰まっているものを見る）。
  - CPU は `getrusage`（Unix 系）の CPU 時間から求める。Windows など取得できない環境では -1 で、グラフや平均には含めない。`runtime/metrics` の `/cpu/classes/*` は GC のたびにしか更新されないため使っていない。
  - 負荷をかける対象を同じプロセスで動かしている（`examples/checkout` など）と、その分も含む。
  - CPU の最大が 90% 以上のとき、テキストと HTML のサマリーに「負荷生成側が詰まって測定値が悪く出ている疑い」の警告を出す。
- **version 1 の JSON との互換**: 読める。`buckets` と `started_at` が無いので、それが混じる結合では次のフォールバックになる。
  - `P50`〜`P99` は件数で重み付けした平均（誤差が大きい）。
  - `Elapsed` は最大値（同時に実行した前提）。

### 生データ（JSON Lines）（実装済み）

集計前の記録を 1 行 1 レコードで書き出し、後から再集計したり、他のツールで可視化したりできる（`record/raw.go`）。集計結果（`Report`）は件数が増えても大きさが変わらないが、生データは件数に比例して大きくなる。

```go
f, _ := os.Create("raw.jsonl")
defer f.Close()
rep, err := jotaro.Run(ctx, jotaro.Plan{ /* ... */ Raw: f }) // 実行中に書き出す
```

```
go run ./examples/checkout -raw raw.jsonl
jotaro report -raw -format html -o report.html raw.jsonl     # 生データから再集計
jotaro report -raw -format json raw.jsonl | jotaro check -t 'p95<200ms' -   # 集計済み JSON を経由して判定
jotaro check -raw -t 'p95<200ms' raw.jsonl                    # 直接判定もできる
```

ライブラリでは `record.WriteRaw(w, *record.Raw)` / `record.RawWriter`（逐次書き出し）/ `record.ReadRaw(r)` / `report.FromRaw(*record.Raw)`。書き出した生データから再集計した結果は、件数・エラー・時系列が `Run` が返した `Report` と一致する。違うのは `P50`〜`P99` だけで、`Run` は逐次集計、`FromRaw` は正確に計算するため、差は 1% 以内。`report` / `merge` / `check` / `compare` の `-raw` で読める。`export`（後述）は常に生データを読む。生データでないもの（集計済みの結果）を `-raw` や `export` に渡すとエラーになる。

- `meta` 行（`type` が `meta`）は実行全体の情報を持つ: `version`（`RawVersion`、現在 `1`）/ `open` / `dropped` / `started_at` / `elapsed_ns`。`record.WriteRaw` は全部を先頭の 1 行に書く。`Run` は最初に 1 行（`open`、`started_at`）を書き、最後に最終的な `dropped` と `elapsed_ns` を持つ 2 行目を書く（後述）。`request` / `step` / `iteration` / `health` の行はその間と後に続く。
- 時間は集計の JSON と同じく**ナノ秒の整数**（`_ns`）。時刻は UTC の RFC 3339。0・空のフィールドは省く（`code`、`err`、`dns_ns`、`scheduled` など。読むときは 0・空に戻る）。
- `request` は `Record` と同じ内容: `scenario` / `step` / `user_id` / `iteration` / `method` / `url` / `code` / `err` / `scheduled` / `start` / `latency_ns` / `conn` / `dns_ns` / `connect_ns` / `tls_ns` / `ttfb_ns` / `bytes_out` / `bytes_in`。`step` は `StepRecord`、`iteration` は `IterationRecord`、`health` は `HealthPoint` と同じ。
- `ReadRaw` は知らない `type` の行を読み飛ばす（新しい版が行を足しても、古い読み手が壊れない）。`meta` が無くても読める（実行時間は記録から求める）。
- 集計済みの JSON（`ReadJSON`）に生データを渡すと、先頭の `meta` 行が `version: 1` の JSON として読めてしまう。`ReadJSON` は JSON の後ろに別のデータが続くとエラーにするので、`-raw` の付け忘れは「生データなら `-raw` を使う」というエラーになる。逆に、集計済みの結果を `-raw` や `export` に渡した場合もエラーになる。
- `Run` は生データを実行中に少しずつ書く（`record.RawWriter`）。記録は届くたびに書くので、リクエスト数が増えてもメモリは一定。そのため行は到着順（`request` / `step` / `iteration` が混ざる）で、最後に `health` が続く。先頭に `meta` 行（`open`、`started_at`）を書き、最後に最終的な `dropped` と `elapsed_ns` を持つ 2 つ目の `meta` 行を書く。`ReadRaw` は同じ項目では最後の値を採る。バッファは記録を書いてから最大 1 秒以内にフラッシュするので、ファイルを `tail -f` などで追えるし、途中で落ちても失うのは最大で 1 秒分ほど。途中で落ちたときも読めるファイルが残る。最後の行が途中で切れていても `ReadRaw` はそれより前の記録を返し、`Raw.Truncated` を立てる（実行時間は記録から求める）。このとき `jotaro report` / `merge` / `check` / `compare` の `-raw` と `export` は stderr に警告を出す。末尾以外の壊れた行は従来どおりエラー。
- 実行中に書くので、中断・キャンセルされた実行でも、そこまでに書いた分が残る。
- 書き出しに失敗しても、`Run` は集計した `Report` を返す（`error` も同時に返す）。

## 2. 出力形式をどう選んだか（実装済み）

> この章は、出力形式を作る前に書いた検討の経緯を残したもの。ここに挙げたものは、その後すべて実装した。JSON のスキーマは 1 章の実装を正とする。

### 出力形式の候補

| 形式 | 内容 | メリット | デメリット |
|---|---|---|---|
| JSON（集計。**実装済み**） | `Report` をそのまま出力 | 実装が小さい。CI で閾値判定しやすい | 生データは残らない |
| JSON Lines（生データ。**実装済み**） | `Record` などを1行ずつ出力 | 後から再集計・可視化できる | ファイルが大きくなる |
| vegeta 互換（**実装済み**: `jotaro export -format vegeta`） | vegeta の `Result` 形式 | 既存の `vegeta report` / `plot` が使える | シナリオ・ステップの情報が落ちる |

**進めた順**: まず JSON（集計）。次に生データ（JSON Lines）。vegeta 互換は需要が出てから。vegeta 形式は `Report` の出力形式ではなく、生データから `jotaro export`（4 章）で作る。`Report` の土台であるシナリオとステップの情報を、この形式が持てないため。

### JSON 化で決めたこと

| 論点 | 決めたこと |
|---|---|
| `time.Duration` の表現 | **ナノ秒の整数で保存する**。フィールド名は `_ns` で終える。Go の標準と一致し、無損失。表示側（コンソール・HTML）でミリ秒などに変換する |
| `map[ErrKind]int` | キーが文字列なのでそのまま JSON オブジェクトにできる |
| 出力先 | CLI のフラグ（例: `-o result.json`）とライブラリの `Report.WriteJSON(w io.Writer)` |
| スキーマの版 | `version` を持たせ、将来の変更に備える。`1` から始まり、今は `3`（1 章を参照） |

### 出力イメージ（最初の案。version 1）

最初に書いたスケッチを、そのまま残している。実際の出力にはもっと多くのフィールドがある（`started_at`、`buckets`、`series`、`health` など）。1 章を見てほしい。

時間はすべてナノ秒の整数（`_ns`）。フィールド名は仮だった。

```json
{
  "version": 1,
  "open": false,
  "dropped": 0,
  "elapsed_ns": 60123400000,
  "requests": {
    "count": 12000,
    "latency": { "count": 12000, "min_ns": 3100000, "mean_ns": 18200000, "p50_ns": 15000000, "p90_ns": 30200000, "p95_ns": 41700000, "p99_ns": 88000000, "max_ns": 240500000 },
    "status": { "2xx": 11990, "5xx": 10 },
    "errors": { "timeout": 3 },
    "bytes_in": 1048576,
    "bytes_out": 204800
  },
  "scenarios": [
    {
      "name": "checkout",
      "iterations": { "count": 4000, "failed": 12 },
      "steps": [ { "name": "login", "count": 4000, "failed": 2 } ]
    }
  ]
}
```

## 3. 結果の閲覧手段（実装済み）

方針は「機械可読な結果ファイルを土台にして、見る手段を後から足す」。専用 Web UI を除いて、下の手段はすべて実装済み。

```
Run ─▶ 結果ファイル（JSON / JSON Lines）─▶ 静的 HTML レポート
                                        ├▶ jq / スクリプト / CI
                                        └▶ Grafana 等（jotaro export 経由）
Run ─(Plan.MetricsAddr)─▶ /metrics ─▶ Prometheus ─▶ Grafana
Run ─(Plan.Progress)─▶ コンソールに 1 秒ごとの 1 行
```

| 手段 | 手間 | 見られるもの | 依存 | 状態 |
|---|---|---|---|---|
| JSON + `jq` / CI | 最小 | 最終値、閾値判定 | なし | 実装済み |
| 静的 HTML レポート | 小 | 分布、ステップ別、エラー内訳、毎秒のグラフ | なし（1ファイルで完結） | 実装済み |
| JSON Lines + 外部ツール | 小 | 生データの再集計、時系列 | 外部ツール | 実装済み（`jotaro export`） |
| 進捗の 1 行 | 最小 | 実行中のスループット・p95・エラー率 | なし | 実装済み（`Plan.Progress`） |
| Prometheus + Grafana | 中 | 実行中の推移 | 基盤が必要 | 実装済み（`Plan.MetricsAddr`） |
| 専用 Web UI（サーバー） | 大 | 複数回の比較、履歴 | 重い | 未実装 |

### 実装の順序（すべて完了）

1. **JSON 出力**（集計結果）: すべての土台。`version` を持たせ、他ツールが形式の変更で壊れないようにする。
2. **JSON Lines 出力**（生データ）: 他ツールでの再集計・可視化用。
3. **静的 HTML レポート**: `jotaro report result.json -o report.html` のように JSON から1ファイルを生成する。サーバー不要で、CI の成果物にも添付できる。
4. **`/metrics`**（Prometheus）: 実行中の推移を見るため。

### 進捗の 1 行（`Plan.Progress`）（実装済み）

`Plan.Progress` に `io.Writer`（たとえば `os.Stderr`）を渡すと、`Run` は実行中、1 秒ごとに次のような 1 行を書き出す。

```
12s/60s rps=480 p95=38ms err=0.2%
```

内容は、経過時間 / `Duration`、直近 1 秒のスループット、ここまでの実行全体の p95 とエラー率（`error_rate` と同じく、通信レベルのエラーで終わったリクエストの割合）。p95 は相対誤差 1% 以内の近似値。`Plan.Raw` の有無にかかわらず動き、記録のコストは増えず、書き込みのエラーは無視し、`Run` が返ったあとは何も書かない。`AbortOn` が実行を止めたときは、`aborted: ...` の行が続く（5 章の「実行を途中で止める」を参照）。

### `/metrics`（`Plan.MetricsAddr`）（実装済み）

`Plan.MetricsAddr` を指定する（たとえば `":9090"`）と、`Run` はここまでの実行の状態を、Prometheus のテキスト形式（バージョン 0.0.4）で `http://<MetricsAddr>/metrics` に公開する。実行中に Prometheus や Grafana でグラフにできる。サーバーは `Run` が返ると止まる。そのアドレスで待ち受けられないときは、`Run` はリクエストを 1 つも送る前に失敗する。

```go
rep, err := jotaro.Run(ctx, jotaro.Plan{ /* ... */ MetricsAddr: ":9090" })
```

| メトリクス | 型 | 意味 |
|---|---|---|
| `jotaro_requests_total` | counter | ここまでに終わったリクエスト数 |
| `jotaro_dropped_total` | counter | `MaxUsers` の上限のため実行しなかった到着の数 |
| `jotaro_bytes_received_total` / `jotaro_bytes_sent_total` | counter | 受信 / 送信したボディのバイト数 |
| `jotaro_responses_total{class="2xx"}` | counter | ステータスクラスごとの応答数 |
| `jotaro_errors_total{kind="timeout"}` | counter | `ErrKind` ごとの通信レベルのエラー数 |
| `jotaro_elapsed_seconds` | gauge | 実行開始からの秒数 |
| `jotaro_requests_per_second` | gauge | ここまでの平均スループット |
| `jotaro_request_duration_seconds{quantile="0.95"}`（0.5 / 0.9 / 0.95 / 0.99）、`_sum`、`_count` | summary | ここまでのリクエストのレイテンシ（相対誤差 1% 以内） |

作るときに決めたこと:

- テキスト形式は自前で書く。`client_golang` のような依存は増やさない。
- ラベルはステータスクラスとエラーの種類だけにする。`scenario` / `step` は出さず、`user_id` や URL は系列数が爆発するので今後も出さない。
- レイテンシは固定バケットのヒストグラムではなく、レポートと同じ逐次集計から求めた分位点の summary にする。バケット境界のせいでレポートとずれることがない。値は直近の窓ではなく、ここまでの実行全体のもの。
- ここで `Sink` と呼んでいたインターフェースは `record.Observer` として存在する。`Run` は記録をこれ経由で集計器・生データの書き出し・進捗カウンタに渡すので、記録の側を変えずに出力先を足せる。

### 補足

- 静的 HTML は JS ライブラリを埋め込んでいない。表と棒、インラインの簡単な SVG 折れ線グラフで足りた。
- 1 秒ごとの時系列（`series`、1 章）は実装済みなので、タイムラインは HTML レポートと、`jotaro export -format csv`（4 章）の CSV にある。

## 4. 出力 API（実装済み）

`Report.Write(w io.Writer, f Format) error` が出力の入口（`report/write.go`）。出力先は `io.Writer` なので、ファイル、標準出力、`bytes.Buffer`、`http.ResponseWriter` のどれでもよい。

```
*Report ─▶ Write(w, format) ─▶ Text / JSON / HTML ─▶ w
```

```go
f, _ := report.ParseFormat("json") // CLI のフラグ文字列から
if err := rep.Write(os.Stdout, f); err != nil {
    log.Fatal(err)
}
```

| `Format` | 内容 | 実体 |
|---|---|---|
| `FormatText` | 人が読める表 | `Print`（書き込みの失敗は `Write` が `error` で返す） |
| `FormatJSON` | 時間はナノ秒の整数 | `WriteJSON` |
| `FormatHTML` | 外部に依存しない 1 枚の HTML | `WriteHTML` |

- 未知の形式は、何も書かずに `error` を返す。`ParseFormat` は文字列から `Format` を作り、未知なら `error` を返す。
- 書き込みの途中で失敗した場合は `error` を返す。途中まで書かれた内容は `w` に残るので、ファイルに書くときは呼び出し側が扱う。
- `Print` / `WriteJSON` / `WriteHTML` も公開したまま残す。形式が決まっているなら直接呼べる。
- サンプル: `go run ./examples/checkout -format json -o result.json`（`-o` を省くと標準出力）。

### `ReadJSON` と `report` サブコマンド（実装済み）

保存した JSON から、あとでレポートを作り直せる。

```go
rep, err := report.ReadJSON(f) // WriteJSON が書いた JSON を Report に戻す
```

- `version` が対応していないとき（1〜`JSONVersion` の外。つまり 1〜3 は読める）は `error` を返す。
- 往復しても `Report` は同じになる（空の map / スライスは `nil` に戻す。テストで `reflect.DeepEqual` を確認）。
- 時間はナノ秒の整数なので、読み戻しで丸めは起きない。

```bash
go run ./examples/checkout -format json -o result.json   # 実行して JSON を保存
go run ./cmd/jotaro report -o report.html result.json    # JSON から HTML を生成
go run ./cmd/jotaro report -format text result.json      # テキストで見る
cat result.json | go run ./cmd/jotaro report -           # 標準入力から読む
```

| フラグ | 既定 | 内容 |
|---|---|---|
| `-format` | `html` | `text` / `json` / `html` |
| `-o` | 標準出力 | 出力先のファイル。書き込みに失敗したら、途中のファイルを消す |
| `-raw` | オフ | 入力を生データ（JSON Lines。`Plan.Raw` の出力）として読み、集計し直す。`merge`・`check`・`compare` にもある |

終了コードは、成功 `0`、使い方の誤り（引数なし・未知の形式など）`2`、読み書きの失敗・未対応の `version` が `1`。実装は `cmd/jotaro/main.go`。

### 2 つの結果を比べる: `compare`（実装済み）

```
jotaro compare [-raw] [-max-p95-regression <パーセント>] <base.json> <new.json>
```

基準の結果から新しい結果へ何が変わったかを表にして出す。全体の `rps` と、全体および両方にあるステップ（シナリオ名とステップ名で突き合わせ）ごとの `p50` / `p95` / `p99` / `error_rate`。どちらかにデータが無い指標は出さない。CHANGE 列は相対的な変化で、`worse` / `better` が向きを示す（`rps` は高いほうが良い）。`error_rate` は 5 章と同じで、通信レベルのエラーだけ。

`-max-p95-regression <パーセント>` を付けると、どれかの p95（全体またはステップ）がその割合より悪化したとき終了コード 1 になり、該当ごとに `FAIL ...` の行を出す。合否を決めるのは p95 だけで、ほかの行は読むためのもの。差が統計的に有意かどうかの検定はしない。`-raw` を付けると、両方の入力を生データとして読む。ライブラリでは `report.Compare(base, cur)` が `Comparison` を返す（`Print`、`Violations(maxPct)`）。終了コードは、`0`、`1`（上限を超える悪化、または入力が読めない）、`2`（使い方の誤り）。

### 生データを変換する: `export`（実装済み）

```
jotaro export -format vegeta|csv [-o 出力先] <raw.jsonl | ->
```

生データ（`Plan.Raw` の出力。集計済みの結果は読まない）を読み、他のツール向けに変換する。

- `vegeta`: リクエストごとに 1 つの JSON オブジェクトを、開始時刻の順に、vegeta の結果の形式で出す。`vegeta report -type=json` や `vegeta plot` で読める。この形式はシナリオやステップの詳細を持てないので、attack 名は `<シナリオ>/<ステップ>` にする。通信レベルのエラーは code 0 で、メッセージにエラーの種類が入る。HTTP 4xx/5xx はステータスコードのまま。ライブラリでは `report.WriteVegeta(w, raw)`。
- `csv`: 1 秒ごとの時系列を、ヘッダー行つきで出す。`second`、`requests`、`errors`、`http_5xx`、`new_conns`、`reused_conns`、`concurrency`、`latency_min_ms` / `mean` / `p50` / `p90` / `p95` / `p99` / `max`（小数つきのミリ秒なので、表計算でそのまま使える）。ライブラリでは `Report.WriteSeriesCSV(w)`。

`-o` は出力先のファイル（省くと標準出力。書き込みに失敗したら途中のファイルを消す）。終了コードは、`0`、`1`（読み書きの失敗）、`2`（使い方の誤り。`-format` が無い・未知など）。

### 形式が増える場合（将来）

利用者が独自の形式を足したくなったら、`Format` の `switch` の代わりにインターフェースへ移る。需要が出るまでは今の形のままにする。

```go
type Writer interface {
    Write(w io.Writer, r *Report) error
}
```

### 注意点

- 生データ（JSON Lines）は件数が多いので、`Report.Write` には含めず、別の API（`record.WriteRaw`、`Plan.Raw`）にした。

## 5. しきい値判定（実装済み）

結果が条件を満たすかを確かめ、CI で「遅くなったら失敗」にする（`report/threshold.go`）。k6 の `thresholds` に近い。実行後の `Report` に対して判定する。`Plan` 自身にはしきい値のフィールドは無い。実行中に止めたいときは `Plan.AbortOn` を使う（この章の最後）。

```go
th, _ := report.ParseThreshold("p95<200ms")
checks := rep.Check(th)  // Checks
checks.Print(os.Stdout)
if !checks.Passed() { os.Exit(1) }
```

```
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'p99{step=login}<100ms' result.json
```

満たさないものが 1 つでもあれば終了コード 1。しきい値の書き方が誤り、またはファイルが読めないときは 2 / 1。

### 書き方

`指標[{scenario=名前,step=名前}] 演算子 値`。演算子は `<` `<=` `>` `>=`。

| 指標 | 意味 | 値 | 絞り込み |
|---|---|---|---|
| `min` `mean` `p50` `p90` `p95` `p99` `max` | リクエストのレイテンシ | 時間（`200ms`、`1.5s`） | `{step=...}` |
| 上の頭に `corrected_` `ttfb_` `dns_` `connect_` `tls_` | CO 補正後 / TTFB / DNS / TCP / TLS（例: `corrected_p99`） | 時間 | `{step=...}` |
| `error_rate` | 通信レベルのエラーで終わったリクエストの割合（リクエスト数に対して）。`Expect*` の失敗は含まない | `1%` または `0.01` | `{step=...}` |
| `http_4xx_rate` `http_5xx_rate` | HTTP 4xx / 5xx の割合 | 割合 | `{step=...}` |
| `iteration_fail_rate` | 失敗したイテレーションの割合 | 割合 | `{scenario=...}` |
| `iteration_` + `min` `mean` `p50` `p90` `p95` `p99` `max` | シナリオ 1 周ぶんのレイテンシ（Think を含む。例: `iteration_p95`） | 時間 | `{scenario=...}` |
| `iteration_active_` + 同じ統計値 | 同上（Think を含まない） | 時間 | `{scenario=...}` |
| `requests` | リクエスト数 | 数 | `{step=...}` |
| `rps` `dropped` | 1 秒あたりのリクエスト数 / ドロップされた到着の数 | 数 | 全体のみ |

#### 特殊な文字を含む名前

`{...}` の中の引用符なしの名前は、次のカンマまで（前後の空白は取り除く）で、波括弧は釣り合っていなければならない。カンマ、釣り合わない `{` `}`、波括弧の外の `<` `>`、前後の空白を含むステップ名・シナリオ名は、Go の二重引用符付き文字列で書く。

```
p95{step="list, then order"}<1s
p95{scenario="checkout, mobile",step=" padded "}<1s
p95{step="say \"hi\""}<1s
```

引用符の中はすべてそのままの文字で、`\"` と `\\` はエスケープ（`\n` や `\u00e9` など Go のほかのエスケープも使える）。引用符なしの名前はこれまでどおりに読むので既存のしきい値は変わらない。ただし 1 つだけ例外がある。`step="login"` のように完全な引用符付き文字列になっている値は、引用符の文字を含む名前ではなく、引用符を外した名前（`login`）を指すようになった。名前に引用符そのものを含むステップを選ぶには `step="\"login\""` と書く。文字列として閉じていない引用符（`step="abc`、`step="a"b`）や、不正なエスケープを含むもの（`step="C:\users"`）は、引用符も含めて普通の文字として扱う。`\t` や `\n` などはエスケープなので、引用符の中でバックスラッシュを書くときは `\\` にする。空の名前（`step=""`）はエラー。`Threshold.String()`、`jotaro check` の出力、`AbortOn` のメッセージ、MCP の `canonical` は、必要なときに名前を引用符で囲むので、そのまま貼り直せる。

### 決めたこと

- `{step=...}` が複数のシナリオにある同名のステップに当たるときは、それぞれを別の結果として確かめる（`scenario=` を足せば 1 つに絞れる）。
- **対象が無い・データが無いときは満たさなかった扱い**にする（ステップ名のタイポ、リクエストが 0 件、`ttfb_` の記録が無い、など）。設定の間違いや、動かなかった試験を成功にしないため。
- `error_rate` は、通信レベルのエラー（タイムアウト、接続拒否、DNS 失敗など）で終わったリクエストの割合。`Expect*` の失敗はリクエストのエラーとしては数えない。ステップが `expect_mismatch` で失敗し（ステップのエラーの一覧に出る）、シナリオがそのエラーを返すと `iteration_fail_rate` に反映される。
- HTTP 4xx/5xx も `error_rate` に含まない。`http_4xx_rate` / `http_5xx_rate` で別に見る。
- 誤ったレスポンスで不合格にしたいときは、ステップのエラーをシナリオから返し（`return u.Step(...)`、または、その後で `return err`）、`iteration_fail_rate` にしきい値を置く。たとえば `iteration_fail_rate<1%`。
- オープンモデルで、`MaxUsers` が満杯のときに来た到着は、待たされずにドロップされ、ドロップされた到着はレイテンシの標本に入らない。`corrected_*` をしきい値に使うときは `dropped<1` を併記する。そうしないと、ドロップが出た走行でも、実行できた到着のレイテンシだけで合格してしまう。

### 実行を途中で止める（`Plan.AbortOn`）（実装済み）

`Plan.AbortOn` は k6 の `abortOnFail` に当たる。条件が成り立ったらすぐに実行を止め、壊れたサーバーに `Duration` のあいだ中負荷をかけ続けないようにする（`abort.go`）。

```go
AbortOn: []jotaro.AbortRule{
	{When: "http_5xx_rate>50%", For: 5 * time.Second},
	{When: "error_rate>50%", For: 5 * time.Second},
	{When: "p95>2s", For: 10 * time.Second},
},
```

- `When` は上のしきい値と同じ書き方（`report.ParseThreshold`）だが、「合格とする状態」ではなく**止める状態**を表す。`http_5xx_rate>50%` は、半分より多くのリクエストが 5xx を受けたら止める。`When` の書き方が誤っていると、`Run` は負荷をかける前に失敗する。
- 1 秒ごとに、ここまでの実行全体に対して判定する（直近の窓ではない）。終わったレポートを `Report.Check` が読むのと同じ方法。そのためパーセンタイルは、逐次集計の近似値になる。まだデータが無いあいだは、成り立っているとは数えない。
- `For` は、条件が途切れず成り立ち続けるべき時間（0 なら、成り立った最初の判定で止める）。数秒にしておくと、統計が落ち着く前の一時的な失敗で止まらない。
- どの指標が失敗を捉えるかに注意する。`error_rate` は通信レベルのエラーだけを数えるので、HTTP のエラーステータスには `http_5xx_rate` / `http_4xx_rate`、誤ったレスポンス（`Expect*`）には `iteration_fail_rate` が要る。
- 止めるときは、新しいイテレーションを始めず、実行中のものには止めた時点から数えて `Grace` だけ待つ。`Run` は途中までのレポート（`Plan.Raw` も書き終える）と、`jotaro.ErrAborted` を包んだエラーを一緒に返す。`errors.Is(err, jotaro.ErrAborted)` で判定する。`Plan.Progress` を指定していれば、`aborted: <ルール> (at <時刻>)` の行を書き出す。

### 未対応

- 結果ファイルへの判定結果の書き込み。`report` / `merge` の `-t`。
- 時系列（1 秒ごと）に対する条件と、`AbortOn` の直近の窓での判定。

## 6. 関連

- 設計の全体像: [`DESIGN_jp.md`](../DESIGN_jp.md)（計測は 5 章、未決は 8 章）
- 実装: `record/record.go` / `report/report.go` / `report/aggregate.go` / `report/print.go` / `report/compare.go` / `report/export.go` / `abort.go` / `metrics.go` / `progress.go`
