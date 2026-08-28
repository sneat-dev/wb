---
format: https://specscore.md/plan-specification
status: Implemented
---
# Plan: Mechanical Worktree Merge implementation plan

**Status:** Implemented
**Reconciled:** 2026-08-27
**Source Feature:** mechanical-worktree-merge
**Date:** 2026-08-27
**Owner:** alex
**Supersedes:** —

## Summary

Implement the complete mechanical worktree merge journey across the CLI,
managed-worktree lifecycle, durable receipts, Git/GitHub policy adapters,
exact-head CI observation, canonical synchronization, forward recovery, help,
capability metadata, and tests.

## Journey

The operator selects ready source worktrees. The record first appears as a
local integration candidate containing the exact target and all source heads,
and dependent agents can consume its SHA immediately. With no further operator
action, Phase 2 chooses a permitted delivery route, waits for exact-head checks,
lands the candidate, and makes the remote and eligible canonical checkout show
the same result. The operator can either retain assets for replay or request
receipt-gated cleanup. At every interruption, resume continues from durable
evidence; after a landed failure, revert prepares a new forward change.

## Approach

Build one receipt-backed state machine and expose three entry points: combined,
prepare, and land/resume. Keep Git and GitHub effects behind injectable runners
so real-Git fixtures prove repository mechanics and deterministic adapters prove
policy/PR/CI mechanics. Reuse Work Log creation, exact target fetch, CI waiting,
canonical guards, and lifecycle cleanup rather than creating parallel rules.

## Tasks

### Task 1: Specify the receipt and command contract

**Id:** task-1
**Verifies:** mechanical-worktree-merge#ac:prepare-produces-an-immutable-consumable-candidate, mechanical-worktree-merge#ac:cleanup-is-explicit-and-receipt-gated, mechanical-worktree-merge#ac:agents-discover-merge-from-creation-and-completion-intent
**Depends-On:** —
**Status:** complete

Define the persisted phase/status vocabulary, exact immutable identities,
resume arguments, route model, output schema, and CLI/help surface. Add the
runtime capability entry so agents never infer support from prose alone.
Make merge prominent in both worktree creation and merger skill trigger text,
pair it from `wb worktree create --help`, and write a safe locally ignored
`.worktree.md` reminder without overwriting a repository-owned file.

### Task 2: Prepare the isolated integration candidate

**Id:** task-2
**Verifies:** mechanical-worktree-merge#ac:prepare-produces-an-immutable-consumable-candidate, mechanical-worktree-merge#ac:dependent-agent-can-use-phase-one-without-waiting, mechanical-worktree-merge#ac:every-pre-landing-failure-preserves-work
**Depends-On:** 1
**Status:** complete

Resolve and validate all source worktrees, fetch the exact remote default or
explicit target, create/resume one WB-managed candidate lane, merge ordered
source heads without resolving conflicts, validate, and atomically persist the
candidate receipt.

### Task 3: Land through verified direct or pull-request routes

**Id:** task-3
**Verifies:** mechanical-worktree-merge#ac:auto-route-never-confuses-bypass-with-permission, mechanical-worktree-merge#ac:exact-head-pr-lands-and-synchronizes-canonical
**Depends-On:** 2
**Status:** complete

Implement authoritative route selection, candidate publication, commit-derived
PR text, bounded exact-head waiting, guarded merge/direct update, remote receipt,
and safe canonical fast-forward.

### Task 4: Compose, resume, clean, and revert

**Id:** task-4
**Verifies:** mechanical-worktree-merge#ac:combined-command-walks-the-whole-journey, mechanical-worktree-merge#ac:unpublished-candidate-rebases-over-target-drift, mechanical-worktree-merge#ac:retry-resumes-without-duplicate-effects, mechanical-worktree-merge#ac:landed-failure-has-a-forward-revert-path, mechanical-worktree-merge#ac:landed-target-ci-failure-accepts-an-audited-forward-repair, mechanical-worktree-merge#ac:cleanup-is-explicit-and-receipt-gated
**Depends-On:** 3
**Status:** complete

Compose both phases in the default command, resume every durable boundary,
delegate exact absorbed-asset retirement to lifecycle cleanup, and prepare a
forward revert from landing receipts while refusing conflicts. A preserved
post-target-CI failure also accepts an additive same-source forward repair,
retains every failed landing in the receipt, and reuses the exclusive lane.

### Task 5: Prove isolation and the full journey

**Id:** task-5
**Verifies:** mechanical-worktree-merge#ac:combined-command-walks-the-whole-journey, mechanical-worktree-merge#ac:unpublished-candidate-rebases-over-target-drift, mechanical-worktree-merge#ac:retry-resumes-without-duplicate-effects, mechanical-worktree-merge#ac:every-pre-landing-failure-preserves-work, mechanical-worktree-merge#ac:agents-discover-merge-from-creation-and-completion-intent
**Depends-On:** 4
**Status:** complete

Add real-Git isolation tests and a fake-host end-to-end journey covering
prepare-only, combined PR and direct routes, interruption/resume, clean and
conflicting target rebases, failed checks, canonical blockers, exact-repository
cleanup gating, and revert.

## Open Questions

None at this time.

---

## Resolution

**Reconciled Approved → Implemented outside the tracked `change-status` flow** (5 task(s) marked complete; this did not walk the legal-transition matrix).

Implemented and verified in the coordinated WB feature worktree; the initial plan statuses were not advanced task-by-task during implementation.

Evidence: internal/orchestrate/worktree_merge.go, internal/orchestrate/worktree_merge_test.go, cmd/wb/worktree_merge.go, internal/worktrees/worklog.go
*This document follows the https://specscore.md/plan-specification*
