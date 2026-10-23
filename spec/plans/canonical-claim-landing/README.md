---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Canonical Claim Landing

**Status:** Draft
**Source Feature:** canonical-claim-landing
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** canonical-claim-mode

## Summary

Implement [canonical-claim-landing](../../features/canonical-claim-landing/README.md):
landing and release restore the canonical clone to its base branch, no removal
path can ever target a canonical path, and sync, hygiene and fleet readers skip
a claimed clone. It builds on [canonical-claim-mode](../canonical-claim-mode/README.md)
(claim fields and lookup, task-1; marker, task-4; entry verb, task-5).

## Journey

After the agent's pull request lands, WB checks out `main` in the canonical
clone, fast-forwards it, deletes the local branch, seals the claim and restores
the strict guard, while `wb sync` and fleet status meanwhile reported the clone
as claimed rather than as the latest `main`. No cleanup command could have
removed the directory at any point.

## Approach

Six tasks. The never-removable guarantee has no dependency and goes first and
in parallel with the inventory of fleet readers. Restore (task 2) needs the other
plan's claim lookup and marker; landing wiring (3) and `end` release (4) then
parallelise; the journey test closes the plan.
Each task keeps new Go code at 100% statement coverage and the per-package
ratchet.

## Tasks

### Task 1: The canonical-path-never-removable guarantee

**Id:** task-1
**Verifies:** canonical-claim-landing#ac:no-removal-path-reaches-a-canonical-clone
**Depends-On:** —
**Status:** planning

Add one shared predicate in `internal/worktrees` and call it at every removal
choke point (`worktrees.Cleanup`, `removeWorktreeResidue`, directory-removal
helpers) used by `Cleanup`, `GC`, `Abort`, `worktreeend.Engine`, `Retire`,
`RetireTaskShells` (`shell_retirement.go`), `Orphans` residue removal,
`cleanupWorktreeMergeAssets`; make `Relocate`, `RelocateCheckout`,
`RelocateRepository` refuse a canonical source and `Adopt`/`Backfill` refuse to
register a canonical path. Add a dedicated test that feeds a canonical path to
each of these with `--apply` and `--force`.

### Task 2: Restore routine and receipt

**Id:** task-2
**Verifies:** canonical-claim-landing#ac:landing-refuses-unsafe-clones-without-destroying
**Depends-On:** 1
**Status:** planning

Needs canonical-claim-mode task-1 and task-4. In `internal/worktrees` add one
resumable restore routine (verify clean on claim branch, fetch, checkout base,
ff-only, containment check, optional branch delete, seal, marker) with per-step
receipt entries, reusing the `syncCanonicalMergeTarget` checks. Expose an
injectable step runner as the fault-injection seam so tests can fail after each
step. It never resets, cleans, or stashes.

### Task 3: Wire restore into the landing verbs

**Id:** task-3
**Verifies:** canonical-claim-landing#ac:landing-restores-clone-and-keeps-it
**Depends-On:** 2
**Status:** planning

Call the restore routine from `wb worktree land` / `wb land`, `wb pr create`'s
landing, and `wb pr land` (`internal/orchestrate/worktree_merge.go`,
`pr_land.go`), replacing the `not_checked_out` outcome for a canonical claim and
skipping `cleanupWorktreeMergeAssets` removal. Real bare-remote tests through
each verb, resumed after each injected failure.

### Task 4: Release a canonical claim with `wb worktree end`

**Id:** task-4
**Verifies:** canonical-claim-landing#ac:end-releases-claim-keeping-work
**Depends-On:** 2
**Status:** planning

In `worktreeend.Engine` and `cmd/wb/worktree_end.go`: for a canonical claim, WIP
commit and push the feature branch through the managed hooks (works for a lapsed
claim), then run the restore routine without branch deletion and seal as
released; commit or push failure switches nothing. Never removes the canonical
directory.

### Task 5: Sync, hygiene, and fleet readers skip a claimed clone

**Id:** task-5
**Verifies:** canonical-claim-landing#ac:sync-and-readers-skip-claimed-clone, canonical-claim-landing#ac:no-removal-path-reaches-a-canonical-clone
**Depends-On:** 1
**Status:** planning

Needs canonical-claim-mode task-1. First inventory every reader that treats the canonical working tree as the base
tip (open question), then: `fleetsync.syncActive` skips with a distinct status,
branch hygiene and cleanup skip and report the claim, and
`inspectCanonicalFreshness` plus fleet status/dependency/scan readers use the
remote base ref and report the clone as claimed.

### Task 6: End-to-end landing journey test

**Id:** task-6
**Verifies:** canonical-claim-landing#ac:landing-restores-clone-and-keeps-it
**Depends-On:** 3, 4, 5
**Status:** planning

Needs canonical-claim-mode task-5. One real-remote test walking the Journey
through each landing verb, an interrupted-restore resume, and a release via
`wb worktree end` (live and lapsed lease); then update `skills/wb-merge` and `skills/wb-worktrees`
landing wording and the `ai/capabilities.json` notes for `land`, `pr land`
and `worktree end`.

## Open Questions

None at this time. The release verb and the fleet-reader inventory (first step of task-3) are confirmed.

---
*This document follows the https://specscore.md/plan-specification*
