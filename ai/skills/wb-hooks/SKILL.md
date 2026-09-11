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
- **Land with the WB verb** — refuses every call that `gh` itself would run
  as `pr merge`, naming `wb worktree land`/`wb land` and
  `wb pr land <owner/repo#n>` instead (rule `land-with-wb-verb`,
  `sneat-co/backstage`). It reads gh's arguments the way gh's own command
  lookup does, so a flag before `pr` or between `pr` and `merge` does not
  hide the call (`gh pr -R o/r merge 1`, `gh pr --squash 1 merge`), while
  `gh help pr merge`, `gh search issues pr merge` and `gh pr merge --help`
  are allowed. It finds the call:
  - chained with `&&`, `||`, `;`, `|` or a newline, or inside `( )`, `{ }`
    or an unquoted `$( )`;
  - in an `if`/`elif`/`while`/`until` condition, a `then`/`else` branch, a
    loop's `do` body, or after `!` or `coproc`;
  - behind `VAR=value` assignments, and behind `sudo`, `env`, `nice`,
    `nohup`, `time`, `stdbuf`, `exec`, `command`, `builtin`, `noglob`,
    `nocorrect`, `timeout`, `caffeinate`, `xargs` and `wb run --`, together
    with each wrapper's own options and their values;
  - in the `-c` payload of `bash`, `sh`, `zsh`, `dash` and `ksh`, taken the
    way that shell takes it (the first word after its option words, so
    `bash -c -e '…'`, `bash -o pipefail -c '…'` and `bash -c -- '…'` count),
    recursing up to a depth of 8.

  **Not inspected, so still allowed:**
  - `gh api` merge routes: the REST `PUT …/pulls/<n>/merge` and the GraphQL
    `mergePullRequest` mutation;
  - `gh` aliases and extensions;
  - scripts run from files, `eval`, here-strings, backticks, a quoted
    `"$( … )"`, ANSI-C `$'…'` quoting and `env -S`;
  - other interpreters (`python3 -c`, `node -e`) and wrappers not listed
    above (`ssh`, `watch`);
  - `git push` to the base branch, and `hub merge`.

  **Escape hatch:** put `WB_AGENTGUARD_ALLOW_GH_PR_MERGE="<reason>"` on
  that exact call, where the shell really puts it into gh's environment.
  That means at the start of the call, or right after `env`, `sudo`, `time`
  or a shell keyword. It lets that one call through, and the guard records
  the reason when it allows the call. The record proves only that the guard
  allowed the call, not that the call ran. The hook process's own ambient
  environment is never read, so a value set ahead of time cannot silently
  cover a whole session. This policy refuses nothing else: `gh pr
  view`/`checks`/`list` and every other gh command pass it.

A `specscore`/`go`/`npm`/`pnpm`/`yarn`/`bun` invocation is never refused for
naming a write verb when its own words are shaped as a genuine help request
(wb#493), but which shape counts depends on the tool:

- `specscore` is cobra-based, so its own trailing `--help`/`-h` always prints
  help no matter how many subcommand words precede it: a subcommand chain
  with no other flag at all, trailing in exactly one `--help`/`-h` and
  nothing after it (`specscore feature change-status --help`), or the
  literal word `help` first with no flag anywhere in the rest (`specscore
  help feature change-status`).
- `go`/`npm`/`pnpm`/`yarn`/`bun` get only the bare top-level shape — exactly
  `<tool> --help`/`<tool> -h` with nothing else after the program name
  (`go --help`, `pnpm -h`), or `<tool> help` followed by zero or more
  non-flag words (`go help build`, `npm help install`) — because each of
  these five has at least one subcommand that passes positional arguments
  straight through to a script or program instead of stopping at its own
  flag parser. Confirmed against the real binaries: `pnpm run build --help`
  and `bun run build --help` ran the build script, and `go run . --help` ran
  the program; pnpm/yarn/bun also run a package.json script when invoked
  WITHOUT `run` (`pnpm build --help` runs the `build` script too), so no
  denylist of pass-through verbs is safe for them either.

Any other flag on the line is inspected normally instead — including one
positioned to be swallowed as an earlier flag's own value (`specscore feature
change-status <id> --caller --help --to Approved` really calls change-status,
because `--caller` takes the next token unconditionally as its value and
never sees `--help` as a flag), a `--` separator that hands `--help` to a
script instead of the wrapper (`npm run build -- --help` really runs the
build script with `--help` as its own argument), and — for the five non-cobra
tools only — any subcommand word at all in front of `--help`/`-h` (`npm run
build --help`, `go test ./... -h`, `pnpm build --help`). For these tools,
"inspected normally" means the line still hits the governed-validation gate
inside a managed worktree exactly like the same command without `--help`
would. `gh pr merge`'s own `--help`/`-h` recognition is separate and reads
the flags the way gh does:
- `gh pr merge 123 --subject --help` and `gh pr merge 123 -st --help` are
  real merges, for the identical reason: `--subject`, like the `-t` that
  ends `-st`, takes the next token as its value.
- `gh pr merge -- --help` is a real merge too, because `--` makes `--help`
  a positional argument.
- Only a `--help`/`-h` that no value-taking flag consumed and that comes
  before any `--` is treated as help. It may stand alone or sit in a
  cluster of boolean short flags such as `-sh`.

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
