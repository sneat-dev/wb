---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Disk Prune

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=request-change) |
**Status:** Draft
**Source Ideas:** disk-prune
**Related Ideas:** [scratch-has-an-owner](../../ideas/scratch-has-an-owner.md), [retire-legacy-wb-home](../../ideas/retire-legacy-wb-home.md) (both Draft; not promoted by this Feature)
**Depends On:** [Worktree Lifecycle](../worktree-lifecycle/README.md), [Cleanup Orchestration](../cleanup-orchestration/README.md), [Daemon Lifecycle Identity](../daemon-lifecycle/README.md)

## Summary

`wb disk prune` gives back the space `wb disk` reports. It plans by default
and deletes only with `--apply`. It removes regenerable bytes that nothing is
using, cheapest to rebuild first, and stops once each affected filesystem
meets its free-space target. That covers scratch whose producer is known,
the Go build cache by last use, and unreferenced pnpm store entries; Go
module versions only on request. WB state is reported, never pruned.
Worktrees stay with `wb clean`. Before any of that, `wb disk` must measure
the right places, which today it does not.

## Problem

The agent VM has run out of disk at least ten times (founder, 2026-11-27).
On 2026-11-27 `/` had 1.0 GB free of 150 GB, the volume holding
`GOMODCACHE` had 364 MB, and a `go build` failed with "no space left on
device". `wb disk` named the right categories, but its own help says
"nothing here deletes anything", and every recovery was done by hand.
`wb cache prune` was planned in the streams spec (commit 9a3480e9) and never
built; it does not exist, and this Feature does not add it.

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
3. **It counts nested roots twice.** The legacy `~/.wb/worktrees` lies
   inside the legacy home, and the walk attributes its bytes to both, in
   reclaim and in apparent size.

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
  this, and can it come back?" Its worst mistake is breaking a build that is
  running, so it checks the process table, ages and the fleet's `go.sum`
  files. It runs when the disk is low.

```text
                           THE DISK
 ┌──────────────────────────────────────────────────────────────┐
 │  WORKBENCHES (one per task)        SHARED STOREROOM          │
 │  ┌─────────┐ ┌─────────┐          ┌──────────────────────┐   │
 │  │ task A  │ │ task B  │          │ Go build cache       │   │
 │  │ landed  │ │ in work │          │ Go module downloads  │   │
 │  └─────────┘ └─────────┘          │ pnpm store           │   │
 │  ┌─────────┐                      │ /tmp scratch         │   │
 │  │ task C  │                      └──────────────────────┘   │
 │  │ landed  │                                ▲                │
 │  └─────────┘                                │                │
 │       ▲                                     │                │
 │   wb clean                            wb disk prune          │
 │   "was this delivered?"               "is anyone using it,   │
 │   risk: losing work                    can it come back?"    │
 │   runs: after every landing           risk: broken build     │
 │                                       runs: disk is low      │
 │                                                              │
 │  RECORDS (<root>/.wb, ~/.wb): Work Logs, locks, claims, hub, │
 │  reports. Neither verb deletes these; prune reports on them. │
 └──────────────────────────────────────────────────────────────┘
        wb disk measures all of it and names the verb for each part
```

They meet at one boundary: a worktree's own `node_modules` lives in the
worktree, so `wb clean` reclaims it with the bench. Prune never touches a
worktree or any Git checkout, however large or wherever it is; it reports
the bytes and names `wb clean`.

## Behavior

### REQ: accounting-roots

`wb disk` and `wb disk prune` MUST measure exactly these roots, each resolved
with `filepath.EvalSymlinks` before measuring and before deciding nesting:

| Category | Roots |
|---|---|
| `wb-state` | the write layout's state directory, `<root>/.wb` (including its `worktrees/` namespace of task shells, locks and claims) |
| `worktrees` | `<root>/.worktrees`; `$HOME/.wb/worktrees` when present; the `.worktrees` directory of every canonical clone found under the projects root at any placement depth; the older `<root>/{org}/{repo}/.worktrees` |
| `legacy-wb-home` | `$HOME/.wb` when that directory exists and is not `<root>/.wb`, whether or not it still holds checkouts |
| `go-build-cache` | `go env GOCACHE` |
| `go-module-cache` | `go env GOMODCACHE` |
| `node-package-store` | the pnpm store path (REQ: node-package-store) and `$HOME/.npm` |
| `scratch` | the candidates of REQ: scratch |

`go env` runs with `GOTOOLCHAIN=local` from the filesystem root, so
resolving a path can never download a toolchain. When one root lies inside
another, the inner root is measured only under its own category and skipped
by the outer walk; no directory entry is visited twice. The sum of
per-category reclaim is at most `attributed_bytes`. It is less when a file
is hard-linked across categories, because that file is reclaimable only
when both go. A root that fails to stat for a reason other than not
existing, and every subtree a walk could not read, is recorded with the
error. A measurement that skipped anything is marked `partial`, and its
bytes are a lower bound.

### REQ: state-is-report-only

`wb-state` and `legacy-wb-home` MUST never be pruned. For each, prune reports
its entries in three classes, with reclaim bytes, and lists the ten largest
second-level entries:

| Class | Entries | What the report says |
|---|---|---|
| `wb-written` | `reports/<name>` where `<name>` is a directory name a static WB writer uses | written by WB; a retention rule is future work |
| `origin-not-proven` | every other entry under `reports/` and `backups/` | WB cannot prove who wrote it; it may be WB's (some writers use dynamic names) or hand-made; a person decides |
| `wb-state` | everything else | WB's own records; never deleted by any verb |

The legacy home's `worktrees/` is reported under `worktrees`, not here. A test
MUST fail when a non-test source file joins a string literal under
`"reports"` that the `wb-written` list does not name.

### REQ: plan-by-default

Without `--apply`, prune MUST NOT create, delete, rename, truncate, chmod or
lock anything, and MUST run delegated tools only in read-only forms
(`go env`, `pnpm store path`). This is enforced by structure: the planner
receives a read-only filesystem and command interface that has no mutating
operations.

The plan lists, per filesystem and then per category in pruning order:
each candidate (or, for the pnpm store, the category), its reclaim bytes,
the reason it is safe, the projected free space after it, and the reason
for every item kept. Reclaim and projections come from one accounting walk
over the exact selected set: two candidates whose last links are shared
reclaim together what neither reclaims alone. Items the plan would not
reach because the target is met first are listed as `beyond-target`.

`--apply` executes the plan through REQ: deletion, re-checking every safety
condition immediately before each item, and reports per item what was
removed, kept or skipped, with each filesystem's free space before and after.

Prune MUST NOT reuse the `wb clean` inventory walk, which purges terminal
artifacts while it enumerates. Every fact prune needs about worktrees,
claims, Work Logs and parked sessions comes from a read-only enumeration:
directory listings, file reads and `git worktree list --porcelain`.

### REQ: selection

- `--until-free <pct|size>` sets the target for every filesystem that
  holds a candidate: a share of that filesystem (`15%`) or an amount of
  free space (`20GiB`). The default is `disk.minimum_available`, a new
  `wb.yaml` key that `wb disk` also reads for its floor (default 0.10;
  `--minimum-available` still overrides it). `max` means no target.
- Each filesystem is pruned only while it is below its target. A candidate
  on a filesystem already at its target is `beyond-target`. A filesystem is
  identified by the device of its root.
- `--category <name>` (repeatable) restricts the run. Valid names are
  `scratch`, `go-build-cache`, `node-package-store` and `go-module-cache`.
  An unknown or report-only name (`wb-state`, `legacy-wb-home`,
  `worktrees`) is a usage error.
- `go-module-cache` runs only when `--category go-module-cache` is given.
  A run without `--category` covers the other three.
- `--older-than <dur>` raises every category's age floor to at least `dur`
  and never lowers one. Each category's default floor is stated in its REQ.
- The order is fixed, cheapest to rebuild first: `scratch`,
  `go-build-cache` trim, `node-package-store`, `go-module-cache`, then
  `go-build-cache` full clean when `--allow-full-clean` is given.
- **No escalation into nothing.** After each category, prune compares each
  filesystem's observed free-space gain with the bytes it removed there.
  When it removed more than 256 MiB and gained less than a tenth of that,
  it stops on that filesystem and reports `space-not-returned`. That
  usually means open deleted files, a snapshot, or reflinks.

### REQ: concurrency

- **One prune per user.** `--apply` and `--auto` MUST hold an exclusive
  `flock` on the user's home directory, opened read-only, for the whole run.
  That needs no file creation, so it works on a full disk, and it
  serialises every projects root and daemon of that user. All the caches
  prune touches are per-user. The holder's PID is written best-effort to
  `<root>/.wb/disk/prune.pid`. A run that cannot take the lock deletes
  nothing, reports `another prune is running` with the PID when known, and
  exits `1`.
- **Admission.** When every affected filesystem is at or above
  `disk.minimum_available`, `--apply` is admitted through
  `runqueue.AdmitExplicit` with one unit and waits at most 60 s, then
  proceeds and reports the wait. Below the floor it bypasses admission and
  reports `admission-bypassed: below floor`, so the remedy never queues
  behind the builds failing for lack of space. Admission is not the safety
  check, because agents run `go build` outside `wb run`.
- **Process evidence (Linux).** Prune reads `/proc` once per category and
  again immediately before each deletion. A process *references* a path
  when its working directory, an open file descriptor, a memory-mapped file
  (`maps`), its command line, or its environment names a location inside
  that path. Failing to list `/proc` makes every category that depends on
  this evidence `unknown` and skipped.
- **Tool recognition.** A process is a *Go tool* when the base name of its
  `exe` or `argv[0]` is one of `go`, `compile`, `link`, `asm`, `cgo`, `vet`,
  `gopls`, `golangci-lint`, `staticcheck`, `govulncheck` or `dlv`, or ends in
  `.test`. It is a *pnpm process* when its `exe` or `argv[0]` base name is
  `pnpm`, or it is `node` with a command-line argument whose base name
  starts with `pnpm`.
- **Fail closed.** A process whose `cwd`, `fd` or `maps` the user cannot
  read counts as referencing every candidate when its command line or name
  identifies it as a Go tool or pnpm process, or when its user is the
  invoking user. It is ignored otherwise.
- **Other platforms.** Without `/proc`, only the build-cache trim runs, with
  its non-Linux floor. Every other category is `unsupported-platform`.

Every skip names its reason, the PIDs and command lines that caused it, and
the bytes it left. A fail-closed skip (`unknown`, `unsupported-platform`,
`incomplete-reference-set`) is a finding.

### REQ: deletion

Every deletion MUST be bound to the object the safety checks examined:

1. Open the candidate's parent with `O_DIRECTORY|O_NOFOLLOW` and stat the
   candidate relative to it without following symlinks. The device and
   inode must equal those recorded when it was checked, and the type must
   be the one planned. Otherwise skip it as `changed`.
2. Rename it, relative to the parent, into a quarantine directory
   `.wb-prune-<pid>` that prune created in the same parent. A rename fails
   rather than crossing filesystems.
3. Re-run the category's checks on the quarantined object. If one now
   fails, rename it back and skip it as `held-open` or `changed`.
4. Remove the quarantined tree through directory descriptors (`openat`,
   `unlinkat`, `fchmodat`), never by path. Never follow a symlink. Never
   descend into a directory on a different device (a mount): report it
   and keep it.
5. Remove the empty quarantine directory.

A quarantine directory left by a prune that died is completed by the next
prune, after the same identity checks on its contents. Any candidate whose
resolved path lies inside a protected root is refused as `protected`,
whatever its category: the state directories, every `worktrees` root, and
every canonical clone. That stops a misconfigured `GOCACHE` or `TMPDIR`
pointing into one of them. Bulk deletion of build-cache entry files uses
`unlinkat` relative to the open shard directory, with the same identity
check per file.

### REQ: zero-free-space

Prune MUST work on a filesystem with no free bytes. Before its first
deletion it creates nothing: the lock is on an existing directory, and the
quarantine directory is created only on the filesystem it is freeing,
where a failed `mkdir` makes prune fall back to unlinking that candidate
in place, still through REQ: deletion's identity check. PID files,
receipts and reports are written after deletion, best-effort, and a failure
to write them is reported, not fatal.

### REQ: scratch

Candidates are the direct entries of `os.TempDir()` and of `$GOTMPDIR` when
set. An entry is a candidate only if the invoking user owns it, it is a
real directory (not a symlink), and its name matches one of these producer
shapes:

| Shape | Producer | Default floor |
|---|---|---|
| `go-build<digits>` | a Go command's work directory | 1 h, and 24 h while any Go tool runs |
| `Test…`, `Benchmark…`, `Fuzz…` + `<Name><digits>` holding numbered subdirectories | `testing.T.TempDir` of a killed test | 1 h, and 24 h while any Go tool runs |
| `wb-*-cache`, `wb-*-go-cache`, `wb-go-test-shards-*`, `wb-test-cpu-queue-*` | WB's per-task caches and queues | 72 h |
| `wb/<task>` | WB's per-task scratch | 72 h, and only when `<task>` has no worktree, claim, Work Log claim, lock or parked session in any layout |

An entry's age is measured from the newest modification *or status-change*
time of the entry itself and of everything under it, because `cp -a`, `tar`
and `rsync` preserve old modification times. An entry is removed only when
it is older than its floor and no process references it. Never remove one
that contains a `.git` file or directory at any depth, or that any canonical
clone's `git worktree list` registers: it is reported as
`checkout-in-scratch`. Any other `wb-*` entry, and every entry another user
owns, is listed as `not-a-known-producer` or `foreign-owner` and kept,
without raising the exit code.

### REQ: go-build-cache

Go refreshes an entry's modification time only when it is more than an hour
old, and hands cache paths straight to the compiler and linker. Deleting an
entry a running build is about to read is a hard error, not a cache miss.

- **Trim.** Prune deletes only entry files of `GOCACHE` (`<2 hex>/<hash>-a`
  and `-d`), oldest first, stopping once the target is met. The floor is
  the larger of `--older-than` (default 24 h) and the age of the oldest
  running Go tool plus 1 h. Without `/proc` the floor is 72 h and cannot be
  lowered. `README`, `trim.txt`, `testexpire.txt`, the `fuzz/` tree
  (corpus data that is not regenerable) and the directory tree are kept.
- **Unsupported configurations.** When `GOCACHE` is `off`, empty, relative
  or not a directory, or `GOCACHEPROG` is set, the category is skipped as
  `unsupported-cache`.
- **Full clean.** `go clean -cache` runs only with `--allow-full-clean`,
  never under `--auto`, never implied by `--until-free max`, and only when
  no Go tool is running immediately before. A Go tool that starts during
  the clean can fail once. That residual risk is accepted, stated in the
  flag's help, and is why the flag exists. Prune checks the process table
  again afterwards and reports any Go tool that started during the clean.

### REQ: node-package-store

pnpm hard-links every store file into every consumer's `node_modules`, so
deleting a store file directly corrupts installs. Prune MUST NOT delete
store files itself.

- **Resolving the store.** Run `pnpm store path` from `/` with
  `COREPACK_ENABLE_DOWNLOAD_PROMPT=0` and `COREPACK_ENABLE_AUTO_PIN=0`.
  The result is the store prune measures and prunes. If `pnpm` is missing
  or the command fails, the category is `unavailable`, which is not a
  finding.
- **Pruning.** When no pnpm process is running, run
  `pnpm store prune --store-dir <path>` from `/` with the same environment.
  If pnpm refuses because a store server is running, report that.
- **Estimate.** The plan's estimate is that store path's reclaim bytes:
  files with no link outside the store, which is what `pnpm store prune`
  removes.
- **Cost.** A worktree whose lockfile names a pruned package but which has
  no `node_modules` yet will download it again on its next install.
  Offline reinstallation is not guaranteed, and the plan says so.
- The npm cache is measured but not pruned.

### REQ: go-module-cache

This category runs only when `--category go-module-cache` is given.

- **Reference set.** Every module version named in any `go.sum` or
  `go.work.sum` inside every canonical clone, and every worktree that any
  clone's `git worktree list --porcelain` registers or any worktrees root
  holds. The search skips `vendor/`, `node_modules/`, `testdata/` and
  dot-directories only below a checkout root. Only current working trees
  are read; a later switch to a branch with other dependencies downloads
  them again.
- **Grammar.** A `go.sum` line is `<module> <version>[/go.mod] h1:<hash>`.
  A non-empty line of any other form, including a merge-conflict marker, is
  a parse failure.
- **Fail closed.** If any directory in the enumeration cannot be read, or
  any `go.sum` cannot be read or parsed, the whole category is skipped as
  `incomplete-reference-set`, with the paths.
- **Provable re-fetch.** A version may be deleted only if it can be
  downloaded again:
  - `go env GOPROXY` names at least one proxy URL before any `off`;
  - `GOFLAGS` does not contain `-mod=vendor`;
  - the module path is matched by none of `GOPRIVATE`, `GONOPROXY` and
    `GONOSUMDB`.

  Otherwise the version is kept as `not-refetchable`.
- **Candidates.** A version is a candidate when it is not in the reference
  set, can be re-fetched, and the newest modification or status-change time
  of its download files (`cache/download/<escaped>/@v/<version>.*`) is older
  than `--older-than` (default 7 days). Go never refreshes module files on
  use, so this is the download age. Module paths are escaped with
  `golang.org/x/mod/module`.
- **Scope.** The reference set covers the fleet under this projects root
  only. Tools installed with `go install`, other projects roots, and
  checkouts elsewhere are not seen. Their unreferenced versions are
  downloaded again on next use, which is why the category is opt-in.
- **Always kept.** `cache/download/sumdb/`, `cache/vcs/`, every `@v/list`
  and `@v/<version>.lock` file, and every version of
  `golang.org/toolchain`, because a toolchain is selected by `go` and
  `toolchain` directives, not by `go.sum`.
- **Busy.** The category is skipped as `busy` while any Go tool runs whose
  working directory is outside every checkout in the reference set, or
  whose command line is `go get`, `go mod …`, `go install <pkg>@<version>`
  or `go run <pkg>@<version>`. A Go tool working inside a fleet checkout
  can only use versions its `go.sum` lists, and those are kept.
- **Crash-safe order.** For each candidate version:
  1. Take Go's own lock on `@v/<version>.lock` with `flock`, as `cmd/go`
     does, and keep the lock file.
  2. Create `<escaped>@<version>.partial`, Go's marker for an incomplete
     extraction.
  3. Remove `@v/<version>.ziphash`.
  4. Remove the extracted `<escaped>@<version>/` directory through REQ:
     deletion. Before each directory's entries are removed, add owner
     write permission to that directory through its descriptor.
  5. Remove `@v/<version>.zip`, `.mod` and `.info`.
  6. Remove the `.partial` marker and release the lock.

  Killed at any step, Go sees the version as missing or partial and
  downloads it again; it never trusts a half-deleted directory.
- Prune MUST NOT run `go clean -modcache`.

### REQ: progress

Prune MUST follow cleanup-orchestration `progress-off-terminal`. On a
terminal or with `--verbose` it prints one line per category and item as it
starts and finishes. Otherwise, unless `--quiet` is given, it prints a
heartbeat at least every 15 s: the phase (`measure`, `references`, `plan`,
`apply`), the category, the elapsed time, and bytes removed so far.

### REQ: cross-references

- `wb disk` findings MUST name the verb that acts on each flagged category:
  `wb disk prune` for scratch and the caches, `wb clean` for worktrees, and
  "a person decides" for `legacy-wb-home`.
- `wb clean` MUST print one line to stderr naming `wb disk prune` when the
  projects root's filesystem is below `disk.minimum_available` after its
  run.
- The help text of both verbs carries the one-paragraph difference from
  "Clean versus prune", and the README explains it with the diagram.
- `wb disk prune`'s search terms include "cache prune", "prune cache",
  "free disk space" and "no space left on device". The existing
  unknown-command hint for `wb cache prune`, `wb commands --search
  "cache"`, then lists `wb disk prune` first.

### REQ: exit-codes

| Exit | When |
|---|---|
| `0` | Plan: nothing eligible, or every filesystem already at its target. Apply: every filesystem reached its target (or, with `max`, every eligible item was removed), nothing failed, and no fail-closed skip occurred. |
| `1` | Plan: items to run, or a fail-closed skip. Apply: a target unmet, a deletion failed, a fail-closed skip, `space-not-returned`, the lock held elsewhere, or cancellation. |
| `2` | An unknown or report-only `--category`; an unparsable `--until-free` or `--older-than`; `--allow-full-clean` without `go-build-cache` selected; `--auto` combined with `--category`, `--until-free` or `--allow-full-clean`. Every flag is validated before anything is read. |

The JSON `outcome` field distinguishes the causes of `1`: `planned`,
`target-unmet`, `failed`, `fail-closed`, `space-not-returned`, `locked`,
`cancelled`.

### REQ: machine-readable-output

`--format json` writes one object to stdout. Progress and diagnostics go to
stderr only.

```json
{"schema_version":1,"mode":"plan|apply|auto","outcome":"done|planned|target-unmet|failed|fail-closed|space-not-returned|locked|cancelled",
 "lock":{"held":true,"wait_ms":0},"admission":{"status":"admitted|bypassed|timed-out","wait_ms":0},
 "filesystems":[{"device":"","path":"","target":{"kind":"ratio|bytes|max","value":0.1},
   "before":{"total_bytes":0,"available_bytes":0},"after":{"total_bytes":0,"available_bytes":0},
   "target_met":true,"removed_bytes":0}],
 "categories":[{"name":"","order":1,"status":"planned|applied|partial|skipped|beyond-target|report-only",
   "reason":"","estimate_bytes":0,"removed_bytes":0,
   "items":[{"path":"","bytes":0,"status":"delete|deleted|keep|beyond-target|skipped|failed","reason":"",
     "pids":[{"pid":0,"command":""}],"error":""}]}],
 "state":[{"root":"","entries":[{"path":"","class":"wb-written|origin-not-proven|wb-state","bytes":0}]}]}
```

`estimate_bytes` is `null` when no estimate exists. `filesystem` values are
shaped as in `wb disk`.

### REQ: daemon-auto-prune

- `wb.yaml` gains `disk.auto_prune` (default `off`) and
  `disk.check_interval` (default 10 m).
- When `disk.auto_prune` is on, the daemon checks every filesystem that
  holds a category root at that interval. When any is below
  `disk.minimum_available`, it runs `wb disk prune --auto`, at most once
  per hour.
- `--auto` implies `--apply` with the floor as target, and runs only
  scratch, the build-cache trim and the pnpm store prune. It never prunes
  Go modules and never runs the full clean. The hourly limit applies only
  to daemon-triggered runs; a manual `--auto` always runs, under the lock.
- Each run appends a receipt under `<root>/.wb/disk/receipts/` (trigger,
  free space before and after per filesystem, bytes per category, skips),
  best-effort per REQ: zero-free-space.
- Exit codes follow REQ: exit-codes, so a run that leaves a filesystem
  below the floor exits `1`. A failed run, or free space still below the
  floor, is a finding in `wb daemon status` and `wb disk`.
- The daemon resolves roots in its own environment and records them in the
  receipt. A daemon whose `TMPDIR` or Go environment differs from the
  agents' prunes what it resolves and says so.

## Acceptance Criteria

### AC: disk-measures-current-layout

**Requirements:** disk-prune#req:accounting-roots

**Given** a projects root with `<root>/.wb/worklogs` and
`<root>/.wb/worktrees/<task>.lock`, a checkout in
`<root>/.worktrees/<task>/github.com/acme/app`, a canonical clone at
`<root>/github.com/acme/app` with a repository-local `.worktrees/`, and a
retired `$HOME/.wb` holding only `reports/x`
**When** the user runs `wb disk --format json`
**Then** `wb-state` has the single root `<root>/.wb`, including the lock;
`worktrees` includes the central store and the repository-local store;
`legacy-wb-home` has root `$HOME/.wb`; and no root is absent from the report
without a `skipped` entry.

### AC: nested-root-counted-once

**Requirements:** disk-prune#req:accounting-roots

**Given** a 1 MiB file under `$HOME/.wb/worktrees/task`, and a second file
hard-linked between that worktree and a path outside every root
**When** `wb disk` measures
**Then** the first file counts under `worktrees` only, in both reclaim and
apparent bytes; the hard-linked file is not reclaimable; and the sum of
category reclaim is at most `attributed_bytes`.

### AC: symlinked-and-unreadable-roots

**Requirements:** disk-prune#req:accounting-roots

**Given** `GOMODCACHE` reached through a symlink, and an unreadable
subdirectory under the build cache
**When** `wb disk` measures
**Then** the module cache is measured at its target with non-zero bytes, and
the build cache is marked `partial` with the unreadable path.

### AC: plan-changes-nothing

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:machine-readable-output

**Given** fake roots for every category, with eligible items in each, and
fake `pnpm` and `go` binaries that record their arguments
**When** the user runs
`wb disk prune --category scratch --category go-build-cache --category node-package-store --category go-module-cache --until-free max --format json`
**Then** a recursive listing of every root, with modes and modification
times, is identical before and after; the recorder saw only `go env` and
`pnpm store path`; the output validates against the documented shape; exit
is `1` with outcome `planned`.

### AC: real-directory-dry-run

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:accounting-roots

**Given** this machine's real projects root, caches and temporary directory,
and an exec recorder wrapped around the real tools
**When** the planner runs with `--until-free max` and every category
selected
**Then** it completes with every category present, the recorder saw only
read-only commands, and the planner was given no mutating filesystem
operation to call.

### AC: per-filesystem-target

**Requirements:** disk-prune#req:selection

**Given** two fake filesystems, A at 50% free holding the scratch root and B
at 1% free holding `GOCACHE`, and a probe whose free space rises as files
are deleted
**When** the user runs `wb disk prune --until-free 20% --apply`
**Then** nothing on A is deleted (`beyond-target`), B's trim stops after the
first entry that brings it to 20%, and exit is `0`.

### AC: space-not-returned-stops-escalation

**Requirements:** disk-prune#req:selection

**Given** a probe whose free space does not rise after 1 GiB of deletions on
one filesystem
**When** `--apply` runs
**Then** prune stops on that filesystem after that category, reports
`space-not-returned`, and exits `1`.

### AC: scratch-producers-only

**Requirements:** disk-prune#req:scratch, disk-prune#req:concurrency

**Given** a fake `/proc` and these temporary entries, all older than 4 days
unless stated otherwise:
- pid 4242 has an open descriptor inside `go-build111`;
- pid 4243 has `wb-pr1-test-cache` in its environment;
- `go-build222` and `wb-pr2-lint-go-cache` are unheld;
- `wb-wt-x` holds a `.git` file;
- `wb-cache-y/repo` holds a `.git` directory;
- `wb-notes` is unheld;
- `wb/live-task` belongs to a task that has a Work Log claim;
- `go-build333` is owned by another user;
- `go-build444` was copied with `cp -a`, so its modification times are old
  but its status-change time is 10 minutes ago

**When** the user runs `wb disk prune --category scratch --until-free max --apply`
**Then**:
- only `go-build222` and `wb-pr2-lint-go-cache` are removed;
- `go-build111` and `wb-pr1-test-cache` are `held-open` with their PIDs;
- `wb-wt-x` and `wb-cache-y` are `checkout-in-scratch`;
- `wb-notes` is `not-a-known-producer`;
- `wb/live-task` is `owned-by-task`;
- `go-build333` is `foreign-owner`;
- `go-build444` is kept as too young;
- exit is `0`.

### AC: unreadable-process-fails-closed

**Requirements:** disk-prune#req:concurrency

**Given** a fake `/proc` entry with command line `go build ./...` whose
`fd`, `cwd` and `maps` are unreadable, and, separately, a `/proc` that
cannot be listed
**When** prune plans scratch and the module cache
**Then** the first makes Go-produced scratch and the module cache `busy`;
the second makes both `unknown`; and both outcomes are findings (exit `1`).

### AC: pnpm-under-node-is-detected

**Requirements:** disk-prune#req:concurrency, disk-prune#req:node-package-store

**Given** a pnpm store with hard-linked and unlinked files, a fake `pnpm`,
and a fake `/proc` with `node /usr/lib/node_modules/pnpm/bin/pnpm.cjs install`
**When** `--apply` runs, then runs again after that process exits
**Then** the first reports the category `busy` and does not invoke
`pnpm store prune`; the second invokes
`pnpm store prune --store-dir <path>` from `/` and deletes no store file
itself.

### AC: deletion-is-bound-to-the-checked-object

**Requirements:** disk-prune#req:deletion

**Given** three eligible scratch entries:
- the first is replaced by a symlink to a directory holding a file
  `precious` after it was checked;
- the second is replaced by a new directory with the same name;
- the third contains a mount point (a fake device change) above a file

**When** `--apply` reaches them
**Then**:
- the symlink target and `precious` survive, and the first entry is skipped
  as `changed`;
- the second entry survives, skipped as `changed`;
- the third's mounted subtree survives and is reported, while the rest of
  that entry is removed.

### AC: protected-roots-override

**Requirements:** disk-prune#req:deletion

**Given** `GOCACHE` set to a directory inside `<root>/.wb`
**When** `--apply` runs
**Then** no file under `<root>/.wb` is removed and the category reports
`protected`.

### AC: interrupted-prune-is-completed

**Requirements:** disk-prune#req:deletion, disk-prune#req:go-module-cache

**Given** a prune killed after quarantining a scratch entry, and another
killed after removing a module version's `.ziphash` with its `.partial`
marker present
**When** the next prune runs with `--apply`
**Then** the quarantine is removed after its identity checks; the module
version is completed (all files removed, marker gone); and a fake
`go mod download` of that version is treated by the fixture's Go stub as a
fresh download.

### AC: works-on-a-full-disk

**Requirements:** disk-prune#req:zero-free-space, disk-prune#req:concurrency

**Given** a fake filesystem on which every create fails with `ENOSPC`, and
eligible build-cache entries
**When** `--apply` runs below the floor
**Then** the entries are deleted, admission is reported `bypassed`, the PID
file and receipt failures are reported, and exit reflects the target only.

### AC: go-cache-trimmed-by-last-use

**Requirements:** disk-prune#req:go-build-cache

**Given** a fake `GOCACHE` of entry files with staggered modification
times, a `fuzz/` tree and `testexpire.txt`, and a fake Go tool process
started 30 h ago
**When** the user runs
`wb disk prune --category go-build-cache --until-free <size> --apply`
**Then** no remaining entry is older than a deleted one; none modified
within 31 h was deleted; `README`, `trim.txt`, `testexpire.txt` and `fuzz/`
remain; and `go clean` was not invoked.

### AC: full-clean-needs-its-flag

**Requirements:** disk-prune#req:go-build-cache, disk-prune#req:exit-codes

**Given** a target the trim cannot meet
**When** prune runs with `--until-free max --apply`; then with
`--allow-full-clean`; then with `--allow-full-clean` while a fake Go tool
runs; then with `--auto --allow-full-clean`
**Then**:
- the first never invokes `go clean`;
- the second invokes `go clean -cache` last;
- the third skips it as `busy`, naming the PID, and exits `1`;
- the fourth exits `2`.

### AC: modules-referenced-or-unrefetchable-kept

**Requirements:** disk-prune#req:go-module-cache

**Given** a fake `GOMODCACHE`, all read-only and downloaded 30 days ago,
holding:
- `example.com/a` v1.0.0 and v1.1.0;
- `example.com/B` v2.0.0, escaped as `!b`;
- `corp.example/private` v1.0.0 under `GOPRIVATE=corp.example`;
- `golang.org/toolchain` v0.0.1-go1.27.4;
- `example.com/c` v1.0.0, downloaded 2 days ago.

The references are:
- a canonical clone whose `go.sum` names `example.com/a v1.1.0`;
- a repository-local worktree, found only through `git worktree list`,
  whose nested module's `go.sum` names `example.com/B v2.0.0/go.mod`;
- a `go.work.sum` naming nothing else.

**When** the user runs
`wb disk prune --category go-module-cache --until-free max --apply`
**Then**:
- only `example.com/a@v1.0.0` and its `.zip`, `.ziphash`, `.mod` and `.info`
  files are removed;
- its `.lock` file, every `@v/list`, `sumdb/`, `vcs/`, the toolchain and
  `example.com/c` remain;
- the private module is `not-refetchable`;
- nothing outside `GOMODCACHE` changed mode.

### AC: module-reference-set-fails-closed

**Requirements:** disk-prune#req:go-module-cache

**Given** one worktree whose `go.sum` holds a merge-conflict marker, then
one whose directory cannot be read
**When** prune runs on the module cache
**Then** each run skips it as `incomplete-reference-set` naming the path,
removes no module file, and exits `1`.

### AC: module-busy-outside-the-fleet

**Requirements:** disk-prune#req:go-module-cache, disk-prune#req:concurrency

**Given** a fake Go tool working in a fleet worktree, then one running
`go install example.com/tool@v1.0.0` from `$HOME`
**When** prune plans the module cache
**Then** the first does not make the category busy; the second does, naming
the PID and command line.

### AC: modules-not-in-the-default-run

**Requirements:** disk-prune#req:selection

**Given** an eligible module version
**When** the user runs `wb disk prune --until-free max --apply` without
`--category`
**Then** no module file is removed and the plan lists the category as
`requires --category go-module-cache` with its estimate.

### AC: state-reported-never-pruned

**Requirements:** disk-prune#req:state-is-report-only

**Given** `<root>/.wb` and a retired `$HOME/.wb`, each holding `worklogs/`,
`hub/`, `reports/worktree-merge/`, `reports/hand-made/` and `backups/x`
**When** the user runs `wb disk prune --until-free max --apply`
**Then** nothing under either directory changed; `reports/worktree-merge` is
`wb-written`; `reports/hand-made` and `backups/x` are `origin-not-proven`;
the rest is `wb-state`; and `--category wb-state` exits `2`.

### AC: one-prune-per-user

**Requirements:** disk-prune#req:concurrency, disk-prune#req:exit-codes

**Given** the user-level prune lock held by a process with another projects
root
**When** the user runs `wb disk prune --apply`
**Then** nothing is deleted, the output reports `another prune is running`,
outcome is `locked`, and exit is `1`.

### AC: flags-validated-first

**Requirements:** disk-prune#req:exit-codes, disk-prune#req:selection

**Given** any fixture
**When** prune runs with each of `--category docker`,
`--category worktrees`, `--until-free 12XB`, `--older-than soon` and
`--auto --category scratch`
**Then** each exits `2` before any filesystem read; and `--older-than 100h`
raises the scratch and trim floors to 100 h without lowering the module
floor.

### AC: unsupported-platform

**Requirements:** disk-prune#req:concurrency, disk-prune#req:go-build-cache

**Given** a process-table reader that reports itself unavailable
**When** prune runs `--apply` on every category
**Then** only build-cache entries older than 72 h are removed, every other
category is `unsupported-platform`, and exit is `1`.

### AC: findings-point-to-the-verb

**Requirements:** disk-prune#req:cross-references

**Given** a disk below the floor with stale scratch and a legacy home
**When** the user runs `wb disk`, then `wb clean --format json`, then
`wb commands --search "cache"`
**Then**:
- the `wb disk` findings name `wb disk prune` for scratch and `wb clean` for
  worktrees;
- `wb clean` writes one stderr line naming `wb disk prune` and leaves its
  JSON stdout valid;
- the search lists `wb disk prune` first.

### AC: prune-proves-it-is-alive

**Requirements:** disk-prune#req:progress

**Given** a fake measurement that takes 40 s under an injected clock, stderr
attached to a pipe
**When** prune plans without `--verbose`
**Then** stderr carries a heartbeat at least every 15 s naming the phase and
category, and stdout is only the report.

### AC: daemon-prunes-below-floor

**Requirements:** disk-prune#req:daemon-auto-prune

**Given** `disk.auto_prune: on`, `disk.minimum_available: 0.99`,
`disk.check_interval: 5s`, a stale build cache, an unreferenced module
version and a landed worktree
**When** the daemon runs for 15 s, and then the user runs
`wb disk prune --auto --format json`
**Then** exactly one daemon receipt was written; the build cache was
trimmed; the module version and the worktree remain; the manual run ran
(it is not rate-limited) and exits `1` because the floor is unreachable;
and with `disk.auto_prune` absent the daemon prunes nothing.

## Delivery Slices

All slices ship in one pull request (founder, 2026-11-27).

0. **Accounting:** accounting-roots, state-is-report-only (report side),
   cross-references for `wb disk`. ACs: disk-measures-current-layout,
   nested-root-counted-once, symlinked-and-unreadable-roots.
1. **`wb clean` liveness:** cleanup-orchestration `progress-off-terminal`,
   plus fixing the renderer's misattributed durations.
2. **The verb:** every remaining REQ except daemon-auto-prune, and every
   remaining AC except daemon-prunes-below-floor.
3. **Daemon:** daemon-auto-prune, default off. AC: daemon-prunes-below-floor.

Deferred to their own Features or slices:

- **Docker:** needs run-record ownership checks against WB's create-then-start
  window, and must keep untagged images WB runs by ID.
- **npm:** a running npm cannot be detected reliably, and `npm cache verify`
  needs free space.
- **Root causes:**
  - CI that runs the suite under a fresh `TMPDIR` and fails on leftovers;
  - the `-trimpath` measurement from the disk-reclaim Draft.

## Open Questions

- Retention for `wb-written` reports: how long should WB keep its own
  receipts, and which must outlive a landing for audit?
- Should every WB report writer drop a marker file, so origin is provable
  instead of inferred from names?
- Should hand-made artifacts get a sanctioned home with a declared
  retention, so a later prune can act on them?
- Lifecycle artifacts in worktrees (`.codegraph` and similar, from
  code-index-freshness) were in the disk-reclaim Draft. They live inside
  worktrees, so they likely belong to `wb clean`.
- Should the module reference set accept extra roots from `wb.yaml`, so
  tool checkouts outside the projects root count?

---
*This document follows the https://specscore.md/feature-specification*
