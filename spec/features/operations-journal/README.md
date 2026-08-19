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

## Behavior

TODO: How does this feature work?

## Acceptance Criteria

TODO: Define acceptance criteria.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
