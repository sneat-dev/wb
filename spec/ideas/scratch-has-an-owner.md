---
format: https://specscore.md/idea-specification
status: Draft
---
# Idea: Non-git space has an owner

**Status:** Draft
**Date:** 2026-09-18
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:fleet-liveness-audit

## Problem Statement

How might WB give every byte it causes to exist outside a git checkout an owner
and a lifetime — so that scratch dies with the task that made it, caches stay
shared and bounded, and the machine says something before it runs out of disk
rather than after?

## Context

### What actually happened

On 2026-09-17 the VM filled. `/` reached 144 GB of 150 GB, leaving **101 MB**.

The first symptom was not a warning. It was a Go build failing in the linker:

```text
/home/ai/.local/go/pkg/tool/linux_amd64/link:
mapping output file failed: no space left on device
```

That error was then piped through `head`, so the shell reported the exit status
of `head` rather than of `go build`, and the session printed `BUILD OK` for a
build that had failed. Two independent silent failures composed: a full disk
that announced nothing, and a pipeline that discarded the exit code. The
correction in `l12-never-pipe-a-command-whose-exit-code-you-are-about-to-trust`
covers the second. This idea is about the first.

### Where the space actually was

| Location | Size | What it is |
|---|---|---|
| `/tmp` | **46 GB** | ~2,543 entries of per-task scratch and per-task build caches |
| `~/.cache/go-build` | 22 GB | one shared Go build cache |
| `~/go/pkg/mod` | 8.5 GB | one shared Go module cache |
| all `.worktrees` fleet-wide | **~340 MB** | the thing that looks expensive and is not |

Worktrees were 0.2% of the problem. The problem was `/tmp`, and its shape
matters more than its size: **2,017 of those entries had not been touched in
over three days, and together they held 27 GB.** They were not one large cache.
They were hundreds of small ones, each created by a task that had finished:

```text
wb-pr301-lint-go-cache      wb-pr308-test-cache       wb-sync-report-lint-cache
wb-pr301-test-cache         wb-pr308-lint-go-cache    wb-sync-issues-go-cache
wb-go-build-cache           wb-sync-report-focused-cache
```

Each is a *cold copy* of a cache whose whole value is being warm and shared.

Deleting everything older than three days recovered 27 GB and took the machine
from 101 MB free to 48 GB free. That worked, but age is a proxy for *dead*, and
a proxy can be wrong: a task still running after four days would have had its
scratch deleted underneath it. It was safe on the day only because every live
session happened to have touched a file within 24 hours. That is luck, not
design.

### Why per-worktree caches are the wrong fix

The obvious reaction to "scratch is leaking" is to move it inside the worktree
so it dies with the worktree. For scratch that is right. For caches it is
actively worse, and the distinction is the whole of this idea:

- A Go build cache is **content-addressed and shared by construction**. Giving
  each worktree its own produces *N* copies of identical objects. The 22 GB
  cache becomes 22 GB times the number of worktrees.
- Every worktree would start cold, paying a full rebuild — the same wall-clock
  cost this fleet is otherwise trying to remove.
- It lives inside the repository tree, one `.gitignore` miss from being
  committed, and it slows every `git status`.

The 46 GB in `/tmp` was not caused by caches being shared. It was caused by
caches being **fragmented per task** — which is per-worktree caching, already
happening, unmanaged. Formalising it would institutionalise the bug.

### Two different things, conflated into one directory

| | Cache | Scratch |
|---|---|---|
| Examples | `GOCACHE`, `GOMODCACHE`, pnpm store | per-run temp dirs, test fixtures, captured logs |
| Correct location | one shared path per machine | one path per task |
| Correct lifetime | evicted by size and age, never by task | dies with the task |
| Sharing | the point | a bug |
| Who owns it | the machine | the worktree |

Neither currently has an owner in WB. `wb worktree create` owns task lifecycle
and `wb run --` owns command execution, and neither sets or records a scratch
location, so every tool invents its own under `/tmp` and nothing cleans up.

### The existing verb already knows, and declines

`wb worktree gc --apply` on 2026-09-17 printed dozens of lines of this form:

```text
artifact secure_worktree_stage /home/ai/projects/strongo/cli-helpers/
  .worktrees/.wb-retired-stage-24956daa3d37…: empty retired canonical local
  sibling stage is terminal residue; no task cleanup action is authorized
```

and then summarised: `0 terminal artefacts purged`. The command identifies the
orphans precisely, names them as terminal, and removes none of them. That is
not a missing detector. It is a detector with nothing authorised to act on what
it finds — which is the same shape as the release that skipped silently, and
belongs to `a-successful-release-run-must-not-hide-a-noop-publication`.

### Nothing was watching

There was no threshold, no report, and no alarm. `wb disk` was reached for
during the incident and does not exist. The fleet has `wb fleet stats`, which
counts repositories, worktrees and attention — and no measure of the bytes WB
itself causes.

## Recommended Direction

Four parts, smallest first. Each is useful alone.

### 1. Scratch is a task-owned location WB hands out

`wb worktree create` allocates a scratch directory for the task, records it in
the worktree manifest, and exports it. `wb run --` exports the same value for
commands run inside a managed worktree.

```text
WB_SCRATCH=/tmp/wb/<task>/
```

Ownership follows from recording it: `wb worktree gc` already retires the
worktree, and now retires the scratch path in the same operation, because the
manifest says which one belongs to the task. Nothing needs to guess from
timestamps, and a task that lives for a week keeps its scratch for a week.

This is the part that removes the cause. The remaining three handle what
ownership cannot: crashes, kills and reboots.

### 2. Caches are pinned, shared and bounded

WB sets `GOCACHE`, `GOMODCACHE` and the pnpm store to one path per machine, so
no task can create its own, and prunes them against a size budget rather than
waiting for the filesystem to refuse a write. Sharing is preserved deliberately:
a worktree of the same repository should hit its siblings' objects.

### 3. `wb disk` exists and reports

One verb that answers where WB's bytes are: caches, scratch, worktrees, and
orphans that belong to no live task. Machine-readable, so it can be asserted on
rather than read.

This is the part that would have caught the incident weeks earlier. The disk
did not fill suddenly; it filled with nobody looking.

### 4. The sweep is scheduled, budget-driven, and announces itself

The daemon already exists, already runs, and already owns a durable queue — so
the sweep is a scheduled WB operation, not a new launchd job. That matters
beyond tidiness: `rule:agent-created-machine-state-has-an-owner` requires a
named owner and teardown verb before creating machine state, and adding a second
unowned scheduled job to fix a problem *caused by* unowned state would be its
own instance of the defect. One owner, one teardown verb, `wb daemon stop`.

Three properties the sweep must have, all learned from the incident:

- **Budget-triggered, not only clock-triggered.** A nightly job frees whatever
  it frees and hopes that is enough. A budget says how much must be free.
- **Reports every run**, including when it did nothing.
- **Announces its own failure.** A cleanup that silently stops working leaves
  the machine exactly where it was, except now it is believed to be covered. A
  gate that cannot report its own failure is not a control — which is the same
  lesson the release chain taught on the same day.

Age-based deletion survives only as the last resort, for scratch whose owning
task left no trace, and it is then reported rather than performed quietly.

## Alternatives Considered

- **Per-worktree caches.** Rejected above: it multiplies shared content by the
  worktree count, makes every worktree start cold, and is a formalisation of the
  fragmentation that caused the incident.
- **A cron or launchd entry that deletes `/tmp` entries older than N days.**
  Rejected as the primary mechanism. It is the workaround performed by hand on
  2026-09-17; it deletes by proxy rather than by ownership, and it is a second
  piece of unowned machine state added to fix unowned machine state. It survives
  only as part 4's last resort, inside the daemon that already has an owner.
- **Leave it to `wb worktree gc`.** It already sees the orphans and is not
  authorised to remove them. Without an ownership record there is nothing to
  authorise it *with*, which is why part 1 comes first.
- **Alert on disk usage only.** An alarm without an owner produces a human doing
  `rm -rf` at 99%, which is what happened. Reporting is part 3 precisely because
  it is necessary and not sufficient.

## MVP Scope

Parts 1 and 3: scratch allocation recorded in the manifest and retired by
`wb worktree gc`, plus `wb disk` with machine-readable output. Together they
remove the cause and make the remaining leak visible. Parts 2 and 4 follow once
`wb disk` can show whether they are needed.

## Not Doing (and Why)

- Moving caches inside worktrees — see Alternatives; it multiplies shared content
- Deleting anything in a canonical clone — `wb worktree rescue` owns that path, and a dirty canonical clone is a rescue, never a cleanup
- Deleting by age as the primary rule — age is a proxy for death and it can be wrong about a long-running task
- Teaching WB about every tool's cache location — it pins the ones it sets itself and reports what it finds; a tool that invents its own path is reported as an orphan, not silently adopted

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Scratch attributable to a task is the dominant share of reclaimable bytes, so ownership fixes most of it | Classify a full `/tmp` snapshot into task-attributable scratch, shared cache and unattributable, and measure the shares before any cleanup |
| Must-be-true | Pinning `GOCACHE` fleet-wide does not slow builds through contention, and raises hit rate across sibling worktrees | Measure cold and warm build times with per-task versus shared cache across concurrent worktrees of one repository |
| Should-be-true | A budget-triggered sweep keeps free space above its floor without ever deleting a live task's scratch | Run with the floor set high enough to trigger routinely, and assert no retired scratch belonged to a registered live session |
| Should-be-true | `wb disk` would have surfaced the growth well before exhaustion | Replay the growth from filesystem timestamps and check where the report would first have crossed a sane floor |

## SpecScore Integration

- **New Features this would create:** `non-git-space-lifecycle`, covering the
  scratch allocation contract, the `WB_SCRATCH` export, the `wb disk` command
  and its machine-readable report, and the budget-triggered sweep operation.
- **Existing Features affected:**
  [Worktree Lifecycle](../features/worktree-lifecycle/README.md) owns
  `wb worktree create` and `wb worktree gc`, so it gains the scratch allocation,
  its record in the manifest, and its retirement alongside the worktree — and is
  where the `.wb-retired-stage-*` residue that `gc` currently names but declines
  to purge belongs;
  [Cleanup Orchestration](../features/cleanup-orchestration/README.md) is the
  precedent for authorised destructive sweeps and for the evidence a retirement
  must carry;
  [Daemon Lifecycle](../features/daemon-lifecycle/README.md) supplies the
  durable queue the scheduled sweep runs on, rather than a new launchd job;
  [Fleet Status](../features/fleet-status/README.md) is the precedent for a
  fleet-first read-only report, which `wb disk` follows.
- **Dependencies:** the worktree manifest as the ownership record; the daemon's
  durable queue for scheduling; `internal/quality`'s existing Go toolchain
  invocation points for pinning `GOCACHE` and `GOMODCACHE`.
- **Decisions likely needed:** whether scratch lives under `/tmp/wb/<task>` or
  inside the worktree's `.wb/local/` (see Open Questions), and whether the free
  space floor is a machine-level setting or a repository-level one.
- **Lessons this closes:** partially controls
  `a-successful-release-run-must-not-hide-a-noop-publication` for the
  `wb worktree gc` case, where a detector reports `0 terminal artefacts purged`
  while naming the artefacts it declined to purge.

## Open Questions

- Should `WB_SCRATCH` live under `/tmp/wb/<task>` or inside the worktree's own `.wb/local/`? The first survives a worktree being removed mid-task; the second dies with it automatically. The runlog already stores per-worktree state under `.wb/local/run/`, and that store is destroyed with the worktree — which was already the cause of one wrong conclusion in this fleet, when a count read from it was mistaken for a fleet-wide total.
- Does the scratch path belong in the public `--format json` envelope of `wb worktree create`, or only in the manifest? Exporting it commits to it as a contract.
