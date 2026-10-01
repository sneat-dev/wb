---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Work-loss risk

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-loss-risk?op=request-change) |
**Status:** Draft
**Source Ideas:** wb-cockpit

## Summary

WB computes, for every worktree, branch and canonical clone it knows about,
how durable the work in it is — from changes that exist only in one working
tree to work merged into the default branch — and names the reasons any of it
could be lost. Cockpit leads its Dashboard with that list, and every
destructive Cockpit action consults a fresh assessment before it runs. This
is the "no work gets silently left behind" promise of
[WB Cockpit](../../ideas/wb-cockpit.md).

## Problem

Agents leave work in many worktrees on several machines, and the facts that
say whether that work is safe are scattered. Dirty and untracked files come
from one scan, unpushed commits and upstream state from another, pull request
and landing evidence from a third, and nothing combines them. A clean
worktree looks safe when its branch was never pushed. A worktree with an open
pull request looks safe when it also holds uncommitted edits. A machine that
stopped publishing looks empty rather than stranded.

Two things make a naive answer dangerous. Local Git knows a remote only as of
the last fetch, so a branch that was deleted or force-pushed since still
looks pushed. And another machine's snapshot lists only what was wrong when
it was published, so silence in a snapshot is not evidence of safety.

## Journey

1. **Start.** An agent on my laptop commits to a new branch and stops without
   pushing. I open Cockpit.
   **Observable good result:** the Dashboard's Work at Risk list shows that
   worktree first, says the work exists only on this machine, and names the
   reason: local commits, branch never pushed.
2. **Middle.** I push the branch from a terminal and do nothing else.
   **Observable good result:** after the next refresh the worktree has left
   Work at Risk and reads "on the remote as of the last fetch", with no
   action from me in Cockpit.
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

When a fact cannot be determined, the level MUST be `unknown` or the lower of
the candidates. WB MUST NOT report a more durable level than it has evidence
for. In particular:

- a repository with no remote, or one that was never fetched, is `unknown`
  with reason `no_remote_evidence`, never "nothing unpushed";
- `merged` requires evidence — integration at the remote, or a merged pull
  request. A squash merge without pull request evidence stays at
  `remote_branch` or lower;
- a Git command that fails yields `unknown`.

#### REQ: snapshot-levels-are-as-of-the-last-fetch

An assessment in the read model judges the remote from local remote-tracking
refs, so `remote_branch`, `pull_request` and `merged` mean "as of the last
fetch". Each assessment carries `remote_verified: false`, and the application
MUST word those levels as of the last fetch, not as a present fact.

### Risk

#### REQ: risk-reasons

An assessment lists every reason that applies, from this closed set:

- `uncommitted_changes` — tracked files modified or staged;
- `untracked_files` — files Git does not track and does not ignore;
- `unpushed_commits` — commits ahead of the upstream branch;
- `branch_never_pushed` — the branch has no remote counterpart;
- `detached_head` — `HEAD` is detached and holds commits no branch or remote
  ref reaches;
- `upstream_gone` — an upstream is configured and its remote ref no longer
  exists;
- `diverged` — the local and remote branches each have commits the other
  lacks;
- `stash` — the repository holds stash entries, which exist on one machine
  only;
- `no_remote_evidence` — the repository has no remote or was never fetched;
- `owner_stopped` — the worktree's owner is no longer live and the worktree
  is otherwise at risk;
- `machine_unreachable` — the assessment comes from another machine's
  snapshot older than the staleness threshold.

#### REQ: at-risk-definition

A worktree, branch or canonical clone is **at risk** when its level is
`local_changes`, `local_commits` or `unknown`, or when its reasons include
`stash`, `diverged`, `upstream_gone` or `detached_head` whatever its level.

#### REQ: cleanup-candidates-are-separate

A worktree or branch whose level is `merged`, which still exists, which has
no live owner, and which is not at risk is a **cleanup candidate**. The
attention list reports the two classes separately. A worktree with no commits
of its own and a live owner is neither.

#### REQ: canonical-clones-are-assessed

The assessment covers each repository's canonical clone as well as its
worktrees: uncommitted and untracked content in the canonical clone, local
branches with unpushed commits that have no worktree, and stash entries are
reported against the repository.

### Other machines

#### REQ: remote-assessment-from-snapshots

For a machine other than this one, the assessment is derived from that
machine's published snapshot and is marked `cached` with the snapshot's
publish time.

- A repository the snapshot lists as non-clean is assessed from the fields it
  carries.
- A worktree in a snapshot carries no Git state, so its level is `unknown`.
- A snapshot that carries no Git state at all — as the hub provider's do not
  — yields `unknown` for everything on that machine.

Absence from a snapshot is never treated as evidence of safety.

### Surfacing

#### REQ: assessment-in-the-read-model

Each worktree, branch and repository in the Cockpit fleet read model carries
its `durability`, its `risks` and `remote_verified`. They are metadata:
reason codes and counts only — no file names, no commit subjects.

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

A caller that is about to change state MUST be able to request a fresh
assessment of one named worktree or branch, computed from Git at the time of
the request. A snapshot assessment never authorizes a mutation.

#### REQ: fresh-assessment-verifies-the-remote

A fresh assessment asks the remote itself for the branch's current head
rather than trusting the remote-tracking ref. When the remote confirms that
the local head is reachable from the remote branch, the assessment carries
`remote_verified: true`. When the remote branch is gone or has moved so that
it no longer contains the local head, the level drops to `local_commits`.
When the remote cannot be reached, the level is `unknown`.

A fresh assessment also counts ignored files in the working tree. They are
not a risk reason, but a caller that is about to delete the tree needs the
count.

### Test coverage

#### REQ: new-code-is-fully-covered

All code this Feature adds MUST reach 100% test coverage, in Go and in
`cockpit/web`, under the gates defined by
[cockpit](../cockpit/README.md)#req:new-code-is-fully-covered.

## Dependencies

- [cockpit](../cockpit/README.md)
- [remote-state](../remote-state/README.md)
- [branch-hygiene](../branch-hygiene/README.md)
- [worktree-lifecycle](../worktree-lifecycle/README.md)

## Not Doing

- Deciding or performing cleanup — `cleanup-orchestration` and
  `branch-hygiene` own eligibility; this Feature only reports.
- Widening any snapshot schema to carry Git state for worktrees — so every
  cached worktree reads `unknown` until a later Feature does.
- Commits inside submodules — a repository with submodules is assessed for
  its own commits only.
- Repositories with several remotes — the assessment uses the remote WB
  already treats as the origin.
- Retrospective risk history and trends — a later slice.
- A `wb` CLI verb for the assessment — the read model is the first consumer.

## Acceptance Criteria

### AC: levels-follow-git-state

**Requirements:** work-loss-risk#req:durability-levels, work-loss-risk#req:risk-reasons, work-loss-risk#req:snapshot-levels-are-as-of-the-last-fetch

Scenario: One worktree walked through every level
Given a worktree on a new branch with one untracked file
When the file is committed, then the branch pushed, then a pull request recorded as open, then the work recorded as merged at the remote, assessing after each step
Then the levels are `local_changes` with `untracked_files`, `local_commits` with `branch_never_pushed`, `remote_branch`, `pull_request` and `merged`, in that order, and the last three carry `remote_verified: false`

### AC: dirty-worktree-with-open-pr-is-at-risk

**Requirements:** work-loss-risk#req:least-durable-content-wins

Scenario: Edits on top of a pushed branch
Given a worktree whose branch is pushed and has an open pull request
When a tracked file is modified, and separately when one further commit is made without pushing
Then the first assessment is `local_changes` with `uncommitted_changes`, and the second is `local_commits` with `unpushed_commits`

### AC: missing-evidence-yields-unknown

**Requirements:** work-loss-risk#req:unknown-is-never-safe

Scenario: Four kinds of missing evidence
Given a repository with no remote, a clone that was never fetched, a worktree whose tracking state cannot be read, and a branch that was squash-merged upstream with no pull request evidence available
When each is assessed
Then the first two are `unknown` with `no_remote_evidence`, the third is `unknown`, the fourth is not `merged`, and all four that are `unknown` are at risk

### AC: upstream-gone-diverged-stash-and-detached

**Requirements:** work-loss-risk#req:risk-reasons, work-loss-risk#req:at-risk-definition

Scenario: Reasons that make a pushed branch at risk
Given a branch whose upstream ref was deleted, a branch that has diverged from its remote, a repository holding one stash entry whose branches are all pushed, and a worktree with a detached `HEAD` holding one commit no ref reaches
When they are assessed
Then the reasons are `upstream_gone`, `diverged`, `stash` and `detached_head` respectively, and all four are at risk

### AC: canonical-clone-is-assessed

**Requirements:** work-loss-risk#req:canonical-clones-are-assessed

Scenario: Work left in the canonical clone
Given a canonical clone with one modified tracked file, one untracked file, and an unpushed local branch that has no worktree
When the repository is assessed
Then it is at risk with `uncommitted_changes`, `untracked_files` and `unpushed_commits`

### AC: stopped-owner-is-flagged

**Requirements:** work-loss-risk#req:risk-reasons

Scenario: The agent is gone
Given a worktree at `local_commits` whose owner is no longer live, and one at `remote_branch` whose owner is also gone
When they are assessed
Then only the first carries `owner_stopped`

### AC: merged-is-cleanup-not-risk

**Requirements:** work-loss-risk#req:cleanup-candidates-are-separate

Scenario: Landed and left behind, and newly created
Given a clean worktree with no live owner whose branch is integrated at the remote, and a worktree created a moment ago with no commits of its own and a live owner
When the attention list is requested
Then the first appears among cleanup candidates and not among at-risk entries, and the second appears in neither

### AC: remote-snapshots-are-cached-or-unknown

**Requirements:** work-loss-risk#req:remote-assessment-from-snapshots, work-loss-risk#req:risk-reasons

Scenario: Two other machines
Given a snapshot from one machine, published before the staleness threshold, that lists one repository with unpushed commits and one worktree, and a hub snapshot from another machine with no Git state
When the attention list is requested
Then the first machine's repository is at risk, marked `cached` with the publish time and `machine_unreachable`, its worktree is `unknown`, and every worktree of the second machine is `unknown`

### AC: read-model-carries-codes-only

**Requirements:** work-loss-risk#req:assessment-in-the-read-model

Scenario: Anonymous reader
Given a worktree with an untracked file named `secret-plan.txt` and an unpushed commit
When the fleet read model is requested with no owner session
Then the worktree shows its level, its reason codes and counts, and the response contains neither the file name nor the commit subject

### AC: attention-list-order-and-empty-state

**Requirements:** work-loss-risk#req:attention-list, work-loss-risk#req:risk-counts-drill-down, work-loss-risk#req:snapshot-levels-are-as-of-the-last-fetch

Scenario: Ordering, drill-down, wording and nothing to show
Given three at-risk worktrees — one `local_commits` idle for a day, one `local_changes` idle for an hour, one `local_changes` idle for a week — and one worktree at `remote_branch`
When the Dashboard is opened, its at-risk count is hovered and clicked, the pushed worktree's row is read, and then all three are made safe and the Dashboard reloaded
Then Work at Risk is the first section and lists the week-old `local_changes` worktree, then the hour-old one, then the `local_commits` one; the hover card names all three and the click opens the Worktrees page filtered to them; the pushed worktree's level is worded as of the last fetch; and afterwards the section states that no work is at risk

### AC: fresh-assessment-sees-new-edits

**Requirements:** work-loss-risk#req:fresh-assessment-on-demand

Scenario: The snapshot is behind
Given a worktree the snapshot records as `remote_branch`
When a tracked file is modified and a fresh assessment of that worktree is requested before the snapshot refreshes
Then the fresh assessment is `local_changes` while the read model still shows `remote_branch`

### AC: fresh-assessment-catches-a-vanished-remote-branch

**Requirements:** work-loss-risk#req:fresh-assessment-verifies-the-remote

Scenario: Deleted, force-pushed, unreachable and intact
Given four pushed worktrees whose remote-tracking refs all still point at their local heads — one whose remote branch has since been deleted, one whose remote branch was force-pushed to an unrelated commit, one whose remote cannot be reached, and one untouched — and three ignored files in the last
When a fresh assessment of each is requested
Then the first two are `local_commits`, the third is `unknown`, the fourth is `remote_branch` with `remote_verified: true` and an ignored-file count of three, while the read model still shows all four as `remote_branch`

### AC: coverage-gates-hold

**Requirements:** work-loss-risk#req:new-code-is-fully-covered

Scenario: The assessment code is fully covered
Given the code this Feature adds
When the Go changed-coverage check and the `cockpit/web` check run
Then both pass with every added Go statement covered and the web thresholds of 100 met

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
- Asking the remote costs a network call per fresh assessment. It is made
  only for one target at a time, before a mutation, never for the read model.

---
*This document follows the https://specscore.md/feature-specification*
