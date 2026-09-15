---
name: pickup
description: >-
  Start a successor for a previously parked WB session or task, locally or on
  another machine. Use for /pickup, "pick up parked work", "resume the parked
  session". Do not use for in-harness subagents or to freeze work (that is /park).
---

# Pickup

Start exactly one successor from a parked checkpoint. `wb session resume` is the
same command as `wb session pickup`.

## Parked session

```sh
wb session pickup <parked-session-id>
wb session pickup <parked-session-id> --to <machine>
```

Use `parked_session_id` from `wb session park` or `wb session list --format json`,
not `wb_session_id`.

## Parked task

```sh
wb task pickup <parked-task-id>
wb task pickup <parked-task-id> --to <machine>
```

Spoken "on VM" / "to claude opus" maps to `--to <configured-machine> --harness claude-code --model opus`.
Unknown machine or harness: refuse and ask once. No local fallback after a remote failure.
Retry with the same id; do not allocate a second successor.
