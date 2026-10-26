# jotaro 設計メモ

シナリオ表現力に重点を置いた、Go 製の負荷試験ツール（ライブラリ＋薄い CLI）。
壁打ちで決めた内容をまとめたもの。未決の項目は末尾に記載。

- モジュール: `github.com/RIRUa/jotaro`
- Go: 1.27.3（`.mise.toml` で固定）

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
var checkout = jotaro.Scenario("checkout", func(ctx context.Context, u *jotaro.User) error {
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

API 名は仮。

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
    // Arrival: jotaro.Rate(10, time.Second) // オープン: 毎秒10シナリオ（フェーズ2）
    Duration: time.Minute,
})
```

- `Users`（クローズド）: N 個の goroutine が逐次実行する。
- `Rate`（オープン）: 到着レート固定。vegeta の強みをシナリオ単位で再現する。

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
| 2 (`Rate`) | ドロップされたイテレーション数、Coordinated Omission の補正（予定開始時刻からの遅延で測る） |
| 3 | 時系列（1 秒ごと）、同時実行数の推移、接続の再利用率と DNS/TCP/TLS 時間、負荷生成側の健全性（CPU・ゴルーチン数） |

## 6. MVP の範囲

**入れる**: `Scenario` / `User` / `Step` / `Do()` / `ExpectStatus` / `Think` / `Plan`（`Users` のみ）/ ステップ単位のレイテンシとエラー数のコンソール出力（フェーズ1の計測）

**後回し**: `Rate`、レスポンスからの値の抽出ヘルパー（まずは `Decode`）、vegeta 互換出力、案B（ビルダー）、YAML

## 7. 未決

- `Pacer` に近い抽象を自前で持つか、vegeta の `lib` を import するか。
- vegeta 互換の程度（`Result` の JSON/CSV 出力だけか、`lib` の型を直接使うか）。
- 値の受け渡しを型安全に保つ範囲（A は Go の変数なので問題ないが、B・YAML では文字列キーになる）。
- 思考時間を含むシナリオ時間を、併記するか。
- ステップの失敗時のデフォルト挙動（継続／中断）をヘルパーで提供するか。
