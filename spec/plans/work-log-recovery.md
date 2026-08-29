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

## Implementation checkpoint (2026-08-10)

- Implemented and tested in the current WB branch: per-repository immutable
  claims in a shared Run, opaque Git-excluded projections, private prompt
  preflight/archive, collision-free terminal/outbox records, exact remote-target
  cleanup evidence, seal-before-delete ordering, explicit safe recycle, and
  audited discard or one-successor handoff/not_landed transitions. The shipped
  projection is `<worktree>/.wb-worklog/recovery.json`; a corroborated one-way
  migration reads the short-lived `.wb-worklog.json` pointer. Claim identity is
  portable across machines and Runs because it hashes effort + canonical
  repository + branch + immutable base, never an absolute worktree or Run ID.
- Implemented and tested lifecycle recovery: cleanup and discarded abort write
  a durable private stage before worktree removal; rerunning the same named
  journey resumes exact local-branch retirement after proving the worktree
  path/registration and remote branch are absent and the local ref did not move.
- Partially implemented: live worktree inventory and recycle crash recovery.
  The capability manifest names their exact limitations; seven-day history and
  every process-crash replay point remain open.
- Delivered in WB #62 (pending merge): creator-supplied execution identity on
  new claims (`model` required as exact ID or `unknown`, independent optional
  `cli`/routing-provider), append-only claim-addressable correction events with
  predecessor chains and offline outbox receipts, and legacy unknown/absent
  projection. Applied handoffs require a fresh successor declaration without
  inheriting the predecessor route; internal recycle rollback uses explicit
  unknown. This is local Work Log evidence, not a Synchestra transport.
- Still planned: the full `wb worktree log` command group, periodic refresh and
  integration enforcement, Synchestra authoritative sync/replica observation,
  Git transport fallback, distributed fencing, Portable Merger Agent,
  per-target merger lanes/queue takeover, model-provenance correction,
  plan-overlap/migration-scope detection, and Fair Split Relay E2E.

## Tasks

### Task 1: Define the local Effort, Run, and journal contract

**Id:** task-1
**Verifies:** work-log#ac:private-recoverable-effort, work-log#ac:unknown-model-is-not-guessed-and-can-be-corrected
**Depends-On:** —
**Status:** planning

Add typed Go models and canonical encoders for Effort, Run, Worktree Claim,
agent provenance, private prompt snapshot, nullable usage states, Git evidence,
typed event, outbox envelope, and public recovery projection. Implement
deterministic IDs/hashes, schema validation, permission policy, redacted public
projection, and atomic projection replacement. Keep model identity nullable,
record `runtime_observed`/`caller_declared` provenance, and append audited
corrections that supersede bad metadata without rewriting history; unit-test
unknown-runtime input, correction recovery, and exact prompt exclusion from
every public representation.

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

Define a versioned, backend-agnostic Synchestra client interface and generic
operational envelope that identifies Effort/Run/Claim/Event without carrying
the private prompt. Separately define an optional, explicitly authorized
encrypted private sealed-prompt payload with digest, receipt, and retention;
the generic envelope, source Git, and Git fallback/mirror carry only public
digest/receipt evidence.
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
Handoff and not-landed work remain resumable. Their applied successor claim
must require an explicit exact model or `unknown` and independently known
optional CLI/provider identifiers before the predecessor is sealed; successor
identity must not be copied from the predecessor. Discard must require explicit
apply plus remote retirement authorization, clean/unlocked revalidation at the
removal boundary, force-with-lease deletion of only an exact remote source
ref, and compare-and-delete of the exact local branch after the archive is
durable. Persist a resume stage before worktree removal so loss of live
inventory cannot hide a remaining local ref; exercise an interruption and
exact retry. Exercise the two-unused-storage-worktrees shape and a concurrent
writer race so aborted claims cannot become untracked branch/worktree debt or
lose late local work.

### Task 9: Add portable merger and overlap/migration coordination

**Id:** task-9
**Verifies:** work-log#ac:exclusive-sequential-handoff, work-log#ac:safe-terminal-retention
**Depends-On:** task-3, task-5, task-7
**Status:** planning

Define a transport-neutral Portable Merger Agent contract that can drain a
compatible batch of task branches through task→feature→main integration and
prove exact remote-target landing plus zero abandoned worktrees/branches.
WB must persist one stable fenced merger lane and ordered queue per canonical
`(repository, target ref)`. Primary sessions enqueue immutable ready heads;
takeover invalidates the old fence and resumes the same lane/Work Log without
replaying receipted steps. One founder-MVP agent may service multiple distinct
lanes, while independent lane keys may execute concurrently at scale.
Add typed plan-overlap and migration-scope evidence so two active efforts can
detect shared files/libraries before duplicating work, select one owner, and
record audited reuse/handoff decisions. These are planned capability seams:
there is no current `wb` command, built-in help topic, or executable AI-skill
example for merger dispatch, overlap detection, or migration-scope claiming.
Schema-valid packages are insufficient: every supported harness must load the
released adapter and assert its exact discovered component IDs/cardinality,
including unique skills and the expected merger agent. The Fair Split Relay is
the first required E2E consumer.

### Task 10: Audit and enforce canonical clone layout at WB admission

**Id:** task-10
**Verifies:** work-log#ac:private-recoverable-effort
**Depends-On:** task-2
**Status:** planning

Keep `wb sync` as the only WB creator of canonical
`<projects-root>/<owner>/<repository>` clones. Add a read-only audit that
derives owner/repository from each Git origin, detects top-level or misowned
clones, and prints an exact move/reclone remedy. Make worktree create/guard
refuse those layouts at WB admission, and expose the same check through help,
hooks, skills, and executable fixtures. WB cannot intercept an arbitrary
external `git clone`; that limitation remains explicit. This capability is
planned and has no current command or skill example.

### Task 11: Move the journal into the worktree at `.wb/local/`

**Id:** task-11
**Verifies:** work-log#ac:orphan-explains-itself-without-anything-else, work-log#ac:adoption-without-stopping-sessions
**Depends-On:** task-1, task-2
**Status:** complete

Relocate the live journal from the WB-home pointer projection to
`<worktree>/.wb/local/`, holding `manifest.yaml`, `prompts/`, and `worklog/`.
Write the single exclude rule `/.wb/local/`, never `/.wb/`, so a repository's own
tracked `.wb/hooks.yaml` and `.wb/templates/` keep working and newly added policy
files are not silently swallowed. Keep reading the legacy `.wb-worklog/`
projection and the older `.wb-worklog.json` singleton; write only the current
path. Adoption is additive — no existing file moves and no working tree is
touched — so a dirty worktree is unaffected. Prove the orphan case in a test that
deletes the canonical clone and the WB home before reading the checkout.

### Task 12: Write and reconstruct the immutable creation manifest

**Id:** task-12
**Verifies:** work-log#ac:orphan-explains-itself-without-anything-else, work-log#ac:adoption-without-stopping-sessions
**Depends-On:** task-11
**Status:** complete

Write `manifest.yaml` at creation and never rewrite it: schema version, effort
ID and parent, effort kind, canonical repository, worktree path, branch,
immutable base ref and SHA, creation time, creator identity and run provenance,
and `provenance: created`. Add reconstruction from Git evidence alone — registered
branch, first reflog entry, commit authorship and dates, merged status — writing
`provenance: reconstructed` with the inferred fields and their evidence recorded.
Reconstruction MUST NOT fabricate a prompt. Corrections use the existing
append-only correction chain rather than rewriting bytes.

### Task 13: Record the ordered prompt sequence

**Id:** task-13
**Verifies:** work-log#ac:steering-is-recorded-in-order-with-honest-provenance
**Depends-On:** task-11
**Status:** complete

Store every directing instruction as `prompts/<NNNN>-<slug>.md`, zero-padded and
strictly monotonic from `0000`, with YAML frontmatter carrying `seq`, `at`,
`sha256`, `source`, and the recording runtime/model/CLI/provider where known.
Ordinal `0000` is the originating instruction and gets no special filename or
location. Add `wb worktree log steer` for agents and the `wb worktree set
--prompt` alias for humans, both writing through one code path with `source`
supplied explicitly and never inferred. Include the harness-hook capture path
that writes `harness_observed`, and keep prompt bodies out of all public output,
projections, reports, hook metrics, and Synchestra envelopes.

### Task 14: Gate commits on manifest and prompt presence

**Id:** task-14
**Verifies:** work-log#ac:commit-refused-and-unblocked-by-recording-not-bypassing, work-log#ac:adoption-without-stopping-sessions
**Depends-On:** task-12, task-13
**Status:** complete

Extend the managed worktree guard so a commit in any WB-managed worktree
requires a valid manifest and a non-empty prompt sequence. Bind on location
only — no environment-marker actor detection, which fails open exactly when it
matters. The refusal message names `wb worktree set --prompt="…"`, and running it
records an ordinary `human_declared` prompt rather than setting a bypass flag.
Ship warn mode first, defaulting to warn, with enforcement as a separate
reversible switch so live sessions adopt without stopping.

### Task 15: Support hierarchical effort paths for sub-agent worktrees

**Id:** task-15
**Verifies:** work-log#ac:sub-agent-families-stay-independently-cleanable
**Depends-On:** task-12
**Status:** complete

Treat an effort ID as a dot-separated path of unbounded depth within the existing
`<task>/<owner>/<repository>` arity; reject empty components, leading or trailing
dots, and paths that would exceed the platform limit. Derive parentage lexically
and corroborate it against each manifest's recorded parent. Never nest a child
worktree inside a parent's directory. Make cleanup terminalize children before
parents and refuse a parent while any live worktree names it as an ancestor,
naming those children in the refusal.

### Task 16: Enumerate orphaned worktrees across every layout generation

**Id:** task-16
**Verifies:** work-log#ac:orphan-explains-itself-without-anything-else, work-log#ac:sub-agent-families-stay-independently-cleanable
**Depends-On:** task-12, task-15
**Status:** complete

Add read-only `wb worktree orphans` covering the current `<WB_HOME>/worktrees`
hierarchy, the legacy `<projects-root>/.wb` hierarchy, and worktrees registered
to a canonical clone but living outside both. Report per worktree: effort
identity and parent, branch, base, last commit and age, dirty state, whether the
branch is merged into the exact remote target, and manifest presence. Group by
lexical effort parentage so an abandoned family is one subject, classify each
with a recommended disposition and its evidence, and label reconstructed
identity as such. The fleet baseline this must handle is 654 linked worktrees
across three generations, of which 489 have no manifest and never will.

### Task 17: Migrate the existing fleet without stopping sessions

**Id:** task-17
**Verifies:** work-log#ac:adoption-without-stopping-sessions
**Depends-On:** task-14, task-16
**Status:** complete

Sequence the rollout so no live session must stop. Ship tasks 11–16 with the
gate in warn mode. Backfill in place: for each reachable worktree, write a
reconstructed manifest, leaving prompt sequences empty where no instruction was
ever recorded. Run `orphans` to produce the triage report, drain the families it
recommends closing, then flip the gate to enforcing as a separate reversible
change. Every step is idempotent and re-runnable, touches no working tree, and
leaves the 8 currently dirty worktrees unmodified.

### Task 18: Terminalize claims whose worktrees and refs are already absent

**Id:** task-18
**Verifies:** work-log#ac:orphaned-claim-terminalization-is-negative-evidence-not-inspection, work-log#ac:safe-terminal-retention
**Depends-On:** task-8, task-16
**Status:** complete
**Implemented-by:** 859d940 (codex/issue-216-orphaned-claim)

Extend `wb worktree abort` with an `orphaned` disposition selected by one exact
immutable claim ID. Require an explicit approving actor and audit reason, keep
dry run read-only, and reject remote-deletion authority because there must be
nothing left to delete. Apply locks the exact claim and rereads the claimed
worktree path, Git worktree registration, local branch, remote branch, and
terminal record. Any present or unreadable predicate refuses; complete absence
and an unchanged immutable claim append a private terminal receipt whose final
commit is empty and whose typed evidence says that content was not inspected.
Exercise races that recreate a branch or replace claim bytes between plan and
apply and prove neither writes a terminal record.

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
