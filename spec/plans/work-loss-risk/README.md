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
the reasons it could be lost, verify the remote before any mutation relies on
it, and lead the Cockpit Dashboard with the result.

## Journey

An agent commits to a new branch and stops without pushing. The operator
opens Cockpit and sees that worktree first under Work at Risk, with the
reason. They push from a terminal and do nothing else; after the next refresh
it has left the list. They edit one file; it is back, as uncommitted changes.

## Approach

Six tasks. The rules are a function over a plain facts struct, so every
combination is table-tested, and the first task also builds the worktree
collector so its acceptance scenarios run against real repositories. The
second task covers the remaining reasons and canonical clones. The fresh
assessment, which asks the remote, is its own task because it is what
destructive actions rely on. Read-model wiring and other machines follow,
then the Dashboard section, then the journey test. Every task keeps the code it adds at 100% coverage: Go through `wb coverage --changed`, and `cockpit/web` through thresholds of 100 for statements, branches, functions and lines plus a rendering test for every component (founder, 2026-10-01).

## Tasks

### Task 1: Assessment rules and the worktree collector

**Id:** task-1
**Verifies:** work-loss-risk#ac:levels-follow-git-state, work-loss-risk#ac:dirty-worktree-with-open-pr-is-at-risk, work-loss-risk#ac:missing-evidence-yields-unknown
**Depends-On:** —
**Status:** planning

Add a package whose core takes a facts struct — dirty, untracked, ahead,
behind, upstream configured and present, remote evidence present, remote head
known, open or merged pull request, integrated at origin, each with a "known"
flag — and returns the level and reason codes, with least-durable-wins and
the rule that a missing fact can only lower the level. Add the worktree
collector over `gitops.Status`, `gitops.Tracking`, `gitops.UnpushedWork` and
`worktrees.ListResult`, treating a repository with no remote-tracking ref as
`no_remote_evidence` rather than as nothing unpushed. Table tests cover the
rules; scenario tests run against temporary repositories with a bare
remote.

### Task 2: Remaining reasons, canonical clones and cleanup candidates

**Id:** task-2
**Verifies:** work-loss-risk#ac:upstream-gone-diverged-stash-and-detached, work-loss-risk#ac:canonical-clone-is-assessed, work-loss-risk#ac:stopped-owner-is-flagged, work-loss-risk#ac:merged-is-cleanup-not-risk
**Depends-On:** 1
**Status:** planning

Add `upstream_gone`, `diverged`, `stash`, `detached_head` and
`owner_stopped`, and the at-risk definition that includes them whatever the
level. Assess canonical clones: their uncommitted and untracked content,
unpushed branches without a worktree from `worktrees.BranchEntry`, and stash
entries. Classify cleanup candidates, excluding anything with a live owner or
at risk.

### Task 3: Fresh assessment that verifies the remote

**Id:** task-3
**Verifies:** work-loss-risk#ac:fresh-assessment-sees-new-edits, work-loss-risk#ac:fresh-assessment-catches-a-vanished-remote-branch
**Depends-On:** 2
**Status:** planning

Add the single-target fresh assessment: recompute from Git, ask the remote
for the branch's current head, drop to `local_commits` when the remote branch
is gone or no longer contains the local head, return `unknown` when the
remote cannot be reached, set `remote_verified`, and count ignored files.
Tests use a bare remote that is deleted from, force-pushed and made
unreachable while the local tracking refs stay unchanged.

### Task 4: Read model, attention list and other machines

**Id:** task-4
**Verifies:** work-loss-risk#ac:remote-snapshots-are-cached-or-unknown, work-loss-risk#ac:read-model-carries-codes-only
**Depends-On:** 2
**Status:** planning

Have the daemon's snapshotter attach `durability`, `risks` and
`remote_verified: false` to every worktree, branch and repository in the
fleet document, as codes and counts only. For other machines, assess listed
non-clean repositories from `remotestate.Snapshot`, mark them `cached`, add
`machine_unreachable` past the staleness threshold, and make every cached
worktree and every hub-only machine `unknown`. Add
`GET /api/v1/cockpit/attention` with the ordering rule.

### Task 5: Work at Risk on the Dashboard

**Id:** task-5
**Verifies:** work-loss-risk#ac:attention-list-order-and-empty-state, work-loss-risk#ac:coverage-gates-hold
**Depends-On:** 4
**Status:** planning

Add the Work at Risk section as the first section of the Dashboard, the
cleanup-candidates section below it, durability and risk columns on the
Worktrees and Repositories tables worded as of the last fetch, risk counts
with hover cards and drill-down, and the explicit empty state.

### Task 6: Whole-journey end-to-end test

**Id:** task-6
**Verifies:** work-loss-risk#ac:whole-journey-e2e
**Depends-On:** 5
**Status:** planning

One Playwright test against a real daemon, a repository and a bare remote:
assert the unpushed worktree is listed with `branch_never_pushed`, push from
outside Cockpit and wait for a refresh, assert it is gone, modify a tracked
file and wait, assert it is back with `uncommitted_changes`.

## Open Questions

- The staleness threshold reuses the 24-hour value `wb remote` already
  applies, unless Task 4 finds a reason not to.

---
*This document follows the https://specscore.md/plan-specification*
