---
name: wb-worktrees
description: Use WB to guard a checkout or create and resume isolated feature worktrees. Use before editing a repository, creating a branch, coordinating one task across repositories, or recovering from an unsafe checkout.
---

# WB worktrees

Keep canonical clones clean, on `main`, and available for synchronization.
Make feature changes only below:

```txt
<projects-root>/.wb/worktrees/<task>/<owner>/<repository>
```

## Route

- Read [create.md](references/create.md) to start or resume a task.
- Read [guard.md](references/guard.md) to validate or recover a checkout.
- Use `$wb-change` when the task spans implementation, hooks, tests, and PRs.

## Fast path

```sh
wb worktree guard .
wb worktree create <task> --branch <prefix>/<task> <owner>/<repository>
wb worktree guard <printed-worktree-path>
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
