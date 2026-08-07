---
name: wb-worktrees
description: Use WB to guard, create, resume, inspect, or safely clean isolated feature worktrees. Use before editing or branching, when coordinating repositories, when checking task state, after pull requests merge, or when recovering from an unsafe checkout.
---

# WB worktrees

Keep canonical clones clean, on `main`, and available for synchronization.
Make feature changes only below the authoritative WB home:

```txt
~/.wb/worktrees/<task>/<owner>/<repository>
```

Set `WB_HOME` only when an explicit isolated home is intended. New work never
falls back to `<projects-root>/.wb`; without an explicit override, WB still
recognizes legacy linked worktrees there during migration.

## Route

- Read [create.md](references/create.md) to start or resume a task.
- Read [guard.md](references/guard.md) to validate or recover a checkout.
- Read [lifecycle.md](references/lifecycle.md) to inspect tasks or clean merged
  worktrees and branches.
- Use `$wb-change` when the task spans implementation, hooks, tests, and PRs.

## Fast path

```sh
wb worktree guard .
wb worktree create <task> --branch <prefix>/<task> <owner>/<repository>
wb worktree guard <printed-worktree-path>
wb worktree list <task>
```

Use `codex/`, `claude/`, or the active harness's required prefix. Use one task
slug and one creation command for a coordinated multi-repository change.

WB fast-forward pulls canonical `main` before branching. If a repository only
supplies read-only integration-test input, its clean, freshly synchronized
canonical checkout may be used. Create a worktree as soon as that repository
needs a modification.

Never substitute `git switch -c`, `git checkout -b`, or `git worktree add`.
Never reset, clean, stash, bypass hooks, or overwrite work to satisfy a guard.
If a WB command fails, inspect state before retrying; a hook may reject an
operation after Git has already changed state.
