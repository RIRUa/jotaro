# jotaro

## バージョン管理

- バージョン管理操作には必ず Jujutsu (`jj`) を使う。`git` コマンドは使わない（`.claude/settings.json` で deny 済み）。
- このリポジトリは colocated（`.git` と `.jj` が共存）。GitHub との同期は `jj git fetch` / `jj git push` を使う。
- 主な対応:
  - 状態確認: `jj st` / `jj log` / `jj diff`
  - 最新取得: `jj git fetch` のあと、必要なら `jj new main`（または `jj rebase`）
  - 作業の宣言: `jj describe -m "..."`（空の変更に書く）/ `jj new -m "..."`（変更を分ける）
  - 作業の区切り: `jj new`
  - プッシュ: `jj bookmark set <name> -r @-` → `jj git push`
- PR 作成などの GitHub 操作は `gh` を使ってよい。

## 作業の進め方（jj）

jj は作業コピーそのものが常にコミットなので、**作業を終えてからメッセージを付けるのではなく、始める前に「何をするか」を宣言する**。

1. 着手前に `jj describe -m "<やること>"` を実行する。今の変更が空で説明がなければそこに書く。すでに別の作業が入っていれば `jj new -m "<やること>"` で変更を分ける。
2. そのあとで実装する。方針が変わったら `jj describe` で書き直す。
3. 終わったら `jj new` で次の空の変更に進む。

- メッセージは日本語で、`feat:` / `fix:` / `docs:` / `chore:` などの接頭辞を付ける。
- 1 つの変更には 1 つの目的だけを入れる。宣言と関係ない修正が混ざりそうなら、先に `jj new -m` で分ける。
