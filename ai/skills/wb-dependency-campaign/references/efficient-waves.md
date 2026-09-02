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
still run. With `--pr`, WB waits for required checks on the exact PR head and
reports the PR as validated/awaiting merge; with `--merge`, it additionally
requires the server-enforced freshness fence before merging. This is distinct from the legacy
`--no-verify` escape hatch.

WB accumulates ready dependency events before updating a consumer. It opens
independent PRs concurrently up to `--parallel`, then waits on their checks
concurrently, merges passing providers, observes their releases, and
recalculates downstream readiness. Dependency layers remain ordered.

Read-only work does not wait for `--parallel`: when the flag is left at its
default, per-wave graph discovery (one `git fetch` plus manifest reads per
fleet repository) and registry release observations run on a floor of 4
workers, matching `wb sync`'s default. An explicit `--parallel` bounds every
pool in both directions — pass `--parallel 1` to force a fully serial
campaign. That explicit authority is persisted in the campaign report, so a
`--resume` that omits `--parallel` restores it rather than regaining the
read-only floor.

Add `--fetch-cache` (opt-in) to stop later waves' DISCOVERY from re-fetching
repositories this run never wrote to. Memoized fetches expire after 15
minutes, and any repository the run ever pushed to, opened a PR for, or
merged is permanently excluded from the cache for the rest of the run — WB
merges server-side, so the landed default-branch commits are invisible to
local-push accounting and only an unconditional re-fetch observes them. The
wave engine always re-fetches before cutting a branch, so mutation bases are
never memoized. The cache is process-local and never persisted; a fresh
invocation (including `--resume`) always starts with full fetches. Do NOT use
`--fetch-cache` when anything other than this run may land on `main`
mid-campaign (a teammate, a sibling campaign, dependabot): memoized discovery
reads of untouched repositories can be up to 15 minutes stale. Honest
expectation: at the default read-only pool of 4, this saves roughly a minute
of discovery per wave on a ~450-repository fleet — worthwhile across a
many-wave campaign, not a silver bullet.

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
