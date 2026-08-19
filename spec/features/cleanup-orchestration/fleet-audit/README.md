---
format: https://specscore.md/feature-specification
status: In Review
---

# Feature: Fleet Audit

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/fleet-audit?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/fleet-audit?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/fleet-audit?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/fleet-audit?op=request-change) |
**Status:** In Review
**Source Ideas:** —

## Summary

`wb audit` is the read-only, whole-fleet answer to "what is actually here?"
across four subjects at once — uncommitted work, stashes, worktrees, and
branches — with every row carrying the evidence class that produced it and the
command that would act on it. It reuses the evidence taxonomies its siblings
already define rather than inventing a third one, and it is structurally
incapable of presenting patch-id evidence as a licence to delete.

It exists because the hand-rolled version of it, written on 2026-08-19, got
that last part wrong.

## Problem

### The hand-rolled audit produced a dangerous number

A hand-rolled fleet audit classified **280 branches as safe to delete** on the
strength of `git cherry` patch-id evidence. That evidence cannot distinguish
work that landed from work that landed and was then reverted: the revert is a
separate commit, every original patch still has an upstream patch-id twin, and
`git cherry` still emits zero `+` lines. A branch whose work was reverted
upstream is therefore the only remaining copy of that work, and it appeared in
exactly the same column as a branch that is genuinely redundant.

[branch-hygiene#req:absorbed-is-report-only](../../branch-hygiene/README.md)
already rules that evidence class report-only forever, and correctly refuses to
delete on it. But refusing to act on a class is not the same as presenting it
safely: the mistake was made in a *report*, not in a deletion command.
`wb audit` is where the class becomes visible rather than dangerous, and the
report's shape is the mechanism.

### Four subjects, four separate tools, no single answer

Answering "what state is the fleet in?" today means running `wb worktree list`,
reading `git stash list` per repository, enumerating branches by hand, and
checking working-tree cleanliness by hand — 401 clones at a time. There is no
command that answers it once. Re-measurement on 2026-08-19 found, across those
401 canonical clones: 1,524 local branches, 288 in the patch-equivalent-only
class, 340 carrying unique content, 279 whose commits reach no remote ref, 36
working trees holding uncommitted changes, and two independent and disagreeing
records of what worktrees exist.

### A slow audit is an unrun audit

The existing inventory path is not usable at fleet scale: a dry-run
`wb worktree cleanup --all-merged` ran for over nine and a half minutes at
under 1% CPU without producing a byte or finishing, and
`wb worktree list --filter chessraiders` produced 254 stderr lines for 58
result rows, 200 of which named tasks in entirely unrelated repositories.

## Interaction with Other Features

`wb audit` MUST NOT define its own evidence classes for subjects another
feature owns:

- **Branches** use the taxonomy of
  [branch-hygiene#req:evidence-class-taxonomy](../../branch-hygiene/README.md)
  — `contained`, `absorbed`, `unique`, `protected`, `in-use`, `unreadable` —
  computed by that feature's engine.
- **At-risk work** uses the taxonomy of
  [Unpushed Work Detection](../unpushed-work/README.md) — `uncommitted`,
  `detached-only`, `local-only-orphaned`, `local-only-never-published`,
  `stash-only`, `ahead-of-remote`, `published` — computed by that command's
  engine.
- **Worktrees** use the lifecycle dispositions of
  [Worktree Lifecycle](../../worktree-lifecycle/README.md), extended only by
  the two classes this feature adds in `#req:worktree-audit-classes`.

[Cleanup Orchestration](../README.md) defines the output contract, `--filter`
semantics, exit codes, per-unit deadline, and the Git/WB worktree-record
reconciliation that `wb audit` obeys.

`wb audit` is the read-only twin of `wb cleanup`: for any fixture, every row
`wb audit` marks retirable MUST be a row `wb cleanup` would act on, and the
reverse. A divergence is a defect.

## Behavior

### Command surface

#### REQ: audit-command-surface

WB MUST expose `wb audit` as a top-level command.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--scope` | string list | `uncommitted,stashes,worktrees,branches` | which subjects to report |
| `--base` | string | `main` | target branch against which branch disposition is computed |
| `--github` | bool | `false` | attach pull-request evidence |
| `--only` | string list | all | restrict to the named evidence classes |
| `--parallel` | int | `8` | maximum repositories inspected concurrently |
| `--format` | string | `text` | `text`, `json`, or `ndjson` |
| `--verbose` | bool | `false` | emit per-artifact informational lines on stderr |
| `--fail-on` | string | `none` | `none`, `at-risk`, or `any`; selects the exit code |

`--scope preserved` MUST additionally be accepted and MUST report the
preservation roots described in
`cleanup-preconditions#req:preservation-location-and-retention`. It is not in
the default set because it describes WB's own storage rather than repository
state.

`wb audit` MUST accept the root `--filter`, `--projects-root`, and
`--non-interactive` flags and MUST be registered for `filter` and
`projects-root` in `persistentFlagSupport` and `docs/cli-flag-matrix.md`.

#### REQ: read-only-in-every-configuration

`wb audit` MUST be read-only in every configuration. It MUST NOT create, move,
delete, or rewrite any ref, index, working tree, worktree registration, stash,
report, or journal, and MUST NOT define an `--apply` flag or any other mutating
mode. Its only permitted remote interaction is fetching, and only where a
disposition requires the exact fetched target.

#### REQ: offline-by-default

`wb audit` MUST NOT contact the hosted API unless `--github` is explicit. A run
without `--github` MUST still be truthful: any disposition that requires
pull-request evidence MUST be reported as `needs-github` rather than guessed,
with the reason naming the flag that would resolve it.

The default matters because a fleet audit that takes twenty minutes of API
calls is an audit that is run once and then never again. The cheap local answer
run daily is worth more than the complete answer run annually.

### Subjects and classes

#### REQ: worktree-audit-classes

In addition to the dispositions
[Worktree Lifecycle](../../worktree-lifecycle/README.md) defines, `wb audit`
MUST report these classes, both of which describe state that is currently
invisible:

- `registration-missing` — a Git worktree registration whose directory no
  longer exists, or which Git reports prunable. Remedy: `git worktree prune`
  for the named clone.
- `unclassified` — a linked worktree whose identity WB could not validate, such
  as the `is not on a feature branch` condition observed for
  `engine-stack-regression` and `review-task5`. It MUST appear as a **row**,
  not only as a diagnostic, so it is countable, and it MUST carry a remedy as
  `cleanup-orchestration#req:every-warning-names-a-remedy` requires.

Every worktree row MUST additionally carry the `known_by` field defined by
`cleanup-orchestration#req:reconcile-git-and-wb-worktree-views`, so the
disagreement between Git's registrations and WB's inventory is visible in the
audit rather than only inside a cleanup run.

#### REQ: every-row-carries-evidence-and-remedy

Every row, in every subject and every format, MUST carry: repository, subject
identity (branch name, worktree path, stash index, or working-tree path), the
exact SHA where one exists, the evidence class, the literal evidence string
that produced the class, the age of the evidence, and the exact command that
acts on it. A row that says only what something is, without saying what to do
about it, reproduces the discoverability failure that made an agent write its
own tool.

For the `absorbed` class the remedy MUST be the receipt-based
`wb worktree cleanup <task> --absorbed-by <pr-or-commit>` for a WB-owned
branch, or an explicit human decision otherwise, exactly as
[branch-hygiene#req:absorbed-names-its-remedy](../../branch-hygiene/README.md)
requires.

### The report's shape is a safety mechanism

#### REQ: absorbed-is-never-summed-with-contained

`wb audit` MUST NOT emit, in any format, any aggregate that adds the
`contained` count to the `absorbed` count, and MUST NOT emit any field, column,
heading, or sentence describing a single number as "safe to delete" that
includes `absorbed`.

In the text report the two MUST appear under separate headings, `absorbed` MUST
be labelled report-only in its heading, and each `absorbed` row MUST carry the
one-sentence reason patch-id evidence is insufficient. In `--format json` the
summary object MUST expose per-class counts only, with no `safe`, `deletable`,
`total_safe`, or equivalent rolled-up key.

This requirement is the whole reason this feature is separate from a printout.
The 280-branch misclassification of 2026-08-19 was not a failure to know the
rule; it was a report whose shape invited the addition.

#### REQ: severity-ordered-sections

The report MUST present its sections in this order, and the summary MUST
present its totals in the same order:

1. **At risk** — `uncommitted`, `detached-only`, `local-only-orphaned`,
   `local-only-never-published`, `stash-only`, in the severity order
   `unpushed-work#req:severity-ordered-output` defines. What a disk failure
   destroys.
2. **Blocked** — `in-use`, `unreadable`, `unclassified`,
   `registration-missing`, `needs-github`. What WB cannot reason about and a
   human must resolve.
3. **Retirable** — `contained`, plus worktrees eligible under Worktree
   Lifecycle. What `wb cleanup --apply` would act on.
4. **Report-only** — `absorbed`. Never actionable by any command.
5. **Healthy** — `published`, `protected`, and everything with nothing to do.

A reader who stops after the first section must have seen the things that
cannot be recovered. A reader who stops after the third must not have seen
`absorbed` presented as an opportunity.

#### REQ: audit-agrees-with-cleanup

For any fixture, the set of rows `wb audit` places in the Retirable section
MUST equal the set of items `wb cleanup --apply` would act on with the same
`--base`, `--older-than`, and `--filter`. Both MUST call the same engines
(`cleanup-orchestration#req:no-independent-eligibility`); the difference
between the two commands MUST be that one acts and one does not, never what
they believe.

### Cost

#### REQ: audit-is-fleet-scale

A default `wb audit` over the whole fleet MUST complete without network access,
MUST stream progress and results as it goes, and MUST use a constant number of
Git subprocesses per repository regardless of ref count, exactly as
`unpushed-work#req:bounded-local-cost` requires.

A run MUST NOT be capable of either observed failure mode — producing no output
at all while it works, or producing only scanning heartbeats while withholding
every finding until the end. `cleanup-orchestration#req:bounded-per-unit-time`,
`cleanup-orchestration#req:bounded-default-stderr`,
`cleanup-orchestration#req:progress-liveness`, and
`cleanup-orchestration#req:incremental-findings` apply to `wb audit` unchanged,
with the per-repository deadline replacing the per-unit one. For an audit the
canonical clone is both the inspection unit and the flush unit, so each
repository's rows are emitted as that repository completes.

## Acceptance Criteria

### AC: four-subjects-one-truthful-report

**Requirements:** fleet-audit#req:audit-command-surface, fleet-audit#req:read-only-in-every-configuration, fleet-audit#req:offline-by-default, fleet-audit#req:worktree-audit-classes, fleet-audit#req:every-row-carries-evidence-and-remedy

Given a fixture fleet containing a dirty working tree, a stash entry with
unpublished commits, a worktree on a merged branch, a Git worktree registration
whose directory has been deleted, a worktree whose branch fails identity
validation, and one branch of each branch-hygiene disposition, when
`wb audit --format json` runs, then every item appears exactly once under its
correct subject with its correct class, including `registration-missing` and
`unclassified` as rows rather than diagnostics; every row carries repository,
identity, SHA where applicable, class, evidence string, age, remedy command,
and — for worktrees — `known_by`; no network request is made; any disposition
needing pull-request evidence is reported `needs-github` naming that flag
rather than guessed; and a byte-for-byte comparison of the fixture's refs,
worktrees, working trees, stashes, and registrations before and after shows no
change. A test MUST assert the command exposes no mutating flag.

### AC: the-report-cannot-express-the-2026-08-19-mistake

**Requirements:** fleet-audit#req:absorbed-is-never-summed-with-contained, fleet-audit#req:severity-ordered-sections

Given a fixture holding branches in the `contained` and `absorbed` classes,
including one branch whose commits were cherry-picked into `main` and then
reverted in `main`, when `wb audit` runs in `text`, `json`, and `ndjson`, then
no output field, column, heading, or sentence sums `contained` and `absorbed`;
the JSON summary exposes per-class counts and no rolled-up `safe` or
`deletable` key; the text report shows the two under separate headings with
`absorbed` labelled report-only; the reverted branch is classified `absorbed`
and its row carries both the reason patch-id evidence is insufficient and its
receipt-based remedy; and the sections appear in the order at risk, blocked,
retirable, report-only, healthy, with the summary totals in the same order. A
test MUST assert by inspecting the emitted JSON schema that no key exists whose
value is the sum of two class counts.

### AC: audit-and-cleanup-never-disagree

**Requirements:** fleet-audit#req:audit-agrees-with-cleanup

Given a fixture fleet exercising every disposition, when
`wb audit --base main --filter <substring>` and
`wb cleanup --base main --filter <substring>` both run without `--apply`, then
the set of repository-and-identity pairs in the audit's Retirable section is
exactly equal to the set the cleanup plan marks eligible, and each pair's
stated reason is identical in both. Repeating with `--older-than 0` and with
`--github` preserves the equality.

### AC: a-fleet-audit-finishes-and-says-so-while-it-runs

**Requirements:** fleet-audit#req:audit-is-fleet-scale

Given a fixture fleet of forty canonical clones each holding many refs, when
`wb audit --format json` runs with stdout and stderr captured separately, then
at least one progress event naming a repository with an `[n/N]` count reaches
stderr before the report is written; stderr carries at most one progress line
per repository plus the fixed run-level lines and exactly one aggregate line
for WB-internal artifacts; each repository's rows are emitted as that
repository completes rather than held to the end, so a run killed after twenty
repositories has already delivered those twenty repositories' rows; a
subprocess counter shows Git invocations bounded by a constant per repository
and not growing with ref count; a repository whose inspection blocks past the
deadline is reported with a timeout outcome while every other repository still
reports; and the process exits.

## Open Questions

- Should `wb audit` persist a dated snapshot so the fleet's drift is measurable
  over time, or does that duplicate what a report directory already provides?
- Should `--scope stashes` attribute a stash entry to the worktree it was
  created in? `refs/stash` is repository-global and Git records no such
  attribution, so this may only be answerable heuristically from the entry's
  message.

---
*This document follows the https://specscore.md/feature-specification*
