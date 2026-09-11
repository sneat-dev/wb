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
PreToolUse payload on stdin and carries these policies:

- **Canonical-clone write** — refuses a tool call that would write inside
  `<projects-root>/<owner>/<repository>`, naming `wb worktree create` as the
  remedy.
- **Hook bypass** — refuses `--no-verify`/`-n` on `git commit`/`push`/`merge`,
  `git -c core.hooksPath=…`, and `git config core.hooksPath`, in any
  WB-managed checkout (canonical clone or linked worktree, not only the
  former). A red hook is informational output about real risk, never an
  obstacle; `wb worktree rescue --push` is the sanctioned recovery path and is
  never itself refused.
- **Auto-tagging** — refuses a hand-pushed `git tag`/`git push --tags`/
  `git push origin <tag>` in a repository whose own CI already tags it: an
  explicit `agent.autoTags: true` in `.wb/hooks.yaml` (or the global hooks
  policy), or a `strongo/cicd` reusable workflow with no
  `disable-version-bumping: true` beside it.
- **Governed heavy validation** — redirects CPU-heavy validation inside a
  WB-managed worktree to the governed command gateway (see below).
- **Missing model** (`Agent`/`Task` tool) — refuses a subagent dispatch that
  names no `model`; an omitted model silently inherits the parent's.
- **Literal report path** (`Agent`/`Task` tool) — refuses a dispatch prompt
  that hand-writes a WB report path (e.g. `$HOME/.wb/reports/...`) instead of
  deriving it from `wb worktree log finalize --report`.
- **Dispatch into a live claim** (`Agent`/`Task` tool) — refuses a dispatch
  naming a repository another live WB claim already covers, read locally from
  the repository's own `.worktrees/` manifests and the local wb-state mirror
  (no network). A dispatch from inside the claimed worktree itself is treated
  as that lane continuing its own work, not a second claim.
- **Land with the WB verb** — refuses `gh pr merge` under any flags, chained
  with `&&`/`;`, subshelled with parentheses or piped, and wrapped in
  `bash -c`/`sh -c`/`zsh -c` (recursed into the payload at a bounded depth,
  so a payload that itself wraps another `bash -c` is still caught), naming
  `wb worktree land`/`wb land` and `wb pr land <owner/repo#n>` instead (rule
  `land-with-wb-verb`, `sneat-co/backstage`). Prefixing that exact call's own
  words with `WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>"` (directly, or via
  `env`) is the explicit, recorded escape hatch for that one call; the hook
  process's own ambient environment is never read, so a value set ahead of
  time cannot silently cover a whole session. `gh pr view`/`checks`/`list`
  and every other read-only `gh` subcommand are never refused.

A read-only `--help`/`-h`/`help` invocation of `specscore`, `go`,
`npm`/`pnpm`/`yarn`/`bun` is never refused for that reason alone — it prints
help and does nothing else, regardless of what verb also appears on the line
(wb#493). `gh pr merge`'s own `--help`/`-h` recognition is separate and
value-flag aware: `gh pr merge 123 --subject --help` is a real merge, because
`--subject` takes the next token unconditionally as its value and never sees
`--help` as a flag; only a bare, unconsumed `--help`/`-h` is ever treated as
help.

Register it once per machine:

```sh
wb hooks agent install
```

Re-running `install` is idempotent, including across a policy rollout: if an
already-registered entry's matcher is narrower than the current policy set
needs (e.g. an install from before the `Agent`/`Task` policies existed), it
widens the matcher in place rather than leaving it stale or adding a
duplicate entry.

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
