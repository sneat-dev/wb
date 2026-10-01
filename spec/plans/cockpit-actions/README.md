---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Cockpit actions

**Status:** Draft
**Source Feature:** cockpit-actions
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** wb-cockpit

## Summary

Implement [cockpit-actions](../../features/cockpit-actions/README.md): a
registry of typed actions that Cockpit discovers per selected entity, a
preview-then-run protocol that shows a fresh work-loss assessment before
anything guarded or destructive, execution through the daemon's operation
queue, and the first six actions.

## Journey

The operator opens a worktree whose branch was never pushed. Cockpit offers
Push and Commit as buttons and Discard under a menu. They choose Discard: the
preview says two commits exist only here and demands the branch name. They
cancel, press Push, and watch it finish in the page. They choose Discard
again: the preview now says the work is recoverable, one confirmation runs
it, and the worktree leaves the list.

## Approach

Seven tasks. The registry and its discovery route come first with all six
actions declared and their applicability rules implemented, so the list is
complete before any handler exists. The preview-and-run protocol follows with
a test action, proving tokens, idempotency, operations and recording
independently of Git. Handlers then land in three groups by risk — commit,
push and create; land; discard and delete — each wrapping the existing WB
operation without weakening its refusals. The application's action controls
come once the API is complete, and the journey test closes the plan and the
master plan's journey. Every task keeps the code it adds at 100% statement coverage: Go through `wb coverage --changed`, and `cockpit/web` through a test run that fails below 100% of the application's own source (founder, 2026-10-01).

## Tasks

### Task 1: Registry, discovery and authorization

**Id:** task-1
**Verifies:** cockpit-actions#ac:actions-are-listed-with-reasons, cockpit-actions#ac:unknown-target-and-path-are-refused, cockpit-actions#ac:anonymous-cannot-run, cockpit-actions#ac:push-refuses-diverged
**Depends-On:** —
**Status:** planning

Add the action registry with the declaration fields, the six actions'
declarations and applicability checks, and
`GET /api/v1/cockpit/actions?target=<type>:<id>`. Resolve targets against the
daemon's inventory only; refuse unknown identifiers and any request field
carrying a path, command or argument vector. Add the six action capabilities
to the owner principal and check the capability before anything else.

### Task 2: Preview, run, operations and records

**Id:** task-2
**Verifies:** cockpit-actions#ac:stale-preview-is-refused, cockpit-actions#ac:guarded-action-needs-a-token, cockpit-actions#ac:operation-is-followed-and-idempotent, cockpit-actions#ac:run-is-recorded
**Depends-On:** 1
**Status:** planning

Add the preview route, which calls the fresh assessment and issues a token
bound to the target's head commit and cleanliness, and the run route, which
requires an idempotency key, re-reads the state, refuses a stale token, and
admits the run to the daemon's operation queue. Add
`GET /api/v1/cockpit/operations/{id}`. Record every run with principal,
action, target, non-free-text parameters, the previewed assessment and the
outcome. Settle here whether handlers call library functions or submit the
equivalent `wb` invocation; either way no argument vector comes from the
browser.

### Task 3: Commit, push and create pull request

**Id:** task-3
**Verifies:** cockpit-actions#ac:commit-push-and-create-pr
**Depends-On:** 2
**Status:** planning

Implement the `worktree.commit`, `branch.push` and `pr.create` handlers over
`gitops.AddCommit`, `gitops.Push` and `PushSetUpstream`, and
`orchestrate.CreatePullRequest`. Tests run against temporary repositories, a
bare remote and a fake forge.

### Task 4: Land pull request

**Id:** task-4
**Verifies:** cockpit-actions#ac:land-performs-the-full-landing, cockpit-actions#ac:verb-refusals-surface
**Depends-On:** 3
**Status:** planning

Implement the `pr.land` handler over WB's existing landing operation with
its admission, check waiting, receipt and cleanup intact. Prove that a failing
required check and a rejecting pre-commit hook end the operation failed with
the verb's own message and that no hook is skipped.

### Task 5: Discard worktree and delete branch

**Id:** task-5
**Verifies:** cockpit-actions#ac:at-risk-discard-demands-the-name, cockpit-actions#ac:safe-discard-confirms-once, cockpit-actions#ac:delete-branch
**Depends-On:** 2
**Status:** planning

Implement `worktree.discard` over `worktrees.Abort` with the discarded
disposition and `branch.delete` over `worktrees.BranchCleanup`. The preview
states the commits and uncommitted or untracked content that exist only in
the target and whether another copy was found, and marks the run as needing
the typed name when the target is at risk.

### Task 6: Action controls in the application

**Id:** task-6
**Verifies:** cockpit-actions#ac:application-renders-what-the-registry-returns, cockpit-actions#ac:at-risk-discard-demands-the-name, cockpit-actions#ac:coverage-gates-hold
**Depends-On:** 2, 5
**Status:** planning

Build the action controls from the discovery response: buttons for safe and
guarded actions, an overflow menu for destructive ones, disabled controls
with their reason, parameter forms, the preview dialog with the assessment,
the typed-name confirmation, and live operation progress without a reload.
No control appears in a hover card.

### Task 7: Whole-journey end-to-end test

**Id:** task-7
**Verifies:** cockpit-actions#ac:whole-journey-e2e
**Depends-On:** 3, 4, 5, 6
**Status:** planning

One Playwright test against a real daemon, a repository and a bare remote
with a worktree holding two unpushed commits: open it, choose Discard, cancel
at the name prompt, Push and wait in the page, Discard again with one
confirmation, and assert the worktree is gone while its commits are on the
remote.

## Open Questions

- Whether `pr.land` needs an approval parameter in its first cut is settled
  in Task 4.

---
*This document follows the https://specscore.md/plan-specification*
