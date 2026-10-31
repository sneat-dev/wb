---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Herdr Session Transport implementation plan

**Status:** Draft
**Source Feature:** herdr-session-transport
**Date:** 2026-09-19
**Owner:** ai
**Supersedes:** —

## Summary

Implement issues #956/#957 as one coherent active lifecycle cutover: original
Herdr TUI 0.9.1/protocol 22 for explicit new agents and receipt-bound messages;
current-chat pickup by default with no terminal dependency or silent spawn.
Preserve immutable legacy readers and digests, one logical resume winner,
all-member custody and append-only recovery. Parallel native chats sharing a
physical app-server must coexist without record or custody overwrite.

The earlier daemon watcher, hook/read-inbox, recorded-only receipt and general
agent-directing design remains future work. It must not be reported as delivered
by this lifecycle change or gain submitted notification authority.

## Journey

1. Park one real native session with every owned member and private continuation.
   Retain dirty state, exact HEADs, original claim attribution and history.
2. Resume in the identifiable current chat. Validate all members before preparing
   the continuation. Return its private path/digest without launching an agent.
3. The agent reads the private file and acknowledges the exact digest in a second
   tool call. Retain bundle -> all journals -> PID barrier through attachment and
   atomic logical successor publication. A peer chat's bytes/custody stay intact.
4. Run an ordinary admitted command, then park and resume again. Reject replay
   of the older completed checkpoint after this newer departure.
5. Interrupt before/after member N, registry publication and completion. Retry the
   same actor idempotently. For a conclusively dead actor, append a bounded
   recovery link and fresh context acknowledgment without changing logical winner.
6. Explicit `--launch` or remote resume creates one fresh owned Herdr pane, runs
   only the fixed gated bootstrap, preserves native process/TTY custody and waits
   for exact active-agent readiness. Unknown-native registration stays pending until independent parent binding;
   retry then preserves the preallocated WB identity and enriches metadata.
7. Send a receipt-bound schema-2 message larger than Linux's per-argument limit.
   Verify exact canonical semantic content and one submission through original
   Herdr. Registry replacement before final observation sends zero input;
   cancellation/ambiguous acknowledgment retains intent with no automatic replay.
8. Prove bounded child-tree cleanup and unrelated process survival. Separately
   validate native Windows pipe deadlines, private ACLs and real lock exclusion.
9. Preserve legacy message/receipt canonical bytes; refuse active tmux paths and
   undelivered legacy automatic submission. Keep future daemon guards unchanged.

Each source milestone is verified in one capped Linux worker at a time; no Mac
project builds/tests/formatters. Native Windows evidence belongs to Windows CI.
Implementation, local proof, exact-head CI, release and real current-chat pickup
remain distinct receipts.

## Approach

Keep one source writer for crosscutting custody and terminal changes. Use native
registry scopes derived from validated immutable runtime/native ID; retain the
coarse PID barrier and lock order bundle -> all journals -> PID. Never migrate
or overwrite another live chat's row or rewrite historical aggregate authority.

Use bounded typed Herdr launcher/message interfaces, strict CLI envelope or
empty-mutation acknowledgment contracts, and the original protocol-22 socket
only for actionable canonical prompts that exceed argv limits. Preserve literal
advisory APIs and daemon capability guards. The generic Transport contract is a
policy surface, not a claim that every lifecycle adapter implements it.

Retain the task and acceptance anchors below. Tasks 3/4/5/10/11 implement and
verify the current cutover; daemon watcher/read-inbox parts of the older plan
remain explicitly deferred. Never mark validation or delivery complete solely
because source, fake adapters or one unit package pass.

## Tasks

### Task 1: `internal/herdr` adapter package

**Id:** task-1
**Verifies:** herdr-session-transport#ac:version-drift-is-detected
**Depends-On:** —
**Status:** in_progress

Already in progress in a separate lane (worktree `herdr-adapter`). A Go
wrapper over the `herdr` CLI: `pane current/list/get/send-text`, `agent
list/get/prompt/send-keys/wait`, version detection (`herdr --version`),
identity-from-environment (`HERDR_PANE_ID`, `HERDR_WORKSPACE_ID`,
`HERDR_SOCKET_PATH`), and a fakeable exec seam so no test reaches the real
herdr socket. Verified against the lane's current commit: `Client.PaneSendText`
already rejects text containing a newline. Every other task below that needs
herdr goes through this package, never through `os/exec` directly.

### Task 2: Define the transport interface and capability matrix

**Id:** task-2
**Verifies:** herdr-session-transport#ac:explicit-override-fails-closed, herdr-session-transport#ac:tmux-never-delivers-unguarded, herdr-session-transport#ac:none-transport-records-only
**Depends-On:** —
**Status:** planning

Define one Go interface covering message, move, park, receive, launch,
courier delivery, successor messaging, and the record/advisory/submit
delivery path, plus a typed capability matrix (live status, empty-input
evidence, submit-vs-advisory) each implementation declares. Add explicit
`wb.yaml`/flag override with fail-closed validation, and the `none`
implementation as a first-class no-op that records rather than errors.
Encode the rule that absence of empty-input evidence is itself a guard
failure — never grounds to submit — and that none cannot deliver input at all. Herdr LineageSubmit authorizes only
explicit successor messages, never daemon-originated submission. This task owns the generic policy contract and shared contract test suite;
production Herdr lifecycle clients retain their separate typed boundaries.

### Task 3: Replace active tmux lifecycle with bounded Herdr custody

**Id:** task-3
**Verifies:** herdr-session-transport#ac:tmux-behavior-is-unregressed, herdr-session-transport#ac:tmux-never-delivers-unguarded, herdr-session-transport#ac:single-transport-interface-has-no-branching-call-sites
**Depends-On:** 2
**Status:** in_progress

Remove active tmux execution, fallback, public flag and prerequisite paths.
Preserve canonical legacy decoders/digests and completed historical receipts.
Implement current native-chat isolation across public registration/resolution,
qualified lookup/list/prune/lifecycle and all custody consumers. Prefer an
existing exact live own scope over mixed-version legacy fallback, preserve an
own legacy row when it remains authoritative, and never inherit a foreign WBID.
Retain all-member custody, source-ID finalization and atomic PID transitions.
Prove ordinary post-pickup admission, repeat park/resume and newer-departure
fencing, including deterministic interleaving before member/PID barriers.

### Task 4: Implement original Herdr launch and schema-2 successor submission

**Id:** task-4
**Verifies:** herdr-session-transport#ac:two-implementations-satisfy-the-same-interface, herdr-session-transport#ac:successor-messaging-is-recorded-on-herdr-and-pulled-via-hook, herdr-session-transport#ac:legacy-and-old-peer-receipts-still-validate
**Depends-On:** 1, 2
**Status:** in_progress

Create only a fresh owned workspace/pane. Strictly quote the fixed private
bootstrap shell text, preserve launch gates, exact foreground argv and native
process generation, and retain bounded supervisor exit/cleanup evidence.
Wait for active AgentGet identity as well as native metadata. Accept only
bounded legitimate startup metadata enrichment of the preallocated record.

New message schema 2 is compact ASCII-escaped single-line canonical JSON with
unchanged 64 KiB semantic body limit. Preserve schema-1 canonical readers but
refuse new legacy delivery. Use the original bounded protocol-22 prompt socket
for payloads beyond argv limits, exact response IDs and no automatic retry.
Retain durable intent before a cancellable registry barrier around complete
recipient snapshot recheck + terminal inspection + sole submission. Work Log
and receipt publication stay outside that barrier. Keep advisory APIs literal.

Recorded-only receipts, hook notification/read-inbox verbs and daemon wake
submission remain deferred; they are not implemented by this task.

### Task 5: Capture native identity and continue in the current chat

**Id:** task-5
**Verifies:** herdr-session-transport#ac:identity-captured-automatically-in-herdr, herdr-session-transport#ac:identity-degrades-outside-herdr, herdr-session-transport#ac:transport-selected-automatically, herdr-session-transport#ac:subagent-resolves-to-parent-owner
**Depends-On:** 2, 3, 4
**Status:** in_progress

Capture complete Herdr terminal coordinates when present, otherwise retain no
terminal. Validate the per-chat Codex thread ID independently from shared root context
and corroborate the observed process
and generation. Shared app-server native chats get independent additive scopes;
private pre-native child registration remains unscoped without ambient adoption. Same-native in-process children retain their existing parent owner; a distinct Codex thread retains independent native-chat custody.

Default/current pickup authenticates the real registered caller, validates every
member and returns private continuation for the agent to read. Exact context
acknowledgment precedes all-member attachment and logical registration. Explicit
launch remains opt-in. Append-only bounded dead-actor recovery preserves one
logical successor across multiple interrupted actors and requires a fresh ack.
No live/unknown actor is replaced, and no old checkpoint reactivates a departed
successor. A standard retained legacy empty setup attempt remains refused unless
the registered operator acknowledges an exact state digest after verifying saved
positive pre-wrapper-start failure evidence. Record this historical assertion
atomically in the initial preparation, preserve every old artifact and logical
winner, and validate it through ordinary retry/recovery without reattribution.
Never treat empty directories as proof of process absence. Automatic SessionStart re-registration and unread-message/body-read
extensions from the earlier task remain deferred.

### Task 6: Read PR bindings and evaluate outcomes (the watcher)

**Deferred daemon extension; not part of issues #956/#957.**

**Id:** task-6
**Verifies:** herdr-session-transport#ac:daemon-watches-only-registered-prs
**Depends-On:** —
**Status:** planning

Add the first reader of `worktrees.RecordClaimPullRequestBinding` (written
today by `internal/orchestrate/pr_create.go` with no reader). The watcher
considers only PRs with a recorded binding — never a fleet-wide scan — and
reuses WB's existing renamed-required-check-aware check-verdict logic (the
same evaluation `wb ci wait`/`wb pr land` use) rather than reimplementing
check-state interpretation. Independent of the transport work; can proceed in
parallel with Tasks 1–5.

### Task 7: Delivery resolution, the unconditional guard, and record-only outcomes

**Deferred daemon extension; not part of issues #956/#957.**

**Id:** task-7
**Verifies:** herdr-session-transport#ac:own-pr-outcome-is-recorded-and-visible, herdr-session-transport#ac:guards-are-unconditional-and-block-submission, herdr-session-transport#ac:move-successor-inherits-the-wake-binding, herdr-session-transport#ac:no-live-owner-is-record-only, herdr-session-transport#ac:at-most-once-per-outcome, herdr-session-transport#ac:pane-resolved-uniquely-or-record-only, herdr-session-transport#ac:herdr-failure-at-delivery-is-record-only
**Depends-On:** 4, 5, 6
**Status:** planning

Walk task → current Work Log claim → session → transport identity at
delivery time (not at registration), re-walked on every attempt so a `/move`
successor's inherited claim resolves to its own transport identity. Persist a
delivery intent keyed by (task, PR, head SHA, outcome) before any record or
send, so the same outcome observed twice never produces a second one — the
coalescing rule for the idea's "Loops" risk; an intent without a receipt is
never auto-retried. Resolve record-only whenever: no live session holds the
claim; the empty-input evidence guard has no mechanism to satisfy it (always,
in this iteration — see Deferred in the Feature); or a herdr failure is
discovered at delivery time (never re-route to another terminal). Make the outcome visible per Task 8.

### Task 8: Facts-only templates and the advisory/record-only split

**Deferred daemon extension; not part of issues #956/#957.**

**Id:** task-8
**Verifies:** herdr-session-transport#ac:non-pr-events-stay-advisory, herdr-session-transport#ac:daemon-line-cannot-approve, herdr-session-transport#ac:template-never-carries-provider-text, herdr-session-transport#ac:check-name-appears-only-when-required
**Depends-On:** 7
**Status:** planning

Add the fixed, closed template set (identifiers only — repository, PR number,
check name only when it is in the target's required-check ruleset otherwise a
count, SHA — never a provider-authored title, body, comment, or log line).
Every event outside the session's own-PR-outcome class is delivered
advisory-only on Herdr or record-only on none, per Task 3's and Task 4's
capability matrices. Ensure no `--approved-by` or equivalent evidence path
accepts a daemon message or its transcript line, and that command help states
a `[wb daemon]` line is a notification, never an approval.

### Task 9: Wake visibility in session and wait listings

**Deferred daemon extension; not part of issues #956/#957.**

**Id:** task-9
**Verifies:** herdr-session-transport#ac:wake-subscription-is-visible
**Depends-On:** 7
**Status:** planning

Surface outstanding and delivered (record-only, in this iteration) wake
subscriptions in `wb session list` and `wb wait list`, naming the task,
target PR, and last outcome.

### Task 10: Fail closed on unavailable or unsupported Herdr lifecycle

**Id:** task-10
**Verifies:** herdr-session-transport#ac:missing-herdr-binary-fails-explicitly, herdr-session-transport#ac:unreachable-socket-fails-explicitly, herdr-session-transport#ac:version-drift-is-detected
**Depends-On:** 1, 4, 5
**Status:** in_progress

Pin original TUI 0.9.1 and protocol 22. Missing binaries/server, unsupported
versions, wrong pane/native/process and malformed acknowledgments must have
private actionable diagnostics. No active fallback or implicit server startup.
Terminal-free public registration/current pickup remains available; only explicit
new-agent or recorded endpoint delivery needs the Herdr prerequisites.

### Task 11: Prove current-chat and real Herdr journeys end to end

**Id:** task-11
**Verifies:** herdr-session-transport#ac:own-pr-outcome-is-recorded-and-visible, herdr-session-transport#ac:tmux-behavior-is-unregressed, herdr-session-transport#ac:none-transport-records-only, herdr-session-transport#ac:wake-subscription-is-visible, herdr-session-transport#ac:daemon-watches-only-registered-prs, herdr-session-transport#ac:advisory-message-strips-control-characters-and-never-submits, herdr-session-transport#ac:successor-messaging-is-recorded-on-herdr-and-pulled-via-hook, herdr-session-transport#ac:no-live-owner-is-record-only, herdr-session-transport#ac:hook-output-never-contains-message-body, herdr-session-transport#ac:recorded-only-receipt-is-honest-about-non-delivery
**Depends-On:** 3, 5, 6, 8, 9, 10
**Status:** in_progress

Collect revision-bound unit and real E2E coverage, not just fake-client receipts.
Run three real Git journeys: twice-interrupted five-member actor recovery;
shared-PID A/B pickup -> ordinary command -> repark -> resume with foreign byte
preservation; and malformed/foreign latest-owner refusal without mutation.
Run the original immutable Herdr 0.9.1 server in an owned isolated Linux fixture,
prove pending-before-observation then same-ID startup retry, active-agent readiness, exact
canonical prompt >128 KiB with one Enter, TTY foreground restoration, exit
capture, inherited-pipe and cancellation cleanup without unrelated process kill.
Run native Windows private named-pipe cancellation/large frame, registry ACL and
interprocess-lock tests. Keep CI/coverage gates unchanged and inspect actual
profiles including reexecuted fixture instrumentation. Finish public contracts,
README/specs/skills, exact-head CI and governed landing/release/live pickup.
Future daemon whole-journey proof remains deferred with Tasks 6-9.

## Deferred AC Coverage

These acceptance criteria belong to the explicitly deferred hook/read-inbox and
recorded-only receipt extensions, rather than the implemented lifecycle cutover:

- herdr-session-transport#ac:session-start-hook-picks-up-a-new-session-id — automatic SessionStart re-registration remains deferred.
- herdr-session-transport#ac:hook-withholds-a-non-conforming-id — requires the deferred SessionStart message notification hook.
- herdr-session-transport#ac:read-verb-labels-a-message-as-anothers-not-the-founders — requires the deferred read-inbox command surface.
- herdr-session-transport#ac:unpatched-sender-fails-closed-on-a-recorded-receipt — requires the deferred recorded-only message receipt state.

## Open Questions

The generic agent-directing command surface and daemon empty-input evidence /
terminal-state race remain future work under the Feature's Deferred section.
Codex per-thread identity and distinct root context, plus the original Herdr version/protocol, are explicit
contracts; no active tmux fallback remains an open implementation choice.
Release and live acceptance remain pending until their actual receipts exist.

---
*This document follows the https://specscore.md/plan-specification*
