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

- `wb remote publish` — scan this machine (attention repositories + live task
  worktrees) and publish one snapshot keyed `<login>/<machine>`
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

## Acceptance criteria

- Publishing writes only the state repository clone; never another clone.
- Every publish creates a commit: `published_at` advances even when fleet state is unchanged, because `wb remote status --stale` uses it as the machine's heartbeat. A byte-identical snapshot (same timestamp) is the only no-op.
- Two machines publishing concurrently both land (rebase on rejection, once).
- `machine` must be configured explicitly; there is no hostname fallback.
- `wb remote status` exits 0 when some entries are undecodable.
- No import of any synchestra module.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
