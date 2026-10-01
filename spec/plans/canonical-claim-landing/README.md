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

Four tasks. The never-removable guarantee has no dependency and goes first and
in parallel with the inventory of fleet readers. Restore needs the guarantee and
the other plan's claim lookup and marker. The journey test closes the plan.
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
helpers) used by cleanup, gc, abort, retire, end and
`cleanupWorktreeMergeAssets`. Add a dedicated test that feeds a canonical path
to every entry point with `--apply` and `--force`.

### Task 2: Landing and release restore the clone

**Id:** task-2
**Verifies:** canonical-claim-landing#ac:landing-restores-clone-and-keeps-it, canonical-claim-landing#ac:restore-refuses-without-destroying
**Depends-On:** 1
**Status:** planning

Needs canonical-claim-mode task-1 and task-4. Add one resumable restore routine (verify clean on claim branch, fetch,
checkout base, ff-only, branch containment check, delete branch, seal, marker)
with per-step receipt entries, reusing the `syncCanonicalMergeTarget` checks in
`internal/orchestrate/worktree_merge.go` and `pr_land.go`. Wire it into
`wb worktree land`/`wb land`, `wb pr create` landing, `wb pr land`, and
`wb worktree end` (release, branch kept). Real bare-remote tests, interrupt
after each step.

### Task 3: Sync, hygiene, and fleet readers skip a claimed clone

**Id:** task-3
**Verifies:** canonical-claim-landing#ac:sync-and-readers-skip-claimed-clone, canonical-claim-landing#ac:no-removal-path-reaches-a-canonical-clone
**Depends-On:** 1
**Status:** planning

Needs canonical-claim-mode task-1. First inventory every reader that treats the canonical working tree as the base
tip (open question), then: `fleetsync.syncActive` skips with a distinct status,
branch hygiene and cleanup skip and report the claim, and
`inspectCanonicalFreshness` plus fleet status/dependency/scan readers use the
remote base ref and report the clone as claimed.

### Task 4: End-to-end landing journey test

**Id:** task-4
**Verifies:** canonical-claim-landing#ac:landing-restores-clone-and-keeps-it
**Depends-On:** 2, 3
**Status:** planning

Needs canonical-claim-mode task-5. One real-remote test walking the Journey
through each landing verb, plus an interrupted-restore resume and a release via
`wb worktree end`; then update `skills/wb-merge` and `skills/wb-worktrees`
landing wording and the `ai/capabilities.json` notes for `land`, `pr land`
and `worktree end`.

## Open Questions

- Release verb (`wb worktree end`) and the fleet-reader inventory are open in the Feature.

---
*This document follows the https://specscore.md/plan-specification*
