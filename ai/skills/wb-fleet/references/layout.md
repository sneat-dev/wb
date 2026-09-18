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

Fleet rollups include layout counts automatically:

```sh
wb fleet stats --format json
```
