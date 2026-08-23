# Change completion contract

This is WB's single, harness-neutral definition of done for an implementation
task. Codex, Claude Code, and any other client that invokes `$wb-change` use
this contract; a harness prompt must not restate or weaken it.

Do not call a change **done**, **complete**, or **delivered** without naming
one of these outcomes and the evidence that establishes it:

| Outcome | Required evidence |
|---|---|
| `implemented` | Scoped verification passed and the change is committed in its WB worktree. |
| `published` | `implemented`, plus the exact commit is pushed and its pull request is created or updated. |
| `landed` | `published`, required checks passed, the exact merge result is contained in `origin/main`, and the canonical checkout was reconciled cleanly. |
| `blocked` | A required check, receipt, permission, or external action is missing; state the blocker and the highest achieved outcome. |

Choose the outcome required by the request. “Implement” normally requires
`implemented`; “push”, “open a PR”, and “deliver for review” require
`published`; “merge”, “ship”, and “push to main” require `landed`.

Do not infer permission to push, open a pull request, merge, or release. If
the requested outcome needs authority the caller has not granted, report
`blocked` rather than presenting a lower outcome as finished.

Before reporting `landed`, use `$wb-worktrees` to reconcile the canonical
checkout and inspect the task cleanup plan. Cleanup only after the task meets
its own lifecycle rules.
