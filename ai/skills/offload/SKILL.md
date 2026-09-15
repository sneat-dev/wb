---
name: offload
description: >-
  Start a new WB session for a portion of work, locally or on another machine,
  while this session stays in command. Use for /offload, "offload the review to
  claude opus", "offload tests to codex sol on VM". Do not use for in-harness
  subagents, Task/explore tools, or "run this in a subagent". Do not use to
  transfer this whole session (that is /move). Never wrap claude --bg.
---

# Offload

This session stays PIC. The successor gets its **own** WB worktree (never the live
dirty checkout) and a self-contained brief.

Do **not** trigger for in-process subagents. Those are not WB sessions.

## Command

```sh
wb task offload <task> [owner/repository...] --context-file <file>
wb task offload <task> --context-file <file> --harness claude --model opus
```

Spoken form:

- `/offload review to claude opus` → `--harness claude-code --model opus` (this machine)
- `/offload review to codex sol on VM` → `--harness codex --model sol --to <vm-machine>`

`claude` and `claude-code` are the same harness. Unknown `VM` / harness: ask once.

## Brief

Goal, context, pointers, acceptance, and: when blocked, ask the user rather than guess.

## After launch

Report task id, worktree, successor session id. Message with `wb session send`.
Ask them to return control with `wb session recall <successor-id>`.

Park without launching: `wb task park` then later `wb task pickup`.
