# wb — the workbench CLI

A Go CLI for running fleet-wide operations across **your** GitHub repositories
(those owned by your user or an org you belong to), and for keeping your local
`~/projects/{org}/{repo}` clones in sync with GitHub.

## Commands

```
wb sync            [flags]   # clone/pull/prune local clones to match GitHub, in parallel
wb sync-readme      [flags]   # ensure the dev-approach section is present & current
wb audit            [flags]   # read-only drift report (exits non-zero on drift)
wb specscore-lint   [flags]   # lint (and optionally --fix) every SpecScore-managed repo
```

### Persistent flags (all commands except noted)

| Flag | Default | Meaning |
|------|---------|---------|
| `--projects-root P` | `~/projects` | Root dir holding `{org}/{repo}` clones. |
| `--filter S` | — | Only process repos whose `org/name` contains `S`. |
| `--org O` | — | Query an additional GitHub owner (repeatable). **Not used by `sync`** — see below. |

### `wb sync`

Reconciles `~/projects/{org}/{repo}` with GitHub:

- non-archived, missing locally → clone
- non-archived, present locally → pull (skip if the working tree is dirty)
- archived, present + safe (clean, no stash, nothing unpushed) → remove
- archived, present + unsafe → keep, report why
- archived, missing → nothing

Runs against every repo owned by your GitHub account and every org you
belong to, in parallel, with a live progress UI (overall + per-org bars, a
live tail of in-flight repos). Anything left needing your attention (a hard
error, or a repo skipped/kept because it's dirty) opens an interactive
drill-down after the run — pick a repo to see exactly what's wrong
(modified/untracked/conflicted files, unpushed commits, stash entries).
Non-interactive runs (piped output, no TTY) print a plain summary instead
and skip the drill-down.

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--dry-run`, `-n` | off | Print the plan; change nothing. |
| `--workers`, `-j` | `8` | Max concurrent git/gh operations. |
| `--org`, `-o` | — (all your orgs + your account) | Only sync this org (repeatable). Restricts, rather than adds — unlike the persistent `--org` on the other commands. |

```sh
wb sync --dry-run              # preview
wb sync -o sneat-co            # sync only the sneat-co org
wb sync -j 16                  # more parallelism
```

### `--apply` / `--fix` (sync-readme / specscore-lint)

| Flag | Applies to | Default | Meaning |
|------|------------|---------|---------|
| `--apply` | `sync-readme` | off (dry-run) | Commit & push changes. Without it, only reports what would change. |
| `--fix` | `specscore-lint` | off (dry-run) | Run `specscore spec lint --fix` and commit & push. Without it, only reports each repo's violation count. |
| `--local-only` | `sync-readme` | off | Only process already-cloned repos; never clone remote-only repos. |

### `specscore-lint`

Lints every repo you own that is **SpecScore-managed** (carries a `specscore.yaml`
at its root); non-SpecScore repos are skipped. Requires the `specscore` CLI on
`PATH`.

- **dry-run** (default): runs `specscore spec lint` read-only in each repo and
  reports the violation count; exits non-zero if any repo has violations.
- **`--fix`**: runs `specscore spec lint --fix` in a detached worktree off the
  default branch and **lands** the result via the same direct-push / auto-merge-PR
  path as `sync-readme` (a PR when the local clone is dirty or the branch is
  protected, otherwise a direct push). In-progress local work is never disturbed.

## How `sync-readme` / `audit` / `specscore-lint` work

1. **Discover (in parallel):**
   - walk `~/projects/{org}/{repo}` for local clones;
   - list repos for **owners you control** — your `gh` user plus every org from
     `gh api user/orgs`, plus any `--org` — via `gh repo list`.
2. **Reconcile** the two sets by `org/name`. Repos found only on GitHub are
   cloned into `~/projects/{org}/{repo}` (on `--apply` only).
3. **Skip**: forks (so the section is never stamped into a fork of someone
   else's project), archived repos, repos with no Go/TS source, and any local
   clone that is **not** under one of your owners (third-party clones are never
   touched).
4. **Per repo**, in a detached worktree off the default branch: locate the
   `<!-- dev-approach:vN -->` … `<!-- /dev-approach -->` block in `README.md`
   and insert / replace / leave it based on the version marker.
5. **Land**: commit the change in the worktree, then:
   - if the local clone is **dirty** (uncommitted/unstaged changes or unpushed
     commits) **or** the default branch is **protected** → push to
     `wb/dev-approach` and open an auto-merge PR (your in-progress work
     is never disturbed);
   - otherwise → push directly to the default branch.

`sync-readme` is **dry-run by default** — it reports what would change and
writes nothing until you pass `--apply`. (`sync` is the opposite — apply by
default, `--dry-run` to preview — matching the old `sync-repos.sh`.)

## The dev-approach template

The canonical section lives in
[`internal/readme/dev-approach.md`](internal/readme/dev-approach.md) and is
embedded into the binary at build time. To change the content:

1. Edit `dev-approach.md`.
2. **Bump the version** in both markers, e.g. `v1` → `v2`. The tool replaces any
   section whose embedded version is lower than the template's, and leaves
   equal-or-newer sections alone. Editing content **without** bumping the
   version will not propagate updates to repos that already have the section.

## Build & run

```sh
cd wb
go build -o ~/.local/bin/wb ./cmd/wb   # install on PATH
go test ./...                          # run tests
wb audit                               # dry-run report
wb sync --dry-run                      # preview a fleet sync
```

## Adding a new operation

The discovery/reconcile/gitops plumbing is shared (`internal/discover`,
`internal/gitops`, `internal/fleetsync`). A new fleet command adds a `case`
in `cmd/wb` and reuses that plumbing; only the per-repo logic differs.

## Known limitation

Discovery keys on `org/name`. If a repo is cloned locally under a directory name
that differs from its GitHub org (e.g. `~/projects/dalgo/...` vs the `dal-go`
org), the mislabeled local copy is treated as local-only and skipped, and the
correctly-named repo is cloned fresh under `~/projects/<org>/` on `--apply` /
during `sync`. Use matching org directory names to avoid duplicate clones.
