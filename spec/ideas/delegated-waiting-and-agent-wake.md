---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Delegated waiting — `wb wait`, and waking the agent less

**Status:** Draft
**Date:** 2026-09-18
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:efficient-agent-pipeline, extends:agent-lane-verbs

## Problem Statement

How might we stop asynchronous work from depending on an agent remembering it,
without paying a frontier-model turn every time a machine-checkable condition
changes?

## Context

### The triggering observation

An orchestrator session ended a turn with:

> "Still in flight and unattended: wb#544, wb#575, wb#577, ext-contracts#69,
> backstage#491, backstage#492, calendarius#73. I'll land them as CI clears."

Nothing was watching any of them. The commitment lived only in the transcript,
and the transcript was about to be compacted.

The same session then demonstrated the failure mode a second way. To wait for
one PR it launched ad-hoc harness background commands of the form:

```sh
until s=$(gh pr view 581 --repo sneat-dev/wb --json mergeStateStatus -q .mergeStateStatus); \
  [ "$s" != "BLOCKED" ] && [ "$s" != "UNKNOWN" ]; do sleep 60; done; gh pr view 581 ...
```

It launched that loop **twice for the same PR** — two live pollers, neither aware
of the other, because nothing owned the waiting. It also had four earlier
one-shot variants of the same query in flight. Six processes, one question.

This is the real cost signature. It is not that the agent waits badly; it is
that waiting is re-implemented per turn, in the transcript, by the most
expensive component in the system.

### What already exists in WB

This audit is the main finding of the idea, and it substantially changes the
proposal that prompted it. WB already owns most of the machinery a "durable
watch" design would introduce:

| Capability | Where it already lives |
|---|---|
| Bounded, authoritative CI observation | `internal/orchestrate/ciwait.go`, `wb ci wait` |
| Signed webhook ingest, HMAC verification, installation binding | `hub/`, `cmd/wb/daemon_hub_webhook.go` |
| Durable event queue with cursor, dedupe, retry, supersession | `internal/repositoryevents`, `internal/runqueue` |
| Durable session wake with receipt + ACK | `internal/sessionmessenger`, `sessioncourier`, `sessionmove` |
| Durable operation lifecycle: submit/get/wait/cancel | `cmd/wb/daemon_operation.go` |
| Park/resume of a whole session aggregate | `spec/features/park-and-resume-agent-sessions` |

A durable-watch programme is therefore mostly **wiring existing parts**, not new
architecture. Two consequences follow, one encouraging and one constraining.

### Constraint: the App event contract cannot carry rich context

`api/githubapp/repositoryevent` states its exclusion list as a requirement, not
an implementation detail:

> It intentionally excludes webhook payloads, installation IDs, credentials,
> paths, actor identities, commit messages, and diagnostics.

The Workbench GitHub App is a shared, multi-tenant provider. Routing PR comment
bodies, review text, or CI log excerpts through it would relay private
repository content through infrastructure that deliberately never sees it.

So "context-rich events" cannot be satisfied by enriching the App contract. The
App event is a **trigger only**. Enrichment must happen locally on the
developer's own daemon, under the developer's own credentials. Any design that
assumes otherwise is not implementable without reversing an approved privacy
requirement.

### Constraint: `wb ci wait` already refused to be a background watcher

Its own documentation makes the stance explicit:

> Every invocation is bounded (eight minutes by default, never ten), foreground,
> and terminating. […] This command never starts a detached watcher or
> background loop.

That is not an oversight to correct. A long-lived watcher's "green" can go stale
between observation and use, which is exactly what `wb ci wait`'s terminal
reread exists to prevent. A new verb that holds a merge verdict open for hours
would reintroduce the hazard that design removed.

The correct shape is therefore a **bounded, terminating, resumable** wait —
longer slices and more conditions than `wb ci wait`, but the same contract:
pending is a first-class result carrying exact resume arguments.

## The critical question: is waking the agent even the goal?

The prompt behind this idea optimises for waking the agent *better*. The
evidence argues for waking it *less*.

Of the seven unattended PRs, the overwhelmingly common outcome was "checks went
green, so land it". That is deterministic. WB already knows how to do it — `wb pr
land` — and the agent had already decided to do it. Waking a frontier model to
issue a command it has already chosen is the single most expensive way to
execute a decision that was made an hour earlier.

The repository's own roadmap already contains the better framing, unclaimed:

> Make `wb worktree land` consume focused receipts and **escalate only
> actionable failures or semantic decisions**.

So the value ordering is:

1. **Deterministic outcome** (all required checks green, no conflicts, review
   satisfied) → WB completes the pre-authorised action. No wake.
2. **Semantic outcome** (CI failed, changes requested, conflict, unexpected
   state) → wake an agent, with enough context to act.
3. **Nothing happened** → stay quiet.

A design that treats every state change as a wake candidate inverts this. The
wake path is the exception path, not the main line.

This does **not** license autonomous merging. `rule:one-landing-owner-per-target`
and landing review evidence still bind; the pre-authorisation must be explicit
and carry the agent's review artifact. "No wake" means WB executes a decision the
agent already made and recorded — never one WB inferred.

## Proposed direction

### A single bounded waiting verb

`wb wait` becomes the one verb for "block until a WB-known condition holds",
replacing per-domain waits and ad-hoc `gh` loops.

```sh
wb wait pr sneat-dev/wb#581 --until checks-settled
wb wait pr wb#544 wb#575 ext-contracts#69 calendarius#73 --until changed
```

Naming: `wait`, not `await`. WB already spells this concept `wait` twice
(`wb ci wait`, `wb daemon operation wait`), and the CLI convention is `wait`
(`kubectl wait --for=…`, `docker wait`). `await` is retained as a hidden alias.

Not CI-only. The verb takes a **target kind**, so the same contract extends to
the long-running operations WB already tracks:

```text
wb wait pr        <repo#number...>     pull-request state
wb wait operation <operation-id...>    durable daemon operations
wb wait run       <run-id...>          agent runs
```

`wb ci wait` keeps its exact current semantics and stays the authoritative
merge-evidence path; `wb wait` is the agent-facing waiting verb and must not
be used as merge evidence.

### Bounded and resumable, never detached

Each invocation waits one bounded slice and terminates. Pending exits non-zero
with exact resume arguments, exactly as `wb ci wait` does today. The harness
runs it as a background command; on exit the harness wakes the agent. No daemon
is required, no process outlives its slice, and a lost wake costs one re-invocation.

### Multi-target, first-change semantics

One waiter for N targets. It returns when any target reaches a reportable state,
reporting every change observed in that same check — so a burst of four PRs
turning green is one wake, not four. This is the cheapest available form of the
coalescing the prompt asks for, and it needs no event bus.

## Non-goals

- No general workflow or rules engine.
- No AI summarisation on the correctness path.
- No claim of exactly-once delivery.
- No enrichment of the shared App event contract with repository content.
- No autonomous merge that the agent did not pre-authorise with review evidence.
- `wb wait` is not merge evidence; `wb ci wait` remains that.

## Open Questions

- Should the pre-authorised "complete it without waking me" path
  (`--then land --approved-by <file>`) ship as part of this, or as a separate
  idea once `wb wait` has real usage? It carries the most value and the most
  risk.
- Does the durable `wb watch` layer earn its complexity once `wb wait` exists,
  given that a bounded waiter plus harness re-invocation already survives
  everything except the harness itself dying?
