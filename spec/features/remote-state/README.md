---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Remote State

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-state?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb remote` shares fleet state across machines through a pluggable store:

- `wb remote publish` — scan this machine (attention repositories + every
  task worktree, active or orphaned, each with its owner state) and publish
  one snapshot keyed `<login>/<machine>`
- `wb remote status` — cross-machine worklist from the store, with `STALE`
  flags for old snapshots and error rows for undecodable entries
- `wb remote machines` — one line per machine with publish age
- `wb sync --publish` — publish after a successful sync

The only provider is `git`: a team (or personal) repository holding
`machines/<login>/<machine>/snapshot.yaml`, cloned to the canonical fleet
location. Design: `docs/superpowers/specs/2026-08-23-remote-state-design.md`.

## Problem

WB sees one machine. Reconciliation across a laptop, a VM, and teammates'
machines — "is there unpushed work anywhere?", "who has a worktree on
task-7?" — needs every machine's state in one place with history.

## Behavior

### Store layout and provider

#### REQ: remote-store-layout

The store MUST hold one file per machine at
`machines/<login>/<machine>/snapshot.yaml`, cloned to the canonical fleet
location.

#### REQ: remote-provider-pluggable

The store MUST be reachable through a pluggable provider abstraction. The
only implementation MUST be `git`. No provider or the code around it MUST
import any synchestra module.

#### REQ: remote-write-scope

Publishing MUST write only the state repository clone; it MUST NOT write to
any other clone.

### Machine identity

#### REQ: remote-machine-required

`machine` MUST be configured explicitly; there MUST be no hostname fallback.

### Publish sequence and heartbeat

#### REQ: remote-publish-heartbeat

Every publish MUST create a commit: `published_at` MUST advance even when
fleet state is otherwise unchanged, because it is one of the two inputs to
the machine's effective heartbeat that `wb remote status --stale` uses (the
other being `last_seen_at`, stamped by claim activity — see the
remote-claims feature). A byte-identical snapshot (same timestamp) MUST be
the only no-op.

#### REQ: remote-publish-concurrent-rebase

Two machines publishing concurrently MUST both land: a push rejected by a
concurrent publish MUST be retried once after rebasing onto the newly
pushed state.

### Status and machines rendering

#### REQ: remote-status-rendering

`wb remote status` MUST report a cross-machine worklist from the store, MUST
flag a machine as stale exactly when its effective heartbeat (the later of
`published_at` and `last_seen_at`) is older than the `--stale` window, and
MUST render an error row for any entry that cannot be decoded rather than
dropping it.

#### REQ: remote-machines-rendering

`wb remote machines` MUST report one line per machine including both its
publish age (PUBLISHED) and its effective-heartbeat age (SEEN); STALE MUST
key off SEEN, not PUBLISHED alone.

#### REQ: remote-status-exit-code

`wb remote status` MUST exit 0 when some entries are undecodable.

## Acceptance Criteria

### AC: pluggable-store-with-git-provider

**Requirements:** remote-state#req:remote-store-layout, remote-state#req:remote-provider-pluggable, remote-state#req:remote-write-scope

Fleet state lives behind a pluggable provider interface with `git` as the
only implementation today: one file per machine under
`machines/<login>/<machine>/snapshot.yaml` in a repository cloned to the
canonical fleet location, and publishing never touches any other clone.

### AC: explicit-machine-identity

**Requirements:** remote-state#req:remote-machine-required

A machine's identity in the store comes only from configuration; there is no
implicit hostname-derived fallback to silently misidentify a machine.

### AC: durable-heartbeat-and-concurrent-publish

**Requirements:** remote-state#req:remote-publish-heartbeat, remote-state#req:remote-publish-concurrent-rebase

Every publish advances `published_at` (except a byte-identical repeat),
feeding the effective heartbeat that staleness detection relies on, and two
machines publishing at the same time both succeed via a rebase-and-retry on
push rejection.

### AC: cross-machine-visibility

**Requirements:** remote-state#req:remote-status-rendering, remote-state#req:remote-machines-rendering, remote-state#req:remote-status-exit-code

`wb remote status` and `wb remote machines` give a readable cross-machine
view — worklist with staleness and error rows, and a one-line-per-machine
summary carrying both the publish age and the effective-heartbeat (SEEN)
age — and a store containing undecodable entries never blocks a zero exit
code.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
