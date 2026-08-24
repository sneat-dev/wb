# Worktree ownership and liveness

WB records worktree ownership as append-only local metadata. An owner record
contains the agent/runtime, model, effort, the declared agent session PID, the
WB version and command that wrote it, and the time. It never replaces the
creator or an earlier session: each attachment adds another record, so a
worktree keeps its full chain of custody.

## Declare who is working

The PID is the **agent session's**, never WB's own. WB is a short-lived
command: its process is dead moments after it runs, and a recycled id would
later report an abandoned worktree as active. Only the driving session knows
its identity, so it declares it.

Once per session, through the environment — every later WB command picks it up:

```sh
export WB_AGENT_PID=$$ WB_AGENT_RUNTIME=claude-code
export WB_AGENT_MODEL=<model> WB_AGENT_ID=<session-id>
```

Best of all, register the session once at start-up and let WB attribute
everything afterwards:

```sh
wb session register --pid $PPID --runtime claude-code --model <model>
wb session register --pid 12345 --runtime claude-code --model claude-sonnet-5
```

`$PPID` from a harness tool call is the agent process itself. WB then resolves
later writes by matching its own ancestors against registered sessions — which
confirms a declaration rather than guessing an owner, since an unregistered
ancestor is never treated as one.

A start-up hook cannot do this on the session's behalf: hooks run in an
isolated subprocess whose parent is an intermediate shell, not the agent, and
they cannot export variables into the session either. A hook should prompt the
agent to register rather than invent a PID.

Inspect what registered:

```sh
wb session list
wb session list --live
wb session prune
```

Or for a single worktree:

```sh
wb worktree own <worktree-path> --pid <agent-pid> --runtime <harness> --model <model>
wb worktree own . --pid 12345 --runtime claude-code --model claude-sonnet-5
```

WB carries the chain forward by itself: any command that writes to a worktree
records the current identity first, appending only when custody actually
changed, so a session doing repeated work leaves one record rather than a
command trace. Writing without a declaration is allowed — the entry still
carries the WB version and command — but WB warns on stderr and the owner's
liveness stays `unknown`.

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

## How triage uses owner state

`wb worktree orphans` prefers a declared owner over the commit-age heuristic,
because one is proof and the other is a guess. Each row is marked
`owner live`, `owner gone`, or `owner unstated`.

| Owner state | Disposition | Basis |
|---|---|---|
| live | `active` | a declared session is running |
| gone | `decide` | its session exited, leaving unmerged work |
| unstated | falls back to commit age | inference, and the evidence says so |

A live owner outranks the age heuristic *and* the no-commit case: a session
that has not committed yet is working, not abandoned. `dirty` and `merged`
still outrank owner state — uncommitted work is most at risk exactly when its
session exits, and merged work is removable whoever owns it.

`unstated` is not the same as `gone`. Never having said who you are is not the
same as having said so and exited, and an entry carrying only WB provenance is
not a dead session. A worktree created with plain `git worktree add` is
`unstated` until someone registers, which is why the evidence for those rows
names `wb worktree own` as the fix.
