# open issue を並列で進める手順書

複数のワーカーと、PR ごとのレビュアーで、open issue を並列に片づける手順です。特定のツールには依存しません。Claude Code のサブエージェントでも、別の AI コーディングツールの並列セッションでも、人間でも、同じ手順で動かせます。版管理は Jujutsu（`jj`）がある場合と、`git` だけの場合の両方を書いています。

**この手順書の例は、GitHub の issue、`gh` コマンド、`release/vX.Y.Z` というブランチ構成、Go のテストを前提に書いています。これらはこのリポジトリの場合で、あなたの環境と同じとは限りません。** タスクの出どころ（GitHub の issue とは限りません）や、PR のベースブランチ（release ブランチとは限りません）などは、**推測せず、始める前に人間に確認します**（§3.0）。以降の `<...>` や `vX.Y.Z` は、その回答で置き換えます。

エージェント同士が直接メッセージを送れる「チームモード」を持つツールでは、その機能で動かせます。**持たないツールでは、tmux を CLI で操作する方式（§8）で動かします。**

## 1. 原則

1. **1 メンバー 1 タスク（= 1 issue = 1 PR）。** 1 つの PR には 1 つの目的だけを入れます。
2. **PR のベースブランチは、人間に確認した値に従う（§3.0）。** 推測しません。このリポジトリでは、`main` ではなく、issue の milestone で決まる `release/vX.Y.Z` で、リリースは release ブランチを `main` にマージして切ります（`CONTRIBUTING.md`）。
3. **作業ディレクトリを分ける。** ワーカーごとに専用の workspace（jj）か worktree（git）を持たせます。1 つの作業コピーを共有しません。
4. **レビュアーは読み取り専用。** 直すのはワーカーだけです。
5. **合意はコメントで残す。** ワーカーとレビュアーのやり取りと結論は、PR のコメントに書きます。後から経緯を追え、リードも途中から入れます。
6. **マージの承認者は、始める前に決めて確認する（§3.0）。** 例: 人間が PR ごとに承認する / レビュアーの `Consensus:` コメントを承認とする。決めた承認の条件がそろうまでマージしません。管理者権限でブランチ保護を迂回する（`--admin`）ことの可否も、同じときに確認します。

## 2. 役割

| 役割 | 仕事 | してはいけないこと |
|---|---|---|
| リード（1 人） | 分解、レーン割り当て、ワーカーとレビュアーの起動、マージ条件の確認、マージの依頼、後片付け | 実装 |
| ワーカー（1 issue につき 1 人） | 実装、テスト、CHANGELOG、PR 作成、レビュー指摘への対応、rebase | マージ、他の issue への手出し |
| レビュアー（PR につき 1 人、PR のたびに新規） | レビュー、ワーカーとの相談、`Consensus:` コメント | ファイルの変更、push、マージ |
| 人間（オーナー） | 設計判断、§3.0 の回答。マージの承認者にするかも §3.0 で決める | |

- ワーカーの同時人数は 5〜6 人までです。ボトルネックはレビューと統合なので、増やしても待ちが増えるだけです。
- ワーカーは PR が閉じるまで残します。レビュアーも PR が閉じるまで残し、ワーカーと相談できるようにします。

## 3. 準備（リード）

### 3.0 最初に人間に確認すること

次の項目は、環境ごとに違います。**リポジトリを調べて当たりを付けてもよいですが、推測のまま進めません。** 当たりを付けた値は「推測」と明記して示し、人間の確認を得てから使います。確認は 1 回にまとめて行い、回答は計画の冒頭に記録します（あとで聞き直さないため）。

| 確認項目 | 例 | 影響する箇所 |
|---|---|---|
| タスクの出どころ | GitHub の issue / Jira / Linear / TODO ファイル / 口頭の一覧 | 集め方（§3.1）、PR との関連づけ、完了の記録（§6） |
| 対象の範囲 | milestone、ラベル、番号の一覧 | §3.1 |
| **PR のベースブランチ** | `release/vX.Y.Z` / `main` / `develop` / 機能ブランチ。タスクごとに違うか | §4.1、§4.4、§6 |
| PR のホストと CLI | GitHub（`gh`）/ GitLab（`glab`）/ Gitea / Bitbucket | 手順内の `gh` を置き換える |
| 版管理 | `jj`（colocated か）/ `git` | §4 |
| マージ方式とブランチ保護 | マージコミット / squash / rebase。承認必須か。管理者権限での迂回を許すか | §6 |
| **マージの承認者** | 人間が PR ごとに承認する / レビュアーの `Consensus:` コメントを承認とみなす（人間には聞かない）/ その他 | §1、§6 |
| タスクを閉じる方法 | PR のマージで自動 / 手動 / 閉じない | §6、§7 |
| 変更履歴の規約 | `CHANGELOG.md` の更新を PR ごとに求めるか | §4.3 |
| 検証コマンド | 例は Go（`go vet`、`go test -race`）。言語やプロジェクトで違う | §4.3、§9 |
| 同時ワーカー数、使うモデル | 5〜6 人、レビュアーのモデルの使い分け | §2、§5 |
| 先に決める設計判断 | 複数のタスクにまたがるもの | §3.1 |

### 3.1 計画を立てる

1. **対象のタスクを集めます。** 範囲は §3.0 の回答に従います（GitHub の issue なら、例えば `gh issue list --milestone vX.Y.Z --state open`）。追跡用のタスクがある場合は、その分類を土台にします。
2. **先に決める設計判断を洗い出します。** 複数の issue にまたがる判断（例: 失敗リクエストをどう数えるか、`Duration` の意味）は、実装を始める前に人間と決めます。後から変えると複数のレーンがやり直しになります。決めたことは、ワーカーへの指示に書きます。
3. **同じファイルを触る issue を同じレーンにまとめます。** レーン内は直列、レーン間は並列です。レーンの例: threshold、raw とマージ、CLI、MCP、エンジン本体、リクエスト／レスポンス、CI とドキュメント。
4. **波に分けます。** 1 つの波は、各レーンの先頭の issue です。前の PR がマージされたら、同じレーンの次の issue を起動します。
5. **最初に直すものがあれば、単独で先に直します。** CI が不安定になる原因（例: テストの競合）は、他の全 PR に影響します。
6. **ブランチ保護とマージ方式を確認します。** 承認レビュー必須の場合、作者と同じアカウントでは承認を付けられません（§7）。

## 4. ワーカーの手順

指示の雛形は §9 にあります。

### 4.1 作業コピーを作る

ベースは、§3.0 で確認したブランチです（このリポジトリの例では、その issue の milestone の release ブランチ）。

**jj の場合**

```sh
jj git fetch
jj workspace add ../ws/issue-<N> -r release/vX.Y.Z@origin
cd ../ws/issue-<N>
```

**git の場合**

```sh
git fetch origin
git worktree add -b <type>/issue-<N>-<slug> ../ws/issue-<N> origin/release/vX.Y.Z
cd ../ws/issue-<N>
```

- ディレクトリ名に空白を入れないでください（シェルの単語分割で壊れます）。
- 他のワーカーの作業ディレクトリと、リードのメインの作業ディレクトリには触れません。

### 4.2 宣言してから実装する

**jj の場合:** 実装の前に、やることを change の説明に書きます。方針が変わったら書き直します。

```sh
jj describe -m "fix: <やること>"
```

**git の場合:** `jj describe` に当たるものはありません。ブランチ名に issue 番号と目的を入れ、最初のコミットのメッセージに目的を書きます。

コミットメッセージには `fix:` / `feat:` / `docs:` / `chore:` / `ci:` の接頭辞を付けます。

### 4.3 実装と検証

1. issue を読み、**その issue の目的だけ**を実装します。隣の issue の修正が混ざりそうなら、別の変更に切り出します。
2. 修正前に失敗し、修正後に通るテストを追加します。**修正前の挙動を確かめるときは、変更を戻す操作を使います。`git stash` のような、状態を一時退避する操作は避けます**（jj では `jj restore` やスナップショットからの復元に頼ると、変更を失いかけます。変更を戻す前に、必ず別の change に保存してください）。
3. 挙動が変わる場合は、ドキュメントと、あれば日本語版（`*_jp.md`）を更新します。
4. `CHANGELOG.md` の `[Unreleased]` に、利用者に見える変更を 1 項目として書きます。既存の挙動に依存するコードに影響しうる場合は、"may affect existing code" の注記を付けます。ほかのワーカーも同じ節を編集するので、項目は独立した 1 つの bullet にします。
5. `go vet ./...` と `go test -race ./...` を通します。実際の出力を報告します。

### 4.4 PR を出す

**jj の場合**

```sh
jj bookmark set <type>/issue-<N>-<slug> -r @
jj git push --bookmark <type>/issue-<N>-<slug>
```

**git の場合**

```sh
git push -u origin <type>/issue-<N>-<slug>
```

どちらも共通です。

```sh
gh pr create --base release/vX.Y.Z --head <type>/issue-<N>-<slug> \
  --title "fix: ..." --body-file <本文ファイル>
gh pr edit <PR番号> --add-assignee @me
```

- 本文は `.github/pull_request_template.md` に沿って書きます（What and why、`Closes #N`、チェックリスト）。
- **PR は、§3.0 で確認したベースブランチ向けです。** 最初から正しい向きで作ります。このリポジトリの例（release ブランチ向け）では、デフォルトブランチ向けの PR とは、issue の自動リンクの挙動が違います（§7）。
- PR を作ったら、リードに「PR 準備完了」と PR の URL を報告します。マージはしません。

### 4.5 レビューへの対応

指摘を 1 件ずつ、次のどれかで返します。

- **受け入れる:** 修正して push します。
- **反論する:** 理由を書きます。
- **質問する:** 不明な点を聞きます。

対応が終わったら、PR に `Worker confirmation: <何を受け入れ、何を直したか。直す必要がなければ "no changes needed">` で始まるコメントを投稿し、レビュアーとリードに知らせます。PR の本文とタイトルも、直した内容に合わせて更新します。

### 4.6 rebase の依頼を受けたとき

別の PR がマージされると、同じ `CHANGELOG.md` の箇所で衝突します。リードに頼まれたときだけ rebase します。

**jj の場合**

```sh
jj git fetch
jj rebase -b <bookmark> -d release/vX.Y.Z@origin
# 衝突を解消したあと
jj git push --bookmark <bookmark>
```

**git の場合**

```sh
git fetch origin
git rebase origin/release/vX.Y.Z
# 衝突を解消し、git rebase --continue
git push --force-with-lease
```

- 解消してよいのは `CHANGELOG.md` だけです。既存の項目をすべて残し、自分の項目を同じ見出しの下に足します。
- **コードやテスト、ドキュメントが衝突したら、自分で解消せずにリードに報告します。**
- rebase 後に `go vet ./...` と `go test -race ./...` を実行します。
- `Rebased: <どのコミットの上に載せたか。どのファイルが衝突したか。コードは変えていない>` で始まるコメントを PR に投稿します。

## 5. レビュアーの手順

指示の雛形は §9 にあります。

1. PR の diff を一次情報にします。ローカルのチェックアウトは別のコミットを指していることがあるので、テストを動かすときはコピーを作って、リポジトリの外で行います。
2. 見る点: 正しさ、既存の挙動の退行、並行処理、境界値、テストが修正前に失敗するか、ドキュメントの日英の整合、CHANGELOG の記述、スコープの逸脱。パッチリリースに入れる規模かも判断します。
3. 指摘は **must-fix / suggestion / question** に分けます。指摘がなければ、そう書きます。無理に作りません。
4. PR に 1 件のコメントを投稿します。1 行目は `Review verdict: approve` または `Review verdict: changes requested` です。
5. ワーカーに知らせ、`Worker confirmation:` コメントを頼みます。往復は最大 2 回です。ワーカーの反論が妥当なら、指摘の取り下げを PR に書きます。
6. 合意したら、`Consensus: reviewer and worker agree, ready to merge` を投稿します。**ただし次の場合は投稿しません。**
   - CI が赤い、またはまだ終わっていない（実行中は赤ではありませんが、全緑を待ちます）。
   - 未解決の must-fix がある。
   - 2 往復で合意できない。この場合は、双方の主張をリードに報告します。
7. ワーカーが rebase したときは、前にレビューした head との差分が `CHANGELOG.md` だけであることを確認し、リードに「機械的」と報告します。

### レビュアーのモデルの選び方

- 数行で影響範囲の狭い PR（例: ロックの範囲を 1 か所変えるだけ）は、軽量なモデルで足ります。
- ロジック、並行処理、浮動小数点、外部コマンドの実行、CI の検査など、見落としの影響が大きいものは、最も高性能なモデルを使います。迷ったら高性能なほうにします。

## 6. リードのマージ手順

PR 1 件ごとに、次の条件をすべて**自分で**確認します。レビュアーの報告だけを根拠にしません。

| # | 確認項目 | 方法 |
|---|---|---|
| 1 | レビュアーの `Consensus:` コメントがある | `gh pr view <N> --json comments` |
| 2 | ワーカーの `Worker confirmation:` コメントがある | 同上 |
| 3 | 未解決の must-fix がない | レビューのコメントを読む |
| 4 | **現在の head** の CI がすべて成功 | `gh pr checks <N>` |
| 5 | `mergeable` が `MERGEABLE` | `gh pr view <N> --json mergeable` |
| 6 | rebase 後なら、コードが変わっていない | 下の「rebase が機械的かの確認」 |

**承認は、§3.0 で決めた承認者から取ります。**

- 承認者が**人間**の場合: 条件がそろったら、マージの前に、PR 番号を示して確認します。承認が得られるまでマージしません。承認は PR ごとで、前の PR の承認は、次の PR には及びません。
- 承認者が**レビュアーの `Consensus:` コメント**の場合: 上の 1〜6 がそろえば、承認済みです。人間には聞きません。ただし 1〜6 は、リードが**自分で**確認します。

マージ方式は、リポジトリの慣習に合わせます（このリポジトリではマージコミット）。

```sh
gh pr merge <N> --merge            # ブランチ保護で通らない場合は --admin（可否は §3.0 で確認済みのとき）
gh issue close <issue> --reason completed --comment "Fixed by #<N>, merged into \`release/vX.Y.Z\`."
```

マージしたあとの後片付け:

1. その issue を閉じます（§7: 自動では閉じない場合があります）。
2. そのワーカーとレビュアーを停止します。
3. ワーカーが使った workspace / worktree を片づけます（jj: `jj workspace forget <name>` とディレクトリの削除、git: `git worktree remove`）。
4. 残りの PR の `mergeable` を全部確認します（直後は `UNKNOWN` のことがあるので、何度か見直します）。
5. 衝突している PR のうち、**次にマージする 1 件にだけ** rebase を依頼します。全員に頼むと、次のマージで全員が再び衝突します。

### rebase が機械的かの確認

レビューした head と rebase 後の head で、`CHANGELOG.md` 以外の変更ファイルの内容が同じであることを確かめます。

```sh
# ファイルごとの blob ハッシュを比べる（同じなら内容が同じ）
gh api "repos/<owner>/<repo>/contents/<file>?ref=<旧head>" --jq .sha
gh api "repos/<owner>/<repo>/contents/<file>?ref=<新head>" --jq .sha
```

git の場合は、`git range-diff <base>..<旧head> <base>..<新head>` で、パッチの差分を比べる方法もあります。jj の場合は `jj interdiff --from <旧> --to <新>` が使えます。`CHANGELOG.md` は、既存の項目が残っていることを、内容で確認します。

### CI が止まった、または落ちたとき

- 実行がキュー待ちで止まっている: `gh run cancel <run>` のあとに `gh run rerun <run> --failed`。完了していない run は、再実行できません。
- 失敗の原因が、PR の変更と無関係かを、ログで確認します。同じコミットが他の環境で通っているか、失敗したテストが PR の対象外か、を見ます。無関係でも、**再実行して緑になった**ことを確かめてから進めます。
- 時間に依存して落ちるテストは、フレーキーテストの issue に証拠（テスト名、環境、再実行の結果）を残します。

## 7. 落とし穴（実際に起きたこと）

| 事象 | 原因と対処 |
|---|---|
| release ブランチ向けの PR で、`Closes #N` の issue が閉じない | GitHub は、デフォルトブランチ向けの PR にしか closing 参照を作りません。`gh issue close` で手動で閉じます。workflow で自動化するなら、PR 本文の `Closes #N` を直接解析する必要があります。 |
| 最初に `main` 向けで作った PR を、あとから release ブランチに付け替えた | `gh pr edit <N> --base release/vX.Y.Z`。ただし最初から正しい向きで作るほうが安全です。 |
| ブランチ保護で「承認レビュー必須」、かつ作者が同じアカウント | AI レビュアーのコメントは GitHub の承認として数えられません。`--admin` でのマージが必要になります。承認者を §3.0 で決めておきます。 |
| 衝突している PR で、push しても CI が走らない | GitHub は、衝突している PR では `pull_request` のワークフローを走らせません。レビュアーが「CI を待つ」だけでは、いつまでも終わりません。次にマージする PR を rebase して衝突を解消します。 |
| ワーカーが「テストだけ追加した」と報告したが、実装に変異（テストが落ちることを確かめるための改変）が残っていた | リードが、レビュー済みの head と新しい head で、ファイルごとの内容のハッシュを比べ、変更が予期したファイルだけかを自分で確認します。変異による確認は、push する変更の外の使い捨てコピーで行わせます。 |
| 1 件マージするたびに、残りの PR が `CHANGELOG.md` で衝突する | 次にマージする 1 件にだけ rebase を依頼します（§6）。 |
| レビュアーが CI の完了前に `Consensus:` を投稿した | 報告を鵜呑みにせず、リードが自分で CI を確認します。 |
| レビュアーが 2 回目の `Consensus:` を足した | 古いものは「早すぎた」と明記させます。最後のものを正とします。 |
| ワーカーが「修正前の挙動を確かめる」ために変更を一時退避して、状態を壊しかけた | §4.3 の注意を、指示に必ず書きます。 |
| ワーカーが `main` 宛てにメッセージを送って届かなかった | 返信先の名前を指示に書きます（例: `team-lead`）。 |
| `go.sum` が release ブランチ上で tidy でなかった | 新しい CI 検査が見つけ、その PR に修正が含まれました。 |
| 浮動小数点の往復、アーキテクチャ依存の変換 | arm64 で通っても amd64 で壊れることがあります。CI の amd64 の結果で確かめます。 |
| 外部コマンドを実行する機能が、新しい toolchain で panic した | 回復処理（`recover`）とフォールバックを必ず付けます。 |

## 8. チームモードのないツールで使う: tmux を CLI で操作する方式

チームモード（名前を付けてエージェントを起動し、互いにメッセージを送れる機能）を持つツールなら、その機能で足ります。**多くの AI コーディングツールには、そのようなチームモードがありません。** その場合は、**ワーカーとレビュアーを、tmux の別ウィンドウで動く独立した CLI セッションとして起動し、リードが tmux のコマンドで操作します。** 手順（§1〜§7）は変わらず、「起動する」「指示を送る」「報告を受ける」「止める」の手段だけが置き換わります。

> この節の tmux の操作は、手順としてまとめたもので、実際に通して動かした記録はまだありません。使う tmux とツールの版で、細部（貼り付けの扱い、待機状態の見分け方）を確かめてください。

### 8.1 対応表

| やりたいこと | チームモード | tmux 方式 |
|---|---|---|
| 起動 | 名前を付けてエージェントを起動 | 名前を付けたウィンドウで CLI を起動 |
| 指示を送る | メッセージ送信 | `send-keys` / `paste-buffer` |
| 報告を受ける | 通知が届く | ステータスファイル、`capture-pane`、PR のコメント |
| ワーカーとレビュアーの相談 | 直接メッセージ | **PR のコメントを正とし、リードが相手のウィンドウに「PR #N のコメントを読んで返信して」と送って中継する** |
| 停止 | エージェントを停止 | CLI を終了させ、ウィンドウを閉じる |

### 8.2 準備

```sh
S=jotaro                          # tmux のセッション名
STATUS=/tmp/jotaro-status         # ステータスファイルの置き場所（リポジトリの外）
mkdir -p "$STATUS"
tmux new-session -d -s "$S" -n lead
```

- **1 エージェント = 1 ウィンドウ**で、ウィンドウ名をそのまま宛先にします（`worker-81`、`review-161` など）。名前と対象の PR、作業ディレクトリは、リードが表にして持ちます。
- 作業ディレクトリは、§4.1 の workspace / worktree をそのまま使い、ウィンドウを開くときに `-c` で指定します。

### 8.3 起動

```sh
tmux new-window -t "$S" -n worker-81 -c "$PWD/../ws/issue-81"
tmux send-keys -t "$S:worker-81" '<ツールの起動コマンド>' Enter
```

- 起動コマンドは、ツールごとに違います。権限の確認で止まらないようにする設定と、モデルの指定（§5: レビュアーのモデルの選び方）も、ここで渡します。
- **権限は、そのウィンドウの作業ディレクトリと、必要なコマンド（`go`、`gh`、`jj` か `git`）だけに絞ります。** 確認を全部飛ばす設定を、リポジトリの外のファイルやほかのワーカーの領域に及ぶ形で使ってはいけません。確認で止まる場合は、`capture-pane` で気づいて、リードが答えます。
- ウィンドウの出力は、後で見返せるように記録しておきます。

  ```sh
  tmux pipe-pane -o -t "$S:worker-81" "cat >> $STATUS/worker-81.log"
  ```

### 8.4 指示を送る

§9 の雛形は複数行なので、`send-keys` ではなくバッファ経由で貼り付けます。

```sh
cat > "$STATUS/worker-81.prompt" <<'EOF'
<§9 の雛形に、issue 番号などを入れたもの>
EOF
tmux load-buffer -b p81 "$STATUS/worker-81.prompt"
tmux paste-buffer -p -b p81 -t "$S:worker-81"      # -p: ツールが対応していれば bracketed paste で貼る
tmux send-keys -t "$S:worker-81" Enter              # 貼り付けのあとに、別に Enter を送る
```

- 貼り付けと Enter を、同じ `send-keys` にまとめないでください。入力が途中で確定することがあります。
- 短い連絡（「PR #<N> のレビューコメントを読んで返信して」など）は、`send-keys -l '<文>'` に続けて `Enter` で足ります。`-l` は、文をキー名として解釈させないための指定です。

### 8.5 報告を受ける

**ワーカーに、完了したらステータスファイルを書かせます。** 指示（§9）に次の 1 行を足します。

```
When you are done (PR created, or blocked), write one line to <STATUS>/<your name>.done: the PR URL, or "BLOCKED: <reason>".
```

リードは、ファイルの有無と、画面の末尾で確認します。

```sh
cat "$STATUS"/*.done 2>/dev/null
tmux capture-pane -p -t "$S:worker-81" -S -40     # 直近 40 行
```

- 待機中かの判定は、ツールの入力待ちの表示が、画面の末尾に出ているかで行います。ツールごとに違うので、最初に 1 回、実際の表示を見て決めます。
- ファイルの代わりに `tmux wait-for` を使う手もあります（ワーカーが `tmux wait-for -S done-81` を実行し、リードが `tmux wait-for done-81` で待つ）。使う tmux の版で、先に送った信号が保持されるかを確かめてください。
- **ポーリングのループを回し続けません。** 完了を待つときは、まとめて 1 回だけ確認し、次の確認までは別の仕事（PR と CI の確認など）をします。

### 8.6 ワーカーとレビュアーの相談

エージェント同士は、直接話せません。次の流れにします。

1. レビュアーが、レビューを PR のコメントに投稿します（§5）。
2. レビュアーが、`$STATUS/review-<N>.done` に「ワーカーへの依頼あり」と書くか、画面に出します。リードが気づいて、ワーカーのウィンドウに「PR #<N> のレビューコメントを読んで、§4.5 のとおりに返信して」と送ります。
3. ワーカーが修正し、`Worker confirmation:` コメントを投稿します。リードがレビュアーに「ワーカーが返信した。確認して」と送ります。
4. 最大 2 往復です。それを超えたら、リードが両方の主張を読んで判断します。

結論は、PR のコメントに残るので、ツールが違っても経緯を追えます。**接頭辞（`Review verdict:`、`Worker confirmation:`、`Consensus:`、`Rebased:`）は、PR のコメントだけで状態を判断するための目印です。必ず守らせます。** 全員が同じ GitHub アカウントで投稿するので、投稿者では見分けられません。

### 8.7 停止と後片付け

```sh
tmux send-keys -t "$S:worker-81" C-c             # 実行中の作業を止める（ツールによっては /exit などの終了コマンド）
tmux kill-window -t "$S:worker-81"
rm -f "$STATUS/worker-81."{done,prompt}
```

マージと issue のクローズのあと（§6）に、そのワーカーとレビュアーのウィンドウを閉じ、workspace / worktree を片づけます。全部終わったら `tmux kill-session -t "$S"` です。

### 8.8 一回きりの実行モードとの使い分け

多くのツールには、1 回の指示で結果を返して終わる実行モード（非対話のモード）があります。**レビュアーの初回レビューのように、1 回で終わる仕事には向きます。** ただし、ワーカーのレビュー対応や rebase のように、「あとから同じ文脈で追加の依頼をする」仕事には、セッションが残る対話モード（このセクションの方式）のほうが向いています。

### 8.9 人間だけで回す場合

ワーカー、レビュアー、リードを、それぞれ人間が担当します。tmux は要りません。手順（§1〜§7）は同じで、PR のコメントの接頭辞（§8.6）も、そのまま使います。

## 9. 指示の雛形（ツールに貼り付ける用）

`<...>`、`vX.Y.Z`、`release/vX.Y.Z` は、§3.0 の回答で置き換えます。`gh` と GitHub の言葉（issue、PR）は、タスクの出どころや PR のホストが違えば、対応するものに読み替えます。

### ワーカー

```
You are a worker on <repository>. Your single task: <fix | implement> GitHub issue #<N> (<title>).
Read it first: `gh issue view <N>`. Area: <files/packages>.

Working directory: <path to your own workspace/worktree, already created on top of release/vX.Y.Z>.
Work ONLY there; never touch the main checkout or other workers' directories.

Rules:
- Version control: <jj | git>. <If jj: never use git commands; run `jj describe -m "<type>: <what you will do>"` BEFORE editing.>
  Messages, comments, godoc and PR text are in English with a prefix (fix:/feat:/docs:/chore:/ci:).
- One purpose only: this issue. Do not fix neighbouring issues (<list the other issues in the same area>).
- Tests that fail before the fix and pass after. To check the pre-fix behaviour, restore the old code by a
  VCS operation that keeps your work safe; never use `git stash` and never discard uncommitted work.
- Update docs (and the Japanese counterpart if one exists) when behaviour changes.
- Add one independent bullet to CHANGELOG.md [Unreleased]; add a "may affect existing code" note when behaviour changes.
- Design decisions already made (apply if relevant): <list>.
- Before pushing: `go vet ./...` and `go test -race ./...` must pass; report real output.
- Publish: push a branch named <type>/issue-<N>-<slug>, then `gh pr create --base release/vX.Y.Z` with a body
  that follows .github/pull_request_template.md and contains `Closes #<N>`; then `gh pr edit <PR> --add-assignee @me`.
- Do NOT merge. Do not create other PRs.

Final report (short): PR URL, what changed, test output summary, decisions the lead should know.
You stay available: a reviewer may comment on the PR. Answer each point (accept / push back with reasons / ask),
post a PR comment starting with "Worker confirmation:", at most 2 rounds; then the lead decides.
Report to the lead as `<lead name>`.
```

### レビュアー

```
You are the reviewer for PR #<N> of <repository> (base release/vX.Y.Z). It <what it does>.
You have no context from the implementation; judge the change on its merits.

READ-ONLY: do not modify files, push or merge. Use `gh pr view/diff/checks <N>` and `gh issue view <issue>`.
The local checkout may be on a different commit; the PR diff is the source of truth. Run tests only in a
scratch copy outside the repository.

Pay particular attention to: <points specific to this PR>.
Classify findings as must-fix / suggestion / question. Do not invent suggestions if there is nothing to fix.

Post ONE comment: `gh pr comment <N> --body ...`, starting with "Review verdict: approve" or
"Review verdict: changes requested".

Consensus step: the author is `<worker name>`. Message it that your review is posted and ask for a
"Worker confirmation:" PR comment (or list what to fix). At most 2 rounds. If the worker pushes back with a good
reason, retract the finding in a new PR comment. When both agree, the worker's confirmation exists, CI is fully
green (not just "not red") and no must-fix is open, post: `Consensus: reviewer and worker agree, ready to merge`.
If there is no agreement after 2 rounds, report both positions to `<lead name>` and do NOT post a consensus comment.
If a rebase happens later, check that only CHANGELOG.md changed relative to the head you reviewed and tell the lead.
```
