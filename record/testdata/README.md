# Fixtures of the raw data format

Stored raw data (JSON Lines) of released versions that `ReadRaw` must keep reading
(`compat_test.go`), and the golden file that pins what `WriteRaw` emits today. They are data, not
output to be regenerated: do not rewrite an old fixture with the current code.

| File | Produced by |
| --- | --- |
| `raw-v0.1.0.jsonl`, `raw-v0.1.2.jsonl`, `raw-v0.1.3.jsonl` | Real `Plan.Raw` output of the released versions (`github.com/RIRUa/jotaro@v0.1.0` / `@v0.1.2` / `@v0.1.3` as a module dependency in a scratch directory), from the same run that wrote `../../report/testdata/v3-<tag>.json`. See `../../report/testdata/README.md` for the scenario and the program. v0.1.0 and v0.1.2 wrote the whole file at the end of the run; v0.1.3 streams it and so has a second `meta` line with the final elapsed time. Every release writes `"version":1`; v0.1.1 has the same format as v0.1.0. |
| `golden-current.jsonl` | `WriteRaw` of the fixed run in `goldenRaw()` (`compat_test.go`). Rewritten by `go test ./record -run TestWriteRawGolden -update`. |

`RawVersion` is still 1. When it is raised, add a fixture of the new version as above, update
`docs/result.md` and `docs/result_jp.md`, and run `go test ./record -run TestWriteRawGolden -update`.
