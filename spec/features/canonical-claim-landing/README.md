---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Canonical Claim Landing

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-landing?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-landing?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-landing?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-landing?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Landing a canonical claim never removes the canonical clone. After the pull
request lands, WB restores the clone to its base branch (checkout base,
fast-forward only to the latest remote, delete the local feature branch), seals
the claim, and the strict guard returns. Cleanup, gc, abort, retire, and end
share one hard guarantee that they can never target a canonical path, and
`wb sync` and fleet readers stop treating a claimed canonical clone as the
latest base branch. Admission, guard, hook, agent-guard, marker, lease, and
rescue behavior is specified in
[canonical-claim-admission](../canonical-claim-admission/README.md).

## Problem

Every existing landing and cleanup path assumes the checkout it retires is a
linked worktree: `cleanupWorktreeMergeAssets` calls `worktrees.Cleanup`, which
removes the worktree. Pointed at a canonical clone that would be catastrophic,
and `syncCanonicalMergeTarget` in `internal/orchestrate/worktree_merge.go`
returns `not_checked_out` when the canonical clone is on a feature branch, so
the clone would stay on a merged branch forever. Meanwhile `fleetsync.syncActive`
runs `git pull` on whatever branch is checked out, and fleet readers that treat
the canonical working tree as the base branch tip would read an unlanded
feature branch as "latest main".

## Behavior

### Landing restores the base branch

#### REQ: landing-restores-canonical

When the landed claim has `mode: canonical`, landing MUST, after the remote
landing is proven, restore the canonical clone in this order: verify the clone
is clean and on the claim's branch; fetch `origin/<base>`; check out the base
branch; fast-forward it with `--ff-only` to the fetched remote tip and verify it
contains the exact landed head (the checks `syncCanonicalMergeTarget` already
makes); delete the local feature branch only when its tip is proven contained in
the landed target; seal the claim; refresh the marker. The canonical path MUST
NOT be removed, renamed, or relocated at any step. The same restore MUST run
whichever verb lands the claim: `wb worktree land` (alias `wb land`),
`wb pr create` with its landing, and `wb pr land`.

#### REQ: restore-is-resumable

Each restore step MUST be idempotent and recorded in the landing receipt, so an
interrupted landing resumes from the first unfinished step with no duplicate
effect. Until the claim is sealed the claim MUST stay active, so a crash after
the remote landed never leaves the clone both admitted and unowned: an
unsealed claim keeps the one-claim rule in force and the lease keeps bounding
admission.

#### REQ: restore-refuses-unsafe-state

If the clone is dirty, off the claim's branch, detached, or cannot fast-forward,
the restore MUST stop before any destructive step, report which step blocked and
why, leave the claim active, and name `wb worktree rescue <path>` for dirty
content. It MUST NOT reset, clean, stash, or force anything. A local feature
branch whose tip is not proven contained in the landed target MUST be kept and
reported.

#### REQ: release-without-landing

`wb worktree end <task>` on a canonical claim MUST release the claim without
landing, with no extra git call from the agent: commit any uncommitted work onto
the claim's feature branch as a WIP commit (never discard, reset, or stash), push
that branch so the work survives, check out the base branch, fast-forward it with
`--ff-only` to the latest remote, KEEP the unmerged feature branch both locally
and on the remote, seal the claim as released, refresh the marker, and restore
the strict guard. It MUST work for a lapsed but unsealed claim, committing and
pushing through the managed hooks (`Guard` admits that on the claim branch; no
hook bypass). If the commit or the push fails, `end` MUST stop before switching
branches, leave the claim active, and report the failure (the same rule as
`end`'s capture failure). It MUST use the same restore routine and receipt as
landing minus the branch deletion.

#### REQ: end-never-removes-canonical

`wb worktree end` on a canonical claim MUST NOT remove, rename, or relocate the
canonical directory, on success or failure; it is covered by
[canonical-path-never-removable](#req-canonical-path-never-removable).

### Cleanup can never target a canonical path

#### REQ: canonical-path-never-removable

Every WB entry point that removes, retires, moves, or adopts a checkout MUST
share one predicate that identifies a canonical clone (the checkout whose Git
directory equals its common directory, or whose path equals a repository's
canonical path) and MUST refuse a path for which it is true, before any
filesystem change, regardless of claim state, `--force`, `--apply`, or the
claim's recorded `worktree` field. Removal and retirement entry points:
`Cleanup`, `GC`, `Abort`, `wb worktree end` (`worktreeend.Engine`), `Retire`,
`RetireTaskShells` (the `os.Remove` calls in `shell_retirement.go`), the orphan
residue removal in `Orphans` and `removeWorktreeResidue`, and the merge-cleanup
path (`cleanupWorktreeMergeAssets`). Movement entry points `Relocate`,
`RelocateCheckout`, and `RelocateRepository` MUST refuse a canonical clone as the
source of a move. `Adopt` and `Backfill` do not remove anything but MUST refuse
to register a canonical path as an adopted or back-filled worktree. The
guarantee MUST be covered by its own dedicated test that feeds a canonical path
to each listed entry point, and every directory-removal helper they share MUST
check the predicate.

#### REQ: cleanup-skips-clone-holding-live-claim

Branch hygiene and cleanup MUST skip a clone that holds an active canonical
claim: the claim's feature branch MUST NOT be deleted, its checkout MUST NOT be
changed, and the skip MUST be reported with the claim's task and lease. A lapsed
but unsealed claim MUST also be skipped and reported as awaiting rescue, end, or
land.

### Sync and fleet readers

#### REQ: sync-skips-live-canonical-claim

`wb sync` (`fleetsync.syncActive`) MUST NOT pull into, fetch-merge, or change
the checkout of a clone holding an active canonical claim; it MUST report a
distinct skipped status naming the task and branch and still refresh the
marker. A clone whose claim is sealed MUST sync as before.

#### REQ: fleet-readers-do-not-read-claimed-clone-as-main

Fleet inspections that treat the canonical working tree as the base branch tip
(canonical freshness via `inspectCanonicalFreshness`, fleet status, dependency
and scan readers) MUST, for a clone holding an active canonical claim, report
the clone as claimed on its feature branch and MUST NOT report its HEAD,
working tree, or dirty state as the base branch or as a fleet health
violation. Readers MUST use the remote base ref for base content.

## Interaction with Other Features

[canonical-claim-admission](../canonical-claim-admission/README.md) defines the
claim, lease, and guard layers this feature depends on.
[Mechanical Worktree Merge](../mechanical-worktree-merge/README.md) owns the
landing receipt and routes this feature extends.
[Worktree Lifecycle](../worktree-lifecycle/README.md) owns cleanup, gc, and
abort. [Fleet Status](../fleet-status/README.md) owns canonical health reporting.

## Dependencies

- canonical-claim-admission

## Acceptance Criteria

### AC: landing-restores-clone-and-keeps-it

**Requirements:** canonical-claim-landing#req:landing-restores-canonical, canonical-claim-landing#req:restore-is-resumable

**Given** a real bare remote and a canonical claim whose feature branch has a landed pull request
**When** `wb worktree land` completes, once through each of the three landing verbs
**Then** the canonical path still exists, is on the base branch at the remote tip containing the landed head, the local feature branch is deleted, the claim is sealed, `.worktree.md` is `writable: false`, and `wb worktree guard` passes strictly; a fault-injection seam on the restore routine (an injectable step runner in `internal/worktrees`, set by tests only) fails it after each step, and rerunning completes without duplicate effects.

### AC: landing-refuses-unsafe-clones-without-destroying

**Requirements:** canonical-claim-landing#req:restore-refuses-unsafe-state

**Given** canonical clones that are dirty, on another branch, detached, or diverged from the remote base, and one whose feature branch holds commits not contained in the landed target
**When** landing runs
**Then** each unsafe clone is refused at the blocking step with its remedy and the claim stays active, nothing is reset, cleaned, or stashed, and the unmerged branch is retained and reported.

### AC: end-releases-claim-keeping-work

**Requirements:** canonical-claim-landing#req:release-without-landing, canonical-claim-landing#req:end-never-removes-canonical

**Given** a real bare remote and a canonical claim whose clone has uncommitted work on the feature branch, a second one where the push is made to fail, and a third that is detached or dirty off the claim branch
**When** `wb worktree end <task>` runs on each, once with a live and once with a lapsed lease
**Then** the first commits the work as a WIP commit on the feature branch, pushes it, checks out the base branch at the latest remote tip, keeps the feature branch locally and on the remote, seals the claim as released, restores the strict guard and a `writable: false` marker, and the canonical directory still exists; the second stops before any checkout, leaves the claim active and the clone on the feature branch with the work intact, and reports the push failure; the third is refused with `wb worktree rescue` named. In every case the canonical directory still exists.

### AC: no-removal-path-reaches-a-canonical-clone

**Requirements:** canonical-claim-landing#req:canonical-path-never-removable, canonical-claim-landing#req:cleanup-skips-clone-holding-live-claim

**Given** a canonical clone, with and without an active canonical claim, with a claim whose recorded worktree is the canonical path
**When** cleanup, gc, abort, end, orphans, backfill, adopt, relocate, relocate-checkout, relocate-repository, retire, retire-shells, and merge cleanup are run with `--apply` and `--force` against it
**Then** every entry point refuses before any filesystem change, the clone and its feature branch are intact, and the dedicated guarantee test passes.

### AC: sync-and-readers-skip-claimed-clone

**Requirements:** canonical-claim-landing#req:sync-skips-live-canonical-claim, canonical-claim-landing#req:fleet-readers-do-not-read-claimed-clone-as-main

**Given** a clone with an active canonical claim, behind its remote base, and a sealed one
**When** `wb sync` and the fleet freshness/status readers run
**Then** the claimed clone's checkout is unchanged and reported as skipped and claimed on its feature branch (not as latest base, not as dirty-clone violation), while the sealed clone syncs as before.

## Open Questions

- The set of "fleet readers" that treat the canonical working tree as the base tip is not yet enumerated; the plan's first step in task 3 (confirmed by the founder) inventories them before any reader changes. The release verb (`wb worktree end <task>`) is confirmed.

---
*This document follows the https://specscore.md/feature-specification*
