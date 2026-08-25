---
format: https://specscore.md/plan-specification
status: In Review
---

# Plan: Agent Session Move implementation plan

**Status:** In Review
**Source Feature:** agent-session-move
**Date:** 2026-08-25
**Owner:** codex
**Supersedes:** —

## Summary

Implement WB's portable agent-session movement from source checkpoint through
target tmux startup, receipt-gated custody, and follow-up messaging. The work
keeps WB as protocol owner while adding SSH first and the Synchestra courier
against the same fixed receiver contract.

## Approach

Build the durable identities, request/receipt types, configuration, and
idempotent state machine first. Layer the exact Git checkpoint and pinned
receiver on existing WB Git/worktree safety primitives, then add fixed harness
launching and SSH delivery, receipt-gated Work Log finalization, Synchestra as
a second courier, and typed messaging. Tests use temporary Git remotes and fake
command adapters so normal validation never requires a live VM, SSH, tmux, or
AI harness; the final smoke test may exercise `hetzner-vm1` explicitly.

## Tasks

### Task 1: Add stable session identity and durable handoff state

**Verifies:** agent-session-move#ac:failed-or-retried-delivery-is-idempotent
**Status:** planning

Extend session registration with a stable WB session ID, machine, lineage,
tmux, and optional native-harness fields while retaining legacy PID records.
Add versioned request, receipt, message, digest, target-config, and append-only
handoff-state types with same-ID/same-digest replay and conflict detection.

### Task 2: Create and push the exact source handover checkpoint

**Verifies:** agent-session-move#ac:unpublished-work-refuses-before-delivery
**Status:** planning

Implement source preflight for a live registered session, managed Work Log,
clean named branch, and non-force remote publication. Preallocate the successor
ID, generate `.wb/handoffs/<id>.md`, stage only that file, commit it, push the
branch, verify the exact remote tip, and persist an offer-only Work Log event.

### Task 3: Receive a bundle into a pinned target worktree

**Verifies:** agent-session-move#ac:moved-branch-refuses-at-target
**Status:** planning

Add the fixed idempotent `wb session receive` boundary and extend WB's secure
worktree creation path to fetch a declared feature branch at an exact expected
commit with source-work ancestry checks. Reuse only a clean matching target and
refuse moved branches, repository mismatches, conflicts, and digest reuse.

### Task 4: Start same-harness or cross-harness successors through SSH

**Verifies:** agent-session-move#ac:ssh-move-starts-same-harness-successor, agent-session-move#ac:explicit-cross-harness-move
**Status:** planning

Add fixed Codex and Claude Code launch specifications plus a private WB launcher
that registers the preallocated session and `exec`s the harness inside a named
detached tmux session. Implement the SSH courier with configured
`hetzner-vm1`, optional trusted `wb_path`, stdin JSON, bounded preflight/start
polling, fixed remote argv, and no shell interpolation.

### Task 5: Finalize receipt-gated custody and Work Log evidence

**Verifies:** agent-session-move#ac:receipt-completes-custody-and-lineage
**Status:** planning

Have the target create its external-parent Work Log claim and return a receipt
only after the stable successor registration and tmux session are live. Add a
new external-handoff acknowledgement that logs both ends and seals the source
claim only after receipt, without using the existing source-local applied
handoff transfer.

### Task 6: Add the Synchestra courier adapter

**Verifies:** agent-session-move#ac:synchestra-uses-the-same-receiver-contract
**Status:** planning

Implement the WB adapter for the typed `wb.session.accept.v1` Synchestra runner
handler using the byte-identical request and receipt types used by SSH. Keep
runner scheduling and fixed-handler execution in the sibling
`synchestra-io/synchestra` Plan `spec/plans/wb-session-transport.md`, and cover
WB behavior with an injectable fake plus compatibility tests.

### Task 7: Deliver messages and request handoff back

**Verifies:** agent-session-move#ac:successor-can-be-messaged-and-asked-back
**Status:** planning

Add `session send`, the fixed target message receiver, durable inbox and
receipts, and `session request-handoff` with its typed reply target. Deliver to
tmux through load/paste-buffer APIs rather than shell or key interpolation, and
record sent/received Work Log events without claiming agent processing.

## Open Questions

1. The live smoke test will determine whether Codex and Claude registration
   hooks expose their native session IDs soon enough for the first receipt; WB
   session ID, tmux name, and liveness remain the mandatory receipt fields.

---
*This document follows the https://specscore.md/plan-specification*
