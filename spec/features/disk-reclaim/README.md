---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Disk Reclaim

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-reclaim?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-reclaim?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-reclaim?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-reclaim?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Related Ideas:** [scratch-has-an-owner](../../ideas/scratch-has-an-owner.md) (Draft; not promoted by this Feature)
**Depends On:** [Worktree Lifecycle](../worktree-lifecycle/README.md), [Daemon Lifecycle Identity](../daemon-lifecycle/README.md)

## Summary

`wb disk reclaim` gives back what `wb disk` reports, planning unless
`--apply`: Go cache trim to a budget, temp directories whose owner is gone,
package stores, WB state and finished worktrees; the daemon runs a safe
subset below a free-space floor. Root causes handled at source: `-trimpath`
cache sharing and tests that leave scratch behind.

## Problem

The founder: "Should we build in disk space cleanup into wb?" (yes) —
"25GB of build cache feels wrong." — "Should tests cleanup after themselves?"

On 2026-09-18 the main agent VM reached 99% full. The shared Go build cache
was 25 GB (untrimmed main-module packages key on their absolute directory, so
each worktree may cache its own copy; Go trims only after 5 unused days).
Temp held 6.3 GB of `wb-*`, 2.7 GB of `go-build*` and 1.9 GB of `t.TempDir`
directories left by killed processes (#641 fixed one leak). `wb disk` cannot
reclaim; the deleting verbs (`wb worktree gc`, `wb cleanup`,
`wb layout clean`, `wb hooks lifecycle gc`) touch no caches or temp.

## Behavior

### REQ: plan-by-default

Without `--apply` nothing is deleted, moved or modified, including the
worktree heartbeat (exempt as `wb version` is); delegated collectors run only
in a side-effect-free planning form, else report `not-planned`. Per category
it reports candidates, reclaim bytes (`wb disk`'s single-walk accounting) and
why each is deleted or kept; `--apply` adds reclaimed bytes and free space
before and after. Exit `0`: nothing to reclaim, or all applied; `1`:
candidates remain in plan mode, or a deletion failed; `2`: usage, including an
unparsable `--budget` or a `--min-age` below its floor.

### REQ: categories

| Category | Candidates | Method |
|---|---|---|
| `go-build-cache` | entry files of `go env GOCACHE` | oldest-first trim to `--budget` (default `disk.go_build_cache.budget`, 10 GiB) |
| `scratch` | temp-directory entries `wb-*`, `wb/<task>`, `go-build*`, `t.TempDir` shapes | REQ: scratch-ownership |
| `node-package-store` | the pnpm store; the npm cache | `pnpm store prune` and `npm cache verify`, each skipped with `skipped: busy` while any `pnpm`/`npm` process runs |
| `wb-state` | lifecycle receipts; session records of exited processes | `wb hooks lifecycle gc`; `wb session prune` (no planning form, so `not-planned` in plan mode) |
| `worktrees` | WB-managed checkouts | `wb worktree gc` with default flags, never `--allow-residue` |
| `lifecycle-artifacts` | executor artifacts in worktrees, over budget | oldest last-receipt first; never a canonical checkout, a checkout with a pending or running execution, or a checkout claimed by a live lane |

The Go module cache, Work Logs and fleet events are report-only (network to
refill; open decision 5). A `.worktrees/` inside a canonical clone is the
store in `repository-local` mode; in central mode, one holding no registered
worktree is reported as `unregistered-checkout-store`, never deleted.

### REQ: go-cache-trim-is-safe

Go refreshes an entry's modification time only when it is over an hour old,
and hands cache paths straight to compile and link, so deleting an entry a
running build will read is a hard error, not a miss. The trim MUST delete only
`-a`/`-d` entry files, oldest first, never one modified within `--min-age`
(default 24 h; at least 1 h plus `disk.go_build_cache.max_build`, default
1 h, else a usage error), stopping once within budget and leaving `README`,
`trim.txt` and the tree intact. If only newer entries remain, it reports
`budget-unreachable` with the excess bytes, a finding (exit `1`).

### REQ: scratch-ownership

A scratch directory MAY be removed only when, checked in order:

1. it is recorded in a worktree manifest (scratch-has-an-owner) whose task
   is retired;
2. it contains a `wb-owner.lock` whose exclusive lock (`flock`; on Windows an
   exclusive open) reclaim can acquire, so its holder is gone; this holds
   across PID namespaces. `wb run` MUST hold one in every shard and scratch
   directory it creates and pass it to its children (`ExtraFiles` on Unix,
   an inheritable handle in `SysProcAttr.AdditionalInheritedHandles` on
   Windows), so it outlives a killed `wb run` while a child runs. A held lock
   is reported with its age;
3. it has no owner record, no file inside it was modified within
   `--older-than` (default 72 h), no live or parked task's claim, manifest or
   worktree references its path, and, on Linux, no process has its working
   directory or an open file under it. These are reported as `ownerless`.

`go-build*` and `t.TempDir` directories (`Test`, `Benchmark` or `Fuzz`
`<Name><digits>` holding numbered subdirectories) are always rule 3. Only entries the invoking user
owns are candidates; others are listed as `foreign-owner` and never raise the
exit code. A directory whose lock is held is kept regardless of age.

### REQ: tests-clean-up

wb's tests MUST leave the temporary directory as they found it (`t.TempDir()`;
`TestMain` scratch removed before `os.Exit`). CI MUST run `go test ./...`
under a fresh empty `TMPDIR` and fail, listing leftovers, if it is not empty
afterwards. `wb run` MUST remove its shard directories on normal exit and on
`SIGINT`/`SIGTERM`; `SIGKILL` is covered by scratch-ownership rule 2.

### REQ: trimpath-investigation

`-trimpath` is scoped to Go commands wb itself runs through `wb run` and
`wb check`, appended to their child `GOFLAGS`; it is never set machine-wide.
Before adopting it WB MUST record, for the gate workload
(`wb check --profile ci`) in two worktrees sharing one `GOCACHE`: the bytes
the second adds untrimmed; the bytes it adds trimmed; and the bytes added
when trimmed wb runs mix with untrimmed plain `go test ./...` runs outside
wb, which keep the other cache key. Tests locating files through
`runtime.Caller` (today only `cmd/wb/module_archive_test.go`) MUST first stop
depending on absolute paths. It is adopted only if the mixed case adds fewer
bytes than the untrimmed case and the suite passes.

### REQ: daemon-auto-reclaim

`wb.yaml` gains `disk.minimum_available` (default 0.10, also `wb disk`'s
`--minimum-available` default), `disk.auto_reclaim` (default `on`) and
`disk.check_interval` (default 10 m). At that interval the daemon checks free
space and, below the floor, runs at most once per hour the safe subset:
`go-build-cache`, `scratch` rules 1 and 2, lifecycle receipts; never
`worktrees`, `node-package-store`, `lifecycle-artifacts` or rule-3 scratch.
`wb disk reclaim --auto` runs exactly that subset and rate limit. Each run
appends a receipt (trigger, free before, bytes per category, free after)
under `<projects-root>/.wb/disk/receipts/`, even when nothing was reclaimed;
a rate-limited check writes none, and `--auto` then reports
`skipped: rate-limited`, exit `0`. A failed run, or free space still below
the floor, is a finding in `wb daemon status` and `wb disk`.

### REQ: machine-readable-output

`--format json|yaml` emits
`{"mode":"plan|apply|auto","filesystem":{...},"categories":[{"name","candidates":[{"path","bytes","reason","action":"delete|keep"}],"planned_bytes","reclaimed_bytes","errors":[]}]}`,
with `filesystem` shaped as in `wb disk`.

## Acceptance Criteria

### AC: plan-changes-nothing

**Requirements:** disk-reclaim#req:plan-by-default, disk-reclaim#req:machine-readable-output

**Given** a 12 GiB Go cache, three stale `wb-*` directories, one landed
worktree, a `.wb-retired-*` stage, and the command run from inside a worktree
**When** the user runs `wb disk reclaim --format json`
**Then** the output validates against the documented shape with every
category present; a recursive listing with modification times of every root,
the stage and the worktree's heartbeat is identical before and after; exit is
`1`.

### AC: go-cache-trimmed-to-budget

**Requirements:** disk-reclaim#req:go-cache-trim-is-safe

**Given** a 12 GiB `GOCACHE` with staggered modification times, some within
the last hour
**When** the user runs
`wb disk reclaim --category go-build-cache --budget 8GiB --apply`
**Then** the cache is at most 8 GiB, no remaining entry file is older than a
deleted one, none modified within 24 h was deleted, `README` and `trim.txt`
remain, and `go build ./...` in wb succeeds.

### AC: concurrent-build-survives-trim

**Requirements:** disk-reclaim#req:go-cache-trim-is-safe

**Given** `go test ./internal/...` running in a worktree
**When** `wb disk reclaim --category go-build-cache --budget 1GiB --min-age 2h --apply`
runs concurrently three times
**Then** the test run's exit status equals an undisturbed run's; and
`--min-age 30m` exits `2` before deleting anything.

### AC: scratch-owners-respected

**Requirements:** disk-reclaim#req:scratch-ownership

**Given** `wb-go-test-shards-A` whose lock a running `wb run` holds (mtime
older than 72 h), the same run inside a separate PID namespace for
`wb-go-test-shards-B`, `wb-go-test-shards-C` whose owner was killed with
`SIGKILL` with no surviving child, `wb-go-test-shards-D` whose `wb run` was
killed while its `go test` child still runs, `wb/parked-task` untouched for
5 days but claimed by a parked task, a `go-build123` untouched for 5 days,
and a `go-build456` owned by another user
**When** the user runs `wb disk reclaim --category scratch --apply`
**Then** A, B, D and `wb/parked-task` remain; C and `go-build123` are
removed, the latter reported `ownerless`; `go-build456` is listed as
`foreign-owner`; exit is `0`.

### AC: interrupted-run-cleans-shards

**Requirements:** disk-reclaim#req:tests-clean-up

**Given** `wb run -- go test ./internal/worktrees/...` with sharding
**When** the run receives `SIGTERM`
**Then** none of the shard directories it created remains.

### AC: test-leak-guard-fails-ci

**Requirements:** disk-reclaim#req:tests-clean-up

**Given** a test that calls `os.MkdirTemp("", "leak-*")` and never removes it
**When** the CI job runs `go test ./...` under a fresh `TMPDIR`
**Then** the job fails listing `leak-…`; with the test removed, it passes on
the whole wb suite.

### AC: trimpath-measured

**Requirements:** disk-reclaim#req:trimpath-investigation

**Given** two worktrees of wb at one commit and an empty shared `GOCACHE`
**When** the gate workload runs in A then B, untrimmed; then, on a fresh
cache, trimmed; then, on a fresh cache, trimmed through wb plus a plain
untrimmed `go test ./...` in each worktree
**Then** the Feature records the bytes each run adds and the suite result,
and the adopt-or-not outcome follows the REQ's rule.

### AC: delegated-categories

**Requirements:** disk-reclaim#req:categories

**Given** a pnpm store with unreferenced packages, a failed lifecycle receipt
not yet surfaced plus 2,000 old receipts, one landed worktree and one with
unpushed commits, and a worktree `.codegraph` over budget whose executor is
`pending`
**When** the user runs `wb disk reclaim --apply` while no `pnpm` runs, then
again while one runs
**Then** the first prunes the store, keeps the unsurfaced failure and the
newest receipts, retires only the landed worktree, and keeps the pending
`.codegraph`; the second reports `node-package-store` as `skipped: busy`.

### AC: failed-deletion-exits-1

**Requirements:** disk-reclaim#req:plan-by-default

**Given** an ownerless scratch directory containing a file the user cannot
delete
**When** the user runs `wb disk reclaim --category scratch --apply`
**Then** the category lists the error with the path and exit is `1`; an
unparsable `--budget 8XB` exits `2`.

### AC: daemon-reclaims-below-floor

**Requirements:** disk-reclaim#req:daemon-auto-reclaim

**Given** `disk.minimum_available: 0.99`, `disk.check_interval: 5s`, a large
Go cache, a rule-3 scratch directory and a landed worktree
**When** the daemon runs for 15 s, then the user runs
`wb disk reclaim --auto --format json`
**Then** exactly one receipt was written by the daemon, the Go cache was
trimmed, the scratch and worktree remain, and the manual `--auto` run reports
it was skipped by the hourly rate limit.

### AC: failed-auto-reclaim-is-visible

**Requirements:** disk-reclaim#req:daemon-auto-reclaim

**Given** an automatic run that leaves free space below the floor
**When** the user runs `wb daemon status` and `wb disk`
**Then** both report the finding with the receipt path, and both exit `1`.

### AC: checkout-stores-never-deleted

**Requirements:** disk-reclaim#req:categories

**Given** central mode and a `.worktrees/` directory inside a canonical clone
holding no registered worktree
**When** the user runs `wb disk reclaim --apply`
**Then** it is untouched and reported as `unregistered-checkout-store`.

## Delivery Slices

0. The `-trimpath` measurement and the fresh-`TMPDIR` CI guard —
   trimpath-measured, test-leak-guard-fails-ci.
1. `wb disk reclaim` with `go-build-cache` and rule-3 `scratch` only —
   plan-changes-nothing (those categories), go-cache-trimmed-to-budget,
   concurrent-build-survives-trim, failed-deletion-exits-1.
2. The `wb run` lock and signal cleanup — interrupted-run-cleans-shards,
   scratch-owners-respected.
3. Delegated categories, after code-index-freshness slice 3 (the `artifacts:`
   field) — delegated-categories, checkout-stores-never-deleted.
4. Daemon auto-reclaim — daemon-reclaims-below-floor,
   failed-auto-reclaim-is-visible.

## Open Questions

- The 10 GiB Go cache budget is a placeholder; a share of the volume?
- **Machine-wide `-trimpath`** could become a future opt-in machine-setup
  item. Prerequisites: a `runtime.Caller` pre-flight across in-scope
  repositories, a gate on every in-scope repository's suite, `--check`
  reporting, and an opt-out. Not a requirement now.
- Work Log and fleet-event retention is open decision 5 (report-only).
- May the daemon remove rule-3 scratch older than 7 days below the floor if
  ownerless leaks persist after `wb-owner.lock` ships?

---
*This document follows the https://specscore.md/feature-specification*
