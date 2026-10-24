---
format: https://specscore.md/decision-specification
status: Approved
---

# Decision: Agent execution uses one engine, inherited leases, and additive records

**Status:** Approved
**Date:** 2026-10-24
**Owner:** alex
**Tags:** agent-execution,agent-dispatch,harnesses,experiments
**Source Idea:** —
**Supersedes:** —
**Superseded By:** —

## Context

`wb agent dispatch` proved one harness and one profile per run. An external
orchestrator needs task types, complexity and effort routing, OpenCode and `dsh`
beside Codex, honest telemetry, objective validation, independent review status,
and a fair comparison of several execution configurations on one task. Adding
those as a second subsystem would duplicate worktree creation, the detached run
owner and the run store.

## Decision

`wb agent run` and `wb agent compare` are surfaces over one engine:
`PrepareRun` (resolve → preflight → worktree → writer lease → durable record) and
`StartRun` (spawn the detached owner). `dispatch` remains the explicit low-level
form and shares both steps. `compare` prepares every attempt — and therefore
every worktree — before starting any of them, pins the first created `BaseSHA`,
and never starts an attempt whose base differs.

A writable execution holds an inherited retained-file `flock` on the real
worktree path. The descriptor travels dispatcher → owner → harness → each
validation step through `ExtraFiles`, so the kernel, not a PID check, decides
when a worktree is free. Windows continues to support only unique-worktree modes.

The durable record stays at `schema_version: 1` and only gains keys: a rich
`execution_status` beside the unchanged legacy `state`, a `telemetry` object with
nullable token counts, cost provenance, validation, and routing provenance. Review
lives in its own append-only `review.json`, so a reviewer never races the owner.

## Rationale

One engine makes "the comparison ran the same code as the single run" a property
of the code rather than a promise. Inheriting a kernel lock removes four
independent races at once (stale replace, dispatcher death, recycled PID, owner
death) and survives an orphaned worker. New keys only keep an older WB able to
list and await new records; a new `state` value would have made an older build
wait forever on a cancelled run.

## Declined Alternatives

### A pinned `CreateOptions.BaseSHA`
Create worktrees from one commit rather than verifying they landed on it. It
needs a contract change in the worktree service, which fetches `origin/<base>`
and validates that the live HEAD descends from the claim base. Verification plus
refusal to start a mismatched attempt preserves fairness without weakening that
invariant, and leaves the pinned-creation option open.

### A lease keyed by PID with stale replacement
Simpler to write, but check-remove-create lets two contenders delete each
other's fresh lease, and a task-signal-free platform cannot tell a dead owner
from a recycled PID.

### A non-blocking `compare` with an owner-side slot scheduler
It needs retained slot files, a queue duration on every owner, and a scheduler
that survives the command exiting. A synchronous `compare` that bounds its own
concurrency delivers the same honest result with far less machinery; `run` is the
asynchronous surface.

### A second `wb-agents` skill beside `wb-agent`
Two names one letter apart would collide when a harness picks a skill. The
dispatch reference moved into `wb-agent/references/dispatch.md` and the old skill
is retired.

## Consequences at Decision Time

Expected positive:

- `run` and `compare` share one dispatch path, so a comparison cannot drift
  into a second implementation;
- every attempt in an experiment starts from one pinned base and its own
  worktree, and a moved base is reported instead of silently compared;
- a worktree is freed by the kernel when the last process holding the lease
  exits, including an orphaned worker;
- an older WB can still list and await a record written by this build;
- review and validation can never lose each other's writes on `run.json`.

Expected negative:

- eight new command leaves, each needing a capability row, a flag-matrix line
  and README documentation;
- `dsh` usage and cost are unavailable, so its comparison rows are honest nulls;
- a stray background process keeps a worktree leased until it exits;
- OpenCode and `dsh` run with the real home directory, so worker tools can read
  their auth stores, unlike the private `CODEX_HOME` a Codex run uses;
- Windows supports only unique-worktree execution modes.

## Observed Consequences

None observed yet.

## Affected Features

- [Agent Execution](../features/agent-execution/README.md)
- [Agent Dispatch](../features/agent-dispatch/README.md)
- [Worktree Lifecycle](../features/worktree-lifecycle/README.md)

## Review record

Gate A (architecture) and Gate B (implementation) are reviewed independently by
Claude Opus 5.5 at high effort against this decision and
[the feature](../features/agent-execution/README.md). Gate A returned
"approve with required changes" and then approved revision 2 after the lease
inheritance, comparison worktree isolation, schema compatibility, review sidecar,
effort/variant, `dsh` argv, name-collision and spec-conformance findings were
addressed.
