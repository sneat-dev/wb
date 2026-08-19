---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Operations Journal

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Append-only journal of every wb operation, bundle-backed preservation for unreachable commits, and a wb restore command that reads reports and journal records back into a branch.

## Problem

### There is no single record of what WB did

The founder's question on 2026-08-19 was direct: *"Do we have a log of wb
destructive (and non destructive) operations? We probably would be able to
restore branches from the log by ref hash."* The honest answer, measured the
same night, is no.

What exists instead is durable but scattered evidence, one directory per run,
with no index tying it together:

| Location | Measured | What it holds |
|---|---|---|
| `reports/worktree-cleanup/<UTC-timestamp>/cleanup.json` | **624 run directories** | Per-candidate decisions from `wb worktree cleanup` — task, repository, branch, `head_sha`, `remote_target_sha`, disposition, and whether it was applied. |
| `reports/worktree-cleanup/backlog/<64-hex>.json` | hundreds of files | Interrupted-operation resume records, named **only by a content hash** — nothing in the filename says repository, branch, or task. |
| `reports/worktree-cleanup/stage-archive/<task>-<hash>/` | — | Internal staging artifacts from a resumed cleanup. |
| `reports/branch-cleanup/<timestamp>/` | — | Written by `wb branch cleanup --apply` before its first destructive operation. |
| `worklogs/<effort-id>/` | **509 entries** | Per-effort agent-session recovery journals (see [Work Log Recovery](../work-log/README.md)) — prompts, claims, provenance. Not about Git mutations across the fleet. |
| `archive/` | **1 entry** | `superseded-patches`, created by hand tonight. |
| `backups/` | **1 entry** | `competios-all-refs-2026-08-12.bundle`, created by hand during a previous scare. |

Reconstructing "what did WB do to `sneat-co/competios` this week" means
walking some subset of those 624 timestamped directories, opening each
`cleanup.json`, and cross-referencing hash-named backlog files by hand. That
this is even possible is a testament to the individual report writers, not to
WB as a system: nothing aggregates them, nothing is greppable by repository or
branch, and nothing distinguishes "WB did this on purpose and it succeeded"
from "WB started this and the process died." `archive/` and `backups/` each
holding exactly **one** hand-made entry, next to 624 machine-written report
directories, is the measurement that the current discipline is manual — a
human remembered to capture something twice — and manual discipline is not a
log.

Tonight's own run — `wb branch cleanup --scope all --apply` deleting **777
branches** (416 local, 361 remote) — is itself evidence of the gap: it is
traceable today only because its own report directory happens to still exist
and because the operator was watching. Non-destructive operations — `wb
worktree create`, `wb sync`, `wb deps set`, a plan run without `--apply` — are
not recorded at all, so "what did WB do" cannot currently distinguish "nothing
happened" from "something happened and left no trace."

### A recorded SHA is not a durable restore path

Every cleanup report already carries `head_sha`, so in principle
`git branch <name> <sha>` restores a deleted branch — **but only while the
object still exists**. A branch that loses its only ref becomes unreachable,
and `git gc` prunes unreachable objects (roughly a two-week default horizon).
After that, the report tells a reader exactly what was lost and gives them no
way back — the log becomes a record of failure rather than a recovery path.

Tonight's 777 deletions are safe precisely because every one of them was
disposition `contained`: every commit is already an ancestor of the fetched
`origin/main`, so the commits stay reachable through that ref even after the
branch ref that used to point at them is gone. The gap is real for the classes
[branch-hygiene#req:evidence-class-taxonomy](../branch-hygiene/README.md)
deliberately does **not** delete: **353 `absorbed`** and **726 `unique`**
branches in the current fleet. Their commits are not proven reachable from any
surviving ref, so a recorded SHA alone is not a promise WB can keep for them.
[Preservation and Pre-Flight](../cleanup-orchestration/cleanup-preconditions/README.md)
already reached the same conclusion independently and requires a Git bundle —
not a patch, not a SHA — for exactly this reason
(`cleanup-preconditions#req:preservation-content`, item 4): a patch cannot
preserve the commits of a branch that is about to lose its only ref.

### Reports carry everything a restore needs; nothing reads them back

`cleanup.json`, the branch-cleanup report, and the preservation manifest
defined by `cleanup-preconditions#req:manifest-carries-its-own-restore`
between them already carry repository, branch, exact SHA, and — for a
preserved branch — the literal restore command. Recovery today means a human
opening the right JSON file, copying a SHA or a bundle path, and typing the
`git` commands by hand. Nothing in WB reads a report or a manifest back into a
live branch.

## Interaction with Other Features

### Why a sibling of Cleanup Orchestration, not a fifth child

[Cleanup Orchestration](../cleanup-orchestration/README.md) is a parent of
four children — `wb cleanup`, `wb unpushed`, `wb audit`, `wb recover` —
because those four share one output contract, one unit-partitioning rule, one
concurrency model, and one preservation gate, and its own "why one parent"
section is explicit that the thing which justifies the grouping is a **shared
evidence model at a bounded scope**: the worktree/branch retirement lifecycle
of one repository at a time.

This feature's scope is wider than that lifecycle in the dimension that
matters for a parent/child decision: the journal MUST record `wb worktree
create`, `wb sync`, `wb deps set`, `wb migrate`, and every other
state-changing WB command, not only the four cleanup-family verbs. Nesting it
under `cleanup-orchestration` would misstate its scope — a reader would
reasonably expect a child of that parent to be about retiring worktrees and
branches, and this is about every operation WB performs, destructive or not.
It is therefore a **sibling** of `cleanup-orchestration`,
`worktree-lifecycle`, and `branch-hygiene`, at the same level in
`spec/features/`.

Within this feature, the journal (an index over every operation) and `wb
restore` (a command that reads that index and reports back) are **one
document, not two children**, for the same reason
[cleanup-preconditions](../cleanup-orchestration/cleanup-preconditions/README.md)
folded preservation and stacked-pull-request pre-flight into one document
rather than two: they share exactly one evidence model — the journal record
**is** `wb restore`'s input — so splitting them would only guarantee the two
halves drift out of sync with each other.

### Composes with cleanup-preconditions; does not re-specify it

Bundle-backed preservation for the worktree/branch retirement lifecycle is
**already fully specified** by
[cleanup-preconditions](../cleanup-orchestration/cleanup-preconditions/README.md):
what gets captured
(`cleanup-preconditions#req:preservation-content`), where it lives and how
long it survives
(`cleanup-preconditions#req:preservation-location-and-retention`), how it is
verified before it counts
(`cleanup-preconditions#req:preservation-is-verified-before-it-counts`), and
that its manifest already carries its own restore command
(`cleanup-preconditions#req:manifest-carries-its-own-restore`). That feature's
own Open Questions section asks the exact question this feature answers:
*"Should a future `wb restore <run-id>` command drive the manifest's recorded
commands, or does adding a restore verb create a new destructive surface that
itself needs these gates?"* This feature's answer is: **yes to both** — `wb
restore` drives the manifest, and because it creates or overwrites a ref it is
itself a destructive-adjacent surface, gated by its own refusal rules in
`#req:restore-refuses-to-clobber-a-diverged-branch` rather than by
cleanup-preconditions' gates, which are about the moment before *deletion*,
not creation.

This feature therefore does not restate bundle creation, verification, or
manifest content. It adds only what cleanup-preconditions does not cover:
a fleet-wide, cross-repository **index** so a bundle or a manifest can be
*found* without knowing its run ID in advance
(`#req:journal-is-an-index-not-a-replacement`), a naming rule that replaces
the `<64-hex>.json` anti-pattern
(`#req:preserved-artifacts-are-self-describing-by-name`), and the read path
that turns a verified manifest back into a live branch (`#req:wb-restore`).

### Composes with Work Log Recovery; answers a different question

[Work Log Recovery](../work-log/README.md) already defines an append-only,
crash-consistent journal — `events.jsonl` plus a derived `projection.json`,
sealed on cleanup into `<WB_HOME>/worklogs/<effort-id>/` and later archived to
`<WB_HOME>/worklogs/archive/<YYYY>/<MM>/<effort-id>/`. This feature's
Operations Journal reuses that crash-safety idiom (append-only JSONL, atomic
projection rewrite, fsync on high-value events, recovery that discards only a
torn final line — `work-log#req:append-only-crash-consistent-events`) rather
than inventing a second one, but it is not the same journal wearing a new
name:

| | Work Log | Operations Journal |
|---|---|---|
| Scope | one effort, one worktree | every repository, every task, the whole fleet |
| Subject | prompts, claims, agent provenance, session recovery | Git-state-changing WB operations: refs, branches, worktrees, stashes, pull requests, preservation |
| Lives | inside the worktree, then `<WB_HOME>/worklogs/<effort-id>/` | `<WB_HOME>/journal/`, never inside a worktree or a canonical clone |
| Answers | "what directed this effort, and can a successor resume it?" | "what did WB do to this repository or branch, and can it be undone?" |

A single operation MAY reference both: a `wb cleanup --apply` run performed
under an active Work Log claim carries that claim's effort ID in its journal
record (`#req:journal-record-fields`), so the two can be cross-referenced, but
neither reads or writes the other's storage.

### Composes with Fleet Audit and the existing report writers

[Fleet Audit](../cleanup-orchestration/fleet-audit/README.md) already defines
`wb audit --scope preserved` to report each preservation run's root, age,
artifact count, and total size
(`cleanup-preconditions#req:preservation-location-and-retention`). This
feature extends that surface with `--scope journal` rather than defining a
competing report command (`#req:journal-audit-scope`).

`internal/worktrees/lifecycle.go` already writes `cleanup.json` durably —
temporary file, `fsync`, atomic rename — via `writeCleanupReport`, at the path
`DefaultCleanupReportDir` computes below `<wb-home>/reports/worktree-cleanup/`.
This feature does not change that writer or that path. The journal is a
second, much smaller record written **alongside** each such report — never a
replacement for it — that exists purely so the report can be *found*
(`#req:journal-is-an-index-not-a-replacement`). The 624 existing report
directories and hundreds of hash-named backlog files that predate this
feature are handled by `#req:journal-backfill`, not by rewriting the report
writer.

## Behavior

TODO: How does this feature work?

## Acceptance Criteria

TODO: Define acceptance criteria.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
