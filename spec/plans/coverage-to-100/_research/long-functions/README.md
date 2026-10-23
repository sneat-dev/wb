# long-functions

Standalone research script, kept as `main.go.txt` reference text (not
`.go`, so this module's build/vet/coverage tooling never sees it), that
produced [`../long-functions.txt`](../long-functions.txt): every
non-test, non-generated function or method in the repository whose body
(open brace to close brace, via `go/ast`, not a line-count/brace-counting
heuristic) spans more than 150 lines. Rename to `main.go` and run with
`go run main.go <repo-root>` to reproduce.

## Open Questions

None at this time.
