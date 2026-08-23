# Work log verbs

Use the local journal under `.wb/local/worklog/` for progress that must survive
an abandoned checkout.

```sh
wb worktree log init . --agent <session-or-agent-id> --agent-runtime <codex-or-claude> --model <exact-model-or-unknown>
wb worktree log steer . --prompt next-slice
wb worktree log checkpoint . --message progress
wb worktree log show . --format json
wb worktree log refresh .
wb worktree log recover .
wb worktree log sync .
wb worktree log finalize . --result success
```

`wb worktree set --prompt` remains the human-facing alias of `log steer` and
records `human_declared`. Bare `wb worktree log` still dumps private prompt
bodies for agent bootstrap; prefer `log show` when bodies must stay redacted.

`log sync` stays offline until a Synchestra endpoint is configured and retains
the local outbox. `log handoff` / `log finalize` record local events first;
pass `--apply` to transfer or seal the hybrid claim.

`log init` also appends the invoking agent/session to the worktree's local
owner history. Read [ownership.md](ownership.md) for PID liveness and takeover
triage.
