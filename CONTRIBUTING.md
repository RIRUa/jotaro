# Contributing

Thanks for your interest in jotaro! Bug reports, ideas and pull requests are all welcome.

## Before you start

- For anything larger than a small fix, please open an issue first so we can agree on the direction. [`DESIGN.md`](DESIGN.md) explains why things are the way they are.
- jotaro is pre-1.0 (v0.x). The API may change between minor versions.

## Development

Requires Go 1.25 or later (the lowest version in `go.mod`).

```sh
go vet ./...
go test -race ./...
go test -run='^$' -bench=. -benchmem ./...   # benchmarks (not run in CI; timings on shared runners are noisy)
golangci-lint run   # optional locally; CI runs it with the version pinned in .github/workflows/ci.yml
```

- Format with `gofmt`.
- Add or update tests together with the change. Behaviour of the result format and the threshold syntax is documented in [`docs/result.md`](docs/result.md); update it when you change either.
- Exported identifiers need a doc comment in English.
- When you add, remove or update a dependency, run `scripts/gen-third-party-notices.sh` and commit the updated `THIRD_PARTY_NOTICES.md`. Binaries that include a dependency must ship its license text, and the release archives include this file.
- The README, `DESIGN.md` and `docs/result.md` have Japanese counterparts (`*_jp.md`). If you can only update the English version, say so in the pull request and the Japanese one will be brought in line.

## Pull requests

- Keep one purpose per pull request.
- Use a short prefix in the title: `feat:` / `fix:` / `docs:` / `chore:` / `ci:`.
- CI (vet, test with `-race` on Go 1.25 and stable, golangci-lint) must pass.

## Releasing (maintainers)

Releases are cut by merging a `release/vX.Y.Z` branch into `main`.

1. Create the branch `release/vX.Y.Z` from the latest `main`.
2. Run `scripts/prepare-release.sh X.Y.Z`. It turns `[Unreleased]` in `CHANGELOG.md` into `[X.Y.Z] - <today>`. Review the entries and commit.
3. Open a pull request to `main` with the `ci:`/`chore:` prefix and wait for CI to pass.
4. Merge it. [`release.yml`](.github/workflows/release.yml) then tags the merge commit `vX.Y.Z`, builds the binaries with GoReleaser and publishes a GitHub Release. The release notes are the `[X.Y.Z]` section of `CHANGELOG.md`; the workflow fails if that section is missing or empty, or if the tag already exists on another commit.

If publishing fails after the tag was pushed, fix the cause and run the `release` workflow from the Actions tab (`workflow_dispatch` on `main`) with the same version. It reuses the existing tag.

## License

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
