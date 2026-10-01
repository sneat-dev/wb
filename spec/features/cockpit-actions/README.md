---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Cockpit actions

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=request-change) |
**Status:** Approved
**Source Ideas:** wb-cockpit

## Summary

Cockpit turns from a view into an operational surface through a registry of
typed actions. Each action declares what it applies to, what it requires and
how dangerous it is; Cockpit discovers the actions that apply to a selected
entity and runs them through the daemon. The first actions are commit, push,
create a pull request, land a pull request, discard a worktree, delete a
branch and refresh the code index. This is the "select, inspect, act" loop of
[WB Cockpit](../../ideas/wb-cockpit.md).

## Problem

Seeing that a worktree holds unpushed work is half an answer; the operator
then has to find a terminal, change directory and remember the right verb.
The existing dashboards cannot help, by design: the loopback dashboard is
read-only and the daemon's operation queue accepts a raw argument vector,
which must never be reachable from a browser.

Actions are also where work is lost. Discarding a worktree or deleting a
branch is safe only if something else holds the work, and the operator
cannot be expected to check five Git facts first.

## Journey

1. **Start.** In Cockpit I open a worktree that holds two commits on a branch
   that was never pushed.
   **Observable good result:** the page offers Push as a button; Discard is
   under a menu and disabled, and says why: two commits exist only here, push
   or land them first.
2. **Middle.** I press Push.
   **Observable good result:** the push shows its progress and result without
   a page reload, and with nothing further from me the worktree's durability
   changes and Discard becomes available.
3. **End.** I edit one file in that worktree, leave it uncommitted, and
   choose Discard.
   **Observable good result:** before anything is removed Cockpit shows a
   fresh assessment — the commits are on the remote, one modified file exists
   only here and will be archived in the Work Log — and will not proceed
   until I type the branch name. After I do, the worktree leaves the list.

## Behavior

### Registry

#### REQ: action-declaration

Every action is registered with:

- a stable identifier and a title;
- the target types it applies to — `worktree`, `branch`, `pull_request` or
  `repository`;
- an applicability check that, for a given target, returns applicable, or
  not applicable with one reason in plain words;
- its parameters, each with a name, type and whether it is required;
- the one capability it requires;
- a safety class: `safe`, `guarded` or `destructive`;
- a handler.

#### REQ: actions-are-discovered

`GET /api/v1/cockpit/actions?target=<type>:<id>` returns every action
registered for that target type, each with its applicability, reason,
parameters, required capability, safety class and whether the caller holds
the capability. It is a metadata route. The application MUST build its action
controls from this response and MUST NOT hardcode which actions exist. An
action the caller cannot run is shown disabled with the reason.

#### REQ: targets-are-resolved-by-the-daemon

A target is named by its type and the stable `id` the fleet read model gives
it ([cockpit](../cockpit/README.md)#req:fleet-read-model). The daemon
resolves it to a path and a ref itself. No request field carries a filesystem
path, a command, or an argument vector, and a target that is not in the
inventory is refused. Every target in this Feature is on this daemon's
machine; a `cached` entry has no applicable action.

### Authorization and safety

#### REQ: capability-then-safety

Running an action requires the capability it declares, under
[cockpit](../cockpit/README.md)#req:owner-routes: a caller with no session is
refused with status 401 and a session without the capability with status 403,
whatever the action's safety class. Safety confirmation is a separate, later
step and never substitutes for the capability.

This Feature adds the capabilities `git.commit`, `branch.push`,
`branch.delete`, `worktree.discard`, `pr.create`, `pr.land` and
`index.refresh`. The `owner` principal holds them; `anonymous-local` does
not.

#### REQ: preview-before-run

`POST /api/v1/cockpit/actions/{id}/preview` computes, for the target and
parameters, what the action will do and returns a preview token. For a
`guarded` or `destructive` action the preview carries a fresh assessment from
[work-loss-risk](../work-loss-risk/README.md)#req:fresh-assessment-on-demand.

The token is single-use, expires after five minutes, and is bound to: the
session that requested it, the action, the target, the parameters, the
target's head commit, a digest of its working-tree status, the remote
branch's head, and whether a live owner holds the worktree.

`POST /api/v1/cockpit/actions/{id}/run` requires an idempotency key and, for
a `guarded` or `destructive` action, a preview token. A `safe` action runs
without a preview.

#### REQ: state-is-reverified-at-execution

A run waits in the daemon's queue before it executes, and the target can
change meanwhile. The daemon MUST re-read every state the token is bound to
twice: when the run is admitted, and again when execution starts, while
holding the lock that excludes other WB writers on that worktree. A
difference at either point ends the run as refused-stale with no effect.

#### REQ: destructive-actions-name-what-is-lost

The preview of a `destructive` action MUST state, from the fresh assessment:
what the operation will preserve and where; what will exist nowhere else once
it completes; and how many ignored files the working tree holds and whether
the operation preserves them.

When the target is at risk, the run request MUST also carry the branch or
task name, and the daemon refuses a run whose name does not match. The
application collects it by having the operator type it. When the target is
not at risk, one confirmation suffices.

#### REQ: wrapped-refusals-are-preserved

An action that performs an existing WB operation MUST NOT weaken it. Every
refusal the corresponding operation would raise — a landing lane held by
another session, a failing check, a missing approval, an active claim, a
remote branch that no longer matches the local head — is raised by the
action and shown to the operator with the operation's own message. Git hooks
run exactly as they do for the operation and are never bypassed.

### Execution

#### REQ: runs-are-typed-daemon-operations

A run is admitted to the daemon's operation queue as a typed operation that
carries the action identifier, the target identifier and the validated
parameters. It returns the operation's identifier at once. The queue's
existing argument-vector submission MUST NOT be reachable from the loopback
HTTP listener.

The application follows a run through
`GET /api/v1/cockpit/operations/{id}`, which reports queued, running or a
terminal state, and the outcome with the operation's summary lines. A second
run with the same idempotency key returns the first run's operation and
starts nothing.

#### REQ: read-model-reflects-a-finished-run

When a run reaches a terminal state, the daemon refreshes the affected
repository in the fleet snapshot
([cockpit](../cockpit/README.md)#req:snapshot-refresh) before the operation
is reported as terminal. A page that re-reads the read model after seeing the
terminal state therefore sees the result.

#### REQ: runs-are-recorded

Every run is recorded on its operation with the principal, the action
identifier, the target, the parameters other than free text, the assessment
shown at preview, and the outcome. A caller holding the action's capability
reads the record through the operations route.

### First actions

#### REQ: commit-action

`worktree.commit` applies to a worktree with uncommitted or untracked
content. It takes a required message and commits every change in the working
tree, as WB's existing commit path does. It is `guarded` and requires
`git.commit`.

#### REQ: push-action

`branch.push` applies to a worktree whose checked-out branch has commits its
remote lacks, and sets the upstream when the branch was never pushed. It is
not applicable to a diverged branch, and says so. It is `guarded` and
requires `branch.push`.

#### REQ: create-pr-action

`pr.create` applies to a worktree whose branch has no open pull request. It
takes a title and an optional body, and commits and pushes first when the
worktree needs it, as the existing pull-request creation operation does. It
is `guarded` and requires `pr.create`.

#### REQ: land-action

`pr.land` applies to an open pull request in the read model, or to a worktree
that has one. It performs WB's existing landing operation, including its
admission, check waiting, remote receipt and cleanup. It is `guarded` and
requires `pr.land`.

#### REQ: discard-worktree-action

`worktree.discard` performs WB's existing abort of one worktree with the
`discarded` disposition, which removes the checkout, retires its branch, and
first archives any uncommitted content in the private Work Log.

It is not applicable while a live owner holds the worktree, and not
applicable while the fresh assessment shows commits that are on no remote:
the reason tells the operator to push or land them first. Discarding
unpushed commits is not offered in this Feature. It is `destructive` and
requires `worktree.discard`.

#### REQ: delete-branch-action

`branch.delete` applies to a local or remote branch with no worktree, through
WB's existing branch cleanup and its eligibility rules. It is `destructive`
and requires `branch.delete`.

#### REQ: refresh-code-index-action

`index.refresh` applies to a repository or a worktree. It runs the indexer
the operator has configured, through the lifecycle runner that already runs
it after a checkout moves, in the background and under the same CPU
admission. It touches only the index, never the working tree, so it is `safe`
and needs no preview. It requires `index.refresh`. When no indexer is
configured it is not applicable and says so.

### Presentation

#### REQ: common-actions-are-direct

On an entity's page, applicable `safe` and `guarded` actions are buttons and
`destructive` actions sit under an overflow menu. The code-index refresh
button sits beside the freshness indicator. No action control appears inside
a hover card.

### Test coverage

#### REQ: new-code-is-fully-covered

All code this Feature adds MUST reach 100% test coverage, in Go and in
`cockpit/web`, under the gates defined by
[cockpit](../cockpit/README.md)#req:new-code-is-fully-covered.

## Dependencies

- [cockpit](../cockpit/README.md)
- [work-loss-risk](../work-loss-risk/README.md)
- [mechanical-worktree-merge](../mechanical-worktree-merge/README.md)
- [branch-hygiene](../branch-hygiene/README.md)
- [worktree-lifecycle](../worktree-lifecycle/README.md)
- [code-index-freshness](../code-index-freshness/README.md)

## Not Doing

- Discarding a worktree that holds commits on no remote. The prompt asks for
  a warning rather than a refusal; this Feature takes the stricter path until
  such commits can be preserved first, as
  [operations-journal](../operations-journal/README.md) describes.
- Agent actions — dispatch, steer, stop, cancel, kill and resume wait for the
  herdr dispatch Feature.
- Instructing an agent with selected context — the same Feature.
- A command palette — the registry makes one possible; it is not built here.
- Multi-selection and bulk actions.
- Actions on another machine.
- Committing only staged changes, and pushing a branch that is not checked
  out in a worktree.

## Acceptance Criteria

### AC: actions-are-listed-with-reasons

**Requirements:** cockpit-actions#req:action-declaration, cockpit-actions#req:actions-are-discovered

Scenario: A clean, pushed worktree
Given a clean worktree whose branch is on the remote with no pull request
When the actions for that worktree are requested with an owner session
Then the response lists `worktree.commit` and `branch.push` as not applicable, each with a reason, lists `pr.create` and `worktree.discard` as applicable, and gives every action its parameters, capability and safety class

### AC: application-renders-what-the-registry-returns

**Requirements:** cockpit-actions#req:actions-are-discovered, cockpit-actions#req:common-actions-are-direct

Scenario: An action the application has never heard of
Given a test registry containing one extra `safe` action for worktrees
When a worktree page is opened and its row is hovered on the Worktrees list
Then the extra action appears as a button on the page with no change to the application, destructive actions are under the overflow menu, and the hover card has no action control

### AC: unknown-target-path-and-cached-entry-are-refused

**Requirements:** cockpit-actions#req:targets-are-resolved-by-the-daemon

Scenario: Three bad targets
Given an owner session
When a run names a worktree identifier that is not in the inventory, another request adds a filesystem path field, and the actions for a `cached` worktree of another machine are requested
Then the first is refused with status 404, the second with status 400, no Git command ran, and the third lists no applicable action

### AC: anonymous-cannot-run

**Requirements:** cockpit-actions#req:capability-then-safety

Scenario: No session, and the hosted origin
Given a loopback request with no session cookie, and a request from the hosted origin
When each attempts to preview and to run `branch.push`
Then every attempt is refused — status 401 for the loopback request — nothing is pushed, and the action list each caller can read marks the action as needing an owner session

### AC: stale-preview-is-refused-at-admission

**Requirements:** cockpit-actions#req:preview-before-run, cockpit-actions#req:state-is-reverified-at-execution

Scenario: The target changed after the preview
Given preview tokens for `worktree.discard` on four clean worktrees
When, before each run is requested, one worktree gets a modified file, one gets a new commit, one has its remote branch moved, and one is taken by a live owner
Then every run is refused as stale and every worktree still exists

### AC: change-while-queued-is-refused-at-execution

**Requirements:** cockpit-actions#req:state-is-reverified-at-execution

Scenario: The target changed after admission
Given a `worktree.discard` run that has been admitted and is held in the queue by a test hook
When a file in the worktree is modified and the run is released
Then the operation ends refused-stale and the worktree and the modified file still exist

### AC: token-is-bound-single-use-and-expires

**Requirements:** cockpit-actions#req:preview-before-run

Scenario: Four misuses of a token
Given a preview token for `worktree.discard` on one worktree
When it is presented for a different action, for a different worktree, from a different session, a second time after a successful run, and separately after six minutes
Then every presentation is refused and nothing is removed, and a `guarded` run with no token at all is refused as well

### AC: unpushed-commits-block-discard

**Requirements:** cockpit-actions#req:discard-worktree-action

Scenario: Two commits exist only here
Given a worktree with two commits on a branch that was never pushed, and another held by a live owner
When the actions for each are requested
Then `worktree.discard` is not applicable for the first with a reason naming two commits that are on no remote, and not applicable for the second with a reason naming the owner

### AC: at-risk-discard-demands-the-name

**Requirements:** cockpit-actions#req:destructive-actions-name-what-is-lost, cockpit-actions#req:discard-worktree-action

Scenario: Pushed commits, one uncommitted file, two ignored files
Given a worktree whose branch is fully pushed, with one modified tracked file and two ignored files
When the operator chooses Discard, and a run is also attempted directly with a wrong name
Then the preview states that the commits are on the remote, that one modified file exists only here and will be archived in the Work Log, and how the two ignored files are treated; the run control is disabled until the branch name is typed; the run with the wrong name is refused by the daemon; and after the right name is confirmed the worktree is gone, recorded as discarded, and the modified file's content is in the Work Log archive

### AC: safe-discard-confirms-once

**Requirements:** cockpit-actions#req:destructive-actions-name-what-is-lost

Scenario: Everything is on the remote
Given a clean worktree whose branch is fully pushed and verified at the remote
When the operator chooses Discard
Then the preview says nothing exists only here and one confirmation runs it

### AC: verb-refusals-surface

**Requirements:** cockpit-actions#req:wrapped-refusals-are-preserved, cockpit-actions#req:land-action

Scenario: Landing while a check fails, and committing against a failing hook
Given a pull request with a failing required check, and a worktree whose pre-commit hook rejects the change
When `pr.land` and `worktree.commit` are run
Then both operations end failed with the operation's own refusal text, the pull request is not merged, no commit is created, and no hook was skipped

### AC: land-performs-the-full-landing

**Requirements:** cockpit-actions#req:land-action

Scenario: A green pull request, from either target
Given a worktree with an open pull request whose checks pass against a fake forge
When the actions for the pull request entry and for the worktree are requested, and `pr.land` is run on the pull request
Then `pr.land` is applicable for both targets, the pull request is merged, the operation's outcome carries the landing receipt, and the worktree has been cleaned up as the landing operation does

### AC: commit-push-and-create-pr

**Requirements:** cockpit-actions#req:commit-action, cockpit-actions#req:push-action, cockpit-actions#req:create-pr-action

Scenario: From dirty to pull request
Given a worktree with one modified file and one untracked file on a never-pushed branch, and a fake forge
When `worktree.commit` is run with a message, then `branch.push`, then `pr.create` with a title
Then one commit with that message contains both files, the branch exists on the remote with its upstream set, and one pull request is open for it

### AC: push-refuses-diverged

**Requirements:** cockpit-actions#req:push-action

Scenario: Both sides moved
Given a worktree whose branch has diverged from its remote
When the actions for it are requested
Then `branch.push` is not applicable and its reason says the branch has diverged

### AC: delete-branch

**Requirements:** cockpit-actions#req:delete-branch-action

Scenario: A leftover branch, and one in use
Given a merged local branch with no worktree, and a branch checked out in a worktree
When the actions for each are requested and `branch.delete` is previewed and run on the first
Then the first branch is deleted, and for the second `branch.delete` is not applicable with a reason naming the worktree

### AC: code-index-refresh

**Requirements:** cockpit-actions#req:refresh-code-index-action, cockpit-actions#req:read-model-reflects-a-finished-run

Scenario: A stale index, and no indexer
Given a worktree whose code index is three commits behind with a fake indexer configured, and a second daemon with no indexer configured
When `index.refresh` is run on the worktree with an owner session and no preview, and the actions are requested on the second daemon
Then the fake indexer ran once for that checkout, the working tree is unchanged, the read model shows `fresh` once the operation is terminal, and on the second daemon `index.refresh` is not applicable with a reason

### AC: operation-is-followed-and-idempotent

**Requirements:** cockpit-actions#req:runs-are-typed-daemon-operations, cockpit-actions#req:read-model-reflects-a-finished-run

Scenario: A double click
Given a `branch.push` run that has been admitted
When the same run is requested again with the same idempotency key, the operation is polled to completion, and the read model is then requested
Then both requests return the same operation identifier, the branch was pushed once, the final state is succeeded with the operation's summary, and the read model already shows the branch on the remote

### AC: argument-vector-is-unreachable-over-http

**Requirements:** cockpit-actions#req:runs-are-typed-daemon-operations

Scenario: Trying to submit a raw command
Given an owner session
When a run request carries an `argv` field, and every route on the loopback HTTP listener is enumerated
Then the request is refused with status 400, and no route on that listener admits an argument-vector operation

### AC: run-is-recorded

**Requirements:** cockpit-actions#req:runs-are-recorded

Scenario: After a commit
Given a completed `worktree.commit` run with a message
When its operation is read with an owner session
Then the record names the owner principal, the action, the target, the assessment shown at preview and the outcome, and does not contain the message text

### AC: coverage-gates-hold

**Requirements:** cockpit-actions#req:new-code-is-fully-covered

Scenario: The registry and handlers are fully covered
Given the code this Feature adds
When the Go changed-coverage check and the `cockpit/web` check run
Then both pass with every added Go statement covered and the web thresholds of 100 met

### AC: whole-journey-e2e

**Requirements:** cockpit-actions#req:actions-are-discovered, cockpit-actions#req:preview-before-run, cockpit-actions#req:destructive-actions-name-what-is-lost, cockpit-actions#req:runs-are-typed-daemon-operations

Scenario: The journey without crutches, from the command to the discard
Given a repository with a bare remote and one worktree holding two commits on a never-pushed branch, and no daemon running
When one end-to-end test runs `wb cockpit`, follows the printed URL, reads the Dashboard, opens the worktree, reads the disabled Discard, presses Push and waits for it to finish without reloading, modifies a tracked file from outside Cockpit, chooses Discard, types the branch name and confirms
Then the worktree was first under Work at Risk with `branch_never_pushed`, Discard was disabled with a reason naming two commits, the push completed in the page, the discard preview named one file to be archived, and afterwards the worktree is gone from the list, its commits are on the remote and the file's content is in the Work Log archive

## Open Questions

- What the existing abort does with ignored files, and with a never-pushed
  branch that has commits, was not established when this was written. The
  action refuses unpushed commits itself, so the second cannot arise through
  Cockpit; the first decides the preview's wording.
- Whether `pr.land` needs an approval parameter in the first cut, since the
  landing operation can require a named review.
- Whether the lifecycle runner can already run the indexer on demand for one
  checkout. The `checkout-updated` event and the runner exist; an on-demand
  trigger was not confirmed, and adding one is part of this Feature if it is
  missing.

---
*This document follows the https://specscore.md/feature-specification*
