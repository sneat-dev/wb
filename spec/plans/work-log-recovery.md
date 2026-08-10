---
format: https://specscore.md/plan-specification
status: Draft
---

# Plan: Work Log Recovery implementation plan

**Status:** Draft
**Source Feature:** work-log
**Date:** 2026-08-10
**Owner:** codex
**Supersedes:** —

## Summary

Implement a WB-owned, offline-first Work Log for each managed Effort. The
implementation adds a private durable journal under `~/.wb/worklogs`, a small
Git-excluded recovery projection in the claimed worktree, deterministic CLI
operations, exclusive local claim control, and a backend-agnostic Synchestra
outbox. It does not implement Synchestra persistence, DALgo/inGitDB, SQLite,
Git, or replica control; WB submits stable envelopes to the authoritative
endpoint and displays its receipt plus server-reported replica state.

## Journey

1. **Start — an Effort is instantiated.** A human or dispatch initiator runs
   `wb worktree log init` against a WB-managed worktree. Observable good result:
   the private exact prompt, immutable source/Git snapshot, agent provenance,
   Run, and exclusive Worktree Claim exist; the source repository shows neither
   a tracked log nor the prompt.
2. **Middle — work progresses without unsafe sharing or stale-base drift.** The
primary Run writes checkpoints while helpers inspect read-only snapshots and
return patches or findings. Observable good result: `show` exposes redacted
current progress, live Git evidence, target SHA/divergence/freshness, and
authoritative-sync/replica lag; only the primary can change the claimed
worktree, a dirty refresh timer records a requirement without changing Git,
and an offline endpoint merely grows the local outbox.
3. **End A — sequential handoff.** The primary is interrupted or deliberately
   hands work off. Observable good result: a successor sees a deterministic
   recovery diagnosis, validates the exact branch/head/base, and acquires the
   Worktree Claim only after the first Run's durable handoff checkpoint.
4. **End B — terminal work.** The Run finalizes. Observable good result: final
   evidence enters the outbox, authoritative receipt and replica
   cursor/health/lag are visible independently, the journal remains in Recent
   for seven days, and an explicit archive preserves recovery evidence before
   cleanup can remove the worktree.
5. **End C — Fair Split Relay proves cross-harness coordination.** A Codex CLI
   task worktree and a Claude Code CLI task worktree receive an ordered cents
   allocation proposal through Synchestra, exchange typed accept/counter/claim
   messages, and each Work Log records decision references. Observable good
   result: both adapters agree on the deterministic €10 allocation Alice €3.34,
   Bob €3.33, Carol €3.33; task branches integrate into a feature branch, then
   `main`, and WB reports zero abandoned worktrees/branches (or an explicit
   audited recycle). Future Copilot and desktop adapters use the same harness
   protocol; the initial fixture does not automate a desktop UI.

## Approach

Build the purely local contract first, because it must recover safely with no
Synchestra server at all. Layer the per-worktree projection and exclusive claim
on WB's existing resolver/guard rather than creating a competing worktree
registry. Add CLI state transitions and real-Git failure fixtures next. Only
then define the Synchestra envelope adapter and outbox acknowledgement model;
this keeps the authoritative-store and replica implementation inside
Synchestra while WB remains an offline-capable client. Finish by deriving
seven-day recent/archive state from the journal and documenting the
no-shared-writer workflow.

## Tasks

### Task 1: Define the local Effort, Run, and journal contract

**Id:** task-1
**Verifies:** work-log#ac:private-recoverable-effort
**Depends-On:** —
**Status:** planning

Add typed Go models and canonical encoders for Effort, Run, Worktree Claim,
agent provenance, private prompt snapshot, nullable usage states, Git evidence,
typed event, outbox envelope, and public recovery projection. Implement
deterministic IDs/hashes, schema validation, permission policy, redacted public
projection, and atomic projection replacement; unit-test exact prompt exclusion
from every public representation.

### Task 2: Bind journal creation to WB-managed worktrees and exclusive claims

**Id:** task-2
**Verifies:** work-log#ac:private-recoverable-effort, work-log#ac:exclusive-sequential-handoff
**Depends-On:** task-1
**Status:** planning

Extend the existing worktree resolver/guard boundary to initialise the durable
`<WB_HOME>/worklogs/<effort-id>` journal and local `/\.wb-worklog/` exclusion
only after canonical/worktree/branch/base validation. Add a descriptor-safe,
crash-detectable Worktree Claim lock keyed by canonical repository/branch/
worktree; prove competing writers fail while read-only helper patch-return
records do not gain write or Git authority.

### Task 3: Provide lifecycle, checkpoint, handoff, and recovery commands

**Id:** task-3
**Verifies:** work-log#ac:private-recoverable-effort, work-log#ac:exclusive-sequential-handoff
**Depends-On:** task-1, task-2
**Status:** planning

Wire `wb worktree log init`, `show`, `checkpoint`, `handoff`, `recover`, and
`finalize` into the CLI with deterministic text and public JSON output. Exercise
real Git fixtures for process crash after a durable checkpoint, a torn final
event, projection rebuild, changed branch/head/base, stale claim, explicitly
approved takeover, and sequential handoff; recovery starts read-only and cannot
silently revive uncertain writes.

### Task 4: Add safe base refresh and checkpointed integration

**Id:** task-4
**Verifies:** work-log#ac:safe-base-refresh-and-integration
**Depends-On:** task-1, task-2, task-3
**Status:** planning

Add typed target-ref freshness evidence and `refresh_required` events to the
journal and public projection. Implement read-safe `refresh` with the
60-minute and target-movement triggers, and clean-checkpoint-only `integrate`.
Use real Git fixtures to prove refresh never changes a dirty worktree, local
base branch, index, or current canonical branch; integration selects rebase for
unpublished single-owner branches and merge for published/shared branches;
rewriting a published branch requires explicit force-with-lease. Prove fetch
and fast-forward of the target before merge, ahead/behind/conflict persistence,
and blocking push/handoff/finalize/merge until required integration resolves.

### Task 5: Add authoritative Synchestra outbox and replica observation

**Id:** task-5
**Verifies:** work-log#ac:authoritative-receipt-with-replica-observation
**Depends-On:** task-1, task-3
**Status:** planning

Define a versioned, backend-agnostic Synchestra client interface and envelope
that identifies Effort/Run/Claim/Event without carrying the private prompt.
Implement `sync` as idempotent outbox drain to exactly one authoritative
endpoint, with acknowledgement watermark, retry classification, and
server-supplied replica purpose/cursor/health/lag. Test offline accumulation,
duplicate submission, acknowledgement before a replica advances, replica
lag/error display, and permanent sync failure without direct writes to Git or
another replica and without SQLite/DALgo/inGitDB assumptions in WB.

### Task 6: Retain, archive, surface, and document the recovery journey

**Id:** task-6
**Verifies:** work-log#ac:safe-terminal-retention
**Depends-On:** task-2, task-3, task-4, task-5
**Status:** planning

Add seven-day Recent and explicit `wb worktree log archive` lifecycle support,
then teach worktree list/cleanup to block active or unfinished journals without
deleting their evidence. Add an end-to-end real-Git journey that starts offline,
recovers through a handoff, synchronizes after the authoritative endpoint
returns, finalizes, proves the source worktree remains private, and archives
only after the recent window.
Update WB README and worktree skill with the exclusive-primary/read-only-helper
and patch-return workflow.

### Task 7: Build the Fair Split Relay cross-agent lifecycle harness

**Id:** task-7
**Verifies:** work-log#ac:private-recoverable-effort, work-log#ac:exclusive-sequential-handoff, work-log#ac:safe-terminal-retention
**Depends-On:** task-1, task-2, task-3, task-5, task-6
**Status:** planning

Create a tiny Go fixture repository and adapter interface that starts two
isolated task worktrees under one feature effort. The first adapters invoke
Codex CLI and Claude Code CLI when installed; deterministic fixture adapters
may stand in only for unavailable binaries and must report that substitution.
Assert bidirectional typed Synchestra negotiation, ordered integer-cent
rounding of €10 to Alice 3.34/Bob 3.33/Carol 3.33, Work Log decision-reference
events, task-to-feature-to-main integration, and a final WB lifecycle audit
with no unmanaged branch/worktree. Define extension points for Copilot and
desktop adapters but do not drive desktop UI automation in this slice.

### Task 8: Make aborted effort disposition explicit and auditable

**Id:** task-8
**Verifies:** work-log#ac:safe-terminal-retention
**Depends-On:** task-1, task-2, task-3
**Status:** planning

Add `wb worktree abort` as the only normal escape hatch for work that cannot
meet merged-PR cleanup evidence. It must require an explicit `handoff`,
`not_landed`, or `discarded` disposition; seal/archive the Work Log and emit an
offline-safe outbox event before changing the worktree; and release the claim.
Handoff and not-landed work remain resumable. Discard must require explicit
apply, clean/unlocked revalidation, and compare-and-delete of the exact local
branch after the archive is durable. Exercise the two-unused-storage-worktrees
shape so aborted claims cannot become untracked branch/worktree debt.

## Open Questions

1. The Synchestra service contract is being authored concurrently. Before Task
   4 starts, which resource owns the envelope and authoritative/replica
   watermarks?
2. Should seven-day retention be measured from finalization or from successful
   authoritative receipt when a workstation stays offline after finalization?
3. Which CI environment variables or explicit adapter flags are the stable way
   to locate Codex CLI and Claude Code CLI without treating a locally installed
   binary as proof that the other harness executed?

---
*This document follows the https://specscore.md/plan-specification*
