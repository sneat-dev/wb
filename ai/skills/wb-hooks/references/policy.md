# Hook policy

Policy layers are conservative WB defaults, user
`~/.config/wb/hooks.yaml`, then repository `.wb/hooks.yaml`.

Use built-in detection when conventional checks are wanted:

```yaml
version: 1
profiles:
  auto: true
```

Worktree protection is installed by default for every repository:

```yaml
version: 1
profiles: {}
```

The worktree profile guards `post-checkout`, `pre-commit`, and `pre-push`.
Git has no pre-checkout hook: post-checkout warns loudly after an unmanaged
checkout has happened, preserves it for inspection, and exits successfully.
`pre-commit` and `pre-push` are the blocking enforcement points. Preserve the
state; `wb worktree rescue` is not available yet.

Only when WB cannot own checkout policy, make the exception explicit and
auditable, then repair hooks:

```yaml
version: 1
profiles:
  exclude: [worktree]
```

Custom profiles should detect a stable repository marker and contribute the
smallest required hook:

```yaml
version: 1
profiles:
  definitions:
    product:
      order: 200
      detect:
        any_files:
          - product.yaml
      hooks:
        pre-push:
          template: templates/product/pre-push.sh
```

Resolve relative templates from the YAML file declaring them. Keep expensive
E2E work in `pre-push`, not `pre-commit`. Prefer one orchestrating command that
reuses results over multiple overlapping blocks.

After editing policy:

```sh
wb hooks check .
wb hooks repair .
wb hooks check .
```
