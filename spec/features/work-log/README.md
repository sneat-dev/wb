---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Work Log Recovery

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=request-change) |

**Status:** Approved
**Source Ideas:** —

## Summary

`wb worktree log` gives every WB-managed effort a private, durable local journal
that survives an interrupted agent session and reconciles safely with
Synchestra. It records the exact original prompt, agent/run provenance, Git
evidence, progress, handoffs, usage evidence, and server/mirror sync state
without placing any of those records in the source repository.

## Problem

An interrupted agent currently leaves only a mutable Git worktree, a branch,
and scattered harness history. A successor cannot tell which prompt and
immutable context created the work, who owns the worktree, whether the branch
was changed after the last test, what remains, or whether Synchestra received a
checkpoint. Treating a Git worktree as the whole recovery record loses private
instruction context and makes concurrent takeover unsafe; committing a detailed
run log to product source leaks private prompts, machine details, and provider
usage.

## Behavior

### Vocabulary and authority

- An **Effort** is one durable requested outcome. `effort_id` is supplied by an
  upstream task/dispatch when available, otherwise WB creates a stable local ID.
- A **Run** is one concrete agent execution of an Effort. It records the
  instantiator and the agent's vendor, surface, role, parent session, and model.
- A **Worktree Claim** is the time-bounded, exclusive local authority for one
  Run to change one WB-managed worktree and branch. It records canonical
  repository identity, worktree path, branch, immutable base, and observed
  head.

Git remains authoritative for files, index, commits, branches, and remotes. WB
remains authoritative for managed-worktree layout and local claim enforcement.
Synchestra exposes exactly one authoritative active endpoint for operational
records and may report one or more replica cursors and health states; replica
purpose (`mirror` or `backup`) is independent of storage type. SQLite, Git,
DALgo, and inGitDB are Synchestra persistence details, not WB dependencies. A
Synchestra Task or SpecScore artifact remains authoritative for product status
and requirements. The Work Log is a local recovery journal and never completes
a task, renews a remote lease, or claims a branch merely because its own file
says so.

#### REQ: durable-private-journal

WB MUST store the durable journal at
`<WB_HOME>/worklogs/<effort-id>/`, with directory and file permissions no more
permissive than `0700` and `0600` respectively. It MUST contain an atomically
replaced `projection.json`, append-only `events.jsonl`, and append-only
`outbox.jsonl`. The journal MUST retain the exact original prompt snapshot,
source reference, captured-at time, and SHA-256 digest; the prompt is private
local data and MUST NOT be printed, copied to a source worktree, included in
normal reports, or sent to Synchestra unless a future explicit encrypted prompt
transfer contract authorizes it.

#### REQ: git-excluded-worktree-projection

WB MUST create a small, non-sensitive recovery projection at
`<worktree>/.wb-worklog/recovery.json` and add `/\.wb-worklog/` to that
repository's local Git exclude mechanism rather than editing the shared
`.gitignore`. The projection MUST contain only the schema version, Effort/Run/
Worktree Claim IDs, journal location reference, repository/branch/base/head
fingerprints, latest event sequence, and sync/mirror watermarks. It MUST NOT
contain prompt text, credentials, transcript, provider output, cost, or a
machine home path. A missing, untracked, or mismatching projection blocks
automatic recovery and produces a deterministic diagnosis.

#### REQ: deterministic-identities-and-evidence

All identifiers that WB derives MUST be deterministic: a Worktree Claim ID is
the SHA-256 digest of canonical Effort ID, canonical repository ID, branch, and
immutable base; an event ID is the SHA-256 digest of Run ID, monotonic sequence,
event type, and canonical typed payload. Every checkpoint records observed HEAD
and a deterministic Git-status fingerprint. Supplied IDs retain their upstream
values after validation. Retried server submissions reuse the same event ID and
must therefore be idempotent.

#### REQ: append-only-crash-consistent-events

`events.jsonl` MUST contain schema-versioned typed events in strictly monotonic
sequence order. `projection.json` is a derived current-state cache, written via
same-directory temporary file, flush, atomic rename, and directory flush. WB
MUST fsync lifecycle, checkpoint, handoff, and terminal events before reporting
success; low-value usage updates MAY batch. Recovery MAY discard only a torn
final JSONL line. It MUST reject a duplicate sequence, invalid interior record,
or projection/event disagreement rather than guessing a safe state.

#### REQ: agent-provenance-and-usage

A Run MUST retain instantiator identity and kind, agent vendor, surface,
model, role (`primary`, `reviewer`, or `helper`), session ID, and nullable
parent-session ID. Token usage MUST carry a discriminator of exactly
`provider_reported`, `estimated`, or `unavailable`; input tokens, output
tokens, total, estimated cost, currency, and provider usage reference are
nullable unless the selected discriminator makes them available. Local metrics
are diagnostic estimates, never billing truth.

#### REQ: exclusive-claim-and-sequential-handoff

At most one active Worktree Claim MAY permit writes for one canonical
repository/branch/worktree tuple. WB MUST take a crash-detectable local claim
lock before `init`, `checkpoint`, `handoff`, `recover`, or `finalize` mutates a
journal. Helpers are read-only by default and may return a patch artifact or
finding to the claim owner; they MUST NOT write the claimed worktree, stage,
commit, push, or finalize it. A handoff is sequential: the outgoing Run writes a
durable checkpoint and handoff offer, releases or expires its claim, then the
incoming Run validates current Git evidence and acquires a new claim. A same
worktree/branch multi-writer mode is out of scope.

#### REQ: commands-and-projections

WB MUST provide the following deterministic command group:

| Command | Required behavior |
|---|---|
| `wb worktree log init` | Create or attach an Effort, record exact private prompt and provenance, verify a WB-managed worktree, and acquire the initial claim. |
| `wb worktree log show` | Read journal and live Git evidence without mutation; default text redacts private data and `--json` exposes the public projection only. |
| `wb worktree log checkpoint` | Append a typed progress/checkpoint event, observed Git evidence, optional nullable usage, and update both projections. |
| `wb worktree log handoff` | Create a bounded handoff summary and next action, then make the claim available only after the outgoing checkpoint is durable. |
| `wb worktree log recover` | Rebuild derived state from journal plus Git, diagnose stale/lost claims, and require explicit takeover after dry-run evidence. |
| `wb worktree log finalize` | Record terminal result or failure, release the claim, preserve recovery evidence, and enqueue final sync. |
| `wb worktree log sync` | Drain idempotent local outbox events to the configured authoritative Synchestra endpoint and display its receipt plus server-reported replica cursor/health/lag. |
| `wb worktree log archive` | After the seven-day Recent window, atomically move a finalized journal to the archive while preserving its events and public recovery evidence. |

#### REQ: synchestra-authoritative-sync-and-replica-observation

The local outbox MUST target a versioned, backend-agnostic Synchestra server
contract and submit only to its authoritative active endpoint. WB MUST NOT
write any Synchestra replica directly or assume Git is the operational hot
path. A successful sync records the authoritative acknowledgement watermark
separately from each server-supplied replica cursor, purpose, health, and lag.
The initial Synchestra deployment may use Git and SQLite in either active or
replica roles, with DALgo and inGitDB below the server boundary; WB observes
only the declared role/cursor/health contract and never implements, polls, or
repairs a replica. When no authoritative endpoint is reachable, WB continues to
append locally, reports offline state, and retries only through explicit sync or
a configured future worker.

#### REQ: recovery-and-retention

`recover` MUST compare the Worktree Claim's canonical repository, branch,
immutable base, recorded head, local lock state, and live Git state before a
new Run can write. A mismatched branch/head, non-clean unknown state, active
claim, unresolved Synchestra ownership loss, or malformed journal is a blocker
with a specific remedy. Finalized journals remain visible as **recent** for
seven days; thereafter `wb worktree log archive` moves them atomically below
`<WB_HOME>/worklogs/archive/<YYYY>/<MM>/<effort-id>/`. Worktree cleanup MUST
not delete an active or unarchived journal; it may surface the derived recent
state and require finalization before destructive worktree cleanup.

#### REQ: privacy-and-redaction

All public CLI text, JSON projections, WB reports, hook metrics, and
Synchestra-sync envelopes MUST be allow-list projections. They MUST exclude the
exact prompt, credentials, environment variables, source file contents, raw
model transcript, command output, provider secrets, and absolute local-home
paths. Handoff summaries and progress messages are user-authored public fields
and MUST be validated against size limits; callers that need sensitive context
use the private local journal or an authorized Synchestra message/artifact
contract.

## Dependencies

- [worktree-lifecycle](../worktree-lifecycle/README.md) — validates the
  worktree identity, branch and cleanup boundary that a Worktree Claim protects.

## Acceptance Criteria

### AC: private-recoverable-effort

**Given** an agent initializes a WB-managed worktree for a task or dispatch
with an exact private prompt and immutable Git base
**When** the process crashes after one checkpoint while Synchestra is offline
**Then** the durable local journal and Git-excluded recovery projection identify
the Effort, Run, claim, agent provenance, prompt digest, Git evidence, progress,
and unsent events without exposing the prompt in Git status, normal command
output, or source files.

### AC: exclusive-sequential-handoff

**Given** a primary Run holds a Worktree Claim and a helper has returned a patch
artifact
**When** the primary hands work to a successor
**Then** the helper never obtains write authority, the outgoing Run durably
records its checkpoint and handoff before release, and exactly one successor
Run can validate current Git evidence and acquire the claim.

### AC: authoritative-receipt-with-replica-observation

**Given** a local journal has queued idempotent events while the Synchestra
server was unreachable
**When** `wb worktree log sync` reaches the configured authoritative endpoint
**Then** it records the authoritative receipt watermark and separately renders
each server-reported replica's purpose, cursor, health, and lag without WB
directly writing Git state or another replica; retrying the same outbox changes
creates no duplicate operational record.

### AC: safe-terminal-retention

**Given** a Run finalizes successfully or unsuccessfully
**When** its worktree later qualifies for cleanup
**Then** WB preserves the journal as recent for seven days, blocks destructive
cleanup while the journal is active or unfinished, and archives the finalized
journal only through an explicit, atomic archive operation with its recovery
evidence intact.

## Open Questions

1. Which Synchestra authentication and transport contract should carry the
   backend-agnostic work-log envelope: an extension of Dispatch worker
   mutations, a Session endpoint, or a dedicated work-log resource?
2. Is encrypted export of the exact private prompt ever required for a
   cross-machine recovery, and if so which user-controlled key and retention
   policy authorize it?

---
*This document follows the https://specscore.md/feature-specification*
