# Worktree ownership and liveness

WB records worktree ownership as append-only local metadata. An owner record
contains the agent/runtime, model, effort, caller PID, and attachment time.
It never replaces the creator or an earlier session: creating a worktree,
resuming it, or attaching with `log init` adds another owner record.

Create with explicit execution identity:

```sh
wb worktree create <task> <owner/repository> \
  --agent <session-or-agent-id> --agent-runtime <codex-or-claude> \
  --model <exact-model-or-unknown> \
  --original-prompt-file <private-prompt-file>
```

When another agent/session takes over an existing checkout, attach it before
editing so its PID and identity are preserved:

```sh
wb worktree log init <worktree-path> \
  --agent <session-or-agent-id> --agent-runtime <codex-or-claude> \
  --model <exact-model-or-unknown>
```

Inspect one worktree without exposing prompt bodies:

```sh
wb worktree info <worktree-path>
wb worktree info <worktree-path> --format json
```

`info` lists every recorded owner and evaluates each PID at read time:

- `active` — the PID currently exists (or exists but cannot be inspected).
- `orphaned` — the PID is conclusively gone.
- `unknown` — the PID was absent or liveness could not be determined.

PID liveness is a current local signal, not proof that a PID has not been
reused for another process. Treat it as triage evidence and use the Work Log,
Git state, and a handoff record to decide ownership.

Inventory globally, per task, or per repository filter:

```sh
wb worktree list
wb worktree list <task>
wb --filter <owner/repository> worktree list
wb worktree list --only active
wb worktree list --only orphaned
```

`--only active` returns worktrees with at least one live owner PID.
`--only orphaned` returns worktrees without a live owner PID; a worktree with
only unknown PID evidence is intentionally excluded from both filters and
needs review.
