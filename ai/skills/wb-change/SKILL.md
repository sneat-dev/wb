---
name: wb-change
description: Deliver a safe code change through WB worktrees, fleet-standard hooks, local verification, and coordinated pull requests. Use for implementation work, especially when multiple repositories or paired provider and consumer PRs must pass together.
---

# WB change

Compose the low-level skills; do not restate their command details.

1. Confirm the required WB surface with `$wb-install`.
2. Identify every repository that needs edits and search for relevant open PRs
   or branches before creating another.
3. Use `$wb-worktrees` to create all editable checkouts in one task.
4. Use `$wb-hooks` to check or repair fleet-standard hooks.
5. Implement and run the smallest targeted checks, then one complete local
   pre-push path.
6. Push once per meaningful revision and let the managed pre-push hook run.
7. Open or update PRs, wait for required checks, and merge in dependency order.
8. After every coordinated PR merges, use `$wb-worktrees` to inspect the
   cleanup dry run, then apply safe task cleanup.

Read [completion.md](references/completion.md) before reporting a terminal
result. It is the canonical definition of done for this workflow; always state
whether the outcome is `implemented`, `published`, `landed`, or `blocked`.

Keep read-only sibling repositories on clean, freshly synchronized canonical
`main`. If a sibling needs any modification, include it in the task's WB
worktrees.

Read [paired-prs.md](references/paired-prs.md) when a provider change can break
its wired consumer or E2E harness.

Minimize work by reusing existing branches, worktrees, reports, and PRs.
Never bypass a guard or hook to save time: that only moves the failure to a
more expensive CI run.
