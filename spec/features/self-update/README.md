---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Self-Update

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-update?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-update?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-update?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-update?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

`wb self-update` (alias `wb update`) brings a running `wb` binary to the latest
release. The behavior is not specified here: wb binds the shared
[strongo/selfupdate](https://specscore.studio/app/github.com/strongo/selfupdate/spec/features/self-update?op=explore)
library, whose Feature owns detection, release resolution, checksum
verification, atomic replacement, and every failure rule. This Feature specifies
only what is wb's own — the command surface, wb's configuration of the library,
and where wb's contract deviates.

## Synopsis

```
wb self-update                          # detect, then self-replace (manual) or upgrade through Homebrew
wb self-update --check                  # report availability only; never modifies
wb self-update --check --format json    # machine-readable verdict
wb self-update --yes                    # skip the confirmation prompt
wb self-update --version v0.24.0        # install an exact release (manual installs)
wb self-update --version 0.23.2 --allow-downgrade   # roll back
wb update                               # alias for `self-update`
```

## Problem

wb ships through a Homebrew cask (`brew install --cask sneat-dev/tap/wb`) and
direct GitHub release archives, plus `go install` for anyone building from
source. Users on any of them have no first-class way to reach the current
release, and agents driving wb cannot tell they are on a stale binary.

The safety rules that make self-update non-trivial are the same for every CLI,
which is why they live in the shared library rather than here. What is genuinely
wb's own is small and worth stating precisely: wb documents a three-code exit
contract that an agent branches on, wb ships a cask rather than a formula, wb
publishes no Windows build, and wb's version placeholder is `unknown` rather
than the `dev` other CLIs use.

## Behavior

### Command surface

#### REQ: command-and-alias

wb MUST expose the command as `wb self-update`, and MUST accept `wb update` as
an alias resolving to identical behavior. The canonical name is `self-update`
because in a CLI whose verbs act on other repositories (`wb sync`, `wb deps`,
`wb migrate`), a bare `update` does not say *what* is updated.

#### REQ: library-provided-behavior

The command MUST obtain its behavior from `github.com/strongo/selfupdate` rather
than reimplementing it. Install-method detection, stable-release resolution,
version comparison, pinned targets and the downgrade guard, asset download,
sha256 verification before extraction, atomic replacement, the post-swap version
check, and the guarantee that every failure leaves a working binary are
inherited from that library's Feature and MUST NOT be restated or reinterpreted
here. A behavior change belongs upstream, in the library, not in a wb-local
fork.

#### REQ: flag-surface

The command MUST expose `--check`, `--yes` (short `-y`), `--version <tag>`, and
`--allow-downgrade`, bound to the library's corresponding options. `--version`
here is `self-update`-local and MUST NOT collide with the root `wb --version`
that prints build identity.

#### REQ: check-json-format

`--check` MUST accept `--format json` and emit a single JSON document carrying
at least the current version, the latest release, and a `verdict` of
`up_to_date`, `update_available`, or `undetermined`. Because wb's exit codes
cannot distinguish "an update is available" from "the release lookup failed"
(see [REQ: exit-code-mapping](#req-exit-code-mapping)), this document is the
channel that MUST make them distinguishable. Human-readable output stays the
default, matching `wb status` and `wb check`.

### wb's configuration of the library

#### REQ: wb-release-identity

wb MUST configure the library with its own release identity: the GitHub
repository `sneat-dev/wb`; the binary name `wb`; release assets named
`wb_<version>_<os>_<arch>.tar.gz` with checksums at
`wb_<version>_checksums.txt`, as this project's GoReleaser publishes them; and
the supported platforms `darwin` and `linux` on `amd64` and `arm64`. A host
outside that set MUST be refused by the library's unsupported-platform rule
rather than attempting a swap wb publishes no asset for.

#### REQ: wb-homebrew-cask

wb MUST configure Homebrew as its managing package manager, with the upgrade
command `brew upgrade --cask wb` and structured executable argv `brew`,
`upgrade`, `--cask`, `wb`. wb ships as a cask, not a formula, so the command
MUST carry `--cask`. After confirmation it MUST run that argv through the
shared updater, then probe the stable `wb` launcher using `version --json`.
Homebrew remains the only writer of its cask binary. `--dry-run` MUST not run
the manager and a managed version pin MUST be refused. Scoop and WinGet MUST
NOT be configured while wb publishes no Windows build.

#### REQ: wb-version-identity

wb MUST supply the version `wb version` reports — the link-time `-ldflags` stamp
of a release build, otherwise the module version and VCS metadata the Go
toolchain embeds — and MUST declare as undetermined every string that version
can take when it identifies no release: `unknown`, wb's own final fallback, and
`(devel)`, what the Go toolchain stamps for a binary built from a source tree
rather than resolved from a module version. An undeclared placeholder is
compared as if it were a real version, which reports an update available *from*
a version that does not exist. A Go pseudo-version such as `v0.23.3-0.20260809071100-889b6d621f76`
is a known version that sorts below its release, not an undetermined one. The
post-swap version probe MUST use `version --json`, wb's machine-readable
spelling.

### Exit codes

#### REQ: exit-code-mapping

The command MUST report through wb's documented three exit codes and MUST NOT
introduce a fourth, mapping the library's outcomes and failure kinds onto them.
`0` means success: the swap completed, the Homebrew manager command completed, or the
binary is already current. `1` means the command ran and reported a finding or a
failure: an update is available under `--check`, or the run failed (ambiguous
install, network, checksum, permission, non-interactive without `--yes`, unknown
tag, refused downgrade). `2` stays reserved for a rejected invocation. This
deliberately differs from other consumers of the library, which reserve a
dedicated code for "update available"; callers needing that distinction in wb
use `--format json`.

#### REQ: permission-remedy-names-brew

When the library reports a permission failure, wb MUST print the executable's
path and a remedy naming both elevated permissions and wb's own installation
channel (`brew install --cask sneat-dev/tap/wb`), so the message is actionable
for a wb user specifically.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [strongo/selfupdate: Self-Update Library](https://specscore.studio/app/github.com/strongo/selfupdate/spec/features/self-update?op=explore) | Owns the behavior contract this Feature binds. wb is a consumer; behavior changes belong there. |
| [Fleet Status](../fleet-status/README.md) | Unrelated in mechanism. Self-update is the one wb command that deliberately writes to the wb install itself. |

## Acceptance Criteria

### AC: canonical-and-alias

**Requirements:** self-update#req:command-and-alias, self-update#req:flag-surface

**Given** an installed wb binary
**When** the user runs `wb self-update --check` and, separately, `wb update --check`
**Then** both invocations execute the same command and produce identical output and exit code, and `wb self-update --version <tag>` is taken as the pinned release rather than as a request for wb's build identity.

### AC: behavior-comes-from-the-library

**Requirements:** self-update#req:library-provided-behavior, self-update#req:wb-release-identity, self-update#req:wb-version-identity

**Given** the wb source tree
**When** the self-update command is built
**Then** detection, release resolution, verification, and replacement come from `github.com/strongo/selfupdate`, wb supplies only its release identity, version and undetermined placeholder, and no copy of that logic exists in wb's own tree.

### AC: homebrew-updates-through-manager-never-overwritten

**Requirements:** self-update#req:wb-homebrew-cask

**Given** a wb binary whose resolved path is inside a Homebrew Caskroom or Cellar
**When** the user runs `wb self-update`, including with `--yes` and with `--version <tag>`
**Then** it asks for confirmation (unless `--yes`), runs `brew upgrade --cask wb`
with structured argv, exits `0` after a successful manager command, and performs
no download, no direct write, and no replacement. A managed version pin is
refused, and `--dry-run` reports the manager command without running it.

### AC: wb-exit-codes-and-json-verdict

**Requirements:** self-update#req:check-json-format, self-update#req:exit-code-mapping, self-update#req:permission-remedy-names-brew

**Given** a newer published release, and separately a release lookup that fails
**When** the user runs `wb self-update --check` with and without `--format json`
**Then** an available update exits `1` and reports `update_available` in JSON, a failed lookup also exits `1` but is distinguishable in JSON, an up-to-date binary exits `0`, no fourth exit code is ever returned, and a permission failure names the executable path and wb's cask install command.

## Open Questions

- Should `wb self-update --check` run implicitly — once a day, cached — during an
  unrelated wb command, so a stale fleet binary is noticed without anyone asking?
  That trades wb's "no surprise network access" property for discoverability, and
  is deliberately out of scope here. The shared library carries the same question
  for all consumers.

---
*This document follows the https://specscore.md/feature-specification*
