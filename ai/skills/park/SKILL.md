---
name: park
description: >-
  Freeze a WB agent session or a named task without starting a successor. Use for
  /park, "park this session", "checkpoint this work for later", or overnight
  stop. Do not use for in-harness subagents, or to start work on another
  machine now (that is /move or /offload), or to delegate a bounded task to a
  WB-dispatched worker (that is /offload). Do not use for Work Log claim
  transfer.
---

# Park

Persist continuation and stop mutating the parked checkout(s). No successor starts.

## This session

```sh
wb session park --context-file <file>
```

Write a cold-start brief: goal, decisions, traps, next step, exact ids. No secrets,
transcript dumps, or raw logs. Then **stop editing those worktrees**. Resume later
with `/pickup`.

## A portion of work

```sh
wb task park <task> [owner/repository...] --context-file <file>
```

Creates a **new** WB worktree from the current base. This session stays in command.
Pick it up later with `wb task pickup <task-id>`.

Registration is not a precondition for `wb session park`; it registers you if needed.
Never hand-roll a park.
