---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Default orchestrators to wb agent dispatch, not manual worktree lifecycle

**Status:** Draft
**Date:** 2026-09-17
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:efficient-agent-pipeline

## Problem Statement

How might we stop orchestrator sessions from hand-rolling worktree create/commit/push/land cycles themselves, so their own transcript doesn't grow into the dominant token cost?

## Context

This is the first, cheapest phase of
[efficient-agent-pipeline](efficient-agent-pipeline.md), which carries the cost
model and the wider direction. It is filed separately because it needs no CLI
change at all, and should not wait on one.

### The observation

Three orchestrator sessions burned 46% of a weekly Claude budget in under 24
hours. Each ran 800–900 turns at $79–$294, from Claude Code's own `cost-state`
accounting. Bash tool-result payloads were small — 250–411 KB per session, about
110 tokens of output per turn — so the driver is turn *count*, not output size,
and with prompt caching that cost grows with the square of turn count.

Breaking down those sessions' Bash calls: `wb worktree create` was invoked
manually 15–30 times per session, while `wb agent dispatch` / `wb agent await` —
which already exist and already collapse create-and-run-a-worker into one
governed call — were used 1–3 times in total across all three.

### The root cause is routing, not documentation

The tempting conclusion is that the skills teach the wrong default. They do not.
`wb-agents` already teaches exactly the right thing, in its own frontmatter:
*"Prefer `wb agent dispatch` over hand-rolling … and `wb agent await` over
polling. Never read a worker's whole transcript into a parent agent's context."*
Rewriting that prose would change nothing, because the problem is that an
orchestrator never loads it:

- The root `wb` skill's "Route by situation" table lists `wb-worktrees`,
  `wb-merge`, `wb-fleet`, `wb-hooks`, `wb-deps`, `wb-ci`, `wb-branches`,
  `wb-streams`, `wb-daemon`, `wb-install`, `wb-run`,
  `wb-dependency-campaign`, `wb-skills` — **`wb-agents` is not in it at all.**
- Every trigger in `wb-agents`' description is phrased as something a *user*
  says: "offload this task", "hand this to a cheaper model", "check on that
  dispatched worker". An orchestrator that decided on its own initiative to hand
  work off never utters any of them.
- `wb-worktrees`, by contrast, triggers on *"Use before editing or branching"*,
  which fires unconditionally, and shows `wb worktree create` as its fast path
  with no fork for "am I doing this myself, or handing it to a worker?"
- Neither skill cross-references the other in either direction.

So `wb-worktrees` wins the load every time, and the correct guidance is never in
context at the moment the decision is made. That makes this a four-line fix in
the routing surface rather than a documentation rewrite — cheaper to ship and far
more likely to work.

A smaller instance of the same shape occurred while drafting this idea: the
authoring agent ran `specscore idea new` in the canonical clone without checking
`.worktree.md`, and spent several recovery turns. That is a repo-hygiene failure a
guard should catch rather than evidence for a delegation default, but it is the
same underlying habit — doing mechanical lifecycle steps by hand, one call at a
time.

## Recommended Direction

Make `wb agent dispatch` + `wb agent await` the *routable* default for handing off
a bounded implementation task, by fixing where the guidance lives rather than what
it says:

1. Add `wb-agents` to the root `wb` skill's routing table, with a trigger phrased
   from the orchestrator's own situation — "about to hand a bounded task to a
   worker" — not from a user's request.
2. Add an explicit fork at the top of `wb-worktrees`' fast path: doing this work
   yourself continues here; handing it to a worker goes to `wb-agents`.
3. Cross-reference the two skills in both directions.

Treat the manual create-then-drive-it-yourself path as a named exception with
criteria an orchestrator can actually evaluate, rather than the vague "when
someone wants to watch it live":

- the task is not bounded — no knowable diff, or an unresolved design decision;
- the brief would cost more than the work (one or two file edits);
- founder decisions land mid-flight;
- a cross-repo wave needs one landing owner to sequence it.

## Alternatives Considered

- **Rewrite the `wb-worktrees` and `wb-agents` bodies to show dispatch first.**
  Rejected as the primary fix: `wb-agents` already says it, and a skill that never
  loads cannot teach anything. Body edits are worth making, but only after the
  routing fix that gets the skill loaded at all.
- **Add a `--cd` flag to `wb worktree create`/`end`.** Rejected: a subprocess
  cannot mutate its parent shell's cwd. `--format json` piped through `jq` already
  achieves it in one Bash call.
- **Enforce a hard turn cap on orchestrator sessions inside WB.** Deferred to
  [efficient-agent-pipeline](efficient-agent-pipeline.md): a cap with nowhere to
  put the work truncates an effort mid-flight.

## MVP Scope

Change the WB skill routing surface so that an orchestrator about to hand off a
bounded task loads `wb-agents` instead of `wb-worktrees`: one row in the root
skill's routing table, one agent-initiated trigger in `wb-agents`' description,
one fork at the head of `wb-worktrees`' fast path, and a cross-reference each way.
No CLI change, no new flags.

## Not Doing (and Why)

- Adding a literal `--cd` flag to `wb worktree create` — a subprocess cannot change its parent shell's cwd, and `--format json` plus `jq` already solves it in one call
- Streaming worker output live into the orchestrator's context — reintroduces the replay cost this removes; `wb agent logs` is bounded by default and sufficient
- Converging `--format json` directory field names across `worktree end` / `pr land` — real inconsistency, but a breaking change to a machine-readable contract; belongs in its own additive task
- Any CLI or pipeline change — that is [efficient-agent-pipeline](efficient-agent-pipeline.md); this phase is deliberately zero-code

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | The routing fix loads `wb-agents` at the moment an orchestrator decides how to hand off work | Replay representative orchestrator openings against the revised routing surface and record which skill loads |
| Must-be-true | Dispatch reduces total cost per landed change, not just the orchestrator's share | Compare cost per landed PR counting worker and retry runs, using `wb agent await`'s recorded `usage`, against the orchestrator's marginal two turns — computed from instrumentation rather than a matched-pair experiment, which this workflow cannot produce |
| Should-be-true | `wb agent logs` gives enough post-hoc visibility that no live-streaming mode is needed | Record, for each dispatched run that went wrong, whether default (bounded) `logs` output was sufficient or `--raw` was required |
| Might-be-true | A thin brief to a cheap worker does not cost more in retries than it saves | Track retry counts and total run cost per dispatch against brief length and acceptance-criteria completeness |

## SpecScore Integration

- **New Features this would create:** none — a revision to the WB skill routing surface
- **Existing Features affected:** the `wb`, `wb-worktrees` and `wb-agents` skills
- **Dependencies:** none; [efficient-agent-pipeline](efficient-agent-pipeline.md) builds on it

## Open Questions

- Does a routing-table row plus a reworded trigger reliably beat `wb-worktrees`'
  unconditional "before editing or branching" trigger, or does that trigger itself
  need narrowing?
- With three delegation mechanisms available — an in-harness subagent, `wb agent
  dispatch`, and a full successor session via `wb task offload` — should the same
  routing change also state which to reach for, or is that a separate concern?
