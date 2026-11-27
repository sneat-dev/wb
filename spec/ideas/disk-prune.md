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

Add `wb disk prune`, which plans by default and removes with `--apply`, with
two rules a person can check (founder, 2026-11-27, after an earlier design
that proved from outside that nothing used each cache entry kept growing
under review):

- remove this user's own old scratch: known producers' directories in the
  temporary directory in which nothing changed for 3 days, never a symlink,
  another filesystem, a mount, a checkout or a socket;
- clear the Go build cache with `go clean -cache`, only while no Go process
  runs.

Revise the disk-reclaim Draft (now [disk-prune](../features/disk-prune/README.md)) into this feature instead of writing a second
one. Everything else stays a report: worktrees (`wb clean`), WB state, the
Go module cache and the npm and pnpm stores. There is no unattended
(daemon) prune; `wb disk` and `wb clean` point to the verb below the floor.

## Alternatives Considered

- **Fold caches into `wb clean`.** Lost because agents run
  `wb clean --apply` after every landing. Emptying caches there would make
  every next build cold. It would also put two safety models (landed work
  versus unreferenced bytes) behind one `--apply`. `wb clean` instead points
  to prune when the disk is below the floor.
- **Top-level `wb prune` or `wb disk reclaim`.** The founder chose
  `wb disk prune` (2026-11-27). "Prune" is the ecosystem's word for
  removing what nothing references, and the command calls those tools.
- **Proving each entry unused** (process references, mount identity, Go's
  own locks). Built and reviewed for six rounds, then dropped for the age
  rule: every round found a narrower case, and the founder preferred a small
  named risk (a process paused for over 3 days) to an open-ended one.

## MVP Scope

One verb, `wb disk prune [--apply] [--category] [--older-than] [--format
json]`, covering scratch and the idle Go build cache, plus the `wb disk`
accounting fixes and `wb clean`'s progress heartbeat. Go module pruning is
issue #1019. Docker, npm and pnpm are not pruned.

## Not Doing (and Why)

- Worktree retirement — wb clean owns it; the two verbs stay separate (founder, 2026-11-27)
- Pruning wb-state — report-only until each subdirectory has a provable owner and retention
- Blanket go clean -modcache — only with an explicit user request
- Docker and npm — deferred; Docker needs run-record ownership checks, npm cannot be detected as idle (founder, 2026-11-27)

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | No running build or test needs a scratch directory unchanged for 3 days | Watch for reports of lost scratch after shipping |
| Must-be-true | `go clean -cache` while no Go process runs breaks no build | The idle check runs immediately before the clean |
| Might-be-true | A manual verb plus findings that name it prevent recurrence | Watch incidents; an unattended trigger is a later decision |


## SpecScore Integration

- **New Features this would create:** none; it revises [disk-prune](../features/disk-prune/README.md)
- **Existing Features affected:** disk-reclaim, renamed to [disk-prune](../features/disk-prune/README.md) and rescoped per this Idea; cleanup-orchestration (`progress-off-terminal`)
- **Dependencies:** worktree-lifecycle

## Open Questions

- Should hand-made artifacts under `~/.wb/reports` get a sanctioned home with a declared retention, so a later prune can act on them?
