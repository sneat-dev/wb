---
format: https://specscore.md/plan-specification
status: Approved
---
# Plan: Cockpit actions

**Status:** Approved
**Source Feature:** cockpit-actions
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** wb-cockpit

## Summary

Implement [cockpit-actions](../../features/cockpit-actions/README.md): a
registry of typed actions that Cockpit discovers per selected entity, a
preview-then-run protocol that shows a fresh work-loss assessment before
anything guarded or destructive, execution as typed daemon operations, and
the first seven actions.

## Journey

The operator opens a worktree whose two commits were never pushed. Discard is
disabled and says to push first. They press Push and watch it finish in the
page; Discard becomes available. They edit a file, leave it uncommitted and
choose Discard: the preview says the commits are on the remote and one file
will be archived, and demands the branch name. They type it and the worktree
leaves the list.

## Approach

Nine tasks. The registry and its discovery route come first with all seven
actions declared and their applicability rules implemented from the snapshot
assessment, so the list is complete before any handler exists. The second
task adds the typed operation kind to the daemon queue with a test action;
the third adds preview tokens and re-verification under the worktree lock;
the fourth records runs. Handlers then land by
risk — commit, push and create; land; discard, delete and index refresh —
each wrapping the existing WB operation without weakening its refusals. The
application's action controls come once the API is complete, and the journey
test closes this plan and the master plan's journey. Every task keeps the code it adds at 100% coverage: Go through `wb coverage --changed`, and `cockpit/web` through thresholds of 100 for statements, branches, functions and lines plus a rendering test for every component (founder, 2026-10-01).

## Tasks

### Task 1: Registry, discovery and authorization

**Id:** task-1
**Verifies:** cockpit-actions#ac:actions-are-listed-with-reasons, cockpit-actions#ac:unknown-target-path-and-cached-entry-are-refused, cockpit-actions#ac:anonymous-cannot-run, cockpit-actions#ac:push-refuses-diverged, cockpit-actions#ac:unpushed-commits-block-discard
**Depends-On:** —
**Status:** planning

Add the action registry with the declaration fields, the seven actions'
declarations and applicability checks, and
`GET /api/v1/cockpit/actions?target=<type>:<id>`. Resolve targets from the
fleet read model's stable identifiers only, and answer the list from the
snapshot alone with no Git command or network call; refuse unknown identifiers, any
request field carrying a path, command or argument vector, and every `cached`
entry. Add the seven action capabilities to the owner principal and check the
capability before anything else: 401 without a session, 403 without the
capability.

### Task 2: Typed operations and the operations route

**Id:** task-2
**Verifies:** cockpit-actions#ac:operation-is-followed-and-idempotent, cockpit-actions#ac:argument-vector-is-unreachable-over-http
**Depends-On:** 1
**Status:** planning

Add a typed operation kind to the daemon's queue that carries an action
identifier, a target identifier and validated parameters, executed in the
daemon by the action's handler; the argument-vector submission stays on the
unix socket only. Add the run route for `safe` actions with its idempotency
key, `GET /api/v1/cockpit/operations/{id}`, and the refresh of the affected
repository in the fleet snapshot before a terminal state is reported. Proved
with a test action.

### Task 3: Preview tokens and re-verification

**Id:** task-3
**Verifies:** cockpit-actions#ac:stale-preview-is-refused-at-admission, cockpit-actions#ac:change-while-queued-is-refused-at-execution, cockpit-actions#ac:token-is-bound-single-use-and-expires
**Depends-On:** 2
**Status:** planning

Add the preview route, which calls the fresh assessment and issues a
single-use, five-minute token bound to session, action, target, parameters,
head commit, working-tree status digest, remote head and owner liveness.
Require the token for `guarded` and `destructive` runs, verify it and the
bound state at admission, and re-verify at execution start under the
worktree's writer lock.

### Task 4: Run records

**Id:** task-4
**Verifies:** cockpit-actions#ac:run-is-recorded
**Depends-On:** 3
**Status:** planning

Record on each operation the principal, action, target, parameters other than
free text, the assessment shown at preview and the outcome, readable through
the operations route by a caller holding the action's capability.

### Task 5: Commit, push and create pull request

**Id:** task-5
**Verifies:** cockpit-actions#ac:commit-push-and-create-pr
**Depends-On:** 3
**Status:** planning

Implement the `worktree.commit`, `branch.push` and `pr.create` handlers over
`gitops.AddCommit`, `gitops.Push` and `PushSetUpstream`, and
`orchestrate.CreatePullRequest`. Tests run against temporary repositories, a
bare remote and a fake forge.

### Task 6: Land pull request, moving landing wiring out of the command layer

**Id:** task-6
**Verifies:** cockpit-actions#ac:land-performs-the-full-landing, cockpit-actions#ac:verb-refusals-surface
**Depends-On:** 5
**Status:** planning

Admission, host-load and landing-lane handling for landing live in `cmd/wb`
today. Move them behind a function the daemon can call, with the command
calling the same function and its behavior unchanged, then implement the
`pr.land` handler over it with check waiting, receipt and cleanup intact. Prove that
a failing required check and a rejecting pre-commit hook end the operation
failed with the operation's own message and that no hook is skipped.

### Task 7: Discard, delete branch and refresh index

**Id:** task-7
**Verifies:** cockpit-actions#ac:unproven-remote-blocks-discard-at-preview, cockpit-actions#ac:at-risk-discard-demands-the-name, cockpit-actions#ac:safe-discard-confirms-once, cockpit-actions#ac:delete-branch, cockpit-actions#ac:code-index-refresh
**Depends-On:** 3
**Status:** planning

Implement `worktree.discard` over `worktrees.Abort` for one worktree with the
discarded disposition, `branch.delete` over `worktrees.BranchCleanup`, and
`index.refresh` through the lifecycle runner, adding an on-demand trigger for
one checkout if the runner has none. The discard preview is refused unless
the fresh assessment proves every commit is on the remote or merged; when it
proceeds it states what the
operation preserves and where, what will exist nowhere else, and how ignored
files are treated; an at-risk run must carry the matching name, checked by
the daemon.

### Task 8: Action controls in the application

**Id:** task-8
**Verifies:** cockpit-actions#ac:application-renders-what-the-registry-returns, cockpit-actions#ac:coverage-gates-hold
**Depends-On:** 3, 7
**Status:** planning

Build the action controls from the discovery response: buttons for safe and
guarded actions, an overflow menu for destructive ones, disabled controls
with their reason, parameter forms, the preview dialog with the assessment,
the typed-name confirmation, the refresh button beside the freshness
indicator, and live operation progress without a reload. No control appears
in a hover card.

### Task 9: Whole-journey end-to-end test

**Id:** task-9
**Verifies:** cockpit-actions#ac:whole-journey-e2e
**Depends-On:** 4, 5, 6, 7, 8
**Status:** planning

One Playwright test from a cold start with a repository, a bare remote and a
worktree holding two commits on a never-pushed branch: run `wb cockpit`,
follow the URL, assert Work at Risk, open the worktree, assert the disabled
Discard and its reason, Push and wait in the page, modify a file from outside
Cockpit, Discard with the typed name, and assert the worktree is gone, its
commits are on the remote and the file's content is in the Work Log
archive.

## Open Questions

- Whether `pr.land` needs an approval parameter in its first cut is settled
  in Task 6.
- What the existing abort does with ignored files is settled in Task 7 and
  decides the preview's wording.

---
*This document follows the https://specscore.md/plan-specification*
