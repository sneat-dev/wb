---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Work-loss risk

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=request-change) |
**Status:** Draft
**Source Ideas:** wb-cockpit

## Summary

WB computes, for every worktree and branch it knows about, how durable the
work in it is — from changes that exist only in one working tree to work
merged into the default branch — and names the reasons any of it could be
lost. Cockpit leads its Dashboard with that list, and every destructive
Cockpit action consults the same assessment before it runs. This is the
"no work gets silently left behind" promise of
[WB Cockpit](../../ideas/wb-cockpit.md).

## Problem

Agents leave work in many worktrees on several machines, and the facts that
say whether that work is safe are scattered. Dirty and untracked files come
from one scan, unpushed commits and upstream state from another, pull request
and landing evidence from a third, and nothing combines them. A clean
worktree looks safe when its branch was never pushed. A worktree with an open
pull request looks safe when it also holds uncommitted edits. A machine that
stopped publishing looks empty rather than stranded.

## Journey

1. **Start.** An agent on my laptop commits to a new branch and stops without
   pushing. I open Cockpit.
   **Observable good result:** the Dashboard's Work at Risk list shows that
   worktree first, says the work exists only on this machine, and names the
   reason: local commits, branch never pushed.
2. **Middle.** I push the branch from a terminal and do nothing else.
   **Observable good result:** after the next refresh the worktree has left
   Work at Risk and reads "recoverable from remote", with no action from me in
   Cockpit.
3. **End.** I edit one file in that worktree and leave it unsaved to Git.
   **Observable good result:** the worktree is back in Work at Risk as
   uncommitted changes, even though its branch is on the remote.

## Behavior

### Durability

#### REQ: durability-levels

Every worktree and every local branch has exactly one durability level:

| Level | Meaning |
|---|---|
| `local_changes` | Uncommitted or untracked content exists; it is in one working tree only. |
| `local_commits` | Everything is committed, but at least one commit is on no remote. |
| `remote_branch` | Every commit is reachable from a remote branch. |
| `pull_request` | As `remote_branch`, and an open pull request carries the branch. |
| `merged` | The work is integrated into the default branch at the remote. |
| `unknown` | WB could not determine one of the facts above. |

#### REQ: least-durable-content-wins

A worktree's level is the level of its least durable content. Uncommitted
edits make it `local_changes` whatever its branch's remote or pull request
state. Local commits beyond the remote branch make it `local_commits` even
when an open pull request exists for earlier commits.

#### REQ: unknown-is-never-safe

When a fact cannot be determined — a Git command fails, the remote was never
fetched, pull request evidence is unavailable and needed to tell
`pull_request` from `merged` — the level MUST be `unknown` or the lower of
the candidates. WB MUST NOT report a more durable level than it has evidence
for.

### Risk

#### REQ: risk-reasons

An assessment lists every reason that applies, from this closed set:

- `uncommitted_changes` — tracked files modified or staged;
- `untracked_files` — files Git does not track and does not ignore;
- `unpushed_commits` — commits ahead of the upstream branch;
- `branch_never_pushed` — the branch has no remote counterpart;
- `upstream_gone` — an upstream is configured and its remote ref no longer
  exists;
- `diverged` — the local and remote branches each have commits the other
  lacks;
- `stash` — the repository holds stash entries, which exist on one machine
  only;
- `owner_stopped` — the worktree's owner is no longer live and its level is
  `local_changes` or `local_commits`;
- `machine_unreachable` — the assessment comes from another machine's
  snapshot older than the staleness threshold and its level is
  `local_changes`, `local_commits` or `unknown`.

A worktree is **at risk** when its level is `local_changes`, `local_commits`
or `unknown`.

#### REQ: cleanup-candidates-are-separate

A worktree or branch whose level is `merged` and which still exists is a
**cleanup candidate**, not a risk. The attention list reports the two classes
separately, and a cleanup candidate is never shown as at risk.

#### REQ: canonical-clones-are-assessed

The assessment covers each repository's canonical clone as well as its
worktrees: local branches with unpushed commits that have no worktree, and
stash entries, are reported against the repository.

### Other machines

#### REQ: remote-assessment-from-snapshots

For a machine other than this one, the assessment is derived from that
machine's published snapshot and is marked `cached` with the snapshot's
publish time. A snapshot that carries no Git state for a repository — as the
hub provider's snapshots do not — yields `unknown`, never a safe level.

### Surfacing

#### REQ: assessment-in-the-read-model

Each worktree and branch in the Cockpit fleet read model carries its
`durability` and its `risks`. Both are metadata. They contain reason codes
and counts only — no file names, no commit messages.

#### REQ: attention-list

`GET /api/v1/cockpit/attention` returns the at-risk entries and the cleanup
candidates. At-risk entries are ordered least durable first and, within a
level, oldest last activity first. The Cockpit Dashboard MUST show Work at
Risk above every other section, and MUST say so plainly when the list is
empty rather than show nothing.

#### REQ: risk-counts-drill-down

Every risk count on a repository, a machine or the Dashboard follows the
hover and drill-down behavior of
[cockpit](../cockpit/README.md)#req:summary-hover-drill-down.

### Fresh assessment for mutations

#### REQ: fresh-assessment-on-demand

The read model serves assessments from the daemon's background snapshot. A
caller that is about to change state MUST be able to request a fresh
assessment of one named worktree or branch, computed from Git at the time of
the request. A snapshot assessment never authorizes a mutation.

### Test coverage

#### REQ: new-code-is-fully-covered

All code this Feature adds MUST reach 100% statement coverage, in Go and in
`cockpit/web`, under the same gates as
[cockpit](../cockpit/README.md)#req:new-code-is-fully-covered.

## Dependencies

- [cockpit](../cockpit/README.md)
- [remote-state](../remote-state/README.md)
- [branch-hygiene](../branch-hygiene/README.md)
- [worktree-lifecycle](../worktree-lifecycle/README.md)

## Not Doing

- Deciding or performing cleanup — `cleanup-orchestration` and
  `branch-hygiene` own eligibility; this Feature only reports.
- Widening the hosted snapshot schema to carry Git state — its privacy
  allowlist is unchanged, so hub-only machines read `unknown`.
- Retrospective risk history and trends — a later slice.
- A `wb` CLI verb for the assessment — the read model is the first consumer.

## Acceptance Criteria

### AC: levels-follow-git-state

**Requirements:** work-loss-risk#req:durability-levels, work-loss-risk#req:risk-reasons

Scenario: One worktree walked through every level
Given a worktree on a new branch with one untracked file
When the file is committed, then the branch pushed, then a pull request recorded as open, then the work recorded as merged at the remote, assessing after each step
Then the levels are `local_changes` with `untracked_files`, `local_commits` with `branch_never_pushed`, `remote_branch`, `pull_request` and `merged`, in that order

### AC: dirty-worktree-with-open-pr-is-at-risk

**Requirements:** work-loss-risk#req:least-durable-content-wins

Scenario: Edits on top of a pushed branch
Given a worktree whose branch is pushed and has an open pull request
When a tracked file is modified, and separately when one further commit is made without pushing
Then the first assessment is `local_changes` with `uncommitted_changes`, and the second is `local_commits` with `unpushed_commits`

### AC: failure-yields-unknown

**Requirements:** work-loss-risk#req:unknown-is-never-safe

Scenario: Evidence is missing
Given a worktree whose remote-tracking state cannot be read, and another whose branch is on the remote while pull request evidence is unavailable
When both are assessed
Then the first is `unknown` and at risk, and the second is `remote_branch`, not `pull_request` or `merged`

### AC: upstream-gone-diverged-and-stash

**Requirements:** work-loss-risk#req:risk-reasons, work-loss-risk#req:canonical-clones-are-assessed

Scenario: The remaining reasons
Given a branch whose upstream ref was deleted, a branch that has diverged from its remote, and a canonical clone holding one stash entry and an unpushed local branch with no worktree
When they are assessed
Then the reasons are `upstream_gone`, `diverged`, and for the repository `stash` and `unpushed_commits`

### AC: stopped-owner-is-flagged

**Requirements:** work-loss-risk#req:risk-reasons

Scenario: The agent is gone
Given a worktree at `local_commits` whose owner is no longer live, and one at `remote_branch` whose owner is also gone
When they are assessed
Then only the first carries `owner_stopped`

### AC: merged-is-cleanup-not-risk

**Requirements:** work-loss-risk#req:cleanup-candidates-are-separate

Scenario: Landed and left behind
Given a clean worktree whose branch is integrated at the remote
When the attention list is requested
Then the worktree appears among cleanup candidates and not among at-risk entries

### AC: remote-snapshots-are-cached-or-unknown

**Requirements:** work-loss-risk#req:remote-assessment-from-snapshots, work-loss-risk#req:risk-reasons

Scenario: Two other machines
Given a snapshot from one machine that reports unpushed commits and was published before the staleness threshold, and a hub snapshot from another machine with no Git state
When the attention list is requested
Then the first machine's repository is at risk, marked `cached` with the publish time and `machine_unreachable`, and the second machine's worktrees are `unknown`

### AC: read-model-carries-codes-only

**Requirements:** work-loss-risk#req:assessment-in-the-read-model

Scenario: Anonymous reader
Given a worktree with an untracked file named `secret-plan.txt` and an unpushed commit
When the fleet read model is requested with no owner session
Then the worktree shows its level, its reason codes and counts, and the response contains neither the file name nor the commit subject

### AC: attention-list-order-and-empty-state

**Requirements:** work-loss-risk#req:attention-list, work-loss-risk#req:risk-counts-drill-down

Scenario: Ordering, drill-down and nothing to show
Given three at-risk worktrees — one `local_commits` idle for a day, one `local_changes` idle for an hour, one `local_changes` idle for a week
When the Dashboard is opened, its at-risk count is hovered and clicked, and then all three are made safe and the Dashboard reloaded
Then Work at Risk is the first section and lists the week-old `local_changes` worktree, then the hour-old one, then the `local_commits` one; the hover card names all three and the click opens the Worktrees page filtered to them; and afterwards the section states that no work is at risk

### AC: fresh-assessment-sees-new-edits

**Requirements:** work-loss-risk#req:fresh-assessment-on-demand

Scenario: The snapshot is behind
Given a worktree the snapshot records as `remote_branch`
When a tracked file is modified and a fresh assessment of that worktree is requested before the snapshot refreshes
Then the fresh assessment is `local_changes` while the read model still shows `remote_branch`

### AC: coverage-gates-hold

**Requirements:** work-loss-risk#req:new-code-is-fully-covered

Scenario: The assessment package is fully covered
Given the code this Feature adds
When the Go changed-coverage check and the `cockpit/web` test run execute
Then both report 100% statement coverage of the added code

### AC: whole-journey-e2e

**Requirements:** work-loss-risk#req:attention-list, work-loss-risk#req:least-durable-content-wins

Scenario: The journey without crutches
Given a repository with a bare remote and one worktree on a new branch with one unpushed commit
When one end-to-end test opens the Dashboard, pushes the branch from outside Cockpit, waits for a refresh, then modifies a tracked file and waits for a refresh
Then the worktree is first listed at risk with `branch_never_pushed`, then absent from Work at Risk, then listed again with `uncommitted_changes`

## Open Questions

- The staleness threshold for `machine_unreachable`: `wb remote` already
  treats a snapshot older than 24 hours as stale. Reusing that value is the
  intent; it is confirmed at implementation.

---
*This document follows the https://specscore.md/feature-specification*
