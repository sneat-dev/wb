---
format: https://specscore.md/plan-specification
status: Draft
---

# Plan: Implement shared worktree ownership and coordination

**Status:** Draft
**Source Feature:** shared-worktree-coordination
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —

## Summary

Implement local cooperative shared-worktree membership, explicit ownership and durable peer messaging in the existing coverage-refactor checkout. The human approved specification and implementation on 2026-10-01, including 100% coverage for new code, the `register --join` shortcut and direct ownership commands. Accumulate reviewed local commits; remote publication remains deferred.

## Journey

Owner A registers and explicitly takes uninitialized ownership. B registers with `--join` and C joins directly; `info` shows one owner and all participants. A and B exchange an idempotent message and consumption acknowledgement. A transfers to B; two takeover contenders demonstrate one atomic winner and stale expectation refusal. C leaves. Throughout, original claims, refs, HEAD and checkout data remain intact; no lease enforcement or background services are required.

## Approach

Use a small `internal/worktreecollab` package with pure transitions and narrow persistence/session/checkout adapters, rather than expanding the already large worktrees package. Make initial ownership explicit, preserve legacy behavior outside opt-in state, and reuse existing durability and locking primitives. Authenticate callers through the invocation's registered ancestor; recipient lookup is not caller authentication. Keep ownership notices independent of ordinary inbox capacity. A single implementation lane owns production integration; the coordinator owns commits and broad checkpoints. Defer file/symbol leases, edit hooks, global lifecycle gates, cross-machine transport and daemon delivery.

## Tasks

### Task 1: Implement the testable coordination core

**Verifies:** shared-worktree-coordination#ac:explicit-owner-and-membership, shared-worktree-coordination#ac:ownership-transitions, shared-worktree-coordination#ac:testability-and-complete-coverage
**Status:** planning

Implement explicit owner/membership state and atomic persisted transitions. Write transition and fault tests as each body is introduced; every new/modified body reaches 100% in a fresh focused profile. Bind checkout and authenticated caller identity without altering legacy claims. Initial opt-in must corroborate effective legacy ownership; test third-party `none` refusal despite absent coordination state, unresolved legacy identity, matching-current-owner opt-in and audited explicit takeover. Recheck legacy journal observation under consistent lock ordering. Test simultaneous owner comparison and refuse stale values even under force.

### Task 2: Wire accepted commands and info

**Verifies:** shared-worktree-coordination#ac:explicit-owner-and-membership, shared-worktree-coordination#ac:registration-shortcut, shared-worktree-coordination#ac:ownership-transitions, shared-worktree-coordination#ac:testability-and-complete-coverage
**Status:** planning

Add join/leave/transfer-ownership/take-ownership, registration's `--join`, and owner/participant fields in existing info. Shared service calls avoid duplicate join logic. Cover all command paths, including registration partial success, using invocation-local dependencies and native boundary journeys.

### Task 3: Add bounded idempotent local peer messaging

**Verifies:** shared-worktree-coordination#ac:durable-local-messages, shared-worktree-coordination#ac:testability-and-complete-coverage
**Status:** planning

Add send/inbox/ack with stable caller idempotency keys bound to the complete immutable request, exact retry checks and consumption acknowledgement. Test changed recipient/sender/kind/bytes under the same key refusing, and identical retry after an ownership change returning the original receipt. Keep owner notices outside ordinary message capacity. Verify private storage, crash/restart retry, out-of-order acknowledgement and quiet empty inbox. Do not reuse handoff-only authority as peer addressing.

### Task 4: Independently review and accumulate the verified implementation

**Verifies:** shared-worktree-coordination#ac:explicit-owner-and-membership, shared-worktree-coordination#ac:registration-shortcut, shared-worktree-coordination#ac:ownership-transitions, shared-worktree-coordination#ac:durable-local-messages, shared-worktree-coordination#ac:testability-and-complete-coverage
**Status:** planning

Review exact source/profile hashes, all changed/new body coverage and the complete native owner/join/message/transfer/take journey. Run focused race, vet, pinned lint, named guards, default compilation and Windows cross-compilation. Coordinator commits accepted batches locally; coalesce broad measurements with coverage work. No routine full-suite reruns for spec-only edits and no remote publication.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
