# WB — the Workbench

Fleet-wide operations across **your** GitHub repositories, from the
terminal: keep every local clone in sync with GitHub, and run config-driven
recipes across every repo that matches — no per-repo scripting.

Part of the [Sneat Developer Platform](https://sneat.dev/workbench/). The CLI
and executable stay intentionally short: `wb`.

## Install

```sh
go install github.com/sneat-dev/wb/cmd/wb@latest
```

A Homebrew cask (`brew install --cask sneat-dev/tap/wb`) is coming soon.

## Commands

```
wb sync   [flags]            # clone/pull/prune local clones to match GitHub, in parallel
wb run    [recipe] [flags]   # run a fleet-wide recipe defined in config
wb ci audit [path] [flags]   # validate coverage gates and artifact promotion
wb hooks  <command> [flags]  # install, validate, run, and measure user-owned Git hooks
```

### Persistent flags

| Flag | Default | Meaning |
|------|---------|---------|
| `--projects-root P` | `~/projects` | Root dir holding `{org}/{repo}` clones. |
| `--filter S` | — | Only process repos whose `org/name` contains `S`. |
| `--org O` | — | Query an additional GitHub owner (repeatable). **Not used by `sync`** — see below. |

### `wb sync`

Reconciles `~/projects/{org}/{repo}` with GitHub:

- non-archived, missing locally → clone
- non-archived, present locally → pull (skip if the working tree is dirty)
- archived, present + safe (clean, no stash, nothing unpushed) → remove
- archived, present + unsafe → keep, report why
- archived, missing → nothing

Runs against every repo owned by your GitHub account and every org you
belong to, in parallel, with a live progress UI (overall + per-org bars, a
live tail of in-flight repos). Anything left needing your attention (a hard
error, or a repo skipped/kept because it's dirty) opens an interactive
drill-down after the run — pick a repo to see exactly what's wrong
(modified/untracked/conflicted files, unpushed commits, stash entries).
Non-interactive runs (piped output, no TTY) print a plain summary instead
and skip the drill-down.

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--dry-run`, `-n` | off | Print the plan; change nothing. |
| `--workers`, `-j` | `8` | Max concurrent git/gh operations. |
| `--org`, `-o` | — (all your orgs + your account) | Only sync this org (repeatable). Restricts, rather than adds — unlike the persistent `--org` on `run`. |

```sh
wb sync --dry-run              # preview
wb sync -o your-org            # sync only one org
wb sync -j 16                  # more parallelism
```

### `wb run` — config-driven recipes

`wb run <recipe>` applies one recipe, defined in a YAML config, across every
repo it matches. **Dry-run by default** — pass `--apply` to commit & push.

```sh
wb run --list                     # show configured recipe names
wb run dev-approach               # preview
wb run dev-approach --apply       # land it
wb run some-lint --filter x       # preview, scoped to repos matching "x"
```

Flags:

| Flag | Default | Meaning |
|------|---------|---------|
| `--apply` | off (dry-run) | Commit & push changes. Without it, only reports what would change. |
| `--config PATH` | `~/.config/wb/wb.yaml` | Path to the recipe config. |
| `--list` | off | Print configured recipe names and exit. |

#### Config format

One YAML file, `~/.config/wb/wb.yaml` by default (override with `--config`).
Two recipe kinds:

**`template-section`** — merge a versioned block from a template file into a
target file (default `README.md`) in every matching repo:

```yaml
recipes:
  dev-approach:
    type: template-section
    target: README.md                          # default: README.md
    template: ~/path/to/dev-approach.md         # required
    marker: dev-approach                        # default: the recipe's own name
    applies_if: "has_source:go,ts"
```

The template file must contain the block wrapped in
`<!-- {marker}:vN -->` … `<!-- /{marker} -->`. Bumping the version number in
the template propagates it to every repo that already has an older section;
repos with a current-or-newer section, or no target file at all, are left
untouched.

**`command`** — run a shell command in the worktree; "changed" means
`git status --porcelain` is non-empty afterward:

```yaml
recipes:
  some-lint:
    type: command
    command: "some-linter --fix"                 # required
    dry_run_command: "some-linter"                # optional: a read-only preview
    count_regex: '(\d+)\s+problem'                # optional: extract a count from dry_run_command's output
    applies_if: has_file:some-linter.yaml
```

`dry_run_command`'s exit code (not the count) determines whether `--apply`
would do anything; `count_regex` only prettifies the dry-run summary. If
`dry_run_command` is omitted, dry-run mode can only report "would run: ...".

**`applies_if`** (all recipe kinds; default `always`):

- `always`
- `has_file:<path>` — e.g. `has_file:specscore.yaml`
- `has_source:go`, `has_source:ts`, or `has_source:go,ts` (comma = OR)

**Landing options** (all optional, defaulted from the recipe's name):
`commit_message`, `pr_branch`, `pr_title`, `pr_body`.

#### How it lands

Same worktree/commit/push-or-PR flow for both recipe kinds:

1. **Discover** repos across your GitHub orgs, same as `wb sync`.
2. **Skip**: forks, archived repos, local-only clones not under one of your
   owners, and any repo `applies_if` excludes.
3. **Land**, in a detached worktree off the default branch: if the local
   clone is dirty (uncommitted/unpushed) or the default branch is protected
   → push to `{pr_branch}` and open an auto-merge PR; otherwise → push
   directly to the default branch.

`wb` itself ships with **no recipes** — you define your own in
`~/.config/wb/wb.yaml`.

### `wb ci audit` — CI/CD policy validation

Audit the current repository, or every local clone, without changing anything:

```sh
wb ci audit --strict
wb ci audit --fleet --strict
wb ci audit --fleet --filter sneat-co/ --json
```

The audit detects Go and frontend stacks independently and requires each to
have an explicit positive coverage threshold. Mixed-stack repositories are
also required to select jobs from changed paths, so a backend-only change does
not start frontend runners (and vice versa). Repeated Playwright setup across
multiple E2E jobs is flagged for consolidation. For deployment workflows it flags
source rebuilds, missing CI artifacts, and artifacts that are downloaded
without source-SHA/checksum verification. `--strict` makes findings fail with a
non-zero exit code, suitable for CI and pre-push hooks; `--json` is intended for
Backstage/ops inventory.

### `wb hooks` — consistent, user-owned Git hooks

WB installs small managed shims while you retain control of the scripts they
run. Start conservatively in one repository, then roll the same policy through
all local clones:

```sh
wb hooks install                         # current repository
wb hooks check
wb hooks repair
wb hooks install --fleet                 # every clone below --projects-root
wb hooks check --fleet --filter sneat-co/
wb hooks repair --fleet
```

`install` and `repair` refuse to replace an existing `core.hooksPath` or an
unmanaged active hook. `repair --force` preserves hooks at an old configured
path and backs up any unmanaged collision inside WB's directory before replacing
it. `check` (alias `validate`) detects missing, stale, unexpected, or
non-executable shims; `--json` makes its result consumable by CI or Backstage.

#### Hook policy and custom templates

Policy layers in this order: WB's conservative built-ins, the user's global
`~/.config/wb/hooks.yaml`, then the repository's `.wb/hooks.yaml`. A repository
entry overrides the same global hook. Relative template paths are resolved from
the YAML file that declares them; template files are run with `/bin/sh` and do
not need to be executable.

```yaml
version: 1

hooks:
  pre-commit:
    template: templates/pre-commit.sh
  pre-push:
    template: templates/pre-push.sh
  # Disable a globally configured hook in this repository:
  # pre-push:
  #   disabled: true

metrics:
  enabled: true
  # path: ~/.local/state/wb/hook-events.jsonl
  labels:                       # optional, user-chosen pseudonyms
    developer: dev-17
    machine: laptop-a
```

Without configuration, pre-commit checks staged whitespace errors and pre-push
checks worktree whitespace errors. Copy and adapt the scripts in
[`examples/hooks-policy/`](examples/hooks-policy/) for repository-specific
format, lint, test, coverage, or build commands. Templates receive
`WB_HOOK`, `WB_REPO_ROOT`, `WB_REPO_SLUG`, `WB_HEAD_SHA`, `WB_BRANCH`,
`WB_HOOKS_CONFIG`, and `WB_HOOK_METRICS_PATH`, plus the original Git hook
arguments and standard input.

#### Local lifecycle metrics

Once installed, hooks append a versioned, local-only JSONL event after each
run. WB records repository, hook/action, outcome, duration, commit, branch,
OS/architecture, and optional labels—not diffs, filenames, commands, output,
credentials, email, hostname, or source. A metrics write failure warns but never
turns a successful hook into a failed commit or push.

```sh
wb hooks metrics                  # 14-day terminal chart
wb hooks metrics --days 30
wb hooks metrics --repo sneat-go
wb hooks metrics --json
```

Successful commits are counted exactly through `post-commit`. Pushes are
reported as **push attempts**, because Git provides `pre-push` but no
`post-push` confirmation. The default event file is
`~/.local/state/wb/hook-events.jsonl`; set `metrics.enabled: false` to disable
collection or configure a different path. Cross-developer/machine aggregation
is intentionally opt-in through explicit labels and a future exporter.

The broader direction—named build/test spans, cache and machine diagnostics,
local/CI/deployment correlation, CI-minute savings, and privacy-safe team
comparisons—is captured in the SpecScore idea
[`developer-lifecycle-metrics`](spec/ideas/developer-lifecycle-metrics.md).

## Build from source

```sh
go build -o ~/.local/bin/wb ./cmd/wb   # install on PATH
go test ./...                          # run tests
wb sync --dry-run                      # preview a fleet sync
wb run --list                          # see your configured recipes
```

## Adding a new operation

For anything expressible as "detect matching repos, mutate, land the
result," add a recipe to your `wb.yaml` — no code change needed. For
something structurally different (like `sync`, which reconciles local
clones with GitHub existence rather than mutating already-cloned content), a
new fleet command adds a `case` in `cmd/wb`, reusing `internal/discover` and
`internal/gitops`.

## Known limitation

Discovery keys on `org/name`. If a repo is cloned locally under a directory
name that differs from its GitHub org (e.g. `~/projects/dalgo/...` vs the
`dal-go` org), the mislabeled local copy is treated as local-only and
skipped, and the correctly-named repo is cloned fresh under
`~/projects/<org>/` during `sync`. Use matching org directory names to avoid
duplicate clones.

## License

MIT — see [LICENSE](LICENSE).
