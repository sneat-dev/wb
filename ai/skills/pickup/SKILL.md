---
name: pickup
description: >-
  Continue previously parked WB work in the current registered chat by default,
  or explicitly launch one local Herdr or remote successor. Use for /pickup,
  "pick up parked work", "resume the parked session". Do not use for in-harness
  subagents, to freeze work (/park), or to delegate a bounded worker (/offload).
---

# Pickup

`wb session pickup` and `wb session resume` are aliases. Local pickup continues
in this identifiable registered chat; it never silently creates another agent.

## Parked session

Use `parked_session_id` from `wb session park` or `wb session list --format json`,
not `wb_session_id`.

```sh
wb session pickup <parked-session-id> --current --format json
```

When the result is `awaiting_context_ack`, read its private `continuation_path`
yourself. Then issue a second tool call using its exact `continuation_digest`:

```sh
wb session pickup <parked-session-id> --current --ack-context <continuation-digest>
```

Never ask the human to copy context or digests. Do not continue member edits
until the result confirms `resumed`. Preparation freezes the complete caller
record; do not re-register different metadata between preparation and acknowledgment.
A conflicting live snapshot refuses rather than silently rebinding. Native current-chat
discovery currently supports macOS/Linux; Windows primitive support is not a
Windows current-chat journey. Choose an explicit launch/target subject to its
platform preflights there. On macOS/Linux, if the caller is unidentified, register
its real harness/native chat identity first; never invent a PID or overwrite a
peer chat's registration. `CODEX_THREAD_ID` identifies this chat; `CODEX_SESSION_ID` is distinct shared root
context and must not be used as its ownership key. Stored NativeHarnessID/AgentID
remain aliases of the per-chat thread ID.

Explicit alternatives:

```sh
wb session pickup <parked-session-id> --launch
wb session pickup <parked-session-id> --to <machine>
```

A new local/remote agent requires original Herdr TUI 0.9.1 with a running selected
server and the chosen harness. Current-chat pickup requires no terminal.
A retained legacy empty setup attempt requires separate historical evidence.
Do not infer absence from empty directories. If WB reports an exact state digest,
verify a saved positive failure proving the old invocation stopped before wrapper
startup. Only then use `--ack-unstarted-launch <digest>` on current/default
pickup. This is the registered operator's explicit assertion, not a filesystem
proof; never use it for unknown execution, a launched wrapper, remote/explicit
launch, or a fresh dead-actor recovery. The assertion is recorded in the initial
immutable preparation; ordinary continuation acknowledgment is still required.

After a process crash, `--recover-current` appends a new actor only when WB proves
the earlier process generation gone. Read and acknowledge the returned context
again, retaining `--recover-current` on the completing tool call. Never edit
immutable preparation or lifecycle records; live or uncertain actors refuse.
A shared still-live app-server cannot establish individual-chat death.

## Parked task

```sh
wb task pickup <parked-task-id>
wb task pickup <parked-task-id> --to <machine>
```

Spoken "on VM" / "to claude opus" maps to `--to <configured-machine> --harness claude-code --model opus`.
Unknown machine or harness: refuse and ask once. No local fallback after a remote
failure. Retry the same ID; do not allocate another logical successor.
