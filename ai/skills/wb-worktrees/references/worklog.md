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

For an internally-created dependency-campaign worktree whose immutable
manifest has a blank `ClaimID`, use the explicit audited recovery path after
reviewing its diagnosis:

```sh
wb worktree log recover <worktree> --establish-claim
wb worktree log recover <worktree> --establish-claim --apply
```

This publishes the deterministic missing private claim and rebuilds derived
projections. It refuses non-campaign manifests, changed live branch/base
identity, or a manifest that already names a claim; immutable manifest and
journal records are never rewritten.

`wb worktree set --prompt` remains the human-facing alias of `log steer` and
records `human_declared`. Bare `wb worktree log` still dumps private prompt
bodies for agent bootstrap; prefer `log show` when bodies must stay redacted.

`log sync` stays offline until a Synchestra endpoint is configured and retains
the local outbox. `log handoff` / `log finalize` record local events first;
pass `--apply` to transfer or seal the hybrid claim.

`log init` also appends the invoking agent/session to the worktree's local
owner history. Read [ownership.md](ownership.md) for PID liveness and takeover
triage.

## Remote checkpoints — persist often without paying the full test tax

Unless `--skip-remote` is given, `log checkpoint` also force-pushes the exact
current HEAD to `refs/wb/checkpoints/<task>` at origin:

```sh
wb worktree log checkpoint . --message progress
wb worktree log checkpoint . --message progress --skip-remote
```

This is a fast, Tier-0-only persistence path: WB's managed pre-push hook
recognizes the `refs/wb/checkpoints/*` namespace and runs neither lint nor
test for it, and the namespace never triggers CI. Retrieve a checkpoint from
another machine with:

```sh
wb worktree checkpoint-fetch . --task <task>
```

which lands the commit under the identically named local
`refs/wb/checkpoints/<task>` ref — never a branch, never checked out
automatically. **A checkpoint is NOT a landing receipt.** It proves a commit
reached the remote, never that it merged anywhere. Work is landed only when
it is merged and pushed to its target branch on origin — never use a
checkpoint ref, and never bypass Git's own hook enforcement, as a substitute
for that.
