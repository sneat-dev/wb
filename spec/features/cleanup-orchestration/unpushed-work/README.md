---
format: https://specscore.md/feature-specification
status: In Review
---

# Feature: Unpushed Work Detection

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/unpushed-work?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/unpushed-work?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/unpushed-work?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/unpushed-work?op=request-change) |
**Status:** In Review
**Source Ideas:** —

## Summary

`wb unpushed` answers one question fast and locally: **what would a disk
failure destroy?** It reports every commit, stash entry, and working-tree
change that exists on this machine and on no remote, ranked by how easily an
ordinary command destroys it, with the exact command that would make each one
safe. It never mutates anything and has no `--apply`.

It is a sibling of [`wb audit`](../fleet-audit/README.md) rather than a mode of
it, because it answers a different kind of question. `wb audit` asks *what can
be retired*; `wb unpushed` asks *what must not be lost*. Burying the second
inside the first is how the second stops being read.

## Problem

### The finding that commissioned this feature

On 2026-08-19 a hand-rolled sweep of the fleet found, in `specscore-cli`, an
unpushed branch carrying **144 lines** of cross-platform atomic-rename
implementation across seven new files, on a branch with no upstream and no
remote counterpart of that name. One disk failure would have destroyed it
permanently, and nothing in WB would have said so first. It was found by
accident, in the course of doing something else.

A re-measurement of the same fleet on the same day found **279 local branches
whose commits are reachable from no remote ref of their repository** —
established by `git branch -r --contains <sha>` returning nothing — plus **36
working trees holding uncommitted changes**. That is the standing data-loss
surface, and no WB command reports it.

### Why it must not live inside a general audit

Two properties make this a separate, fast command rather than a section of a
larger report:

1. **It needs no network.** Every question it asks is answered from local refs.
   A general audit that wants pull-request evidence is minutes long; this is
   seconds long, so it can be run reflexively — before a cleanup, before
   closing a laptop, in a pre-push hook.
2. **Its severity is different in kind.** Everything else in
   [Cleanup Orchestration](../README.md) is about tidiness, where the cost of
   being wrong is a leftover branch. Here the cost of being wrong is permanent
   loss of work nobody knew existed. A report that mixes the two invites the
   reader to skim.

## Interaction with Other Features

[Cleanup Orchestration](../README.md) defines the output contract, exit codes,
and `--filter` semantics this command obeys; they are not restated here.

`wb unpushed` MUST NOT be a precondition of `wb cleanup`: the orchestrator's
own refusals
([branch-hygiene#req:evidence-class-taxonomy](../../branch-hygiene/README.md)
and
[worktree-lifecycle#req:exact-remote-target-evidence](../../worktree-lifecycle/README.md))
already prevent it from deleting unpushed work, and making a report a gate
would tempt someone to add a bypass flag to the report.

The classes this feature defines are the classes
[`wb audit`](../fleet-audit/README.md) uses for its at-risk section; `wb audit`
MUST reuse them rather than define its own.

## Behavior

### Command surface

#### REQ: unpushed-command-surface

WB MUST expose `wb unpushed` as a top-level command.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--only` | string list | all classes | restrict the report to the named classes |
| `--fetch` | bool | `false` | refresh remote-tracking refs before classifying |
| `--parallel` | int | `8` | maximum repositories inspected concurrently |
| `--fail-on` | string | `at-risk` | `none`, `at-risk`, or `any`; selects the exit code |
| `--format` | string | `text` | `text`, `json`, or `ndjson` |

It MUST accept the root `--filter`, `--projects-root`, and `--non-interactive`
flags and MUST be registered for `filter` and `projects-root` in
`persistentFlagSupport` and `docs/cli-flag-matrix.md`.

The `--parallel` default of `8` is higher than `wb cleanup`'s `4` because this
command performs no network I/O and no mutation, so the only contended resource
is local disk.

#### REQ: read-only-forever

`wb unpushed` MUST be read-only in every configuration. It MUST NOT create,
move, delete, or rewrite any ref, index, working tree, worktree registration,
stash, report, or journal, and MUST NOT define an `--apply` flag or any other
mutating mode, ever. Its only permitted remote interaction is the fetch
selected by `--fetch`.

A command whose entire purpose is to be trusted about what is at risk must be
incapable of putting anything at risk.

#### REQ: no-fetch-by-default

`wb unpushed` MUST NOT contact any remote unless `--fetch` is explicit, and
MUST classify against the remote-tracking refs already present in the
repository.

Not fetching is the fail-safe choice here, and this is the reason the command
can be fast. A stale remote-tracking ref can only make WB believe a commit is
*less* published than it is, so the error direction is over-reporting risk. The
opposite default is required of `wb cleanup`
([cleanup-orchestration#req:fetch-before-every-decision](../README.md)), where
a stale ref makes WB believe work is *more* landed than it is and the error
direction is deletion.

The report MUST state, per repository, the age of the newest remote-tracking
ref it classified against, so a reader can judge how stale the evidence is.

#### REQ: reachability-is-repository-wide

A commit counts as published when it is reachable from **any** ref under
`refs/remotes/` in that repository, not only from `origin`. A branch pushed
only to a fork remote is published, and reporting it as at risk would train the
reader to ignore the report.

Reachability MUST be proved by Git's own reachability, not by branch name, not
by upstream configuration, and not by commit message. Upstream configuration is
used only to distinguish `local-only-orphaned` from
`local-only-never-published`, never to decide whether work is published.

#### REQ: covers-every-local-ref-holder

The report MUST cover every place local-only work can hide, and MUST state
which places it covered so a reader knows what a clean report does and does not
promise:

- every local branch in the canonical clone and in every linked worktree;
- the `HEAD` of every linked worktree, including a detached `HEAD`;
- an in-progress `rebase-merge`, `rebase-apply`, `CHERRY_PICK_HEAD`, or
  `MERGE_HEAD` state, whose commits may be reachable from no ref at all;
- every entry of `refs/stash`, which is repository-global and therefore
  invisible to any per-branch listing;
- the uncommitted contents of every working tree.

A place that cannot be inspected MUST be reported as `unreadable` for that
repository rather than silently omitted, because a clean report that skipped
something is worse than no report.

### Evidence classes

#### REQ: unpushed-evidence-taxonomy

Every reported row MUST carry exactly one class from this closed set, together
with the evidence that produced it and the command that resolves it:

| Class | Definition | Evidence | Remedy |
|---|---|---|---|
| `uncommitted` | tracked modifications, staged changes, or untracked non-ignored files in a working tree | `git status --porcelain`, with file count and total bytes | commit, or `git stash push -u` |
| `detached-only` | commits reachable from a worktree `HEAD` or an in-progress rebase/merge state and from no ref at all | the holder path and the unreachable commit list | `git branch <name> <sha>` |
| `local-only-orphaned` | branch commits reachable from no `refs/remotes/` ref, where `branch.<name>.remote`/`branch.<name>.merge` is configured or the branch reflog records a push | the branch SHA, the configured upstream, and the fact that it no longer resolves | `git push -u <remote> <branch>` |
| `local-only-never-published` | branch commits reachable from no `refs/remotes/` ref, with no upstream ever configured | the branch SHA and unique commit count | `git push -u origin <branch>` |
| `stash-only` | a `refs/stash` entry whose commits are reachable from no `refs/remotes/` ref | the stash index, message, and SHA | `git stash branch <name> stash@{n}` |
| `ahead-of-remote` | an upstream resolves and the branch head is ahead of it | ahead count and the upstream ref | `git push` |
| `published` | every commit is reachable from some `refs/remotes/` ref | the containing remote ref | none |
| `unreadable` | the required evidence could not be obtained | the failure | resolve the named failure |

`published` MUST NOT appear in the default text report; it MUST appear in
`--format json` and under `--only published`, so a reader can prove the command
looked at something rather than infer it from silence.

#### REQ: severity-ordered-output

Rows MUST be ordered by class severity descending, then by repository, then by
identity. The severity order is exactly the order of the classes above, and the
ordering criterion MUST be documented in the help text as: **how few and how
ordinary are the actions that destroy this permanently.**

- `uncommitted` is first because one ordinary command — `git checkout -f`,
  `git worktree remove --force`, any cleanup that ignores the dirty check —
  destroys it now, and no object exists to recover.
- `detached-only` is second because no ref protects it, so `git gc` removes it
  once the unreachable reflog expiry passes, and no ordinary listing shows it.
- `local-only-orphaned` is third because a ref protects the objects, but the
  operator believes the work is published; every "delete branches with no
  upstream" heuristic deletes exactly this class. This is the class that held
  the 144 lines of atomic-rename work in `specscore-cli`.
- `local-only-never-published` is fourth: same exposure, without the false
  belief.
- `stash-only` is fifth: protected by a ref, but invisible to every per-branch
  listing, so it is routinely forgotten.
- `ahead-of-remote` is last: real unpublished commits, but with a live remote
  counterpart and a one-word remedy.

The summary MUST lead with the total count of rows in the first five classes
under a single heading naming them as at risk, and MUST NOT fold
`ahead-of-remote` or `published` into that total.

#### REQ: uncommitted-belongs-in-this-report

`uncommitted` MUST be reported by `wb unpushed` even though it is not a commit
and the command is named for commits. A report that answers "what exists only
here" while ignoring 36 dirty working trees is a false all-clear, and a false
all-clear from a safety command is worse than no command.

The row MUST NOT reproduce file contents; it carries counts, byte size, and the
path, so the report can be pasted into an issue without leaking source.

### Cost

#### REQ: bounded-local-cost

`wb unpushed` MUST issue no network request unless `--fetch` is given, and MUST
use a constant number of Git subprocesses per repository regardless of how many
refs that repository holds. Reachability for every candidate MUST be decided by
a single batched pass — enumerating candidates with `git for-each-ref` and
deciding containment with one `git rev-list --stdin --not --remotes` — never by
one `git branch -r --contains` or `git merge-base` invocation per branch.

The hand-rolled sweep this feature replaces ran one `--contains` subprocess for
each of 1,524 branches. Cost MUST be O(repositories + refs), not
O(refs × subprocesses), because a safety check that is slow is a safety check
that is not run.

## Acceptance Criteria

### AC: every-hiding-place-is-classified

**Requirements:** unpushed-work#req:unpushed-command-surface, unpushed-work#req:read-only-forever, unpushed-work#req:covers-every-local-ref-holder, unpushed-work#req:unpushed-evidence-taxonomy, unpushed-work#req:uncommitted-belongs-in-this-report

Given a fixture repository with a real remote holding `main`, and containing a
branch fully pushed to `origin`, a branch pushed only to a second remote
`fork`, a branch with commits on no remote whose `branch.<name>.remote` is
configured to a name that no longer resolves, a branch with commits on no
remote and no upstream ever configured, a linked worktree on a detached `HEAD`
carrying a commit no ref points at, a linked worktree with an in-progress
rebase, a stash entry with unpublished commits, and a working tree with one
modified tracked file and one untracked non-ignored file, when `wb unpushed
--format json` runs, then each is reported exactly once with the classes
`published`, `published`, `local-only-orphaned`, `local-only-never-published`,
`detached-only`, `detached-only`, `stash-only`, and `uncommitted`
respectively; every row carries its evidence and its remedy command; the
`uncommitted` row carries a file count and byte size and no file contents; and
a byte-for-byte comparison of the fixture's refs, worktrees, working trees,
stash stack, and configuration before and after the run shows no change. A test
MUST assert that the command exposes no flag that mutates.

### AC: classification-is-local-fast-and-fail-safe

**Requirements:** unpushed-work#req:no-fetch-by-default, unpushed-work#req:reachability-is-repository-wide, unpushed-work#req:bounded-local-cost

Given the fixture above with its remote-tracking refs deliberately made stale —
the remote has advanced and `refs/remotes/origin/*` has not been updated — when
`wb unpushed` runs without `--fetch`, then no network request is made, and a
branch that is in fact published but not yet visible in the stale tracking ref
is reported at risk rather than published, proving the error direction is
over-reporting. When `--fetch` is added, then the same branch is reported
`published`. Across a fixture repository holding 500 branches, a subprocess
counter shows the number of Git invocations is bounded by a constant and does
not grow with the branch count, and the report states the age of the newest
remote-tracking ref used.

### AC: the-report-is-read-top-down

**Requirements:** unpushed-work#req:severity-ordered-output, unpushed-work#req:unpushed-evidence-taxonomy

Given a fixture fleet holding at least one row of every class, when
`wb unpushed` runs, then the text rows appear in exactly the severity order
`uncommitted`, `detached-only`, `local-only-orphaned`,
`local-only-never-published`, `stash-only`, `ahead-of-remote`; `published` rows
are absent from the text report but present in `--format json`; the summary's
at-risk total counts exactly the first five classes and excludes
`ahead-of-remote` and `published`; and `wb unpushed --only
local-only-orphaned` reports only that class. The exit code is `1` with the
default `--fail-on at-risk` when any of the first five classes is present, `0`
when only `ahead-of-remote` and `published` rows exist, `0` with `--fail-on
none` in both cases, and `1` with `--fail-on any` when any non-`published` row
exists.

## Open Questions

- Should `wb unpushed` be installable as a managed pre-shutdown or pre-push
  hook, so the check happens without anyone remembering it, or does that
  duplicate what a merger lane should be doing?
- Should a `local-only-orphaned` row whose upstream is gone attempt to
  distinguish "the remote branch was deleted" from "the branch was never
  pushed"? Local evidence could not separate the two for the `specscore-cli`
  finding; doing so would require a hosted lookup and would make the command
  slow.

---
*This document follows the https://specscore.md/feature-specification*
