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

The built-in Go and Node pre-push blocks are tiered by what is actually being
pushed, via `wb hooks push-tier` (the base/worktree/custom/metrics Tier 0
layer above always runs first, regardless of tier):

- **Tier 0** (always, sub-second): base `git diff --check`, worktree
  admission, canonical-clone guard, custom policy, metrics. Never skippable.
- **Tier 1** (`go vet` / `lint`): runs on any push that is not a pure
  remote-ref deletion (40- or 64-zero-SHA) and not confined to the
  `refs/wb/checkpoints/*` checkpoint namespace.
- **Tier 2** (`go test` / `test`): runs only on a *publication* push — the
  default branch, a tag, or a branch with an open pull request. An unresolved
  PR status (no network, or the bounded `gh` lookup timed out or missed)
  degrades to Tier 1, never silently up to Tier 2; CI is the real gate for a
  publication push either way.

Every invocation prints one line naming the tier and the reason. There is no
Git-hook-bypass escape hatch: Tier 0 is mandatory on every push, including a
`refs/wb/checkpoints/*` checkpoint push (see `wb worktree log checkpoint` in
the wb-worktrees skill), which is Tier 0 only by classification, not by
skipping the hook. General secure-hook cache and durable metrics authority is
tracked in WB issue #61; do not treat this tiering as that broader fix.

After editing policy:

```sh
wb hooks check .
wb hooks repair .
wb hooks check .
```
