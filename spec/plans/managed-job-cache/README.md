---
format: https://specscore.md/plan-specification
status: Implemented
---

# Plan: Deliver owned job cache boundaries

**Status:** Implemented
**Reconciled:** 2026-11-03
**Source Feature:** managed-job-cache
**Date:** 2026-11-02
**Owner:** querydock_improvements_spec
**Supersedes:** —

## Summary

Deliver the approved dependency-archive slice without touching active global stores or implementing the broader Draft Disk Reclaim feature. The implemented slice passed independent producer and cold/warm consumer verification and shipped through [PR #965](https://github.com/sneat-dev/wb/pull/965) in [WB v0.205.0](https://github.com/sneat-dev/wb/releases/tag/v0.205.0), with merge, release and cleanup evidence recorded below.

## Approach

Journey: the operator first sees a no-write plan; explicit apply acquires the existing producer lock and checks headroom; a cold job installs only into mounted ephemeral masks and atomically publishes an owned archive; normal job cleanup closes its consumers; the next equivalent job restores it into fresh masks without reinstalling; policy safely bounds old archives and reports any unreachable reserve. Each step has an observable result, and the final cold/warm run uses the actual successor Graph profile rather than a disconnected fixture.

## Tasks

### Task 1: Bound the proven producer and its first-party command

**Id:** task-1
**Status:** complete
**Implemented-by:** sneat-dev/wb@3a850624864b5c524bb7148ef8776f7b40dec1f5
**Note:** Implemented and shipped; pinned source, CLI and real cold/warm adoption verified.
**Evidence:** Code: https://github.com/sneat-dev/wb/pull/965; merge 3a850624864b5c524bb7148ef8776f7b40dec1f5; CI: https://github.com/sneat-dev/wb/actions/runs/41636972526; release WB 0.205.0: https://github.com/sneat-dev/wb/releases/tag/v0.205.0. Selected producer: seven packages, 1308 positive blocks and zero uncovered (audit sha256:12047995d94dd868a1f9264beb8b86578b045e7e8250cd3e356a7d8a5441868e). Real cold/warm adoption: identical 130135-entry tree, install skipped on fresh restore, 82 tests in five files with zero Nx result-cache hits; lock/reserve refusal, no-write planning and bounded eviction verified with protected entries preserved (audit sha256:78ed88df292c75c9f46848b52f5958576ce4ae163dd8c357340da3c8cbb009f0). Normal hook publication audit sha256:4766dd6eb92baf3190dc54d3032d0e1fb81456f0687e28e9b99a1a6cb44b25b6. All owned runtime containers/volumes and the merged code branch/worktree were normally cleaned.
**Verifies:** managed-job-cache#ac:plan-preserves-state, managed-job-cache#ac:bounded-policy, managed-job-cache#ac:pinned-inputs-and-masks

Adopt the existing secure Python archive producer as a WB asset; add exact packageManager/source-manifest binding, no-write planning, bounded policy and a minimal command. Preserve old extraction/privacy/mask limits. The existing broad Disk Reclaim Draft stays unchanged.

### Task 2: Pin concurrency, failure and input regressions

**Id:** task-2
**Status:** complete
**Implemented-by:** sneat-dev/wb@3a850624864b5c524bb7148ef8776f7b40dec1f5
**Note:** 26 Python regressions reused byte-identically; selected producer, normal full vet, final CI and public release verified.
**Evidence:** Code: https://github.com/sneat-dev/wb/pull/965; merge 3a850624864b5c524bb7148ef8776f7b40dec1f5; CI: https://github.com/sneat-dev/wb/actions/runs/41636972526; release WB 0.205.0: https://github.com/sneat-dev/wb/releases/tag/v0.205.0. Selected producer: seven packages, 1308 positive blocks and zero uncovered (audit sha256:12047995d94dd868a1f9264beb8b86578b045e7e8250cd3e356a7d8a5441868e). Real cold/warm adoption: identical 130135-entry tree, install skipped on fresh restore, 82 tests in five files with zero Nx result-cache hits; lock/reserve refusal, no-write planning and bounded eviction verified with protected entries preserved (audit sha256:78ed88df292c75c9f46848b52f5958576ce4ae163dd8c357340da3c8cbb009f0). Normal hook publication audit sha256:4766dd6eb92baf3190dc54d3032d0e1fb81456f0687e28e9b99a1a6cb44b25b6. All owned runtime containers/volumes and the merged code branch/worktree were normally cleaned.
**Verifies:** managed-job-cache#ac:lease-and-race-safety, managed-job-cache#ac:bounded-policy, managed-job-cache#ac:pinned-inputs-and-masks

Cover inherited leases, inode replacement, source drift, unknown/incomplete pairs, partial deletion, archive write bounds and size/age/reserve decisions using real temporary files and kernel locks. Run required changed-statement/package ratchets under Go 1.27 on capped Linux.

### Task 3: Adopt in the next real contained validation profile

**Id:** task-3
**Status:** complete
**Verifies:** managed-job-cache#ac:real-cold-warm-journey

Prepare a separate source/hash-bound successor profile with the dedicated owned shared archive volume and exact two dependency masks. Reuse unchanged source/resource/privacy/cleanup guards; do not edit active profiles or extend unsupported executor coverage.

### Task 4: Review and qualify the whole cold/warm journey

**Id:** task-4
**Status:** complete
**Verifies:** managed-job-cache#ac:real-cold-warm-journey, managed-job-cache#ac:plan-preserves-state, managed-job-cache#ac:lease-and-race-safety

Independently review the frozen source and adapter, then retain actual cold/warm, policy, resource and cleanup evidence. Normal WB release/cleanup follows exact-head checks; no task is complete merely because source or an adapter proposal exists.

## Open Questions

None requiring product permission. The final adapter/profile pins and actual qualification receipts are recorded in the Resolution below.

---

## Resolution

**Reconciled Draft → Implemented outside the tracked `change-status` flow** (2 task(s) marked complete; this did not walk the legal-transition matrix).

Explicitly user-approved archive slice delivered ahead of recorded lifecycle. Tasks 3/4 now reflect real cold/warm policy and 82-test adoption, independent review, code merge/release and normal cleanup; no failed or aborted task overridden.

Evidence: Code: https://github.com/sneat-dev/wb/pull/965; merge 3a850624864b5c524bb7148ef8776f7b40dec1f5; CI: https://github.com/sneat-dev/wb/actions/runs/41636972526; release WB 0.205.0: https://github.com/sneat-dev/wb/releases/tag/v0.205.0. Selected producer: seven packages, 1308 positive blocks and zero uncovered (audit sha256:12047995d94dd868a1f9264beb8b86578b045e7e8250cd3e356a7d8a5441868e). Real cold/warm adoption: identical 130135-entry tree, install skipped on fresh restore, 82 tests in five files with zero Nx result-cache hits; lock/reserve refusal, no-write planning and bounded eviction verified with protected entries preserved (audit sha256:78ed88df292c75c9f46848b52f5958576ce4ae163dd8c357340da3c8cbb009f0). Normal hook publication audit sha256:4766dd6eb92baf3190dc54d3032d0e1fb81456f0687e28e9b99a1a6cb44b25b6. All owned runtime containers/volumes and the merged code branch/worktree were normally cleaned.
*This document follows the https://specscore.md/plan-specification*
