# 結果（リザルト）の仕様

jotaro の実行結果に関する仕様メモ。**実装済みの部分**と**未実装（提案）の部分**を分けて書く。

## 1. 現状（実装済み）

結果は **メモリ上の `Report` 構造体**と**コンソール出力**のみ。ファイルへの書き出しは未実装。

```
Run() ─▶ Collector ─▶ BuildReport() ─▶ *Report ─▶ report_print.go ─▶ コンソール
        (生データ)      (集計)          (メモリ)
```

| 層 | 型 | ファイル | 役割 |
|---|---|---|---|
| 生データ | `Record` / `StepRecord` / `IterationRecord` | `record.go` | リクエスト・ステップ・シナリオ1回ごとの記録 |
| 収集 | `Collector` | `record.go` | 並行に記録を受け取る（`Snapshot()` でコピー取得） |
| 集計 | `Report` | `report.go` | 3階層（リクエスト / ステップ / シナリオ）の集計結果 |
| 出力 | — | `report_print.go` | コンソール表示 |

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
- 集計は全記録をソートして求める。長時間・高負荷でメモリが問題になったら、ヒストグラムに置き換える予定。

## 2. 未実装（提案）

> 以下は提案であり、**まだ決まっていない**。`DESIGN.md` の「未決」にある vegeta 互換の程度の判断が前提になる。

### 出力形式の候補

| 形式 | 内容 | メリット | デメリット |
|---|---|---|---|
| JSON（集計） | `Report` をそのまま出力 | 実装が小さい。CI で閾値判定しやすい | 生データは残らない |
| JSON Lines / CSV（生データ） | `Record` を1行ずつ出力 | 後から再集計・可視化できる | ファイルが大きくなる |
| vegeta 互換 | vegeta の `Result` 形式 | 既存の `vegeta report` / `plot` が使える | シナリオ・ステップの情報が落ちる |

**推奨**: まず JSON（集計）。次に生データ（JSON Lines）を追加する。vegeta 互換は需要が出てから。

### JSON 化の際に決めること

| 論点 | 案 |
|---|---|
| `time.Duration` の表現 | ナノ秒の整数（Go 標準）か、ミリ秒の小数か |
| `map[ErrKind]int` | キーが文字列なのでそのまま JSON オブジェクトにできる |
| 出力先 | CLI のフラグ（例: `-o result.json`）とライブラリの `Report.WriteJSON(w io.Writer)` |
| スキーマの版 | `"version": 1` を持たせ、将来の変更に備える |

### 出力イメージ（案）

```json
{
  "version": 1,
  "open": false,
  "dropped": 0,
  "elapsed_ms": 60123.4,
  "requests": {
    "count": 12000,
    "latency": { "count": 12000, "min_ms": 3.1, "mean_ms": 18.2, "p50_ms": 15.0, "p90_ms": 30.2, "p95_ms": 41.7, "p99_ms": 88.0, "max_ms": 240.5 },
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

## 3. 関連

- 設計の全体像: [`DESIGN.md`](../DESIGN.md)（計測は 5 章、未決は 8 章）
- 実装: `record.go` / `report.go` / `report_print.go`
