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

# Read the issues report

Every `wb sync` writes `~/.wb/last-sync-issues.md` (or
`$WB_HOME/last-sync-issues.md`). It lists only the repositories that need
attention plus the errors — never the successful ones — with the local clone
path, the exact state, read-only inspection commands, and the resolution
options for each.

```sh
cat ~/.wb/last-sync-issues.md
```

The path is stable and the file is overwritten every run, so it always
describes the most recent sync and never a stale one. A clean run still writes
it, saying explicitly that there are no issues; a run that failed before
scanning reports that failure instead, because broken GitHub authentication
leaves every clone unmanaged.

Read it before deciding what to fix. Run the inspection commands before any
resolution command: the inspect commands are read-only and safe as-is, while
the resolution options are choices to make after reading their output, not a
script to run top to bottom.

## Check the report is not stale before acting

The report states its own scope on every run: whether it covered every visible
owner, or was restricted by `--org`/`--filter`. A restricted run never claims
the fleet is in sync, and a run that finished fewer repositories than it
selected is marked `**Incomplete:**`. Read those lines before treating the file
as a fleet-wide picture — it is overwritten by every run, including scoped ones.

Each entry records `**HEAD when reported:**`, and its first inspection command
re-reads HEAD:

```sh
git -C <clone> rev-parse HEAD   # must equal <recorded sha>, or this entry is stale
```

Run it first. If HEAD has moved, the finding was made against a different
commit and may no longer hold — re-run `wb sync` rather than acting on it. This
matters most for the destructive options (resetting a clone to its upstream,
discarding commits): those are unrecoverable if the repository changed after
the report was written.
