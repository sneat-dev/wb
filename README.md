# WB — the Workbench

Fleet-wide operations across **your** GitHub repositories, from the
terminal: keep every local clone in sync with GitHub, and run config-driven
recipes across every repo that matches — no per-repo scripting.

Part of the [Sneat Developer Platform](https://sneat.dev/workbench/). The CLI
and executable stay intentionally short: `wb`.

The public [wb.sneat.dev](https://wb.sneat.dev) site is tracked in
[`website/`](website/README.md). It has its own Astro build and CI gate while
remaining versioned beside the CLI it presents.

## Install

```sh
go install github.com/sneat-dev/wb/cmd/wb@latest
```

A Homebrew cask (`brew install --cask sneat-dev/tap/wb`) is coming soon.

## Commands

```
wb sync   [flags]            # clone/pull/prune local clones to match GitHub, in parallel
wb run    [recipe] [flags]   # run a fleet-wide recipe defined in config
wb migrate <spec> <roots...> # plan or apply a declarative source migration
wb deps set <kind> <dep>@<v> # set existing dependency references to an exact version
wb deps bump go --changed M@V # propagate published Go releases through dependency waves
wb deps graph [path] [flags] # inspect dependency topology and open an SVG report
wb ci audit [path] [flags]   # validate coverage gates and artifact promotion
wb coverage [path] [flags]   # measure Go test coverage for one repo or a local fleet
wb verify [path] [flags]     # run conventional lint, test, and build checks
wb check [path] [flags]      # run a named local CI-equivalent check profile
wb status [path] [flags]     # inspect every local repo by default, or one path
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

### Fleet coverage and verification

These commands are read-only: they operate on existing local clones and never
fetch, modify source, commit, or push. Without `--fleet` they run against one
repository path (the current directory by default). `--fleet` scans every Git
repository below `--projects-root`.

```sh
# Go coverage for all cloned Sneat repositories, aggregated by statements.
wb coverage --fleet --match 'sneat-co/*' --parallel=2

# Emit a deterministic report for a human or agent.
wb coverage --fleet --regex '^sneat-co/(sneat|bots)' \
  --report-dir /tmp/wb-coverage --format yaml

# Run Go vet/test/build and defined Node lint/test/build scripts.
wb verify --fleet --filter sneat-co/ --parallel=2

# Restrict verification to compilation-oriented checks for one repository.
wb verify ~/projects/sneat-co/sneat-bots --checks lint,build

# CI profile adds SpecScore lint for repositories that contain spec/.
wb check --fleet --match 'sneat-co/*' --profile ci --parallel=2 \
  --timeout 10m --retry 1 --report-dir /tmp/wb-check

# After a partial failure, re-run only prior failed repositories.
wb check --fleet --match 'sneat-co/*' --profile ci \
  --resume --report-dir /tmp/wb-check
```

`--filter` (substring), `--match` (glob), and `--regex` are composed against
the `org/repo` name; every supplied filter must match. Both commands write
Markdown by default, can print YAML or JSON, and can write stable Markdown and
YAML files with `--report-dir`.

Coverage discovers every `go.mod` below a selected repository (excluding
`.git`, `vendor`, and `node_modules`) and uses temporary profiles outside the
repository. Its fleet percentage is statement-weighted, rather than an average
of repository percentages. Verification runs `go vet ./...`, `go test ./...`,
and `go build ./...` for each Go module; for a root Node project it runs only
defined `lint`, `test`, and `build` scripts with the detected package manager.
Other stacks remain explicit, reusable `wb run` recipes.

`wb check` provides stable local CI profiles: `fast` runs lint, `full` (the
default) runs lint/test/build, and `ci` additionally runs `specscore spec lint`
for repositories with `spec/`. `--timeout` applies to each external command;
`--retry=N` retries only failed commands N additional times; and
`--resume --report-dir DIR` selects only repository failures from the previous
YAML report. These controls also apply to `wb coverage` and `wb verify`.

### `wb status` — fleet-first local Git health

Status is fleet-first because the normal question is “which local checkouts
need attention?” Run `wb status` with no flags to scan all repositories below
`--projects-root`; there is intentionally no `--fleet` flag. Supplying a path
narrows the same command to one checkout.

```sh
wb status
wb status --filter sneat-co/ --match 'sneat-co/*' --parallel=8
wb status ~/projects/sneat-co/sneat-bots --details --format yaml
```

It reads only local Git state—never fetches, pulls, modifies, commits, or
pushes—and reports clean, attention, or inspection-error status. Attention
covers modified, untracked, conflicted, stashed, and unpushed work. Markdown
defaults to concise summaries; YAML/JSON and `--details` provide individual
paths and Git entries.

### `wb deps set` — one exact dependency version

Use `deps set` when the desired version is already known and must be applied
consistently. It updates existing references only; a repository that does not
already use the dependency is skipped with an explicit reason. Dependency
identities are fully qualified, so WB never guesses that `cicd` means a
particular owner and repository.

```sh
# Inspect one repository without creating a worktree.
wb deps set github-actions strongo/cicd@v1.10.5 \
  ~/projects/sneat-dev/wb --dry-run

# Set an exact reusable-workflow version across the selected fleet, verify
# locally, open PRs, wait for CI, and merge only passing PRs.
wb deps set github-actions strongo/cicd@v1.10.5 --fleet \
  --parallel=2 --commit --push --pr --merge

# Set an existing Go module requirement with go get and go mod tidy.
wb deps set go github.com/dal-go/dalgo@v0.63.1 \
  ~/projects/sneat-co/sneat-go
```

The initial adapters are `github-actions` and `go`. GitHub Actions tags are
resolved once to immutable commit SHAs; WB preserves the action or reusable
workflow subpath and writes `# <version>` next to the SHA. The Go adapter uses
official Go tooling rather than implementing module selection itself. A
semantic downgrade is rejected unless `--allow-downgrade` is explicit.

#### Private Go modules

Use repeatable `--go-private` for module-path patterns that must be fetched
without a public Go proxy or checksum-database lookup. WB merges each pattern
into `GOPRIVATE`, `GONOPROXY`, and `GONOSUMDB` for Go commands only; it does not
modify global Go configuration or accept credentials. Configure Git access
first—for GitHub, `gh auth setup-git` configures Git to use the existing GitHub
CLI authentication.

```sh
wb deps set go github.com/acme/private-sdk@v1.4.0 \
  --go-private github.com/acme

wb deps bump go --fleet \
  --changed github.com/acme/private-sdk@v1.4.0 \
  --go-private github.com/acme --merge
```

Canonical clones remain untouched, including dirty clones. WB fetches
`origin/<ref>` (`main` by default) and creates branches and worktrees below
`<projects-root>/.wb/worktrees/<operation>/<org>/<repo>`. Without publication
flags, verified changes remain in those local worktrees. `--push` implies
`--commit`; `--pr` implies push and commit; and `--merge` implies all prior
stages. Local lint, test, and build checks are enabled by default; use
`--checks`, `--timeout`, and `--retry` to tune them or `--no-verify` to disable
them explicitly.

WB opens all eligible PRs before entering its CI-wait phase, so independent
repository work continues while earlier PRs build. A merge requires at least
one observed GitHub check and every observed check must pass or be explicitly
skipped. Failing, cancelled, conflicted, checkless, and timed-out PRs remain
open. `--resume` validates and reuses the expected worktree branch and open PR.

Every run writes `deps-set.md` and `deps-set.yaml` below
`<projects-root>/.wb/reports/<operation>` (or `--report-dir`). Both formats
record observed and target versions, resolved SHAs, reasons, changed files,
verification, commits, PR links, CI checks, and merge outcomes. Git remains the
source of detailed patches; the Markdown report includes the exact diff command.

See the [Exact Dependency Set feature specification](spec/features/dependency-set/README.md)
for synthetic use cases and acceptance criteria. By default, `deps set` does
not discover newer releases or recalculate provider-to-consumer release waves.
For an exact Go target, `--propagate --fleet` delegates to `deps bump` with one
initial release event; `--max-waves` and `--release-poll` tune that delegated
campaign.

### `wb deps bump` — published-version propagation waves

Use `deps bump` after one or more exact Go module versions have been published
and their dependants must be moved in provider-first order:

```sh
wb deps bump go \
  --changed github.com/dal-go/record@v0.3.0 \
  --changed github.com/dal-go/dalgo@v0.64.0 \
  --fleet --parallel=2 --merge

# The same planner with one seed release:
wb deps set go github.com/dal-go/record@v0.3.0 \
  --fleet --propagate --parallel=2 --merge
```

`--propagate` is therefore similar to bump limited to one *initial* dependency,
but the campaign is not limited to that dependency. When an updated consumer
is merged and a newer module version is observed, that consumer module becomes
a release event for the next wave. `deps bump` also accepts multiple initial
`--changed` events, which is useful when a coordinated release publishes
several providers together.

Each wave rebuilds the graph from `origin/<ref>` and changes direct consumers
whose requirements are stale. Independent repositories share the same typed
clone/worktree/verification/commit/PR/CI lifecycle used by `deps set`. After
green PRs merge, WB captures an actual newer registry version before touching
downstream repositories; it never invents the next version. If a release is
not visible before `--timeout`, the report remains `awaiting_release` and
`--resume` continues from the persisted pre-merge baseline.

A second sweep can traverse repositories that were already updated before the
campaign: WB requires both a current `origin/<ref>` manifest and a published
module whose downloaded `go.mod` contains the seed versions. This evidence
turns the existing consumer release into the next event. Relevant
cross-repository dependency cycles fail before any worktree is created because
they need a separate coordinated-release protocol.

Without publication flags, the first changed wave stays in local worktrees.
`--commit`, `--push`, `--pr`, and `--merge` are cumulative just as for
`deps set`; automatic downstream propagation requires `--merge` so WB can
associate each next wave with observed publication evidence. Markdown and YAML
state are written as `deps-bump.md` and `deps-bump.yaml` below the operation's
report directory.

See the [Dependency Bump Waves feature specification](spec/features/dependency-bump-waves/README.md)
for synthetic use cases and acceptance criteria.

### `wb deps graph` — one scan, three dependency views

`deps graph` scans Go module declarations and requirements once, preserves the
manifest evidence, and derives three views from the same canonical model:

- `--view repos` shows internal provider repository → consumer repository
  edges for release order and propagation blast radius.
- `--view dependencies` shows dependency/module → consuming repository edges,
  including external dependencies.
- `--view selections` shows `dependency@version` → consuming repository edges
  and highlights versions behind the highest comparable version observed in
  this fleet. “Fleet-highest” is deliberately not described as registry-latest.

```sh
# Generate all report artifacts and open the repository view in a browser.
wb deps graph --fleet --match 'dal-go/*' --view repos --open

# Find every selected consumer of one exact module.
wb deps graph --fleet \
  --dependency github.com/dal-go/dalgo \
  --view dependencies

# Inspect one checkout and emit standalone SVG to stdout.
wb deps graph ~/projects/sneat-co/sneat-go \
  --view selections --format svg
```

The default report directory is
`<projects-root>/.wb/reports/deps-graph-go` (override it with `--report-dir`).
Every run writes:

- `deps-graph.md` — compact human and AI evidence index;
- `deps-graph.yaml` and `deps-graph.json` — deterministic canonical evidence;
- `deps-graph.svg` — accessible standalone rendering of the selected view;
- `deps-graph.html` — self-contained interactive report containing all three
  projections, search, path highlighting, fleet-drift highlighting, zoom/pan,
  organization highlighting, selected-node details, and CodeGrapher drill-down.

`--open` is explicit: headless and CI runs never attempt a GUI action. WB writes
every report before invoking the operating system's browser command, so an open
failure still leaves a usable HTML path. Providers flow left-to-right toward
consumers; direct and indirect requirements have distinct edge styles, and
cross-repository cycles are rendered rather than rejected.

Repository-backed nodes link to both GitHub and
[CodeGrapher](https://codegrapher.dev/), which provides repository-level symbol,
call, import, and impact exploration beneath WB's fleet-level topology. These
links are deterministic and passive: WB does not query CodeGrapher, publish a
snapshot, or trigger indexing while generating a report.

The first discovery adapter is Go and uses `golang.org/x/mod/modfile`.
Projection and rendering are independent of that adapter so Python and
TypeScript evidence can later feed the same report model.

See the [Dependency Graph feature specification](spec/features/dependency-graph/README.md)
for synthetic use cases and acceptance criteria.

### `wb migrate` — declarative source migrations

`wb migrate` is for repeatable codebase migrations rather than arbitrary shell
recipes. An HCL specification, decoded with HashiCorp's official HCL decoder,
declares the intended edit. WB discovers source files below the explicit roots,
produces a deterministic plan, and writes only when `--apply` is passed.

```sh
# Preview a migration; no files are edited.
wb migrate examples/migrations/dalgo-record-v1.hcl ~/projects/sneat-co

# Make the planned edits. `--check` instead exits 1 when drift is found.
wb migrate examples/migrations/dalgo-record-v1.hcl ~/projects/sneat-co --apply
```

Every planned file carries a SHA-256 of the source it was built from. Apply
refuses to overwrite a file changed after planning, and each replacement is
atomic. Migration specs contain no arbitrary commands, which keeps a preview
meaningful and makes the same spec suitable for CI.

#### Review reports

Markdown is the default stdout format. It is a compact index of changed files,
operations, source hashes, local-file links, and the exact `git diff` command
for each file. The detailed patch remains in Git, where humans and AI agents
can inspect it normally after an apply.

Use `--report-dir` to also write both representations:

```sh
wb migrate examples/migrations/dalgo-record-v1.hcl ~/projects/sneat-co \
  --report-dir /tmp/dalgo-record-report
```

- `migration.md` is the linked review index for humans and AI agents.
- `migration.yaml` is the sorted deterministic manifest for tools.
- `--format yaml` writes the same manifest to stdout instead of Markdown.

Reports are opt-in files, so an ordinary dry-run leaves source trees untouched.
Specifications can also declare regex-based `review` rules. They never edit
code; WB indexes matching files and line numbers under **Required review** so
an agent or human can handle semantic changes separately from mechanical ones.

The runner is language-neutral; structural transformations are supplied by
language adapters rather than by regexes. Today the Go adapter supports
syntax-aware `import.replace`, `selector.rewrite`, and `selector.rename`
operations, preserving comments and strings and choosing an import alias when
a name would be shadowed. The generic `text.replace` operation is available for
Go, Python, and TypeScript. Python and TypeScript structural adapters are
intentionally not implemented yet: a spec requesting one fails safely instead
of performing an unsafe text rewrite.

```hcl
format = "https://sneat.dev/workbench/formats/migration/v1"

migration "rename-api-v1" {
  title = "Rename the shared API"

  scope {
    languages = ["go"]
  }

  import_replace "go" {
    from = "example.com/old/api"
    to   = "example.com/new/api"
  }

  selector_rewrite "go" {
    import        = "example.com/old/service"
    add_import    = "example.com/new/model"
    add_import_as = "model"
    rewrites = {
      Record = "model.Record"
    }
  }

  # Repeat this block freely, including with the same "go" label.
  selector_rename "go" {
    import = "example.com/new/model"
    from   = "OldType"
    to     = "NewType"
  }

  composite_field_rename "go" {
    from = "OldEmbeddedField"
    to   = "NewEmbeddedField"
  }
}
```

`format` is the migration-spec contract, not an opaque integer. It is a link
to the format definition at
[`https://sneat.dev/workbench/formats/migration/v1`](https://sneat.dev/workbench/formats/migration/v1).
The first implementation recognises that exact format offline; it does not
fetch the URL while planning a migration.

Every `selector_rename "go"` block is a list entry, not a map entry, so many
blocks with the same language label are valid. It renames a qualified package
member such as `model.OldType`; it does not rename locally declared Go types or
unqualified identifiers. Those need a future type-aware rename operation based
on `go/types` (and corresponding LibCST/TypeScript compiler adapters), rather
than an unsafe text replacement.

`composite_field_rename "go"` renames only identifier keys in explicitly typed
named composite literals, such as `Entry{OldEmbeddedField: value}`. It skips
maps, arrays, slices, elided nested literals, strings, comments, declarations,
and ordinary identifier uses. The instruction is intentionally syntax-aware,
not owner-type-aware; use a distinctive field name and a narrow file scope
when the old name is common.

For a deterministic specification, WB evaluates HCL operation phases in this
order: `text_replace`, `import_replace`, `selector_rewrite`,
`selector_rename`, then `composite_field_rename`. Repeated blocks keep their
source order within a phase. The
separate, future local-type rename is deliberately not accepted until an
adapter can resolve declarations and references across its complete package.

Semantic review rules can omit already-correct forms on the same source line:

```hcl
review "changes-executor" {
  language        = "go"
  pattern         = "[.]ApplyChanges[(]"
  exclude_pattern = "dal[.]ApplyChanges[(]"
  message         = "Call the DAL executor with the record changes envelope."
}
```

`exclude_pattern` is optional and line-scoped. A matching exclusion suppresses
only review matches on that line, so a correct form elsewhere in the file does
not hide a method call that still needs semantic migration.

When a migration introduces a new Go module, declare its version explicitly:

```hcl
go_module_require "github.com/example/new-model" {
  version = "v1.2.3"
}

# Required when a campaign branch that used a local worktree replacement is
# about to become a PR. This version must already be available to remote CI.
go_module_release "github.com/example/new-model" {
  version = "v1.2.3"
}
```

The normal source-only runner leaves this declaration alone. It is consumed by
the hierarchical Go workflow below, which adds the requirement and redirects it
to the campaign's local worktree. `go_module_release` is intentionally
separate: it says which published version replaces that temporary local path
before a PR can be opened.

#### Hierarchical Go campaigns

Use `--hierarchical` when the migration must move a Go dependency graph rather
than one checked-out repository. It reads the source module's `go mod graph`,
finds the reverse dependency closure of the module paths referenced by the
migration, and prepares each GitHub repository independently.

```sh
# Plan only. No clone, fetch, worktree, source, commit, or push occurs.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical

# Apply into dedicated branches and worktrees, verifying every changed Go
# module with `go vet ./...` and `go test ./...` (the default `full` mode).
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply

# Commit only after all default verification succeeds. Push is separately
# opt-in and pushes those branches only.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --commit --push

# Open one PR per changed repository. WB continues with other ready
# repositories while GitHub Actions runs for PRs already opened.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --pr --parallel=2

# Merge only after every campaign PR has successful required GitHub checks.
# This does not enable auto-merge or bypass protected-branch rules.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --merge

# Resume partial campaign worktrees on their expected branches.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  ~/projects/sneat-co/sneat-bots \
  --hierarchical --apply --resume

# Remove only clean worktrees for the named migration. No source root is used.
wb migrate examples/migrations/dalgo-record-v1.hcl \
  --hierarchical --cleanup
```

Canonical clones live at `<github-dir>/<org>/<repo>`; `--github-dir` defaults
to `--projects-root`. The campaign creates its worktrees under
`<github-dir>/.wb/worktrees/<migration>/<org>/<repo>` from `origin/<ref>`
(`main` by default). A dirty canonical clone is never checked out, reset, or
otherwise modified: WB only fetches `origin`, then branches its dedicated
worktree from the requested remote ref. Missing, resolvable GitHub repositories
are cloned during `--apply`, regardless of organisation.

For changed consumer modules, WB updates `go.mod` requirements declared in the
spec and writes relative `replace` directives to the matching campaign
worktrees. It does not create a shared `go.work` file. This lets dependent
worktrees compile together while keeping the changes reviewable and
committable per repository. Before `--pr` (and therefore `--merge`), WB removes
those temporary replacements, requires an explicit `go_module_release` for
each affected module, runs `go mod tidy`, and reruns the selected verification.
This prevents a PR from containing local paths that GitHub Actions cannot
resolve. If a module has not been released yet, the campaign fails safely
before the affected repository is committed, pushed, or submitted for review.

Verification is enabled by default for every `--apply` campaign:

| Setting | Checks |
|---|---|
| `--verify=compile` | `go test -run=^$ ./...` |
| `--verify=test` | `go test ./...` |
| `--verify=full` (default) | `go vet ./...`, then `go test ./...` |
| `--no-verify` or `--verify=none` | No checks |

`--commit` requires `--apply`. `--push` implies `--commit` and also requires
`--apply`. `--pr` implies `--push`; it opens one ordinary (non-draft) PR per
changed repository, with no auto-merge. `--merge` implies `--pr` and is a
separate final phase: WB first checks every campaign PR's required GitHub
checks, then uses GitHub's normal merge operation in dependency order. It
stops before merging anything when a check is pending or failing, and never
bypasses branch protection.

`--parallel=N` (default `1`) runs independent repositories concurrently. WB
still processes dependency layers in order: a provider's migration and local
verification complete before a consumer that uses its local replacement starts.
Within each layer, WB completes source edits across all repositories before
normalizing manifests, then verifies the layer. This makes cyclic Go module
groups safe because dependency tooling never reads a peer's half-rewritten
source tree.
Once a repository is verified and `--pr` is active, its PR is opened
immediately; WB does not wait for its remote CI before working on later ready
repositories. Only the final `--merge` phase waits for required GitHub checks.
Local campaigns without commit or publishing flags continue verification after
a failure so the final report indexes every failing repository. Publishing
campaigns remain fail-fast before dependent branches can be committed.

`--resume` is an explicit recovery path: it accepts an existing worktree on the
expected campaign branch, preserves partial or manually corrected migration
changes, and verifies those existing changes. Dependency discovery also uses
the validated root campaign worktree, so prerequisite refactors that introduce
modules bring those providers into the next campaign pass automatically. Go's
own module tooling repairs incomplete `go.mod`/`go.sum` metadata during that
apply-only resume discovery. An
apply campaign holds an
exclusive lock under its migration worktree root, so concurrent runs fail safely.
`--cleanup` removes only clean worktrees for that migration; it leaves
canonical clones, branches, and reports intact.

Every hierarchical run writes a linked human index and deterministic manifest
to `<github-dir>/.wb/reports/<migration>/campaign.md` and `campaign.yaml`
(or `--report-dir`). Per-module `migration.md` and `migration.yaml` reports
are nested beneath that directory. The campaign index lists every
repository-relative path that differs from its configured base ref, including
committed, staged, unstaged, and untracked files. This cumulative index remains
truthful after an idempotent `--resume`; per-module counts describe only files
rewritten by the current mechanical pass. The Markdown index points at
worktrees and per-module reports, while Git remains the source of the detailed
diff.

On a dry run the campaign deliberately reports `plan_state: deferred` and no
`changed_files` count: WB has not created worktrees or evaluated their source.
Its Markdown index says `unknown (worktree not created)` rather than implying
that no files will change.

Adapter work is deliberately isolated behind the same planning and apply
protocol:

| Language | Structural adapter | Package/manifest work |
|---|---|---|
| Go | Implemented with `go/ast`, `go/types`, and `go/format` | `go.mod` support is implemented; local type rename remains a future type-aware operation |
| Python | Planned with LibCST | `pyproject.toml` |
| TypeScript | Planned with the TypeScript compiler API | `package.json` |

The initial DALgo migration definition is
[`examples/migrations/dalgo-record-v1.hcl`](examples/migrations/dalgo-record-v1.hcl).

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

#### Hook policy, detection, and composable profiles

Policy layers in this order: WB's conservative built-ins, the user's global
`~/.config/wb/hooks.yaml`, then the repository's `.wb/hooks.yaml`. A repository
entry overrides the same global hook. Automatic profiles are opt-in, so
upgrading WB never adds expensive checks to an existing installation
unexpectedly.

```yaml
version: 1

profiles:
  auto: true                    # detect all built-in and custom definitions
  # include: [sneat-product]    # force a profile even without a match
  # exclude: [node]             # suppress a detected or inherited profile
  definitions:
    sneat-product:              # custom product/tool/domain profile
      order: 200
      detect:
        any_files:
          - sneat.project.yaml
      hooks:
        pre-push:
          template: templates/sneat-product/pre-push.sh

# A direct hook replaces WB's conservative base block. Setting it disabled
# suppresses the whole hook, including blocks contributed by profiles.
# hooks:
#   pre-push:
#     disabled: true

metrics:
  enabled: true
  # path: ~/.local/state/wb/hook-events.jsonl
  labels:                       # optional, user-chosen pseudonyms
    developer: dev-17
    machine: laptop-a
```

With `profiles.auto: true`, the built-in detectors currently contribute:

| Profile | Detection | Pre-commit block | Pre-push block |
|---|---|---|---|
| `go` | `go.mod` | `gofmt` on staged Go files | `go vet ./...`, then `go test ./...` |
| `node` | `package.json` | — | run `lint` and `test` scripts when present, using the detected lockfile's package manager |

A Go-only repository therefore runs the base and Go blocks, a Node-only
repository runs the base and Node blocks, and a mixed repository runs all
relevant blocks. Custom definitions use repository-relative `any_files` and
`all_files` detectors; standard glob patterns are supported. A definition with
the same name as `go` or `node` overrides selected built-in hooks, so users can
replace either language template globally. The base block runs first; profiles
run by ascending `order`, then name. Each pre-push block receives an independent
copy of Git's stdin and execution stops on the first failure.

Relative template paths are resolved from the YAML file that declares them;
templates run with `/bin/sh` and need not be executable. Copy and adapt
[`examples/hooks-policy/`](examples/hooks-policy/). Templates receive
`WB_HOOK`, `WB_PROFILE`, `WB_BLOCK`, `WB_REPO_ROOT`, `WB_REPO_SLUG`,
`WB_HEAD_SHA`, `WB_BRANCH`, `WB_HOOKS_CONFIG`, and `WB_HOOK_METRICS_PATH`, plus
the original Git hook arguments and standard input. `wb hooks check` displays
the detected profiles and exact block order; `--json` exposes the same data.

#### Local user sections around WB

Generated hook files are ordinary shell scripts. WB owns only the delimited
dispatcher and preserves user commands before and after it during install or
repair:

```sh
#!/bin/sh
set -eu

# Local commands that run before WB.

### Start of WB managed hook ###
'/path/to/wb' hooks run 'pre-push' -- "$@"
_wb_hook_status=$?
if [ "$_wb_hook_status" -ne 0 ]; then
    exit "$_wb_hook_status"
fi
### End of WB managed hook ###

# Local commands that run after every WB block succeeds.
```

Policy templates are preferable for shared, version-controlled checks. The
outer sections are useful for machine-local behavior and remain untouched as
WB updates only the marked section.

#### Local lifecycle metrics

Once installed, hooks append versioned, local-only JSONL events in one batched
write per WB run. WB records its managed dispatch and per-block
outcomes/durations alongside repository, commit, branch, OS/architecture, and
optional labels—not diffs, filenames, commands, output, credentials, email,
hostname, or source. Machine-local commands outside the WB delimiter are
intentionally not observed or timed. A metrics write failure warns but never
turns a successful WB block into a failed commit or push.

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

Discovery keys on `org/name` and ignores linked Git worktrees, which are
alternate checkouts rather than additional fleet repositories. If a repo is
cloned locally under a directory
name that differs from its GitHub org (e.g. `~/projects/dalgo/...` vs the
`dal-go` org), the mislabeled local copy is treated as local-only and
skipped, and the correctly-named repo is cloned fresh under
`~/projects/<org>/` during `sync`. Use matching org directory names to avoid
duplicate clones.

## License

MIT — see [LICENSE](LICENSE).
