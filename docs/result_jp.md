# 結果（リザルト）の仕様

[English](result.md) | 日本語

jotaro の実行結果に関する仕様メモ。**実装済みの部分**と**未実装（提案）の部分**を分けて書く。

## 1. 現状（実装済み）

結果は **メモリ上の `Report` 構造体**で、コンソール表示・JSON・HTML に書き出せる（4 章）。生データは JSON Lines に書き出せる（下の「生データ」）。

```
Run() ─▶ Collector ─▶ report.BuildReport() ─▶ *report.Report ─▶ print.go ─▶ コンソール
        (生データ)      (集計)          (メモリ)
```

| 層 | 型 | ファイル | 役割 |
|---|---|---|---|
| 生データ | `record.Record` / `StepRecord` / `IterationRecord` | `record/record.go` | リクエスト・ステップ・シナリオ1回ごとの記録 |
| 収集 | `record.Collector` | `record/record.go` | 並行に記録を受け取る（`Snapshot()` でコピー取得） |
| 集計 | `report.Report` | `report/report.go` | 3階層（リクエスト / ステップ / シナリオ）の集計結果 |
| 出力 | — | `report/print.go` | コンソール表示 |

### `Report` の構造

```
Report
├─ Open        オープンモデル（Rate）で実行したか
├─ Dropped     MaxUsers 超過で実行しなかった到着の数
├─ Elapsed     実行時間（Duration より少し長くなる）
├─ Requests    RequestStats（全リクエスト合計）
└─ Scenarios[] ScenarioReport
   ├─ Name
   ├─ Iterations  IterationStats（Active = Think 除く / Total = Think 含む）
   └─ Steps[]     StepStats（最初に実行された順）
      └─ Requests RequestStats
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
| `Corrected` | 予定開始時刻からの時間（Coordinated Omission 補正後）。`Users` モデルでは `Latency` と同じ |
| `Status` | `"2xx"` などステータスクラスごとの件数 |
| `Errors` | 通信エラーの内訳（`ErrKind` ごと） |
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
| `expect_mismatch` | `ExpectStatus` などの期待値の不一致 |
| `other` | 上記以外 |

HTTP 4xx/5xx は `ErrKind` ではなくステータス（`Status`）として記録される。

### 補足

- `RPS()` は `Requests.Count / Elapsed`。
- `Plan.Raw` を指定しない `Run` は逐次集計する（`report.Aggregator`）。記録は届くたびにカウンタと対数ヒストグラムへ畳み込み、保持しないので、メモリはリクエスト数に比例して増えない。件数・最小・最大・平均は正確で、`P50`〜`P99` は相対誤差 1% 以内の近似値（`[最小, 最大]` に収める）。
- `Plan.Raw` を指定した `Run`、`FromRaw`、`BuildReport` は全記録を持ち（受け取り）ソートするので、`P50`〜`P99` は nearest-rank の正確な値になる。メモリよりパーセンタイルの正確さを優先したいときはこちらを使う。

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
  - `2`: `started_at`（実行の開始時刻、RFC 3339）と、各 `latency` の `buckets`（ヒストグラム）を追加
  - `1`: 初版
- 内部の `Report` とは別の出力用の型を持つので、`Report` を変えても JSON の形は変わらない。
- 空の集合は `null` ではなく `[]` / `{}` で出す（読む側が分岐しなくて済む）。
- 追加で `rps` を出す（`Requests.Count / Elapsed`）。リクエストの集計は `latency` / `ttfb` / `corrected` の 3 つを持つ。

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
- CSS は埋め込み、JS と外部リソースは使わない。ライト／ダークは OS の設定に従う。
- 内容: サマリー、ステータス・エラーの内訳、レイテンシ（p50〜max の棒）、`corrected`（`Rate` のときのみ）、TTFB、シナリオ別のイテレーション時間とステップ別の表。
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

- **ヒストグラム**: 各 `latency` に対数バケット（DDSketch 方式、相対誤差 1%）を持たせた（JSON の `buckets`: バケット番号 → 件数）。バケットは足し算で結合できるので、何台分でも順序に関係なく同じ結果になる。全記録から作ったレポート（`BuildReport`、`FromRaw`、`Plan.Raw` つきの `Run`）の `P50`〜`P99` は nearest-rank の正確な値。逐次集計したもの（`Plan.Raw` なしの `Run`）と、結合したものは近似になる。
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
rep, err := jotaro.Run(ctx, jotaro.Plan{ /* ... */ Raw: f }) // 終了時に書き出す
```

```
go run ./examples/checkout -raw raw.jsonl
jotaro report -raw -format html -o report.html raw.jsonl     # 生データから再集計
jotaro report -raw -format json raw.jsonl | jotaro check -t 'p95<200ms' -   # 集計済み JSON を経由して判定
jotaro check -raw -t 'p95<200ms' raw.jsonl                    # 直接判定もできる
```

ライブラリでは `record.WriteRaw(w, *record.Raw)` / `record.ReadRaw(r)` / `report.FromRaw(*record.Raw)`。`Run` の結果も `report.FromRaw` を通すので、書き出した生データから再集計した結果は `Run` が返した `Report` と一致する。`report` / `merge` / `check` の `-raw` で読める。

- 先頭の行は実行全体の情報（`type` が `meta`）。`version`（`RawVersion`、現在 `1`）/ `open` / `dropped` / `started_at` / `elapsed_ns`。そのあとに `request` / `step` / `iteration` / `health` の行が続く。
- 時間は集計の JSON と同じく**ナノ秒の整数**（`_ns`）。時刻は UTC の RFC 3339。0・空のフィールドは省く（`code`、`err`、`dns_ns`、`scheduled` など。読むときは 0・空に戻る）。
- `request` は `Record` と同じ内容: `scenario` / `step` / `user_id` / `iteration` / `method` / `url` / `code` / `err` / `scheduled` / `start` / `latency_ns` / `conn` / `dns_ns` / `connect_ns` / `tls_ns` / `ttfb_ns` / `bytes_out` / `bytes_in`。`step` は `StepRecord`、`iteration` は `IterationRecord`、`health` は `HealthPoint` と同じ。
- `ReadRaw` は知らない `type` の行を読み飛ばす（新しい版が行を足しても、古い読み手が壊れない）。`meta` が無くても読める（実行時間は記録から求める）。
- 集計済みの JSON（`ReadJSON`）に生データを渡すと、先頭の `meta` 行が `version: 1` の JSON として読めてしまう。`ReadJSON` は JSON の後ろに別のデータが続くとエラーにするので、`-raw` の付け忘れは「生データなら `-raw` を使う」というエラーになる。
- 生データは `Run` が終わってからまとめて書く（記録はもともとメモリに溜めているため。溜めるのは `Plan.Raw` を指定したときだけ）。実行中に少しずつ書く（メモリを使わない）形にはなっていない。
- 書き出しに失敗しても、`Run` は集計した `Report` を返す（`error` も同時に返す）。

## 2. 未実装（提案）

> 以下は提案であり、**まだ決まっていない**。JSON 出力の項目は 1 章の実装を正とする。`DESIGN_jp.md` の「未決」にある vegeta 互換の程度の判断が前提になる。

### 出力形式の候補

| 形式 | 内容 | メリット | デメリット |
|---|---|---|---|
| JSON（集計） | `Report` をそのまま出力 | 実装が小さい。CI で閾値判定しやすい | 生データは残らない |
| JSON Lines（生データ。**実装済み**） | `Record` などを1行ずつ出力 | 後から再集計・可視化できる | ファイルが大きくなる |
| vegeta 互換 | vegeta の `Result` 形式 | 既存の `vegeta report` / `plot` が使える | シナリオ・ステップの情報が落ちる |

**推奨**: まず JSON（集計）。次に生データ（JSON Lines）を追加する（どちらも実装済み）。vegeta 互換は需要が出てから。

### JSON 化の際に決めること

| 論点 | 案 |
|---|---|
| `time.Duration` の表現 | **ナノ秒の整数で保存する（決定）**。フィールド名は `_ns` で終える。Go の標準と一致し、無損失。表示側（コンソール・HTML）でミリ秒などに変換する |
| `map[ErrKind]int` | キーが文字列なのでそのまま JSON オブジェクトにできる |
| 出力先 | CLI のフラグ（例: `-o result.json`）とライブラリの `Report.WriteJSON(w io.Writer)` |
| スキーマの版 | `"version": 1` を持たせ、将来の変更に備える |

### 出力イメージ（案）

時間はすべてナノ秒の整数（`_ns`）。フィールド名は仮。

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

## 3. 結果の閲覧手段（提案）

> 提案であり、**まだ決まっていない**。方針は「機械可読な結果ファイルを土台にして、見る手段を後から足す」。

```
Run ─▶ 結果ファイル（JSON / JSON Lines）─▶ 静的 HTML レポート
                                        ├▶ jq / スクリプト / CI
                                        └▶ Grafana 等
Run ─(任意)─▶ /metrics ─▶ Prometheus ─▶ Grafana
```

| 手段 | 手間 | 見られるもの | 依存 |
|---|---|---|---|
| JSON + `jq` / CI | 最小 | 最終値、閾値判定 | なし |
| 静的 HTML レポート | 小 | 分布、ステップ別、エラー内訳 | なし（1ファイルで完結） |
| JSON Lines + 外部ツール | 小 | 生データの再集計、時系列 | 外部ツール |
| Prometheus + Grafana | 中 | 実行中の推移 | 基盤が必要 |
| 専用 Web UI（サーバー） | 大 | 複数回の比較、履歴 | 重い |

### 実装の順序

1. **JSON 出力**（集計結果）: すべての土台。`version` を持たせ、他ツールが形式の変更で壊れないようにする。
2. **JSON Lines 出力**（生データ。実装済み）: 他ツールでの再集計・可視化用。
3. **静的 HTML レポート**: `jotaro report result.json -o report.html` のように JSON から1ファイルを生成する。サーバー不要で、CI の成果物にも添付できる。
4. **`/metrics`**（Prometheus）: 実行中の推移を見たくなったら追加する。

### Prometheus 連携の注意点

- `scenario` / `step` はラベルにしてよいが、`user_id` や URL は系列数が爆発するのでラベルにしない。
- ヒストグラムは固定バケットなので、nearest-rank のパーセンタイルとは値がずれる。バケット境界を決める必要がある。
- `client_golang` を使うか、テキスト形式を自前で書くか（依存を増やさない方針との兼ね合い）は未決。
- 記録の受け口を `Sink` のようなインターフェースにすると、`Run` を変えずに出力先を足せる。

### 注意点

- 静的 HTML のグラフは、JS ライブラリを埋め込むと容量が増える。最初は表と簡単な SVG の棒グラフで足りるかもしれない。
- 時系列を見るには、`DESIGN_jp.md` のフェーズ3（1秒ごとの集計）が先に必要。それまでは最終値の可視化が中心になる。

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

- `version` が対応していないときは `error` を返す（`JSONVersion` と一致しないもの）。
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

終了コードは、成功 `0`、使い方の誤り（引数なし・未知の形式など）`2`、読み書きの失敗・未対応の `version` が `1`。実装は `cmd/jotaro/main.go`。

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

結果が条件を満たすかを確かめ、CI で「遅くなったら失敗」にする（`report/threshold.go`）。k6 の `thresholds` に近い。実行後の `Report` に対して判定する（実行中に打ち切る `abortOnFail` 相当は無い）。

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
| `error_rate` | 通信エラーと Expect 不一致の割合（リクエスト数に対して） | `1%` または `0.01` | `{step=...}` |
| `http_4xx_rate` `http_5xx_rate` | HTTP 4xx / 5xx の割合 | 割合 | `{step=...}` |
| `iteration_fail_rate` | 失敗したイテレーションの割合 | 割合 | `{scenario=...}` |
| `requests` | リクエスト数 | 数 | `{step=...}` |
| `rps` `dropped` | 1 秒あたりのリクエスト数 / ドロップされた到着の数 | 数 | 全体のみ |

### 決めたこと

- `{step=...}` が複数のシナリオにある同名のステップに当たるときは、それぞれを別の結果として確かめる（`scenario=` を足せば 1 つに絞れる）。
- **対象が無い・データが無いときは満たさなかった扱い**にする（ステップ名のタイポ、リクエストが 0 件、`ttfb_` の記録が無い、など）。設定の間違いや、動かなかった試験を成功にしないため。
- `error_rate` は HTTP 4xx/5xx を含まない（`ExpectStatus` を付けたステップの不一致は `ErrExpect` として含まれる）。4xx/5xx は `http_4xx_rate` / `http_5xx_rate` で別に見る。
- シナリオ単位のレイテンシは無い（シナリオ全体のリクエスト分布は持っていないため）。ステップ単位か全体で見る。

### 未対応

- 実行中に判定して早く止める（`abortOnFail`）。
- 結果ファイルへの判定結果の書き込み。`report` / `merge` の `-t`。
- 時系列（1 秒ごと）に対する条件。

## 6. 関連

- 設計の全体像: [`DESIGN_jp.md`](../DESIGN_jp.md)（計測は 5 章、未決は 8 章）
- 実装: `record/record.go` / `report/report.go` / `report/print.go`
