---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Work-loss risk

**Status:** Draft
**Source Feature:** work-loss-risk
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** wb-cockpit

## Summary

Implement [work-loss-risk](../../features/work-loss-risk/README.md): assess
how durable the work in every worktree, branch and canonical clone is, name
the reasons it could be lost, and lead the Cockpit Dashboard with the result.

## Journey

An agent commits to a new branch and stops without pushing. The operator
opens Cockpit and sees that worktree first under Work at Risk, with the
reason. They push from a terminal and do nothing else; after the next refresh
it has left the list. They edit one file; it is back, as uncommitted changes.

## Approach

Five tasks. The assessment is a pure function over facts first, so its rules
are tested exhaustively without Git. Collectors that gather those facts from
`gitops` and `worktrees` come second. The third task wires assessments into
the read model, adds the attention list and the fresh on-demand route, and
handles other machines' snapshots. The Dashboard section follows, and the
journey test closes the plan. Every task keeps the code it adds at 100% statement coverage: Go through `wb coverage --changed`, and `cockpit/web` through a test run that fails below 100% of the application's own source (founder, 2026-10-01).

## Tasks

### Task 1: Assessment rules

**Id:** task-1
**Verifies:** work-loss-risk#ac:levels-follow-git-state, work-loss-risk#ac:dirty-worktree-with-open-pr-is-at-risk, work-loss-risk#ac:failure-yields-unknown
**Depends-On:** —
**Status:** planning

Add a package that takes a plain facts struct — dirty, untracked, ahead,
behind, upstream configured and present, remote head known, open or merged
pull request, integrated at origin, each with a "known" flag — and returns
the durability level and reason codes. Implement least-durable-wins and the
rule that a missing fact can only lower the level. Table tests cover every
level, every reason and every unknown combination.

### Task 2: Fact collectors

**Id:** task-2
**Verifies:** work-loss-risk#ac:upstream-gone-diverged-and-stash, work-loss-risk#ac:stopped-owner-is-flagged, work-loss-risk#ac:merged-is-cleanup-not-risk
**Depends-On:** 1
**Status:** planning

Build the facts from what WB already computes: `gitops.Status`,
`gitops.Tracking` and `gitops.UnpushedWork` for dirty, untracked, stash,
ahead, behind, diverged and upstream-gone; `worktrees.ListResult` for remote
head, integration and pull request evidence and owner state;
`worktrees.BranchEntry` for branches without a worktree. Cover canonical
clones. Classify `merged` entries that still exist as cleanup candidates.
Tests run against real temporary repositories with a bare remote.

### Task 3: Read model, attention list and other machines

**Id:** task-3
**Verifies:** work-loss-risk#ac:remote-snapshots-are-cached-or-unknown, work-loss-risk#ac:read-model-carries-codes-only, work-loss-risk#ac:fresh-assessment-sees-new-edits
**Depends-On:** 2
**Status:** planning

Have the daemon's snapshotter attach `durability` and `risks` to every
worktree and branch in the fleet document, as codes and counts only. Derive
assessments for other machines from `remotestate.Snapshot` repository state,
marked `cached`, with `machine_unreachable` past the staleness threshold, and
`unknown` where a snapshot carries no Git state. Add
`GET /api/v1/cockpit/attention` with the ordering rule, and the fresh
single-target assessment used by callers about to change state.

### Task 4: Work at Risk on the Dashboard

**Id:** task-4
**Verifies:** work-loss-risk#ac:attention-list-order-and-empty-state, work-loss-risk#ac:coverage-gates-hold
**Depends-On:** 3
**Status:** planning

Add the Work at Risk section as the first section of the Dashboard, the
cleanup-candidates section below it, durability and risk columns on the
Worktrees and Repositories tables, risk counts with hover cards and
drill-down, and the explicit empty state.

### Task 5: Whole-journey end-to-end test

**Id:** task-5
**Verifies:** work-loss-risk#ac:whole-journey-e2e
**Depends-On:** 4
**Status:** planning

One Playwright test against a real daemon, a repository and a bare remote:
assert the unpushed worktree is listed with `branch_never_pushed`, push from
outside Cockpit and wait for a refresh, assert it is gone, modify a tracked
file and wait, assert it is back with `uncommitted_changes`.

## Open Questions

- The staleness threshold reuses the 24-hour value `wb remote` already
  applies, unless Task 3 finds a reason not to.

---
*This document follows the https://specscore.md/plan-specification*
