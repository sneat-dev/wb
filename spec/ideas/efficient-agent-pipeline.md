---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Efficient agent pipeline: move the whole lifecycle out of the orchestrator's transcript

**Status:** Draft
**Date:** 2026-09-17
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:agent-lane-verbs, extends:mechanical-worktree-merge, extends:graph-assisted-fleet-optimization, depends_on:mutation-requires-an-isolated-worktree

## Problem Statement

How might we make the cost of an agent-run software pipeline scale with the work done, rather than with the number of turns the orchestrator takes to supervise it?

## Context

### The triggering observation

On 2026-09-16 three orchestrator sessions burned 46% of a weekly Claude budget
in under 24 hours. Each ran 800–900 turns at $79–$294, measured from Claude
Code's own `cost-state` accounting rather than estimated. No single call was
expensive. The sessions were expensive because they took a great many turns.

### The cost law this repository should design against

An earlier draft of this idea asserted that cost grows with the **square** of
turn count. Adversarial review falsified that, and the correction matters
because the lever ordering was derived from it.

The mechanism is real: each turn re-reads the prefix accumulated so far, at the
cache-read rate. The error was assuming the prefix grows without bound. It does
not — harnesses compact, so the prefix saturates at a ceiling `W`, and above that
ceiling cost is **linear**:

```text
cost ≈ read_rate × Σⱼ bⱼ·(N−j)      below the compaction ceiling
cost ≈ read_rate × N × W̄            above it
```

The quadratic term is real only in the first stretch of a session, before
compaction binds. Past that, every additional turn costs about the same.

The triggering sessions are the evidence *against* the quadratic reading, not
for it: three sessions at 800–900 turns cost **$79 to $294**, a 3.7× spread.
N² can account for at most 1.27× of that, so turn count explains well under a
third of the variation. The linear model, with prefix size as the multiplier,
accounts for the whole band.

Three consequences, all different from the earlier draft:

- **Halving turns halves cost.** Not quarters it. Still worth doing, worth less
  than claimed.
- **Prefix size `W̄` is a first-class multiplier.** Trimming what each turn
  carries — verbose command output, oversized loaded skills — is *not* worthless,
  as the earlier draft said. It scales the whole session linearly.
- **Splitting a session into k parts does not divide cost by k.** It helps only
  while the parts stay below the compaction ceiling, and it adds the cost of
  re-establishing context. Taken to its limit the earlier claim drives cost to
  zero, which is its own refutation.

### The first lever is `W̄` itself, and it is a setting

The cost law above treats `W̄` as a property of the harness. It is not — it is a
number you choose, and choosing it badly is the single largest line on the bill.

Measured across 24 session transcripts on this machine: **mean context per turn
is ~500K tokens, and cache reads are 80–85% of weighted cost in every expensive
session.** The cause is documented, not mysterious. On a model with a 1M context
window the default auto-compact threshold is about **967K tokens**, so a session
of several thousand turns compacts two or three times in total. The prefix
spends most of its life near the ceiling, and every turn pays to re-read it.

The threshold is directly configurable — `autoCompactWindow` in settings, or
`CLAUDE_CODE_AUTO_COMPACT_WINDOW` in the environment. It is capped at the
model's real context window, so it binds on 1M-window models and silently does
nothing on a 200K one. Sonnet 5 runs at 1M unconditionally on the direct API, so
this reaches the long Sonnet implementation sessions, which are the most
expensive ones measured, not only the Opus ones.

Set to 400K on 2026-09-18. That drops mean context per turn from ~500K to
roughly 220K and takes about half off the dominant term, for no code.

This supersedes an explicit rejection in an earlier draft, which is recorded
below under Alternatives Considered. The rejection reasoned that compaction
"attacks prefix size, not turn count". That was true and beside the point: when
the prefix is a 500K multiplier on every one of several thousand turns, prefix
size *is* the bill. The second half of the rejection — that compaction is lossy
where judgement lives — is a real cost, but it is smaller here than it looks,
because the state that must survive is already on disk by construction:
worktrees, WB manifests and work logs, SpecScore artifacts. Keeping that state
durable and re-readable is what the rest of this strategy is for.

It is not free, and the honest failure mode is that an over-tight window makes
the orchestrator re-read files it has already read, converting a cache read into
a fresh read plus an output. That is why the window was set to 400K rather than
the 200K that would maximise the arithmetic saving, and why the next step is
measurement — `/context` in a long session, and the cache-read line in usage —
before tightening further.

One mechanism the earlier draft added and this one removes: cache-expiry misses.
A turn that blocks longer than the cache TTL would pay a cache *write* rather
than a read. But the harness in use here runs a **1-hour** TTL, not the 5-minute
default, so ordinary CI waits sit comfortably inside it. Keep-alive polling to
hold a cache warm is waste, and the harness documentation says so directly.

What replaces it is simpler and verifiable: **never let a turn be pure waiting.**
Background a long command and do other work; the harness reports completion
without a turn spent watching.

### What is already true here

This repository has been attacking the problem one leak at a time, and one of
those attacks has already shipped and proved the thesis:

| Existing artifact | Status | The leak it closes |
|---|---|---|
| [mechanical-worktree-merge](mechanical-worktree-merge.md) | Implemented | Mechanical Git/GitHub operations, collapsed into one verb |
| [agent-lane-verbs](agent-lane-verbs.md) | Draft | Repository facts re-derived once per session |
| [graph-assisted-fleet-optimization](graph-assisted-fleet-optimization.md) | Draft | Ad-hoc, unauditable test selection |
| [secret-vault-injection](secret-vault-injection.md) | Draft | Secret values landing in an agent's context |
| [mutation-requires-an-isolated-worktree](mutation-requires-an-isolated-worktree.md) | Draft | Worker lifecycle run turn-by-turn by the orchestrator |

`mechanical-worktree-merge` asked how to land changes "without spending AI
tokens on mechanical Git and GitHub operations", shipped, and `wb pr land` is now
used 24–35 times per orchestrator session precisely because one verb replaced a
hand-run sequence. The pattern works. What is missing is not another instance of
it — it is the statement of the pattern, so the remaining instances get built in
the right order instead of opportunistically.

### The other half already exists, in SpecScore

WB has no review concept and should not grow one. SpecScore already owns that
half of the pipeline, deterministically:

| SpecScore surface | What it decides |
|---|---|
| `specscore rehearse run` | Did the change satisfy its acceptance criteria, with recorded evidence |
| `specscore consilium verdict` | What an expert panel's votes reduce to, reproducibly, under a named gate config |
| `specscore issue` | Findings, as durable artifacts rather than prose in a transcript |
| `specscore spec` / `rule` / `lesson` | Spec coherence, normative rules, recorded process gaps |

Both verdict engines are **deterministic**: same inputs, same verdict. That is
what makes them safe to put in an automated gate, and it is why the gate belongs
to SpecScore rather than to a model's opinion at the end of a worker run.

## Recommended Direction

**The orchestrator should spend turns on judgement, and never on mechanism.**
Every mechanical step it runs itself costs not only its own turn but a share of
every turn that follows it.

Four levers implement that, ordered here by multiplier times cheapness to adopt.
They are complementary, not alternatives.

1. **Collapse** — a mechanical multi-call sequence becomes one deterministic WB
   verb. Already the house style (`rule:wb-principles-speed-and-load`), already
   proven by `wb pr land`.
2. **Relocate** — work that genuinely needs many turns runs in a disposable
   transcript, not in the orchestrator's growing one. A harness-native subagent
   already does this, and is the default; `wb agent dispatch` earns its keep only
   where isolation, CPU admission, model routing or cross-machine execution are
   the point. Either way the delegate's turns are paid once and never replayed.
3. **Certify** — the orchestrator confirms an outcome by reading a bounded
   receipt of facts WB observed, never by re-deriving those facts from raw
   evidence. Reading a diff, a log, or a CI page to decide "did this go well"
   costs many turns and imports unbounded text.
4. **Don't block** — never spend a turn waiting. Background long commands and
   let completion be reported. This replaces the earlier "Reset" lever (park to
   restart the quadratic), which the corrected cost law largely deflates: above
   the compaction ceiling, splitting a session buys far less than it appeared to
   and costs a handoff.

Governing all four is one standing constraint: **what crosses into the
orchestrator must be decision-relevant and bounded.** Transcripts, raw logs and
full diffs never cross. Verdicts, receipts, counts and paths do. This is the same
distinction `rule:lane-reports-are-claims-not-receipts` draws, applied to tooling
rather than to prose.

### Division of labour: WB runs it, SpecScore judges it

The pipeline needs two different competences, and they must not be merged into
one tool. WB's own help already draws the line: it "never judges whether the
resulting diff is correct — that is the caller's decision". Keep that.

| | WB | SpecScore |
|---|---|---|
| Owns | Mechanism — isolation, worktrees, dispatch, governed commands, hooks, landing, CI waiting, claims, cleanup, **cross-repo dependency topology and propagation order** | Meaning — acceptance criteria, verdicts, findings, rules, spec coherence |
| Records | What it observed happen | What it decided, and why |
| Knows about the other | Nothing | Nothing |

**WB must not grow a review concept.** It should grow a *phase* concept, which is
semantics-free: a phase is a dispatched agent run or a governed command, and what
WB records is its exit status, duration and artifact path. WB never interprets
what the phase meant. That keeps WB a standalone efficiency multiplier: with no
SpecScore in the project at all, phases are still useful (build, tests, lint), and
the receipt is still worth reading.

The seam between them is deliberately thin, but it is **not** semantics-free, and
an earlier draft overclaimed that it was. WB needs exactly one semantic bit per
phase — `required: bool` — plus a stated convention: **a phase's exit code is the
gate decision**. Severity policy, waivers and flake handling live in whatever
produces that exit code, not in WB. With that one bit named, the seam is exit
codes, artifact paths and a required flag.
A review phase is just `specscore consilium verdict …` or `specscore rehearse
run …` run as a phase. WB sees a command that exited 0 and wrote a file. SpecScore
sees its own artifacts. Neither imports the other, and either can be swapped.

### The target pipeline

```text
orchestrator:  write brief ──► dispatch ─────────────────────► read receipt ──► decide
                                  │                                 ▲
WB (no orchestrator turns):       ├─ create isolated worktree       │
                                  ├─ run worker to terminal state   │
                                  ├─ run phases, gate on exit code  │
                                  │    └─► specscore rehearse run   │  ← meaning
                                  │    └─► specscore consilium …    │     lives here
                                  ├─ land, or hold for an owner     │
                                  └─ record observed facts ─────────┘
```

Two orchestrator turns per delegated task, whatever happens in between. Today the
same task costs somewhere between fifteen and forty.

Three constraints make that safe rather than merely cheap:

- **The worker never chooses or runs its own reviewer.** A worker that picks its
  reviewer and weighs its own findings is self-certifying. Review phases are
  configured on the dispatch and executed by WB after the worker reaches a
  terminal state, against the diff and the acceptance criteria — never against
  the worker's transcript.
- **No receipt field may be populated from the worker's own claim.** Every field
  comes from something WB observed itself or from a third party (Git, CI,
  SpecScore's own artifact). One self-reported field degrades the whole artifact
  from a receipt to a claim, and makes a confidently wrong `PASS` available again.
  This is `rule:lane-reports-are-claims-not-receipts` enforced by construction.
- **WB decides who lands — but not yet from `wb deps`.** An earlier draft
  claimed consumer ordering was derivable from the cross-repo dependency graph.
  It is not: `wb deps graph` inspects manifests on `origin/<base>` (defaulting to
  `main`), so it is blind to unlanded work by construction, and
  `RepositoryOrder` derives provider-first layering from requirement evidence —
  *who depends on whom*, never *who has work in flight*. It also sees only
  locally checked-out repos, one ecosystem at a time, and no coupling that is by
  API contract rather than manifest.

  The predicate actually needed — "does any consumer have unlanded work that must
  follow this repo?" — is stream and worktree state (`wb stream status`, worktree
  manifests), not dependency topology. Until that is wired, **worker-side
  auto-landing must fail closed**: land automatically only for a single repo with
  no active stream, a clean merge, green CI and green required phases, and hold
  for a landing owner in every other case. The failure mode of getting this wrong
  is silent and damaging, which is the one kind of error this whole direction
  exists to remove.

## Alternatives Considered

- **Wait for bigger context windows or cheaper orchestrator models.** Rejected,
  and a bigger window is worse than neutral: the default compaction threshold
  tracks the window, so a larger window raises `W̄` and every turn pays more to
  re-read it. A window is only a benefit once its compaction threshold is set
  deliberately rather than inherited. A cheaper orchestrator is separately
  wrong — brief quality is the main determinant of worker retries, and retries
  are the expensive failure mode.
- **Compact or summarise the orchestrator's context aggressively.** *Rejected in
  an earlier draft, and that rejection was wrong.* It read: "it attacks prefix
  size, not turn count, and it is lossy exactly where judgement lives. It is a
  useful backstop and already happens implicitly; it is not a strategy." The
  first clause is true and irrelevant — prefix size is 80–85% of the bill — and
  "already happens implicitly" was the actual error: it happens at a default
  threshold of ~967K, which is the problem, not the mitigation. Capping the
  compaction window is now the first lever, above.
- **Cap orchestrator turns inside WB.** Rejected on its own: a cap without
  somewhere to put the work truncates an effort mid-flight, and
  [agent-lane-verbs](agent-lane-verbs.md) records that stopping after
  implementing and before committing is the most expensive place to stop. A cap
  is safe only once Relocate and Certify give the work another home — i.e. as a
  consequence of this direction, not a substitute for it.
- **Stream worker output live into the orchestrator for visibility.** Rejected:
  it reintroduces precisely the replay cost being removed. Live watching is a
  human concern served by a separate channel, and costs the orchestrator nothing
  when it stays there.
- **Build review, acceptance criteria and verdicts into WB.** Rejected: it
  duplicates `specscore rehearse` and `specscore consilium`, it forces WB to hold
  opinions about correctness that its own charter disclaims, and it couples a
  standalone execution tool to a spec system that many projects using WB will not
  have. A semantics-free phase concept gets the same pipeline with neither cost.
- **Drive the pipeline from SpecScore instead, calling WB.** Rejected in this
  direction: execution, isolation and admission are WB's, and inverting the
  dependency would make the cheap standalone case (run a worker, get a receipt,
  no spec tree) impossible.

## MVP Scope

Ship **Certify** first, in WB, and semantics-free: a phase list on `wb agent
dispatch`, and a per-run conformance receipt returning a bounded set
of facts WB observed about that run — isolation, base SHA, hooks, each phase's
command, exit status and artifact path, landing route, CI verdict, cleanup. Each
a yes/no plus an evidence pointer, none of them sourced from the worker.

The first consumer is a review phase running `specscore rehearse run` against the
feature's acceptance criteria, but WB ships knowing nothing about that: it records
a command that exited 0 and wrote an evidence file. A project with no SpecScore
gets the same receipt with `go test` in the phase list.

Extend the existing surface rather than inventing one. `wb verify receipt`
already exists to "compose exact verification, remote, deployment, and cleanup
evidence", and `internal/runlog` already writes a durable event per governed
command — `duration_ms`, `user_cpu_ms`, `queue_wait_ms`, `admitted_at`,
`exit_code` — to `<root>/.wb/local/run/events.jsonl`. The receipt is largely a
join over records WB already keeps — with one gap that must be closed first:
`runlog` resolves its store per managed worktree (`internal/runlog/runlog.go:310`),
records nothing outside one, and is deleted with the worktree. A receipt cannot
join over records the cleanup step it attests has already destroyed, so store
export or relocation is part of this MVP, not an afterthought.

That telemetry is also the strategy's own first evidence of the discovery
problem: fleet-wide it holds **24 events, of which 2 are `go/test`**, because
almost nothing is routed through `wb run --`. The instrument exists and is
unused, which is why test cost could be suspected but not measured.

Certify comes before Relocate and before worker-side landing because it is what
makes both verifiable, and because it pays for itself alone: even with today's
manual pipeline it replaces a multi-turn re-derivation (`git diff`, `status
--short`, re-run tests, poll CI) with one bounded read. Building delegation first
and verification second would mean trusting exactly the claims this strategy
exists to stop trusting.

## Not Doing (and Why)

- Adding a `--follow` live-tail mode to `wb agent dispatch` — dispatch is detached by contract, the worker's `events.jsonl` is already append-only and tailable locally, and if a live mode is ever justified it belongs additively on `wb agent logs`, attachable to any run at any time
- Judging whether a worker's diff is correct inside WB — WB reports observed process facts; correctness stays SpecScore's or the caller's decision
- Teaching WB what a review, an acceptance criterion or a verdict is — phases carry exit codes and artifact paths, nothing more, so WB stays useful with no spec tree present
- Making WB depend on SpecScore, or SpecScore depend on WB — either coupling costs the standalone case on both sides
- Renaming existing `--format json` fields to unify them — a machine-readable contract; any convergence must be additive, and belongs in its own task rather than bundled here

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Capping the compaction window lowers total cost without a matching rise in re-read work — the orchestrator does not simply re-fetch what it compacted away | Compare mean context per turn and cache-read tokens before and after the 400K cap over comparable sessions, and count turns that re-read a file already read earlier in the same session |
| Must-be-true | Turn count and payload size both matter, and turn removal pays off linearly rather than super-linearly | Regress recorded `cost-state` totals against turn count and against mean context per turn; a linear-in-turns model with prefix size as multiplier should fit, and the quadratic term should not be needed |
| Must-be-true | A bounded receipt of WB-observed facts is sufficient for an orchestrator to accept or reject a delegated task without reading the diff or transcript | Run delegated tasks to completion on receipt alone, and record every case where the orchestrator had to open raw evidence anyway, and why |
| Should-be-true | Total fleet cost falls, rather than moving from the orchestrator to workers and reviewers | Compare end-to-end cost per landed PR, counting worker, reviewer and retry runs, before and after — not orchestrator cost alone |
| Should-be-true | Machine-checkable preconditions, including consumer ordering read from `wb deps`, can decide worker-side landing safely with no unsupervised conflict resolution on a shared target | Count holds versus auto-lands over a sample; confirm no auto-land involved a non-trivial merge, and no provider auto-landed ahead of an unlanded consumer the graph knew about |
| Should-be-true | A semantics-free phase concept is expressive enough for a SpecScore-backed review gate, with no review knowledge added to WB | Implement the review gate purely as phases running `specscore rehearse run` / `consilium verdict`, and check whether any WB change was needed to interpret them |
| Might-be-true | Skill routing changes shift orchestrator behaviour without any CLI change | Re-run the session-log analysis a few weeks after the routing fix and compare the manual-lifecycle versus dispatch call ratio |

## SpecScore Integration

- **New Features this would create:** a phase list (with `required`) on `wb agent dispatch`; a conformance receipt extending the existing `wb verify receipt`; fail-closed, precondition-driven landing ownership
- **Existing Features affected:** `wb agent dispatch`/`await`/`status`/`logs`, `wb pr land`, `wb worktree land`, `wb deps`, the WB skill routing surface
- **Dependencies:** [mutation-requires-an-isolated-worktree](mutation-requires-an-isolated-worktree.md) for the routing fix; [agent-lane-verbs](agent-lane-verbs.md) shares the Collapse lever. No code dependency on SpecScore — the review gate composes through phase exit codes and artifact paths only

## Open Questions

- This strategy spans WB and SpecScore, but lives in WB's spec tree. Should it
  move to `backstage/spec/ideas` as a cross-product idea, with WB keeping only the
  phase-and-receipt feature?
- Does a held (not auto-landed) run belong in a queue WB can hand to a landing
  owner on request, or should it simply surface as a state the orchestrator polls?
- Should a failing review phase block the land outright, or only above a severity
  named in the gate config — and if the latter, does WB read that severity (which
  would give it review semantics) or does the phase itself exit non-zero only when
  the gate says so?
- Is a phase list on `wb agent dispatch` the right home, or should phases be a
  project-level pipeline declared in `wb.yaml` that any dispatch inherits?
