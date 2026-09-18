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

### Measured: the existing wait verb is real, and agents route around it

`wb ci wait` is not missing. It is unused relative to the hand-rolled
alternative. Counted across 857 Claude session transcripts on this machine:

| What agents actually ran | Occurrences | Distinct sessions |
|---|---|---|
| `wb ci wait` | 366 | 46 |
| Hand-rolled `gh` polling loops | 400 | 50 |
| `gh run watch` | 391 | — |
| Any `sleep N; done` loop | 1384 | — |

Ad-hoc waiting outnumbers the WB verb by more than two to one and reaches more
sessions. (These are text matches in transcripts, so an occurrence includes a
command quoted in a plan rather than executed. Both sides are counted the same
way, so the ratio is the signal, not the absolute counts.)

The cause is in the signature, not in agent discipline:

```text
--target  required target branch containing the exact direct-push head, or the PR base
--head    required exact 40- or 64-hex Git head SHA
--pr      optional pull request number or URL to corroborate before waiting
```

Waiting on a pull request requires already knowing the repository, the base
branch and the **exact head SHA**; `--pr` only corroborates and cannot stand
alone. So "tell me when #581 is done" costs one or two `gh` calls before
`wb ci wait` may be called at all, whereas `gh pr view 581 --json
mergeStateStatus` in a loop costs none. Agents took the cheaper path, and were
right to.

Two contributing factors compound it:

- The nine-minute cap exists to keep "a single agent-tool call under the common
  ten-minute harness ceiling", so the command cannot be fire-and-forget; it
  returns pending and must be re-invoked.
- It lives under `wb ci`, whose skill is described as auditing CI/CD *policy*.
  Waiting does not sound like it belongs there.

This is the decisive design input. A waiting verb an agent will actually use
must accept the reference the agent already has — `owner/repo#number` — and
resolve target and head itself. And the nine-minute ceiling is precisely the
gap a background command fills: a harness background job is not a foreground
tool call, so it is not bound by that ceiling. The new verb therefore extends
the existing stance rather than overriding it.

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

Word order is **verb first**: `wb wait <kind>`, not `wb <kind> wait`. The thing
being waited for is the argument; waiting is the act. Verb-first also gives the
capability one discoverable home — `wb wait --help` enumerates everything WB can
wait for, which no amount of `wb ci wait` / `wb daemon operation wait` ever
will, because nothing lists them together. The two existing spellings become the
older form of `wb wait ci` and `wb wait operation`; they keep working, and the
help names the verb-first spelling as current. This is a surface migration, not
a behaviour change: `wb wait ci` must reach the identical implementation, so
merge evidence produced either way is the same evidence.

Not CI-only. The verb takes a **target kind**, so the same contract extends to
the long-running operations WB already tracks:

```text
wb wait pr        <repo#number...>     pull-request state          (new)
wb wait ci        --repo --target --head   exact-head check policy (existing wb ci wait)
wb wait operation <operation-id...>    durable daemon operations   (existing wb daemon operation wait)
wb wait run       <run-id...>          agent runs                  (later)
wb wait test      <...>                long-running local suites   (later)
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

### Already close: `wb pr land` waits and then lands

`wb pr land --timeout` already "uses bounded resumable CI observation slices
internally" and lands when checks pass, so the motivating scenario is nearer to
solved than it looked. Its default budget is eight minutes and each internal
slice is capped at nine, but the total budget is the caller's.

That narrows what is genuinely missing to four things:

1. **Observation without action.** `wb pr land` lands. There is no way to ask
   "tell me when this changes" without authorising a merge.
2. **More than one target per process.** One landing call watches one PR.
3. **Conditions other than checks.** Review submitted, changes requested, a new
   comment, a conflict appearing — none are waitable today.
4. **A reference an agent already has.** See the measurement above.

## Open Questions

- Should the pre-authorised "complete it without waking me" path
  (`--then land --approved-by <file>`) ship as part of this, or as a separate
  idea once `wb wait` has real usage? It carries the most value and the most
  risk.
- Does the durable `wb watch` layer earn its complexity once `wb wait` exists,
  given that a bounded waiter plus harness re-invocation already survives
  everything except the harness itself dying?
