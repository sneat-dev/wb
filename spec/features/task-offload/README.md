---
format: https://specscore.md/feature-specification
status: Approved
---
# Feature: Task offload, park, and pickup

## Summary

Agents can freeze a **portion** of work (`wb task park`), start it later
(`wb task pickup`), or start it immediately (`wb task offload`) as a successor
WB session with its own worktree. This is distinct from whole-session
`wb session park` / `pickup` / `move`, and from in-harness subagents.

## Behavior

#### REQ: offload-owns-a-new-worktree

`wb task offload` and `wb task park` MUST create a new WB worktree and MUST NOT
use the caller's live dirty checkout as the successor working directory.

#### REQ: offload-is-harness-neutral

Successor launch MUST use WB's tmux launcher with a closed harness set
(`claude-code`, `codex`). Spoken `claude` MUST select `claude-code`. A requested
model MUST be passed to the successor even across harnesses. Launch MUST NOT
use `claude --bg`.

#### REQ: local-loopback-move

When `wb session move` targets this machine's validated `remote.machine`
(including by omitting `--to`), delivery MUST use an in-process loopback
courier rather than SSH.
