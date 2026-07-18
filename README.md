# all-repos

A Go CLI for running fleet-wide operations across **your** GitHub repositories
(those owned by your user or an org you belong to). It reconciles your local
`~/projects/{org}/{repo}` clones with what GitHub reports, then applies an
operation to each repo that contains Go or TypeScript source.

The first operation keeps the **"Our approach to development"** section in each
repo's root `README.md` in sync with a single canonical template.

## Commands

```
all-repos sync-readme    [flags]   # ensure the dev-approach section is present & current
all-repos audit          [flags]   # read-only drift report (exits non-zero on drift)
all-repos specscore-lint [flags]   # lint (and optionally --fix) every SpecScore-managed repo
```

### Flags

| Flag | Applies to | Default | Meaning |
|------|------------|---------|---------|
| `--apply` | `sync-readme` | off (dry-run) | Commit & push changes. Without it, only reports what would change. |
| `--fix` | `specscore-lint` | off (dry-run) | Run `specscore spec lint --fix` and commit & push. Without it, only reports each repo's violation count. |
| `--local-only` | `sync-readme` | off | Only process already-cloned repos; never clone remote-only repos. |
| `--filter S` | all | — | Only process repos whose `org/name` contains `S`. |
| `--org O` | all | — | Query an additional GitHub owner (repeatable). |
| `--projects-root P` | all | `~/projects` | Root dir holding `{org}/{repo}` clones. |

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

## How it works

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
     `all-repos/dev-approach` and open an auto-merge PR (your in-progress work
     is never disturbed);
   - otherwise → push directly to the default branch.

`sync-readme` is **dry-run by default** — it reports what would change and
writes nothing until you pass `--apply`.

## The template

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
cd all-repos
go build -o ~/.local/bin/all-repos ./cmd/all-repos   # install on PATH
go test ./...                                         # run tests
all-repos audit                                       # dry-run report
all-repos sync-readme --apply --filter myrepo         # apply to one repo first
```

Recommended workflow: run `audit` (or `sync-readme` with no `--apply`) to review
the plan, try `sync-readme --apply --filter <one-repo>` on a single repo, then
drop the filter to roll out fleet-wide.

## Adding a new operation

The discovery/reconcile/land plumbing is shared. A new fleet command (e.g.
`sync-file` for `CLAUDE.md`/`renovate.json`, or other audits) adds a `case` in
`cmd/all-repos/main.go` and reuses `internal/discover` and `internal/gitops`;
only the per-repo mutator differs.

## Known limitation

Discovery keys on `org/name`. If a repo is cloned locally under a directory name
that differs from its GitHub org (e.g. `~/projects/dalgo/...` vs the `dal-go`
org), the mislabeled local copy is treated as local-only and skipped, and the
correctly-named repo is cloned fresh under `~/projects/<org>/` on `--apply`. Use
matching org directory names to avoid duplicate clones.
