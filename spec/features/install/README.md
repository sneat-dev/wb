---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Install

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/install?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/install?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/install?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/install?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

`wb install` lists the other fleet CLIs relevant to wb (`specscore`,
`codegrapher`, `cover100`), each with its live installed status, and
`wb install <name>...` installs named ones consistently with how wb itself was
installed. `wb upgrade` is the fleet-wide counterpart: it brings every
*installed* catalog CLI to its latest release, named ones or all of them with
`--all`, including wb itself — `wb self-update` is exactly `wb upgrade wb`.
The behavior is not specified here: wb binds the shared
[strongo/cli-helpers](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=explore)
library (`github.com/strongo/cli-helpers/cliinstall`), whose Feature owns the
catalog, status probing, destination policy, Homebrew-cask and direct-release
install methods, the upgrade policy, and every failure guarantee. This Feature
specifies only what is wb's own — the command wiring and wb's exit-code
mapping.

## Synopsis

```
wb install                          # list fleet CLIs relevant to wb, with live status
wb install --all                    # list every catalog CLI, not just the ones relevant to wb
wb install specscore                # show details/relevance/plan for specscore, confirm once, install it
wb install specscore codegrapher --yes  # install several, skipping the confirmation prompt
wb install specscore --dry-run      # report the plan without installing anything
wb install nosuchcli                # refused before any confirmation, network request, or write
wb install --format json            # machine-readable listing/result

wb upgrade                          # read-only report over every installed catalog CLI, plus wb
wb upgrade --check                  # same report; exits findings when an upgrade is available
wb upgrade --all                    # upgrade every installed catalog CLI, plus wb, after one confirmation
wb upgrade specscore --dry-run      # report the plan without upgrading anything
wb upgrade specscore --yes          # skip the confirmation prompt
wb upgrade wb                       # exactly `wb self-update`: same Config, same after-update hook
wb upgrade nosuchcli                # refused before any confirmation, network request, or write
wb upgrade --format json            # machine-readable report/result
```

## Problem

wb is used alongside sibling fleet CLIs — `specscore` lints every checkout's
`spec/` tree through wb's own `ci` profile, `codegrapher` keeps a code index
fresh via a wb lifecycle hook, and `cover100` opens a single repository's
coverage (measured fleet-wide by `wb coverage`) as a zoomable treemap — but wb
had no way to tell a user any of this or help them install a sibling CLI
consistently with how they installed wb itself.

This is the same problem `self-update` already solved for wb's own binary:
detect the install method, resolve a release, verify it, and place it safely.
Installing a *different* CLI needs the identical machinery plus a catalog of
identities and a destination policy, which now live once in
`github.com/strongo/cli-helpers/cliinstall` rather than being rederived here.

## Behavior

### Command surface

#### REQ: command-name

wb MUST expose the command as `wb install`, taking zero or more target-name
positional arguments.

#### REQ: library-provided-behavior

The command MUST obtain its behavior from
`github.com/strongo/cli-helpers/cliinstall`'s `cobracmd.New` adapter rather
than reimplementing it. The catalog, relevance matrix, status probing,
destination policy, Homebrew-cask and direct-release install methods,
verification, confirmation gating, dry run, and batch reporting are inherited
from that library's Feature and MUST NOT be restated or reinterpreted here. wb
MUST NOT hand-roll its own catalog entries, process execution, or
package-manager invocation code.

#### REQ: flag-surface

The command MUST expose `--all`, `--yes` (short `-y`), `--dry-run`, `--dir`,
and `--format text|json`, bound to the library's corresponding options.

### wb's configuration of the library

#### REQ: wb-host-identity

wb MUST identify itself to the library by its catalog id, `"wb"`
(cli-install#req:host-identity-from-catalog). A host id absent from the
compiled catalog is a programming error the command constructor panics on,
caught by this repository's own tests, never a runtime state a user sees. The
same catalog entry (`github.com/strongo/cli-helpers/cliinstall`'s
`catalog_wb.go`) is what wb's own `self-update` command builds its
`selfupdate.Config` from, so the two commands can never disagree about how wb
itself is released (cli-install#req:catalog-identity-single-source; see
[Self-Update](../self-update/README.md#req-wb-release-identity)).

### Upgrading

#### REQ: upgrade-command-name

wb MUST expose the command as `wb upgrade`, taking zero or more target-name
positional arguments, with no `update` alias
(cli-install#req:update-alias-policy: "`upgrade` MUST NOT get an `update`
alias" — only `self-update` keeps one).

#### REQ: upgrade-library-provided-behavior

The command MUST obtain its behavior from
`github.com/strongo/cli-helpers/cliinstall`'s `cobracmd.NewUpgrade` adapter
rather than reimplementing it. Per-target upgrade policy (managed redirect or
executable command, manual replacement, ahead-of-latest, ambiguous refusal,
non-release-build skipping), release resolution, batch confirmation, dry run,
`--check` reporting and the upgrades-available signal are inherited from that
library's Feature and MUST NOT be restated or reinterpreted here.

#### REQ: upgrade-flag-surface

The command MUST expose `--all`, `--check`, `--yes` (short `-y`), `--dry-run`,
and `--format text|json`, bound to the library's corresponding options. There
is no `--dir`: upgrade always acts on the copy status-probing already located,
never a caller-chosen destination.

#### REQ: upgrade-host-config-and-hook

wb MUST configure `cobracmd.UpgradeCommandOptions.HostConfig` and
`HostAfterUpdate` with the EXACT SAME `selfupdate.Config` value and
after-update hook `wb self-update`'s own command configures — not a second
call that happens to build an equal-looking value — so `wb upgrade wb`
reaches the identical library call `wb self-update` does
(cli-install#req:host-target-is-running-binary,
cli-install#req:self-update-equals-upgrade-self). wb's after-update hook
(daemon restart, then skills re-sync) MUST be a single shared implementation
both commands configure, never two independent copies that could drift.

### Exit codes

#### REQ: exit-code-mapping

The command MUST report through wb's documented three exit codes and MUST NOT
introduce a fourth: `0` for success (including a no-op batch, e.g. every
target already installed, and a `--dry-run` report), `1` for every failure —
an unknown target, no usable destination directory, a destination already
occupied by a file the library will not overwrite, and every failure kind
[self-update](../self-update/README.md#req-exit-code-mapping) already maps
(ambiguous detection, release-lookup or download failure, checksum mismatch,
permission denied, a refused downgrade, an unhonorable version pin, or a
managed command failing) — and `2` stays reserved for an invocation Cobra
itself rejects before any command starts. This includes the three kinds
`cli-install` appends after `KindManagedCommand`
(`KindUnknownTarget`, `KindNoInstallDir`, `KindDestinationExists`), each
mapped explicitly rather than falling into a default branch
(cli-install#req:host-owned-exit-codes). No message from this command carries
a `self-update:` prefix, so a script that greps for one to distinguish the two
commands cannot mistake one for the other.

#### REQ: upgrade-exit-code-mapping

`wb upgrade` MUST use the identical error mapper `wb install` does, extended
with the upgrades-available method `cli-install#req:upgrade-check` requires
(cli-install#req:host-owned-exit-codes: "The upgrade command MUST use the
same error mapper"), so every failure kind maps exactly as
[REQ: exit-code-mapping](#req-exit-code-mapping) already maps it for install.
`--check` (or the bare, no-argument report) with at least one target reporting
an available or undetermined upgrade MUST map onto wb's `exitFindings`,
mirroring how [self-update maps its own `UpdateAvailable`
signal](../self-update/README.md#req-exit-code-mapping)
(cli-install#req:upgrade-check: "a host maps it as its self-update maps
UpdateAvailable"). wb reserves no fourth exit code for "an upgrade is
available" any more than self-update does.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [strongo/cli-helpers: CLI Install Command Library](https://specscore.studio/app/github.com/strongo/cli-helpers/spec/features/cli-install?op=explore) | Owns the behavior contract this Feature binds. wb is a consumer; behavior changes belong there. |
| [Self-Update](../self-update/README.md) | Sibling command built on the same fleet catalog (`cliinstall.ByID("wb")`); `wb install wb` is reported as already installed with a `wb self-update` pointer rather than reinstalling; `wb upgrade wb` and `wb self-update` reach the identical library call (REQ: upgrade-host-config-and-hook, cli-install#req:self-update-equals-upgrade-self). |

## Acceptance Criteria

### AC: lists-relevant-fleet-clis

**Requirements:** install#req:command-name, install#req:library-provided-behavior

**Given** an installed `wb` binary
**When** the user runs `wb install` with no arguments
**Then** the command lists the fleet CLIs relevant to wb (`specscore`,
`codegrapher`, `cover100`), each with its live status, without any network
request or filesystem write.

### AC: unknown-target-refused

**Requirements:** install#req:exit-code-mapping

**Given** an installed `wb` binary
**When** the user runs `wb install nosuchcli`
**Then** the command fails before any confirmation, network request, or
write, exits `1`, and the message names the unknown target and lists valid
catalog ids, carrying no `self-update:` prefix.

### AC: shared-failures-map-like-self-update

**Requirements:** install#req:exit-code-mapping

**Given** a release lookup that fails
**When** the user runs `wb install <name> --yes` and, separately,
`wb self-update` fails for the identical underlying reason
**Then** both exit with wb's `exitFindings` code and neither message carries
a `self-update:` prefix on the install side.

### AC: upgrade-unknown-target-refused

**Requirements:** install#req:upgrade-exit-code-mapping

**Given** an installed `wb` binary
**When** the user runs `wb upgrade nosuchcli`
**Then** the command fails before any confirmation, network request, or
write, and exits `1` naming the unknown target and listing valid catalog ids.

### AC: self-update-equals-upgrade-self

**Requirements:** install#req:upgrade-host-config-and-hook

**Given** the wb source tree
**When** `wb self-update` and `wb upgrade` are built
**Then** both commands' host `selfupdate.Config` and after-update hook come
from the identical constructor and factory function — never two independently
maintained copies — so `wb self-update <flags>` and `wb upgrade wb <flags>`
are wired to reach the same library call and the same after-update behavior
(cli-install#req:self-update-equals-upgrade-self).

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
