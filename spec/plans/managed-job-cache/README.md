---
format: https://specscore.md/plan-specification
status: Draft
---

# Plan: Deliver owned job cache boundaries

**Status:** Draft
**Source Feature:** managed-job-cache
**Date:** 2026-11-02
**Owner:** querydock_improvements_spec
**Supersedes:** —

## Summary

Deliver the approved dependency-archive slice without touching active global stores or implementing the broader Draft Disk Reclaim feature. Code and tests are in preparation; runtime qualification and release remain pending.

## Approach

Journey: the operator first sees a no-write plan; explicit apply acquires the existing producer lock and checks headroom; a cold job installs only into mounted ephemeral masks and atomically publishes an owned archive; normal job cleanup closes its consumers; the next equivalent job restores it into fresh masks without reinstalling; policy safely bounds old archives and reports any unreachable reserve. Each step has an observable result, and the final cold/warm run uses the actual successor Graph profile rather than a disconnected fixture.

## Tasks

### Task 1: Bound the proven producer and its first-party command

**Id:** task-1
**Status:** in_progress
**Note:** First-party asset and command are written; tests and actual successor-profile adoption remain unqualified.
**Verifies:** managed-job-cache#ac:plan-preserves-state, managed-job-cache#ac:bounded-policy, managed-job-cache#ac:pinned-inputs-and-masks

Adopt the existing secure Python archive producer as a WB asset; add exact packageManager/source-manifest binding, no-write planning, bounded policy and a minimal command. Preserve old extraction/privacy/mask limits. The existing broad Disk Reclaim Draft stays unchanged.

### Task 2: Pin concurrency, failure and input regressions

**Id:** task-2
**Status:** in_progress
**Note:** Real-filesystem/lease Python regressions and Go command/wrapper tests authored; no runtime result yet.
**Verifies:** managed-job-cache#ac:lease-and-race-safety, managed-job-cache#ac:bounded-policy, managed-job-cache#ac:pinned-inputs-and-masks

Cover inherited leases, inode replacement, source drift, unknown/incomplete pairs, partial deletion, archive write bounds and size/age/reserve decisions using real temporary files and kernel locks. Run required changed-statement/package ratchets under Go 1.27 on capped Linux.

### Task 3: Adopt in the next real contained validation profile

**Id:** task-3
**Status:** planning
**Verifies:** managed-job-cache#ac:real-cold-warm-journey

Prepare a separate source/hash-bound successor profile with the dedicated owned shared archive volume and exact two dependency masks. Reuse unchanged source/resource/privacy/cleanup guards; do not edit active profiles or extend unsupported executor coverage.

### Task 4: Review and qualify the whole cold/warm journey

**Id:** task-4
**Status:** planning
**Verifies:** managed-job-cache#ac:real-cold-warm-journey, managed-job-cache#ac:plan-preserves-state, managed-job-cache#ac:lease-and-race-safety

Independently review the frozen source and adapter, then retain actual cold/warm, policy, resource and cleanup evidence. Normal WB release/cleanup follows exact-head checks; no task is complete merely because source or an adapter proposal exists.

## Open Questions

None requiring product permission. The final adapter/profile pins and actual qualification receipts are pending implementation evidence.

---
*This document follows the https://specscore.md/plan-specification*
