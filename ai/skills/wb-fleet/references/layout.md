# Clone layout

Canonical clones live at `{projects-root}/{owner}/{repository}` with a real
`.git` directory. Linked worktrees are ignored.

```sh
wb layout audit --format json
wb layout clean
wb layout clean --apply
```

`audit` reports:

- `ok` — path matches origin
- `top_level` — clone sits directly under the projects root
- `misowned` — path owner/repo does not match origin
- `no_origin` / `unreadable` — cannot classify safely

`clean` only considers `top_level` clones. It requires a clean working tree and,
by default, an existing canonical copy. Pass `--apply` to delete; default is
dry-run. Use `--allow-missing-canonical` only when removing the sole local copy
is intentional.

Fleet rollups include layout counts automatically:

```sh
wb fleet stats --format json
```
