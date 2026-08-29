---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Archived Clone Cleanup

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/archived-clone-cleanup?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/archived-clone-cleanup?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/archived-clone-cleanup?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/archived-clone-cleanup?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

wb archive clean safely removes local clones of repositories confirmed archived on GitHub, but only when the clone holds nothing that would be lost.

## Problem

30 `sneat-co/ext-*` repositories were archived on GitHub in one wave. Their
local clones stayed on disk: still counted in fleet inventory, still noise in
every audit. Nothing in WB retires a clone once its repository is archived,
and this will recur with every future retirement wave.

`wb sync` already removes a *clean* archived clone as a side effect of its own
`Dirty()` check (working tree, stash, unpushed commits), but that check is not
the strict predicate this deletion needs: it does not look for a linked
worktree, a local-only branch, an unpushed tag, or a live WB Work Log claim
before calling `os.RemoveAll` on the canonical clone. A linked worktree in
particular shares Git object storage with the canonical clone it is linked
from — removing the canonical directory out from under a linked worktree
breaks or orphans it, which `wb sync`'s current check cannot see. `wb sync`
also mutates by default (`-n`/`--dry-run` is opt-in), which is the wrong
default shape for a purpose-built, more thorough, more destructive check.

Two fleet incidents the same night this feature was scoped illustrate the
blind spots a naive "is the working tree clean?" check has: a lane nearly lost
~2,400 lines of real work that sat entirely in untracked files (invisible to
`git diff`), and a separate clone held 25 unpushed commits on a branch that
was not checked out plus a lone unpushed commit on another.

## Behavior

`wb archive clean` inventories every locally-cloned repository below
`--projects-root`, confirms each one's archived status live against GitHub
(never a name pattern, never a cached/stale local list), and reports, per
clone, whether it is deletable and exactly why or why not. It is a dry-run
plan by default; `--apply` is required to actually delete anything, matching
the `wb layout clean` / `wb branch cleanup` / `wb worktree cleanup` convention
of "plan by default, explicit apply to mutate".

A clone is eligible for deletion only when **every** one of these holds:

1. The repository is confirmed **archived on GitHub right now** (a live
   `isArchived` check for that exact repository, not an inference from name or
   from a cached inventory).
2. The working tree has **no uncommitted changes**. Untracked paths are a
hard blocker by default (they are invisible to `git diff` and have caused real
near-losses). The sole exception is the narrow, separately authorized
`--apply --delete-untracked` flow in
[Decision 0001](../../decisions/0001-archive-clean-untracked-deletion.md): it
itemizes every path, rereads the exact manifest, rejects drift/links/traversal,
records a durable receipt, then reevaluates before pruning.
3. **No stashes.** The stash stack is repo-global across worktrees; a blind
   drop is unrecoverable.
4. **No unpushed commits on any local branch** — every branch is checked, not
   only the one currently checked out.
5. **No local-only branches** — every local branch must exist on `origin`,
   independent of whether its commits are also reachable via another ref;
   deleting the clone would otherwise silently discard a branch name/pointer
   GitHub never saw.
6. **No unpushed tags** — every local tag must exist on `origin`.
7. **No linked worktrees** registered against this clone (`git worktree
   list`), since a linked worktree shares this clone's Git object storage and
   would be broken or orphaned by removing it.
8. **Nothing under WB's home references it**: no live task worktree, no open
   (non-terminal) Work Log claim recorded against this repository.

Any check that cannot be completed — GitHub is unreachable, a ref cannot be
resolved, a Work Log claim cannot be read — makes that clone **not
deletable**, reported with the failing check named. Failing closed is the only
acceptable default for a destructive, irreversible-on-this-machine operation.

`wb archive clean` never touches a repository that is not confirmed archived,
regardless of any other state. Output lists every inspected clone, deletable
or not, with the exact reason; skips are reported as prominently as deletions,
never summarized away as a bare count.

### `wb sync --prune-archived`

`wb sync` reconciles local clones with GitHub fleet-wide and, on its own,
already removed a clean archived clone by default — gated only on a plain
working-tree/stash/unpushed check with no awareness of linked worktrees,
local-only branches, unpushed tags, or WB Work Log claims, and with no way to
opt out short of `--dry-run`. The founder ruled this out explicitly: archived
handling in `wb sync` must be **opt-in**, via one explicit flag
(`--prune-archived`), and when opted in it must use the exact same predicate
above — not a second, weaker implementation of it.

With `--prune-archived` absent (the default), an archived repository is
synced exactly like any other: pulled if its clone exists and is clean, never
deleted; its clone is still reported as archived so it is never silently
indistinguishable from an ordinary one. With `--prune-archived` present, an
archived repository's clone is evaluated against the identical eligibility
check `wb archive clean` uses (the same function, not a re-derivation) and
removed only if it passes every check above; a refusal names the reason,
exactly like `wb archive clean`'s own report.

## Acceptance Criteria

- AC-001: **Given** a local clone of a repository confirmed archived on
  GitHub, with a clean working tree, no untracked files, no stash, no local
  branch or tag missing from `origin`, and no linked worktree or open WB
  claim, **when** `wb archive clean --apply` runs, **then** the clone is
  deleted and the plan/report names it deleted with the reason "archived and
  clean".
- AC-002: **Given** the same clean, archived clone, **when** `wb archive
  clean` runs without `--apply`, **then** nothing is deleted and the report
  marks it eligible/would-delete.
- AC-003: **Given** an otherwise-clean archived clone that holds one unpushed
  commit on a branch that is **not** the checked-out branch, **when** `wb
  archive clean [--apply]` runs, **then** the clone is refused, naming the
  branch and the unpushed commit.
- AC-004: **Given** an otherwise-clean archived clone with one or more
untracked files and no other issue, **when** `wb archive clean [--apply]`
runs without `--delete-untracked`, **then** the clone is refused and the plan
itemizes every untracked path and size.
- AC-013: **Given** the AC-004 clone, **when** an operator first reviews its
dry-run plan and then runs `wb archive clean --apply --delete-untracked`,
**then** WB rereads the exact path descriptors, refuses changed/additional
paths, symlinks, and traversal, writes a durable itemized receipt, deletes
only the unchanged authorized paths, reevaluates the archive predicate, and
prunes the clone only if it remains eligible.
- AC-005: **Given** an otherwise-clean archived clone holding a stash entry,
  **when** `wb archive clean [--apply]` runs, **then** the clone is refused,
  naming the stash.
- AC-006: **Given** an otherwise-clean archived clone with a linked worktree
  registered against it, **when** `wb archive clean [--apply]` runs, **then**
  the clone is refused, naming the linked worktree path.
- AC-007: **Given** a local clone whose repository is **not** archived on
  GitHub, **when** `wb archive clean [--apply]` runs, **then** the clone is
  refused as not archived, regardless of how clean it is.
- AC-008: **Given** a local clone whose live archived-status check against
  GitHub fails (network error, auth failure, rate limit), **when** `wb archive
  clean [--apply]` runs, **then** the clone is refused as not deletable, with
  the check failure named, and no other clone's evaluation is aborted by one
  failure.
- AC-009: **Given** an otherwise-clean archived clone holding a local-only
  branch or an unpushed tag, **when** `wb archive clean [--apply]` runs,
  **then** the clone is refused, naming the branch or tag.
- AC-010: **Given** an otherwise-clean archived clone referenced by a live WB
  task worktree or a non-terminal Work Log claim, **when** `wb archive clean
  [--apply]` runs, **then** the clone is refused, naming the referencing task.
- AC-011: **Given** a clean archived clone, **when** `wb sync` runs **without**
  `--prune-archived`, **then** the clone is never deleted — it is pulled (or
  left alone) exactly like an ordinary clone — and the report still names it
  as archived.
- AC-012: **Given** the same fleet, **when** `wb sync --prune-archived` runs,
  **then** each archived repository's clone is evaluated with the identical
  predicate AC-001 through AC-010 describe (the shared implementation, not a
  re-derivation): a qualifying clone is deleted, and a disqualified one is
  refused with the reason named, matching `wb archive clean`'s own report.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
