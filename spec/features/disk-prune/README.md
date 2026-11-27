---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Disk Prune

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/disk-prune?op=request-change) |
**Status:** Draft
**Source Ideas:** disk-prune
**Related Ideas:** [scratch-has-an-owner](../../ideas/scratch-has-an-owner.md), [retire-legacy-wb-home](../../ideas/retire-legacy-wb-home.md) (both Draft; not promoted by this Feature)
**Depends On:** [Worktree Lifecycle](../worktree-lifecycle/README.md), [Cleanup Orchestration](../cleanup-orchestration/README.md)

## Summary

`wb disk prune` gives back the part of the space `wb disk` reports that is
safe to rebuild, with two rules a person can check: it removes this user's
own old scratch from the temporary directory, and it clears the Go build
cache while no Go process runs. It plans by default and removes only with
`--apply`. Everything else (worktrees, WB state, the Go module cache, the npm
and pnpm stores) is measured and never pruned. Before any of that, `wb disk`
must measure the right places, which it did not.

The founder chose this simple version on 2026-11-27, after six adversarial
review rounds of a version that proved from outside that nothing used each
cache entry kept finding ever narrower cases. A smaller rule set with a small,
named risk is preferred to a large one no review can close.

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
 │  └─────────┘ └─────────┘          │                      │   │
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

`wb disk` MUST measure exactly these roots:

| Category | Roots |
|---|---|
| `wb-state` | the write layout's state directory, `<root>/.wb`, including its `worktrees/` namespace of task shells, locks and claims |
| `worktrees` | `<root>/.worktrees`; `$HOME/.wb/worktrees` when present; the `.worktrees` directory of every canonical clone under the projects root, at any placement depth; the older `<root>/{org}/{repo}/.worktrees` |
| `legacy-wb-home` | `$HOME/.wb` when that directory exists and is not `<root>/.wb`, whether or not it still holds checkouts |
| `go-build-cache` | the resolved `GOCACHE` |
| `go-module-cache` | the resolved `GOMODCACHE` |
| `node-package-store` | `~/.local/share/pnpm/store` and `~/Library/pnpm/store` when present, and `$HOME/.npm` |
| `scratch` | every direct entry of each temporary directory (REQ: scratch) that the invoking user owns and whose name starts with `wb-`, `wb` (the directory), `go-build`, `Test`, `Benchmark` or `Fuzz`; deletion candidates are a subset |

Measurement rules:

- Every root is resolved with `filepath.EvalSymlinks` before it is measured
  and before nesting is decided.
- When one root lies inside another, the inner root is measured only under
  its own category and skipped by the outer walk.
- When two categories resolve to the same directory, it belongs to the
  first in the table's order. The other category lists it as
  `same-as <category>`.
- No directory entry is visited twice.
- The sum of per-category reclaim is at most `attributed_bytes`. It is less
  when a file is hard-linked across categories, because that file comes
  back only when every category holding it goes.
- A root that fails to stat for any reason other than not existing is
  recorded with the error.
- Every subtree or entry the walk could not read is recorded, and the
  measurement is marked `partial`. A partial figure is a lower bound.

Go settings are resolved without running Go, because even `go env` can
write telemetry counters. Each of `GOCACHE`, `GOMODCACHE`, `GOPATH`,
`GOPROXY`, `GOSUMDB`, `GOPRIVATE`, `GONOPROXY`, `GONOSUMDB`, `GOFLAGS` and
`GOCACHEPROG` is resolved in this order:

1. the process environment;
2. Go's environment file (`$GOENV`, else `<user config dir>/go/env`), parsed
   read-only exactly as cmd/go parses it (split at LF, nothing trimmed);
3. the toolchain's `$GOROOT/go.env` (`GOROOT` from the environment, else
   from the user's env file, else the directory above the `bin/` of the
   `go` on `PATH`);
4. Go's documented default (`<user cache dir>/go-build`,
   `<first GOPATH entry>/pkg/mod`, `https://proxy.golang.org,direct`,
   `sum.golang.org`).

A key present in the user's env file wins over the toolchain's file even
when its value is empty; the empty value then takes Go's default.

prune uses the same resolution to find `GOCACHE`.

### REQ: state-is-report-only

Worktrees, `wb-state`, `legacy-wb-home`, the Go module cache and the npm and
pnpm stores MUST never be pruned. `wb disk` measures them and names the verb
that acts on them (`wb clean` for worktrees; `go clean -modcache` and
`pnpm store prune` for a person to run). `--category` naming any of them is a
usage error.

### REQ: plan-by-default

Without `--apply`, prune MUST change nothing: it lists what it would remove,
with sizes, and exits `1` when there is work.

### REQ: scratch

Scratch is a direct entry of a temporary directory (`os.TempDir()` and
`GOTMPDIR`, each resolved to an absolute path and deduplicated; one that
cannot be resolved is left out, never cleaned lexically into another path)
whose name is one of:

- `go-build<digits>` (go build and go test work directories);
- `Test…<digits>`, `Benchmark…<digits>`, `Fuzz…<digits>` (what `t.TempDir`
  and friends create);
- WB's own regenerable directories: `wb-index-<digits>`,
  `wb-coverage-baseline-<digits>` and the other prefixes listed in the code,
  and per-task Go caches `wb-<task>-cache` / `wb-<task>-go-cache`, where
  `<task>` uses only letters, digits, `.`, `_` and `-`.

Nothing else in the temporary directory is ever considered. A temporary
directory that is, or lies inside, a git checkout (a `.git` in it or an
ancestor) is skipped whole: its entries are that checkout's files. When Go's
settings cannot be read, the Go caches cannot be located, and scratch
removes nothing (`failed`). A candidate is
removed only when all of these hold; otherwise it is kept and counted under
its reason:

- it is a real directory, not a symlink, owned by the invoking user;
- it is on the same filesystem as its temporary directory, and it is not, and
  does not hold, the resolved `GOCACHE` or `GOMODCACHE`;
- no directory below it is on another filesystem, no mount point listed in
  `/proc/self/mountinfo` lies inside it (a bind mount on the same filesystem
  included), and it holds no `.git` and no socket or FIFO;
- nothing in it, files or directories, was modified in the last 3 days.
  `--older-than` raises that (a Go duration such as `100h`, or whole days
  such as `5d`) and can never lower it.

Its identity is taken before it is measured or inspected, and immediately
before removal the name must still be that directory and the mount points
are read again; a directory replaced meanwhile, or one that gained a mount,
is kept. Removal never follows a symlink. A tree Go wrote read-only is made
writable (directories only, through a descriptor opened without following
symlinks) and removed. A temporary directory that exists but cannot be listed, unreadable
mount points, or a cancelled run make the category `failed`.

### REQ: go-build-cache

With `--apply`, prune runs `go clean -cache` against the resolved `GOCACHE`
(symlinks resolved once, so the check, the measurement and the clean name
one directory), with `GOFLAGS` set to a single space, only when no Go
process is running. A blank `GOFLAGS` overrides Go's env files and parses as
no flags; an empty one would let a `GOFLAGS=-modcache` there widen the clean
to the module cache. A Go process is one whose `comm`, or the base name
of whose `argv[0]`, is a Go tool (`go`, `compile`, `link`, `asm`, `cgo`,
`vet`, `cover`, `gopls`, `dlv`, `golangci-lint`, `staticcheck`,
`govulncheck`) or ends in `.test`. The check is made when planning and again
immediately before the clean. A process whose `comm` or `cmdline` cannot be
read for any reason other than having exited means prune cannot tell.

The category is skipped, and the run exits `1`, when a Go process runs (the
PIDs are named) or when that cannot be told (no `/proc`, as on macOS). It is
skipped without a finding when `GOCACHEPROG` is set, `GOCACHE` is `off` or
relative, there is no cache, or `GOCACHE` and `GOMODCACHE` overlap (the
clean would take the module cache with it), and kept as `nothing to clear`
when no shard (two lowercase hex digits) holds an entry. The size counts
only the shards the clean removes. Go's settings or `GOCACHE` that cannot be inspected make
the category `failed`. Go trims its build cache itself day to day;
this category is for a full disk.

### REQ: cross-references

Below `disk.minimum_available` (10% by default, in the user's `wb.yaml`),
`wb disk` and `wb clean` name `wb disk prune`. Both verbs' help carries the
same clean-versus-prune paragraph. There is no `wb cache` command, and
`wb commands --search "cache"` finds `wb disk prune`.

### REQ: exit-codes

`0` nothing needs attention; `1` findings (a plan with work, a category
skipped because Go was running or could not be checked, or a failure); `2`
usage, before anything is read.

### REQ: machine-readable-output

`--format json` writes the report: `schema_version`, `mode`
(`plan`/`apply`), `outcome` (`done`, `planned`, `skipped`, `failed`), and per
category its `status`, `reason`, `bytes`, the `items` it acts on (path,
bytes, status, error) and the `kept` counts by reason.

## Accepted Risks

The founder accepted these on 2026-11-27 in exchange for simplicity:

1. **A process that pauses for more than 3 days** with a scratch directory
   it will come back to can lose it. No running build or test does.
2. **A Go process that starts between the final check and `go clean`** can
   miss cache entries and rebuild them; the cache is a cache.
3. **Prune does not look at which processes hold scratch open.** The 3-day
   rule stands in for that.

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

### AC: nested-and-equal-roots-counted-once

**Requirements:** disk-prune#req:accounting-roots

**Given** a 1 MiB file under `$HOME/.wb/worktrees/task`; a second file
hard-linked between that worktree and a path outside every root; and
`GOMODCACHE` set to the same directory as `GOCACHE`
**When** `wb disk` measures
**Then** the first file counts under `worktrees` only, in both reclaim and
apparent bytes; the hard-linked file is not reclaimable; the shared Go
directory counts under `go-build-cache` with `go-module-cache` listing
`same-as go-build-cache`; and the sum of category reclaim is at most
`attributed_bytes`.

### AC: symlinked-and-unreadable-roots

**Requirements:** disk-prune#req:accounting-roots

**Given** `GOMODCACHE` reached through a symlink, and an unreadable
subdirectory under the build cache
**When** `wb disk` measures
**Then** the module cache is measured at its target with non-zero bytes, and
the build cache is marked `partial` with the unreadable path.

### AC: plan-changes-nothing

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:scratch

**Given** a temporary directory with old `go-build1`, `TestFoo123`,
`wb-index-7` and `wb-x-go-cache` directories and an unrelated `my-notes`
**When** the user runs `wb disk prune`, then `wb disk prune --apply`
**Then** the plan lists the four producers and changes nothing; apply
removes exactly those four and keeps `my-notes`.

### AC: scratch-kept-when-not-safely-old

**Requirements:** disk-prune#req:scratch

**Given** scratch-named entries that are: changed an hour ago, a symlink, a
plain file, another user's, on another filesystem, holding a mount, holding
a `.git`, holding a socket
**When** prune runs with `--apply`
**Then** each is kept and counted under its reason, and the run exits `0`.

### AC: go-cache-cleared-only-while-idle

**Requirements:** disk-prune#req:go-build-cache

**Given** a Go build cache
**When** prune runs with `--apply` while a `go` process runs, then where
`/proc` is missing, then with no Go process
**Then** the first two skip the category (naming the PIDs, or why it cannot
tell) and exit `1`; the third runs `go clean -cache` with that `GOCACHE` and
an empty `GOFLAGS`, and exits `0`.

### AC: flags-validated-first

**Requirements:** disk-prune#req:exit-codes

**Given** any machine
**When** the user runs `--category go-module-cache`, `--category=`,
`--category=scratch,`, `--older-than 1h`, `--older-than=` or `--format yaml`
**Then** each exits `2` before anything is read.

### AC: findings-point-to-the-verb

**Requirements:** disk-prune#req:cross-references

**Given** a disk below the floor
**When** the user runs `wb disk`, then `wb clean --format json`, then
`wb cache prune`, then `wb commands --search "cache"`
**Then**:
- the `wb disk` findings name `wb disk prune` for scratch and `wb clean` for
  worktrees;
- `wb clean` writes one stderr line naming `wb disk prune` and leaves its
  JSON stdout valid;
- `wb cache prune` fails with the existing unknown-command hint;
- the search lists `wb disk prune` first.

## Delivery Slices

All in one pull request (founder, 2026-11-27):

0. **Accounting:** accounting-roots and cross-references for `wb disk`.
1. **`wb clean` liveness:** cleanup-orchestration `progress-off-terminal`.
2. **The verb:** every remaining REQ.

Not in this Feature: pruning the Go module cache (issue #1019; a reviewed
implementation is preserved at commit 715776ad), the pnpm store, npm and
Docker, and any unattended (daemon) prune.

## Open Questions

- Should WB's own report writers drop a marker file, so their output can be
  retired by a rule instead of by hand?
- Lifecycle artifacts in worktrees (`.codegraph` and similar) likely belong
  to `wb clean`.

---
*This document follows the https://specscore.md/feature-specification*
