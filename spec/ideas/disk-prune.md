---
format: https://specscore.md/idea-specification
status: Specifying
---

# Idea: Disk prune acts on what wb disk reports

**Status:** Specifying
**Date:** 2026-11-27
**Owner:** ai
**Promotes To:** disk-prune
**Supersedes:** —
**Related Ideas:** extends:scratch-has-an-owner

## Problem Statement

How might WB give back the disk space wb disk already measures, deterministically and without ever deleting something a live build, task or unlanded change still needs, so the agent VM stops filling up?

## Context

### It keeps happening

The agent VM has run out of disk at least ten times (founder, 2026-11-27).
Dated instances include 2026-09-17 (101 MB free), 2026-09-18 (99% full) and
2026-11-27, when `/` had 1.0 GB free of 150 GB, the volume holding
`GOMODCACHE` had 364 MB, and a `go build` failed with "no space left on
device". Every recovery was done by hand.

### wb disk diagnoses, nothing acts

On 2026-11-27 `wb disk` (0.205.9) reported, as RECLAIM / APPARENT:
go-module-cache 21.2 / 19.8 GB, wb-state 6.9 / 8.8 GB, go-build-cache
5.9 / 5.8 GB, node-package-store 5.1 / 18.3 GB, worktrees 538 MB / 2.7 GB,
scratch 65 / 63.8 MB. Its own help says "nothing here deletes anything".
Recovery was removing two orphaned `/tmp/go-build*` directories (1.6 GB, no
open handles, no live Go process) and `go clean -cache` (5.9 GB).

`wb cache prune`, which agent memory mentions, was never implemented: it
exists only as a planned verb in the streams spec (commit 9a3480e9). The
Draft Feature [disk-prune](../features/disk-prune/README.md) designed a
`wb disk reclaim`, but no code was ever written for it.

### What wb-state actually is

On the incident host `du -xh --max-depth=2 ~/.wb` (2026-11-27) gave 10 GB:
5.6 GB `reports/`, of which about 5.5 GB is four hand-named directories no WB
code writes (`cm-peer-groups-park` alone 3.5 GB); 3.5 GB `worktrees/` (the
legacy central layout, already a `worktrees` root in `wb disk`); 607 MB of
hand-made `backups/`; and about 270 MB of `bin/`, `hub/`, `worklogs/` and
`archive/`. Work Logs are 30 MB. The growth is not WB state. It is other
categories' bytes, and artifacts of unknown origin, that happen to be stored
under `~/.wb`.

## Recommended Direction

Add `wb disk prune`, which plans by default and runs the plan with
`--apply`. It reuses `wb disk`'s single accounting walk, so each candidate is
shown with its measured RECLAIM and a reason it is safe. It prunes in order
of rebuild cost (orphaned scratch, Go build cache by last use, `pnpm store
prune`, Go module versions no `go.sum` in a canonical clone or live
worktree references) and stops once `--until-free` is met. Anything held
open, owned by a live task or locked by a running build is skipped, and the
skip is reported.

Revise the disk-reclaim Draft (now [disk-prune](../features/disk-prune/README.md)) into this feature instead of writing a second
one. Because the incident keeps recurring, the daemon's below-floor trigger
stays in scope: a manual verb nobody remembers to run does not stop the
eleventh incident. The trigger runs only the subset that is safe to run
unattended. The verb is the engine; the daemon calls it.

wb-state is report-only. The prune plan breaks `~/.wb` down by provenance
(written by WB, of unknown origin, or a worktree that belongs to `wb clean`)
and lists the largest entries, so a person can decide. WB cannot prove a
hand-made report is finished, so it deletes none of them.

## Alternatives Considered

- **Fold caches into `wb clean`.** Lost because agents run
  `wb clean --apply` after every landing. Emptying caches there would make
  every next build cold. It would also put two safety models (landed work
  versus unreferenced bytes) behind one `--apply`. `wb clean` instead points
  to prune when the disk is below the floor.
- **Top-level `wb prune` or `wb disk reclaim`.** The founder chose
  `wb disk prune` (2026-11-27). "Prune" is the ecosystem's word for
  removing what nothing references, and the command calls those tools.
- **Age-only deletion.** Lost because age is a proxy for "dead". A task that
  is still running after four days would lose its scratch.

## MVP Scope

One verb, `wb disk prune [--apply] [--until-free] [--category]
[--older-than] [--allow-full-clean] [--format json]`. By default it covers
scratch from known producers, the Go build-cache trim and the pnpm store.
Go module versions are pruned only with `--category go-module-cache`, and
only when re-download is provable. wb-state is a report. It works against
hermetic fakes and passes a real-directory dry run. The daemon trigger ships
in the same pull request, default off (founder, 2026-11-27). Docker and npm
are deferred.

## Not Doing (and Why)

- Worktree retirement — wb clean owns it; the two verbs stay separate (founder, 2026-11-27)
- Pruning wb-state — report-only until each subdirectory has a provable owner and retention
- Blanket go clean -modcache — only with an explicit user request
- Docker and npm — deferred; Docker needs run-record ownership checks, npm cannot be detected as idle (founder, 2026-11-27)

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A running Go build never reads a module version or build-cache entry that prune deletes | Hermetic test plus a concurrent `go test` while prune applies; check the module set against every `go.sum` |
| Must-be-true | `pnpm store prune` only removes entries no consumer links | pnpm's documented contract; a real-pnpm test keeps a linked package and removes an unlinked one (offline reinstall is not promised) |
| Should-be-true | The go.sum-referenced module set is much smaller than the 21 GB cache | Count referenced versions against cached ones on the incident host in a dry run |
| Might-be-true | The daemon trigger alone prevents recurrence without root-cause fixes | Watch incidents after the second phase ships |


## SpecScore Integration

- **New Features this would create:** none; it revises [disk-prune](../features/disk-prune/README.md)
- **Existing Features affected:** disk-reclaim, renamed to [disk-prune](../features/disk-prune/README.md) and rescoped per this Idea; cleanup-orchestration (`progress-off-terminal`)
- **Dependencies:** worktree-lifecycle, daemon-lifecycle

## Open Questions

- Should hand-made artifacts under `~/.wb/reports` get a sanctioned home with a declared retention, so a later prune can act on them?
