---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Cockpit actions

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit-actions?op=request-change) |
**Status:** Draft
**Source Ideas:** wb-cockpit

## Summary

Cockpit turns from a view into an operational surface through a registry of
typed actions. Each action declares what it applies to, what it requires and
how dangerous it is; Cockpit discovers the actions that apply to a selected
entity and runs them through the daemon. The first actions are commit, push,
create a pull request, land a pull request, discard a worktree and delete a
branch. This is the "select, inspect, act" loop of
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

1. **Start.** In Cockpit I open a worktree that is at risk because its branch
   was never pushed.
   **Observable good result:** the page offers Push and Commit as buttons and
   Discard under a menu, and each unavailable action says why it is
   unavailable.
2. **Middle.** I choose Discard.
   **Observable good result:** before anything happens Cockpit shows a fresh
   assessment — two commits exist only here, no other copy was found — and
   the confirm control is disabled until I type the branch name.
3. **End.** I cancel, press Push, and when it finishes choose Discard again.
   **Observable good result:** the push shows its progress and result without
   a page reload; the second Discard preview says the work is recoverable
   from the remote and confirms with one click; the worktree then disappears
   from the list.

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
the capability. The application MUST build its action controls from this
response and MUST NOT hardcode which actions exist. An action the caller
cannot run is shown disabled with the reason.

#### REQ: targets-are-resolved-by-the-daemon

A target identifier names an entity in the daemon's current inventory. The
daemon resolves it to a path and a ref itself. No request field carries a
filesystem path, a command, or an argument vector, and a target that is not
in the inventory is refused.

### Authorization and safety

#### REQ: capability-then-safety

Running an action requires the capability it declares, under
[cockpit](../cockpit/README.md)#req:owner-routes. The capability check comes
first and a caller without it is refused with status 403 whatever the
action's safety class. Safety confirmation is a separate, later step and
never substitutes for the capability.

This Feature adds the capabilities `git.commit`, `branch.push`,
`branch.delete`, `worktree.discard`, `pr.create` and `pr.land`. The `owner`
principal holds them; `anonymous-local` does not.

#### REQ: preview-before-run

`POST /api/v1/cockpit/actions/{id}/preview` computes, for the target and
parameters, what the action will do and returns a preview token. For a
`guarded` or `destructive` action the preview carries a fresh assessment from
[work-loss-risk](../work-loss-risk/README.md)#req:fresh-assessment-on-demand.
The token is bound to the target's observed state — its head commit and
whether its working tree was clean.

`POST /api/v1/cockpit/actions/{id}/run` requires an idempotency key and, for
a `guarded` or `destructive` action, a preview token. The daemon re-reads the
target's state and refuses the run when it differs from the state the token
was bound to. A `safe` action runs without a preview.

#### REQ: destructive-actions-name-what-is-lost

When the fresh assessment of a `destructive` action's target is at risk, the
preview MUST state how many commits and whether uncommitted or untracked
content exist only in that target, and MUST say when WB could find no other
copy. The application MUST then require the operator to type the branch or
task name before the run control is enabled. When the target is not at risk,
one confirmation suffices.

#### REQ: wrapped-refusals-are-preserved

An action that performs an existing WB operation MUST NOT weaken it. Every
refusal the corresponding `wb` verb would raise — a landing lane held by
another session, a failing check, a missing approval, an active claim — is
raised by the action and shown to the operator with the verb's own message.
Git hooks run exactly as they do for the verb and are never bypassed.

### Execution

#### REQ: runs-are-daemon-operations

A run is admitted as an operation in the daemon's existing operation queue
and returns the operation's identifier at once. The application follows it
through `GET /api/v1/cockpit/operations/{id}`, which reports queued, running
or a terminal state, and the outcome with the verb's summary lines. A second
run with the same idempotency key returns the first run's operation and
starts nothing.

#### REQ: runs-are-recorded

Every run is recorded with the principal, the action identifier, the target,
the parameters other than free text, the assessment shown at preview, and
the outcome.

### First actions

#### REQ: commit-action

`worktree.commit` applies to a worktree with uncommitted or untracked
content. It takes a required message and a scope of all changes or staged
changes only. It is `guarded` and requires `git.commit`.

#### REQ: push-action

`branch.push` applies to a worktree or local branch with commits its remote
lacks, and sets the upstream when the branch was never pushed. It is not
applicable to a diverged branch, and says so. It is `guarded` and requires
`branch.push`.

#### REQ: create-pr-action

`pr.create` applies to a worktree whose branch has no open pull request. It
takes a title and an optional body, and commits and pushes first when the
worktree needs it, as the existing pull-request creation operation does. It
is `guarded` and requires `pr.create`.

#### REQ: land-action

`pr.land` applies to an open pull request, or to a worktree that has one. It
performs WB's existing landing operation, including its check waiting,
remote receipt and cleanup. It is `guarded` and requires `pr.land`.

#### REQ: discard-worktree-action

`worktree.discard` applies to any WB worktree that no live owner holds. It
removes the worktree and records it as discarded. It is `destructive` and
requires `worktree.discard`.

#### REQ: delete-branch-action

`branch.delete` applies to a local or remote branch with no worktree. It is
`destructive` and requires `branch.delete`.

### Presentation

#### REQ: common-actions-are-direct

On an entity's page, applicable `safe` and `guarded` actions are buttons and
`destructive` actions sit under an overflow menu. No action control appears
inside a hover card.

### Test coverage

#### REQ: new-code-is-fully-covered

All code this Feature adds MUST reach 100% statement coverage, in Go and in
`cockpit/web`, under the same gates as
[cockpit](../cockpit/README.md)#req:new-code-is-fully-covered.

## Dependencies

- [cockpit](../cockpit/README.md)
- [work-loss-risk](../work-loss-risk/README.md)
- [mechanical-worktree-merge](../mechanical-worktree-merge/README.md)
- [branch-hygiene](../branch-hygiene/README.md)
- [worktree-lifecycle](../worktree-lifecycle/README.md)
- [operations-journal](../operations-journal/README.md)

## Not Doing

- Agent actions — dispatch, steer, stop, cancel, kill and resume wait for the
  herdr dispatch Feature.
- Instructing an agent with selected context — the same Feature.
- A command palette — the registry makes one possible; it is not built here.
- Multi-selection and bulk actions.
- Actions on another machine — every target here is on this daemon's
  machine.

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

### AC: unknown-target-and-path-are-refused

**Requirements:** cockpit-actions#req:targets-are-resolved-by-the-daemon

Scenario: A forged target
Given an owner session
When a run names a worktree identifier that is not in the inventory, and another request adds a filesystem path field
Then the first is refused with status 404, the second is refused with status 400, and no Git command ran

### AC: anonymous-cannot-run

**Requirements:** cockpit-actions#req:capability-then-safety

Scenario: No session, and the hosted origin
Given a loopback request with no session cookie, and a request from the hosted origin
When each attempts to preview and to run `branch.push`
Then every attempt is refused, nothing is pushed, and the action list each caller can read marks the action as needing an owner session

### AC: stale-preview-is-refused

**Requirements:** cockpit-actions#req:preview-before-run

Scenario: The worktree changed after the preview
Given a preview token for `worktree.discard` on a clean worktree
When a file in that worktree is modified and the run is then requested with the token
Then the run is refused as stale, the worktree still exists, and a new preview reports uncommitted changes

### AC: guarded-action-needs-a-token

**Requirements:** cockpit-actions#req:preview-before-run

Scenario: Skipping the preview
Given an owner session
When `worktree.discard` is run with an idempotency key and no preview token
Then it is refused and the worktree still exists

### AC: at-risk-discard-demands-the-name

**Requirements:** cockpit-actions#req:destructive-actions-name-what-is-lost, cockpit-actions#req:discard-worktree-action

Scenario: Two commits exist only here
Given a worktree with two unpushed commits on a branch that was never pushed
When the operator chooses Discard
Then the preview states that two commits exist only in this worktree and that no other copy was found, the run control is disabled until the branch name is typed, and after it is typed and confirmed the worktree is gone and recorded as discarded

### AC: safe-discard-confirms-once

**Requirements:** cockpit-actions#req:destructive-actions-name-what-is-lost

Scenario: Everything is on the remote
Given a clean worktree whose branch is fully pushed
When the operator chooses Discard
Then the preview says the work is recoverable from the remote and one confirmation runs it

### AC: verb-refusals-surface

**Requirements:** cockpit-actions#req:wrapped-refusals-are-preserved, cockpit-actions#req:land-action

Scenario: Landing while a check fails, and committing against a failing hook
Given a pull request with a failing required check, and a worktree whose pre-commit hook rejects the change
When `pr.land` and `worktree.commit` are run
Then both operations end failed with the verb's own refusal text, the pull request is not merged, no commit is created, and no hook was skipped

### AC: land-performs-the-full-landing

**Requirements:** cockpit-actions#req:land-action

Scenario: A green pull request
Given a worktree with an open pull request whose checks pass against a fake forge
When `pr.land` is run
Then the pull request is merged, the operation's outcome carries the landing receipt, and the worktree has been cleaned up as the landing verb does

### AC: commit-push-and-create-pr

**Requirements:** cockpit-actions#req:commit-action, cockpit-actions#req:push-action, cockpit-actions#req:create-pr-action

Scenario: From dirty to pull request
Given a worktree with one modified file on a never-pushed branch, and a fake forge
When `worktree.commit` is run with a message, then `branch.push`, then `pr.create` with a title
Then a commit with that message exists, the branch exists on the remote with its upstream set, and one pull request is open for it

### AC: push-refuses-diverged

**Requirements:** cockpit-actions#req:push-action

Scenario: Both sides moved
Given a branch that has diverged from its remote
When the actions for it are requested
Then `branch.push` is not applicable and its reason says the branch has diverged

### AC: delete-branch

**Requirements:** cockpit-actions#req:delete-branch-action

Scenario: A leftover branch, and one in use
Given a merged local branch with no worktree, and a branch checked out in a worktree
When the actions for each are requested and `branch.delete` is previewed and run on the first
Then the first branch is deleted, and for the second `branch.delete` is not applicable with a reason naming the worktree

### AC: operation-is-followed-and-idempotent

**Requirements:** cockpit-actions#req:runs-are-daemon-operations

Scenario: A double click
Given a `branch.push` run that has been admitted
When the same run is requested again with the same idempotency key and the operation is then polled to completion
Then both requests return the same operation identifier, the branch was pushed once, and the final state is succeeded with the verb's summary

### AC: run-is-recorded

**Requirements:** cockpit-actions#req:runs-are-recorded

Scenario: After a discard
Given a completed `worktree.discard` run
When the daemon's record of it is read
Then it names the owner principal, the action, the target, the assessment shown at preview and the outcome, and contains no commit message text

### AC: coverage-gates-hold

**Requirements:** cockpit-actions#req:new-code-is-fully-covered

Scenario: The registry and handlers are fully covered
Given the code this Feature adds
When the Go changed-coverage check and the `cockpit/web` test run execute
Then both report 100% statement coverage of the added code

### AC: whole-journey-e2e

**Requirements:** cockpit-actions#req:actions-are-discovered, cockpit-actions#req:preview-before-run, cockpit-actions#req:destructive-actions-name-what-is-lost, cockpit-actions#req:runs-are-daemon-operations

Scenario: The journey without crutches
Given a repository with a bare remote and one worktree holding two unpushed commits on a never-pushed branch
When one end-to-end test opens the worktree in Cockpit, chooses Discard, cancels at the name prompt, presses Push and waits for it to finish without reloading, then chooses Discard again and confirms
Then the first preview demanded the branch name, the push completed in the page, the second preview said the work is recoverable and took one confirmation, and the worktree is gone from the list while its commits are on the remote

## Open Questions

- Whether an action handler calls the existing library function directly or
  submits the equivalent `wb` invocation to the operation queue. Admission,
  host-load and landing-lane wiring lives in the command layer today, which
  favors the second; either way the browser supplies no argument vector.
- Whether `pr.land` needs an approval parameter in the first cut, since the
  landing verb can require a named review.

---
*This document follows the https://specscore.md/feature-specification*
