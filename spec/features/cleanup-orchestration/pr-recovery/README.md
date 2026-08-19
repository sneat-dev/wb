---
format: https://specscore.md/feature-specification
status: In Review
---

# Feature: Pull Request Recovery Forensics

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/pr-recovery?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/pr-recovery?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/pr-recovery?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/pr-recovery?op=request-change) |
**Status:** In Review
**Source Ideas:** —

## Summary

`wb recover` is forensics for damage already done. It finds pull requests that
were closed unmerged around the time their base branch was deleted — the
signature of a stacked pull request killed by `gh pr merge --delete-branch` —
and, for each one, establishes whether its content later reached the target or
is still missing. It answers in seconds a question that currently takes hours
of reading merge commits by hand.

It is read-only and has no `--apply`. It reports; a human or a merger lane
decides.

## Problem

### Deleting a base branch closes its dependents

`gh pr merge --delete-branch` on a branch that other open pull requests are
based on closes those dependents rather than retargeting them. The founder's
notes record a verified instance: `sneat-co/chessraiders` pull request #61,
stacked on #60, on 2026-08-10.

The honest state of the evidence, established on 2026-08-19, is this: the
recorded count is **one verified instance**, the fleet's
`LESSONS-LEARNED.md` carries **no** entry for it, and an attempt to count the
real number across the fleet was abandoned as too expensive — there is no
hosted query for "pull requests closed within N minutes of their base ref being
deleted", so counting means enumerating every closed pull request in every
repository along with its timeline events.

**That is the argument for the feature, not an argument against it.** The
belief that this has happened many times is plausible and unverified precisely
because verifying it by hand is prohibitive. A hazard whose frequency nobody
can afford to measure is a hazard nobody can afford to prioritise.
`cleanup-preconditions#req:stacked-pr-preflight` stops the next occurrence;
this feature measures the ones already behind us and finds any work that never
came back.

### Establishing the fate of one orphaned pull request is slow and manual

Triage on 2026-08-19 took hours, per pull request, to establish by reading each
merge commit whether an orphaned pull request's work had been re-landed by
hand. That is exactly the shape of question a tool answers well: mechanical,
evidence-driven, and identical every time.

The one piece of good news that makes recovery possible at all is that GitHub
retains `refs/pull/<number>/head` after the head branch is deleted. Work
orphaned this way is almost always still fetchable — but only if someone knows
the ref exists and which pull request to fetch.

## Interaction with Other Features

[Cleanup Orchestration](../README.md) defines the output contract, `--filter`
semantics, exit codes, bounded hosted queries, and per-repository deadline that
`wb recover` obeys.

[Preservation and Pre-Flight](../cleanup-preconditions/README.md) is the
forward-looking half of the same hazard: its pre-flight prevents new
occurrences, this feature investigates old ones. Neither replaces the other.

The re-landing proof `wb recover` uses MUST be the same proof
[worktree-lifecycle#req:absorbed-integration-containment-evidence](../../worktree-lifecycle/README.md)
already defines — a landing receipt from GitHub's commit-to-pull-request index
plus a local three-way merge proof — and MUST NOT introduce a weaker one.
`git cherry` patch-id equality MUST NOT be used to conclude that orphaned work
came back, for the reason
[branch-hygiene#req:absorbed-is-report-only](../../branch-hygiene/README.md)
gives.

## Behavior

### Command surface

#### REQ: recover-command-surface

WB MUST expose `wb recover` as a top-level command.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--since` | duration | `2160h` (90 days) | how far back to enumerate closed pull requests |
| `--window` | duration | `10m` | how close a base-ref deletion must be to the close to implicate it |
| `--base` | string | `main` | target branch against which re-landing is proved |
| `--only` | string list | all | restrict to the named fates |
| `--parallel` | int | `4` | maximum repositories queried concurrently |
| `--format` | string | `text` | `text`, `json`, or `ndjson` |

It MUST accept the root `--filter`, `--projects-root`, and `--non-interactive`
flags and MUST be registered for `filter` and `projects-root` in
`persistentFlagSupport` and `docs/cli-flag-matrix.md`.

#### REQ: read-only-forensics

`wb recover` MUST be read-only. It MUST NOT reopen, retarget, comment on, or
otherwise mutate any pull request; MUST NOT create, delete, or move any ref;
and MUST NOT define an `--apply` flag or any other mutating mode. Reopening an
orphaned pull request is a judgement call about a stack that may have moved on,
and this command does not have the context to make it.

### Detection

#### REQ: orphaned-by-base-deletion-signature

A pull request MUST be reported as `orphaned-by-base-deletion` when, and only
when, all of the following hold on evidence read from the host:

1. its state is closed and it has no merge timestamp;
2. its base ref does not currently exist as a remote branch in that repository;
3. a timeline event recording the deletion of that base ref occurred within
   `--window` of the pull request's close timestamp.

All three MUST be recorded as evidence on the row, including both timestamps
and the timeline event's identity, so the classification can be checked by a
human without re-running the tool.

A pull request closed unmerged whose base ref still exists, or whose base ref
was deleted far outside the window, MUST NOT carry this class. Over-reporting
would train the reader to dismiss the report, which is the same failure as
under-reporting.

#### REQ: never-report-a-false-all-clear

`wb recover` MUST distinguish "no incidents found" from "could not determine",
and MUST NOT print the former when any repository, pull request, or timeline
page could not be read. A repository whose evidence was incomplete MUST be
named in the report with the reason, and the summary MUST state how many
repositories were fully examined out of how many were selected.

A forensics command that silently reports all-clear when the API refused it is
worse than no forensics command, because it ends the investigation.

### Fate of the orphaned work

#### REQ: fate-taxonomy

Every `orphaned-by-base-deletion` row MUST additionally carry exactly one fate
from this closed set, with its evidence:

| Fate | Definition | Evidence |
|---|---|---|
| `lost` | the head commits are still resolvable and their content is **not** in the fetched target | the head SHA, the target SHA, and the unlanded commit list |
| `relanded` | the head commits' content is provably in the fetched target | the head SHA is an ancestor of the target, **or** a landing receipt plus the local three-way merge proof of [worktree-lifecycle#req:absorbed-integration-containment-evidence](../../worktree-lifecycle/README.md) |
| `relanded-unverified` | the work appears to have returned but no proof is obtainable, typically because the head commits can no longer be resolved | what was resolvable and what was not |
| `unknown` | the evidence needed to decide could not be obtained | the failure |

`relanded-unverified` MUST NOT be presented, counted, or coloured as
`relanded`. The distinction is the entire value of the fate column: "probably
fine" and "proved fine" lead to different actions.

#### REQ: lost-rows-name-the-recovery-command

Every `lost` and every `relanded-unverified` row MUST print the exact command
that retrieves the work, exploiting GitHub's retention of the pull request head
ref after branch deletion:

```
git fetch origin pull/<number>/head:recover-pr-<number>
```

This single line is the difference between a report and a recovery. It MUST
appear on the row itself, not only in the summary or the documentation, because
the row is what gets pasted into an issue.

#### REQ: fate-ordered-output

Rows MUST be ordered `lost`, then `relanded-unverified`, then `unknown`, then
`relanded`, and within each fate by close date, newest first. The summary MUST
lead with the `lost` count. A reader who stops after the first section must
have seen every piece of work that is still missing.

### Cost

#### REQ: batched-hosted-queries

`wb recover` MUST issue a number of hosted requests proportional to the number
of result **pages**, not to the number of pull requests. Closed pull requests
and their timeline events MUST be retrieved in batched, paginated queries — one
query family per repository — rather than one request per pull request.

The manual investigation this replaces was abandoned as too expensive at
fleet scale. A tool that reproduces its per-pull-request request pattern
reproduces its cost and will be abandoned the same way.

Hosted queries MUST obey
`cleanup-orchestration#req:secondary-rate-limits-fail-to-unreadable`: bounded
concurrency, backoff on rate limits, and a candidate whose evidence was not
obtained surfacing as `unknown` rather than being dropped.

## Acceptance Criteria

### AC: the-stacked-pull-request-signature-is-detected-exactly

**Requirements:** pr-recovery#req:recover-command-surface, pr-recovery#req:read-only-forensics, pr-recovery#req:orphaned-by-base-deletion-signature

Given a deterministic host double modelling a repository with: a pull request
closed unmerged two minutes after a timeline event deleting its base ref; a
pull request closed unmerged two hours after its base ref was deleted; a pull
request closed unmerged whose base ref still exists; a merged pull request
whose base ref was deleted immediately after; and an open pull request, when
`wb recover --window 10m --format json` runs, then only the first is classified
`orphaned-by-base-deletion`; its row carries the close timestamp, the deletion
timestamp, and the timeline event identity; and the double records no mutating
call of any kind. A test MUST assert the command exposes no mutating flag.

### AC: the-fate-of-orphaned-work-is-proved-not-guessed

**Requirements:** pr-recovery#req:fate-taxonomy, pr-recovery#req:lost-rows-name-the-recovery-command, pr-recovery#req:fate-ordered-output

Given four orphaned pull requests — one whose head SHA is an ancestor of the
fetched target; one whose commits were re-landed inside a merged integration
pull request, provable only by a landing receipt plus the local three-way merge
proof; one whose head commits are resolvable and whose content is absent from
the target; and one whose head commits can no longer be resolved — when
`wb recover` runs, then their fates are `relanded`, `relanded`, `lost`, and
`relanded-unverified` respectively; the `lost` and `relanded-unverified` rows
each print `git fetch origin pull/<number>/head:recover-pr-<number>` with the
correct number; `relanded-unverified` is never counted or presented as
`relanded`; rows appear in the order `lost`, `relanded-unverified`, `unknown`,
`relanded`; and the summary leads with the `lost` count. Given a fifth orphaned
pull request whose commits are patch-identical to commits in the target but
were reverted there, then its fate is `lost` and not `relanded`, proving
patch-id equality was not used as the re-landing proof.

### AC: forensics-never-reports-a-false-all-clear

**Requirements:** pr-recovery#req:never-report-a-false-all-clear, pr-recovery#req:batched-hosted-queries

Given a fleet selection of ten repositories in which the host double refuses
one repository's timeline pages and rate-limits another, when `wb recover`
runs, then the report does not state that no incidents were found; both
repositories are named with their reasons; affected pull requests carry the
`unknown` fate rather than being omitted; and the summary states how many
repositories were fully examined out of how many were selected. Given a
repository with 500 closed pull requests, a request counter shows the number of
hosted requests is proportional to the page count and does not grow with the
pull request count, and concurrent hosted requests never exceed the configured
bound.

## Open Questions

- Should `wb recover` be able to write its findings into the owning SpecScore
  Feature's Open Questions, or into a Lesson, so a `lost` row becomes tracked
  work rather than terminal output?
- Should the detection window default be tunable per repository? Ten minutes
  fits the observed `gh pr merge --delete-branch` pattern, but a merge queue
  that deletes branches on a schedule would need a wider one.

---
*This document follows the https://specscore.md/feature-specification*
