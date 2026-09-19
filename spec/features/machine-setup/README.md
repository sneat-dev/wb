---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Machine Setup

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Depends On:** [Install](../install/README.md), [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md), [Code Index Freshness](../code-index-freshness/README.md), [strongo/cli-helpers catalog](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=explore) (new entry fields)
**Related:** [Expert Tool Routing](../expert-tool-routing/README.md) (delivered by `skills:*`, `selector:*`, `agent-guard:*`)

## Summary

`wb setup` makes any Linux, macOS or Windows machine an efficient agent
machine with one idempotent command; `wb setup --check` reports drift from
that state without network access. It owns no installation, hook or skill
behavior: each item delegates to an existing command, and every tool-specific
value except wb's own minimum versions is data the tool's cli-helpers catalog
entry declares. Its contribution is the **inventory**, resolved per machine
and checked or converged in one pass.

## Problem

The 2026-09-18 SDLC logging-gap analysis found the efficient path missing on
the main agent VM: the wb guard was not registered (92% of heavy commands
bypassed `wb run` admission); no lifecycle executor was configured, so the
most-used canonical clones had no code index and 3,015 symbol greps ran
against 0 codegrapher analysis calls in a week; `.codegraph/` was not
ignored, so the one index created (134 MB) tripped `dirty-worktree`
refusals. Each piece has a WB command; nothing lists them. The founder: "make
sure we have a script or wb command to configure any machine."

**Why `wb setup`, not `wb install`:** the Install Feature forbids adding
behavior to the cli-helpers binding (install#req:library-provided-behavior).
Setup composes commands, as [`wb cleanup`](../cleanup-orchestration/README.md)
does; `wb doctor` reads as report-only.

## Behavior

### REQ: item-inventory

`wb setup` MUST evaluate this ordered set of items, each with a stable `id`
(`--only <prefix>,…` limits a run to matching ids). `<cli>` ranges over
catalog CLIs marked relevant to wb (today `specscore`, `codegrapher`,
`cover100`). A per-CLI item other than `cli:` exists only when that catalog
entry declares its data; until the catalog fields ship, those items are
absent and a single `catalog-fields-missing` note says so.

| Id | Desired state | Delegates to |
|---|---|---|
| `cli:<cli>` | installed; apply upgrades to latest; `--check` compares with the minimum-version table | `wb install` / `wb upgrade` |
| `git-hooks` | managed shims current in every canonical clone in scope, with the profiles `wb hooks install` selects by default | `wb hooks install` / `repair` |
| `lifecycle:<executor>` | the declared executor template and a `checkout-updated` binding for the scope | lifecycle config writer |
| `backfill:<executor>` | in-scope canonical clones indexed at least once | `wb hooks lifecycle backfill --apply` |
| `gitignore:<cli>` | the declared ignore globs in the global Git excludes file | line-append writer |
| `selector:<cli>` | the declared test selector as `check.test_selection` in `wb.yaml` | config writer |
| `agent-guard:<harness>` | the wb pre-tool-use guard registered, with the matcher its nudge setting implies | `wb hooks agent install` |
| `skills:<cli>` | CLI-matched Agent Skills in every present harness | `wb skills sync`, or the declared skill-sync argv |
| `harness-settings` | only settings with a recorded decision | JSON-merge writer |
| `daemon` | report only: supervised and ready, from its state file | none |

WB code MUST NOT name any fleet CLI other than wb
(trusted-repository-update-hooks#req:catalog-declared-templates). The one
exception is the minimum-version table (`ai/setup/minimums.yaml`), which
records wb's own dependency on a CLI, not the CLI's behavior; a CLI without a
row has no minimum. Repository content MUST NOT enable items.

### REQ: scope-and-backfill

The scope is `setup.repositories` (include/exclude globs of canonical
identities) in the trusted user `wb.yaml`, defaulting to every canonical
clone under the projects root. `git-hooks` MUST NOT write any hooks-policy
key: it leaves profile selection exactly as `wb hooks install` sets it (today
`worktree`, and `lifecycle` once Code Index Freshness ships), and its item
detail names each installed profile's effect, including that `worktree`
refuses commits outside managed worktrees and how to opt out. `backfill`
MUST call `wb hooks lifecycle backfill --apply --only-never --order recent
--limit <setup.backfill.limit>` (new flags; default limit 10). When the
executor's `priority: background` cannot be written (see version-skew), the
backfill is deferred with it rather than run at normal priority.

### REQ: per-machine-resolution

Every path an item writes MUST be resolved on the machine running it:

- an executor's `run:` path is the copy the install library selects, the
  first on `PATH`; other copies are `shadowed` notes that never raise the
  exit code. Only when no copy is on `PATH` and several exist do path-writing
  items report `ambiguous`, write nothing, and name the remedy
  (`--prefer <cli>=<path>`). The path MUST pass
  trusted-repository-update-hooks#req:generic-executors before it is written;
- the global excludes file is `git config --global core.excludesFile` when
  set, else `$XDG_CONFIG_HOME/git/ignore`, else `~/.config/git/ignore`
  (`%USERPROFILE%\.config\git\ignore` on Windows);
- harness settings follow the harness's own resolution.

### REQ: version-skew

`wb setup` MUST write `hooks.version: 2` keys
(trusted-repository-update-hooks#req:version-2-extensions) only when every
executable that reads that config supports them: the PATH-first `wb` (as the
hook shims resolve it) and `WB_EXECUTABLE` when set, each by the version
`<exe> version --format json` reports; and the running daemon, by
`provenance.version` in its state file, read without contacting it. If the
binary now at the daemon's recorded executable reports a different version,
the daemon runs older code and counts as unsupported until
`wb daemon restart`. Support means a release at or above a constant compiled
into wb; a development, unparsable or missing version is unsupported.
Otherwise setup writes a version-1 config and reports
`deferred-version-skew`, naming each executable and its remedy. Shadowed
`wb` copies are notes, never inputs.

### REQ: check-is-offline-and-read-only

`--check` and `--dry-run` MUST NOT write any file, including receipts and the
worktree heartbeat (exempt as `wb version` is). `--check` MUST NOT open a
network connection; the only processes it starts are local version probes of
wb executables and catalog CLIs, run with each CLI's declared offline
environment when the catalog declares one. States: `ok`, `drift`, `missing`,
`conflict`, `ambiguous`, `blocked`, `deferred-version-skew`,
`pending-decision`, `unsupported`, `not-checked` (upgrade availability).

### REQ: converge-idempotently

`wb setup` MUST converge every `drift` and `missing` item and be idempotent:
a second run on a converged machine writes nothing, not even a receipt.
Running it is the confirmation for delegated commands (it passes `--yes`); it
never prompts. Items run in order; a failed item does not stop independent
items; dependants report `blocked` with the blocker's id.

### REQ: never-overwrite-foreign-config

An item MUST only add content it owns or replace content it recorded writing.
Any other change is a `conflict` with a unified diff, not written unless
`--overwrite <exact id>` names it. Writes are atomic, go through a symlinked
config to its target without replacing the link, and keep the previous
content as `<file>.wb-setup.<timestamp>.bak` (newest five retained).

### REQ: machine-local-state

Nothing setup reads or writes may be published by `wb remote publish`, a hub
or a fleet event, except item ids and states in a fleet event. A receipt is
written only when a run changed something, to
`<projects-root>/.wb/setup/receipts/<timestamp>.json`, mode `0600`.

### REQ: output-contract

Text: one line per item (`<state>  <id>  <detail>`) and a summary. JSON:
`{"version":1,"os","arch","mode":"check|dry-run|apply","items":[{"id","state","detail","notes","diff","blocked_by"}],"summary":{"<state>":n}}`.
Exit `0` when every item is `ok` or converged; `1` for `drift`, `missing`,
`conflict`, `ambiguous`, `blocked`, `deferred-version-skew` or a failure; `2`
for usage. `pending-decision`, `unsupported`, `not-checked` and notes never
raise it. It MUST run on Linux, macOS and Windows; an unsupported item says
why.

## Acceptance Criteria

### AC: fresh-machine-converges

**Requirements:** machine-setup#req:item-inventory, machine-setup#req:converge-idempotently, machine-setup#req:scope-and-backfill

**Given** a machine with wb installed and the catalog fields shipped, no fleet
CLIs, no `hooks:` section, an empty excludes file, no registered guard, no
recorded settings decision, and 3 canonical clones without managed hooks
**When** the user runs `wb setup --format json`
**Then** it exits `0`; items report `ok`, `pending-decision` or
`unsupported`; no `cleanupPeriodDays` key is written; the 3 clones have a
queued or completed backfill; the hooks policy files are byte-identical; the
`git-hooks` detail names the installed profiles and the `worktree` opt-out;
a following `wb setup --check` exits `0`.

### AC: second-run-writes-nothing

**Requirements:** machine-setup#req:converge-idempotently

**Given** a machine on which `wb setup` has just succeeded
**When** the user runs `wb setup` again
**Then** no file under the home directory or projects root changes content or
modification time, no receipt is written, and every item reports `ok`.

### AC: check-is-offline

**Requirements:** machine-setup#req:check-is-offline-and-read-only

**Given** a converged Linux machine whose guard registration was removed, and
a running daemon
**When** the user runs `strace -f -e trace=connect wb setup --check --format json`
from outside any worktree, and again from inside one
**Then** both exit `1` with `agent-guard:claude` as `drift`; the trace has
no `connect` to an `AF_INET`/`AF_INET6` address or to the daemon's socket;
no file changes, including the heartbeat. On macOS and Windows, the same
command with networking disabled produces the same output.

### AC: path-resolved-per-machine

**Requirements:** machine-setup#req:per-machine-resolution

**Given** two machines with codegrapher first on `PATH` at
`~/.local/bin/codegrapher` and `/opt/homebrew/bin/codegrapher`
**When** `wb setup` runs on each
**Then** each `wb.yaml` names its own path and `wb hooks lifecycle check`
exits `0` on both.

### AC: shadowed-copy-is-a-note

**Requirements:** machine-setup#req:per-machine-resolution, machine-setup#req:version-skew

**Given** specscore first on `PATH` with a second copy off `PATH`; and,
separately, codegrapher only in two directories, neither on `PATH`
**When** the user runs `wb setup --format json` in each case
**Then** the first reports `cli:specscore` `ok` with a `shadowed` note and
exits `0`; the second reports `ambiguous` for
path-writing items, names `--prefer`, writes nothing for them and exits `1`;
rerunning with `--prefer codegrapher=<path>` converges.

### AC: foreign-config-not-overwritten

**Requirements:** machine-setup#req:never-overwrite-foreign-config

**Given** a user-edited `code-index` executor with `timeout: 10m`, and
`wb.yaml` a symlink into a dotfiles checkout
**When** the user runs `wb setup`, then
`wb setup --overwrite lifecycle:code-index`
**Then** the first reports `conflict` with a diff, leaves the file
byte-identical and exits `1`; the second applies exactly that diff through
the symlink, which stays a symlink, and leaves a timestamped backup.

### AC: version-skew-deferred

**Requirements:** machine-setup#req:version-skew, machine-setup#req:scope-and-backfill

**Given**, in turn: a daemon whose `provenance.version` is below the
version-2 constant although its on-disk binary was since upgraded;
`WB_EXECUTABLE` pointing at a development build; and an older `wb` shadowed
behind a supported PATH-first `wb`
**When** the user runs `wb setup` in each case
**Then** in the first two, `wb.yaml` has no version-2 key, no backfill is
queued, the lifecycle and backfill items report `deferred-version-skew`
naming the daemon (with `wb daemon restart`) or the executable, and setup
exits `1`; in the third, version-2 keys are written and the older copy is a
`shadowed` note.

### AC: nothing-published

**Requirements:** machine-setup#req:machine-local-state

**Given** a converged machine configured for `wb remote publish`
**When** the user publishes and inspects the published record
**Then** it holds no path, setting, excludes content or receipt; the receipt
has mode `0600`.

### AC: usage-errors

**Requirements:** machine-setup#req:output-contract

**Given** an installed wb
**When** the user runs `wb setup --only nosuchitem`, then
`wb setup --check --overwrite gitignore:codegrapher`
**Then** both exit `2` before any read of harness settings or any write,
each naming the offending flag.

### AC: no-tool-names-in-code

**Requirements:** machine-setup#req:item-inventory

**Given** the wb source tree
**When** `grep -rniE 'codegrapher|specscore|cover100'` runs over non-test Go files in `internal/setup` and `cmd/wb/setup*.go`
**Then** it finds no match.

## Delivery Slices

1. `cli:*`, `agent-guard:*`, `skills:` via `wb skills sync`, `daemon` from
   its state file; no catalog dependency — check-is-offline,
   second-run-writes-nothing, shadowed-copy-is-a-note (first case),
   usage-errors, nothing-published, and expert-tool-routing's
   efficient-path-drift-reported.
2. After the cli-helpers catalog fields ship: `lifecycle:` as a version-1,
   canonical-only executor, `gitignore:`, `git-hooks` —
   path-resolved-per-machine, foreign-config-not-overwritten,
   no-tool-names-in-code, shadowed-copy-is-a-note (second case).
3. Version skew, version-2 keys and `backfill:` — version-skew-deferred,
   fresh-machine-converges. `selector:` follows `wb check --changed`.

## Open Questions

- **Harness settings.** Pending decisions from the 2026-09-18 analysis:
  decision 1 (transcript retention) and decision 7 (the nudge). Decision 2
  (rewrite rather than refuse) is decided; #637 is open, implemented in PR
  #645. Where are decisions recorded for setup to read: `setup.decisions`?
- **Catalog fields** (executor template, ignore globs, test selector,
  skill-sync argv, offline environment) need a strongo/cli-helpers release
  and a wb bump; this is on the critical path for slices 2 and 3.
- golangci-lint is not in the catalog (wb's gates run a pinned `go run`);
  specscore has no skill-sync verb. Should either change?

---
*This document follows the https://specscore.md/feature-specification*
