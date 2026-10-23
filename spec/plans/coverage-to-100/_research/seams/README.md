# seams

Standalone research script (`main.go.txt` — kept as reference text, not
`.go`, so this module's build/vet/coverage tooling never sees it) used by
the coverage-to-100 research to count replaceable-function-variable seams,
injected function fields, interfaces, functions and over-size functions per
package, by walking Go AST across a target tree. Rename it to `main.go` and
`go run main.go <path>` to reproduce; kept here as evidence for the counts
cited in [`../REPORT.md`](../REPORT.md).

## Open Questions

None at this time.
