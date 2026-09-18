# Clone layout

Canonical clones live at `{projects-root}/{host}/{owner}/{repository}` with a
real `.git` directory, where `{host}` is the literal forge hostname
(`github.com`). The local path inverts to the remote URL with no configuration
read: `{projects-root}/github.com/dal-go/dalgo` is
`https://github.com/dal-go/dalgo`. Linked worktrees are ignored.

```sh
wb layout audit --format json
wb layout clean
wb layout clean --apply
wb layout migrate
wb layout migrate --apply
```

`audit` reports, for every clone it inspects, the remote URL its path
corresponds to (`remote_url` in `--format json`/`yaml`): a clone at the host
level inverts to `https://{host}/{owner}/{repository}` by path arithmetic alone,
without reading any configuration or repository remote, while a legacy
two-level clone reports the host its origin already names — the host level it
must move under.

- `ok` — path matches origin
- `top_level` — clone sits directly under the projects root
- `misowned` — path does not match origin, or a host level is missing its
  `{owner}/{repository}`
- `bad_host` — a first-level entry that is not a valid hostname carries clones
  at the legacy `{owner}/{repository}` placement; they are still read in place
  and never removed
- `no_origin` / `unreadable` — cannot classify safely

`clean` only considers `top_level` clones. It requires a clean working tree and,
by default, an existing canonical copy. Pass `--apply` to delete; default is
dry-run. Use `--allow-missing-canonical` only when removing the sole local copy
is intentional. A legacy `{owner}/{repository}` first level is never treated as
a removable top-level clone.

`migrate [owner/repo...]` moves each legacy `{owner}/{repository}` clone under
the root to its host-level `{host}/{owner}/{repository}` placement, taking
`{host}` from the clone's `origin` remote, and repoints every linked worktree
Git has registered against it — one inside the clone (which moves with it) and
one anywhere else (which is repointed in place) alike. Dry-run by default; pass
`--apply` to move. With no arguments every legacy clone under the root is
covered; name `owner/repository` arguments to migrate only those. A clone
already at the host level is re-verified — its worktree registration must have
no missing or prunable entry — before being reported `already_done` and left
untouched; a stranded one found broken (a manual rename, or an earlier
migration interrupted before repair) is repaired and reported `repaired`, or
`failed` naming what is still wrong. This is how an interrupted or repeated
`--apply` finishes the job.

A clone is `skipped`, with a reason, when it has no usable origin, its origin
owner/repository differs from its path, its origin host is not a valid
directory name, its destination already exists, a Git operation (merge,
rebase, cherry-pick, revert, or a held index lock) is in progress or cannot be
inspected, a live Work Log claim holds it or a linked worktree (checked across
every home WB resolves, including a retired legacy one), or an un-picked-up
`wb session park` bundle names it or a linked worktree as a member. Uncommitted
changes are never a refusal reason — the rename preserves them, and neither is
an unborn `HEAD` (a repository with no commit yet). The command exits with the
findings code whenever any clone is skipped or fails.

`--apply` writes a manifest under `<root>/.wb/layout-migrations/<id>/` before
its first move; `--undo <id>` reverses every clone that manifest records done,
with the same repair and verification. It also invalidates WB's cached
repository-path index and reports when a running daemon must be restarted to
see the moved paths.

```sh
wb layout migrate --apply
wb layout migrate --undo 20260918T120000Z-ab12cd34
```

Fleet rollups include layout counts automatically:

```sh
wb fleet stats --format json
```
