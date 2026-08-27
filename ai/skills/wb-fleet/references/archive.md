# Archived clone cleanup

`wb archive clean` deletes a local clone only when its repository is confirmed
archived on GitHub *right now* and the clone holds nothing that would be lost.

```sh
wb archive clean --filter acme
wb archive clean --apply
```

A clone is eligible only when **every** one of these holds:

- the repository's `isArchived` status is confirmed live against GitHub for
  this exact repository (never a name pattern, never a cached fleet listing)
- no uncommitted changes and no untracked files
- no stashes
- no unpushed commits on any local branch, not only the checked-out one
- no local-only branches — every local branch exists on `origin`
- no unpushed tags — every local tag exists on `origin`
- no linked worktrees registered against the clone
- no live WB task worktree or non-terminal Work Log claim recorded against it
- the clone is not marked `wb.skip-sync`

Any check that cannot be completed — GitHub unreachable, a ref that will not
resolve, a claim that cannot be read — makes that clone **not deletable**; it
is never treated as safe by default. Default is a dry-run plan; `--apply` is
required to delete anything. Every clone is reported, deletable or not, with
the exact reason — a skip is never summarized away as a bare count.

This is a purpose-built sibling of `wb sync`'s own archived-clone pruning
(which uses a looser check and mutates by default), not a mode of it: see
[spec/features/archived-clone-cleanup/README.md](../../../../spec/features/archived-clone-cleanup/README.md).
