---
name: wb-hooks
description: Install, inspect, repair, and price WB-managed Git hooks, including the cheap commit profile and the stream-branch push that defers to CI. Use when a repository needs fleet-standard pre-commit or pre-push checks, hook drift is reported, worktree policy must be enforced, or local hook cost and failures need diagnosis.
---

# WB hooks

Repository policy lives in `.wb/hooks.yaml`; optional user policy lives in
`~/.config/wb/hooks.yaml`. WB owns managed shim blocks while preserving local
commands around them.

## Route

- Read [manage.md](references/manage.md) to install, check, or repair hooks.
- Read [policy.md](references/policy.md) to add profiles or templates.
- Read [metrics.md](references/metrics.md) to diagnose hook time and failures,
  and to price the profiles with `wb hooks measure`.

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

## Agent tool-call guard

Git hooks judge a commit. They cannot see the write that never reaches one,
and a canonical clone is ruined by the write: a `git checkout -- .` that
discards an unlanded lesson never commits anything.

`wb hooks agent pre-tool-use` closes that gap. It reads a Claude Code
PreToolUse payload on stdin and refuses a tool call that would write inside
`<projects-root>/<owner>/<repository>`, naming `wb worktree create` as the
remedy. Register it once per machine:

```sh
wb hooks agent install
```

It fails open without exception — an unreadable payload, an unknown tool, a
shell construct it cannot model, and a WB too old to know the subcommand all
allow the call. It leaves `git fetch`, `git merge --ff-only`, `git status`, and
`git log` alone inside a canonical clone.

Inside a WB-managed worktree, the same hook redirects CPU-heavy validation to
the governed command gateway. Agents run `go test`, `go vet`, `go build`, and
common Node/Rust test, build, lint, and E2E commands as:

```sh
wb run -- go test ./internal/worktrees
```

That boundary gives validation an operation ID and privacy-safe timing receipt,
and lets a local scheduler queue or coalesce it. Run `gofmt` and Prettier
directly on edited files; immediate formatting is deliberately outside the
queue. Unmanaged worktrees and human shells are unaffected because this is an
agent PreToolUse policy, not a shell wrapper.

Rehearse a decision against a saved payload without a pipe:

```sh
wb hooks agent pre-tool-use --input payload.json
```
