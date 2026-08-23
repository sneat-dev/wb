# Design: `wb repo ignore` and `wb repo init-remote`

**Date:** 2026-08-23
**Status:** Approved — revised after two review rounds; naming resolved

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

## Naming decision

The marker means **"don't sync this repo"** — a choice the user declares.
It was initially called *local-only*, but that collides with an existing,
unrelated concept in the same command family: `fleetRemoteStats.LocalOnly`
(`cmd/wb/fleet_cmd.go:208`, incremented at line 324 when
`repository.Local && !repository.Remote`) means *"cloned locally, absent
from GitHub"* — a condition wb **detects**.

The two are genuinely different. `datatug-proj-1`, the repo that motivated
this feature, **does** exist on GitHub (it is simply empty), so wb does not
count it as local-only — yet it is exactly the repo we want to mark. A
command named `local-only` would assert something factually false about it.
Both counts also appear in the same `fleetRemoteSummary` line (line 572),
which would have read `0 local-only · … · 2 skipped-local-only`.

**Resolved: `wb repo ignore`**, git key `wb.skip-sync`, status
`SkippedIgnored`, summary token `ignored`. Names the action rather than a
mistaken property of the repo.

## Design

### 1. `wb repo ignore [path]`

Sets `git config --local wb.skip-sync true` in the given repo's
`.git/config`. Defaults to `.` like `wb repo status`. A `--unset` flag
clears it.

**`--local` is mandatory on both read and write.** Verified: a plain
`git config --bool wb.skip-sync` read falls back to `~/.gitconfig` and
system config, so a single stray global key would silently disable sync
across the entire fleet.

**`--unset` must use `--unset-all`.** Verified on git 2.43: with a
duplicated key, plain `git config --unset` exits **5**
(`warning: … has multiple values`) and **leaves both values in place** —
so treating exit 5 as success would report success on a repo that is still
marked. `--unset-all` clears all values (exit 0) and returns exit 5 only
when the key is genuinely absent, which is the idempotent no-op case and
should be treated as success.

(Related: a `--bool` read of a multi-valued key silently returns the *last*
value. `--unset-all` on write keeps that state from arising.)

### 2. `fleetsync.Sync` gains a skip check

In `internal/fleetsync/fleetsync.go`, `Sync` currently starts with:

```go
if !repo.Remote || repo.IsFork {
    res.Status = NoOp
    return res
}
```

Add, right after that:

```go
if repo.Path != "" {
    skip, err := gitops.SkipSync(repo.Path)
    if err != nil {
        res.Status = Failed
        res.Err = err
        return res
    }
    if skip {
        res.Status = SkippedIgnored
        return res
    }
}
```

#### Placement vs. archived repos — deliberate

The check sits **before** the `repo.Archived` branch, so the marker wins
over archived-clone cleanup. This is intended: wb must never delete a clone
the user explicitly told it to leave alone. The cost is that a
marked-then-archived repo disappears from the `Archived kept` /
`Archived removed` / `Archived absent` accounting — it reports as skipped
instead. Accepted; documented here so it isn't later "fixed" as a bug.

#### `gitops.SkipSync` error handling

`gitops.SkipSync(path string) (bool, error)` runs
`git config --local --bool wb.skip-sync`. Exit codes (all verified
empirically against git 2.43):

| Situation | Exit code | Returns |
|---|---|---|
| Key absent | 1 | `false, nil` |
| Key set to a bool (`true`/`false`/`1`/`0`/`yes`…) | 0 | parsed value, `nil` |
| Key set to a non-bool (e.g. `garbage`) | 128 (`fatal: bad boolean config value`) | `false, err` |
| Path is not a git repo | 128 (with `--local`) | `false, err` |

Only exit 1 means "absent". Because `gitops.run()` wraps errors with
`fmt.Errorf(...%w...)`, the implementation must `errors.As` down to
`*exec.ExitError` and check `ExitCode() == 1` specifically. It must **not**
follow the looser convention in `internal/hooks/git.go`'s
`configuredHooksPath`, which swallows *all* `git config --get` errors as
"absent" — swallowing exit 128 here would treat a malformed marker as
unmarked and resume pulling a repo the user asked wb to leave alone.

Note the `--local` flag also disambiguates the not-a-repo case: **without**
`--local` that exits 1 (indistinguishable from "absent"); **with** it, 128.

#### Status naming and enum safety

New `fleetsync.Status` value **`SkippedIgnored`**, `String() == "skipped
(ignored)"`.

**Append after `Failed`.** Verified safe: `fleetsync.Status` is never
marshalled or persisted numerically — only `String()` is used — so iota
reordering is not a concern, but appending avoids renumbering regardless.

(The pre-existing `TestSyncLocalOnlyNoOp` in `fleetsync_test.go:209` tests
the unrelated `repo.Remote == false` NoOp path. Leave it alone and do not
mirror its name for the new test.)

### 3. Consumers of the new Status — full blast radius

Every consumer of `fleetsync.Status` was traced:

| Consumer | Effect of a new variant | Action |
|---|---|---|
| `cmd/wb/fleet_cmd.go:330` switch | **No `default:` case** — the variant is silently dropped, so `wb fleet --remote` counts stop summing to the filtered repo count | **Must fix:** add a `SkippedIgnored int \`yaml:"skipped_ignored" json:"skipped_ignored"\`` field to `fleetRemoteStats`, a `case` in the switch, and a token in `fleetRemoteSummary` (line 572) |
| `cmd/wb/sync.go printSyncSummary` | Map-based (`counts[...]`), safe | Add one line: `fmt.Printf("Skipped (ignored) %d\n", counts[fleetsync.SkippedIgnored])`, next to the other skip rows |
| `tui.Reviewable` | `default: false` — correctly treats it as not reviewable | None |
| `internal/tui/progress.go` | Status-agnostic | None |

The `fleet_cmd.go` gap is the one genuine integration bug; without it the
new status silently corrupts fleet dry-run accounting.

### 4. `wb repo init-remote [path]`

Bootstraps a repo whose current branch has never been pushed. Defaults to
`.`. Steps, in this order:

1. **Refuse if the repo is marked.** If `SkipSync` is true, fail with a
   message telling the user to `wb repo ignore --unset` first — otherwise
   the promise "sync starts working normally afterwards" is false, because
   `Sync` would still skip it.
2. Verify an `origin` remote exists (`git remote get-url origin`); fail
   early with a clear message rather than letting `git push` emit
   `'origin' does not appear to be a git repository`.
3. Determine the branch name (`git branch --show-current`). **Guard against
   detached HEAD:** verified to exit 0 printing an empty string, which would
   produce a malformed `git push -u origin ""`. Empty output must abort with
   an explicit "detached HEAD; check out a branch first" error.
4. `git rev-parse --verify --quiet HEAD` — if it fails, the branch is
   unborn, so run `git commit --allow-empty -m "Initial commit"`.
   Use `--verify --quiet` (verified: exit 1, silent) rather than bare
   `rev-parse HEAD` (exit 128 plus `fatal: ambiguous argument 'HEAD'`,
   which `run()` would fold into the error string).
5. `git push -u origin <branch>`.

Checks 1–3 precede the commit so a misconfigured repo isn't left with a
stray empty commit. Note this is **not** a full guarantee: if step 5 fails
(e.g. the remote already has unrelated history), the empty commit from
step 4 remains. That is acceptable — it is a legitimate local commit the
user can push later or reset — but the spec should not claim otherwise.

`git commit` inherits the user's identity via `console.Env()`. With
`user.email` unset, git emits its raw "Please tell me who you are" error;
surfacing that as-is is fine.

If step 5 fails the command surfaces the git error as-is rather than
guessing a resolution — this is a one-shot convenience for the
empty/never-pushed case, not a general publish-and-merge tool.

Running from a linked git worktree needs no special handling: worktrees
share the canonical repo's object and ref store, so commit and push behave
normally.

### 5. `wb repo` command description

`newRepoCmd`'s `Short` is currently "Inspect a single local repository".
With two mutating subcommands it becomes inaccurate — update to something
like "Inspect or configure a single local repository".

## Applying this now

- `trakhimenok/datatug-proj-1`: `wb repo ignore` (leave empty on both ends;
  sync stops erroring).
- `trakhimenok/webrtc-relay`: `wb repo init-remote` (pushes an empty initial
  commit to `origin/main`; sync works normally from then on).

## Testing

### Harness prerequisite (blocker)

`runWB` (`cmd/wb/cli_smoke_test.go:68`) sets neither `Dir` nor extra env, so
it inherits the test process's cwd — **the wb repo itself**. Since both new
subcommands default to `.`, naive smoke tests would write `wb.skip-sync`
into the real wb checkout and could push to wb's own origin. Before writing
these tests, add a cwd/env-capable `runWB` variant (or require explicit
temp paths in every invocation) and set `GIT_AUTHOR_*` / `GIT_COMMITTER_*`
for the commit. Reuse the `git(t, dir, …)` helper in
`internal/gitops/gitops_test.go`, which already sets `Dir` and identity env.

### Cases

- `internal/gitops`: table-driven tests for `SkipSync` covering all four
  rows of the exit-code table — absent (1 → `false, nil`), set true/false
  (0 → parsed), non-bool (128 → error, **not** silently `false`), and path
  not a git repo (128 → error). The non-bool row is the regression guard
  against copying the looser `configuredHooksPath` convention.
- `internal/gitops`: a test that a **global** `wb.skip-sync true` does
  **not** leak into a repo without a local key — the `--local` guard.
- `internal/fleetsync`: a case where a repo has the marker set, asserting
  `Status == SkippedIgnored`. Name it distinctly from the existing
  `TestSyncLocalOnlyNoOp`. Note: "assert no git command runs against the
  remote" is **not** achievable — there is no seam over `gitops` to observe
  calls. Instead assert `SkippedIgnored` on a repo configured with **no
  origin**, which would fail loudly if a pull were attempted.
- `internal/fleetsync`: a marked **and archived** repo, asserting the clone
  survives and status is `SkippedIgnored` (locks in the placement decision).
- `cmd/wb` smoke tests, all with explicit temp paths:
  - `wb repo ignore` — set, verified via `git config --local --get`.
  - `wb repo ignore --unset` — run twice; second run still succeeds.
  - `wb repo ignore --unset` on a duplicated key — asserts the key is
    actually gone (the `--unset-all` regression guard).
  - `wb repo init-remote` — against a bare-repo fixture as origin,
    asserting the branch ends up pushed.
  - `wb repo init-remote` on a detached HEAD — fails with the explicit
    error, leaves no commit.
  - `wb repo init-remote` on a marked repo — refuses (step 1).

## Performance note (optional)

The check adds one `git config` subprocess per repo per sync. If that
matters at fleet scale it could fold into the existing `gitops.Status`
call, which already shells out per repo. Not required for correctness.
