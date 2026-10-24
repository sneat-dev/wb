# Worktrees, isolation and the writer lease

## The three modes

| Mode | Flag | Behaviour |
|---|---|---|
| `auto` | `--worktree auto` (default) | creates a new worktree through `wb worktree create`'s own code |
| `current` | `--worktree current` | runs in the invoking checkout |
| path | `--worktree /absolute/path` | runs in an existing Git worktree |

`--new-worktree <name>` and `--use-worktree <name>` remain for compatibility and
for callers that already name a WB worktree.

An `auto` worktree is named `<task-type>-<task hash>-<run id>`, so re-running the
same task never resumes the previous checkout by accident.

## Refusals that protect you

- A canonical clone (`.worktree.md` says `writable: false`) is refused unless
  `--allow-canonical`. Create an isolated checkout instead.
- A dirty checkout is refused unless `--allow-dirty`. WB never cleans or resets
  it; the run records the paths that were already dirty and a
  `git stash create` snapshot, so `wb agent diff` still shows only the worker's
  change.

## One writer per worktree

Every writable execution holds an inherited `flock` on the symlink-resolved
worktree path. The dispatcher takes it, and the descriptor is passed to the
detached owner, then to the harness and each validation step. The kernel releases
it when the last of those processes exits.

Consequences:

- a second `run` into a busy worktree fails immediately and names the owning run;
- a killed owner does **not** free a worktree whose worker is still writing;
- a stray background process left by a worker keeps the lease until it exits —
  use `wb agent stop` to terminate the whole group;
- on Windows the lock is unavailable, so `current` and path runs are refused
  while unique-worktree modes keep working.

## Parallel work

`compare` and repeated `run` invocations each get their own worktree. Anything
that needs the same checkout runs one at a time, and the lease enforces it.
