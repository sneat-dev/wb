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
implementation and tmux moved behind it unregressed, then build the MVP wake
flow — "your PR has an outcome → your session wakes" — on top of it: automatic
identity capture, delivery resolved and guarded at send time, facts-only
templates, and visibility in existing listings.

## Journey

**An agent registers, inside herdr, and keeps working.** `wb worktree create`
(which calls `wb session register`) captures `HERDR_PANE_ID`,
`HERDR_WORKSPACE_ID`, and `CLAUDE_CODE_SESSION_ID` with no flags. Observable
good result: `wb session list` shows the session's pane and `transport: herdr`
immediately, before anything else happens.

**The agent opens a pull request and moves on to other work — a null-action
step.** `wb pr create` records the task-to-PR binding. Nothing about a wake is
visible yet, and nothing should be: observable good result is that
`wb wait list` shows no outstanding wake, because delivery is resolved later,
at the outcome, not now.

**Checks settle while the agent is idle.** Observable good result: the daemon
delivers one facts-only, submitted message — `[wb daemon] sneat-dev/wb#NNN:
checks passed` — into the exact pane, the transcript shows it with the same
`❯` prefix a human's own input gets, and `wb session list` / `wb wait list`
now mark that wake delivered.

**Checks settle while the agent is mid-turn — a guarded null-action step.**
Observable good result: nothing is sent. The daemon re-checks on the next
tick and only delivers once the session is idle or done *and* its input box
is empty, so a message never lands on top of a half-typed human draft.

**A second agent is doing the same thing in tmux, not herdr.** Observable good
result: the identical PR-outcome event is delivered advisory-only — pasted,
never submitted — because the capability matrix says tmux cannot verify
idle/empty-input, so WB will not guess. The agent sees it, unsubmitted, next
time it looks.

**A third session has neither herdr nor tmux — a bare shell.** Observable good
result: the event is recorded only. `wb wait list` says so explicitly; nothing
hangs, and nothing is silently dropped.

**herdr itself is unreachable when a herdr-registered session's event fires.**
Observable good result: WB reports a distinct "herdr socket unreachable"
failure, then falls back to what automatic selection would have chosen
without herdr (tmux, or `none`) rather than aborting the caller's command.

## Approach

Build the transport interface and its capability matrix first, as a contract
both implementations satisfy. Move today's tmux code behind it with
characterization tests proving no behavior change, in parallel with building
the herdr implementation on the `internal/herdr` adapter (Task 1, already
in progress in a separate lane). Layer identity capture on the same interface,
then the wake flow's resolution, guards, message templates, and listings on
top of both. Finish with the explicit herdr failure-mode surface and one
whole-journey end-to-end test walking every stage above, including its
null-action steps, against fake transports — never the real herdr socket or
tmux server.

## Tasks

### Task 1: `internal/herdr` adapter package

**Id:** task-1
**Verifies:** herdr-session-transport#ac:version-drift-is-detected
**Depends-On:** —
**Status:** in_progress

Already in progress in a separate lane (worktree `herdr-adapter`). A Go
wrapper over the `herdr` CLI: `pane current/list/get`, `agent
list/get/prompt/send-keys/wait`, version detection (`herdr --version`),
identity-from-environment (`HERDR_PANE_ID`, `HERDR_WORKSPACE_ID`), and a
fakeable exec seam so no test reaches the real herdr socket. Every other task
below that needs herdr goes through this package, never through `os/exec`
directly.

### Task 2: Define the transport interface and capability matrix

**Id:** task-2
**Verifies:** herdr-session-transport#ac:transport-selected-automatically, herdr-session-transport#ac:explicit-override-fails-closed, herdr-session-transport#ac:tmux-never-delivers-unguarded, herdr-session-transport#ac:none-transport-records-only
**Depends-On:** —
**Status:** planning

Define one Go interface covering message, move, park, receive, launch,
courier delivery, successor messaging, and paste-into-pane, plus a typed
capability matrix (live status, empty-input check, submit-vs-advisory) each
implementation declares. Add automatic selection (`HERDR_PANE_ID` → herdr;
`TMUX`/recorded tmux name → tmux; else `none`), explicit `wb.yaml`/flag
override with fail-closed validation, and the `none` implementation as a
first-class no-op that records rather than errors. No production call site is
rewired yet — this task ships the contract and its own tests.

### Task 3: Move tmux behind the transport interface, no behavior change

**Id:** task-3
**Verifies:** herdr-session-transport#ac:tmux-behavior-is-unregressed, herdr-session-transport#ac:tmux-never-delivers-unguarded
**Depends-On:** 2
**Status:** planning

Write characterization tests first for the current tmux behavior wherever
coverage is thin, then move it behind Task 2's interface with no behavior
change: `internal/sessionlaunch/tmux.go`, `internal/sessionlaunch/launch.go`,
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
capability matrix (no live status, no empty-input check) and its documented
advisory/record-only fallback; never wire an unguarded submit path.

### Task 4: Wire the herdr transport onto the `internal/herdr` adapter

**Id:** task-4
**Verifies:** herdr-session-transport#ac:transport-selected-automatically, herdr-session-transport#ac:explicit-override-fails-closed, herdr-session-transport#ac:none-transport-records-only
**Depends-On:** 1, 2
**Status:** planning

Implement the herdr transport against Task 1's adapter: pane resolution for
message/move/park/receive/launch, `agent prompt` for submitted delivery,
`agent send-keys` for advisory delivery, and `agent wait`/`agent get` for the
capability checks Task 6 depends on. Declare herdr's capability matrix (live
status, empty-input check, both delivery modes).

### Task 5: Automatic identity capture at registration

**Id:** task-5
**Verifies:** herdr-session-transport#ac:identity-captured-automatically-in-herdr, herdr-session-transport#ac:identity-degrades-outside-herdr, herdr-session-transport#ac:subagent-resolves-to-parent-owner
**Depends-On:** 2
**Status:** planning

Extend `wb session register` and the session record with `HERDR_PANE_ID`,
`HERDR_WORKSPACE_ID`, and the harness session ID (`CLAUDE_CODE_SESSION_ID`,
or the Codex equivalent once confirmed — see Open Questions), captured
together and recorded alongside the resolved transport from Task 2's
selection logic. Degrade to tmux identity, then to `none` with only harness
ID and machine recorded, per REQ:identity-capture-outside-herdr. Verify a
subagent's `CLAUDE_PID`/`CLAUDE_CODE_SESSION_ID`/`HERDR_PANE_ID` resolve to
its parent's existing record rather than creating a second one.

### Task 6: Delivery resolution and re-checked guards

**Id:** task-6
**Verifies:** herdr-session-transport#ac:own-pr-outcome-wakes-the-owner, herdr-session-transport#ac:guards-refuse-an-unsafe-wake
**Depends-On:** 4, 5
**Status:** planning

Walk task → current Work Log claim → session → transport identity at
delivery time (not at registration), re-walked on every attempt. Before any
submitted delivery, verify and re-verify immediately before sending: live
claim chain (successor claims inherit the binding), matching native harness
session ID, same machine, `idle`/`done` state, and empty input box where the
transport exposes it. Cancel rather than send on any guard failure; never
cache a guard check across the send.

### Task 7: Facts-only templates and the submit/advisory split

**Id:** task-7
**Verifies:** herdr-session-transport#ac:own-pr-outcome-wakes-the-owner, herdr-session-transport#ac:non-pr-events-stay-advisory, herdr-session-transport#ac:daemon-line-cannot-approve
**Depends-On:** 6
**Status:** planning

Add the fixed, closed template set (identifiers only — repository, PR number,
check name or count, SHA). Restrict submitted (`agent prompt`-equivalent)
delivery, enumerated in code, to the session's own registered PR outcome;
every other event class is advisory or record-only. Ensure no `--approved-by`
or equivalent evidence path accepts a daemon message or its transcript line,
and that command help states a `[wb daemon]` line is a notification, never an
approval.

### Task 8: Wake visibility in session and wait listings

**Id:** task-8
**Verifies:** herdr-session-transport#ac:wake-subscription-is-visible
**Depends-On:** 6
**Status:** planning

Surface outstanding and delivered wake subscriptions in `wb session list` and
`wb wait list`, naming the task, target PR, and last delivery outcome.

### Task 9: Explicit herdr failure modes

**Id:** task-9
**Verifies:** herdr-session-transport#ac:missing-herdr-binary-fails-explicitly, herdr-session-transport#ac:unreachable-socket-fails-explicitly, herdr-session-transport#ac:version-drift-is-detected
**Depends-On:** 1, 4
**Status:** planning

Give each herdr failure mode a distinct, actionable message — binary not on
`PATH`, socket unreachable, pane/agent no longer exists, version drift
(missing command or unrecognized JSON shape) — and fall back to the automatic
-selection outcome with herdr excluded, except when herdr was named by an
explicit override, which refuses per Task 2's fail-closed rule.

### Task 10: Prove the whole wake journey end-to-end

**Id:** task-10
**Verifies:** herdr-session-transport#ac:own-pr-outcome-wakes-the-owner, herdr-session-transport#ac:guards-refuse-an-unsafe-wake, herdr-session-transport#ac:tmux-behavior-is-unregressed, herdr-session-transport#ac:none-transport-records-only, herdr-session-transport#ac:wake-subscription-is-visible
**Depends-On:** 3, 7, 8, 9
**Status:** planning

Walk the Journey above against fake herdr and fake tmux adapters in one
end-to-end test: register in herdr, register a task's PR, hold delivery while
`working`, deliver once idle with an empty input box, show the same event as
advisory-only on a tmux-transport session and record-only on a `none`-transport
session, and confirm both listings and the transcript-visible submitted line.
Assert on the exact commands issued to the fake transports (mechanism), not
only on the reported outcome.

## Open Questions

- Task 5 assumes a Codex equivalent of `CLAUDE_CODE_SESSION_ID` exists. This
  was not observed in this session (no Codex process was running to inspect).
  If none exists, Task 5 records the harness session ID as `unknown` for
  Codex, per REQ:identity-capture-outside-herdr's degrade rule, and this note
  is removed once confirmed either way.
- The Feature's "Directing agents" section is deliberately out of this Plan's
  scope: it needs a founder decision (see herdr-session-transport's Open
  Questions) before it can become a task.

---
*This document follows the https://specscore.md/plan-specification*
