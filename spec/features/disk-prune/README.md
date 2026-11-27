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
and the Go build cache by last use. WB state, the Go module cache and the
pnpm store are reported, never pruned; Go module pruning is a later Feature.
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

`wb disk` and `wb disk prune` MUST measure exactly these roots:

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
   read-only;
3. Go's documented default (`<user cache dir>/go-build`,
   `<first GOPATH entry>/pkg/mod`, `https://proxy.golang.org,direct`,
   `sum.golang.org`).

A value prune cannot parse makes the dependent category
`unsupported-cache`.

### REQ: state-is-report-only

`wb-state` and `legacy-wb-home` MUST never be pruned. For each, prune reports
its entries in three classes, with reclaim bytes, and lists the ten largest
second-level entries:

| Class | Entries | What the report says |
|---|---|---|
| `wb-written` | `reports/<name>` where `<name>` is a directory name a static WB writer uses | written by WB; a retention rule is future work |
| `origin-not-proven` | every other entry under `reports/` and `backups/` | WB cannot prove who wrote it. It may be WB's, since some writers use dynamic names, or hand-made; a person decides |
| `wb-state` | everything else | WB's own records; never deleted by any verb |

The legacy home's `worktrees/` is reported under `worktrees`, not here. A
test MUST fail when a non-test source file joins a string literal under
`"reports"` that the `wb-written` list does not name.

### REQ: plan-by-default

Without `--apply`, prune MUST NOT create, delete, rename, truncate, chmod or
lock anything in any root it measures or manages. The only external command it runs is
`git worktree list --porcelain -z` in canonical clones. It reads `/proc`, and
it never runs `go`. Every mutation goes through a seam of the
World (tree and file removal, locks, admission, HTTP probes) that the
planner never calls; a test against this machine's real directories fails if
any is called, and records every external command the plan runs.

The plan is grouped by filesystem, then by category in pruning order. For
each candidate it lists:

- its reclaim bytes;
- the reason it is safe;
- the filesystem and the projected free space after it;
- the reason for every item kept.

Reclaim and projections come from one accounting walk over the exact
selected prefix, so two candidates whose last links are shared reclaim
together what neither reclaims alone. Items the plan would not reach,
because the target is met first, are listed as `beyond-target`.

`--apply` executes the plan through REQ: deletion. It re-checks every safety
condition immediately before each item and reports, per item, what was
removed, kept or skipped. For each filesystem it reports three figures
separately: the estimated reclaim, the bytes actually unlinked, and the
observed change in free space.

Prune MUST NOT reuse the `wb clean` inventory walk, which purges terminal
artifacts while it enumerates. Every fact prune needs about worktrees,
claims, Work Logs and parked sessions comes from a read-only enumeration:
directory listings, file reads and `git worktree list --porcelain -z`.

On `--apply`, prune writes only these, best-effort:

- the runqueue admission files, when admitted, before deletion;
- `<root>/.wb/disk/receipts/`, after deletion.

Every pre-existing entry under the state directories is left unchanged.

### REQ: selection

- **Target.** `--until-free <pct|size>` sets the target for every
  filesystem that holds a candidate: a share of that filesystem (`15%`) or
  an amount of free space (`20GiB`). The default is `disk.minimum_available`,
  a new key in the user-level `wb.yaml` (`~/.config/wb/wb.yaml`) that
  `wb disk` also reads for its floor. Its default is 0.10, and
  `--minimum-available` still overrides it. `max` means no target.
- **Per filesystem.** A filesystem, identified by the device of its root,
  is pruned only while it is below its target. A candidate on a filesystem
  already at its target is `beyond-target`. A byte target larger than the
  whole filesystem is reported `target-unreachable` and that filesystem is
  not pruned. It is a finding. Free space is probed again before each item
  that can be stopped independently: each scratch entry, each build-cache
  shard.
- **Categories.** `--category <name>` (repeatable) restricts the run. Valid
  names are `scratch` and `go-build-cache`. An unknown or report-only name
  (`wb-state`, `legacy-wb-home`, `worktrees`, `node-package-store`,
  `npm-cache`, `go-module-cache`) is a usage error. A category left out of
  `--category` is not walked and is reported `skipped: not-selected`, so the
  report never lists candidates the run would not act on.
- **Age floors.** `--older-than <dur>` (a Go duration such as `100h`, or
  whole days such as `3d`) raises every category's age floor to at least
  `dur` and never lowers one. Each category's default floor is
  stated in its REQ. The full build-cache clean is an indivisible
  operation that cannot honour an age. When `--older-than` is given it is
  skipped as `requested-step-skipped: age-filter-unsupported`.
- **Order.** The order is fixed, cheapest to rebuild first:
  1. `scratch`;
  2. `go-build-cache` trim;
  3. `go-build-cache` full clean, when `--allow-full-clean` is given.
- **No escalation into nothing.** After each category, prune compares each
  filesystem's observed free-space gain with the bytes it unlinked there.
  When it unlinked more than 256 MiB and gained less than a tenth of that,
  it stops on that filesystem and reports `space-not-returned`. That
  usually means open deleted files, a snapshot, or reflinks. Free space is
  probed again before the first deletion and between categories, and a
  filesystem that reaches its target by any means stops being pruned.

### REQ: concurrency

- **Run lock.** `--apply` and `--auto` MUST hold an exclusive `flock` on
  the user's home directory, opened read-only, for the whole run. That
  needs no file creation, so it works on a full disk. The holder is
  reported from `/proc/locks` when it is visible. A run that cannot take the
  lock deletes nothing, reports `another prune is running`, and exits `1`.
- **Resource locks.** Each category also takes an exclusive, non-blocking
  `flock` on its root directory (`GOCACHE`, each temporary
  directory) for as long as it works there. Two users or
  homes that share one physical cache are therefore serialised too. A
  category whose lock is held is skipped as `locked`. A root that is the
  run lock's own directory (`TMPDIR=$HOME`) is not locked a second time.
- **Lock failures.** A lock that cannot be taken for any reason, including
  `flock` being unsupported on a network home, means nothing under it is
  deleted, and the reason is reported.
- **Admission.** When every affected filesystem is at or above
  `disk.minimum_available`, `--apply` is admitted through
  `runqueue.AdmitExplicit` with one unit. It waits at most 60 s, then
  proceeds and reports the wait. Below the floor it bypasses admission and
  reports `admission-bypassed: below floor`, so the remedy never queues
  behind the builds that are failing for lack of space. Admission is not
  the safety check, because agents run `go build` outside `wb run`.
- **Process evidence (Linux).** Prune lists `/proc` once per category,
  again every 256 build-cache entries, and immediately before each deletion.
  A process *references* a path when any of these names a location inside
  it: its working directory, an open file descriptor, a memory-mapped file
  (`maps`, its pathname taken byte for byte), its command line (a relative
  argument resolved against its working directory), or its environment.
- **Evidence it cannot trust.** Process evidence is `unknown`, and every
  category that depends on it is skipped, when:
  - `/proc` cannot be listed;
  - `/proc` is mounted with `hidepid` above 0 and the category root is
    accessible to other users;
  - prune runs in a nested PID namespace, which shows as more than one
    `NSpid` entry in `/proc/self/status`;
  - prune runs in a container, shown by `/.dockerenv`,
    `/run/.containerenv` or `container=` in PID 1's environment. A
    container's own `/proc` can list a single PID namespace while host
    processes share its caches.
- **Go build tools.** A process is a *Go build tool* when the base name of
  its `exe` or `argv[0]` is one of `go`, `compile`, `link`, `asm`, `cgo`,
  `vet`, `golangci-lint`, `staticcheck` or `govulncheck`, or ends in
  `.test`. `gopls` and `dlv` are *Go tools* but not build tools. They can
  run for weeks, so they do not set the build-cache floor, though they do
  block the full clean.
- **pnpm processes.** A process is a *pnpm process* when its `exe` or
  `argv[0]` base name is `pnpm`, or it is `node` with a command-line
  argument whose base name starts with `pnpm`.
- **Unreadable processes.** A process whose `cwd`, `fd` or `maps` cannot be
  read counts as referencing every candidate only when its command line or
  name identifies it as a Go tool or pnpm process. That status is
  `busy-unverified`. A process that changed user, such as the per-session
  `sshd` child, is unreadable to its own user and is otherwise ignored.
- **Other platforms and untrusted evidence.** Without `/proc`, or with
  `unknown` process evidence, only the build-cache trim runs, at a fixed
  floor of 72 h. Every other category is `unsupported-platform` or
  `unknown`. The help says that prune run inside a container or a
  PID-namespace sandbox reports `unknown` for most categories.
- **Mount IDs.** Mount IDs come from `statx(STATX_MNT_ID)`, so Linux 5.8 or
  later is required. Earlier kernels give no mount ID, and which mount a
  lookup reaches cannot be established from `/proc/self/mountinfo` alone (a
  mount over a parent can hide a deeper one), so every category that
  deletes is `unknown` there. A bind-mount check finds the line of
  mountinfo carrying the statx mount ID.

Every skip names its reason, the PIDs and command lines that caused it, and
the bytes it left. These skips are fail-closed and count as findings:

- `unknown`;
- `busy-unverified`;
- `unsupported-platform`;
- `incomplete-reference-set`;
- `partial-scan`;
- `requested-step-skipped`, when a step the caller asked for by flag was
  skipped;
- `target-unreachable`.

`busy` caused by a readable process is an ordinary skip.

### REQ: deletion

Every deletion MUST act on the object the safety checks examined, never on
whatever a path names later:

1. **Open by descriptor.** Open the candidate's parent with
   `O_DIRECTORY|O_NOFOLLOW`. Open the candidate relative to it with
   `O_NOFOLLOW`; for a directory, use `O_DIRECTORY` as well. `fstat` it.
   Its device, inode and type must equal those recorded when it was
   checked; otherwise it is skipped as `changed`.
2. **Never cross a mount.** Mount boundaries are detected by the mount ID
   from `statx(STATX_MNT_ID)`, which also catches same-device bind mounts.
   The candidate's mount ID must equal its parent's; a candidate that is
   itself a mount, such as a bind alias of protected data, is skipped as
   `contains-mount`. So is a candidate containing any directory on another
   mount ID. A category root that lies on a bind mount of a subdirectory
   (mountinfo root other than `/`), or whose mount cannot be identified, is
   `unknown`: it may alias protected data under another path. Scans also
   refuse a directory whose device and inode are a protected root's.
3. **Remove through descriptors.** Remove the candidate's contents through
   directory descriptors (`openat`, `unlinkat`), never by path, and never
   follow a symlink. Before removing the entries of a directory that lacks
   owner write permission, add it with `fchmod` on that directory's
   descriptor. Every opened descendant is checked against the recorded
   mount ID. Before descending into each directory, prune resolves the
   candidate descriptor's current path through `/proc/self/fd`. If the
   candidate has been moved out of its original parent, prune stops,
   reports `moved`, and keeps what remains.
4. **Remove the top directory last.** `fstatat` the name in the parent: if
   it still names the opened inode, `unlinkat(AT_REMOVEDIR)` it. A name that
   now names something else is left alone, and the item is reported
   `replaced` after its contents were removed.

There is no quarantine or rename step. A run killed part-way leaves a
partly emptied candidate. The next run re-examines it under the full
category rules and finishes it or keeps it.

**Protected roots override every category.** These roots are protected:

- the state directories;
- every `worktrees` root;
- every canonical clone;
- every worktree any clone's `git worktree list` registers, wherever it is.

A candidate is refused as `protected` when its resolved path lies inside a
protected root, or when a protected root lies inside it. If the
protected-root inventory is incomplete because a clone or worktree list
could not be read, every category that deletes directories is `unknown`.

**Bulk build-cache deletion.** Build-cache entry files are unlinked
relative to the open shard directory, each checked by identity first:
device, inode, the inode's creation time where the filesystem records one
(a freed inode number is often reused at once) and the file's status-change
time. For the trim, the `/proc` re-check every 256 entries and at every shard
change of REQ: go-build-cache satisfies
the per-deletion check of REQ: concurrency. The trim is reported per shard
(entries and bytes), never per entry file, in the plan, the JSON and
`--verbose` alike.

### REQ: zero-free-space

Prune MUST work on a filesystem with no free bytes:

- The run lock and resource locks are on existing directories.
- Deletion creates nothing.

When admitted above the floor, only the runqueue admission files are
written before deletion. Below the floor, admission is bypassed and nothing
is written before deletion. Receipts and reports are always written after deletion,
best-effort. Failing to write them is reported, not fatal.

### REQ: scratch

**Temporary directories.** They are `os.TempDir()`, and `$GOTMPDIR` when it
is set. Another per-session temporary directory, such as one a harness
gives each agent, is covered only when prune runs with that `TMPDIR`.

**Candidate shapes.** An entry is a deletion candidate only if all of these
hold:

- the invoking user owns it;
- it is a real directory, not a symlink;
- its name matches one of these shapes.

| Shape | Producer | Floor |
|---|---|---|
| `go-build<digits>` | a Go command's work directory | 24 h |
| `Test…`, `Benchmark…`, `Fuzz…` + `<Name><digits>` holding numbered subdirectories | `testing.T.TempDir` of a killed test | 24 h |
| `wb-<label>-cache`, `wb-<label>-go-cache`, and only when the directory has the exact shape of a Go build cache: a Go `README`, two-hex-digit shard directories holding only entry files, and optionally `trim.txt` and `testexpire.txt` | per-task Go build caches set up by agents' scripts | 72 h |
| a WB *regenerable* prefix followed only by the digits `os.MkdirTemp` appends: `wb-coverage-baseline-`, `wb-coverage-tiers-`, `wb-go-test-shards-`, `wb-index-`, `wb-sync-report-validate-`, `wb-test-cpu-queue-`, `wb-worktree-merge-main-deadcode-`, `wb-agent-preflight-`, `wb-smoke-`, `wb-deadcode-tool-`, `wb-native-cover-` | temporary directories WB creates and removes itself | 72 h |

`wb/<task>` is report-only, as `owner-unverifiable`. Nothing writes there
today, and the temporary directory can be shared by several projects roots,
so this projects root cannot prove the task is finished.

**Kept prefixes.** Three more classes of WB prefix are always kept:

| Class | Prefixes | Why |
|---|---|---|
| `recovery-material` | `wb-rescue-`, `wb-retired-prepare-`, `wb-retired-tag-metadata-`, `wb-retirement-archive-`, `wb-retirement-history-`, `wb-canonical-sync-ignore-` | a crash can leave the only copy of rescued or archived work there |
| `runtime` | `wb-daemon-`, `wb-daemon-cli-`, `wb-hub-`, `wb-sock-`, `wb-supervisor-`, `wb-shutdown-`, `wb-peer-`, `wb-peer-host-`, `wb-poll-`, `wb-host-stage-`, `wb-pr-land-`, `wb-e2e-`, `wb-e2e-origin-`, `wb-cwwt-`, `wb-layout-test-home-`, `wb-legacy-home-`, `wb-nls-`, `wb-rawpolicy-` | a live daemon's sockets or a running operation's state; a listening socket shows in `/proc/<pid>/fd` as `socket:[inode]`, not as a path, so process references cannot see it |
| `checkout` | `wb-wt-`, `wb-orchestrate-git-seeds-` | Git checkouts |

Any entry containing a socket or FIFO at any depth is also kept, as
`runtime`. Every other `wb-` entry is kept as `not-a-known-producer`, and
entries another user owns are kept as `foreign-owner`. None of these raises
the exit code.

**Pinning test.** A test MUST fail when non-test code passes a `wb-` prefix
literal to `os.MkdirTemp` that is in none of the four classes, so every new
producer gets a decision.

**Removal rules.** An entry's age is the newest modification or
status-change time of every non-directory entry under it, because `cp -a`,
`tar` and `rsync` preserve old modification times. Directory times are used
only when the entry holds no non-directory entry. Removing children updates
a directory's times, so this is what lets a run finish an entry a killed
run left half-deleted. A candidate is
removed only when:

- it is older than its floor;
- no process references it;
- the scan of it was complete.

**Never removed.** These are kept even when every rule above passes:

- an entry with any unreadable descendant, reported as `partial-scan`;
- an entry that contains a `.git` file or directory at any depth, or that
  a clone's `git worktree list` registers, reported as
  `checkout-in-scratch`;
- an entry that contains or lies inside a protected root;
- an entry that holds a module cache (`cache/download/`), a non-empty
  `fuzz/` directory, or a resolved `GOCACHE`, `GOMODCACHE`, npm cache or
  pnpm store, reported as `contains-cache-data`. Scratch never deletes what an opt-in or
  always-kept rule protects.

### REQ: go-build-cache

Go refreshes an entry's modification time on use when the time is more than
an hour old. It hands cache paths straight to the compiler and linker, so
deleting an entry a running build is about to read is a hard error, not a
cache miss.

- **Trim.** Delete only entry files of `GOCACHE` (`<2 hex>/<hash>-a` and
  `-d`), oldest first across all shards, stopping once the target is met.
  Prune probes the target before every entry, so it stops at the first entry
  that suffices. Every 256 entries, and whenever it moves to another shard,
  it re-reads `/proc` and recomputes the floor. The floor is the larger
  of:
  - `--older-than`, default 24 h;
  - the age of the oldest running Go build tool, plus 1 h.

  Every entry a running build has looked up was refreshed to within an
  hour before its lookup, so no such entry is ever old enough to delete.
  The floor is applied as an absolute cutoff, fixed when the process table
  is read (the clock first, then the table): an entry is eligible only when
  modified no later than that cutoff, however long planning and applying
  take.
  The plan and the report state the effective floor and the PID that set
  it.
- **Without `/proc`.** The floor is 72 h and cannot be lowered.
- **Always kept.** `README`, `trim.txt`, `testexpire.txt`, the `fuzz/` tree
  (corpus data that is not regenerable), and the directory tree itself.
- **Inside a checkout.** A `GOCACHE` with a `.git` entry in itself or an
  ancestor below home (a personal clone outside the projects root) is
  `protected`; a repository at home itself (dotfiles) does not count. A
  cache entry name that is a mount of a file from another filesystem is
  not an entry, and makes the enumeration partial.
- **Unsupported configurations.** The category is skipped as
  `unsupported-cache` when `GOCACHE` is `off`, empty, relative or not a
  directory, or when `GOCACHEPROG` is set. The full clean, explicitly
  requested, is then `requested-step-skipped: unsupported-cache`, a
  finding; the daemon does not trigger on such a cache's filesystem.
- **Full clean.** With `--allow-full-clean`, prune runs the trim again with
  no age floor. It removes every entry file and keeps everything listed
  above. It never runs `go clean`, which would delete whatever a shard
  directory holds and honour a `GOFLAGS` that adds `-modcache`. It runs
  only when no Go tool is running immediately before it and before each
  shard, never under `--auto`, never implied by `--until-free max`, and
  never with `--older-than`.
- **Full clean risk.** A Go tool that starts during the full clean can fail
  once. That residual risk is accepted, stated in the flag's help, and is
  why the flag exists. A requested full clean that is skipped as `busy` is
  reported as `requested-step-skipped`.

### REQ: node-package-store

The pnpm store and the npm cache are measured by `wb disk` and MUST NOT be
pruned. pnpm hard-links every store file into every consumer's
`node_modules`, so only pnpm can tell which files are unreferenced. Running
`pnpm store prune` safely would mean verifying pnpm's own traversal: its
internal store layout (`v*/files`, `v*/index`), its cache directory and
every mount and symlink in them. That depends on pnpm internals and walks
the whole store, which costs more than it saves. `wb disk` names
`pnpm store prune` for a person to run, and `--category node-package-store`
is a usage error.

### REQ: go-module-cache

The Go module cache is measured by `wb disk` and MUST NOT be pruned by this
version; `--category go-module-cache` is a usage error. Module pruning was
specified, implemented and reviewed for three rounds (sum-file reference
set, provable re-fetch from an independent proxy, Go's per-version lock,
crash-safe ordering) and then moved to a later Feature by the founder on
2026-11-27: its remaining review findings concerned edge cases the first
version does not need, and the 2026-11 incidents are reclaimed by scratch
and the build-cache trim. The implementation is preserved in this
Feature's history (commit 715776ad) for that Feature.

### REQ: progress

Prune follows cleanup-orchestration `progress-off-terminal`, with the same
interval, injectable clock, first-interval silence and closing line.

- **Per-line mode.** On a terminal, or with `--verbose`, it prints one line
  per category and item as it finishes.
- **Heartbeat mode.** Otherwise, unless `--quiet`, the heartbeat names:
  - the phase (`measure`, `references`, `plan`, `apply`);
  - the category;
  - the elapsed time;
  - the bytes unlinked so far.

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
  "cache"`, then lists `wb disk prune` first. There is no `wb cache`
  command and no alias (founder, 2026-11-27).

### REQ: exit-codes

| Exit | When |
|---|---|
| `0` | Plan: nothing eligible, or every filesystem already at its target, and no fail-closed skip. Apply: every filesystem reached its target (or, with `max`, every eligible item was removed), nothing failed, and no fail-closed skip occurred. |
| `1` | Any reason listed under `outcome` other than `done`. |
| `2` | An unknown or report-only `--category`; an unparsable `--until-free` or `--older-than`; `--allow-full-clean` without `go-build-cache` selected; `--auto` combined with `--category`, `--until-free`, `--older-than` or `--allow-full-clean`. Every flag is validated before anything is read. |

The JSON `outcome` is the first that applies, in this order:

1. `locked`
2. `cancelled`
3. `failed`
4. `fail-closed`
5. `space-not-returned`
6. `target-unmet`
7. `planned`
8. `done`

`reasons` lists every outcome that applies, so a partial run that failed,
skipped and missed its target says all three.

### REQ: machine-readable-output

`--format json` writes one object to stdout. Progress and diagnostics go to
stderr only.

```json
{"schema_version":1,"mode":"plan|apply|auto",
 "outcome":"done|planned|target-unmet|space-not-returned|fail-closed|failed|cancelled|locked","reasons":[""],
 "lock":{"held":true,"holder_pid":0,"wait_ms":0},"admission":{"status":"admitted|bypassed|timed-out|not-needed","wait_ms":0},
 "filesystems":[{"device":"","path":"","target":{"kind":"ratio|bytes|max","value":0.1},
   "before":{"total_bytes":0,"available_bytes":0},"after":{"total_bytes":0,"available_bytes":0},
   "target_met":true,"estimated_bytes":0,"unlinked_bytes":0,"observed_gain_bytes":0}],
 "categories":[{"name":"","order":1,"status":"planned|applied|partial|skipped|beyond-target|requires-category|report-only",
   "reason":"","partial":false,"effective_floor":"24h","floor_set_by_pid":0,
   "estimate_bytes":0,"estimate_is_lower_bound":false,"estimate_is_upper_bound":false,"unlinked_bytes":0,
   "shards":[{"path":"","entries":0,"bytes":0,"status":"delete|deleted|keep|beyond-target|skipped"}],
   "items":[{"path":"","filesystem":"","bytes":0,"projected_available_bytes":0,
     "status":"delete|deleted|keep|beyond-target|skipped|failed|replaced","reason":"",
     "pids":[{"pid":0,"command":""}],"error":""}]}],
 "state":[{"root":"","entries":[{"path":"","class":"wb-written|origin-not-proven|wb-state","bytes":0}]}]}
```

`shards` is used by the build-cache trim instead of `items`.
`estimate_bytes` and `projected_available_bytes` are `null` when unknown.
`filesystem` values are shaped as in `wb disk`.

### REQ: daemon-auto-prune

- **Configuration.** The user-level `wb.yaml` gains `disk.auto_prune`
  (default `off`) and `disk.check_interval` (default 10 m).
- **Trigger.** When `disk.auto_prune` is on, the daemon checks, at that
  interval, every filesystem holding a root of a category `--auto` may
  prune. When any is below `disk.minimum_available`, it runs
  `wb disk prune --auto`, at most once per hour. Filesystems holding only
  report-only roots never trigger it.
- **What `--auto` does.** `--auto` implies `--apply` with the floor as
  target. It runs only scratch and the build-cache trim, and never the full
  clean.
- **Rate limit.** The hourly limit applies only to daemon-triggered runs; a
  manual `--auto` always runs, under the lock. The limit is kept in the
  daemon's memory and in the newest receipt. A daemon that restarts
  without a readable receipt may run once more; prune is idempotent, so
  that is harmless.
- **Receipts.** Each run appends a receipt under `<root>/.wb/disk/receipts/`,
  best-effort per REQ: zero-free-space. It records the trigger, free space
  before and after per filesystem, bytes per category, and skips.
  A `locked` receipt measured nothing: the `wb disk` finding comes from the
  newest automatic receipt that is not `locked`.
- **Exit and findings.** Exit codes follow REQ: exit-codes, so a run that
  leaves a filesystem below the floor exits `1`. A failed run, or free space
  still below the floor, is shown by `wb daemon status` as `disk_auto_prune`
  and is a finding in `wb disk`, which then exits `1`.
- **Environment.** The daemon resolves roots in its own environment and
  records them in the receipt. A daemon whose `TMPDIR` or Go environment
  differs from the agents' prunes what it resolves, and says so.

## Accepted Risks

The founder accepted these on 2026-11-27, after Codex Astra objected that
deletion should require enforceable reader exclusion through the last
unlink. Plain `go build` takes no lease WB could check, so that exclusion
cannot exist without wrapping Go itself. A lease design is a separate
Feature, should these risks materialise.

1. **Any process that starts using a candidate in the window between
   prune's last check and an unlink** can lose what it uses: a build-cache
   entry, a scratch directory, or a file it creates in a directory prune
   has judged unused. Linux cannot unlink by descriptor, so a name is
   unlinked moments after the object behind it was checked. Go's own
   automatic cache trim has the same race. The window is bounded by the
   re-checks every 256 build-cache entries and before each other item (founder, widened on 2026-11-27).
2. **`go build -work` directories retained longer than 24 h**, and any
   `go-build*` or test directory whose producer has been paused longer
   than 24 h, are deleted.
3. **A failed `Chtimes` inside Go** can leave a used build-cache entry
   looking older than it is. Go ignores that error.

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

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:machine-readable-output

**Given** fake roots for every category, with eligible items in each, a
worktree holding terminal artifact debris and a live claim, a Go env
file setting `GOCACHE`, and fake `go`, `git` and `pnpm` binaries that
record their arguments
**When** the user runs every category with `--until-free max --format json`:
`wb disk prune --category scratch --category go-build-cache --until-free max --format json`
**Then**:
- a recursive listing of every root, the debris, the claim and the
  worktree's heartbeat, with modes and modification times, is identical
  before and after;
- the recorder saw only `git worktree list --porcelain -z`, and never `go` or
  `pnpm`;
- the output validates against the documented shape;
- exit is `1` with outcome `planned`.

### AC: real-directory-dry-run

**Requirements:** disk-prune#req:plan-by-default, disk-prune#req:accounting-roots

**Given** this machine's real projects root, caches and temporary directory,
and an exec recorder wrapped around the real tools
**When** the planner runs with `--until-free max` and every category
selected
**Then** it completes with every category present, the recorder saw only
the commands REQ: plan-by-default allows, and the planner was given no
mutating filesystem operation to call.

### AC: per-filesystem-target

**Requirements:** disk-prune#req:selection

**Given** two fake filesystems, A at 50% free holding the scratch root and B
at 1% free holding `GOCACHE`; a probe whose free space rises as files
are deleted; and a 1 GiB tmpfs `C` holding a second temporary directory
**When** the user runs `wb disk prune --until-free 20% --apply`, then
`--until-free 20GiB`
**Then**:
- in the first run, nothing on A is deleted (`beyond-target`);
- B's trim stops after the first entry that brings it to 20%;
- the first run exits `0`;
- in the second run, C is reported `target-unreachable`, nothing on it is
  deleted, and exit is `1`;
- with two eligible 600 MiB scratch entries on B where the first suffices,
  only the first is removed.

### AC: target-reached-elsewhere-stops-pruning

**Requirements:** disk-prune#req:selection

**Given** a probe that reports the target met after admission, because
another process freed space, and then reports concurrent allocation
during the trim
**When** `--apply` runs
**Then** nothing is deleted on the first probe; when the target is met
again during the run, pruning stops; and the report keeps estimated,
unlinked and observed bytes separate.

### AC: space-not-returned-stops-escalation

**Requirements:** disk-prune#req:selection

**Given** a probe whose free space does not rise after 1 GiB of deletions on
one filesystem
**When** `--apply` runs
**Then** prune stops on that filesystem after that category, reports
`space-not-returned`, and exits `1`.

### AC: scratch-producers-only

**Requirements:** disk-prune#req:scratch, disk-prune#req:concurrency

**Given** a fake `/proc`, and these temporary entries, all older than 4
days unless stated:
- pid 4242 has an open descriptor inside `go-build111`;
- pid 4243 has `wb-pr1-test-cache` in its environment;
- `go-build222`, `wb-pr2-lint-go-cache` and `wb-index-77` are unheld;
- `wb-wt-x` holds a `.git` file, and `wb-index-9/repo` holds a `.git`
  directory;
- `wb-notes` is unheld;
- `wb-rescue-1` and `wb-sock-1` are unheld, and `wb-index-5` holds a
  Unix socket;
- `wb/old-task` is unheld;
- `wb-ci-cache` holds a module cache at `wb-ci-cache/mod/cache/download`,
  and `wb-fz-go-cache` is a Go build cache with a non-empty `fuzz/`;
- `wb-index-backup` (no digits) and `wb-x-cache` (holding a non-cache file)
  are unheld;
- `go-build333` is owned by another user;
- `go-build444` was copied with `cp -a`, with old modification times and a
  status-change time 10 minutes ago;
- `go-build555` has an unreadable subdirectory;
- `wb-coverage-baseline-3` contains `<root>/.wb` through a bind mount;
- `TestFoo123` holds numbered subdirectories

**When** the user runs `wb disk prune --category scratch --until-free max --apply`
**Then**:
- only `go-build222`, `wb-pr2-lint-go-cache`, `wb-index-77` and
  `TestFoo123` are removed;
- `go-build111` and `wb-pr1-test-cache` are `held-open` with their PIDs;
- `wb-wt-x` and `wb-index-9` are `checkout-in-scratch`;
- `wb-notes` is `not-a-known-producer`;
- `wb-rescue-1` is `recovery-material`;
- `wb-sock-1` and `wb-index-5` are `runtime`;
- `wb/old-task` is `owner-unverifiable`;
- `wb-ci-cache` and `wb-fz-go-cache` are `contains-cache-data`;
- `wb-index-backup` and `wb-x-cache` are `not-a-known-producer`;
- `go-build333` is `foreign-owner`;
- `go-build444` is too young;
- `go-build555` is `partial-scan`;
- `wb-coverage-baseline-3` is `contains-mount`;
- exit is `1` because of `partial-scan`.

### AC: scratch-removed-on-a-real-filesystem

**Requirements:** disk-prune#req:scratch, disk-prune#req:deletion

**Given** a real temporary directory on ext4 or tmpfs, an injected clock 4
days ahead, and an eligible `go-build` directory holding read-only
subdirectories
**When** `--apply` runs
**Then** the directory is actually gone, and nothing outside it changed.

### AC: unreadable-process-fails-closed

**Requirements:** disk-prune#req:concurrency

**Given** three fake process tables:
- a `/proc` entry with command line `go build ./...` whose `fd`, `cwd` and
  `maps` are unreadable;
- an unreadable same-user `sshd: user@pts/0` entry;
- a `/proc` that cannot be listed, a `/proc` mounted with `hidepid=2`
  while `TMPDIR` is world-writable, and a nested PID namespace

**When** prune plans scratch and the build-cache full clean
**Then**:
- with the first table, Go-produced scratch and the full clean are
  `busy-unverified`;
- with the second, scratch is unaffected;
- with each case of the third, both categories are `unknown`;
- every fail-closed case exits `1`.

### AC: deletion-is-bound-to-the-checked-object

**Requirements:** disk-prune#req:deletion

**Given** three eligible scratch entries:
- the first is replaced by a symlink to a directory holding `precious`
  after it was checked;
- the second is renamed away and replaced by a new non-empty directory of
  the same name while its contents are being removed;
- the third's top directory is mode 0555 with read-only files

**When** `--apply` reaches them
**Then**:
- the symlink target and `precious` survive, and the first entry is
  skipped as `changed`;
- the renamed original's contents are removed through its descriptor, the
  replacement survives, and the item is reported `replaced`;
- the third is removed completely.

### AC: protected-roots-override

**Requirements:** disk-prune#req:deletion

**Given** three setups:
- `GOCACHE` set to a directory inside `<root>/.wb`;
- `TMPDIR` holding a `go-build1` entry that contains a canonical clone;
- a registered external worktree under a `go-build2` directory

**When** `--apply` runs on each
**Then** nothing inside any protected root is removed, and the build cache,
`go-build1` and `go-build2` are each reported `protected`.

### AC: interrupted-prune-is-completed

**Requirements:** disk-prune#req:deletion

**Given** a prune killed after removing half of a scratch entry's contents
**When** the next prune runs with `--category scratch --until-free max --apply`
**Then** the scratch entry is re-examined and removed.

### AC: works-on-a-full-disk

**Requirements:** disk-prune#req:zero-free-space, disk-prune#req:concurrency

**Given** a fake filesystem on which every create fails with `ENOSPC`, with
eligible build-cache entries and a scratch directory
**When** `wb disk prune --category scratch --category go-build-cache --until-free max --apply`
runs below the floor
**Then**:
- both are deleted;
- admission is reported `bypassed`;
- the failure to create the receipt is reported;
- exit reflects the target only.

### AC: go-cache-trimmed-by-last-use

**Requirements:** disk-prune#req:go-build-cache

**Given** a fake `GOCACHE` of entry files with staggered modification
times, a `fuzz/` tree and `testexpire.txt`, a fake Go build tool started
30 h ago, and a fake `gopls` started 40 days ago
**When** the user runs
`wb disk prune --category go-build-cache --until-free <size> --apply`
**Then**:
- no remaining entry is older than a deleted one;
- none modified within 31 h was deleted;
- the report names an effective floor of 31 h, set by the build tool's PID;
- `README`, `trim.txt`, `testexpire.txt` and `fuzz/` remain;
- `go clean` was not invoked.

### AC: full-clean-needs-its-flag

**Requirements:** disk-prune#req:go-build-cache, disk-prune#req:exit-codes

**Given** a target the trim cannot meet, a `fuzz/` sentinel, and a
directory `ab/backup` inside a shard
**When** prune runs:
1. with `--category go-build-cache --until-free max --apply`;
2. with `--allow-full-clean` added;
3. with `--allow-full-clean` while a fake Go tool runs;
4. with `--allow-full-clean` and `--older-than 2d`;
5. with `--auto --allow-full-clean`

**Then**:
1. keeps every entry younger than the floor;
2. removes every entry file, keeps the `fuzz/` sentinel and `ab/backup`, and
   never invokes `go`;
3. skips it as `busy`, naming the PID, reports `requested-step-skipped`,
   and exits `1`;
4. skips it as `age-filter-unsupported`;
5. exits `2`.

### AC: state-reported-never-pruned

**Requirements:** disk-prune#req:state-is-report-only, disk-prune#req:plan-by-default

**Given** `<root>/.wb` and a retired `$HOME/.wb`, each holding `worklogs/`,
`hub/`, `reports/worktree-merge/`, `reports/hand-made/` and `backups/x`
**When** the user runs `wb disk prune --until-free max --apply`, once while
admitted and once below the floor
**Then**:
- every pre-existing entry under either directory is unchanged;
- the only new files are the admission files and
  `<root>/.wb/disk/receipts/…`;
- `reports/worktree-merge` is `wb-written`;
- `reports/hand-made` and `backups/x` are `origin-not-proven`;
- the rest is `wb-state`;
- `--category wb-state` exits `2`.

### AC: one-prune-per-user-and-resource

**Requirements:** disk-prune#req:concurrency, disk-prune#req:exit-codes

**Given** the run lock held by a process with another projects root; then a
second home that shares this user's `GOCACHE` and holds its resource lock
**When** the user runs `wb disk prune --apply`
**Then**:
- with the run lock held, nothing is deleted, the holder's PID is reported
  from `/proc/locks`, the outcome is `locked`, and exit is `1`;
- with the resource lock held, the build-cache category is skipped as
  `locked`, and the other categories proceed.

### AC: flags-validated-first

**Requirements:** disk-prune#req:exit-codes, disk-prune#req:selection

**Given** any fixture
**When** prune runs with each of `--category docker`,
`--category worktrees`, `--category node-package-store`, `--until-free 12XB`,
`--older-than soon`, `--auto --category scratch`, and an explicitly empty
`--category=`, `--until-free=` or `--older-than=`
**Then** each exits `2` before any filesystem read; and `--older-than 100h`
raises the scratch and trim floors to 100 h.

### AC: outcome-precedence

**Requirements:** disk-prune#req:exit-codes, disk-prune#req:machine-readable-output

**Given** a run in which one deletion fails, one category is
`busy-unverified`, and a filesystem stays below its target; and a run
cancelled after one unlink
**When** each finishes
**Then** the first reports outcome `failed` with reasons `failed`,
`fail-closed` and `target-unmet`; the second reports `cancelled`; both
exit `1`.

### AC: unsupported-platform

**Requirements:** disk-prune#req:concurrency, disk-prune#req:go-build-cache

**Given** a process-table reader that reports itself unavailable, and,
separately, `GOCACHEPROG` set
**When** prune runs `--apply` on every category
**Then** only build-cache entries older than 72 h are removed, every other
category is `unsupported-platform`, and exit is `1`; with `GOCACHEPROG`,
the build cache is `unsupported-cache`.

### AC: findings-point-to-the-verb

**Requirements:** disk-prune#req:cross-references

**Given** a disk below the floor with stale scratch and a legacy home
**When** the user runs `wb disk`, then `wb clean --format json`, then
`wb cache prune`, then `wb commands --search "cache"`
**Then**:
- the `wb disk` findings name `wb disk prune` for scratch and `wb clean` for
  worktrees;
- `wb clean` writes one stderr line naming `wb disk prune` and leaves its
  JSON stdout valid;
- `wb cache prune` fails with the existing unknown-command hint;
- the search lists `wb disk prune` first.

### AC: prune-proves-it-is-alive

**Requirements:** disk-prune#req:progress

**Given** a fake measurement that takes 40 s under an injected clock, with
stderr attached to a pipe
**When** prune plans without `--verbose`
**Then** stderr carries a heartbeat at every 10-second tick naming the phase
and category, then a closing line, and stdout is only the report.

### AC: daemon-prunes-below-floor

**Requirements:** disk-prune#req:daemon-auto-prune

**Given** `disk.auto_prune: on`, `disk.minimum_available: 0.99`,
`disk.check_interval: 5s`, a stale build cache, an old Go module version
and a landed worktree
**When** the daemon runs for 15 s, and then the user runs
`wb disk prune --auto --format json`
**Then**:
- exactly one daemon receipt was written;
- the build cache was trimmed;
- the module version and the worktree remain;
- the manual run ran (it is not rate-limited) and exits `1`, because the
  floor is unreachable;
- `wb daemon status` reports the floor finding;
- with `disk.auto_prune` absent, the daemon prunes nothing.

## Delivery Slices

All slices ship in one pull request (founder, 2026-11-27).

0. **Accounting:** accounting-roots, state-is-report-only (report side),
   and cross-references for `wb disk`. ACs: disk-measures-current-layout,
   nested-and-equal-roots-counted-once, symlinked-and-unreadable-roots.
1. **`wb clean` liveness:** cleanup-orchestration `progress-off-terminal`,
   plus the fix for misattributed durations.
2. **The verb:** every remaining REQ except daemon-auto-prune, and every
   remaining AC except daemon-prunes-below-floor.
3. **Daemon:** daemon-auto-prune, default off. AC: daemon-prunes-below-floor.

Deferred to their own Features or slices:

- **Docker.** It needs run-record ownership checks against WB's
  create-then-start window, and must keep the untagged images WB runs by ID.
- **npm.** A running npm cannot be detected reliably, and
  `npm cache verify` needs free space.
- **Reader leases** (Accepted Risks).
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

---
*This document follows the https://specscore.md/feature-specification*
