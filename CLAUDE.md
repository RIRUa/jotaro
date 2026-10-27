# jotaro

## バージョン管理

- バージョン管理操作には必ず Jujutsu (`jj`) を使う。`git` コマンドは使わない（`.claude/settings.json` で deny 済み）。
- このリポジトリは colocated（`.git` と `.jj` が共存）。GitHub との同期は `jj git fetch` / `jj git push` を使う。
- 主な対応:
  - 状態確認: `jj st` / `jj log` / `jj diff`
  - 最新取得: `jj git fetch` のあと、必要なら `jj new main`（または `jj rebase`）
  - コミット: `jj describe -m "..."` → `jj new`
  - プッシュ: `jj bookmark set <name> -r @-` → `jj git push`
- PR 作成などの GitHub 操作は `gh` を使ってよい。
