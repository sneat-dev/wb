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

`wb disk reclaim` gives back the bytes `wb disk` reports, planning by default
and deleting only under `--apply`. It trims the Go build cache oldest-first to
a budget, removes temporary directories whose owner is provably gone, prunes
package stores, collects WB state through its existing collectors, and
retires finished worktrees through `wb worktree gc`. The daemon runs a safe
subset on a fixed cadence when free space is below a configured floor. Two
root causes are handled at the source: whether `-trimpath` lets worktrees
share Go cache entries, and tests that leave scratch behind.

## Problem

The founder: "Should we build in disk space cleanup into wb?" (yes) —
"25GB of build cache feels wrong." — "Should tests cleanup after themselves?"

On 2026-09-18 the main agent VM reached 99% (1.8 GB free), a day after an
earlier incident at 101 MB free. The Go build cache was 25 GB: one shared
`GOCACHE`, but the main module's packages include their absolute directory in
the cache key without `-trimpath`, so each worktree path may cache its own
copy, and Go trims only entries unused for 5 days. The temporary directory
held 377 `wb-*` directories (6.3 GB), 67 `go-build*` work directories left by
killed Go processes (2.7 GB), and 1.9 GB of `Test*` directories left by
killed test binaries. The `internal/layout` `TestMain` leak was fixed in
#641; `wb-go-test-shards-*` still leak when `wb run` is killed. `wb disk`
measures but cannot reclaim. The deleting verbs that exist (`wb worktree gc`,
`wb cleanup`, `wb layout clean`, `wb hooks lifecycle gc`) retire checkouts,
clones and receipts; none touches caches or temporary directories.

## Behavior

### REQ: plan-by-default

Without `--apply` the command MUST NOT delete, move or modify anything,
including the worktree heartbeat (it is exempt as `wb version` is); delegated
collectors run in their planning form only, and a collector without a
side-effect-free planning form reports `not-planned`. It reports per category
the candidates, their reclaim bytes (the `wb disk` single-walk accounting, so
shared content is not double-counted), and why each is deleted or kept. With
`--apply` it also reports reclaimed bytes and free space before and after.
Exit `0`: nothing to reclaim, or every planned deletion applied; `1`:
candidates remain in plan mode, or any deletion failed; `2`: usage, including
an unparsable `--budget` or a `--min-age` below its floor.

### REQ: categories

| Category | Candidates | Method |
|---|---|---|
| `go-build-cache` | entry files of `go env GOCACHE` | oldest-first trim to `--budget` (default `disk.go_build_cache.budget`, 10 GiB) |
| `scratch` | temp-directory entries `wb-*`, `wb/<task>`, `go-build*`, `Test*` | REQ: scratch-ownership |
| `node-package-store` | the pnpm store; the npm cache | `pnpm store prune` and `npm cache verify`, each skipped with `skipped: busy` while any `pnpm`/`npm` process runs |
| `wb-state` | lifecycle receipts; session records of exited processes | `wb hooks lifecycle gc`, `wb session prune`, with their own protections |
| `worktrees` | WB-managed checkouts | `wb worktree gc` with default flags, never `--allow-residue` |
| `lifecycle-artifacts` | executor artifacts in worktrees, over budget | oldest last-receipt first; never a canonical checkout, a checkout with a pending or running execution, or a checkout claimed by a live lane |

The Go module cache, Work Logs and fleet events are report-only (the first
costs network to refill; the others await open decision 5). A `.worktrees/`
directory inside a canonical clone is the legitimate store in
`repository-local` mode; in central mode, one holding no worktree registered
with Git is reported as `unregistered-checkout-store` and never deleted.

### REQ: go-cache-trim-is-safe

Go refreshes an entry's modification time on use only when it is older than
one hour, and passes cache paths directly to compile and link, so deleting an
entry a running build will read causes a hard build error, not a miss. The
trim MUST therefore delete only `-a` and `-d` entry files, oldest
modification time first, never one modified within `--min-age`, stopping as
soon as the cache is within budget, leaving `README`, `trim.txt` and the
directory tree intact. `--min-age` defaults to 24 h and MUST be at least
1 h plus `disk.go_build_cache.max_build` (default 1 h); a smaller value is a
usage error.

### REQ: scratch-ownership

A scratch directory MAY be removed only when, checked in order:

1. it is recorded in a worktree manifest (scratch-has-an-owner) whose task
   is retired;
2. it contains a `wb-owner.lock` that the creating process holds with an
   exclusive `flock` (on Windows, an exclusive open) for its lifetime, and
   reclaim can acquire that lock: it was released, so the owner is gone. This
   holds across PID namespaces. `wb run` MUST create and hold such a lock in
   every shard and scratch directory it creates;
3. it has no owner record, no file inside it was modified within
   `--older-than` (default 72 h), no live or parked task's claim, manifest or
   worktree references its path, and, on Linux, no process has its working
   directory or an open file under it. These are reported as `ownerless`.

`go-build*` and `Test*` directories are always rule 3. A directory whose lock
is held MUST be kept regardless of age.

### REQ: tests-clean-up

Tests in the wb repository MUST leave the temporary directory as they found
it: per-test scratch through `t.TempDir()`, and `TestMain` scratch removed
before `os.Exit`. CI MUST run `go test ./...` with `TMPDIR` set to a fresh
empty directory and fail, listing the leftovers, if it is not empty
afterwards. `wb run` MUST remove its shard directories on normal exit and on
`SIGINT`/`SIGTERM`; a `SIGKILL` leak is covered by scratch-ownership rule 2.

### REQ: trimpath-investigation

Before changing Go flags, WB MUST record a measurement of the bytes a second
worktree adds to a shared `GOCACHE` for the real gate workload (`wb check
--profile ci`: tests with coverage, vet, build), with and without
`-trimpath`, and whether that suite passes with it. The standard library and
module-cache dependencies are shared either way; only the main module's
packages can differ. Tests that locate files through `runtime.Caller` (today
only `cmd/wb/module_archive_test.go`) MUST first be made independent of
absolute paths. If `-trimpath` cuts the second worktree's added bytes by at
least half and the suite passes, `wb run` and `wb check` MUST append
`-trimpath` to `GOFLAGS` (never replacing a user's value) for Go build and
test commands; otherwise the measurement is recorded here and nothing
changes.

### REQ: daemon-auto-reclaim

The trusted user `wb.yaml` gains `disk.minimum_available` (share of the
volume; default 0.10, also `wb disk`'s default for `--minimum-available`),
`disk.auto_reclaim` (`on|off`, default `on`) and `disk.check_interval`
(default 10 m). The daemon MUST check free space at that interval; below the
floor it runs the safe subset — `go-build-cache`, `scratch` rules 1 and 2,
and `wb-state` lifecycle receipts — at most once per hour, never
`worktrees`, `node-package-store`, `lifecycle-artifacts` or rule-3 scratch.
`wb disk reclaim --auto` runs exactly that subset and rate limit, so it is
testable without waiting. Each run appends a receipt (trigger, free before,
bytes per category, free after) under `<projects-root>/.wb/disk/receipts/`,
including runs that reclaimed nothing. A failed run, or free space still
below the floor, is a finding in `wb daemon status` and `wb disk`.

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
`SIGKILL`, `wb/parked-task` untouched for 5 days but claimed by a parked
task, and a `go-build123` untouched for 5 days
**When** the user runs `wb disk reclaim --category scratch --apply`
**Then** A, B and `wb/parked-task` remain; C and `go-build123` are removed,
the latter reported `ownerless`; exit is `0`.

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
**When** the gate workload runs in A then B, the bytes B adds to `GOCACHE`
are recorded, and the same is repeated with a fresh cache and
`GOFLAGS=-trimpath`
**Then** the Feature records both byte counts and the suite result, and the
adopt-or-not outcome follows the REQ's rule.

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

## Open Questions

- **Go cache budget.** 10 GiB is a placeholder; after `-trimpath` the working
  set may be far smaller. A share of the volume instead?
- **Work Log and fleet-event retention (open decision 5).** Report-only until
  decided.
- **Rule-3 scratch in automatic runs.** Excluded today. If ownerless leaks
  persist once `wb-owner.lock` ships, may the daemon remove rule-3 scratch
  older than 7 days below the floor?

---
*This document follows the https://specscore.md/feature-specification*
