# Guard and recover a checkout

Run before editing, committing, or pushing:

```sh
wb worktree guard .
git status --short --branch
```

A passing canonical clone is safe for synchronization and read-only use only.
A passing linked checkout is safe for feature work.

WB rejects:

- feature branches or changes in canonical clones;
- linked worktrees outside a resolver-recognized `.wb/worktrees` hierarchy;
- base branches or arbitrary detached HEADs in linked worktrees;
- mismatched projects roots.

If the canonical clone is dirty or on a feature branch, stop and report the
exact state. Do not reset, clean, stash, switch, or overwrite automatically.

During a real Git rebase, a linked checkout can be temporarily detached. WB
permits that state only while its own Git directory has active `rebase-merge`
or `rebase-apply` state; finish or abort the rebase rather than treating a
general detached checkout as safe.

Git has no pre-checkout hook. WB's `post-checkout` guard therefore reports an
unmanaged checkout only after Git has made it, then exits successfully to avoid
leaving a deceptive half-failed `git checkout && ...` flow. Inspect and
preserve the branch; `wb worktree rescue` is not available yet. Restore
canonical `main` only when doing so is known to be safe. `pre-commit` and
`pre-push` remain blocking boundaries.

Use `wb hooks check .` to confirm the guard is enforced by `post-checkout`,
`pre-commit`, and `pre-push`. Use `$wb-hooks` for installation or repair.
