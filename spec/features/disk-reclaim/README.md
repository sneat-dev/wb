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

`wb disk reclaim` gives back the bytes `wb disk` reports, one category at a
time, planning by default and deleting only under `--apply`. It trims the Go
build cache least-recently-used first to a budget, removes leaked test scratch
whose owner is provably gone, prunes the pnpm store, collects WB's own state
and receipts through their existing collectors, and retires finished
worktrees through `wb worktree gc`. The daemon runs a safe subset on its own
when free space falls below the floor `wb disk` reports against. Two root
causes are addressed at the source: whether `-trimpath` lets worktrees share
one Go cache, and tests that leave scratch behind.

## Problem

The founder: "Should we build in disk space cleanup into wb?" (yes) —
"25GB of build cache feels wrong." — "Should tests cleanup after themselves?"

Facts on 2026-09-17/18:

- The disk reached 99% (1.8 GB free) a day after an earlier incident at
  101 MB free.
- The Go build cache was 25 GB. It is one shared `GOCACHE`, but without
  `-trimpath` the compile action ID includes each package's absolute
  directory, so every worktree path caches its own copy. Go trims only
  entries unused for 5 days.
- 66 leaked `/tmp/wb-layout-test-home-*` directories came from the
  `internal/layout` `TestMain` (fix open as #641);
  `/tmp/wb-go-test-shards-*` directories leak whenever a `wb run` is killed.
- `wb disk` measures and cannot reclaim; there is no `wb cache` command; the
  only deleting verb is `wb worktree gc`, which covered 0.2% of the problem.
- A stray `.worktrees/` directory sits inside the canonical wb clone.

## Synopsis

```
wb disk reclaim                               # plan every category; delete nothing
wb disk reclaim --apply                       # reclaim every category
wb disk reclaim --category go-build-cache --budget 8GiB --apply
wb disk reclaim --category scratch --older-than 72h --format json
```

## Behavior

### REQ: plan-by-default

Without `--apply` the command MUST NOT delete, move, or modify anything, and
MUST report per category the candidates, their RECLAIM bytes (the same
single-walk accounting `wb disk` uses, so hard-linked and shared content is
not double-counted), and the reason each candidate qualifies or is kept. With
`--apply` it MUST report reclaimed bytes per category and free space before
and after. Exit codes: `0` nothing to reclaim or all planned work applied;
`1` candidates remain in plan mode or any deletion failed; `2` usage.

### REQ: categories

| Category | Candidates | Method |
|---|---|---|
| `go-build-cache` | entries of `go env GOCACHE` | LRU trim to `--budget` (default `disk.go_build_cache.budget`, 10 GiB) |
| `scratch` | `$TMPDIR/wb-*` and `$TMPDIR/wb/<task>` | owner-proven or age-based, see REQ: scratch-ownership |
| `node-store` | the pnpm store | `pnpm store prune`, only when `pnpm` is installed |
| `wb-state` | lifecycle receipts, expired wait and run records | the existing collectors (`wb hooks lifecycle gc --apply`, …) with their own protections |
| `worktrees` | WB-managed checkouts | `wb worktree gc --apply` with default flags; never `--allow-residue` |
| `lifecycle-artifacts` | executor artifacts in worktrees over budget | LRU by last receipt, worktrees only ([Code Index Freshness](../code-index-freshness/README.md)) |

The Go module cache, Work Logs and fleet events are report-only here: the
first costs network to refill; retention of the others is founder
decision 5. `.worktrees/` directories inside canonical clones are reported
with a pointer to `wb layout audit` and never deleted by this command.

### REQ: go-cache-trim-is-safe

The Go cache trim MUST delete only cache entry files (`-a` action and `-d`
output files) in ascending last-use order, using each file's modification
time, which Go refreshes on use; this is the same per-file rule Go's own
cache trim applies, and a missing half of a pair is a cache miss to Go. It
MUST skip entries used in the last `--min-age` (default 24 h), MUST stop as
soon as the cache is under budget, and MUST leave `README`, `trim.txt` and the directory structure
intact. A build running concurrently MUST at worst see a cache miss, never
an error; the AC below proves it.

### REQ: scratch-ownership

A scratch directory MAY be removed only when one of these holds, checked in
order:

1. it is recorded in a worktree manifest (scratch-has-an-owner) whose task is
   retired;
2. it holds a `wb-owner` file naming a process whose PID and start time no
   longer match a live process (`wb run` MUST write this file for every shard
   and scratch directory it creates);
3. it has no owner record and nothing inside it was modified within
   `--older-than` (default 72 h); these are reported as `ownerless` so the
   leak's source can be found.

A directory with a live owner MUST be kept regardless of age.

### REQ: tests-clean-up

Every test in the wb repository MUST leave `os.TempDir()` as it found it:
per-test scratch through `t.TempDir()`, and any `TestMain` scratch removed
before `os.Exit`. CI MUST run a guard that lists `wb-*` entries in the temp
directory before and after `go test ./...` and fails, naming each new entry
and the package that created it where the name identifies it, when the run
leaves any behind. `wb run` MUST remove its own shard directories on normal
exit and on `SIGINT`/`SIGTERM`; a `SIGKILL` leak is covered by
REQ: scratch-ownership rule 2.

### REQ: trimpath-investigation

Before any change to Go flags, WB MUST settle with a recorded measurement
whether building with `-trimpath` makes identical packages in two worktree
paths share Go cache entries, and whether wb's tests still pass with it.
Tests that locate files through `runtime.Caller` (today
`cmd/wb/module_archive_test.go`) MUST first be made independent of absolute
source paths (package-relative paths; `go test` runs in the package
directory). If sharing is proven, `wb run` and `wb check` MUST pass
`-trimpath` (via `GOFLAGS`, appended, never replacing a user's value) for
`go build` and `go test`, and [Machine Setup](../machine-setup/README.md)
MAY offer it machine-wide; if refuted, the measurement is recorded in this
Feature and nothing changes.

### REQ: daemon-auto-reclaim

When `wb disk`'s free-space check (`--minimum-available`, default 10% of the
volume) fails, the daemon MUST run the safe subset — `go-build-cache`,
`scratch` rules 1 and 2 only, and `wb-state` — at most once per hour. It MUST
never run `worktrees`, `node-store`, or ownerless scratch automatically. Each
run MUST append a receipt (trigger, free before, per-category bytes, free
after) under `<projects-root>/.wb/disk/receipts/`, including runs that
reclaimed nothing, and a run that fails or leaves free space below the floor
MUST surface as a finding in `wb daemon status` and `wb disk`.

### REQ: machine-readable-output

`--format json|yaml` MUST emit
`{"mode":"plan|apply","filesystem":{...},"categories":[{"name","candidates":[{"path","bytes","reason","action":"delete|keep"}],"planned_bytes","reclaimed_bytes","errors":[]}]}`,
with the `filesystem` object shaped as in `wb disk`.

## Acceptance Criteria

### AC: plan-changes-nothing

**Requirements:** disk-reclaim#req:plan-by-default

**Given** a machine with a 12 GiB Go cache, three stale `wb-*` scratch
directories, and one landed worktree
**When** the user runs `wb disk reclaim --format json`
**Then** every category lists its candidates with bytes and reasons, a
recursive listing with modification times of every root is identical before
and after, and the exit code is `1`.

### AC: go-cache-trimmed-lru-to-budget

**Requirements:** disk-reclaim#req:go-cache-trim-is-safe

**Given** a `GOCACHE` of 12 GiB whose entries have staggered modification
times, some within the last hour
**When** the user runs `wb disk reclaim --category go-build-cache --budget 8GiB --apply`
**Then** the cache is at most 8 GiB, no remaining entry is older than any
deleted entry, no entry modified within 24 h was deleted, `README` and
`trim.txt` remain, and `go build ./...` in wb then succeeds.

### AC: concurrent-build-survives-trim

**Requirements:** disk-reclaim#req:go-cache-trim-is-safe

**Given** `go test ./internal/...` running in a worktree
**When** `wb disk reclaim --category go-build-cache --budget 1GiB --min-age 0s --apply`
runs concurrently, three times in a row
**Then** the test run's exit status equals that of an undisturbed run.

### AC: live-scratch-kept

**Requirements:** disk-reclaim#req:scratch-ownership

**Given** `/tmp/wb-go-test-shards-A` owned by a running `wb run` (older than
72 h by mtime), `/tmp/wb-go-test-shards-B` whose owner was killed with
`SIGKILL`, and an ownerless `/tmp/wb-layout-test-home-C` untouched for 5 days
**When** the user runs `wb disk reclaim --category scratch --apply`
**Then** A remains, B and C are removed, C is reported as `ownerless`, and
the exit code is `0`.

### AC: interrupted-run-cleans-shards

**Requirements:** disk-reclaim#req:tests-clean-up

**Given** `wb run -- go test ./internal/worktrees/...` with sharding active
**When** the run receives `SIGTERM`
**Then** no `wb-go-test-shards-*` directory created by that run remains.

### AC: test-leak-guard-fails-ci

**Requirements:** disk-reclaim#req:tests-clean-up

**Given** a test that creates `os.MkdirTemp("", "wb-leak-*")` and never
removes it
**When** the CI temp-directory guard runs around `go test ./...`
**Then** the job fails naming `wb-leak-…`; with the test removed, the guard
passes on the whole wb suite.

### AC: trimpath-sharing-measured

**Requirements:** disk-reclaim#req:trimpath-investigation

**Given** two worktrees of wb at the same commit, one empty `GOCACHE`, and
`GOFLAGS` unset
**When** `go build ./...` runs in worktree A and then in worktree B, and the
number of files added to `GOCACHE` by B is counted; then the same is repeated
with a fresh cache and `GOFLAGS=-trimpath`
**Then** the recorded result states both counts; sharing is proven when B
adds under 5% of A's entries with `-trimpath` and over 50% without.

### AC: trimpath-keeps-tests-green

**Requirements:** disk-reclaim#req:trimpath-investigation

**Given** `GOFLAGS=-trimpath`
**When** `wb check` runs wb's full test suite
**Then** it passes, including every test that uses `runtime.Caller`; if any
fails, the Feature records it and `-trimpath` is not adopted until it is fixed.

### AC: daemon-reclaims-below-floor

**Requirements:** disk-reclaim#req:daemon-auto-reclaim

**Given** a running daemon, `--minimum-available` effectively above the
current free share (set by configuration for the test), a large Go cache, an
ownerless scratch directory, and a landed worktree
**When** the daemon's next disk check fires
**Then** one receipt is written, the Go cache is trimmed, the ownerless
scratch and the worktree remain, and a second check within the hour does not
run again.

### AC: failed-auto-reclaim-is-visible

**Requirements:** disk-reclaim#req:daemon-auto-reclaim

**Given** an automatic run that leaves free space below the floor
**When** the user runs `wb daemon status` and `wb disk`
**Then** both report the finding with the receipt path, and both exit `1`.

### AC: canonical-strays-reported-not-deleted

**Requirements:** disk-reclaim#req:categories

**Given** a stray `.worktrees/` directory inside a canonical clone
**When** the user runs `wb disk reclaim --apply`
**Then** the directory is untouched and the output names it with a pointer to
`wb layout audit`.

## Non-goals

- Deleting the Go module cache, Work Logs or fleet events.
- Per-worktree caches; they multiply shared content (scratch-has-an-owner).
- Disk as a `wb run` admission dimension; that is scratch-has-an-owner part 4.

## Open Questions

- **Go cache budget.** 10 GiB is a placeholder. A share of the volume, or a
  fixed size per machine class? After `-trimpath`, the working set may be far
  smaller.
- **Retention for Work Logs and fleet events (founder decision 5).** Until
  decided, `wb-state` collects only lifecycle receipts and expired wait and
  run records.
- **Ownerless scratch in automatic runs.** Kept out of the daemon subset. If
  ownerless leaks persist after #641 and the `wb-owner` files ship, should the
  daemon remove ownerless scratch older than 7 days when below the floor?
- Should `node-store` also cover `npm cache verify`, which garbage-collects
  without deleting valid entries?

---
*This document follows the https://specscore.md/feature-specification*
