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
        res.Status = LocalOnly
        return res
    }
}
```

`gitops.LocalOnly(path string) (bool, error)` runs
`git config --bool wb.local-only` and reports `true` only when the value is
set and true; a missing key (git's "key not found" exit status) is not an
error — it means `false`.

New `fleetsync.Status` value `LocalOnly`, with `String() == "local-only"`.
`printSyncSummary` (in `cmd/wb/sync.go`) gets one more line:

```go
fmt.Printf("Local only        %d\n", counts[fleetsync.LocalOnly])
```

placed next to the other skip-style rows (`Not owned/fork`, `Archived kept`).

### 3. `wb repo init-remote [path]`

Bootstraps a repo whose current branch has never been pushed. Defaults to
`.`. Steps:

1. `git rev-parse HEAD` — if it fails (unborn branch, no commits), run
   `git commit --allow-empty -m "Initial commit"`.
2. Determine the current branch name (`git branch --show-current`).
3. `git push -u origin <branch>`.

If step 3 fails (e.g. because the remote branch already has unrelated
history), the command surfaces the git error as-is rather than trying to
guess a resolution — this is a one-shot convenience for the empty/never-
pushed case, not a general publish-and-merge tool.

## Applying this now

- `trakhimenok/datatug-proj-1`: `wb repo local-only` (leave empty on both
  ends; sync stops erroring).
- `trakhimenok/webrtc-relay`: `wb repo init-remote` (pushes an empty initial
  commit to `origin/main`; sync starts working normally from then on).

## Testing

- `internal/gitops`: table-driven tests for `LocalOnly` (unset, true,
  false, non-bool value) alongside existing `gitops_test.go` style.
- `internal/fleetsync`: extend `fleetsync_test.go` with a case where a repo
  path has `wb.local-only` set, asserting `Status == LocalOnly` and that no
  git command is invoked against the remote.
- `cmd/wb`: extend `cli_smoke_test.go` with smoke tests for
  `wb repo local-only` (set + `--unset`, verified via `git config --get`)
  and `wb repo init-remote` (against a scratch repo with a real but empty
  remote, or a bare repo fixture, asserting the branch ends up pushed).
