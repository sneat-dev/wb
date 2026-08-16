# Dependency drift

`wb deps drift` is a read-only Go convergence report for one repository or a
selected fleet.

```sh
wb deps drift . --format json
wb deps drift --fleet --match 'acme/*' --format yaml
wb deps drift --fleet --fail-on-drift
wb deps drift --fleet --online --dependency example.com/sdk
```

Use it before a bump or migration when the question is “do these checkouts
agree on dependency versions?” rather than “what does the topology look like?”
(`wb deps graph`) or “set/propagate this exact version” (`wb deps set` /
`wb deps bump`).

Offline is the default: declared and selected versions come from local
`go.mod` / `go list -m`. Latest is only observed with `--online`. Local
`replace` directives are classified as `replaced`. Parallel major module paths
(`example.com/lib` and `example.com/lib/v2`) are `major_path_split`.

`--fail-on-drift` exits non-zero after the complete report when divergent,
replaced, or major-path-split groups are present. Inspection errors always
exit non-zero after the report.
