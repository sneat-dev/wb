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

Automatic lifecycle executors MUST be declared only in the user-owned XDG
config file (`$XDG_CONFIG_HOME/wb/wb.yaml`, or `~/.config/wb/wb.yaml`)
`hooks:` section. The config MUST be a non-symlink regular file owned by the
current user or an administrator and MUST NOT be writable by an untrusted
principal. Repository content, including
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
executables, reject files not owned by the current user or root and files
writable by group or other users, and revalidate the resolved executable
identity immediately before execution. It provides only a minimal environment
plus `WB_HOOK_EVENT`,
`WB_REPOSITORY`, `WB_CHECKOUT`, `WB_OLD_SHA`, `WB_NEW_SHA`, `WB_UPDATE_CAUSE`,
and `WB_OPERATION_ID` when one exists. WB MUST NOT contain a built-in
CodeGrapher registry, installer, updater, or graph-specific behavior. On
Windows, WB MUST execute only direct `.exe` or `.com` files and MUST reject an
owner or ACL that grants broad write access.

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
patterns against a canonical repository identity. Exclusions win. Repeated
pending events for the same executor and checkout MUST coalesce across WB
operations and processes to one durable execution carrying the earliest old
SHA and latest new SHA. Distinct updated repositories MUST remain distinct
executions; repositories that did not update MUST not enter the batch.

### REQ: durable-non-blocking-delivery

The repository mutation path MUST durably enqueue matching executions and
return without waiting for an external executor. A detached worker is only a
wake-up mechanism: queued state remains authoritative when the worker cannot
start or is interrupted. Exactly one worker owner per state directory MUST
claim queue entries atomically, recover interrupted claims, run a bounded
number concurrently, and close the empty-queue shutdown race without blocking
enqueue for the duration of an external command.
Delivery is at least once: executors MUST be idempotent for a repository and
target commit. Before every attempt WB MUST resolve the checkout to its
physical directory and verify both its canonical repository identity and
current HEAD against the queued event. The XDG state, receipt, and
configuration paths MUST resolve outside the checkout and use trusted
non-symlink storage. A corrupt queue item MUST be quarantined without blocking
other valid work. WB SHOULD NOT start another detached process while a worker
visibly owns the queue.

### REQ: observable-outcome

Each attempted or coalesced execution MUST append a private local receipt containing
the event, repository, checkout, old/new SHAs, executor name, timing, and
terminal disposition. Standard output and standard error MUST be retained only
in private per-attempt files, bounded independently to 64 KiB, and referenced
by the receipt rather than copied into routine WB output. Receipts MUST name a
bounded failure class and message sufficient to find diagnostics and retry the
attempt without exposing command output or credentials.
`failure: warn` MUST preserve the already-successful repository update and
report the hook failure. Configuration, dispatch, and receipt failures after
the checkout mutation MUST likewise warn without changing the truthful Git
outcome. Version 1 MUST NOT offer a failure mode that pretends the repository
update can be rolled back after the hook starts.
Because execution is asynchronous, a failed attempt MUST remain visible in
worker health and status and MUST produce a warning on the next lifecycle
dispatch. A malformed receipt record MUST be isolated and reported rather than
hiding later valid receipts.

### REQ: operator-recovery-surface

WB MUST provide lifecycle-hook commands to validate trusted configuration and
executables, inspect queued/running/recent attempts, retry one exact failed
receipt, and explicitly plan or apply a backfill over existing canonical
repositories. Backfill MUST be read-only by default, use each repository's
current HEAD without changing Git, and pass through the same bindings, durable
queue, trust checks, and configured executor arguments as update-driven work.
It MUST also provide an explicit resume operation for stranded durable work
and a dry-run-by-default retention command. Failed attempts that have not yet
been surfaced MUST be protected from collection. Receipts MUST have a per-ID
index so an exact retry does not depend on scanning the append-only stream.

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

### AC: update-does-not-wait-for-indexing

**Requirements:** trusted-repository-update-hooks#req:durable-non-blocking-delivery,
trusted-repository-update-hooks#req:matching-and-coalescing

**Given** a matching executor is slow and another WB process reports a newer
commit for the same checkout
**When** both repository operations finish
**Then** neither waits for the executor, the pending record retains the latest
commit, worker concurrency stays bounded, and interrupted claimed work becomes
eligible again after worker recovery.

### AC: diagnose-retry-and-backfill

**Requirements:** trusted-repository-update-hooks#req:observable-outcome,
trusted-repository-update-hooks#req:operator-recovery-surface

**Given** one failed attempt and existing matching repositories that have never
emitted an update event
**When** the operator checks status, retries the receipt, and previews then
applies backfill
**Then** status points to bounded private diagnostics, retry enqueues the exact
failed executor and checkout revision under current trusted configuration,
preview changes nothing, and apply enqueues only matching canonical
repositories.

### AC: corrupt-state-isolation-and-retention

**Requirements:** trusted-repository-update-hooks#req:observable-outcome,
trusted-repository-update-hooks#req:operator-recovery-surface

**Given** valid work beside one malformed queue item, an interrupted worker,
and old receipts with diagnostics
**When** the operator runs status, resume, and previews then applies retention
**Then** valid work remains runnable, malformed state is quarantined and named,
worker failure remains visible, collection preserves unseen failures and the
newest configured receipts, and only explicitly applied candidates and their
own diagnostics are removed.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
