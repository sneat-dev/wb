---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Trusted Repository Update Hooks

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/trusted-repository-update-hooks?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/trusted-repository-update-hooks?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/trusted-repository-update-hooks?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/trusted-repository-update-hooks?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Run trusted user-configured executors after WB changes a repository checkout, without embedding knowledge of any particular tool.

## Problem

WB changes checked-out source through several governed journeys: fleet sync,
repository-event sync, pull-request landing, and worktree landing. Tools such as
code indexes need to react to those changes, but embedding each tool in WB
couples release, installation, and execution policy to one product. Repository
content must not be able to add an executable that WB later runs automatically.

## Behavior

### REQ: trusted-user-configuration

Automatic lifecycle executors MUST be declared only in the user-owned
`~/.config/wb/wb.yaml` `hooks:` section. Repository content, including
`.wb/hooks.yaml`, MUST NOT define, replace, or enable a lifecycle executor.
Organization and repository policy is expressed by matching canonical
`host/owner/repository` identities in that trusted user file.

The existing user Git-hook policy MUST move into the same standard file under
`git_hooks:`. WB MUST NOT fall back to the former user
`~/.config/wb/hooks.yaml`; repository `.wb/hooks.yaml` remains the repository's
declarative Git-hook policy and cannot declare lifecycle executors.

### REQ: generic-executors

WB MUST know only an executor name, an absolute executable path, an argument
array, `cwd: repository`, `mode: coalesced`, a timeout, and `failure: warn`.
It MUST execute the binary directly without a shell, reject repository-local
executables, and provide only a minimal environment plus `WB_HOOK_EVENT`,
`WB_REPOSITORY`, `WB_CHECKOUT`, `WB_OLD_SHA`, `WB_NEW_SHA`, `WB_UPDATE_CAUSE`,
and `WB_OPERATION_ID` when one exists. WB MUST NOT contain a built-in
CodeGrapher registry, installer, updater, or graph-specific behavior.

### REQ: exact-update-trigger

WB MUST emit `checkout-updated` only after a successful checkout mutation. An
existing checkout qualifies only when its post-operation `HEAD` differs from
its pre-operation `HEAD`; an already-current pull, fetch-only operation,
dry-run, failed update, dirty-checkout skip, or update to another ref MUST NOT
execute a binding. A newly cloned canonical checkout qualifies with an empty
old SHA and its checked-out commit as the new SHA.

The trigger MUST cover canonical checkouts changed by `wb sync`, daemon
repository-event sync, `wb pr land`, and `wb worktree merge` canonical
fast-forward synchronization.

### REQ: matching-and-coalescing

Bindings MUST select the `checkout-updated` event and use include/exclude glob
patterns against a canonical repository identity. Exclusions win. In one WB
operation, repeated events for the same executor and checkout MUST coalesce to
one execution carrying the latest new SHA. Distinct updated repositories MUST
remain distinct executions; repositories that did not update MUST not enter
the batch.

### REQ: observable-outcome

Each attempted or coalesced execution MUST append a local receipt containing
the event, repository, checkout, old/new SHAs, executor name, timing, and
terminal disposition without retaining command output or credentials.
`failure: warn` MUST preserve the already-successful repository update and
report the hook failure. Configuration, dispatch, and receipt failures after
the checkout mutation MUST likewise warn without changing the truthful Git
outcome. Version 1 MUST NOT offer a failure mode that pretends the repository
update can be rolled back after the hook starts.

## Acceptance Criteria

### AC: updated-repositories-only

**Requirements:** trusted-repository-update-hooks#req:exact-update-trigger,
trusted-repository-update-hooks#req:matching-and-coalescing

**Given** one newly cloned repository, one pulled repository whose HEAD moves,
one already-current repository, and one dirty repository
**When** WB completes a fleet sync
**Then** the matching executor runs exactly once in each changed checkout and
does not run for the already-current or dirty repositories.

### AC: merge-then-refresh

**Requirements:** trusted-repository-update-hooks#req:exact-update-trigger,
trusted-repository-update-hooks#req:observable-outcome

**Given** WB lands a worktree or pull request and fast-forwards the checked-out
canonical target
**When** the canonical HEAD changes to the proven landing SHA
**Then** the matching executor receives the exact old and new SHAs and WB writes
a terminal receipt; no event fires when that canonical target is not checked
out or was already at that SHA.

### AC: repository-cannot-authorize-code

**Requirements:** trusted-repository-update-hooks#req:trusted-user-configuration,
trusted-repository-update-hooks#req:generic-executors

**Given** a repository adds lifecycle-looking executor configuration or points
at a binary within its checkout
**When** WB updates that repository
**Then** repository configuration cannot register the executor and a trusted
configuration that names the repository-local executable is refused before it
runs.

### AC: tool-agnostic-code-index

**Requirements:** trusted-repository-update-hooks#req:generic-executors

**Given** the trusted user config binds an executor whose argv is
`/opt/homebrew/bin/codegrapher sync .` to all repository identities
**When** a matching checkout changes
**Then** WB invokes that argv through the generic lifecycle runner, and no WB
code path branches on CodeGrapher's name or behavior.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
