# Synchronize canonical clones

`wb sync` can clone missing repositories, fast-forward clean clones, and prune
safe archived clones. Preview the exact scope:

```sh
wb sync --dry-run
wb sync --dry-run --org <owner>
```

Review planned removals and skipped dirty repositories, then repeat without
`--dry-run`:

```sh
wb sync --org <owner> --workers 8
```

Use repeatable sync-local `--org` (`-o`) to restrict owners. It differs from
the root `--org` used by other fleet commands. Use `--filter` for a repository
substring.

WB preserves dirty, stashed, conflicted, or unpushed repositories and reports
them for attention. Never clean or reset them merely to make sync pass.

Canonical clones should remain on their default branch. Make feature changes
through `$wb-worktrees`.
