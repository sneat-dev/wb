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

## Commit admission

`--admission` additionally requires a managed worktree to carry its own record:
a `.wb/local/manifest.yaml` and at least one recorded instruction under
`.wb/local/prompts/`.

```sh
wb worktree guard . --admission warn      # report what would be refused
wb worktree guard . --admission enforce   # refuse the commit
```

Admission binds on the worktree's location. It never inspects environment
markers to tell an agent from a human, because a marker that can be absent — a
subshell, a wrapper, a script — fails open exactly when it matters.

When it refuses, record what you were asked to do:

```sh
wb worktree set --prompt="..."
```

That appends a `human_declared` prompt at the next ordinal and, for a worktree
created before the journal existed, reconstructs a manifest from Git evidence
and labels every inferred field. It records rather than bypasses: unblocking the
commit is itself the record of who directed the work. There is no bypass flag.

Adopt with `warn` first. A fleet running unattended agents cannot verify that it
has stopped them, so enforcement must never be a flag day.
