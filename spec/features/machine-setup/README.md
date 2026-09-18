---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Machine Setup

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/machine-setup?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Depends On:** [Install](../install/README.md), [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md), [Code Index Freshness](../code-index-freshness/README.md)

## Summary

`wb setup` makes any Linux, macOS or Windows machine an efficient agent
machine with one idempotent command, and `wb setup --check` reports every way
the machine has drifted from that state, without network access. It owns no
installation, hook, or skill behavior of its own: each item delegates to the
existing expert command (`wb install`/`wb upgrade`, `wb hooks install`,
`wb hooks agent install`, `wb skills sync`, and each fleet CLI's own skill
sync). Its contribution is the **inventory**: one list of what an agent
machine needs, resolved per machine, checked in one pass, converged in one
pass.

## Synopsis

```
wb setup --check                  # read-only drift report; no network; exit 1 on drift
wb setup --check --format json    # the same report as a stable JSON document
wb setup --dry-run                # full plan with a diff per write; writes nothing
wb setup                          # converge every item that does not overwrite foreign config
wb setup --only cli,gitignore     # restrict to item groups (id prefixes before ':')
wb setup --overwrite agent-guard:claude  # accept the shown diff for one exact item id
```

## Problem

On 2026-09-18 the founder's VM ran agents that had none of the efficient path
in place (`~/.wb/reports/sdlc-logging-gaps-2026-09-18/REPORT.md` §7, §9):

- the wb pre-tool-use guard was not registered, so 92% of heavy commands
  bypassed `wb run` admission;
- no `code-index` lifecycle executor was configured (`~/.config/wb/wb.yaml`
  has no `hooks:` section), so no canonical clone of wb, sneat-core-modules or
  backstage had a CodeGrapher index, and 3,015 symbol greps ran against 0
  codegrapher analysis calls;
- `.codegraph/` was not ignored, so a 134 MB untracked directory tripped wb's
  `dirty-worktree` refusals the one time an index was created.

Each piece has a WB command. Nothing lists the pieces, so a new machine, a
reinstalled machine, or a machine whose CLIs moved from `~/.local/bin` to
`/opt/homebrew/bin` silently loses them. The founder's ask: "make sure we have
a script or wb command to configure any machine."

### Why `wb setup`, not `wb install`

`wb install` is a thin binding of the cli-helpers library; its Feature forbids
wb from adding behavior to it (install#req:library-provided-behavior). Machine
setup is a composition over several commands, the same shape as
[`wb cleanup`](../cleanup-orchestration/README.md) over the scoped cleanup
commands. `wb doctor` was rejected because it reads as report-only; `setup`
names both the check and the convergence and sits in the existing
"Learn and configure" group beside `install`, `upgrade` and `skills`.

## Behavior

### REQ: item-inventory

`wb setup` MUST evaluate this fixed, ordered set of items, each with a stable
`id` used in output, `--only` and `--overwrite`:

| Id | Desired state | Delegates to |
|---|---|---|
| `cli:<name>` | each catalog CLI relevant to wb (today `codegrapher`, `specscore`) installed; in apply mode upgraded to its latest release, in `--check` at or above wb's compiled minimum version | `wb install` / `wb upgrade` |
| `git-hooks` | managed shims installed in every canonical clone under the projects root, including the lifecycle profile from [Code Index Freshness](../code-index-freshness/README.md) | `wb hooks install --fleet` / `wb hooks repair` |
| `lifecycle:code-index` | a `code-index` executor and `checkout-updated` binding in the trusted user `wb.yaml` | the lifecycle config writer |
| `gitignore:codegraph` | `.codegraph/` present in the global Git excludes file | a line-append writer |
| `agent-guard:<harness>` | the wb pre-tool-use guard registered for each present harness that supports it (Claude Code today) | `wb hooks agent install` |
| `skills:<cli>` | the CLI-matched Agent Skills of wb, codegrapher and specscore present in every present harness | `wb skills sync`, `codegrapher skills sync`; specscore reports `unsupported` until it ships a skill-sync verb |
| `harness-settings` | only settings with a recorded founder decision (see Open Questions) | a JSON-merge writer |
| `daemon` | report only: whether the wb daemon is supervised and ready | none; `wb daemon` owns it |

Adding an item is a change to this Feature. An item MUST NOT be enabled by
repository content.

### REQ: per-machine-resolution

Every path an item writes MUST be resolved on the machine running the
command, never copied from another machine or from repository content:

- the `code-index` executor `run:` path MUST be the absolute path of the
  installed `codegrapher` the `cli:codegrapher` status probe located (for
  example `~/.local/bin/codegrapher`, `/opt/homebrew/bin/codegrapher`, or
  `%LOCALAPPDATA%\Programs\codegrapher\codegrapher.exe`), and MUST pass the
  trust checks of trusted-repository-update-hooks#req:generic-executors before
  it is written;
- the global excludes file MUST be `git config --global core.excludesFile`
  when set, else `$XDG_CONFIG_HOME/git/ignore`, else `~/.config/git/ignore`
  (`%USERPROFILE%\.config\git\ignore` on Windows);
- harness settings files MUST follow each harness's own resolution
  (`$CLAUDE_CONFIG_DIR`, then `~/.claude/settings.json`).

When the probe finds more than one copy of a CLI, the item MUST report
`ambiguous` and write nothing for it.

### REQ: check-is-offline-and-read-only

`wb setup --check` MUST NOT open a network connection, start a process that
does, or write any file, including receipts. It reports each item as `ok`,
`drift`, `missing`, `conflict`, `ambiguous`, `pending-decision`,
`unsupported` or `not-checked`. Release freshness needs the network, so
`--check` compares CLIs only against wb's compiled minimum versions and
reports upgrade availability as `not-checked`.

### REQ: converge-idempotently

`wb setup` MUST bring every `drift` and `missing` item to its desired state
and MUST be idempotent: a second run on a converged machine performs no write
and reports every item `ok`. Invoking `wb setup` without `--check` or
`--dry-run` is the confirmation for every delegated command (it passes their
`--yes`); it never prompts, per wb's non-interactive contract. Items run in
inventory order because later items depend on earlier ones (`lifecycle:code-index` needs `cli:codegrapher`). A
failed item MUST NOT stop independent later items; dependent items report
`blocked` with the blocking item's id.

### REQ: never-overwrite-foreign-config

An item MUST only add content it owns or replace content it previously wrote
and recorded. When the desired change would modify or remove content WB did
not write (a user-edited `code-index` executor, a different pre-tool-use hook
entry, a hand-set `core.excludesFile` content line), the item MUST report
`conflict` with a unified diff and MUST NOT write, unless the invocation
names that item in `--overwrite <id>`. `--dry-run` MUST print the diff of
every planned write. Every write MUST be atomic (write-temp, fsync, rename)
and keep the previous file as `<file>.wb-setup.bak`.

### REQ: machine-local-state

Everything `wb setup` reads or writes is machine-local and MUST NOT be
published by `wb remote publish`, the hub, or any fleet event: resolved
executable paths, harness settings, the trusted `wb.yaml`, the global excludes
file, and the setup receipt. The receipt MUST be written to
`<projects-root>/.wb/setup/receipts/<timestamp>.json` with mode `0600`. A
fleet event MAY record only item ids and their states.

### REQ: output-contract

Text output MUST print one line per item (`<state>  <id>  <detail>`) and a
final summary. `--format json` MUST emit one document:
`{"version":1,"os","arch","mode":"check|dry-run|apply","items":[{"id","state","detail","diff","action","blocked_by"}],"summary":{"<state>":n}}`,
with `diff` present only for `conflict` and in `--dry-run`. Exit codes follow
wb's contract: `0` when every item is `ok` (or, in apply mode, converged);
`1` when any item is `drift`, `missing`, `conflict`, `ambiguous`, `blocked`
or failed; `2` for a usage error (unknown item id, `--check` combined with
`--overwrite`). `pending-decision` and `not-checked` never raise the exit code.

### REQ: platform-parity

The command MUST run on Linux, macOS and Windows. An item a platform cannot
support MUST report `unsupported` with the reason and MUST NOT fail the run.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [Install](../install/README.md) | `cli:*` items call the same library entry points as `wb install` and `wb upgrade`; setup adds no catalog entry and no install method. |
| [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md) | `lifecycle:code-index` writes the trusted user config that feature reads; the lifecycle runner stays tool-agnostic because setup writes data, not code. |
| [Code Index Freshness](../code-index-freshness/README.md) | Defines the executor arguments, the lifecycle Git-hook profile and the disk budget that `git-hooks` and `lifecycle:code-index` install. |
| [Expert Tool Routing](../expert-tool-routing/README.md) | `skills:*` and `agent-guard:*` deliver that Feature's skill descriptions and point-of-use nudges to a machine. |
| [Disk Reclaim](../disk-reclaim/README.md) | Setup never deletes anything; reclaim owns deletion. |

## Acceptance Criteria

### AC: fresh-machine-converges

**Requirements:** machine-setup#req:item-inventory, machine-setup#req:converge-idempotently

**Given** a machine with wb installed and no codegrapher, no `hooks:` section
in `wb.yaml`, no `.codegraph/` ignore line, and no registered agent guard
**When** the user runs `wb setup --format json`
**Then** it exits `0`, every item in the inventory reports `ok` or an
explicit `pending-decision`/`unsupported`, and `wb setup --check` then exits
`0` with no `drift` or `missing` item.

### AC: second-run-writes-nothing

**Requirements:** machine-setup#req:converge-idempotently

**Given** a machine on which `wb setup` has just succeeded
**When** the user runs `wb setup` again
**Then** no file's content or modification time changes, and every item
reports `ok`.

### AC: check-reports-drift-offline

**Requirements:** machine-setup#req:check-is-offline-and-read-only, machine-setup#req:output-contract

**Given** a converged machine from which `.codegraph/` was removed from the
global excludes file, running with networking disabled (no route, or
`HTTPS_PROXY` pointing at a closed port)
**When** the user runs `wb setup --check --format json`
**Then** it exits `1`, `gitignore:codegraph` reports `drift`, CLI upgrade
availability reports `not-checked`, no connection attempt is observed, and no
file under the home directory or projects root is created or modified.

### AC: executable-path-resolved-per-machine

**Requirements:** machine-setup#req:per-machine-resolution

**Given** two machines with codegrapher installed at `~/.local/bin/codegrapher`
and `/opt/homebrew/bin/codegrapher` respectively
**When** `wb setup` runs on each
**Then** each machine's `wb.yaml` `code-index` executor names its own absolute
path, and `wb hooks lifecycle check` exits `0` on both.

### AC: ambiguous-cli-refused

**Requirements:** machine-setup#req:per-machine-resolution

**Given** codegrapher installed both in `~/.local/bin` and in `~/go/bin`
**When** the user runs `wb setup`
**Then** `cli:codegrapher` and `lifecycle:code-index` report `ambiguous` and
`blocked` respectively, naming both paths, nothing is written for either, and
the command exits `1`.

### AC: foreign-config-not-overwritten

**Requirements:** machine-setup#req:never-overwrite-foreign-config

**Given** a user-edited `code-index` executor with `timeout: 10m`
**When** the user runs `wb setup`
**Then** `lifecycle:code-index` reports `conflict` with a unified diff,
`wb.yaml` is byte-identical afterwards, and the command exits `1`; running
`wb setup --overwrite lifecycle:code-index` then applies exactly that diff and
leaves `wb.yaml.wb-setup.bak` holding the previous content.

### AC: dry-run-shows-every-write

**Requirements:** machine-setup#req:never-overwrite-foreign-config, machine-setup#req:output-contract

**Given** a machine with several `missing` items
**When** the user runs `wb setup --dry-run --format json`
**Then** every item that would write carries a `diff`, no file changes, and
the exit code is `0`.

### AC: undecided-settings-not-applied

**Requirements:** machine-setup#req:item-inventory

**Given** founder decision 1 (transcript retention) has no recorded outcome
**When** the user runs `wb setup`
**Then** `harness-settings` reports `pending-decision` naming the decision,
`~/.claude/settings.json` gains no `cleanupPeriodDays` key, and the exit code
is not raised by that item.

### AC: nothing-published

**Requirements:** machine-setup#req:machine-local-state

**Given** a converged machine configured for `wb remote publish`
**When** the user runs `wb remote publish` and inspects the published record
**Then** it contains no executable path, settings value, excludes-file content,
or setup receipt; the receipt exists locally with mode `0600`.

### AC: usage-errors

**Requirements:** machine-setup#req:output-contract

**Given** an installed wb
**When** the user runs `wb setup --only nosuchitem` and, separately,
`wb setup --check --overwrite gitignore:codegraph`
**Then** both exit `2` before any read of harness settings or any write, and
each message names the offending flag.

### AC: windows-parity

**Requirements:** machine-setup#req:platform-parity, machine-setup#req:per-machine-resolution

**Given** a Windows machine with codegrapher installed as a direct `.exe`
**When** the user runs `wb setup`
**Then** the executor path ends in `.exe`, the excludes file is resolved under
`%USERPROFILE%`, and any item the platform cannot support reports
`unsupported` instead of failing.

## Non-goals

- Installing Go, Node, Git or harnesses themselves.
- Deciding harness settings; setup only applies recorded decisions.
- Starting or supervising the daemon; `daemon` is report-only.

## Open Questions

- **Harness settings to apply.** Only recorded founder decisions are applied.
  Candidates from REPORT.md §8: decision 1 (`cleanupPeriodDays` 90),
  decision 2 (guard registration, decided: rewrite inside managed worktrees,
  shipped by #637), decision 7 (codegrapher nudge; see
  [Expert Tool Routing](../expert-tool-routing/README.md)). Where are decision
  outcomes recorded so setup can read them: a `wb.yaml` `setup.decisions`
  block, or backstage decision artifacts?
- **Where the `code-index` executor template lives.** Recommendation: in the
  cli-helpers catalog entry for codegrapher (beside its `relevance` text), so
  wb carries no CodeGrapher-specific data and tool-plugins' retirement holds.
  Alternative: a template inside wb's setup inventory.
- **golangci-lint.** It is not in the cli-helpers catalog, and wb's own gates
  run a pinned `go run …golangci-lint@<sha>`. Should it join the catalog so
  `cli:golangci-lint` can exist, or stay out because the gates do not need it?
- **specscore skills.** specscore has `specscore agent setup` (per-project
  instruction files) but no CLI-matched skill sync like `wb skills sync` and
  `codegrapher skills sync`. Should specscore-cli gain one, so
  `skills:specscore` can converge instead of reporting `unsupported`?
- **Scope of `git-hooks`.** Every canonical clone under the projects root, or
  only repositories matched by a `setup.repositories` include list?

---
*This document follows the https://specscore.md/feature-specification*
