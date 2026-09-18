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

| Capability | Where it already lives | Load-bearing? |
|---|---|---|
| Bounded, authoritative CI observation | `internal/orchestrate/ciwait.go`, `wb ci wait` | yes |
| **Wait for checks, then merge** | **`wb pr land`** | **yes — see below** |
| Multi-PR fleet inventory with mergeability, conflict and checks | `wb fleet prs`, `internal/prinventory` | yes |
| Blocking wait on a dispatched agent run, incl. over SSH | `wb agent await` | yes |
| Durable operation lifecycle: submit/get/wait/cancel | `cmd/wb/daemon_operation.go` | yes |
| Signed webhook ingest, HMAC verification, installation binding | `hub/`, `cmd/wb/daemon_hub_webhook.go` | yes, but see contract limit |
| Repository-lifecycle event queue | `internal/repositoryevents` | **no — two reasons only** |
| Session-move follow-up messaging | `internal/sessionmessenger` | **no — not a wake primitive** |

An earlier draft of this table claimed more than the code supports. Adversarial
review falsified three rows, and the corrections matter more than the original
claim did:

- **`internal/sessionmessenger` cannot wake a session.** It delivers to a live
  tmux successor of a *completed session move*, and only from that successor's
  live predecessor process: `LoadSuccessorAddress` requires a durable completed
  handoff receipt, `validateSource` requires exact identity equality with the
  recorded predecessor, and the receiving side refuses unless the target is live
  with exactly one matching pane. A detached waiter is none of those things. WB
  has **no** mechanism to wake an arbitrary agent session. The only wake this
  idea can rely on is the harness noticing that a background command exited —
  which is outside WB and unverifiable by it.
- **`internal/repositoryevents` cannot carry pull-request or check state.**
  `repositoryevent.Reason` admits exactly `default_branch_updated` and
  `repository_renamed`; anything else is rejected as an unsupported reason, and
  the only shipped processor syncs repositories. Adding a PR trigger is a
  `ContractVersion` change to the shared multi-tenant App — which the privacy
  section below argues against on its own terms.
- **`internal/runqueue` is not an event queue.** It is CPU-lease admission for
  heavy commands. Citing it here was padding.

What survives is still substantial, but it changes the conclusion: the missing
piece is **not** a durable watch programme. It is a small gap around commands
that already exist.

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

That constraint is real but **narrow**, and an earlier draft over-generalised it
into a repository-wide principle. It is not one. `wb agent await` waits an hour
by default and zero means unbounded; `wb daemon operation wait --timeout`
defaults to no limit at all. WB has no objection to long waits.

What `wb ci wait` actually protects is *merge evidence going stale between
observation and use*, which is why it ends with a terminal reread. That applies
to evidence, not to observation. A report-only waiter holds no verdict and can
safely run long.

The nine-minute cap has a second, more practical cause, stated in the code:
`MaxForegroundCheckWaitSlice` "keeps a single agent-tool call under the common
ten-minute harness ceiling". That ceiling binds **foreground** tool calls. A
harness *background* job is not a foreground tool call, so it is not bound by
it — which is precisely the room this idea occupies.

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

This does **not** license autonomous merging, and an earlier draft was wrong to
assert that the existing guards would simply hold. Review established two
concrete holes, **both of which already exist today and are not created by this
idea**:

- The landing lane resolves its owner from the *calling process's* session
  (`landingLaneOwner` → `session.ResolveForProcess`) and `acquireLandingLane` is
  "a deliberate no-op" when that yields an empty session ID. A background
  landing process that has been reparented away from its harness therefore lands
  with no lane at all — the exact enforcement that
  `rule:one-landing-owner-per-target` relies on. Worse, when the walk *does*
  succeed, every waiter spawned by one agent inherits the same session ID, and
  same-session acquisition is a refresh rather than a conflict, so N concurrent
  landings are admitted under one lane.
- `--approved-by` is never validated. It is checked only for non-emptiness and
  is then interpolated into the commit message. It is not bound to a head SHA,
  so a deferred landing can merge a head that arrived after the review was
  written, under that review's name. It also bypasses the mechanical classifier
  outright, making it a blanket token rather than a scoped one.

Neither is a reason to withhold `wb pr land`, which agents already run
interactively and which is unaffected in the foreground. Both are reasons that
a **detached, land-later** waiter must not be built until a head-bound approval
token and a lane identity a non-session process can present exist. They are
filed as their own hardening work, not as a tax on the observation verb.

## Proposed direction

### A single bounded waiting verb

`wb wait` becomes the one verb for "block until a WB-known condition holds",
replacing per-domain waits and ad-hoc `gh` loops.

```sh
wb wait pr sneat-dev/wb#581 --until checks-settled
wb wait pr wb#544 wb#575 ext-contracts#69 calendarius#73 --until changed
```

Naming: `wait`, not `await`. The CLI convention is `wait` (`kubectl wait
--for=…`, `docker wait`).

An earlier draft said `await` was free. It is not: **`wb agent await` already
exists** as a first-class leaf that blocks until a dispatched agent run is
terminal, with `--wait-timeout` defaulting to one hour. So `await` is taken, with
established semantics, and must not be quietly repurposed as an alias for a
different verb.

WB therefore spells waiting **three** ways today — `wb ci wait`,
`wb agent await`, `wb daemon operation wait` — with three different bounding
contracts (9 minutes hard, 1 hour default, unbounded default). There is no
existing "wait convention" to be consistent with; there is a scattering to be
consolidated. Adding a fourth top-level spelling *beside* the other three would
make this worse, which is why the verb-first home below must **absorb** them
rather than sit next to them.

Object naming: the argument after `wait` names a **thing**, not a domain. "CI"
is not a thing one can point at; a pull request, a workflow run, an agent run
and an operation are. `wb ci wait`'s real object is one exact commit's checks,
so its verb-first spelling is `wb wait checks`, not `wb wait ci`.

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
wb wait pr        <owner/repo#n...>       pull-request state      (new)
wb wait checks    --repo --target --head  exact-head check policy (absorbs wb ci wait)
wb wait agent     <agent-id>              dispatched agent runs   (absorbs wb agent await)
wb wait operation <operation-id>          durable operations      (absorbs wb daemon operation wait)
```

Deliberately **not** a target kind: a local command. `wb wait run --
./script.sh` would duplicate `wb run -- <command>`, which already exists, and
there is nothing to delegate — waiting on a process you just launched in the
foreground is what a shell does. The kinds that earn a verb are the ones whose
process the caller does **not** own: remote checks, a dispatched agent, a daemon
operation.

`wb ci wait` keeps its exact current semantics and stays the authoritative
merge-evidence path; `wb wait` is the agent-facing waiting verb and must not
be used as merge evidence.

### Bounded and resumable, never detached

Each invocation waits one bounded slice and terminates. Pending exits non-zero
with exact resume arguments, exactly as `wb ci wait` does today. The harness
runs it as a background command; on exit the harness wakes the agent. No daemon
is required, no process outlives its slice, and a lost wake costs one re-invocation.

### Multi-target, all-terminal semantics

One waiter for N targets, returning when **every** target is terminal or the
slice ends, reporting all of their states together.

An earlier draft proposed returning on the first change. Review showed that is a
starvation machine, not coalescing: targets finish minutes apart, so N targets
produce N wakes, each re-invocation restarting observation of the remainder from
cold — and a noisy target under `--until changed` returns every poll, so the
quiet target that actually needed attention is never reached.

**The GitHub API budget is the real cost here and the earlier draft never priced
it.** Each observation of one PR costs roughly 4-6 REST calls: check runs,
commit statuses, `actions/runs?head_sha=`, PR base verification, plus branch
protection and branch rules on a cache miss. Seven targets polled every 30s is
on the order of 800-1000 calls per hour against a 5000/hour authenticated
budget, replacing an ad-hoc loop that cost one call per minute per PR.
`DefaultCheckPollInterval` exists precisely to leave "room for other WB
operations sharing the authenticated GitHub user budget". A waiting verb that
multiplies that budget by five to save model turns has moved the cost, not
removed it. Default poll intervals must be chosen against the API budget, not
against responsiveness.

## Non-goals

- No general workflow or rules engine.
- No AI summarisation on the correctness path.
- No claim of exactly-once delivery.
- No enrichment of the shared App event contract with repository content.
- No autonomous merge that the agent did not pre-authorise with review evidence.
- `wb wait` is not merge evidence; `wb ci wait` remains that.

### `wb pr land` already is the wait-then-merge verb

`wb pr land` chains bounded resumable check-wait slices and then merges. It is
not a watch programme away from the motivating scenario; it *is* the
deterministic half of it, shipped. Its sequence is one concept — complete this
pull request atomically — and contains no dependency propagation:

```text
inspect_pull_request -> inspect_changed_files -> candidate_checks ->
preflight_cleanup -> inspect_source_commits -> merge_pull_request ->
verify_remote_landing -> sync_canonical -> delete_remote_branch -> cleanup
```

That tail is not scope creep; `rule:land-work-dont-queue-it` defines work as
done only when merged, pushed, and every branch and worktree cleaned.

#### A stale comment, and the claim it cost

The code justifies keeping a lane heartbeat alive with:

> This wait can run the full slice budget (routinely 30-60 minutes for this
> fleet) in one call

**Measurement contradicts it.** Observed successful workflow durations,
wall-clock from creation to completion including queue time:

| Repository | PR runs | Post-merge runs |
|---|---|---|
| `sneat-dev/wb` | 8 min | 2-3 min |
| `sneat-co/sneat-go` | 3-11 min | — |
| `sneat-co/backstage` | 0 min | — |

Nothing in the fleet approaches thirty minutes, let alone sixty. An earlier
draft of this idea read that comment, inferred that `wb pr land`'s eight-minute
`--timeout` default was four to seven times too small, and built its headline
finding on it. That was wrong, and it is recorded here because the failure mode
is worth keeping: **a comment is not a measurement**, and this one had been
carried forward long enough to look authoritative.

#### What is actually left

The default is set at roughly the *median* CI duration rather than above it, so
a landing started right after a push times out about as often as it succeeds —
and the `checks-pending` refusal then returns

```text
SanctionedCommand = "wb pr land <repository>#<number>"
```

with no `--timeout`, so the sanctioned retry carries the same budget that just
expired. That is a real but modest defect: a default that should sit above the
observed distribution rather than in the middle of it, and a resume hint that
should name the larger budget. It is a tuning fix and a message fix, not a
feature.

The measured adoption gap therefore rests on the ergonomics finding above —
`wb ci wait` demanding a head SHA the caller does not have — and on discovery
across three scattered spellings. Those are what the new verb addresses.

That narrows what is genuinely missing to three things:

1. **Observation without authorising a merge.** `wb pr land` lands. There is no
   way to ask "tell me when this changes" without granting permission to merge.
   This is the one real gap.
2. **More than one target per process.** One landing call watches one PR;
   `wb fleet prs` snapshots many but does not wait.
3. **A reference an agent already has.** See the measurement above.

Conditions beyond checks — review submitted, changes requested, a comment, a
conflict appearing — are worth having but are not what the motivating scenario
needed, and should not delay the first three.

### A waiter nobody can see is the same as no waiter

This reverses part of the argument above and is the strongest case for durable
state in the whole idea.

The case against persistence was made purely on **wake reliability**: a bounded
waiter plus harness re-invocation survives everything except the harness dying,
so a durable watch buys little. That reasoning missed **legibility**.

When an agent correctly delegates its waiting and goes quiet, the session
becomes indistinguishable from one that has crashed, hung, or simply stopped.
The founder put it exactly:

> a session will be looked like stopped without active waiters that are supposed
> to wake session up once event happens

Nothing in WB records that a wait is outstanding. `wb session list` reports
`live`, `gone` or parked — a session idle *because it is waiting* looks the same
as one idle because it gave up. The only recourse available to a watching human
is to interrupt, which destroys the quiet the feature exists to create.

#### What the harness does and does not show

Verified against Claude Code's current behaviour, because the answer decides how
much WB has to carry:

| Surface | Exists | Limit |
|---|---|---|
| `Ctrl+B` | yes — interactive background-task view | in tmux the first press is swallowed; press twice |
| `/tasks` | yes — lists background tasks and subagents | must be typed; nothing prompts it |
| Persistent indicator | **no** | a session with live waiters looks identical to an idle one |
| Completion | terminal notification | the session does not visibly re-engage on its own |
| Custom `statusLine` | **cannot close this** | its payload carries no background-task state, and it refreshes only on events — during an idle wait nothing fires, so any "waiting" text goes stale immediately |

Two consequences for WB.

First, the harness surfaces work only **inside** the session that owns the
waiter. A second agent, a second terminal, or a human on another machine sees
nothing. WB's records work from anywhere, which is why they are worth keeping
even though `Ctrl+B` exists.

Second, a session that *dies* holding a wait is invisible to the harness — there
is no session left to press `Ctrl+B` in. A stale record is the only remaining
evidence that something was supposed to be watched and no longer is.

So delegated waiting has a precondition the first draft never stated:

> **A delegated wait MUST be observable by someone other than the process doing
> it.** A wait that exists only as a background process in one harness's memory
> has moved the "did anyone remember this?" problem rather than solved it.

This does not require a daemon, webhooks, or an event bus. It requires the
waiter to record what it is waiting for, for whom, since when, and the exact
command that resumes it — and for something to list those records. The record
is small, local, and removed when the wait ends; a record whose process is gone
is prunable evidence that a wait died, which is itself the thing worth knowing.

Note what this justifies and what it does not. It justifies durable *state about
waits*. It does not resurrect the durable watch programme: WB still cannot wake
an arbitrary session, and the App event contract still cannot carry pull-request
state. Observability is a much cheaper requirement than delivery.

### What this still does not do

Both surfaces require someone to look. Nothing pushes an outstanding wait into
view, and per the refresh model above a status line cannot sustain one during
idle. Closing that needs a push channel outside WB — a notification hook, or a
periodic prompt — and is deliberately out of scope here: WB's job is to make the
state true and queryable, not to own the operator's attention.

## Open Questions

- Should a wait record be attributed to the WB session, the effort, or both?
  Session is what a human asks about ("what is this session doing?"); effort is
  what survives the session.
- Does a durable `wb watch` layer earn its complexity *beyond* the observability
  requirement above, now that the audit shows WB cannot wake an arbitrary
  session and the App event contract cannot carry pull-request state?

- Should `wb wait pr` take a per-`(repository, number)` advisory lock so a
  second waiter for the same target attaches or refuses instead of duplicating?
  The idea's own triggering observation was one agent launching two pollers for
  one PR, and nothing proposed here prevents that. WB already has the pattern in
  `internal/landinglane` and `internal/runqueue`.
- What poll interval survives contact with the GitHub API budget for a realistic
  seven-target wait? See the budget arithmetic above; this needs measuring, not
  choosing.

## Superseded in part

The finding that WB cannot initiate contact with a session was too strong. It
was drawn from `internal/sessionmessenger`, which is genuinely only a
predecessor-to-successor handoff channel, plus a documentation review that
missed two push routes. Claude Code **channels** were tested and do not wake an
idle session; **`herdr agent prompt`** does, and herdr-hosted sessions can be
woken that way, with the injected text rendered as the user's own input.

So the conclusion "for unattended work, acting beats notifying" still holds for
a session that is **gone**, and no longer holds for one that is **alive**. The
successor idea is [[daemon-as-coordinator]], which also records why that route
must never carry provider text: a message delivered as the founder, built from
a pull request title an attacker wrote, is prompt injection with the founder's
authority.

## Follow-up work this idea identified but does not do

- `wb pr land`'s `--timeout` default sits at the median observed CI duration
  rather than above it, and its `checks-pending` refusal returns a resume
  command with no `--timeout`, so the sanctioned retry carries the budget that
  just expired.
- `internal/orchestrate/pr_land.go` claims check waits run "routinely 30-60
  minutes for this fleet". Measured durations are 8 minutes for `wb`, 3-11 for
  `sneat-go`, 0 for `backstage`. The comment should be corrected or dated.
- The landing lane no-ops for a process with no resolvable session, and treats
  same-session re-acquisition as a refresh.
- `--approved-by` accepts any non-empty string and is not bound to a head SHA.
