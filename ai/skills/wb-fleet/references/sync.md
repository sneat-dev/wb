# Synchronize canonical clones

`wb sync` clones missing repositories and fast-forwards clean ones. An
archived repository is treated exactly like any other by default — pulled if
present, never deleted. Preview the exact scope:

```sh
wb sync --dry-run
wb sync --dry-run --org <owner>
```

Pass `--prune-archived` to additionally delete a local clone whose repository
is confirmed archived on GitHub, but only when it passes the exact same
safety predicate `wb archive clean` uses (live-confirmed archived status, no
uncommitted/untracked changes, no stash, no unpushed commits on any branch, no
local-only branch, no unpushed tag, no linked worktree, no non-terminal WB
Work Log claim, not marked `wb.skip-sync`):

```sh
wb sync --prune-archived --dry-run
wb sync --prune-archived
```

Without `--prune-archived`, an archived repository's clone still shows up in
the report (never silently indistinguishable from an ordinary one) — it is
simply pulled or left alone, never removed.

Review planned removals and skipped dirty repositories, then repeat without
`--dry-run`:

```sh
wb sync --org <owner> --parallel 8
```

Use repeatable sync-local `--org` (`-o`) to restrict owners. It differs from
the additive root `--org` used by most other fleet commands; specifically for
sync, either `wb --org acme sync` or `wb sync --org acme` restricts owners so
both advertised positions are effective. Use `--filter` for a repository substring.

Never run `git clone` directly into `<projects-root>/<repository>`. Canonical
clones are owned by WB at `<projects-root>/<owner>/<repository>`; use `wb sync`
so the owner segment and remote identity are deterministic. A misplaced
top-level clone is not a canonical WB clone and must be moved/recloned through
the approved owner/repository layout before worktree creation.

WB preserves dirty, stashed, conflicted, or unpushed repositories and reports
them for attention. Never clean or reset them merely to make sync pass.

Canonical clones should remain on their default branch. Make feature changes
through `$wb-worktrees`.
