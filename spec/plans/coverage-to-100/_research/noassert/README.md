# noassert

Standalone research script (`main.go.txt` — kept as reference text, not
`.go`, so this module's build/vet/coverage tooling never sees it) used by
the coverage-to-100 research to find test functions with no assertion call
(`Error`, `Errorf`, `Fatal`, `Fatalf`, `Fail`, `FailNow`), by walking Go AST
across a target tree. Rename it to `main.go` and `go run main.go <path>` to
reproduce; kept here as evidence for the assertion-free test rate cited in
[`../REPORT.md`](../REPORT.md).

## Open Questions

None at this time.
