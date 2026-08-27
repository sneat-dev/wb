---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Work Log Recovery

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/work-log?op=request-change) |

**Status:** Approved
**Source Ideas:** —

## Summary

`wb worktree log` gives every WB-managed effort a private, durable journal that
lives inside the worktree it describes, survives an interrupted agent session,
and reconciles safely with Synchestra. It records the creation manifest, the
ordered sequence of instructions that directed the work, agent/run provenance,
Git evidence, progress, handoffs, usage evidence, and server/mirror sync state —
Git-excluded, never entering the source repository — and it gates commits so no
worktree can accumulate work without a record of who asked for it.

## Problem

An interrupted agent currently leaves only a mutable Git worktree, a branch,
and scattered harness history. A successor cannot tell which prompt and
immutable context created the work, who owns the worktree, whether the branch
was changed after the last test, what remains, or whether Synchestra received a
checkpoint. Treating a Git worktree as the whole recovery record loses private
instruction context and makes concurrent takeover unsafe; committing a detailed
run log to product source leaks private prompts, machine details, and provider
usage.

Two failures follow from that gap and recur in practice. First, abandoned
worktrees accumulate faster than anyone can triage them, because a checkout that
cannot explain its own origin can only be judged by reading its diff — so it is
never judged, and the branch dies with it. Second, a record that only the initial
prompt creates is a record of the first instruction and none of the corrections
that actually shaped the work, which is precisely the context a successor needs.

A journal held outside the worktree cannot fix either failure: when the checkout
is orphaned, the external record is exactly what has gone missing or stale.

## Behavior

### Vocabulary and authority

- An **Effort** is one durable requested outcome. `effort_id` is supplied by an
  upstream task/dispatch when available, otherwise WB creates a stable local ID.
  An `effort_id` is a dot-separated path: `gg-input-types` is a feature effort,
  `gg-input-types.fix-parser` one of its task efforts, and depth is unbounded.
  The path is the parentage; a manifest corroborates it but never contradicts it.
- A **Prompt** is one instruction that directed an Effort, retained in order.
  The originating instruction and every later steering instruction are the same
  kind of record, distinguished only by ordinal.
- A **Run** is one concrete agent execution of an Effort. Its creator explicitly
  records the exact child model or `unknown`, the declaring identity and
  provenance (`runtime_observed`, `caller_declared`, or `unknown`), plus
  independent optional `cli` and routing/billing `provider` identity. Neither
  route field is inferred; provider is never a credential.
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

#### REQ: self-describing-worktree-journal

The live journal MUST live inside the worktree it describes, at
`<worktree>/.wb/local/`, with directory and file permissions no more permissive
than `0700` and `0600` respectively. It MUST contain `manifest.yaml`, a
`prompts/` directory, and a `worklog/` directory holding an atomically replaced
`projection.json`, append-only `events.jsonl`, and append-only `outbox.jsonl`.

A worktree MUST be explicable from its own contents alone: identity, origin,
instructions, and history MUST NOT require the canonical clone, WB home, or any
index to remain intact. This is the requirement that makes abandoned-worktree
triage possible, and it is why the journal is not a pointer.

WB MUST add exactly one rule, `/.wb/local/`, to that repository's local Git
exclude mechanism rather than editing the shared `.gitignore`. The rule MUST NOT
be `/.wb/`: a repository's own `.wb/hooks.yaml` and `.wb/templates/` are tracked
team policy, and excluding their parent silently swallows newly added policy
files.

WB MUST read `<worktree>/.wb/local/` first and fall back to a legacy
`<worktree>/.wb-worklog/` projection when only the legacy path exists. WB MUST
write only the current path. A missing, untracked, or mismatching journal blocks
automatic recovery and produces a deterministic diagnosis.

#### REQ: immutable-creation-manifest

WB MUST write `<worktree>/.wb/local/manifest.yaml` when it creates a worktree,
and MUST NOT rewrite it afterwards. It MUST record the schema version, effort ID
and its parent effort, effort kind (`feature` or `task`), canonical repository
identity, worktree path, branch, immutable base ref and base SHA, creation
timestamp, creator identity and run provenance, and `provenance` of exactly
`created` or `reconstructed`.

`provenance: reconstructed` marks a manifest WB derived from Git evidence for a
worktree that predates this requirement. A reconstructed manifest MUST record
which fields were inferred and from what evidence, so triage can never mistake
inference for a creation record.

Correcting a manifest MUST use the same append-only correction chain as
execution identity: WB appends a typed, claim-addressable correction and never
rewrites prior bytes.

#### REQ: ordered-prompt-sequence

Every instruction that directs an effort MUST be retained as one file in
`<worktree>/.wb/local/prompts/`, named `<NNNN>-<slug>.md` with a zero-padded
four-digit ordinal, strictly monotonic from `0000`. The originating instruction
is ordinal `0000` and carries no special location or filename; a steering
instruction is simply the next ordinal. Zero padding is required so lexical
order equals chronological order past ordinal nine.

Each prompt file MUST carry YAML frontmatter recording `seq`, `at`, `sha256`,
and `source` of exactly `harness_observed`, `agent_declared`, or
`human_declared`, plus the recording runtime, model, CLI, and provider where
known. WB MUST NOT infer `source`: a prompt captured by a harness hook is
`harness_observed`, one an agent reports about itself is `agent_declared`, and
one a person supplies at the terminal is `human_declared`. The body is the exact
instruction bytes.

Prompt bodies are private local data. They MUST NOT be printed in normal output,
entered into public projections, reports, hook metrics, source Git, or a
Synchestra envelope; only the digest and ordinal may enter public state. A
configured archive MAY accept them only as an explicitly authorized encrypted
sealed payload with declared retention. Local exact retention remains mandatory
whether or not export is configured.

#### REQ: sealed-archive-outlives-worktree

`<WB_HOME>/worklogs/` is the archive, not a second live journal. Before removing
a checkout, worktree cleanup, abort, and recycle MUST seal that worktree's
`.wb/local/` and move it to `<WB_HOME>/worklogs/<effort-id>/`, preserving the
manifest, every prompt, and the complete event history. History MUST outlive the
worktree, because a finished effort requires the worktree to be removed.

Cleanup MUST NOT delete an unsealed journal. A failed seal MUST leave both the
worktree and its journal intact and report the precise non-terminal state.

#### REQ: commit-admission-gate

The managed worktree guard MUST refuse a commit in any WB-managed worktree that
lacks a valid manifest, or whose prompt sequence is empty. The gate binds on
location, not on actor: WB MUST NOT attempt to distinguish an agent from a human
by environment markers, because a marker that can be absent fails open exactly
when it matters.

Refusal MUST name the remedy in its message. Supplying the missing instruction
MUST produce an ordinary `human_declared` prompt at the next ordinal, never a
bypass flag, so the act of unblocking a commit is itself the record of who
directed it.

The gate MUST support an explicit warn mode that reports what would be refused
without refusing it, so a fleet with live sessions can adopt enforcement without
stopping them.

#### REQ: hierarchical-effort-paths

An effort path is a dot-separated sequence of segments, `feature.task.subtask`,
of arbitrary depth. It occupies the existing `<task>/<owner>/<repository>`
worktree arity: WB MUST NOT nest a child worktree inside a parent's directory,
because filesystem nesting couples lifetimes and makes removing a parent destroy
live child work.

Parentage MUST be derivable lexically — the parent of `a.b.c` is `a.b` — and
MUST be corroborated by each manifest's recorded parent effort. WB MUST reject
an empty path component, a leading or trailing dot, and a depth or length that
would exceed the platform path limit.

Cleanup MUST terminalize children before parents, and MUST refuse to terminalize
an effort while any live worktree names it as an ancestor.

#### REQ: orphan-enumeration

WB MUST enumerate every linked worktree it can reach across all supported layout
generations — the current `<WB_HOME>/worktrees` hierarchy, the legacy
`<projects-root>/.wb` hierarchy, and worktrees registered to a canonical clone
but living outside both — and report, for each, its effort identity, parent,
branch, base, last commit and its age, dirty state, whether its branch is merged
into the exact remote target, and whether a manifest exists.

Where no manifest exists, WB MUST reconstruct what it can from Git evidence
alone: registered branch, first reflog entry, commit authorship and dates, and
merged status. Reconstructed identity MUST be labelled as such and MUST NOT be
presented as a creation record.

Enumeration MUST group by lexical effort parentage so an abandoned family is
reported as one subject rather than as unrelated rows, and MUST classify each
worktree with an explicit recommended disposition and the evidence for it. It
MUST be read-only.

#### REQ: non-disruptive-adoption

Adoption MUST NOT require quiescing the fleet. Agents run unattended, so a
rollout that depends on stopping sessions cannot be verified to have stopped
them.

Every step MUST therefore be additive: `.wb/local/` is a new path, no existing
file moves, and no working tree is touched, so a worktree with uncommitted work
is unaffected by construction. A WB build that writes the current path MUST
still read the legacy one, so a session running an older binary continues to
work and a session running the current binary adopts its own worktree on first
touch. The commit gate MUST ship in warn mode and become enforcing only as a
separate, reversible step.

Backfilling an existing worktree MUST write `provenance: reconstructed` and MUST
NOT fabricate a prompt: a worktree whose instructions were never recorded has
none, and the gate's remedy is how its first prompt is supplied.

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

A Run creator MUST explicitly supply the exact child model or literal `unknown`
before a new claim is published. WB MUST retain the declaring identity and model
provenance of exactly `runtime_observed`, `caller_declared`, or `unknown`; it
MUST NOT infer model, CLI, or provider from a harness, environment, or one
another. CLI and provider are independent optional bounded identifiers;
provider is routing/billing/subscription metadata only and never a credential.
Legacy absent model projects as unknown and absent route fields remain absent.
An applied handoff/not-landed transition is also a claim creation: its caller
MUST provide the successor model or explicit `unknown`, plus independently
known CLI/provider identifiers. It MUST NOT inherit those fields from the old
claim. A WB-internal rollback recovery with no external creator MUST publish
explicit model/provenance `unknown` and absent route identifiers.
If later evidence proves identity metadata wrong, WB MUST append a typed,
claim-addressable correction event with reason, actor, timestamp, explicit
predecessor, and replacement/clear field presence; it MUST NOT rewrite history.
Recovery and projections reject an incomplete, forked, cyclic, cross-claim, or
malformed chain and apply the latest valid correction while retaining complete
history, including after worktree cleanup. Token usage MUST carry a discriminator of exactly
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

#### REQ: checkpointed-base-refresh-and-integration

The Work Log MUST distinguish **refresh** from **integrate**. Refresh only
fetches the claimed branch's configured target ref and measures divergence; it
does not modify the worktree, index, branch, or local base branch. The journal
and public projection MUST retain target ref and SHA, last successful fetch,
last integration, ahead/behind counts, and nullable conflict state. WB MUST
request refresh at least every 60 minutes and whenever observed target-ref
movement makes the recorded target SHA stale.

Integration is a separate, claim-owner operation permitted only at a clean
checkpoint. WB MUST require it after every checkpoint that records a clean
committed state and before push, handoff, finalize, or merge. Before merging,
WB MUST fetch and fast-forward the target ref first. If the refresh timer is
due while the worktree is dirty, WB MUST append a `refresh_required` event and
leave all Git state untouched: it MUST NOT auto-stash, reset, rebase, merge, or
rewrite. For an unpublished branch with one owner, integration defaults to
rebase; for a published or shared branch, it defaults to merge. Rewriting a
published branch requires explicit user approval and a force-with-lease guard.
Conflict state blocks handoff/finalization until a clean checkpoint records its
resolution or an explicit failed terminal result explains it.

#### REQ: commands-and-projections

WB MUST provide the following deterministic command group:

| Command | Required behavior |
|---|---|
| `wb worktree log init` | Create or attach an Effort, record the manifest and prompt ordinal `0000` with its provenance, verify a WB-managed worktree, and acquire the initial claim. |
| `wb worktree log steer` | Append the next prompt ordinal with its exact bytes, digest, and explicit source; the agent-facing verb for recording steering. |
| `wb worktree set --prompt` | Human-facing alias of `log steer` that records a `human_declared` prompt; the remedy the commit gate names when it refuses. |
| `wb worktree orphans` | Read-only enumeration of every reachable linked worktree across all layout generations, grouped by effort parentage, with reconstructed identity and a recommended disposition per family. |
| `wb worktree log show` | Read journal and live Git evidence without mutation; default text redacts private data and `--json` exposes the public projection only. |
| `wb worktree log checkpoint` | Append a typed progress/checkpoint event, observed Git evidence, optional nullable usage, and update both projections. |
| `wb worktree log refresh` | Fetch and measure target-ref divergence without changing the claimed worktree; record target SHA and freshness evidence. |
| `wb worktree log integrate` | At a clean checkpoint, integrate the fetched target using the policy-selected rebase or merge strategy and record the result/conflict state. |
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
with a specific remedy.

When a merged landing deliberately leaves a live worktree on a different branch
than its immutable active claim, `wb worktree log recover --reconcile-branch`
MUST remain dry-run by default. Its applied mode MUST require the exact live
branch and head, clean managed-worktree and readable-owner evidence, no open
dependent pull request, exact fetched target containment, and unchanged local
and remote claim refs. Before either ref is retired it MUST preserve both heads
in verifiable private recovery bundles, journal every mutation stage, rebind
only the live branch to the existing claim identity, append one idempotent
`branch_reconciled` event, and re-corroborate the claim. It MUST never rewrite
the immutable claim; interrupted applies resume only from their exact recorded
stage and a completed receipt explicitly states that normal cleanup may proceed.
Cleanup seals a finalized journal into
`<WB_HOME>/worklogs/<effort-id>/`, where it remains visible as **recent** for
seven days; thereafter `wb worktree log archive` moves it atomically below
`<WB_HOME>/worklogs/archive/<YYYY>/<MM>/<effort-id>/`. Worktree cleanup MUST
not delete an active, unfinalized, or unsealed journal; it may surface the
derived recent state and require finalization before destructive cleanup.

#### REQ: privacy-and-redaction

All public CLI text, JSON projections, WB reports, hook metrics, and
Synchestra-sync envelopes MUST be allow-list projections. They MUST exclude the
exact prompt, credentials, environment variables, source file contents, raw
model transcript, command output, provider secrets, and absolute local-home
paths. Handoff summaries and progress messages are user-authored public fields
and MUST be validated against size limits; callers that need sensitive context
use the private local journal or an authorized Synchestra message/artifact
contract. An authorized encrypted private prompt archive is a separate sealed
payload, never a field added to the generic operational envelope or Git mirror;
only its digest and receipt may enter public state.

## Dependencies

- [worktree-lifecycle](../worktree-lifecycle/README.md) — validates the
  worktree identity, branch and cleanup boundary that a Worktree Claim protects.

## Acceptance Criteria

### AC: private-recoverable-effort

**Given** an agent initializes a WB-managed worktree for a task or dispatch
with an exact private prompt and immutable Git base
**When** the process crashes after one checkpoint while Synchestra is offline
**Then** the Git-excluded journal inside that worktree identifies the Effort,
Run, claim, agent provenance, prompt digests, Git evidence, progress, and unsent
events without exposing prompt bodies in Git status, normal command output, or
source files.

### AC: orphan-explains-itself-without-anything-else

**Given** a worktree whose canonical clone has been re-cloned, whose WB home
index is unavailable, and whose agent session ended weeks ago
**When** a person or successor agent reads the checkout directly
**Then** `.wb/local/manifest.yaml` and `.wb/local/prompts/` identify the effort,
its parent, its branch and immutable base, who created it, and every instruction
that directed it, using nothing outside that directory.

### AC: steering-is-recorded-in-order-with-honest-provenance

**Given** an effort that begins with one instruction and is redirected several
times, some captured by a harness hook and some reported by the agent itself
**When** the prompt sequence is read back
**Then** ordinals are zero-padded and strictly monotonic so lexical order equals
chronological order past ordinal nine, the originating instruction is simply
ordinal `0000`, and each file records `harness_observed`, `agent_declared`, or
`human_declared` as its actual source, never inferred from the other fields.

### AC: commit-refused-and-unblocked-by-recording-not-bypassing

**Given** a WB-managed worktree with no manifest, or with a manifest and an
empty prompt sequence
**When** any commit is attempted in it while the gate is enforcing
**Then** the commit is refused with a message naming the remedy, no environment
marker can exempt the attempt, and running that remedy records an ordinary
`human_declared` prompt at the next ordinal — after which the same commit
succeeds and the journal shows who directed it.

### AC: sub-agent-families-stay-independently-cleanable

**Given** a feature effort with several sub-agent task efforts, each holding its
own worktree for the same repository
**When** cleanup is attempted on the parent while a child is still live
**Then** every worktree occupies the same `<task>/<owner>/<repository>` arity
with no child nested inside a parent's directory, parentage resolves lexically
and agrees with each manifest, and cleanup refuses the parent, naming the live
children, rather than removing their working trees.

### AC: adoption-without-stopping-sessions

**Given** a fleet with live agent sessions, worktrees holding uncommitted work,
and worktrees created by older WB builds across all layout generations
**When** the current WB build is rolled out and the gate is left in warn mode
**Then** no working tree is modified, legacy journals are still read, new writes
go only to `.wb/local/`, backfilled manifests are marked
`provenance: reconstructed` with no fabricated prompts, and every commit that
would later be refused is reported without being refused.

### AC: exclusive-sequential-handoff

**Given** a primary Run holds a Worktree Claim and a helper has returned a patch
artifact
**When** the primary hands work to a successor
**Then** the helper never obtains write authority, the outgoing Run durably
records its checkpoint and handoff before release, and exactly one successor
Run can validate current Git evidence and acquire the claim.

### AC: unknown-model-is-not-guessed-and-can-be-corrected

**Given** a dispatcher creates a Run with an exact child model or explicit
`unknown`, or later evidence contradicts its immutable identity metadata
**When** WB records or recovers the Work Log
**Then** it rejects omission before publication, never guesses model/CLI/provider,
retains caller/creator provenance and independent optional route identity, and
applies a deterministic append-only correction chain without modifying prior
bytes, including after the worktree is removed.

### AC: safe-base-refresh-and-integration

**Given** a claimed branch has a stale target ref and its canonical checkout's
local base branch may be stale or in use elsewhere
**When** its 60-minute refresh is due while the claimed worktree is dirty
**Then** WB records `refresh_required` without changing Git state; after a
clean checkpoint, it fetches and measures the target, records target SHA,
ahead/behind/conflict evidence, and integrates before the next push, handoff,
finalize, or merge using rebase only for unpublished single-owner work and
merge by default for published/shared work.

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

---
*This document follows the https://specscore.md/feature-specification*
