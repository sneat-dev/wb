# Guard and recover a checkout

For branch and worktree hygiene sweeps rather than one checkout's safety, read
[cleanup.md](cleanup.md): it routes `wb worktree orphans`, `wb worktree backfill`,
and `wb worktree cleanup` for historic leftovers across every repository.

Run before editing, committing, or pushing:

```sh
wb worktree guard .
git status --short --branch
```

A passing canonical clone is safe for synchronization and read-only use only.
A passing linked checkout is safe for feature work.

WB rejects:

- feature branches or changes in canonical clones;
- linked worktrees outside a resolver-recognized `.wb/worktrees` hierarchy —
  **unless** the worktree carries an active Work Log claim from
  `wb worktree adopt --apply` (see [cleanup.md](cleanup.md)). Adoption's whole
  point is never relocating the checkout, so guard resolves that one exact
  case from the worktree's own claim instead of its path, then applies every
  other check — admission included — unchanged;
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
preserve the branch, and run `wb worktree rescue <path>` to move any
uncommitted work onto a branch first. Restore
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

To inspect this checkout safely (identity and digests, no prompt bodies):

```sh
wb worktree info .
```

To hand an agent the exact original prompt and the ordered work log for this
checkout:

```sh
wb worktree log .
```

That dump is local private recovery context. Do not commit it or publish it.

WB's managed pre-commit hook requests `enforce`. A commit with no record of who
asked for it is what this exists to prevent, so declining it is the default
rather than an opt-in.

A fleet still adopting the journal can step back without editing hook policy:

```sh
WB_ADMISSION=warn git commit ...   # report instead of refusing
WB_ADMISSION=off  git commit ...   # skip the check entirely
```

A worktree adopted by `wb worktree backfill` has a manifest and no prompt,
because backfill never fabricates one. Its first commit therefore asks for the
instruction once, and then it is on record.

## Triage abandoned worktrees

`wb worktree orphans` explains every linked worktree reachable from the projects
root and recommends what to do with each. It is read-only.

```sh
wb worktree orphans                  # every family, with evidence
wb worktree orphans --only remove    # families whose work has all landed
wb worktree orphans --format json    # for scripting
```

Discovery goes through each canonical clone's own Git worktree registry, so it
sees all three layout generations at once — WB's current home, the legacy
`<projects-root>/.wb` hierarchy, and pre-WB checkouts living anywhere else.

Dispositions, always paired with their evidence:

- `remove` — the branch is already contained in the remote target, so nothing
  would be lost.
- `review` — uncommitted changes, or a registration whose working tree is gone.
- `decide` — unmerged and idle. WB will not decide this for you.
- `active` — committed recently; likely still in use.

Rows group by root effort, so `feature` and `feature.task-one` are one subject.
A family is recommended for removal only when every worktree in it has landed,
and `wb worktree cleanup` refuses a parent effort while any sub-effort is live.

To adopt existing worktrees into the journal:

```sh
wb worktree backfill            # dry run
wb worktree backfill --apply    # write reconstructed manifests
```

Backfill is additive and idempotent — it touches no working tree and is safe to
re-run after an interruption — so a fleet with live agents adopts without
stopping. It never fabricates a prompt.
