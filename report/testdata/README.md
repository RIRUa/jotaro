# Fixtures of the result format

Stored results of older versions that `ReadJSON` must keep reading (`compat_test.go`), and the golden
file that pins what `WriteJSON` emits today. They are data, not output to be regenerated: do not
rewrite an old fixture with the current code, that would defeat the purpose.

All of them are one run of the same small scenario (`browse`: steps `list`, `order` and `health`; the
last one always gets a 500 and fails its check), 2 users for 300 ms against an `httptest` server,
written by the program in the section "How they were produced". The numbers differ between files
because every run measures its own latencies.

| File | `version` | Produced by |
| --- | --- | --- |
| `v1-derived.json` | 1 | Derived from `v2-3f79071.json`: `version` set to 1, `started_at` and every `buckets` removed, as `docs/result.md` describes version 1. No release and no commit in the history of `main` wrote version 1 (the history is squashed per pull request), so this one is not a real output. |
| `v2-3f79071.json` | 2 | Real output of the code at commit `3f790719fbde` (the squash commit of #4, `JSONVersion = 2`, before the first release), built as a module dependency `github.com/RIRUa/jotaro@3f790719fbde…`. |
| `v3-v0.1.0.json`, `v3-v0.1.2.json`, `v3-v0.1.3.json` | 3 | Real output of the released versions, built as a module dependency `github.com/RIRUa/jotaro@v0.1.0` / `@v0.1.2` / `@v0.1.3`. v0.1.1 has the same format as v0.1.0. v0.1.0 computes percentiles exactly and v0.1.2 and later aggregate incrementally (within 1%); the file format is the same. |
| `golden-current.json` | current | `WriteJSON` of the fixed report in `goldenReport()` (`compat_test.go`). Rewritten by `go test ./report -run TestWriteJSONGolden -update`. |

The raw data of the same runs is in `../../record/testdata` (`raw-v0.1.0.jsonl` and so on), and
`compat_raw_test.go` checks that `FromRaw` on it gives the same counts, errors and time series as
the JSON of the same run.

## How they were produced

In a scratch directory outside the repository, a module with `go get github.com/RIRUa/jotaro@<version>`
(the module proxy serves the tagged source) and this `main.go`; it writes `result.json` and
`raw.jsonl`:

```go
srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
	switch r.URL.Path {
	case "/items":
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `[{"id":1},{"id":2}]`)
	case "/orders":
		w.WriteHeader(http.StatusCreated)
	default:
		http.Error(w, "boom", http.StatusInternalServerError)
	}
}))
sc := jotaro.NewScenario("browse", func(ctx context.Context, u *jotaro.User) error {
	if err := u.Step("list", func(s *jotaro.Step) error { return s.Get("/items").Do().ExpectStatus(200) }); err != nil {
		return err
	}
	if err := u.Step("order", func(s *jotaro.Step) error {
		return s.Post("/orders").JSON(map[string]int{"item": 1}).Do().ExpectStatus(201)
	}); err != nil {
		return err
	}
	u.Think(80 * time.Millisecond)
	return u.Step("health", func(s *jotaro.Step) error { return s.Get("/health").Do().ExpectStatus(200) })
})
rep, _ := jotaro.Run(ctx, jotaro.Plan{Scenario: sc, BaseURL: srv.URL, Users: 2, Duration: 300 * time.Millisecond, Raw: rawFile})
rep.WriteJSON(resultFile)
```

(The commit of version 2 has no `Plan.Raw`, so that run writes the result only.)

## When the format changes

When `JSONVersion` is raised, or a version-3 field is added or changed in a way readers can see:

1. Produce a fixture of the new version with the released code (or the release candidate) as above and
   commit it as `v<N>-<tag>.json`; add it to `jsonFixtures` in `compat_test.go`. `TestFixtureVersionsCoverTheCurrentOne`
   fails until there is a fixture of every version from 1 to `JSONVersion`.
2. Update `docs/result.md` and `docs/result_jp.md`.
3. Run `go test ./report -run TestWriteJSONGolden -update` and commit the new `golden-current.json`, so the
   change of the emitted shape is a diff in the review.
