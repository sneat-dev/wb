---
name: wb-worktrees
description: Use WB to guard, create, resume, inspect, or safely clean isolated feature worktrees. Use before editing or branching, when coordinating repositories, when checking task state, after pull requests merge, or when recovering from an unsafe checkout.
---

# WB worktrees

Keep canonical clones clean and available for synchronization; prefer `main`,
but WB creation leaves any clean currently checked-out branch untouched.
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
- Read [lifecycle.md](references/lifecycle.md) to inspect live tasks,
  finalize merged work, recycle caches safely, or abort interrupted claims.
- Consult [capabilities.json](../../capabilities.json) before assuming a WB
  surface exists; execute only commands whose runtime evidence is present.
- Use `$wb-change` when the task spans implementation, hooks, tests, and PRs.

## Fast path

```sh
wb worktree guard .
wb worktree create <task> --branch <prefix>/<task> <owner>/<repository> \
  --effort <effort> --run <run> --agent <agent> --agent-runtime <runtime> --model <model> \
  --original-prompt-file <private-prompt-file>
wb worktree guard <printed-worktree-path>
wb worktree list <task>
```

Use `codex/`, `claude/`, or the active harness's required prefix. Use one task
slug and one creation command for a coordinated multi-repository change.

WB fetches and pins `origin/main` before branching without switching or
fast-forwarding the canonical checkout. If a repository only supplies read-only integration-test input, its clean, freshly synchronized
canonical checkout may be used. Create a worktree as soon as that repository
needs a modification.

Before create, write the exact originating request to a readable non-empty
0600 private file outside source Git. Every create requires that file and has
a private Hybrid Work Log. Its per-repository claim is under
`<WB_HOME>/worklogs/<effort>/runs/<run>/claims/`, while the tiny local
`.wb-worklog/recovery.json` projection has no prompt/history and its
`/.wb-worklog/` directory is locally Git-excluded. Pass
`--original-prompt-file` with the exact private original prompt; never put
prompt text in a repository or command argument. The local outbox preserves
recovery evidence while Synchestra is unavailable, so local create, seal, and
cleanup do not wait for a server. It is not the planned Git-repository
communication fallback and does not deliver messages to agents.

One agent is the concurrent writer for each worktree/branch. Helpers are
read-only and return patches/findings. Inspect upstream state explicitly and
never auto-stash, reset, rebase, merge, or rewrite dirty work. Do not invent a
command absent from the capability manifest and built-in help.

Never substitute `git switch -c`, `git checkout -b`, or `git worktree add`.
Never reset, clean, stash, bypass hooks, or overwrite work to satisfy a guard.
If a WB command fails, inspect state before retrying; a hook may reject an
operation after Git has already changed state.
