# Dependency graph

Inspect one repository first:

```sh
wb deps graph .
```

Use fleet mode to discover cross-repository consumers and provider-first
layers:

```sh
wb deps graph --fleet --match '<owner>/*' \
  --dependency <module> --view repos
```

Repeat `--dependency` for multiple roots. Use `--view dependencies` for module
relationships and `--view selections` to explain filtering.

Write deterministic reports when another step will consume the result:

```sh
wb deps graph --fleet --dependency <module> \
  --report-dir <dir> --format json
```

Use `--open` only when a human needs the self-contained HTML visualization.
Agents should prefer Markdown, YAML, or JSON output.

Filters compose: `--filter` is a substring, `--match` a glob, and `--regex` a
regular expression against `owner/repository`. A strict graph discovery error
must not be silently converted into an incomplete rollout.
