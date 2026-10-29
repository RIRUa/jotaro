# Contributing

Thanks for your interest in jotaro! Bug reports, ideas and pull requests are all welcome.

## Before you start

- For anything larger than a small fix, please open an issue first so we can agree on the direction. [`DESIGN.md`](DESIGN.md) explains why things are the way they are.
- jotaro is pre-1.0 (v0.x). The API may change between minor versions.

## Development

Requires Go 1.22 or later (the lowest version in `go.mod`).

```sh
go vet ./...
go test -race ./...
go test -run='^$' -bench=. -benchmem ./...   # benchmarks (not run in CI; timings on shared runners are noisy)
golangci-lint run   # optional locally; CI runs it with the version pinned in .github/workflows/ci.yml
```

- Format with `gofmt`.
- Add or update tests together with the change. Behaviour of the result format and the threshold syntax is documented in [`docs/result.md`](docs/result.md); update it when you change either.
- Exported identifiers need a doc comment in English.
- The README, `DESIGN.md` and `docs/result.md` have Japanese counterparts (`*_jp.md`). If you can only update the English version, say so in the pull request and the Japanese one will be brought in line.

## Pull requests

- Keep one purpose per pull request.
- Use a short prefix in the title: `feat:` / `fix:` / `docs:` / `chore:` / `ci:`.
- CI (vet, test with `-race` on Go 1.22 and stable, golangci-lint) must pass.

## License

By contributing you agree that your contribution is licensed under the [MIT License](LICENSE).
