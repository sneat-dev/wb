# Inspect and clean worktree tasks

## Inspect

Use the offline default for a fast view of every WB-managed worktree:

```sh
wb worktree list
wb worktree list <task> --format json
```

Add GitHub PR evidence only when it affects the decision:

```sh
wb worktree list <task> --github
```

Do not replace this with recursive Git loops. WB validates that each path is a
real linked worktree belonging to its expected canonical clone.

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
safety immediately before mutation and writes an audit report below
`<projects-root>/.wb/reports/worktree-cleanup/`.

Remote deletion is deliberately separate:

```sh
wb worktree cleanup <task> --apply --remote
```

WB uses force-with-lease against the previously observed SHA. Omit `--remote`
when another actor or follow-up may still need the published branch.
