---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Task offload, park, and pickup

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/task-offload?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/task-offload?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/task-offload?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/task-offload?op=request-change) |
**Status:** Approved
**Source Ideas:** —

## Summary

Agents can freeze a **portion** of work (`wb task park`), start it later
(`wb task pickup`), or start it immediately (`wb task offload`) as a successor
WB session with its own worktree. This is distinct from whole-session
`wb session park` / `pickup` / `move`, and from in-harness subagents.

`wb task` owns the durable, addressable portion of work: a parked task keeps
its worktree and its continuation brief until a successor is started, locally
or on another machine.

## Problem

A parent agent that wants to hand a bounded piece of work to a different
harness — or to the same work later, on another machine — has nowhere to put
it. Whole-session parking is the wrong granularity: it freezes everything the
current session owns. An in-harness subagent is the wrong mechanism: it shares
the parent's process, provider, and model, and cannot be resumed after the
parent stops.

Three properties are missing: a durable record for a portion of work that
outlives the parent process; a guaranteed isolated checkout so the parent's
live dirty worktree is never the successor's working directory; and a launch
step that can target this machine or a configured remote one through the same
path.

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

#### REQ: task-park-survives-without-successor

`wb task park` MUST persist the task record, its worktree, and its continuation
brief under WB's resolved home without starting any successor, so that
`wb task pickup` can start exactly one successor later, after the parking
process is gone.

#### REQ: skill-boundary-with-agent-dispatch

The `/offload` Agent Skill MUST drive the supervised bounded-task flow defined
by [Agent Dispatch](../agent-dispatch/README.md) — a cheap native supervisor
subagent that calls `wb agent dispatch` and `wb agent await` and returns
`PASS`, `FAIL`, or `ESCALATE`. `wb task offload` remains the durable
peer-session variant of the same intent, and is the one to use when the
successor must be an addressable WB session that survives on this machine or
another one. The two MUST remain distinguishable by outcome: `dispatch`
returns execution facts about a bounded worker; `wb task offload` returns a
successor WB session ID. Neither MUST be presented as the other.

## Acceptance Criteria

### AC: portion-of-work-is-isolated (verifies REQ:offload-owns-a-new-worktree, REQ:task-park-survives-without-successor)

Scenario: Park and pick up a portion of work
Given a registered agent session in a dirty worktree
When it runs `wb task park <task> --context-file brief.md` and the process later exits
Then a new worktree exists with the task record and brief under WB's home, and a later `wb task pickup <task-id>` starts exactly one successor in that worktree

### AC: successor-launch-is-harness-neutral (verifies REQ:offload-is-harness-neutral)

Scenario: Request another harness
Given a configured, reachable target
When a task is offloaded with a requested harness and model
Then the successor starts under the closed harness set with the requested model, through the tmux launcher and never `claude --bg`

### AC: same-machine-move-does-not-use-ssh (verifies REQ:local-loopback-move)

Scenario: Move to this machine
Given `remote.machine` names this machine
When `wb session move` runs without `--to`
Then delivery completes through the in-process loopback courier without invoking SSH

### AC: offload-skill-drives-supervised-dispatch (verifies REQ:skill-boundary-with-agent-dispatch)

Scenario: Supervised bounded task
Given the `/offload` skill and a bounded task
When a parent agent runs the skill against a configured WB agent profile
Then the skill drives `wb agent dispatch` and `wb agent await` and returns a single `PASS`, `FAIL`, or `ESCALATE` verdict, and the peer-session path is offered only when the caller needs an addressable successor session

## Open Questions

- Whether `wb task offload` should eventually be re-expressed on top of the
  `wb agent dispatch` primitive for the local case, keeping the courier path
  only for remote targets. Deferred until the dispatch primitive has been used
  in real work; see Agent Dispatch's Open Questions.

---
*This document follows the https://specscore.md/feature-specification*
