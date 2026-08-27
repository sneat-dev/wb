---
format: https://specscore.md/feature-specification
status: Approved
---
# Feature: Park and Resume Agent Sessions

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=request-change) |
**Status:** Approved
**Source Ideas:** —

## Summary

Park and resume lets a coordinator suspend one active WB session while
preserving every worktree it owns and bounded private continuation context.
Later, any coordinator process can resume the whole parked aggregate locally or
on a configured remote machine. Resume itself starts exactly one fresh
successor and attaches that same session to every member; it never requires an
already-running successor.

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

### Session lifecycle and bundle custody

#### REQ: exact-private-parked-aggregate

`wb session park` MUST persist an immutable source declaration, stable parked
checkpoint ID, every worktree currently owned by that session, exact canonical
Git and source Work Log custody evidence, and bounded continuation context. It
MUST project the source as parked rather than live or claimable, and MUST NOT
stage, commit, push, change HEAD, or remove user work. Continuation artifacts
MUST be mode `0600` outside every worktree and MUST NOT appear in normal
text/JSON output, command arguments, diagnostics, journals, or Work Log prompts.

#### REQ: coordinator-starts-one-local-successor

Local `wb session resume <parked-session-id>` MUST preflight every member's
exact parked branch, HEAD, source claim, and latest owner before any custody
mutation. It MUST then launch exactly one fresh successor itself, inject the
private continuation through `WB_SESSION_CONTINUATION_FILE`, and attach the same
successor to every member. The first admitted member is the launch working
directory. A zero-member parked session remains locally resumable using a
deterministic private neutral directory under the retained parked aggregate;
no caller path or arbitrary current directory is accepted.

#### REQ: no-later-session-custody-theft

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

### Compatibility and transport security

#### REQ: independent-versioned-bundle-protocol

The parked-session envelope, request, target aggregate, and receipt MUST have
their own strict versioned schemas. Existing `sessionmove.Request` schema
version 1 and public `wb session move` behavior MUST remain wire compatible.
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

### AC: local-resume-launches-one-successor (verifies REQ:coordinator-starts-one-local-successor, REQ:no-later-session-custody-theft)

**Given** a locally parked session whose members still match their parked custody, including a dirty or unpushed member
**When** any coordinator process resumes the checkpoint twice
**Then** WB launches one fresh successor with private continuation, attaches it to every member, preserves each exact HEAD and dirty status, and returns the same durable outcome on retry.

### AC: zero-member-local-resume-has-private-root (verifies REQ:coordinator-starts-one-local-successor)

**Given** a parked source session owned no worktrees
**When** a coordinator resumes it locally
**Then** exactly one successor launches in the deterministic private neutral directory under the retained aggregate, without using the caller's current directory.

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

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
