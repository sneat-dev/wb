---
name: move
description: >-
  Transfer this whole WB session now: park it and start one successor here or on
  another machine. Use for /move, "move this session to the VM", "continue this
  session on Codex". Do not use for a bounded task delegated to a WB-dispatched
  worker (that is /offload) or to freeze without a successor (that is /park). Do
  not use for in-harness subagents.
---

# Move

Whole-session, successor **now**. This process must stop mutating the owned worktrees.

Preferred composition (all owned members, dirty local state allowed on local pickup):

```sh
wb session park --context-file <file>
wb session pickup <parked-session-id> [--to <machine>]
```

One owned worktree, clean and pushed, immediate courier (including this machine):

```sh
wb session move --handover-file handover.md
wb session move --handover-file handover.md --harness claude --model opus
wb session move --resume <handoff-id>
```

Omit `--to` to target this machine (`remote.machine`) via in-process loopback.
`--to` another configured machine uses SSH. Never `claude --bg`.

Ask a live successor to return control with `wb session recall <successor-id>`.
