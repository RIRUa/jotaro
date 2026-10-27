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
2. **JSON Lines 出力**（生データ）: 他ツールでの再集計・可視化用。
3. **静的 HTML レポート**: `jotaro report result.json -o report.html` のように JSON から1ファイルを生成する。サーバー不要で、CI の成果物にも添付できる。
4. **`/metrics`**（Prometheus）: 実行中の推移を見たくなったら追加する。

### Prometheus 連携の注意点

- `scenario` / `step` はラベルにしてよいが、`user_id` や URL は系列数が爆発するのでラベルにしない。
- ヒストグラムは固定バケットなので、nearest-rank のパーセンタイルとは値がずれる。バケット境界を決める必要がある。
- `client_golang` を使うか、テキスト形式を自前で書くか（依存を増やさない方針との兼ね合い）は未決。
- 記録の受け口を `Sink` のようなインターフェースにすると、`Run` を変えずに出力先を足せる。

### 注意点

- 静的 HTML のグラフは、JS ライブラリを埋め込むと容量が増える。最初は表と簡単な SVG の棒グラフで足りるかもしれない。
- 時系列を見るには、`DESIGN.md` のフェーズ3（1秒ごとの集計）が先に必要。それまでは最終値の可視化が中心になる。

## 4. 関連

- 設計の全体像: [`DESIGN.md`](../DESIGN.md)（計測は 5 章、未決は 8 章）
- 実装: `record.go` / `report.go` / `report_print.go`
