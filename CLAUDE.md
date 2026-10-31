# jotaro

## Language

- Reply in the same language the user used in their first message of the session. If the user switches language mid-session, follow the switch.
- Keep code identifiers, commands, file paths and error messages in their original form regardless of the reply language.
- Code comments, godoc, commit messages and PR descriptions are written in English, independent of the conversation language.

## Version control

- Prefer Jujutsu (`jj`) for version control operations. Do not use `git` commands. In environments where `jj` is not installed (e.g. Cloud), `git` is allowed (it is not denied).
- This repository is colocated (`.git` and `.jj` coexist). Use `jj git fetch` / `jj git push` to sync with GitHub.
- Common operations:
  - Check state: `jj st` / `jj log` / `jj diff`
  - Get the latest: `jj git fetch`, then `jj new main` (or `jj rebase`) if needed
  - Declare work: `jj describe -m "..."` (write it on an empty change) / `jj new -m "..."` (split into a separate change)
  - Finish a unit of work: `jj new`
  - Push: `jj bookmark set <name> -r @-` → `jj git push`
- `gh` may be used for GitHub operations such as creating PRs.

## Workflow (jj)

In jj the working copy is always a commit, so **declare what you are about to do before you start, instead of writing the message after you finish**.

1. Before starting, run `jj describe -m "<what you will do>"`. If the current change is empty and has no description, write it there. If it already holds other work, split with `jj new -m "<what you will do>"`.
2. Then implement. If the approach changes, rewrite the description with `jj describe`.
3. When done, move on to the next empty change with `jj new`.

- Write messages in English (see Language above) with a prefix such as `feat:` / `fix:` / `docs:` / `chore:`.
- Put only one purpose in each change. If an unrelated fix is about to creep in, split it off first with `jj new -m`.
