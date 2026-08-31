# Efficient dependency waves

Combine root events:

```sh
wb deps bump go --fleet \
  --changed <root-a>@<version-a> \
  --changed <root-b>@<version-b> \
  --match '<owner>/*' --dry-run
```

Review the first ready wave, affected repositories, and dependency layers.
Then reuse the exact selection:

```sh
wb deps bump go --fleet \
  --changed <root-a>@<version-a> \
  --changed <root-b>@<version-b> \
  --match '<owner>/*' --validation=fast --parallel 4 --merge
```

`--validation=full` is the default and runs local lint, test, and build checks.
Use `--validation=fast` for a CI-authoritative campaign: repository push hooks
still run, and WB accepts the mode only with `--merge`, so the exact PR head
must pass required GitHub checks. This is distinct from the legacy
`--no-verify` escape hatch.

WB accumulates ready dependency events before updating a consumer. It opens
independent PRs concurrently up to `--parallel`, then waits on their checks
concurrently, merges passing providers, observes their releases, and
recalculates downstream readiness. Dependency layers remain ordered.

Leave `--refresh-after` unset to use the `5m` default. If a release event has
waited longer than that, WB checks for a newer semantic version immediately
before a downstream build and uses it when available. This avoids paying for a
build against a version already superseded during the wait.

Use `--refresh-after=0` only when reproducibility requires the exact older
event. Tune `--release-poll` for latency, not CI minutes.

After a failure:

```sh
wb deps bump go --fleet \
  --changed <root-a>@<version-a> \
  --changed <root-b>@<version-b> \
  --match '<owner>/*' --validation=fast --parallel 4 --merge --resume
```

Keep any explicit `--report-dir` unchanged. Do not close/reopen the PR or push
empty retries; resume reuses validated worktrees, branches, and open PRs.
