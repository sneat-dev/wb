---
format: https://specscore.md/plan-specification
status: Executing
---
# Plan: Park and Resume Agent Sessions

**Status:** Executing
**Source Feature:** park-and-resume-agent-sessions
**Date:** 2026-08-26
**Owner:** codex
**Supersedes:** —

## Summary

Implement the complete delayed whole-session journey: private local parking,
coordinator-launched local resume, and receipt-gated multi-worktree remote
resume through the existing configured SSH route. Keep the legacy
single-worktree session-move protocol and command wire compatible.

## Approach

Use one independent versioned parked-session envelope and receipt, and factor
only the hardened execution primitives needed by both protocols. The source
captures canonical Git/Work Log custody through retained descriptors, persists
the exact envelope before delivery, and holds an exclusive resume fence. The
target admits the envelope with no-follow private storage, reconstructs exact
pins, establishes every Work Log claim and owner behind one launcher barrier,
then publishes one all-member receipt. The source changes from parked to
resumed only after validating and storing that receipt.

Local resume uses the same launcher/private-continuation contract and performs
an all-member no-steal preflight before any owner append. Dirty and unpushed
members remain locally valid. Zero-member local resume uses a deterministic
private neutral directory; remote zero-member resume refuses before courier.

The plan remains non-terminal until independent Sol/Terra review and a live
Mac-to-VM Luna journey prove the configured SSH transfer, target launch, bundle
receipt, and source finalization.

## Tasks

### Task 1: Durable parked aggregate and lifecycle projection

**Id:** task-1
**Verifies:** park-and-resume-agent-sessions#ac:park-preserves-whole-bundle, park-and-resume-agent-sessions#ac:remote-refusal-has-zero-delivery-or-mutation, park-and-resume-agent-sessions#ac:retry-and-race-converge, park-and-resume-agent-sessions#ac:legacy-session-move-remains-compatible
**Depends-On:** —
**Status:** in_progress

Deliver and adversarially verify the independent parked bundle protocol,
descriptor-retained source and target stores, exact canonical envelope reuse,
all-member Git/Work Log receiver, private launcher continuation injection,
exclusive local/remote winner fences, source receipt transaction, and strict
secrecy/bounds/target-machine authorization. Preserve the exact legacy
session-move test vectors and tracked handover policy.

### Task 2: Complete command journey and live release evidence

**Id:** task-2
**Verifies:** park-and-resume-agent-sessions#ac:local-resume-launches-one-successor, park-and-resume-agent-sessions#ac:zero-member-local-resume-has-private-root, park-and-resume-agent-sessions#ac:remote-bundle-resume-completes
**Depends-On:** 1
**Status:** planning

Wire `wb session park`, coordinator-launched local `wb session resume`, the
fixed `wb session receive-park` target command, and a separate fixed-argv SSH
courier. Prove one- and two-member success, zero-delivery refusal, zero-member
local launch, dirty local preservation, exact continuation non-disclosure,
per-stage interruption repair, concurrent winner behavior, and source
parked-to-resumed finalization. Then obtain independent Sol/Terra review and run
the live Mac-to-VM Luna journey before marking this task or plan complete.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
