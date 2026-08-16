# Inspect local Git state

Choose the noun that matches the question:

| Need | Command |
|---|---|
| One glance at fleet size and attention | `wb fleet` or `wb fleet overview` |
| Counts only | `wb fleet stats` |
| Fleet attention worklist | `wb fleet status` |
| One repository | `wb repo status` |

```sh
wb fleet --format json
wb fleet overview --format json
wb fleet stats --format json
wb fleet status --format json
wb repo status . --details --format json
```

`wb status` remains as a compatibility entry point: no path matches
`wb fleet status`, and a path matches `wb repo status`.

Fleet commands are local and read-only. They report modified, untracked,
conflicted, stashed, and unpushed state, plus inventory and managed-worktree
counts for stats/overview. Use `--details` only when individual entries are
needed; concise output saves context.

A fleet worklist reports only the repositories needing attention and gives the
number of clean ones as `hidden_clean`; a named repository is always reported,
clean or not. Reach for `--all` only when the clean repositories are the
question — listing a whole fleet costs context for rows with nothing to act
on.

For a durable result:

```sh
wb fleet status --filter <scope> --report-dir <dir> --format yaml
wb status <repository-path> --details --format yaml
```

Do not infer remote freshness from status; use `wb sync --dry-run` when GitHub
reconciliation is the actual question. For linked-worktree debt beyond managed
WB tasks, use `wb worktree orphans`.
