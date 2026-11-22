# Contributing

Thanks for your interest in jotaro! Bug reports, ideas and pull requests are all welcome.

## Before you start

- For anything larger than a small fix, please open an issue first so we can agree on the direction. [`DESIGN.md`](DESIGN.md) explains why things are the way they are.
- jotaro is pre-1.0 (v0.x), but we do not change the API without a reason (see the README). Pull requests are checked for that, see [Compatibility checks](#compatibility-checks).
- Please follow the [Code of Conduct](CODE_OF_CONDUCT.md). Security problems go through [`SECURITY.md`](SECURITY.md), not a public issue.

## Development

Requires Go 1.25 or later (the lowest version in `go.mod`).

```sh
go vet ./...
go test -race ./...
go test -run='^$' -bench=. -benchmem ./...   # benchmarks (not run in CI; timings on shared runners are noisy)
go test -run='^$' -fuzz=FuzzReadJSON -fuzztime=30s ./report   # fuzz one target (not run in CI; plain go test only runs the seed corpus)
golangci-lint run   # optional locally; CI runs it with the version pinned in .github/workflows/ci.yml
go test -coverprofile=coverage.out $(go list ./... | grep -v /examples/) && scripts/coverage-summary.sh   # optional: coverage per package
```

- Format with `gofmt`.
- Parsers of external input have fuzz targets: `FuzzParseThreshold` and `FuzzReadJSON` (`./report`), `FuzzReadRaw` (`./record`), `FuzzResponseJSON`, `FuzzLoadCSV`, `FuzzStagesAt` and `FuzzUserChanges` (the root package), `FuzzPercentile` (`./internal/histogram`). Run one with `-fuzz=<name>` as above. If the fuzzer finds a crasher, commit the file it writes under the package's `testdata/fuzz/<name>/` together with the fix.
- Add or update tests together with the change. Behaviour of the result format and the threshold syntax is documented in [`docs/result.md`](docs/result.md); update it when you change either.
- Exported identifiers need a doc comment in English. `golangci-lint` checks this (the `revive` exported rule) together with a few other linters listed in [`.golangci.yml`](.golangci.yml), and checks import grouping (`goimports`).
- When you add, remove or update a dependency, run `scripts/gen-third-party-notices.sh` and commit the updated `THIRD_PARTY_NOTICES.md`. Binaries that include a dependency must ship its license text, and the release archives include this file.
  CI checks that `go mod tidy` and the script leave the tree unchanged (the `generated` job), so a stale `go.mod`, `go.sum` or `THIRD_PARTY_NOTICES.md` fails the pull request.
- Dependabot does not regenerate `THIRD_PARTY_NOTICES.md`, so the `generated` job usually fails on its pull requests (whenever a module linked into the `jotaro` binary changes version). Check out the branch, run `go mod tidy` and `scripts/gen-third-party-notices.sh`, and push the result before merging.
- The README, `DESIGN.md`, `docs/result.md` and `examples/README.md` have Japanese counterparts (`*_jp.md`). If you can only update the English version, say so in the pull request and the Japanese one will be brought in line, and add the document to the allowlist described in [Documentation checks](#documentation-checks) so that CI stays green until then.

## Documentation checks

The Markdown documents are checked by Go tests in [`internal/doccheck`](internal/doccheck). Run them with `scripts/check-docs.sh` (CI runs the same script in the `docs` workflow). A plain `go test ./...` runs them too, apart from the step that builds the snippets, so the parity and link checks also run in the `test` jobs, which are required: a pull request that breaks them cannot be merged even though the `docs` workflow itself is not a required check. Everything is offline.

**English/Japanese parity.** Every English document has a Japanese twin named `<name>_jp.md` next to it, and the two must have the same structure: the same number and order (levels) of headings, the same number of fenced code blocks with the same language, and the same code in them. Comments are not compared (`// ...` in `go` blocks, `# ...` in `sh` blocks), because they are translated, and neither are blank lines. A block without a language, such as a diagram or a usage line, has translated words in it, so only its number of lines is compared. A twin is required for `README.md`, `DESIGN.md` and every Markdown file below `docs/` and `examples/`, so a new guide such as `docs/foo.md` needs `docs/foo_jp.md`. A `_jp.md` file without an English one fails too. The other root documents (this one, `CHANGELOG.md`, ...) are English only.

If you can only write the English version, add a line to [`internal/doccheck/parity_allowlist.txt`](internal/doccheck/parity_allowlist.txt):

```text
docs/foo.md | twin | Japanese version follows in #123
```

The second field is the check that is allowed to fail (`twin`, `headings`, `blocks` or `code`) and the third is the reason, which is required. An entry that no longer matches a difference fails the test, so remove it when you bring the Japanese document in line.

**Go snippets compile.** Every fenced `go` block in the Markdown documents (both languages) is built, not run, in a temporary module that replaces `github.com/RIRUa/jotaro` with your checkout, so a renamed method or a changed signature turns the check red. This step needs the `go` tool and the module cache, so a plain `go test ./...` skips it; `scripts/check-docs.sh` sets `DOCCHECK_COMPILE=1` and runs it. Most snippets are fragments, so each one is wrapped into a file of its own before it is built:

- A block that starts with `package` is built as it is. One that starts with `var`, `type`, `func` or `const` is a list of top-level declarations. Anything else is a list of statements inside a function that has `ctx context.Context`.
- The file imports `context`, `errors`, `fmt`, `io`, `log`, `net/http`, `os`, `time`, `jotaro`, `record` and `report`, and an unused import is fine. A variable that a fragment declares and does not use is fine too.
- Anything else a snippet relies on, such as the `rep` of an earlier snippet, has to be declared with a comment on the line before the block (several comments can be stacked, and the Japanese file needs the same ones):

```markdown
<!-- doccheck: before var rep *report.Report -->
<!-- doccheck: fields jotaro.Plan -->
<!-- doccheck: stmts -->
<!-- doccheck: skip: the declaration of report.Writer, quoted for reference -->
```

`before <Go code>` adds declarations the block needs (several can go on one line separated by `;`), `fields <Type>` says that the block is the body of a composite literal of that type, `stmts` says that a block starting with `var` is a list of statements, and `skip: <reason>` leaves the block out (the reason is required; use it for pseudo code). An unknown comment fails the check. The documents embedded in the MCP server (`internal/mcpserver/content/`) are not scanned, because they are served to models verbatim; `internal/mcpserver/guide_test.go` compiles the skeleton in the guide.

**Links.** Every relative link in every Markdown file (`[text](docs/result.md)`, `[text](#threshold-syntax)`, images, reference definitions, `href="..."`) has to point to a file or directory that exists, and a `#fragment` on a Markdown file has to match one of its headings (spelled as GitHub does: lower case, punctuation dropped, spaces turned into `-`, repeated headings numbered `-1`, `-2`) or an `<a id="...">`. Fragments on directories and source files (`#L10`) are not checked. Links with a scheme (`https:`, `mailto:`) are not followed, so a dead site never fails a pull request; run a link checker such as `lychee` by hand if you want to look for those.

## Pull requests

- Keep one purpose per pull request.
- Use a short prefix in the title: `feat:` / `fix:` / `docs:` / `chore:` / `ci:`.
- CI (vet, test with `-race` on Go 1.25 and stable, golangci-lint, and the generated-files check) must pass, and so should the `api-compat` check (see [Compatibility checks](#compatibility-checks)). The `docs` workflow (documentation checks) is not a required check, but please keep it green. The run on Linux with the latest Go also puts the statement coverage per package in the job summary; please look at it if you change code that was well covered, and add tests when the number drops. Pull requests that push new commits cancel the previous CI run of the same pull request.

## Compatibility checks

The README promises to keep existing code working, in the spirit of the Go 1 compatibility promise. CI checks the exported API by machine, so a break is a decision in a reviewed file and never an accident.

- **API diff** (the `api-compat` workflow, [`scripts/api-compat.sh`](scripts/api-compat.sh)): compares every public package (not `internal/`, `examples/` or `cmd/`) with the last release, the highest `vX.Y.Z` tag, using [`apidiff`](https://pkg.go.dev/golang.org/x/exp/cmd/apidiff) at the version pinned in the script. Additions (a new method, a new field) pass. An incompatible change fails the job: a removed or renamed identifier, a changed signature, a method added to an exported interface, a struct that stops being comparable, and so on. Run `scripts/api-compat.sh` locally to see the same report (it needs the release tags and network access for `go run`).
- **An intended break**: add one line per identifier to [`.github/api-breaks.txt`](.github/api-breaks.txt):

  ```
  v0.1.3 LoadCSV -- takes a second argument so that a repeated header can be allowed; callers add ", false"
  ```

  That is the tag the job compares with, the identifier as the report prints it before the first `: `, and the reason (required). Describe the break in `CHANGELOG.md` under "Changed" as well, with the note "This may affect existing code". The line is part of the pull request, so the reviewer sees the decision. Entries for older tags are ignored and stay as a history of what each release broke. An entry that matches no incompatible change only produces a warning. There is no label or other switch: the job cannot be passed by anything but this file.
- The job is not a required check, so a maintainer can still merge a red run on purpose, but the reason should then be in the pull request.
- **Stored results of old versions**: `report/testdata` and `record/testdata` hold results and raw data written by older versions (JSON `version` 1 to 3, raw data of v0.1.0, v0.1.2 and v0.1.3). `ReadJSON`, `ReadRaw`, `FromRaw` and `MergeReports` are tested against them, with the numbers that are stored in the files (`compat_test.go`), so a change to a field name or to the meaning of a field cannot pass just because the writer and the reader changed together. Never rewrite these files with the current code. How each one was produced is in the `README.md` next to them.
- **What WriteJSON and WriteRaw emit** is pinned by a golden file (`TestWriteJSONGolden`, `TestWriteRawGolden`). When one fails, the shape of the output changed: update `docs/result.md` (and `docs/result_jp.md`), raise `JSONVersion` / `RawVersion` if readers could break, add a fixture of the new version (the tests fail while a version has none), run `go test ./report -run TestWriteJSONGolden -update` (or `./record ... TestWriteRawGolden`) and commit the new golden file, so the change is a diff in the review.
- What counts as a break: for the Go API, what the API diff reports; for the JSON result and the raw data, removing or renaming a field, changing its unit or meaning, or making a file that an older version reads unreadable (adding a field is fine); for the threshold syntax, a string that was valid and means something else now or is rejected; for the CLI, removing or renaming a command or flag or changing an exit code; for the MCP tools (`jotaro mcp`), removing or renaming a tool, an input field or an output field, or changing what one means or its type (adding an optional input or an output field is fine). The API diff does not cover any of these but the first, so review them by hand, keep the docs in line and list a deliberate change in `CHANGELOG.md` under "Changed".

## Releasing (maintainers)

Releases are cut by merging a `release/vX.Y.Z` branch into `main`.

1. Create the branch `release/vX.Y.Z` from the latest `main`.
2. Run `scripts/prepare-release.sh X.Y.Z`. It turns `[Unreleased]` in `CHANGELOG.md` into `[X.Y.Z] - <today>`. Review the entries and commit.
3. Open a pull request to `main` with the `ci:`/`chore:` prefix and wait for CI to pass.
4. Merge it. [`release.yml`](.github/workflows/release.yml) then tags the merge commit `vX.Y.Z`, builds the binaries with GoReleaser and publishes a GitHub Release. The release notes are the `[X.Y.Z]` section of `CHANGELOG.md`; the workflow fails if that section is missing or empty, or if the tag already exists on another commit.
5. If the release raised `JSONVersion` or `RawVersion`, or added a field to the result or raw data, produce a result and a raw file with the released tag and commit them as fixtures (`report/testdata/v<N>-vX.Y.Z.json`, `record/testdata/raw-vX.Y.Z.jsonl`, how: `report/testdata/README.md`), so that every format a user may still have stays covered. The tests already fail until there is a fixture of every `version` up to `JSONVersion`.

If publishing fails after the tag was pushed, fix the cause and run the `release` workflow from the Actions tab (`workflow_dispatch` on `main`) with the same version. It reuses the existing tag. The same run attaches build provenance attestations (`actions/attest-build-provenance`) to the archives and `checksums.txt`. If only that last step failed, the assets are already uploaded and a plain re-run fails earlier, because GoReleaser does not replace assets that exist (`release.replace_existing_artifacts` is not set). Delete the assets of that GitHub Release (or the release itself, keeping the tag) first, then run the workflow again with the same version.

The GoReleaser version, the `govulncheck` version and the `apidiff` version are pinned in the workflows and scripts, and every action is pinned to a commit SHA with its version in a trailing comment (Dependabot keeps those up to date). When you bump GoReleaser, govulncheck or apidiff by hand, change the version in `release.yml` / `vulncheck.yml` / `scripts/api-compat.sh`.

## License

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
