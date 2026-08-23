# Design: `wb repo local-only` and `wb repo init-remote`

**Date:** 2026-08-23
**Status:** Approved

## Problem

`wb sync` unconditionally runs `git pull --quiet` on every non-archived,
non-fork, remote-owned repo (`fleetsync.syncActive` ->
`gitops.Pull`, see `internal/fleetsync/fleetsync.go` and
`internal/gitops/gitops.go:216`). When a repo's checked-out branch has never
been pushed to its remote counterpart — either because the repo is brand new
on both ends, or because the local branch tracks a ref that no longer exists
on the remote — `git pull` fails with:

```
Your configuration specifies to merge with the ref 'refs/heads/main'
from the remote, but no such ref was fetched.
```

This surfaced for two repos discovered via `wb sync`:

- `trakhimenok/datatug-proj-1` — local clone and GitHub repo both have zero
  commits (`gh repo view` reports `isEmpty: true`). Nothing to pull, ever,
  until someone commits.
- `trakhimenok/webrtc-relay` — same: both ends empty.

(A third repo, `taxcalc-pro/taxcalc-pro-core`, hit a related but distinct
issue — local `master` tracked `origin/master`, but the remote's default
branch had been renamed to `main`. That one was fixed directly by renaming
the local branch and re-pointing the upstream; it needs no new tooling.)

There is currently no way to tell `wb` "leave this repo alone" or to
one-shot "give this empty repo an initial commit and push it," so these
repos will keep failing every `wb sync` run indefinitely.

## Goals

- Let a repo be marked so `wb sync` never attempts clone/pull/push against
  its remote, regardless of working-tree or history state.
- Let a repo whose branch has never been pushed be bootstrapped in one
  command, after which `wb sync` treats it normally.
- Follow existing `wb repo` subcommand conventions (see `wb repo status`).

## Non-goals

- Auto-detecting "both ends empty" and silently skipping it. The user
  wants an explicit, persistent choice per repo, not implicit tool
  cleverness.
- Handling divergent-history bootstrap scenarios (remote already has
  unrelated commits under a different ref). `init-remote` only helps the
  "never pushed" case; anything with real conflicting history needs manual
  resolution.

## Design

### 1. `wb repo local-only [path]`

Sets `git config wb.local-only true` in the given repo's local
`.git/config`. Defaults to `.` like `wb repo status`. A `--unset` flag
removes the key (`git config --unset wb.local-only`).

`--unset` must be idempotent: `git config --unset` exits **5** when the key
was never set (verified empirically). Treat exit 5 as success, not an error,
so unsetting an unmarked repo is a no-op rather than a failure.

This is a per-clone marker, not a fleet-wide config: it only exists once a
repo is cloned locally, and does not survive a fresh re-clone. That's an
accepted tradeoff — this feature is for repos you already have checked out
and want `wb` to stop touching.

### 2. `fleetsync.Sync` gains a local-only check

In `internal/fleetsync/fleetsync.go`, `Sync` currently starts with:

```go
if !repo.Remote || repo.IsFork {
    res.Status = NoOp
    return res
}
```

Add, right after that (only meaningful once a path exists):

```go
if repo.Path != "" {
    localOnly, err := gitops.LocalOnly(repo.Path)
    if err != nil {
        res.Status = Failed
        res.Err = err
        return res
    }
    if localOnly {
        res.Status = SkippedLocalOnly
        return res
    }
}
```

#### `gitops.LocalOnly` error handling

`gitops.LocalOnly(path string) (bool, error)` runs
`git config --bool wb.local-only`. Exit codes differ meaningfully and must
be distinguished (all verified empirically):

| Situation | Exit code | `LocalOnly` returns |
|---|---|---|
| Key absent | 1 | `false, nil` |
| Key set to a bool (`true`/`false`/`1`/`0`/`yes`…) | 0 | parsed value, `nil` |
| Key set to a non-bool (e.g. `garbage`) | 128 (`fatal: bad boolean config value`) | `false, err` |

Only exit 1 means "absent". Because `gitops.run()` wraps errors with
`fmt.Errorf(...%w...)`, the implementation must `errors.As` down to
`*exec.ExitError` and check `ExitCode() == 1` specifically — it must **not**
follow the looser convention in `internal/hooks/git.go`'s
`configuredHooksPath`, which swallows *all* `git config --get` errors as
"absent". Swallowing exit 128 here would silently treat a malformed marker
as unmarked and resume pulling a repo the user asked wb to leave alone.

#### Status naming

New `fleetsync.Status` value **`SkippedLocalOnly`**, with
`String() == "skipped (local-only)"`.

Deliberately *not* named `LocalOnly`: `cmd/wb/fleet_cmd.go:208` already has
`fleetRemoteStats.LocalOnly`, paired with `RemoteOnly` and incremented at
line 324 when `repository.Local && !repository.Remote` — i.e. "cloned
locally, absent from GitHub", a *detected* condition. The new status is a
*declared* marker and means something different. `SkippedLocalOnly` also
parallels the existing `SkippedDirty` / `"skipped (dirty)"` convention.

(The pre-existing `TestSyncLocalOnlyNoOp` in `fleetsync_test.go:209` tests
the `repo.Remote == false` NoOp path and is unrelated; leave it alone, but
do not mirror its name for the new test.)

`printSyncSummary` (in `cmd/wb/sync.go`) gets one more line:

```go
fmt.Printf("Skipped (local)   %d\n", counts[fleetsync.SkippedLocalOnly])
```

placed next to the other skip-style rows (`Not owned/fork`, `Skipped
(dirty)`, `Archived kept`).

### 3. `wb repo init-remote [path]`

Bootstraps a repo whose current branch has never been pushed. Defaults to
`.`. Steps:

1. Verify an `origin` remote exists (`git remote get-url origin`). If not,
   fail early with a clear message rather than letting `git push` produce
   `'origin' does not appear to be a git repository`.
2. Determine the current branch name (`git branch --show-current`).
   **Guard against detached HEAD:** on a detached HEAD this exits 0 but
   prints an empty string (verified), which would otherwise produce a
   malformed `git push -u origin ""`. Empty output must abort with an
   explicit "detached HEAD; check out a branch first" error.
3. `git rev-parse HEAD` — if it fails (unborn branch, no commits; exits 128,
   verified), run `git commit --allow-empty -m "Initial commit"`.
4. `git push -u origin <branch>`.

Note the ordering: the branch-name and origin checks come *before* the
commit, so a misconfigured repo isn't left with a stray empty commit after
a failed run.

If step 4 fails (e.g. because the remote branch already has unrelated
history), the command surfaces the git error as-is rather than trying to
guess a resolution — this is a one-shot convenience for the empty/never-
pushed case, not a general publish-and-merge tool.

Running from a linked git worktree is safe and needs no special handling:
worktrees share the canonical repo's object and ref store, so commit and
push behave normally.

## Applying this now

- `trakhimenok/datatug-proj-1`: `wb repo local-only` (leave empty on both
  ends; sync stops erroring).
- `trakhimenok/webrtc-relay`: `wb repo init-remote` (pushes an empty initial
  commit to `origin/main`; sync starts working normally from then on).

## Testing

- `internal/gitops`: table-driven tests for `LocalOnly` covering all three
  rows of the exit-code table above — key absent (exit 1 → `false, nil`),
  key set true/false (exit 0 → parsed, `nil`), and key set to a non-bool
  (exit 128 → error, **not** silently `false`). That last case is the
  regression guard against copying the looser `configuredHooksPath`
  convention.
- `internal/fleetsync`: extend `fleetsync_test.go` with a case where a repo
  path has `wb.local-only` set, asserting `Status == SkippedLocalOnly` and
  that no git command is invoked against the remote. Name it distinctly
  from the existing `TestSyncLocalOnlyNoOp`, which covers the unrelated
  `repo.Remote == false` path.
- `cmd/wb`: extend `cli_smoke_test.go` with smoke tests for:
  - `wb repo local-only` — set, verified via `git config --get`.
  - `wb repo local-only --unset` — run twice, asserting the second run
    still succeeds (exit-5 idempotency).
  - `wb repo init-remote` — against a bare-repo fixture as origin,
    asserting the branch ends up pushed.
  - `wb repo init-remote` on a detached HEAD — asserting it fails with the
    explicit error and leaves no stray empty commit.
