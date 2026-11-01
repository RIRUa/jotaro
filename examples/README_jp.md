# サンプル

[English](README.md) | [日本語](README_jp.md)

そのまま動かせる jotaro のサンプルです。どれも自前のモックサーバーを立てるので、ほかに準備するものはありません。リポジトリのルートで実行してください。

| サンプル | 内容 |
|---|---|
| [`checkout`](checkout) | 基本: 短いフロー（ログイン → 商品一覧 → 注文）、クローズド／オープンの負荷モデル、ステージ、HTML と生データの出力 |
| [`marketplace`](marketplace) | 深く現実的なシナリオ: 1 つのシナリオで複数種類の訪問者を演じ、状態を持つセッション、再試行、ポーリング、実行を失敗させるしきい値を扱う |

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
