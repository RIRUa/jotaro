# Contributing

Thanks for your interest in jotaro! Bug reports, ideas and pull requests are all welcome.

## Before you start

- For anything larger than a small fix, please open an issue first so we can agree on the direction. [`DESIGN.md`](DESIGN.md) explains why things are the way they are.
- jotaro is pre-1.0 (v0.x). The API may change between minor versions.
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

The Markdown documents are checked by Go tests in [`internal/doccheck`](internal/doccheck). Run them with `scripts/check-docs.sh` (CI runs the same script in the `docs` workflow; a plain `go test ./...` runs them too, apart from the steps that need the `go` tool to build something). Everything is offline.

**English/Japanese parity.** Every English document has a Japanese twin named `<name>_jp.md` next to it, and the two must have the same structure: the same number and order (levels) of headings, the same number of fenced code blocks with the same language, and the same code in them. Comments are not compared (`// ...` in `go` blocks, `# ...` in `sh` blocks), because they are translated, and neither are blank lines. A block without a language, such as a diagram or a usage line, has translated words in it, so only its number of lines is compared. A twin is required for `README.md`, `DESIGN.md` and every Markdown file below `docs/` and `examples/`, so a new guide such as `docs/foo.md` needs `docs/foo_jp.md`. A `_jp.md` file without an English one fails too. The other root documents (this one, `CHANGELOG.md`, ...) are English only.

If you can only write the English version, add a line to [`internal/doccheck/parity_allowlist.txt`](internal/doccheck/parity_allowlist.txt):

```text
docs/foo.md | twin | Japanese version follows in #123
```

The second field is the check that is allowed to fail (`twin`, `headings`, `blocks` or `code`) and the third is the reason, which is required. An entry that no longer matches a difference fails the test, so remove it when you bring the Japanese document in line.

## Pull requests

- Keep one purpose per pull request.
- Use a short prefix in the title: `feat:` / `fix:` / `docs:` / `chore:` / `ci:`.
- CI (vet, test with `-race` on Go 1.25 and stable, golangci-lint, and the generated-files check) must pass. The `docs` workflow (documentation checks) is not a required check, but please keep it green. The run on Linux with the latest Go also puts the statement coverage per package in the job summary; please look at it if you change code that was well covered, and add tests when the number drops. Pull requests that push new commits cancel the previous CI run of the same pull request.

## Releasing (maintainers)

Releases are cut by merging a `release/vX.Y.Z` branch into `main`.

1. Create the branch `release/vX.Y.Z` from the latest `main`.
2. Run `scripts/prepare-release.sh X.Y.Z`. It turns `[Unreleased]` in `CHANGELOG.md` into `[X.Y.Z] - <today>`. Review the entries and commit.
3. Open a pull request to `main` with the `ci:`/`chore:` prefix and wait for CI to pass.
4. Merge it. [`release.yml`](.github/workflows/release.yml) then tags the merge commit `vX.Y.Z`, builds the binaries with GoReleaser and publishes a GitHub Release. The release notes are the `[X.Y.Z]` section of `CHANGELOG.md`; the workflow fails if that section is missing or empty, or if the tag already exists on another commit.

If publishing fails after the tag was pushed, fix the cause and run the `release` workflow from the Actions tab (`workflow_dispatch` on `main`) with the same version. It reuses the existing tag. The same run attaches build provenance attestations (`actions/attest-build-provenance`) to the archives and `checksums.txt`. If only that last step failed, the assets are already uploaded and a plain re-run fails earlier, because GoReleaser does not replace assets that exist (`release.replace_existing_artifacts` is not set). Delete the assets of that GitHub Release (or the release itself, keeping the tag) first, then run the workflow again with the same version.

The GoReleaser version and the `govulncheck` version are pinned in the workflows, and every action is pinned to a commit SHA with its version in a trailing comment (Dependabot keeps those up to date). When you bump GoReleaser or govulncheck by hand, change the version in `release.yml` / `vulncheck.yml`.

## License

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
