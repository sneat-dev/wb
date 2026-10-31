---
format: https://specscore.md/feature-specification
status: Approved
---
# Feature: Park and Resume Agent Sessions

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=request-change) |
**Status:** Approved
**Source Ideas:** —
**Depends On:** [Herdr Session Transport](../herdr-session-transport/README.md) (sole active terminal lifecycle; current-chat pickup requires no terminal)

## Summary

Park checkpoints every worktree owned by one native WB session and bounded
private continuation. Local pickup continues in the identifiable registered
current chat by default; it starts no terminal or agent. Explicit `--launch`
starts one Herdr successor, and `--to <machine>` retains remote resume.
Every mode shares one logical winner, all-member custody and immutable history.

## Problem

Session parking is neither worktree abort nor an implicit commit or push.
Without an auditable whole-session aggregate, dirty work, secondary worktrees,
private continuation, and Work Log lineage can be lost or transferred under
the wrong authority. A multi-worktree resume also needs one bundle-level
decision: no member may appear owned by the successor until every member is
ready and corroborated, and the source must remain parked until one durable
receipt proves the complete target outcome.

## Behavior

<!-- How the feature works. Group rules under ### topic headings; write each
     enforceable rule as a `#### REQ: <slug>` entry under its topic. -->

> **Transport contract.** New terminal successors use original Herdr TUI 0.9.1
> (protocol 22), with no active tmux fallback. Historical bytes/digests remain
> readable; current-chat pickup does not use any terminal transport.

### Session lifecycle and bundle custody

#### REQ: exact-private-parked-aggregate

`wb session park` MUST persist an immutable source declaration, stable parked
checkpoint ID, every worktree currently owned by that session, exact canonical
Git and source Work Log custody evidence, and bounded continuation context. It
MUST project the source as parked rather than live or claimable, and MUST NOT
stage, commit, push, change HEAD, or remove user work. Continuation artifacts
MUST be mode `0600` outside every worktree and MUST NOT appear in normal
text/JSON output, command arguments, diagnostics, journals, or Work Log prompts.

#### REQ: park-registers-an-unregistered-session

`wb session park` MUST resolve the calling session, and when none is registered
it MUST register one from what it can observe or infer — the agent PID from
`--pid`, an existing environment declaration, or the nearest recognised harness
in the process tree; the runtime from `--runtime`, the environment, or the
observed parent process name; the model from `--model` or the environment; the
machine from this hostname — record `registered_at_park` on both the session
record and the park output, and then continue. A runtime or model that can
neither be declared nor observed MUST be recorded as `unknown`; missing
identity metadata MUST NOT refuse a park. An explicit `--wb-session-id` MUST
still target an already-registered session and MUST NOT create a registration.
Registration MUST NOT overwrite a session record already projected as parked or
resumed, and the existing fail-closed member checks MUST remain unchanged.

#### REQ: coordinator-starts-one-local-successor

Before any route or member mutation, local resume MUST authenticate the real
registered caller and validate every member's repository, Work Log, branch,
HEAD, dirty state and latest bound owner. Default or `--current` mode MUST
prepare a single logical successor in that same native chat, returning the
private continuation path and digest. The agent MUST read it and acknowledge
with `--ack-context`; users MUST NOT manually copy continuation or digests.
Without an identifiable caller WB MUST refuse with guidance, never spawn.
Preparation freezes the complete caller registration, including metadata. A
concurrent re-registration changing that snapshot is an identity conflict, not
permission to silently rebind an already prepared live actor.

After acknowledgment, lock order MUST remain bundle -> all member journals ->
PID transition barrier. Revalidate departure and complete caller snapshot under
that barrier before attachments and atomic publication. Partial Nth-member,
registry and receipt interruptions MUST retry only the same prepared authority.
Parallel native chats sharing one physical PID MUST retain independent records,
lifecycle markers and custody; foreign nonempty WB owner IDs never permit PID
fallback. Own native legacy registration remains readable without migration.

`--launch` explicitly selects a new Herdr agent. It uses a fixed gated bootstrap
and exact owned process/native readiness. A zero-member explicit launch uses
the deterministic private neutral directory, never an arbitrary caller path.

#### REQ: discoverable-resume-identity-and-launch-diagnostics

Listings/help MUST distinguish `parked_session_id` from `wb_session_id` and
explain current-chat acknowledgment plus explicit launch. Current-chat mode MUST
require no Herdr or harness executable. Explicit launch MUST preflight original
Herdr TUI 0.9.1, selected running socket, fixed WB/harness executables before
claiming route or custody. Exact exit status and bounded startup diagnostics
remain receipt-gated attempt evidence, never permission to create a second agent.

#### REQ: no-later-session-custody-theft

Effective current ownership comes from the latest correctly bound owner event;
immutable original claim attribution is not rewritten. A newer unbound or
contradictory event refuses. A completed older checkpoint MUST NOT reactivate a
successor that has parked or handed off again; departure is rechecked under all
member journals and the PID transition barrier before mutation.

`--recover-current` MUST prove the previous physical process generation gone,
then append a digest-linked actor preparation and fresh acknowledgment while
retaining the same logical winner. It MUST preserve earlier actor completions,
repair only authorized partial attachments, and reject live/unknown generations,
cycles, discontinuities, foreign owners or more than 16 recovery links. Shared
app-server liveness cannot prove that one of its individual chats has died.

A resumer MUST refuse before any member mutation when a worktree's branch,
HEAD, source Work Log claim, or latest owner differs from the exact parked
evidence. It MUST NOT checkpoint later HEADs, push later work, or replace a
newer sequential session's owner record.

### Remote bundle resume

#### REQ: exact-remote-preflight-before-courier

Remote resume via `--to <machine> --via ssh` MUST require at least one owned
worktree and MUST refuse before courier or target mutation when any member is
dirty, lacks an exact remote branch tip, differs from its recorded pushed
commit, lacks source Work Log custody evidence, or is now owned by a newer
session. The refusal MUST identify the actionable member without disclosing a
credential-bearing remote or private continuation.

#### REQ: corroborated-all-member-target-barrier

The configured target MUST authenticate its own `remote.machine`, admit one
bounded versioned parked-session envelope, reconstruct every member at its
exact commit on a deterministic pin, and prepare a corroborated active target
Work Log claim and owner for every member before releasing one successor. The
same successor MUST be attached to every member. Its private successor context
MUST include all target paths and be supplied only through
`WB_SESSION_CONTINUATION_FILE`.

#### REQ: durable-bundle-receipt

The target MUST publish one durable receipt only after the all-member
readiness, claim, owner, and successor barriers succeed. Every receipt member
MUST bind the repository, target path, pin, exact commit, and target Work Log
reference. The source MUST remain parked until it validates and durably stores
that exact receipt, and only then append its parked-to-resumed transition.

#### REQ: exclusive-idempotent-resume-authority

The parked aggregate plus an exclusive resume fence is the authority; the
predecessor process need not remain alive. Retries after envelope admission,
individual member reconstruction or claim publication, launcher readiness,
receipt publication, or source finalization MUST converge on the same
successor and receipt. Concurrent local or remote resumers MUST have one winner,
and losing callers MUST NOT mutate their session registry, member custody, or
source aggregate with a competing target.

#### REQ: resume-resolves-members-by-identity

A parked member MUST be identified by its logical identity: repository
(owner/repository, corroborated by the recorded remote), branch, and Work Log
reference. Its recorded absolute paths are hints, never the identity. At resume
time WB MUST resolve the member's canonical clone from the repository through
the same host-level-first placement resolution every other command uses, and
MUST resolve its checkout from the recorded worktree path or, when that path no
longer exists, from the relocation receipts recorded for the member's Work Log
reference. A member whose resolved checkout is a linked worktree of the
resolved canonical clone, on the recorded branch, under the recorded Work Log
reference, MUST be accepted even when the canonical clone, the worktree, or the
store root has moved since park. A member that cannot be resolved this way, or
whose resolved checkout's repository, branch or Work Log reference differs,
MUST still be refused with the existing identity-changed diagnostic, naming
the recorded and the resolved paths.

### Compatibility and transport security

#### REQ: independent-versioned-bundle-protocol

The parked-session envelope, request, target aggregate, and receipt MUST have
their own strict versioned schemas. Existing `sessionmove.Request` schema
version 1 and public `wb session move` behavior MUST remain wire compatible.
New Herdr receipts include typed terminal coordinates; an older strict peer
that cannot decode those fields must refuse and be upgraded. Existing legacy
canonical fixtures remain unchanged; WB must not silently translate receipt
authority or claim a mixed-version transfer succeeded.
The SSH courier MUST use a fixed non-shell argv, configured host and optional
safe user, bounded stdin/stdout/stderr, canonical envelope bytes on stdin, and
strict receipt decoding. Credential-bearing Git remotes MUST be rejected
without echoing them.

## Acceptance Criteria

The primary journey starts with one registered agent owning two clean,
already-pushed worktrees. Parking returns one stable ID, leaves both Git
checkouts byte-for-byte in place, stores private continuation, and makes the
source visibly parked. Later, from any coordinator process, remote resume sends
the exact durable envelope to one configured machine. Both exact commits and
target Work Log claims become ready before one successor starts; that successor
receives private continuation plus both target paths and owns both members. One
receipt reports both repositories, paths, pins, commits, and target Work Log
references. Only after the source validates that receipt does it become
resumed. Repeating any interrupted step returns the same successor and receipt.

The local epilogue uses the retained paths, supports dirty and unpushed state,
and starts the same one-successor flow without Git mutation. Its zero-member
branch uses the private neutral launch directory. The refusal epilogue names a
dirty, unpushed, changed, or newly-owned member and performs no courier,
registry, or custody mutation.

### AC: park-preserves-whole-bundle (verifies REQ:exact-private-parked-aggregate)

**Given** one registered session owns two worktrees and one has uncommitted changes
**When** the coordinator runs `wb session park --context-file <private-file>`
**Then** a stable parked ID is returned, both exact Git and Work Log custody records are stored, the dirty checkout remains dirty, the source is reported parked, and continuation is absent from output, argv, and Work Logs.

### AC: unregistered-session-can-park (verifies REQ:park-registers-an-unregistered-session)

**Given** no session is registered for the calling process
**When** the coordinator runs `wb session park --context-file <private-file>`
**Then** a session record exists carrying the inferred identity, the park completes, and the output names the new `wb_session_id` and that it was registered at park.

### AC: local-resume-launches-one-successor (verifies REQ:coordinator-starts-one-local-successor, REQ:no-later-session-custody-theft)

**Given** a locally parked session whose members still match their parked custody, including a dirty or unpushed member
**When** any coordinator process resumes the checkpoint twice
**Then** WB returns private continuation for the current caller without launch, and after the agent reads and acknowledges it, attaches the same logical successor to every member, preserving each exact HEAD and dirty status and returning the same durable outcome on retry.

### AC: zero-member-local-resume-has-private-root (verifies REQ:coordinator-starts-one-local-successor)

**Given** a parked source session owned no worktrees
**When** a coordinator resumes it locally
**Then** current-chat pickup still needs no launch; explicit `--launch` starts exactly one successor in the deterministic private neutral directory under the retained aggregate, without using the caller's current directory.

### AC: resume-is-discoverable-and-startup-failures-are-actionable (verifies REQ:discoverable-resume-identity-and-launch-diagnostics)

**Given** a registered session is parked and only public CLI output is available
**When** a coordinator lists sessions and starts local resume
**Then** the parked row exposes the exact resume argument, an explicit launch with missing Herdr or harness refuses before route or custody mutation, and an immediately exiting harness reports and retains its exact status plus bounded startup diagnostic for a receipt-gated retry.

### AC: remote-bundle-resume-completes

**Requirements:** REQ:corroborated-all-member-target-barrier, REQ:durable-bundle-receipt

**Given** a parked session has two clean members at exact pushed commits
**When** a coordinator resumes it to a configured machine through SSH
**Then** the target authenticates its machine, reconstructs both exact pins, prepares both active Work Log claims and owners before release, starts one successor attached to both, and returns one validated receipt containing every required member field before the source becomes resumed.

### AC: remote-refusal-has-zero-delivery-or-mutation (verifies REQ:exact-remote-preflight-before-courier, REQ:no-later-session-custody-theft)

**Given** a parked bundle has zero members or contains a dirty, unpushed, changed, or newly-owned member
**When** remote resume is requested
**Then** WB refuses before SSH delivery, registry change, source event, or member custody mutation and names the actionable reason without disclosing credentials or continuation.

### AC: retry-and-race-converge (verifies REQ:exclusive-idempotent-resume-authority)

**Given** resume is interrupted at any member, claim, launch, receipt, or source-finalization boundary, or two coordinators race
**When** resume is retried or the callers complete
**Then** exactly one successor and one byte-identical receipt win, all members converge on that successor, and every loser leaves its registry and custody unchanged.

### AC: legacy-session-move-remains-compatible (verifies REQ:independent-versioned-bundle-protocol)

**Given** an existing client uses `sessionmove.Request` schema version 1 or `wb session move`
**When** the park-and-resume protocol is installed
**Then** the prior request/receipt bytes, tracked handover behavior, receiver proofs, and public command behavior remain accepted unchanged.

### AC: resume-survives-a-layout-migration (verifies REQ:resume-resolves-members-by-identity)

**Given** a session parked with two members: a worktree in a legacy home whose
canonical clone is at the legacy `<root>/<org>/<repo>`, and a worktree inside
its canonical clone
**When** `wb layout migrate --apply` moves both clones to the host level, and
then `wb session resume <id>` runs
**Then** the resume accepts both members at their current paths, and the
successor's context names the current paths; and a member whose checkout was
switched to a different branch after park is still refused as identity changed.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
