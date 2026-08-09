# Inspect local Git state

`wb status` is fleet-first when no path is supplied:

```sh
wb status
wb status --filter <owner-or-repository> --parallel 8
```

Supply a path for one repository; there is no `--fleet` flag:

```sh
wb status <repository-path> --details --format yaml
```

The command is local and read-only. It reports modified, untracked,
conflicted, stashed, and unpushed state. Use `--details` only when individual
entries are needed; concise output saves context.

A fleet run reports only the repositories needing attention and gives the
number of clean ones as `hidden_clean`; a named repository is always reported,
clean or not. Reach for `--all` only when the clean repositories are the
question — listing a whole fleet costs context for rows with nothing to act
on.

For a durable result:

```sh
wb status --filter <scope> --report-dir <dir> --format yaml
```

Do not infer remote freshness from status; use `wb sync --dry-run` when GitHub
reconciliation is the actual question.
