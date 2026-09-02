# Dependency drift

`wb deps drift` is a read-only convergence report for one repository or a
selected fleet, in the `go` or `npm` ecosystem.

```sh
wb deps drift . --format json
wb deps drift --fleet --match 'acme/*' --format yaml
wb deps drift --fleet --fail-on-drift
wb deps drift --fleet --online --dependency example.com/sdk
wb deps drift --fleet --ecosystem npm --online --scope '@sneat/*' --fail-on-behind
```

Use it before a bump or migration when the question is “do these checkouts
agree on dependency versions?” rather than “what does the topology look like?”
(`wb deps graph`) or “set/propagate this exact version” (`wb deps set` /
`wb deps bump`).

Offline is the default: declared and selected versions come from local
manifests. Latest is only observed with `--online`. Inspection errors always
exit non-zero after the report.

## Go

Declared versions come from every `go.mod`; selected versions come from
`go list -m`. Local `replace` directives are classified as `replaced`. Parallel
major module paths (`example.com/lib` and `example.com/lib/v2`) are
`major_path_split`.

## npm

Declared versions are the specifiers written in every `package.json`
dependency field and every `pnpm-workspace.yaml` `overrides:`/`catalog:` entry.
**Selected** versions come from the governing `pnpm-lock.yaml` or
`package-lock.json` — the version a build actually installs. This distinction
is the point: `"^0.30.0"` says what a fresh resolve *could* pick, while the
committed lockfile says what CI *will* install, and only the second one
explains why two repositories behave differently.

A lockfile whose importers pin conflicting versions is reported as a conflict
rather than collapsed to one guessed answer, and a package with no lockfile
evidence falls back to its declared specifier with the reason attached.

## Behind latest

With `--online`, a group is marked `behind` when at least one repository
provably resolves or admits only versions older than the published latest:

- a locked/selected exact version lower than latest, or
- an npm specifier that provably cannot admit latest — an exact pin such as
  `"0.14.0"` against a published `0.14.3`, or `^0.24.1` against `0.25.0`
  (npm's caret does not cross a `0.x` minor).

WB evaluates only the specifier shapes it can decide exactly: `*`, an exact
version, `^`, `~`, and the `>`/`>=`/`<`/`<=` comparators. Unions, hyphen
ranges, wildcards, and the `workspace:`/`catalog:`/`npm:`/`file:` protocols are
reported as **unevaluated** and are never counted as behind.

`--fail-on-drift` exits non-zero when divergent, replaced, or major-path-split
groups are present. `--fail-on-behind` exits non-zero when any repository lags
a published latest version.

## Bounding the run

An online fleet run makes one registry query per retained dependency, so
restrict the question:

- `--scope '@sneat/*'` (repeatable) retains dependencies matching a glob, using
  `path.Match` semantics — `*` never crosses a `/`.
- `--dependency <exact>` (repeatable) retains one exact path or package name.
- `--exclude 'sneat-co/sneat-go'` (repeatable) removes whole repositories by
  `owner/name` glob. Excluded slugs are listed in the report, so “clean” is
  never confused with “never inspected”.
