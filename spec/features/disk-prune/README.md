---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Disk Prune

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=request-change) |
**Status:** Draft
**Source Ideas:** disk-prune
**Related Ideas:** [scratch-has-an-owner](../../ideas/scratch-has-an-owner.md) (Draft; not promoted by this Feature)
**Depends On:** [Worktree Lifecycle](../worktree-lifecycle/README.md), [Cleanup Orchestration](../cleanup-orchestration/README.md), [Daemon Lifecycle Identity](../daemon-lifecycle/README.md)

## Summary

`wb disk prune` gives back the space `wb disk` reports. It plans by default
and acts only with `--apply`. It removes regenerable bytes that nothing is
using, cheapest to rebuild first, and stops once a free-space target is met:
ownerless scratch, the Go build cache by last use, unreferenced pnpm and npm
store entries, and Go module versions that no `go.sum` in the fleet
references. Docker is opt-in. WB state is reported, never pruned. Worktrees
stay with `wb clean`. Before any of that, `wb disk` must measure the right
places, which today it does not.

## Problem

The agent VM has run out of disk at least ten times (founder, 2026-11-27).
On 2026-11-27 `/` had 1.0 GB free of 150 GB and a `go build` failed with
"no space left on device". `wb disk` named the right categories, but its own
help says "nothing here deletes anything", and every recovery was done by
hand. `wb cache prune` was planned in the streams spec (commit 9a3480e9) and
never built.

Measuring the incident host turned up three accounting defects in `wb disk`
itself. Prune cannot act correctly on categories that are mismeasured:

1. **It measures the retired state directory.** Since v0.135.0 (2026-09-16)
   state lives at `<projects-root>/.wb`, but `wb disk` never passes the
   resolved home, so `wb-state` falls back to `$HOME/.wb`. On the incident
   host that directory held 10 GB: 3.5 GB of legacy-layout worktrees, about
   5.5 GB of hand-made report directories no WB code writes, 607 MB of
   hand-made backups, and a live hub store (`~/.wb/hub`, still the hub's
   default). Work Logs were 30 MB.
2. **It misses the current checkout store.** The `worktrees` category scans
   only `<root>/{org}/{repo}/.worktrees`. The fleet uses
   `<root>/{host}/{org}/{repo}`, and the central store is
   `<root>/.worktrees`, which held 2.7 GB unreported on 2026-11-27.
3. **It counts nested roots twice.** `~/.wb/worktrees` is a `worktrees` root
   inside the `wb-state` root, and the walk attributes its bytes to both
   categories, in reclaim and in apparent size
   (`internal/disk/nested_root_test.go`).

## Clean versus prune

The two verbs answer different questions about different bytes, and keeping
them apart is deliberate (founder, 2026-11-27).

Think of the machine as a workshop shared by many agents. Each worktree is a
**workbench** where one task is done. Every bench draws from a **shared
storeroom**: compiled Go packages, downloaded modules, Node packages, and
scraps on the floor of `/tmp`.

- **`wb clean` clears finished workbenches.** It asks "was this work
  delivered?" and retires a worktree only when its commits have provably
  landed. Its worst mistake is losing unlanded work, so it checks Git, the
  pull request, the Work Log and the locks for every candidate. Agents run it
  after every landing.
- **`wb disk prune` empties the storeroom.** Nothing in it is anyone's work;
  everything can be rebuilt or downloaded again. It asks "is anyone using
  this, or does any checkout still need it?" Its worst mistake is a slow
  rebuild, or breaking a build that is running, so it checks the process
  table and the fleet's `go.sum` files. It runs when the disk is low.

```text
                           THE DISK
 ┌──────────────────────────────────────────────────────────────┐
 │  WORKBENCHES (one per task)        SHARED STOREROOM          │
 │  ┌─────────┐ ┌─────────┐          ┌──────────────────────┐   │
 │  │ task A  │ │ task B  │          │ Go build cache       │   │
 │  │ landed  │ │ in work │          │ Go module downloads  │   │
 │  └─────────┘ └─────────┘          │ pnpm / npm stores    │   │
 │  ┌─────────┐                      │ /tmp scratch         │   │
 │  │ task C  │                      │ Docker (opt-in)      │   │
 │  │ landed  │                      └──────────────────────┘   │
 │  └─────────┘                                ▲                │
 │       ▲                                     │                │
 │   wb clean                            wb disk prune          │
 │   "was this delivered?"               "is anyone using it,   │
 │   risk: losing work                    does anything need    │
 │   runs: after every landing            it?"                  │
 │                                       risk: slow rebuild     │
 │                                       runs: disk is low      │
 │                                                              │
 │  RECORDS (<root>/.wb, ~/.wb): Work Logs, locks, claims, hub, │
 │  reports. Neither verb deletes these; prune reports on them. │
 └──────────────────────────────────────────────────────────────┘
        wb disk measures all of it and names the verb for each part
```

They meet at one boundary: a worktree's own `node_modules` lives in the
worktree, so `wb clean` reclaims it with the bench. Prune never touches a
worktree, however large; it reports the bytes and names `wb clean`.

## Behavior

### REQ: accounting-roots

`wb disk` and `wb disk prune` MUST derive every WB root from
`wbhome.Resolve` for the selected projects root, never from `$HOME`:

- `wb-state` is the write layout's state directory, `<root>/.wb`.
- `worktrees` is every read layout's checkout store (`<root>/.worktrees`,
  the legacy `~/.wb/worktrees` when present) plus the repository-local
  `.worktrees` of every canonical clone `discover.ScanLocal` finds, at
  whatever depth the placement uses.
- The retired `$HOME/.wb`, when it exists and is not the write layout's
  state directory, is its own category, `legacy-wb-home`.

When one category root lies inside another's, the inner root MUST be
measured only under its own category and excluded from the outer walk, so
no byte is attributed to two categories. A root that cannot be read is
listed under `skipped` with the error, as today.

### REQ: state-is-report-only

`wb-state` and `legacy-wb-home` MUST never be pruned. Prune reports each
first-level entry in one of four classes, with its reclaim bytes, and lists
the ten largest second-level entries:

| Class | Entries | What the report says |
|---|---|---|
| `protected` | `worklogs`, `worktrees` (task shells, locks, claims), `locks`, `hub`, `agents`, `streams`, `sessions`, `hook-runtime`, `runtime` | never deleted by any verb |
| `wb-reports` | `reports/<name>` where `<name>` is a directory a WB writer creates (`worktree-merge`, `worktree-cleanup`, `pr-update`, `default-branch`, `archive-clean`, `deps-graph-*`) | written by WB; retention is a later decision |
| `legacy-checkouts` | `legacy-wb-home` `worktrees/` | retire with `wb clean` |
| `unknown-origin` | everything else, for example hand-made `reports/<name>` and `backups/` | not written by WB; a person decides |

The set of WB writer directories MUST be pinned by a test that fails when a
writer under `internal/` adds a new `reports/` subdirectory without listing
it.

### REQ: plan-by-default

Without `--apply`, prune MUST NOT delete, move, truncate, chmod or otherwise
modify anything, and MUST NOT run a delegated tool in a mutating form. The
plan lists, per category in pruning order: each candidate (or, for caches
pruned as a whole by a tool, the category), its measured reclaim bytes from
the single accounting walk, the reason it is safe, the projected free space
after it, and the reason for each item kept. Items the plan would not reach
because the target is met before them are listed as `beyond-target`.

`--apply` executes the same plan, re-checking every safety condition
immediately before each deletion, and reports bytes reclaimed per category
with free space before and after.

### REQ: selection

- `--until-free <pct|size>` sets the target: a percentage of the
  filesystem (`15%`) or a size of free space (`20GiB`). The default is the
  floor `wb disk` flags, `disk.minimum_available` (0.10). `max` means no
  target: everything eligible. Prune stops between items once free space on
  the filesystem holding the projects root reaches the target. A category
  whose roots are on another filesystem counts toward that filesystem's
  free space, which is reported separately; the target applies to the
  projects root's filesystem only.
- `--category <name>` (repeatable) restricts the run to those categories.
  Valid names: `scratch`, `go-build-cache`, `node-package-store`,
  `go-module-cache`, `docker`. An unknown name, or a report-only category
  (`wb-state`, `legacy-wb-home`, `worktrees`), is a usage error.
- `--older-than <dur>` raises every age floor below to at least `dur`. It
  never lowers one.
- The pruning order is fixed, cheapest to rebuild first:
  1. `scratch`
  2. `go-build-cache` trim by last use
  3. `node-package-store` (`pnpm store prune`, then `npm cache verify`)
  4. `docker`, only when selected
  5. `go-module-cache` unreferenced versions
  6. `go-build-cache` full clean, the last resort (REQ: go-build-cache)

  The full clean comes after module pruning, not straight after the trim,
  because a cold build cache costs every agent a full rebuild while
  unreferenced modules cost nothing until someone needs them again.

### REQ: concurrency

- **One prune at a time.** `--apply` MUST hold an exclusive `flock` on
  `<root>/.wb/locks/disk-prune.lock` for the whole run. A second `--apply`
  reports `skipped: another prune is running (pid N)` and exits `1`
  without deleting anything. Plan mode takes no lock.
- **Admission.** `--apply` MUST be admitted through `runqueue.AdmitExplicit`
  with one unit before its first deletion, so it queues behind heavy jobs
  WB already coordinates, and reports any wait. Admission is not the safety
  check: agents run `go build` outside `wb run`, so prune MUST still check
  the process table.
- **Held open.** On Linux, a candidate path is `held-open` when any process
  whose `/proc/<pid>` the user can read has its working directory, an open
  file descriptor, or a memory-mapped file inside it. A held-open candidate
  is skipped and reported with the PID and command. Processes the user
  cannot read are not evidence. Because the caches are under the user's
  home with user-only permissions, other users' processes cannot be using
  them.
- **Toolchain busy.** A category is `busy` while any readable process
  runs a named tool that uses it: Go (`go`, `compile`, `link`, `asm`, `cgo`,
  `vet`, `gopls`, any `*.test` binary under a `go-build*` directory) for
  `go-module-cache` and the full build-cache clean; `pnpm` for the pnpm
  store; `npm` for the npm cache; `docker` builds for `docker`. A busy
  category is skipped whole and reported with the PIDs.
- **Other platforms.** Where the process table cannot be read, held-open
  and busy checks cannot be proven. Every category that depends on them is
  skipped as `unsupported-platform`. Only the age-guarded build-cache trim
  runs.

Every skip names its reason, its paths or PIDs, and the bytes it left.

### REQ: scratch

Candidates are direct entries of the temporary directory the invoking user
owns: `wb/<task>`, `wb-*`, `go-build*`, and `t.TempDir` shapes (`Test`,
`Benchmark` or `Fuzz` + `<Name><digits>` holding numbered
subdirectories). Symlinks are never followed; a symlinked entry is reported
`not-a-directory` and kept. Entries other users own are listed as
`foreign-owner` and kept without raising the exit code.

An entry may be removed only when it is not held open and:

- `go-build*`: no Go toolchain process is running, or no file under it was
  modified within 24 h. A live `go` process may hold no descriptor in its
  work directory between steps, so held-open alone is not enough.
- `wb/<task>`: no worktree, claim or parked session for `<task>` exists in
  any read layout, and no file under it was modified within 72 h.
- everything else: no file under it was modified within 72 h.

WB has no scratch ownership registry yet
([scratch-has-an-owner](../../ideas/scratch-has-an-owner.md)), so for
`wb-*` and `t.TempDir` shapes age is the only ownership evidence. That is
why their floor is the longest.

### REQ: go-build-cache

Go refreshes an entry's modification time only when it is more than an hour
old, and hands cache paths straight to the compiler and linker, so deleting
an entry a running build is about to read is a hard error, not a cache miss.

- **Trim.** Delete only entry files (`<2 hex>/<hash>-a` and `-d`) of
  `go env GOCACHE`, oldest modification time first. Never delete one
  modified within `--min-age` (default 24 h; at least 2 h, else a usage
  error). Stop once the target is met. `README`, `trim.txt` and the
  directory tree stay.
- **Full clean.** Only when the target is still unmet after every earlier
  step, no Go toolchain process is running, and the trim reached its age
  floor, run `go clean -cache`. Plan mode reports it as the last item with
  the cache's reclaim bytes.

### REQ: node-package-store

pnpm hard-links every store file into every consumer's `node_modules`, so
deleting a store file directly corrupts installs. Prune MUST NOT delete
store files itself.

- **pnpm.** When `pnpm` is on `PATH` and not busy, run `pnpm store prune`.
  The plan's estimate is the store's reclaim bytes from the walk: files
  with no link outside the store. That is what `pnpm store prune` removes.
- **npm.** When `npm` is on `PATH` and not busy, run `npm cache verify`,
  which garbage-collects content no index entry references. The plan
  reports `estimate: not-predictable` because npm has no dry run.
- A missing tool is reported `unavailable` and is not a finding.

### REQ: go-module-cache

- **Reference set.** The set is every module version named in any `go.sum`
  or `go.work.sum` inside every canonical clone `discover.ScanLocal` finds
  and every worktree in every read layout. Searches skip `vendor/`,
  `node_modules/`, `testdata/` and dot-directories, and a line counts in
  either its `<version>` or `<version>/go.mod` form. Only the files in
  their current working trees count; other branches' `go.sum` files are not
  read, and a later switch to such a branch costs only a re-download.
- **Fail closed.** If any checkout cannot be enumerated or any `go.sum`
  cannot be read or parsed, the whole category is skipped as
  `incomplete-reference-set`, with the paths.
- **Candidates.** A version is a candidate only when it is not in the set
  and its download files (`cache/download/<escaped>/@v/<version>.*`) are
  older than `--older-than` (default 7 days). Go never refreshes module
  files on use, so this is the download age. For each candidate prune
  removes its download files and its extracted
  `<escaped>@<version>/` directory. Paths are escaped with
  `golang.org/x/mod/module`.
- **Always kept.** `cache/download/sumdb/`, `cache/vcs/`, every `@v/list`
  file, and every version of `golang.org/toolchain`. A toolchain is selected
  by `go`/`toolchain` directives in `go.mod`, not by `go.sum`, so the
  reference set cannot vouch for it.
- **Permissions.** Go writes extracted modules read-only. Before removing a
  candidate, prune adds owner write permission to that candidate's
  directories only, after checking that their resolved path is inside
  `GOMODCACHE`.
- **Busy.** The category is skipped while it is busy (REQ: concurrency).
- Prune MUST NOT run `go clean -modcache`. Clearing the whole module cache
  is outside this verb; a user who wants it runs that command themselves.

### REQ: docker

Docker is never pruned unless `--category docker` is given. When given and
the `docker` CLI is available and not busy, prune removes:

- dangling images (`docker image prune --force`, no `--all`);
- stopped containers (status `exited`, `created` or `dead`) that carry the
  label `dev.wb.agents.run-id` or whose name starts with `wb-agent-`.

Running containers, tagged images, volumes, networks and the build cache are
never touched. The plan lists each container and image with its size as
Docker reports it.

### REQ: cross-references

- `wb disk` findings MUST name the verb that acts on each flagged category:
  `wb disk prune` for scratch and the caches, `wb clean` for worktrees and
  `legacy-checkouts`, and "a person" for `unknown-origin`.
- `wb clean` MUST print one line naming `wb disk prune` when the projects
  root's filesystem is below the floor after its run.
- The help text of both verbs MUST carry the one-paragraph difference from
  "Clean versus prune", and the README MUST explain it with the diagram.

### REQ: exit-codes

The contract matches `wb disk`'s:

- `0`: nothing needs attention. In plan mode nothing is eligible, or the
  target is already met. With `--apply` the target was met (or, with
  `max`, every eligible item was removed) and no deletion failed.
- `1`: findings. In plan mode the plan has items to run. With `--apply`
  the target is unmet, a deletion failed, or another prune holds the lock.
- `2`: usage. An unknown or report-only `--category`, an unparsable
  `--until-free` or `--older-than`, or `--min-age` below 2 h.

### REQ: machine-readable-output

`--format json` emits one object on stdout:

```json
{"schema_version":1,"mode":"plan|apply","target":{"kind":"ratio|bytes|max","value":0.1},
 "filesystem":{"before":{},"after":{}},
 "categories":[{"name":"","order":1,"status":"planned|applied|skipped|beyond-target|report-only",
   "reason":"","planned_bytes":0,"reclaimed_bytes":0,
   "items":[{"path":"","bytes":0,"action":"delete|keep|tool","reason":""}],
   "skipped":[{"path":"","reason":"","pids":[0]}],"errors":[{"path":"","error":""}]}]}
```

`filesystem` entries are shaped as in `wb disk`. Progress and diagnostics go
to stderr only.

### REQ: daemon-auto-prune

`wb.yaml` gains `disk.auto_prune` (default `on`) and `disk.check_interval`
(default 10 m). At that interval the daemon checks free space against
`disk.minimum_available` and, below it, runs at most once per hour
`wb disk prune --auto`. That is the unattended-safe subset: scratch (all
rules), go-build-cache trim, and `pnpm store prune`. It never prunes Go
modules, never runs the full build-cache clean, never touches npm or Docker.
Each run appends a receipt (trigger, free before, bytes per category, free
after, skips) under `<root>/.wb/disk/receipts/`. A rate-limited check writes
none and `--auto` then reports `skipped: rate-limited` with exit `0`. A
failed run, or free space still below the floor, is a finding in
`wb daemon status` and `wb disk`.

### REQ: tests-clean-up

wb's tests MUST leave the temporary directory as they found it. CI MUST run
`go test ./...` under a fresh empty `TMPDIR` and fail, listing leftovers, if
it is not empty afterwards. This is a root-cause fix: scratch should not
accumulate in the first place.

### REQ: trimpath-investigation

Carried over unchanged from the disk-reclaim Draft. Before WB appends
`-trimpath` to Go commands it runs, WB MUST record, for
`wb check --profile ci` in two worktrees sharing one `GOCACHE`, the bytes
the second adds untrimmed, trimmed, and trimmed mixed with untrimmed plain
`go test` runs. `-trimpath` is adopted only if the mixed case adds fewer
bytes than the untrimmed case and the suite passes.

## Acceptance Criteria

### AC: disk-measures-current-layout

**Requirements:** disk-prune#req:accounting-roots

**Given** a projects root with `<root>/.wb/worklogs`, a checkout in
`<root>/.worktrees/<task>/github.com/acme/app`, a canonical clone at
`<root>/github.com/acme/app` with a repository-local `.worktrees/`, and a
retired `$HOME/.wb` holding `worktrees/<task>` and `reports/x`
**When** the user runs `wb disk --format json`
**Then** `wb-state` has root `<root>/.wb`; `worktrees` includes the central
store, the repository-local store and `$HOME/.wb/worktrees`;
`legacy-wb-home` has root `$HOME/.wb`; and the sum of category reclaim bytes
equals `attributed_bytes`.

### AC: nested-root-counted-once

**Requirements:** disk-prune#req:accounting-roots

**Given** a 1 MiB file under `$HOME/.wb/worktrees/task`
**When** `wb disk` measures
**Then** `worktrees` reports at least 1 MiB and `legacy-wb-home` reports
less than 1 MiB, in both reclaim and apparent bytes.

### AC: plan-changes-nothing

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:machine-readable-output

**Given** fake roots for every category, with stale scratch, an old build
cache, an unreferenced module version, and a fake `pnpm` that records its
arguments
**When** the user runs `wb disk prune --until-free max --format json`
**Then** a recursive listing of every root, with modes and modification
times, is identical before and after; the fake `pnpm` was not invoked with
`prune`; the output validates against the documented shape; exit is `1`.

### AC: stops-at-target

**Requirements:** disk-prune#req:selection

**Given** a fake filesystem probe that reports free space rising as files
are deleted, and candidates in three categories
**When** the user runs `wb disk prune --until-free 20% --apply`
**Then** deletion stops after the first item that brings free space to 20%,
later items are `beyond-target` and untouched, and exit is `0`.

### AC: order-is-fixed

**Requirements:** disk-prune#req:selection

**Given** eligible items in every category, Docker selected
**When** the plan is rendered
**Then** categories appear in the REQ's order, with the full build-cache
clean last.

### AC: held-open-scratch-kept

**Requirements:** disk-prune#req:scratch, disk-prune#req:concurrency

**Given** a fake `/proc` in which pid 4242 has an open descriptor inside
`go-build111`, pid 4243 has its working directory inside `wb-old-a`, and
pid 4244 maps a file in `wb-old-b`; plus an unheld `go-build222` older than
24 h, `wb/live-task` older than 72 h whose task has a worktree, and
`go-build333` owned by another user
**When** the user runs `wb disk prune --category scratch --until-free max --apply`
**Then** only `go-build222` is removed; the held entries are reported
`held-open` with their PIDs; `wb/live-task` is reported `owned-by-task`;
`go-build333` is `foreign-owner`; exit is `0`.

### AC: recheck-before-delete

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:concurrency

**Given** an eligible scratch entry that becomes held open after the plan
is built and before its deletion
**When** `--apply` reaches it
**Then** it is kept and reported `held-open`.

### AC: go-cache-trimmed-by-last-use

**Requirements:** disk-prune#req:go-build-cache

**Given** a fake `GOCACHE` of entry files with staggered modification
times, some within the last hour
**When** the user runs `wb disk prune --category go-build-cache --until-free <size> --apply`
**Then** no remaining entry is older than a deleted one, none modified
within 24 h was deleted, `README` and `trim.txt` remain, `go clean` was not
invoked, and `--min-age 90m` exits `2` before deleting anything.

### AC: full-clean-is-last-resort

**Requirements:** disk-prune#req:go-build-cache, disk-prune#req:concurrency

**Given** a target the trim cannot meet, then the same with a fake Go
toolchain process in `/proc`
**When** `--apply` runs
**Then** the first run invokes `go clean -cache` after module pruning; the
second skips it as `busy` naming the PID and exits `1`.

### AC: pnpm-through-its-own-command

**Requirements:** disk-prune#req:node-package-store

**Given** a pnpm store with hard-linked and unlinked files and a fake `pnpm`
**When** `--apply` runs, then runs again while a `pnpm` process exists
**Then** the first invokes `pnpm store prune` and deletes no store file
itself; the second reports the category `busy`.

### AC: modules-referenced-by-go-sum-kept

**Requirements:** disk-prune#req:go-module-cache

**Given** a fake `GOMODCACHE` holding `example.com/a` v1.0.0 and v1.1.0,
`example.com/B` v2.0.0 (escaped `!b`), and `golang.org/toolchain`
v0.0.1-go1.27.4, all read-only and downloaded 30 days ago; a canonical clone
whose `go.sum` names `example.com/a v1.1.0`; and a worktree whose nested
module's `go.sum` names `example.com/B v2.0.0/go.mod`
**When** the user runs `wb disk prune --category go-module-cache --until-free max --apply`
**Then** only `example.com/a@v1.0.0` and its download files are removed;
the toolchain, `sumdb/`, `vcs/` and every `@v/list` remain; nothing outside
`GOMODCACHE` changed mode.

### AC: unreadable-go-sum-fails-closed

**Requirements:** disk-prune#req:go-module-cache

**Given** one worktree whose `go.sum` cannot be read
**When** prune runs
**Then** `go-module-cache` is `skipped: incomplete-reference-set` naming the
path, and no module file is removed.

### AC: state-reported-never-pruned

**Requirements:** disk-prune#req:state-is-report-only

**Given** `<root>/.wb` and a retired `$HOME/.wb` holding `worklogs/`,
`hub/`, `reports/worktree-merge/`, `reports/hand-made/`, `backups/x` and
`worktrees/<task>`
**When** the user runs `wb disk prune --until-free max --apply`
**Then** nothing under either directory changed, and the report classifies
each entry as the REQ's table says; `--category wb-state` exits `2`.

### AC: docker-opt-in

**Requirements:** disk-prune#req:docker

**Given** a fake `docker` reporting a dangling image, a running and a stopped
`wb-agent-` container, and a stopped unlabelled container
**When** prune runs without `--category docker`, then with it and `--apply`
**Then** the first never invokes `docker`; the second removes the dangling
image and the stopped `wb-agent-` container only.

### AC: one-prune-at-a-time

**Requirements:** disk-prune#req:concurrency, disk-prune#req:exit-codes

**Given** the prune lock held by another process
**When** the user runs `wb disk prune --apply`
**Then** nothing is deleted, the output names the holder, and exit is `1`.

### AC: findings-point-to-the-verb

**Requirements:** disk-prune#req:cross-references

**Given** a disk below the floor with stale scratch and a legacy home
**When** the user runs `wb disk`, and then `wb clean`
**Then** the `wb disk` findings name `wb disk prune` for scratch and
`wb clean` for legacy checkouts, and `wb clean` ends with a line naming
`wb disk prune`.

### AC: real-directory-dry-run

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:accounting-roots

**Given** this machine's real projects root, caches and temporary directory
**When** `wb disk prune --until-free max --format json` runs
**Then** it exits `0` or `1`, every category is present, a before-and-after
listing (paths, sizes, modes, modification times) of every root is
identical, and no `pnpm`, `npm`, `go clean` or `docker` mutating command
ran.

### AC: daemon-prunes-below-floor

**Requirements:** disk-prune#req:daemon-auto-prune

**Given** `disk.minimum_available: 0.99`, `disk.check_interval: 5s`, a stale
build cache, an unreferenced module version and a landed worktree
**When** the daemon runs for 15 s, then the user runs
`wb disk prune --auto --format json`
**Then** exactly one receipt was written, the build cache was trimmed, the
module version and the worktree remain, and the manual run reports
`skipped: rate-limited`.

### AC: test-leak-guard-fails-ci

**Requirements:** disk-prune#req:tests-clean-up

**Given** a test that calls `os.MkdirTemp("", "leak-*")` and never removes it
**When** the CI job runs `go test ./...` under a fresh `TMPDIR`
**Then** the job fails listing `leak-…`; without that test the whole wb
suite passes it.

### AC: trimpath-measured

**Requirements:** disk-prune#req:trimpath-investigation

**Given** two worktrees of wb at one commit and an empty shared `GOCACHE`
**When** the three runs the REQ names complete
**Then** the Feature records the bytes each run added and the suite result,
and the adopt-or-not outcome follows the REQ's rule.

## Delivery Slices

0. **Accounting:** accounting-roots, state-is-report-only (report side),
   cross-references for `wb disk`. ACs: disk-measures-current-layout,
   nested-root-counted-once.
1. **The verb:** plan-by-default, selection, concurrency, scratch,
   go-build-cache, node-package-store, go-module-cache, docker,
   cross-references, exit-codes, machine-readable-output. ACs:
   everything not listed in slices 0 or 2–3.
2. **Daemon auto-prune:** daemon-auto-prune. AC: daemon-prunes-below-floor.
   A separate pull request after slice 1 lands.
3. **Root causes:** tests-clean-up, trimpath-investigation. ACs:
   test-leak-guard-fails-ci, trimpath-measured. Separate pull requests.

The `wb clean` progress fixes this work depends on are specified in
[Cleanup Orchestration](../cleanup-orchestration/README.md)
(`progress-liveness`, `progress-carries-a-denominator`,
`progress-off-terminal`) and ship alongside slice 0.

## Open Questions

- Retention for `wb-reports`: how long should WB keep its own receipts, and
  which must outlive a landing for audit?
- Should hand-made artifacts get a sanctioned home with a declared
  retention, so a later prune can act on them?
- Lifecycle artifacts in worktrees (`.codegraph` and similar, from
  code-index-freshness) were in the disk-reclaim Draft. They live inside
  worktrees, so they may belong to `wb clean` instead.
- Moving the hub store default off `~/.wb/hub` and draining `~/.wb`
  entirely is a separate Idea.

---
*This document follows the https://specscore.md/feature-specification*
