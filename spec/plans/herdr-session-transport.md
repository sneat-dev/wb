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

Give WB one pluggable terminal-transport interface, with herdr as the default
implementation (founder-confirmed, 2026-09-19) and tmux moved behind it
unregressed. Build the MVP wake flow — "your PR has an outcome → your session
wakes" — as **record-only** in this iteration: the empty-input evidence a
submitted delivery would need is deferred (founder, 2026-09-19: "Out of scope
for performance work we focus now on"), so every own-PR-outcome event is
recorded and made visible in existing listings, never pasted into a pane.
Advisory delivery for every other event class, which never needed that
evidence, still ships now. No daemon-originated event is submitted into any
pane; the one pre-existing exception is `wb session send`'s tmux successor
-messaging path, which already submits today and is unaffected — its herdr
counterpart is deferred alongside the same empty-input evidence gap.

## Journey

**An agent registers, inside herdr, and keeps working.** `wb session register`
captures `HERDR_PANE_ID`, `HERDR_WORKSPACE_ID`, `HERDR_SOCKET_PATH`, a
resolved terminal ID, and `CLAUDE_CODE_SESSION_ID` with no flags. Observable
good result: `wb session list` shows the session's pane and `transport: herdr`
immediately, before anything else happens.

**The agent opens a pull request and moves on to other work.** `wb pr create`
records the task-to-PR binding. Observable good result: `wb wait list` already
shows this task's outstanding subscription — visibility is a property of
registration, not of delivery, so watching what's pending never requires the
daemon to have acted yet.

**Checks settle while the agent is idle.** The daemon's watcher reads only
bindings it recorded itself (never a fleet-wide PR scan) and evaluates the
outcome through WB's existing check-verdict logic. Observable good result:
WB records one facts-only, identifiers-only entry — `sneat-dev/wb#NNN: checks
passed` — and `wb session list`/`wb wait list` mark it recorded. **Nothing is
submitted into the pane**: the empty-input evidence a submit would need does
not exist yet, so this is record-only even though the agent is idle.

**Checks settle while the agent is mid-turn — a null-action step proving the
guard is unconditional, not selectively triggered.** Observable good result:
the identical record-only outcome as the idle case above, because the guard's
evidence requirement can never be satisfied today regardless of idle/working
state. Once the deferred mechanism lands, this is where idle and mid-turn
diverge; today they don't.

**The daemon observes the same settled outcome again on a later poll, before
anyone acted on the first record.** Observable good result: no second record
is produced for that (task, PR, head SHA, outcome) tuple — one entry, however
many times it's observed, which is also the fix for the idea's "Loops" risk.

**A different, non-own-PR event reaches the same herdr-hosted agent.**
Observable good result: WB pastes it into the pane unsubmitted, via
`pane send-text` with the message stripped of control characters and no
trailing newline — advisory delivery never needed the deferred evidence in
the first place, only a submit does, so this keeps working today.

**A second agent is doing the same thing in tmux, not herdr.** Observable good
result: every daemon-originated event, including its own PR's outcome, is
recorded only — never pasted — because tmux exposes no live status and no
input-emptiness signal to guard even an advisory paste against a mid-typing
human. This is the lead's default; the founder separately confirmed tmux
stays supported, 2026-09-19 ("yes").

**A predecessor sends the herdr-hosted agent a follow-up message through
`wb session send`, unrelated to the daemon.** Observable good result: the
courier's `wb session receive-message` durably records it and returns a
receipt exactly as it does on tmux, but nothing is pasted or submitted; the
next time the agent's `SessionStart` hook fires, its announcement names the
recorded, unread message. tmux is unaffected — its pre-existing submit
behavior (a `paste-buffer` payload that already ends in a newline) keeps
working exactly as it does today.

**A third session has neither herdr nor tmux — a bare shell.** Observable good
result: the event is recorded only, `wb wait list` says so explicitly, and
when no live session holds the task's claim at all, the next session that
picks up the task sees the record waiting for it.

**The owning session moved to another machine via `/move` before the outcome
arrived.** Observable good result: resolution follows the task's claim to the
successor session's own registered transport identity, not the predecessor's;
the record lands where the work actually continued.

**The agent runs `/clear`, which gives Claude Code a new session ID in the
same pane.** Observable good result: the installed `SessionStart` hook
re-registers the session's harness session ID where it can, or falls back to
reminding the agent to re-run `wb session register` where it can't; either
way the next delivery addresses the current session, never a stale one.

**herdr itself is unreachable when a herdr-registered session's event
fires.** Observable good result: WB reports a distinct "herdr socket
unreachable" failure and records the event only — it does **not** re-route
that session to tmux, since nothing ever established it had a tmux pane.
Fallback between transports applies only at registration, before any
transport has been chosen.

## Approach

Build the transport interface and its capability matrix first, as a contract
both implementations satisfy, in parallel with the `internal/herdr` adapter
(Task 1, already in progress in a separate lane) and the watcher (Task 6,
independent of transport). Move today's tmux code behind the interface with
characterization tests proving no behavior change, then wire the herdr
implementation onto Task 1's adapter. Layer identity capture (including the
`SessionStart` re-registration hook) on top, then delivery resolution — which
resolves record-only for the own-PR-outcome class in this iteration, because
the empty-input evidence guard can never be satisfied yet — then the
facts-only templates and advisory split, listings, and the explicit
registration-time failure surface. Finish with one whole-journey end-to-end
test walking every stage above against fake transports, never the real herdr
socket or tmux server.

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
failure — never grounds to submit — and that tmux declares no live status and
no input evidence at all, so it is record-only for daemon-originated events
by default. No production call site is rewired yet; this task ships the
contract, a shared contract test suite both implementations must pass, and
its own tests.

### Task 3: Move tmux behind the transport interface, no behavior change

**Id:** task-3
**Verifies:** herdr-session-transport#ac:tmux-behavior-is-unregressed, herdr-session-transport#ac:tmux-never-delivers-unguarded, herdr-session-transport#ac:single-transport-interface-has-no-branching-call-sites
**Depends-On:** 2
**Status:** planning

Write characterization tests first for the current tmux behavior wherever
coverage is thin, then move it behind Task 2's interface with no behavior
change, converting every call site so none branches on herdr-vs-tmux
directly: `internal/session/session.go`, `internal/session/exact.go`,
`internal/sessionlaunch/tmux.go`, `internal/sessionlaunch/launch.go`,
`internal/sessionlaunch/private.go`, `internal/sessionlaunch/state.go`,
`internal/sessionmessage/tmux.go`, `internal/sessionmessage/receive.go`,
`internal/sessionmessenger/send.go`, `internal/sessionmove/route.go`,
`internal/sessionmove/message.go`, `internal/sessionmove/store.go`,
`internal/sessionmove/types.go`, `internal/sessionpark/protocol.go`,
`internal/sessionparkreceive/receive.go`, `internal/sessionreceive/receive.go`,
`internal/sessioncourier/receiver.go`, `internal/sessionauthority/authority.go`,
`internal/worktrees/session_custody.go`, `internal/worktrees/session_message.go`,
`internal/worktrees/session_park_custody.go`, `cmd/wb/session_message.go`,
`cmd/wb/session_move.go`, `cmd/wb/session_park.go`,
`cmd/wb/session_receive.go`, `cmd/wb/session_register.go`. Declare tmux's
capability matrix (no live status, no input evidence) and prove its
record-only default for daemon-originated events; the pre-existing
successor-messaging path (`wb session send`) keeps submitting on tmux exactly
as it does today, unaffected by this default.

### Task 4: Wire the herdr transport onto the `internal/herdr` adapter

**Id:** task-4
**Verifies:** herdr-session-transport#ac:two-implementations-satisfy-the-same-interface, herdr-session-transport#ac:pane-resolved-uniquely-or-record-only, herdr-session-transport#ac:advisory-message-strips-control-characters-and-never-submits, herdr-session-transport#ac:successor-messaging-is-recorded-on-herdr-and-pulled-via-hook
**Depends-On:** 1, 2
**Status:** planning

Implement the herdr transport against Task 1's adapter and Task 2's contract
test suite: pane resolution for message/move/park/receive/launch,
`pane send-text` for advisory delivery (control characters stripped, never a
trailing newline), and `agent list`/`agent get` for capability checks. Pane
resolution MUST call `agent list` on the session's recorded
`HERDR_SOCKET_PATH` and match uniquely on `agent_session`; zero or multiple
matches resolve to record-only, never a guess. No `agent prompt` submit path
is built in this iteration — not for the daemon wake, and not for
`wb session send`'s herdr side either, since both need the same deferred
empty-input evidence. On herdr, `wb session receive-message`
(`cmd/wb/session_message.go`) durably records a successor message and
returns a receipt without pasting it; the recorded, unread message is
surfaced through Task 5's extended `SessionStart` hook.

### Task 5: Automatic identity capture, selection, and session-start re-registration

**Id:** task-5
**Verifies:** herdr-session-transport#ac:identity-captured-automatically-in-herdr, herdr-session-transport#ac:identity-degrades-outside-herdr, herdr-session-transport#ac:subagent-resolves-to-parent-owner, herdr-session-transport#ac:transport-selected-automatically, herdr-session-transport#ac:session-start-hook-picks-up-a-new-session-id, herdr-session-transport#ac:successor-messaging-is-recorded-on-herdr-and-pulled-via-hook
**Depends-On:** 2, 3, 4
**Status:** planning

Files: `internal/session/session.go`, `internal/session/exact.go`,
`cmd/wb/session_register.go`, `cmd/wb/skills_hook_run.go`,
`cmd/wb/skills_hook_install.go`. Depends on Task 4 because the hook's
announcement names a recorded, unread successor message, which only exists
correctly once Task 4's herdr `receive-message` recording behavior is built.
Builds on Task 3's already-refactored
`internal/session` package rather than racing it. Extend `wb session
register` and the session record with `HERDR_PANE_ID`, `HERDR_WORKSPACE_ID`,
`HERDR_SOCKET_PATH`, a resolved terminal ID, and the harness session ID
(`CLAUDE_CODE_SESSION_ID`, or the Codex equivalent once confirmed — see Open
Questions), recorded together with the transport Task 2's selection logic
resolves. Degrade to tmux identity, then to `none` with only harness ID and
machine recorded. Verify a subagent's `CLAUDE_PID`/`CLAUDE_CODE_SESSION_ID`/
`HERDR_PANE_ID` resolve to its parent's existing record rather than creating
a second one. Extend WB's existing Claude Code `SessionStart` hook
(`cmd/wb/skills_hook_run.go`, installed by `skills_hook_install.go`) to
re-register the transport identity on a new `CLAUDE_CODE_SESSION_ID` in the
same pane (e.g. after `/clear`) where it can resolve identity, falling back
to its existing reminder-only behavior where it cannot. Also extend the
hook's announcement to name any recorded, unread successor message for this
session, so a herdr-hosted successor sees a follow-up message on its next
start even though nothing was pasted or submitted to deliver it.

### Task 6: Read PR bindings and evaluate outcomes (the watcher)

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
discovered at delivery time (never re-route to tmux; that fallback applies
only at registration, Task 9). Make the outcome visible per Task 8.

### Task 8: Facts-only templates and the advisory/record-only split

**Id:** task-8
**Verifies:** herdr-session-transport#ac:non-pr-events-stay-advisory, herdr-session-transport#ac:daemon-line-cannot-approve, herdr-session-transport#ac:template-never-carries-provider-text, herdr-session-transport#ac:check-name-appears-only-when-required
**Depends-On:** 7
**Status:** planning

Add the fixed, closed template set (identifiers only — repository, PR number,
check name only when it is in the target's required-check ruleset otherwise a
count, SHA — never a provider-authored title, body, comment, or log line).
Every event outside the session's own-PR-outcome class is delivered
advisory-only on herdr or record-only on tmux/none, per Task 3's and Task 4's
capability matrices. Ensure no `--approved-by` or equivalent evidence path
accepts a daemon message or its transcript line, and that command help states
a `[wb daemon]` line is a notification, never an approval.

### Task 9: Wake visibility in session and wait listings

**Id:** task-9
**Verifies:** herdr-session-transport#ac:wake-subscription-is-visible
**Depends-On:** 7
**Status:** planning

Surface outstanding and delivered (record-only, in this iteration) wake
subscriptions in `wb session list` and `wb wait list`, naming the task,
target PR, and last outcome.

### Task 10: Explicit herdr failure modes at registration

**Id:** task-10
**Verifies:** herdr-session-transport#ac:missing-herdr-binary-fails-explicitly, herdr-session-transport#ac:unreachable-socket-fails-explicitly, herdr-session-transport#ac:version-drift-is-detected
**Depends-On:** 1, 4, 5
**Status:** planning

Give each herdr failure mode a distinct, actionable message at registration
time — binary not on `PATH`, socket unreachable, version drift (missing
command or unrecognized JSON shape) — and fall back to whatever automatic
selection would produce with herdr excluded (tmux, if applicable, else
`none`), except when herdr was named by an explicit override, which refuses
per Task 2's fail-closed rule. This fallback is registration-only; delivery-time
herdr failure is Task 7's record-only rule, not a re-route.

### Task 11: Prove the whole journey end-to-end

**Id:** task-11
**Verifies:** herdr-session-transport#ac:own-pr-outcome-is-recorded-and-visible, herdr-session-transport#ac:tmux-behavior-is-unregressed, herdr-session-transport#ac:none-transport-records-only, herdr-session-transport#ac:wake-subscription-is-visible, herdr-session-transport#ac:daemon-watches-only-registered-prs, herdr-session-transport#ac:advisory-message-strips-control-characters-and-never-submits, herdr-session-transport#ac:successor-messaging-is-recorded-on-herdr-and-pulled-via-hook, herdr-session-transport#ac:no-live-owner-is-record-only
**Depends-On:** 3, 5, 6, 8, 9, 10
**Status:** planning

Walk the Journey above against fake herdr and fake tmux adapters in one
end-to-end test: register in herdr with full identity, register a task's PR,
observe the watcher record the outcome record-only whether idle or mid-turn,
confirm the same outcome observed twice produces one record, show a
non-own-PR event still pasted advisory-only on herdr, show every
daemon-originated event record-only by default on tmux and on `none`, follow
a `/move` successor's inherited binding, and confirm both listings surface
it. Assert on the exact commands issued to the fake transports (mechanism),
not only on the reported outcome.

## Open Questions

- Task 5 assumes a Codex equivalent of `CLAUDE_CODE_SESSION_ID` exists. This
  was not observed in this session (no Codex process was running to inspect).
  If none exists, Task 5 records the harness session ID as `unknown` for
  Codex, per REQ:identity-capture-outside-herdr's degrade rule, and this note
  is removed once confirmed either way.
- The Feature's "Directing agents" section is deliberately out of this Plan's
  scope: it needs a founder decision (see herdr-session-transport's Open
  Questions) before it can become a task.
- The Feature's Deferred section (empty-input evidence mechanism; the
  `state_change_seq` re-check; the herdr side of successor messaging) has no
  task here by design — all three wait on future work once the founder
  revisits the out-of-scope ruling.

---
*This document follows the https://specscore.md/plan-specification*
