# Inspect and clean worktree tasks

## Inspect

Use the offline default for a fast view of every WB-managed worktree:

```sh
wb worktree list
wb worktree list <task> --format json
wb worktree list --format json # active/recent cleanup backlog inventory
```

Add GitHub PR evidence only when it affects the decision:

```sh
wb worktree list <task> --github
```

Do not replace this with recursive Git loops. WB validates that each path is a
real linked worktree belonging to its expected canonical clone, stops at Git
repository boundaries, and reports malformed candidates without hiding valid
siblings. Default-mode inventory includes legacy `<projects-root>/.wb`
worktrees during migration.

Treat a `merged` result as cleanup-ready, not done. A feature is done only
after it is merged to `main` **and** every related worktree/branch has an
applied removal or audited recycle. A task has the same rule after integration
to its feature branch. A validated or pushed branch is never done.

## Plan cleanup

After all coordinated PRs merge, always inspect the dry run:

```sh
wb worktree cleanup <task>
wb worktree cleanup --all-merged
```

The default 24-hour `--older-than` window reduces races with recent merges.
Use `--older-than 0` only when immediate removal is intentional.

WB skips the whole task if any member is locked, dirty, has an open PR, lacks
a merged PR for the exact branch head and base, or has an advanced remote
branch. Preserve skipped work and report the reason; do not reset, clean,
stash, or delete it manually.

## Apply

Apply only after reading the plan:

```sh
wb worktree cleanup <task> --apply
```

This removes the linked worktree and its exact local branch ref. WB rechecks
safety immediately before mutation and writes an audit report below the
authoritative WB home, normally `~/.wb/reports/worktree-cleanup/`.

Remote deletion is deliberately separate:

```sh
wb worktree cleanup <task> --apply --remote
```

WB uses force-with-lease against the previously observed SHA. Omit `--remote`
when another actor or follow-up may still need the published branch.

## Recycle only explicit caches

Recycling is an optimization, never a way to hide unfinished work. First use
the normal cleanup path when the work is merged. To reuse a clean, unlocked
worktree for a new task, plan then apply a rename:

```sh
wb worktree rename <finished-task> <next-task> --preserve-cache node_modules
wb worktree rename <finished-task> <next-task> --apply \
  --preserve-cache node_modules --effort <new-effort> --run <new-run> \
  --agent <new-agent> --agent-runtime <runtime> --model <model>
```

`--preserve-cache` is an allow-list. Any other ignored/untracked path blocks
recycle with a precise diagnostic; archive or intentionally remove it outside
the worktree before retrying. WB seals the old local Work Log/outbox before
the move, resets the Git-excluded projection, and creates a fresh run claim
after fetching the new base. Never copy a prior task's projection, prompt, or
source state into the new task.

## Abort instead of abandoning

An unused or interrupted worktree has no merged PR, so `cleanup` must refuse
it. Do not delete it manually. Inspect an explicit disposition first:

```sh
wb worktree abort <task> --disposition handoff
wb worktree abort <task> --disposition not_landed --apply
wb worktree abort <task> --disposition discarded --apply
```

`handoff` and `not_landed` seal/archive/outbox the Work Log and keep the clean
worktree/branch resumable. `discarded --apply` is the explicit authorization to
seal first, then remove a clean unlocked worktree and its exact local branch.
Run `wb worktree list` afterwards and resolve every remaining recent/active
entry; the normal terminal state is zero cleanup backlog, not a pile of
apparently-finished branches.
