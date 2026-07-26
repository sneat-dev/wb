# Hook policy

Policy layers are conservative WB defaults, user
`~/.config/wb/hooks.yaml`, then repository `.wb/hooks.yaml`.

Use built-in detection when conventional checks are wanted:

```yaml
version: 1
profiles:
  auto: true
```

Force worktree protection for agent-edited repositories:

```yaml
version: 1
profiles:
  include:
    - worktree
```

The worktree profile guards `post-checkout`, `pre-commit`, and `pre-push`.
Git has no pre-checkout hook, so inspect state after a rejected checkout.

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
