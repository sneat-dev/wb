---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Machine Setup

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Depends On:** [Install](../install/README.md), [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md), [Code Index Freshness](../code-index-freshness/README.md)
**Related:** [Expert Tool Routing](../expert-tool-routing/README.md) (delivered by `skills:*`, `selector:*`, `agent-guard:*`)

## Summary

`wb setup` makes any Linux, macOS or Windows machine an efficient agent
machine with one idempotent command, and `wb setup --check` reports every way
the machine has drifted from that state, without network access. It owns no
installation, hook, or skill behavior of its own: each item delegates to the
existing expert command (`wb install`/`wb upgrade`, `wb hooks install`,
`wb hooks agent install`, `wb hooks lifecycle backfill`, `wb skills sync`),
and every tool-specific value is data the tool's catalog entry declares. Its
contribution is the **inventory**, resolved per machine and checked or
converged in one pass.

## Synopsis

```
wb setup --check                  # read-only drift report; no network; exit 1 on drift
wb setup --dry-run --format json  # full plan with a diff per write; writes nothing
wb setup                          # converge every item that does not overwrite foreign config
wb setup --overwrite lifecycle:code-index    # accept the shown diff for one exact item id
wb setup --prefer codegrapher=/opt/tools/codegrapher  # pick one copy when none is on PATH
```

## Problem

The 2026-09-18 SDLC logging-gap analysis found the efficient path missing on
the main agent VM: the wb pre-tool-use guard was not registered, so 92% of
heavy commands bypassed `wb run` admission; no lifecycle executor was
configured, so the most-used canonical clones had no code index and 3,015
symbol greps ran against 0 codegrapher analysis calls in a week; and
`.codegraph/` was not ignored, so the one index created (134 MB) tripped wb's
`dirty-worktree` refusals. Each piece has a WB command. Nothing lists the
pieces, so a new or changed machine silently loses them. The founder's ask:
"make sure we have a script or wb command to configure any machine."

**Why `wb setup`, not `wb install`:** the Install Feature forbids wb from
adding behavior to the cli-helpers binding
(install#req:library-provided-behavior). Setup composes several commands, the
shape of [`wb cleanup`](../cleanup-orchestration/README.md). `wb doctor` reads
as report-only; `setup` names check and convergence and sits beside
`install`, `upgrade` and `skills`.

## Behavior

### REQ: item-inventory

`wb setup` MUST evaluate this ordered set of items, each with a stable `id`.
`<cli>` ranges over catalog CLIs the catalog marks relevant to wb (today
`specscore`, `codegrapher`, `cover100`); a per-CLI item exists only when that
CLI's catalog entry declares the data it needs:

| Id | Desired state | Delegates to |
|---|---|---|
| `cli:<cli>` | installed; apply mode upgrades to latest; `--check` compares with wb's minimum-version table | `wb install` / `wb upgrade` |
| `git-hooks` | the `lifecycle` profile ([Code Index Freshness](../code-index-freshness/README.md)) installed in every canonical clone in scope; other profiles untouched | `wb hooks install` / `repair` |
| `lifecycle:<executor>` | the executor template the catalog entry declares, plus a `checkout-updated` binding for the repositories in scope | the lifecycle config writer |
| `backfill:<executor>` | the in-scope canonical clones indexed at least once | `wb hooks lifecycle backfill --apply` |
| `gitignore:<cli>` | the ignore globs the catalog entry declares, in the global Git excludes file | a line-append writer |
| `selector:<cli>` | the `test_selection` executor the catalog entry declares, for `wb check --changed` | the lifecycle config writer |
| `agent-guard:<harness>` | the wb pre-tool-use guard registered for each present harness that supports it | `wb hooks agent install` |
| `skills:<cli>` | CLI-matched Agent Skills in every present harness | `wb skills sync`, or the skill-sync argv the catalog entry declares |
| `harness-settings` | only settings with a recorded decision | a JSON-merge writer |
| `daemon` | report only: supervised and ready | none |

WB code MUST NOT name any fleet CLI other than wb
(trusted-repository-update-hooks#req:catalog-declared-templates); the
templates, globs and skill-sync argv are fields of the cli-helpers catalog
entry. Repository content MUST NOT enable items; `--only <prefix>,…` limits
a run to matching ids. Minimum versions are data shipped in wb
(`ai/setup/minimums.yaml`); a CLI without a row has no minimum.

### REQ: scope-and-backfill

The repositories in scope are `setup.repositories` (include/exclude globs of
canonical identities) in the trusted user `wb.yaml`, defaulting to every
canonical clone under the projects root. `git-hooks` MUST install only the
`lifecycle` profile; it MUST NOT add the `worktree` profile, whose pre-commit
refuses commits, unless `setup.git_hooks.profiles` names it. `backfill`
MUST enqueue at most `setup.backfill.limit` (default 10) never-indexed clones,
most recently fetched first, at background priority under CPU admission
(code-index-freshness#req:background-execution); the rest are indexed on
their next checkout move and reported as `never` meanwhile.

### REQ: per-machine-resolution

Every path an item writes MUST be resolved on the machine running it:

- an executor's `run:` path is the absolute path of the copy the install
  library selects: the first on `PATH`, as `wb install` reports it. Other
  copies are reported as `shadowed` notes, which never raise the exit code;
- only when no copy is on `PATH` and several exist, items that write a path
  report `ambiguous`, write nothing, and name the remedy
  (`--prefer <cli>=<path>`, or remove a copy); the selected path MUST pass
  trusted-repository-update-hooks#req:generic-executors before it is written;
- the global excludes file is `git config --global core.excludesFile` when
  set, else `$XDG_CONFIG_HOME/git/ignore`, else `~/.config/git/ignore`
  (`%USERPROFILE%\.config\git\ignore` on Windows);
- harness settings follow the harness's own resolution (`$CLAUDE_CONFIG_DIR`,
  then `~/.claude/settings.json`).

### REQ: version-skew

`wb setup` MUST write `hooks.version: 2` keys
(trusted-repository-update-hooks#req:version-2-extensions) only when every wb
that may read the config supports version 2: the running binary, the daemon's
executable as `wb daemon status` reports it, every `wb` on `PATH`, and
`WB_EXECUTABLE` when set. Otherwise it MUST write a version-1 config without
those keys and report the item `deferred-version-skew`, naming each older
executable and the remedy (`wb upgrade wb`, restart the daemon).

### REQ: check-is-offline-and-read-only

`wb setup --check` and `--dry-run` MUST NOT write any file (including
receipts and the worktree heartbeat, from which they are exempt as `wb
version` is). `--check` MUST NOT open a network connection or start a process
that does; it reports each item as `ok`, `drift`, `missing`, `conflict`,
`ambiguous`, `blocked`, `deferred-version-skew`, `pending-decision`,
`unsupported` or `not-checked`, with upgrade availability `not-checked`.

### REQ: converge-idempotently

`wb setup` MUST bring every `drift` and `missing` item to its desired state
and be idempotent: a second run on a converged machine writes nothing,
including no receipt. Invoking it without `--check`/`--dry-run` is the
confirmation for every delegated command (it passes their `--yes`); it never
prompts. Items run in inventory order; a failed item MUST NOT stop
independent items, and dependent items report `blocked` with the blocker's id.

### REQ: never-overwrite-foreign-config

An item MUST only add content it owns or replace content it previously wrote
and recorded. A change that would modify or remove content WB did not write
MUST be reported as `conflict` with a unified diff and not written, unless the
invocation names that exact id in `--overwrite`. Writes MUST be atomic
(temp file, fsync, rename) and write through a symlinked config to its
target, never replacing the link; each write keeps the previous content as
`<file>.wb-setup.<timestamp>.bak`, retaining the newest five.

### REQ: machine-local-state

Everything setup reads or writes is machine-local and MUST NOT be published
by `wb remote publish`, a hub, or a fleet event: executable paths, harness
settings, `wb.yaml`, the excludes file, and the receipt. A receipt is written
only when a run changed something, to
`<projects-root>/.wb/setup/receipts/<timestamp>.json` with mode `0600`. A
fleet event MAY record item ids and states only.

### REQ: output-contract

Text output prints one line per item (`<state>  <id>  <detail>`) and a
summary. `--format json` emits
`{"version":1,"os","arch","mode":"check|dry-run|apply","items":[{"id","state","detail","notes","diff","blocked_by"}],"summary":{"<state>":n}}`.
Exit `0` when every item is `ok` or converged; `1` when any item is `drift`,
`missing`, `conflict`, `ambiguous`, `blocked`, `deferred-version-skew` or
failed; `2` for usage errors. `pending-decision`, `unsupported`,
`not-checked` and `shadowed` notes never raise the exit code.

### REQ: platform-parity

It MUST run on Linux, macOS and Windows; an unsupported item reports why.

## Acceptance Criteria

### AC: fresh-machine-converges

**Requirements:** machine-setup#req:item-inventory, machine-setup#req:converge-idempotently, machine-setup#req:scope-and-backfill

**Given** a machine with wb installed, no fleet CLIs, no `hooks:` section, an
empty excludes file, no registered guard, no recorded settings decision, and
3 canonical clones without managed hooks
**When** the user runs `wb setup --format json`, then commits in one clone
**Then** it exits `0`; each item reports `ok`, `pending-decision` or
`unsupported`; `harness-settings` is `pending-decision` and the settings file
gains no `cleanupPeriodDays`; all 3 clones have a queued or completed
backfill; only `lifecycle` shims are installed and the commit is not refused;
a following `wb setup --check` exits `0`.

### AC: second-run-writes-nothing

**Requirements:** machine-setup#req:converge-idempotently

**Given** a machine on which `wb setup` has just succeeded
**When** the user runs `wb setup` again
**Then** no file under the home directory or projects root changes content or
modification time, no receipt is written, and every item reports `ok`.

### AC: check-is-offline

**Requirements:** machine-setup#req:check-is-offline-and-read-only

**Given** a converged Linux machine whose excludes file lost the codegrapher
catalog entry's `.codegraph/` glob
**When** the user runs `strace -f -e trace=connect wb setup --check --format json`
from outside any worktree, and separately from inside a worktree
**Then** both exit `1` with `gitignore:codegrapher` as `drift`; the trace shows
no `connect` to an `AF_INET` or `AF_INET6` address; no file changes,
including the worktree's heartbeat.

### AC: path-resolved-per-machine

**Requirements:** machine-setup#req:per-machine-resolution

**Given** two machines with codegrapher first on `PATH` at
`~/.local/bin/codegrapher` and `/opt/homebrew/bin/codegrapher`
**When** `wb setup` runs on each
**Then** each `wb.yaml` names its own absolute path, and
`wb hooks lifecycle check` exits `0` on both.

### AC: shadowed-copy-is-a-note

**Requirements:** machine-setup#req:per-machine-resolution

**Given** specscore first on `PATH` at `~/.local/bin/specscore` and a second
copy in `~/go/bin` off `PATH`; separately, codegrapher only in two
directories, neither on `PATH`
**When** the user runs `wb setup --format json` in each case
**Then** the first reports `cli:specscore` `ok` with a `shadowed` note and
does not raise the exit code; the second reports `ambiguous` for the
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
the symlink, which remains a symlink, and leaves a timestamped backup.

### AC: version-skew-deferred

**Requirements:** machine-setup#req:version-skew

**Given** a running daemon whose executable is a wb release without
version-2 support
**When** the user runs `wb setup`
**Then** `wb.yaml` contains no version-2 key, the lifecycle item reports
`deferred-version-skew` naming the daemon executable, running that
executable as `<daemon-wb> hooks lifecycle check` still exits `0`, and the
command exits `1`.

### AC: nothing-published

**Requirements:** machine-setup#req:machine-local-state

**Given** a converged machine configured for `wb remote publish`
**When** the user publishes and inspects the published record
**Then** it holds no path, setting, excludes content or receipt; the local
receipt has mode `0600`.

### AC: usage-errors

**Requirements:** machine-setup#req:output-contract

**Given** an installed wb
**When** the user runs `wb setup --only nosuchitem`, then
`wb setup --check --overwrite gitignore:codegrapher`
**Then** both exit `2` before any read of harness settings or any write, each
naming the offending flag.

### AC: no-tool-names-in-code

**Requirements:** machine-setup#req:item-inventory

**Given** the wb source tree
**When** `grep -rniE 'codegrapher|specscore|cover100' --include='*.go' internal/setup cmd/wb/setup*.go` runs, excluding tests
**Then** it finds no match.

## Open Questions

- **Harness settings.** Only recorded decisions are applied. Pending from the
  2026-09-18 analysis: decision 1 (transcript retention, 90 days) and
  decision 7 (the nudge; see [Expert Tool Routing](../expert-tool-routing/README.md)).
  Decision 2 (guard rewrites rather than refuses) is decided; #637 is open,
  implemented in PR #645. Where are decisions recorded so setup can read them:
  a `setup.decisions` block in `wb.yaml`?
- **Catalog fields.** The executor template, ignore globs, test-selection
  template and skill-sync argv need new fields in the cli-helpers catalog
  (strongo/cli-helpers). Until they exist, those items report `unsupported`.
- **golangci-lint** is not in the catalog (wb's gates run a pinned `go run`);
  specscore has no skill-sync verb. Should either change?

---
*This document follows the https://specscore.md/feature-specification*
