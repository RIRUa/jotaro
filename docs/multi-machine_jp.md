# 複数のマシンから動かす

[English](multi-machine.md) | 日本語

1 台の負荷生成側では必要な負荷を出せないとき、同じテストを複数のマシンで同時に走らせ、結果を `jotaro merge`（またはライブラリの `report.MergeReports`）で 1 つにまとめる。まとめた結果は 1 回の実行のように読める。jotaro はマシンどうしの調整をしない。コントローラもエージェントもない。負荷を分け、マシンを同時に始め、ファイルを集めるのは自分でやる。このガイドは、そのやり方と、マージした数字の意味を説明する。

## 複数の負荷生成側を使うとき

1 台をチューニングし、それでも限界だと分かるまでは使わない。[`tuning_jp.md`](tuning_jp.md) を見る。限界のしるしは次のとおり。

- レポートが、負荷生成側の CPU が 90% に達したと警告している（テキストレポートの `loadgen:` 行と `warning:` 行、HTML レポートでも同じ）。
- マシンの限界に当たり、これ以上のチューニングでは上げられない。ネットワークインターフェースや、1 つのアドレスに開ける接続数などだ（[`tuning_jp.md`](tuning_jp.md#負荷生成側マシンの限界)）。
- 負荷を、わざと複数の場所から出したい。

ノート PC 級のマシンでも、1 台の負荷生成側でかなりのところまで行ける。測った例が [`tuning_jp.md` の CPU の節](tuning_jp.md#cpu-と-gomaxprocs-とコンテナ)にある。複数の負荷生成側には代償もある。始めるものも集めるものも増え、時計を揃える必要があり、結果をマージしなければならない。

## 負荷を分ける

どのマシンも同じプログラムを、それぞれの分担で動かす。分担の合計が、欲しい負荷になるようにする。

- **クローズドモデル（`Users`）**: ユーザー数を分ける。割り切れないときは、最初の何台かに 1 人ずつ多く割り当てて、合計をちょうどにする（下のプログラムの `share`）。
- **オープンモデル（`Arrival`）**: レートを分ける。`Rate(n, per)` は整数を取るので、`Rate(1000/3, time.Second)` は 1 台あたり毎秒 333、合計 999 になる。代わりに `Rate(1000, 3*time.Second)` を使う。3 秒に 1 000 回の到着は、毎秒 1 000 回のちょうど 3 分の 1 だ。`MaxUsers` は**負荷生成側ごとの**上限（0 なら各 1 000）なので、全体の上限が大事なら、これも分ける。
- **ステージ**（`UserStages`、`Stages`、`Ramp`）: すべての目標値やレートを同じように分ける。
- **Feeder の行**: 各プロセスの `Feeder` は最初の行から始まるので、N 台では、どのマシンの最初のユーザーも最初の行を受け取る。共有してはいけないデータ（1 回しか使えないアカウント（`Once`）、同時に 2 つ開けないセッション）は、手で分ける。各マシンに、行のそれぞれの部分を渡す。下のプログラムは N 行おきに取る。各部分が実行の間もつようにする。`Once` では、行を使い切ると `Next` は false を返す。
- **ユーザー ID**: `User.ID` はどのマシンでも 0 から数える（オープンモデルでは到着ごとに ID があり、これもマシンごとに 0 から数える）。`u.ID` からアカウントや番号を選ぶシナリオは、マシンごとに同じ値を使ってしまう。マシンごとのオフセットを足すか、`Feeder` を使う。
- **ランダムな Feeder**: `Random` の Feeder は、マシンごとに別々に選ぶので、マシンどうしで選んだものが重なる（繰り返しを再現するシードもない。[#124](https://github.com/RIRUa/jotaro/issues/124)）。

## 自分の分を受け持つプログラム

1 台で動かすときと同じプログラムに、自分の番号、マシンの台数、開始時刻のフラグをつけたものだ。結果ファイルは自分で書く。

```go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"time"

	"github.com/RIRUa/jotaro"
	"github.com/RIRUa/jotaro/report"
)

// share は、shards 台のうち（0 始まりの）shard 番のマシンが受け持つ total の分担。分担の合計は total になる。
func share(total, shard, shards int) int {
	n := total / shards
	if shard < total%shards {
		n++
	}
	return n
}

// shardRows は、shard 行目から shards 行おきに行を返す。どの 2 台も同じ行を受け取らない。
func shardRows[T any](rows []T, shard, shards int) []T {
	var mine []T
	for i := shard; i < len(rows); i += shards {
		mine = append(mine, rows[i])
	}
	return mine
}

func newScenario(accounts *jotaro.Feeder[map[string]string]) *jotaro.Scenario {
	return jotaro.NewScenario("login", func(ctx context.Context, u *jotaro.User) error {
		acc, ok := accounts.Next()
		if !ok {
			return fmt.Errorf("no account left for user %d", u.ID)
		}
		return u.Step("login", func(s *jotaro.Step) error {
			return s.Post("/login").JSON(map[string]string{"user": acc["user"]}).Do().ExpectStatus(http.StatusOK)
		})
	})
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "loadtest:", err)
		os.Exit(1)
	}
}

func run() error {
	shard := flag.Int("shard", 0, "number of this machine, from 0 to shards-1")
	shards := flag.Int("shards", 1, "number of load generators")
	startAt := flag.Int64("start-at", 0, "Unix time in seconds at which every machine starts; 0 starts at once")
	users := flag.Int("users", 100, "virtual users in total, over all machines")
	duration := flag.Duration("duration", time.Minute, "run time")
	out := flag.String("o", "result.json", "where to write the result")
	flag.Parse()
	if *shards < 1 || *shard < 0 || *shard >= *shards {
		return fmt.Errorf("-shard must be from 0 to -shards-1, got %d of %d", *shard, *shards)
	}

	f, err := os.Open("testdata/accounts.csv")
	if err != nil {
		return err
	}
	rows, err := jotaro.LoadCSV(f)
	f.Close()
	if err != nil {
		return err
	}
	accounts := jotaro.NewFeeder(shardRows(rows, *shard, *shards), jotaro.Circular)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()
	if *startAt > 0 { // 全マシンが重なるように、共通の開始時刻まで待つ
		select {
		case <-time.After(time.Until(time.Unix(*startAt, 0))):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	rep, runErr := jotaro.Run(ctx, jotaro.Plan{
		Scenario: newScenario(accounts),
		BaseURL:  os.Getenv("BASE_URL"),
		Users:    share(*users, *shard, *shards),
		Duration: *duration,
		Progress: os.Stderr,
	})
	if rep == nil {
		return runErr // Plan が不正（たとえばユーザー数がマシンの台数より少ない）: 何も実行されていない
	}
	file, err := os.Create(*out)
	if err != nil {
		return err
	}
	if err := rep.Write(file, report.FormatJSON); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	return runErr
}
```

オープンモデルでは、Plan の `Users` の行を、レートのちょうど 3 分の 1 に置き換え、上限も分ける。

<!-- doccheck: before var shards int -->
<!-- doccheck: fields jotaro.Plan -->
```go
Arrival:  jotaro.Rate(1000, time.Duration(shards)*time.Second), // 全マシンで毎秒 1 000 回
MaxUsers: 1000 / shards,                                        // 上限は負荷生成側ごと
```

どのマシンでも、プログラムには同じバージョンの jotaro を使い（同じバイナリにするのが一番簡単）、シナリオ名とステップ名も同じにする。マージはシナリオとステップを名前で対応づけるので、あるビルドで打ち間違えると、正しい行に足されず、別の行ができる。

## 負荷生成側を同時に始める

マージは、各マシンの開始時刻でマシンを重ね合わせる。同時に走っていないと、マージした時系列とスループットは、実際には起きなかったことを表す。1 か所から、決めた 1 つの時刻に始め、全マシンの終了を待つ。

### 時計

どの結果にも `started_at` があり、それを実行したマシンの時計（UTC）から読む。`jotaro merge` は、この開始時刻の差だけ、各マシンの 1 秒ごとの時系列を、秒単位でずらし、マージした結果の実行時間は、最初の開始から最後の終了までになる。つまり時計が揃っている必要がある。NTP（`chrony`、`systemd-timesyncd`、またはお使いのプラットフォームにあるもの）で揃えておく。

ずれがどう効くかは、始め方による。

- **同じ実時刻にコマンドで始める**（`-start-at` なしの `ssh` ループ）: 時計が 3 秒進んでいるマシンは、結果の時刻を 3 秒遅く記録する。マージはそれを信じ、そのマシンの時系列を 3 秒ずらし、実行時間は 3 秒長くなる。負荷は実際には重なっていたのにだ。ここで測った値は、3 つの 6 秒の結果の 1 つの `started_at` を +3 秒に書き換えたとき、マージした実行時間は 6.0 秒ではなく 9.0 秒、`rps` は 4 510 ではなく 3 015、並行数は終始 100 ではなく 67、100、33 と動いた。
- **決めた壁時計の時刻に始める**（`-start-at`）: 時計が 3 秒進んでいるマシンは、その時刻に実時間で 3 秒早く達するので、そのマシンは早く始まる。`started_at` は正しく見えるので、マージはずれを示せない。負荷はずれているのに、結果にはそれを示すものが何もない。

どちらでも、現れるのは同じ種類のずれで、対処も同じだ。時計を同期させ、マージを信じる前に、結果の `started_at` と、マージした時系列の並行数（実行の大半で、分担の合計に近いはず）を確かめる。開始のずれが 0.5 秒未満なら時系列はずれない（ずれは秒単位に丸める）が、実行時間には数えられる。

### ssh で

プログラムを 1 度ビルドし、同じバイナリと `testdata` を全マシンに置き、自分のマシンから起動する。`-start-at` は全マシンに、始めるための同じ Unix 時刻を渡す。すべての `ssh` 接続が立ち上がるのに十分な先にする。時刻は負荷生成側のどれかから読み、先の余裕を、負荷生成側が合意している時計で測るようにする。

```sh
hosts="gen1 gen2 gen3"
n=3
start=$(( $(ssh gen1 date +%s) + 20 ))
i=0
pids=""
for h in $hosts; do
  ssh "$h" "cd /opt/loadtest && BASE_URL=https://staging.example.com ./loadtest -shard $i -shards $n -users 300 -start-at $start -o /tmp/result.json" &
  pids="$pids $!"
  i=$((i + 1))
done

fail=0
for p in $pids; do wait "$p" || fail=1; done
mkdir -p results
files=""
for h in $hosts; do
  scp "$h:/tmp/result.json" "results/$h.json" || fail=1
  files="$files results/$h.json"
done
[ "$fail" = 0 ] || { echo "a generator failed: not merging" >&2; exit 1; }
jotaro merge -o results/merged.json $files
```

マージするのは、すべての負荷生成側が終わり、すべてのファイルが届いたときだけにする。`merge` は渡されたファイルをマージするので、`results/*.json` のようなグロブでは、ファイルが届かなかったマシンは単に一覧に入らず、結果は意図したより小さい実行を表す。プロセスごとの `wait` は、素の `wait` では捨てられてしまう各 `ssh` の終了ステータスを残す。

### Kubernetes の Job として

Indexed の Job は N 個の Pod を起動し、それぞれに `JOB_COMPLETION_INDEX` で番号を渡す。Pod は別々の時刻にスケジュールされる（イメージの取得に時間がかかる）ので、始める時刻は共有する。このマニフェストは Kubernetes の Job のドキュメントから書いたもので、このリポジトリのテストでは**動かしていない**。自分のクラスターで確かめること。

```yaml
apiVersion: batch/v1
kind: Job
metadata:
  name: loadtest
spec:
  completions: 3
  parallelism: 3
  completionMode: Indexed
  backoffLimit: 0
  template:
    spec:
      restartPolicy: Never
      containers:
        - name: loadtest
          image: registry.example.com/myservice/loadtest:1.0
          env:
            - name: BASE_URL
              value: http://myservice.staging.svc:8080
            - name: START_AT
              value: "1900000000" # Unix 時刻。Job を投入するときに、現在時刻 + 全 Pod が起動するのに十分な時間にする
          command: ["/bin/sh", "-c"]
          args:
            - ./loadtest -shard "$JOB_COMPLETION_INDEX" -shards 3 -users 300 -start-at "$START_AT" -o "/results/gen$JOB_COMPLETION_INDEX.json"
          volumeMounts:
            - name: results
              mountPath: /results
      volumes:
        - name: results
          persistentVolumeClaim:
            claimName: loadtest-results # 全 Pod が書き込めるボリューム。または代わりにプログラムからアップロードする
```

`START_AT` は Job を投入するときに設定し（たとえば `$(( $(date +%s) + 120 ))`）、一番遅い Pod が起動するのに十分な時間を残す。その時刻より後に起動した Pod はすぐ始まり、マージした結果では、遅れたマシンとして現れる。`gen0.json` から `gen2.json` までをボリューム（またはアップロード先のバケット）から集め、上と同じように `jotaro merge` にかける。`completions` と `-shards` は同じ数にする。

## 結果をマージする

```sh
jotaro merge -o results/merged.json results/gen1.json results/gen2.json results/gen3.json
jotaro check -t 'p95<200ms' -t 'error_rate<1%' -t 'dropped<1' results/merged.json
jotaro report -o results/report.html results/merged.json
```

`merge` は既定で JSON を書く（`-format html` や `text` も使える）ので、マージした結果は、ほかの結果と同じように、再びマージしたり、チェックしたり、比べたり、レポートにしたりできる。`-raw` を付けると、代わりに生データ（`Plan.Raw`）を読み、すべての入力を同じ方法で読む。ライブラリでは `report.MergeReports(reps...)` だ。

## マージした数字の意味

| 数字 | マージのしかた |
|---|---|
| リクエスト数、バイト数、ステータス、エラー、`dropped`、`conn_new`、`conn_reused` | 合計する |
| 実行時間と `rps` | 実行時間は最初の開始から最後の終了まで。`rps` は全リクエスト数をそれで割ったもの。時計が揃っていて開始が共通なら、1 台の実行時間と同じになる。ずれや遅れた開始は、実行時間を長くし、`rps` を下げる |
| `min`、`max`、`mean` | 正確 |
| p50〜p99 | ヒストグラムを足して再計算する。**相対誤差 1% 以内**で、入力がすべて正確でもそうなる。空の分布とマージした分布は、そのまま残る |
| 時系列の並行数 | ずらしたあとで合計する。**上限の見積もり**で、あるマシンの一番忙しい秒が、別のマシンの一番忙しい秒とは限らないため |
| 健全性の時系列のゴルーチン数 | 合計する |
| 健全性の時系列の CPU | **最大**。複数の負荷生成側があるときは、飽和に一番近いものが、数字を信じてよいかを決める。どこか 1 か所でも 90% があれば、マージしたレポートに警告が付く |
| シナリオとステップ | 名前で対応づける。順序は最初に現れた順 |

マージを拒むか歪める規則が 2 つあるので、知っておく。

- モデル（`open`）が食い違う結果はマージできない。推測せずにエラーになる。
- マージはプランを比べない。別のレート、別の長さ、別のビルドで走ったマシンも、ほかと同じように足される。プランは結果に記録されない（[#92](https://github.com/RIRUa/jotaro/issues/92)）ので、マージした結果はそれを覚えていない。走らせたコマンドは残しておく。

マージは、重なって走った実行のためのものだ。同じテストを続けて 2 回走らせたものは 2 つの実行で、マージすると、間の空きをまたぐ 1 つの実行時間になり（2 秒の実行を続けて 2 回始めたものは、マージした実行時間が 4.6 秒になった）、時系列は穴のある時間軸の上に置かれる。

## 落とし穴

- **分け方の丸め。** 分担の合計が合っているか確かめる。`share` は合う。1 台あたり `1000 / 3` は合わない。配置を確かめる一番手早い方法は、マージした結果のリクエスト数の合計だ（プランに対する 1 台あたりの件数に、台数を掛けたものに近いはず）。
- **対象と上限は共有される。** すべての負荷生成側が同じ対象を叩くので、対象の上限（と、全クライアントをまとめて数えるレートリミッター）には合計が見える。アドレスごとのレート制限は、各負荷生成側を別々に見るので、合計のレートで 1 つのクライアントが叩く場合とは同じではない。
- **1 台の失敗。** CPU、ファイルディスクリプタ、ポートの限界にある 1 台は、全員のマージしたパーセンタイルを損なう。見つける手がかりが CPU の最大値だ。CPU の警告が出ているときは、マージした結果だけでなく、個々の結果も見る。
- **集めること。** 結果は生データに比べれば小さいが、ごく小さいわけではない（上の例の 6 秒の実行は 1 台あたり約 26 KB で、JSON は実行の長さに応じて大きくなる。[#93](https://github.com/RIRUa/jotaro/issues/93)）。生データ（`Plan.Raw`）はずっと大きい。再集計が必要でなければ、集計済みの結果をマージする。
- **共有される中断はない。** `AbortOn` は、各マシンが自分の数字で評価する。あるマシンが止まっても、ほかは続けることがあり、そのときマージした結果には、1 つの分担が早く終わった実行が現れる。
