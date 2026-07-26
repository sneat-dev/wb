---
name: wb-hooks
description: Install, inspect, repair, and measure WB-managed Git hooks. Use when a repository needs fleet-standard pre-commit or pre-push checks, hook drift is reported, worktree policy must be enforced, or local hook cost and failures need diagnosis.
---

# WB hooks

Repository policy lives in `.wb/hooks.yaml`; optional user policy lives in
`~/.config/wb/hooks.yaml`. WB owns managed shim blocks while preserving local
commands around them.

## Route

- Read [manage.md](references/manage.md) to install, check, or repair hooks.
- Read [policy.md](references/policy.md) to add profiles or templates.
- Read [metrics.md](references/metrics.md) to diagnose hook time and failures.

## Fast path

```sh
wb hooks check .
wb hooks install .
wb hooks check .
```

Use `repair` when managed hooks are stale or missing:

```sh
wb hooks repair .
```

Do not hand-edit generated WB shim blocks. Do not use `--force` until the
reported unmanaged hook or `core.hooksPath` has been inspected; repair backs
up conflicts, but replacement is still an explicit decision.

For a non-default projects root, pass the same `--projects-root` to every WB
command. Let Git invoke hidden `wb hooks run`; it is an internal dispatcher,
not the normal way to test policy.
