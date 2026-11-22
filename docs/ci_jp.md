# CI で jotaro を動かす

[English](ci.md) | 日本語

プルリクエストのたびに、あるいは毎晩動き、サービスが遅くなったらビルドを落とし、理由を調べるための材料を残す負荷テスト。このガイドは GitHub Actions で、完全なワークフローとしてそれを組み立てる。シェルを動かせる CI なら、同じ手順がどれでも使える。複数のマシンから動かす方法は [`multi-machine_jp.md`](multi-machine_jp.md)、数字をどこまで信じてよいかは [`tuning_jp.md`](tuning_jp.md) を見る。

## CI が jotaro に求めるもの

| 求めるもの | jotaro が用意しているもの |
|---|---|
| 上限を超えたらジョブを落とす | `jotaro check -t ... result.json` は、しきい値を満たさないものがあると終了コード 1 で終わる |
| 以前より遅くなったらジョブを落とす | `jotaro compare -max-p95-regression <percent> base.json new.json` は、どれかの p95 がその割合より悪化すると終了コード 1 で終わる |
| 証拠を残す | JSON の結果（`jotaro report` で自己完結した HTML ページにできる）と、必要なら生データ（`Plan.Raw`） |
| 実行ページに結果を出す | `check` と `compare` の出力はプレーンテキストなので、ジョブサマリーに載せられる |

終了コード（`cmd/jotaro/main.go` の実装どおり）:

| コマンド | `0` | `1` | `2` |
|---|---|---|---|
| `jotaro check` | すべてのしきい値を満たした | 満たさないしきい値がある、またはファイルを読めない | 使い方の誤り（`-t` がない、解釈できないしきい値、ファイルがない） |
| `jotaro compare` | ゲートの指定がない、またはどの p95 も上限を超えて悪化していない | p95 が上限を超えて悪化した、またはファイルを読めない | 使い方の誤り |
| `jotaro report` / `merge` | 完了 | ファイルを読めない、または書けない。または（`merge`）結果をマージできない（オープンとクローズドの組み合わせ） | 使い方の誤り |

パイプラインでは 2 点に注意する。`1` は「サービスが遅くなった」と「ファイルがない」を区別しないので、サービスのせいにする前にファイルがあるかを確かめる。また `-max-p95-regression` なしの `jotaro compare` は表を出すだけで、常に終了コード 0 で終わる。

## テストプログラム

テストはリポジトリの中にある普通の Go プログラムだ（[フォルダを構成する](../README_jp.md#4-フォルダを構成する)を参照）。CI 向けには 3 つが要る。対象のアドレスを環境変数から受け取ること、実行が途中で止められても結果ファイルを書くこと、実行そのものが失敗したら 0 以外の終了コードにすること。しきい値は `jotaro check` に任せるので、上限を変えてもプログラムは変わらない。

```go
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

var health = jotaro.NewScenario("health", func(ctx context.Context, u *jotaro.User) error {
	return u.Step("get", func(s *jotaro.Step) error {
		return s.Get("/health").Do().ExpectStatus(http.StatusOK)
	})
})

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	baseURL := os.Getenv("BASE_URL")
	if baseURL == "" {
		baseURL = "http://localhost:8080"
	}
	rep, runErr := jotaro.Run(ctx, jotaro.Plan{
		Scenario: health,
		BaseURL:  baseURL,
		Arrival:  jotaro.Rate(50, time.Second),
		Duration: 30 * time.Second,
		Progress: os.Stderr, // ジョブのログに 1 秒に 1 行出す
		AbortOn:  []jotaro.AbortRule{{When: "error_rate>50%", For: 5 * time.Second}},
	})
	if rep == nil {
		return runErr // Plan が不正: 何も実行されていない
	}

	// runErr を見る前に結果を書く。読みたいのは、途中で止められた実行のほうだ。
	if err := os.MkdirAll("results", 0o755); err != nil {
		return err
	}
	f, err := os.Create("results/result.json")
	if err != nil {
		return err
	}
	if err := rep.Write(f, report.FormatJSON); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return runErr // AbortOn が実行を止めたときは jotaro.ErrAborted を包んでいる
}
```

選んだ理由:

- `AbortOn` が実行を止めると、`jotaro.Run` は途中までのレポートと**同時に**エラーを返す。そこでファイルを先に書き、終了コードはそのあとでエラーによって決める。ジョブは失敗するが、アーティファクトには中断までの数字が残る。
- オープンモデル（`Arrival`）は、サービスが遅くなってもペースを保つ。回帰テストが欲しいのはそれだ。下の `dropped<1` のしきい値と組み合わせる。到着を取りこぼした実行は、走れた分しか測っていないからだ。
- 実行は、少なくとも数百リクエストを送れる長さにする。100 件に満たないと p99 は一番遅い 1 件にすぎず、20 件に満たないと p95 もそうなる（`summarize_result` が警告する条件と同じ）。

## ワークフロー

これが完全な `.github/workflows/load-test.yml` だ。`docker compose` でサービスを起動し、応答するまで待ち、テストを走らせ、`jotaro check` でゲートをかけ、`main` の直近の成功した実行と比べ、どうなってもアーティファクトとして結果を残す。

```yaml
name: load-test

on:
  pull_request:
  push:
    branches: [main]
  workflow_dispatch:

permissions:
  contents: read
  actions: read # 以前の実行のベースラインのアーティファクトをダウンロードする

concurrency:
  group: load-test-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

jobs:
  load-test:
    runs-on: ubuntu-latest
    timeout-minutes: 15
    env:
      JOTARO_VERSION: v0.1.3
      BASE_URL: http://localhost:8080
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
        with:
          persist-credentials: false
      - uses: actions/setup-go@d8cbb8045d7f43706b9177cceb36c1f91a6644d5 # v7.1.0
        with:
          go-version-file: go.mod

      - name: Install the jotaro CLI
        run: |
          go install "github.com/RIRUa/jotaro/cmd/jotaro@${JOTARO_VERSION}"
          echo "$(go env GOPATH)/bin" >> "$GITHUB_PATH"
          "$(go env GOPATH)/bin/jotaro" version

      - name: Start the service and wait until it answers
        run: |
          docker compose up -d --build
          curl --fail --silent --show-error --retry 30 --retry-delay 2 --retry-all-errors "$BASE_URL/health"

      - name: Run the load test
        run: go run ./loadtest

      - name: Check the thresholds
        run: |
          status=0
          jotaro check \
            -t 'p95<300ms' -t 'error_rate<1%' -t 'http_5xx_rate<1%' \
            -t 'iteration_fail_rate<1%' -t 'dropped<1' \
            results/result.json > results/check.txt 2>&1 || status=$?
          cat results/check.txt
          { echo '### jotaro check'; echo '```'; cat results/check.txt; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
          exit "$status"

      - name: Download the baseline (the last successful run on main)
        if: always()
        env:
          GH_TOKEN: ${{ github.token }}
          GH_REPO: ${{ github.repository }}
        run: |
          id=$(gh run list --workflow load-test.yml --branch main --event push --status success --limit 1 --json databaseId --jq '.[0].databaseId')
          if [ -n "$id" ] && [ "$id" != "null" ]; then
            gh run download "$id" --name load-test-results --dir baseline || echo "the baseline artifact is gone (expired?)"
          else
            echo "no successful run on main yet"
          fi

      - name: Compare with the baseline
        if: always() && hashFiles('baseline/result.json') != '' && hashFiles('results/result.json') != ''
        continue-on-error: true # 共有ランナーはノイズが大きい: 悪化は報告するが、ブロックはしない（下を参照）
        run: |
          status=0
          jotaro compare -max-p95-regression 30 baseline/result.json results/result.json > results/compare.txt 2>&1 || status=$?
          cat results/compare.txt
          { echo '### jotaro compare (against the last successful run on main)'; echo '```'; cat results/compare.txt; echo '```'; } >> "$GITHUB_STEP_SUMMARY"
          exit "$status"

      - name: Build the reports
        if: always() && hashFiles('results/result.json') != ''
        run: |
          jotaro report -o results/report.html results/result.json
          jotaro report -format text -o results/report.txt results/result.json
          if grep -q '^warning:' results/report.txt; then
            echo "::warning title=jotaro::$(grep -m 1 '^warning:' results/report.txt)"
          fi
          { echo '<details><summary>jotaro report</summary>'; echo; echo '```'; cat results/report.txt; echo '```'; echo; echo '</details>'; } >> "$GITHUB_STEP_SUMMARY"

      - name: Upload the results
        if: always()
        uses: actions/upload-artifact@cf430e030ddbb5b0abf93d22962f4752f3646cd9 # v7.0.2
        with:
          name: load-test-results
          path: results/
          if-no-files-found: ignore
          retention-days: 30

      - name: Show the logs of the service
        if: failure()
        run: docker compose logs --no-color --tail 200

      - name: Stop the service
        if: always()
        run: docker compose down
```

サービスは、リポジトリにある `compose.yaml` から起動する。たとえば次のようなものだ。

```yaml
services:
  app:
    build: .
    ports:
      - "8080:8080"
```

### 各ステップの役割

- **バージョンの固定。** `go install ...@v0.1.3` は、CLI のそのリリースをそのままインストールする（モジュールパスは `github.com/RIRUa/jotaro`。`jotaro version` がバージョンをログに出す）。`go.mod` のバージョンと同じにしておくと、結果を書いたライブラリと読む CLI が同じリリースになる。アクションは、このリポジトリ自身のワークフローと同じく、コミット SHA で固定し、バージョンをコメントに書いてある。Dependabot はアクションの更新を出せる。
- **準備ができるまで待つ。** `docker compose up -d` はコンテナが起動したら戻るのであって、サービスが応答するようになってから戻るのではない。`curl --retry` のループは `/health` が成功するまで待つ（最大 1 分ほど）ので、まだ起動中のサービスにテストを始めてしまうことがなく、いつまでも立ち上がらないサービスは、`connection_refused` だらけの実行ではなくここで失敗する。ループの代わりに、ヘルスチェックつきのサービスコンテナでもよい。コンテナが healthy になるまで、ジョブはステップを始めない。
- **`if: always()`。** 面白い実行は失敗した実行だ。証拠を集めるステップにはすべて `always()` がついているので、しきい値が失敗しても、ベースラインとの比較、HTML レポート、アーティファクトは出る。`results/result.json` を読むステップは、先にそれがあるかを確かめる。書く前に落ちた実行への備えだ。
- **ゲート。** `jotaro check` は結果を読み、満たさないしきい値が 1 つでもあれば終了コード 1 で終わる。出力はいったんファイルに書くので、終了ステータスは保たれ、同じテキストがログとジョブサマリー（`$GITHUB_STEP_SUMMARY`）の両方に届く。対象やデータが無いしきい値（ステップ名の打ち間違い、リクエストが 0 件の実行）は**満たさない**扱いになるので、設定を間違えたゲートは黙って通らず、失敗する。書き方は [`result_jp.md`](result_jp.md#5-しきい値判定実装済み) と README にある。
- **警告はしきい値ではない。** 負荷生成側の CPU が 90% に達したことは、テキストレポートの `warning:` 行として出るもので、`jotaro check` が判定できるものではない。「Build the reports」のステップは、それをワークフローのアノテーションにし、テキストレポート全体をサマリーに入れるので、行が埋もれない。通ったことを信じる前に読む（[`tuning_jp.md`](tuning_jp.md) を参照）。
- **アーティファクト。** `report.html` は自己完結した 1 ファイルだ。アーティファクトをダウンロードして、ブラウザで開く。`result.json` は、あとで `jotaro compare` や `jotaro merge` が読むものだ。JSON は実行の長さに応じて大きくなる（`checkout` サンプルの 2 秒の実行で 59 KB）ので、`retention-days` と実行の長さは釣り合わせる。長い実行は、既定の 90 日より数日だけ残すほうがよい。

## ベースラインとの比較

`jotaro compare` は、rps、p50、p95、p99、エラー率を全体とステップごとに表にして出す。`-max-p95-regression <percent>` を付けると、どれかの p95（全体またはステップ）がその割合より悪化したとき終了コード 1 で終わる。ゲートの対象は p95 だけだ。差が有意かどうかの統計的な検定はない。

**ベースラインをどこに置くか。**

- **`main` の直近の成功した `push` の実行のアーティファクト**（ワークフローのとおり）。探すときは `--event push` を付ける。`--branch main` だけだと、ブランチがたまたま `main` という名前のフォークからのプルリクエストの実行にも一致し、信頼できない実行がベースラインになってはならないからだ。保守するものがない。マージのたびにベースラインが動き、しきい値に失敗した `main` の実行は、成功した実行だけを一覧にするので、ベースラインにはならない。アーティファクトは期限切れになる（`retention-days`）が、上のステップはベースラインが無い場合を扱っている。最初の実行にはベースラインが無いので、何も比べない。
- **コミットしたファイル**（`loadtest/baseline.json` など）。ベースラインは、誰かが変えると決めたときだけ変わり、プルリクエストに変更が見える。代わりに古くなる。意図した遅くなり方は、新しいベースラインとしてコミットしなければならず、ファイルも大きい（上のサイズの注を参照）。ダウンロードのステップを `cp loadtest/baseline.json baseline/result.json` に置き換えるか、`compare` にコミットしたファイルを直接渡す。

**共有ランナーはノイズが大きい。** GitHub のホステッドランナーはホストを他のジョブと共有するので、同じコミットでも実行のたびにレイテンシが変わる。さらに、サービスと負荷生成側がランナーの少数の CPU を分け合う（[負荷生成側と対象を同じマシンに置く](tuning_jp.md#負荷生成側と対象を同じマシンに置く)を参照）。p95 に 10% のような相対ゲートをかけると、ノイズで落ちる。やること:

- 上限を決める前にノイズを測る。同じコミットでワークフローを何度か起動し（`workflow_dispatch`）、p95 がどれだけ動くかを見る。上限はそのばらつきより十分に大きくする。ワークフローの `30` は仮の値で、測定から導いた推奨値ではない。
- 比較はまず情報として使い（上のように `continue-on-error: true`）、ゲートは絶対値のしきい値にしておく。ノイズが分かり、上限が安全になってから `continue-on-error` を外す。
- 安定したものを比べる。多数のリクエストにわたるステップの p95 は、p99 や max より安定している。`jotaro compare` がゲートにするのは p95 だけで、ほかは読んで判断するためのものだ。
- 厳しいゲートにするなら、専用のランナー（セルフホストのマシン、または大きさが決まった大きいランナー）で動かし、対象は別のホストに置く。

## バリエーション

- **`docker compose` の代わりにサービスコンテナ**: サービスを `jobs.<id>.services` に置き、`options` に `--health-cmd` を書くと、ジョブは healthy になるまで待ってからステップを始める。サービスには、公開したポートで `localhost` から届く。
- **`go install` の代わりにリリースのアーカイブ**: CLI のコンパイル時間を省ける。リリースのアーカイブと `checksums.txt` をダウンロードし、合計を確かめて展開する:

  ```sh
  base=https://github.com/RIRUa/jotaro/releases/download/v0.1.3
  curl -fsSL -O "$base/jotaro_0.1.3_linux_amd64.tar.gz" -O "$base/checksums.txt"
  sha256sum --check --ignore-missing checksums.txt
  tar -xzf jotaro_0.1.3_linux_amd64.tar.gz jotaro
  ```

  アーカイブの名前は `jotaro_<version>_<os>_<arch>.tar.gz`（Windows は `.zip`）。リリースのワークフローは、アーカイブにビルドの来歴証明（attestation）も付ける（[#167](https://github.com/RIRUa/jotaro/pull/167) から。それ以前のリリースには付いていない）。
- **ほかの CI**: YAML 以外に GitHub 固有のものはない。CLI をインストールし、サービスを起動し、プログラムを走らせ、`jotaro check` を実行し、`results/` を残す。ステップは終了コードで失敗し、`check` と `compare` のテキスト出力は、その CI にあるサマリーに出せる。
- **1 台に収まらない負荷**: 複数のマシンでプログラムを走らせ、結果をマージする。[`multi-machine_jp.md`](multi-machine_jp.md) を見る。

## 既知の制限

- `jotaro check` には負荷生成側の健全性についてのしきい値がないので、詰まった負荷生成側は、レポートの警告としては見えても、ゲートの失敗としては現れない。通ったことを信じる前に警告を読む。[`tuning_jp.md`](tuning_jp.md) を参照。
- `check` と `compare` の終了コード 1 は、上に書いたとおり、読めないファイルも意味する。ワークフローのように、ファイルがあるかを先に確かめる。
- `check` には機械可読な出力がなく、しきい値をファイルから読むこともまだできない（[#97](https://github.com/RIRUa/jotaro/issues/97)）ので、コマンドラインに書く。`compare` がゲートにするのは p95 だけで、片方の結果にしかないステップは表示しない（[#98](https://github.com/RIRUa/jotaro/issues/98)）。
- このための composite action や reusable workflow は用意していない。上のワークフローは、コピーして使うものだ。
