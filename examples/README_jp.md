# サンプル

[English](README.md) | [日本語](README_jp.md)

そのまま動かせる jotaro のサンプルです。どれも自前のモックサーバーを立てるので、ほかに準備するものはありません。リポジトリのルートで実行してください。

| サンプル | 内容 |
|---|---|
| [`checkout`](checkout) | 基本: 短いフロー（ログイン → 商品一覧 → 注文）、クローズド／オープンの負荷モデル、ステージ、HTML と生データの出力 |
| [`mix`](mix) | 重み付きのシナリオ構成（browse 70%・buy 30%）、人数の波、壊れた実行の中断、進捗とメトリクスの公開、シナリオごとのしきい値。小さな負荷試験の構成の見本 |
| [`marketplace`](marketplace) | 深く現実的なシナリオ: 1 つのシナリオで複数種類の訪問者を演じ、状態を持つセッション、再試行、ポーリング、実行を失敗させるしきい値を扱う |

自分のフローを書くときにどちらを見るか: 訪問者が独立した流れ（閲覧する／買う）をたどるなら、流れごとにシナリオを 1 つ書き、`mix` のように `Plan.Scenarios` で混ぜます。1 人の訪問者が途中のステップの結果で分岐するなら、`marketplace` のように 1 つのシナリオの中で分岐します。

## 実行 → 判定 → 読む

`checkout` を使って、`jotaro` コマンドで一周する手順です。実行して結果を保存し、しきい値で判定し、読みます。リポジトリのルートでは `go run ./cmd/jotaro` がそのままコマンドになります。どこからでも使いたいときは、一度 `go install github.com/RIRUa/jotaro/cmd/jotaro@latest` で入れて、`jotaro` と書きます。

```sh
go run ./examples/checkout -format json -o result.json                             # 実行して結果を保存
go run ./cmd/jotaro check -t 'p95<200ms' -t 'error_rate<1%' result.json           # しきい値で判定。満たさなければ終了ステータス 1
go run ./cmd/jotaro report -format text result.json                                # 読む（既定は HTML。-o result.html を付ける）
```

2 回の実行を比べたり、ほかのツールにデータを渡したりするときは、生データも保存（`-raw raw.jsonl`）して、`jotaro compare -max-p95-regression 20 base.json new.json`（p95 が 20% より悪化したら終了ステータス 1）や `jotaro export -format csv -o series.csv raw.jsonl`（`-format vegeta` もあります）を使います。

## checkout

自前のモックサーバーを立てる小さなサンプルです。

```sh
go run ./examples/checkout                                    # 20 ユーザー固定
go run ./examples/checkout -rate 50                           # 毎秒 50 シナリオ
go run ./examples/checkout -rate 100 -duration 12s -stages    # 上げる → 保つ → 下げる
go run ./examples/checkout -format html -o result.html        # HTML レポート
go run ./examples/checkout -raw raw.jsonl                     # 生データ（JSON Lines）も書き出す
```

| フラグ | 意味 |
|---|---|
| `-rate N` | 毎秒開始するシナリオ数（オープンモデル）。`0`（既定）なら固定ユーザーのクローズドモデル |
| `-ramp D` | `-rate` に達するまでの時間。`0`（既定）なら最初から一定 |
| `-stages` | `-rate` `-ramp` `-duration` から 3 ステージ（上げる → 保つ → 下げる）を組む |
| `-duration D` | 実行時間（既定 `3s`） |
| `-format F` | 結果の形式: `text`（既定）・`json`・`html` |
| `-o FILE` | 結果の書き出し先。空なら標準出力 |
| `-raw FILE` | 生データ（JSON Lines）も書き出す。空なら書かない |

## marketplace

もう少し深いサンプルです。状態を持つモックショップに対し、1 つのシナリオが 5 種類の訪問者（閲覧のみ・会員・新規登録・購入後キャンセル・カート放棄）を演じます。次のものを扱います。

- CSRF トークン付きで失効するセッションと自動再ログイン
- ページネーション
- 条件付き GET
- レート制限された決済確定への `Idempotency-Key` 付きバックオフ再試行
- 非同期決済のポーリング
- 実行を失敗させるしきい値

```sh
go run ./examples/marketplace                         # 20 秒: 上げる → 保つ → スパイク → 保つ → 下げる
go run ./examples/marketplace -rate 50                # しきい値を割る負荷（終了ステータス 1）
go run ./examples/marketplace -users 10 -pace 0.2     # クローズドモデル、思考時間を 20% に
```

| フラグ | 意味 |
|---|---|
| `-rate N` | 定常ステージで毎秒到着する訪問者数（オープンモデル、既定 `20`） |
| `-users N` | 訪問者数の固定値。`0` より大きければ `-rate` の代わりにクローズドモデルを使う |
| `-duration D` | 実行時間（既定 `20s`） |
| `-pace X` | すべての思考時間に掛ける倍率。`0.2` なら 5 倍速（既定 `1`） |
| `-session-ttl D` | モックサーバー上のログインセッションの平均寿命（既定 `4s`） |
| `-newcomers N` | 新規登録の訪問者が使えるアカウント数。各 1 回のみ（既定 `40`） |
| `-check` | 結果をしきい値で判定し、満たさなければ終了ステータス `1` で終わる（既定 `true`） |
| `-format F` / `-o FILE` / `-raw FILE` | `checkout` と同じ |

訪問者の行動とモックサーバーが見た内容の集計は標準エラーに出るので、`-format json` をパイプでつなげます。

## mix

プラン全体の機能をまとめた小さな負荷試験です。モックショップに対し、イテレーションの 70% が `browse`、30% が `buy` を実行します。次のものを扱います。

- `Plan.Scenarios` と `Weighted`: 独立した流れを重みでランダムに選ぶ
- `Plan.UserStages`・`RepeatUserStages`・`StartUsers`: 5 人から 20 人まで上げて戻す波を 2 回
- `Plan.AbortOn` と `ErrAborted`: 壊れたサーバーを叩き続けず、中断までのレポートも出力する
- `Plan.Progress`（1 秒ごとに 1 行、標準エラーへ）と `Plan.MetricsAddr`（Prometheus の `/metrics`。`-metrics` を付けたときだけ有効）
- `ExpectStatusIn`・`ExpectJSON`・`Response.JSON(path)`: 構造体を宣言せずに JSON を検証・取り出し
- シナリオごとのしきい値（`{scenario=...}`）と、イテレーション全体の時間（`iteration_p95`）

ファイル構成がそのまま見本です: `main.go`（フラグ・プラン・実行・保存・判定）、`scenarios.go`、`thresholds.go`、`mock.go`。

```sh
go run ./examples/mix                           # 12 秒、人数の波が 2 回
go run ./examples/mix -break-after 2s           # 2 秒後にモックが壊れ、AbortOn が実行を止める（終了ステータス 1）
go run ./examples/mix -metrics :9090            # 実行中は http://localhost:9090/metrics も公開
go run ./examples/mix -format json -o result.json
```

| フラグ | 意味 |
|---|---|
| `-duration D` | 実行時間（既定 `12s`）。2 回の波はこの時間に割り付けられる |
| `-break-after D` | この時間が過ぎたらモックサーバーが `503` を返すようにする。`0`（既定）なら壊さない |
| `-metrics ADDR` | 実行中、このアドレス（例 `:9090`）で `/metrics` を公開する。空なら公開しない |
| `-check` | 結果をしきい値で判定し、満たさなければ終了ステータス `1` で終わる（既定 `true`） |
| `-format F` / `-o FILE` / `-raw FILE` | `checkout` と同じ |

進捗の行は標準エラーに出るので、`-format json` をパイプでつなげます。`error_rate` に入るのは通信レベルのエラーで終わったリクエストだけです。`Expect*` の失敗はステップを失敗させ、シナリオがそのエラーを返すことで `iteration_fail_rate` に表れます。
