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
**Related Ideas:** extends:agent-lane-verbs, extends:mechanical-worktree-merge, extends:graph-assisted-fleet-optimization, depends_on:orchestrator-default-to-dispatch

## Problem Statement

How might we make the cost of an agent-run software pipeline scale with the work done, rather than with the number of turns the orchestrator takes to supervise it?

## Context

### The triggering observation

On 2026-09-16 three orchestrator sessions burned 46% of a weekly Claude budget
in under 24 hours. Each ran 800–900 turns at $79–$294, measured from Claude
Code's own `cost-state` accounting rather than estimated. No single call was
expensive. The sessions were expensive because they took a great many turns.

### The cost law this repository should design against

With prompt caching, the dominant term in a session's cost is not what a turn
carries — it is that a turn happens at all. Each turn re-reads the whole prefix
accumulated so far, so if the prefix grows roughly linearly in turns, total cost
grows with the **square** of turn count:

```text
cost ≈ Σ(prefix_i × cache_read_rate) ≈ rate × growth × N² / 2
```

Two consequences follow, and they should drive design decisions here:

- **Halving turns quarters cost.** A 30% reduction in turns is a ~51% reduction
  in spend. Turn-count reductions compound in a way byte-count reductions do not.
- **Splitting one N-turn session into k sessions divides cost by k.** Three
  300-turn sessions cost about a third of one 900-turn session for identical
  work.

The triggering sessions confirm the mechanism directly. Their total Bash
tool-result payload was only 250–411 KB each — about 110 tokens of output per
turn across 900 turns. Almost none of the cost was the output. **The unit of
cost is the turn, not the byte.**

This re-orders the obvious optimisations. Trimming verbose command output is
close to worthless. Removing a turn is worth a multiple of its own size, because
every later turn stops paying for it too.

### What is already true here

This repository has been attacking the problem one leak at a time, and one of
those attacks has already shipped and proved the thesis:

| Existing artifact | Status | The leak it closes |
|---|---|---|
| [mechanical-worktree-merge](mechanical-worktree-merge.md) | Implemented | Mechanical Git/GitHub operations, collapsed into one verb |
| [agent-lane-verbs](agent-lane-verbs.md) | Draft | Repository facts re-derived once per session |
| [graph-assisted-fleet-optimization](graph-assisted-fleet-optimization.md) | Draft | Ad-hoc, unauditable test selection |
| [secret-vault-injection](secret-vault-injection.md) | Draft | Secret values landing in an agent's context |
| [orchestrator-default-to-dispatch](orchestrator-default-to-dispatch.md) | Draft | Worker lifecycle run turn-by-turn by the orchestrator |

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
   transcript, not in the orchestrator's growing one. This is `wb agent
   dispatch`. The worker's turns cost what they cost once; they are never
   replayed.
3. **Certify** — the orchestrator confirms an outcome by reading a bounded
   receipt of facts WB observed, never by re-deriving those facts from raw
   evidence. Reading a diff, a log, or a CI page to decide "did this go well"
   costs many turns and imports unbounded text.
4. **Reset** — bound turns per session. Park and hand off rather than letting one
   session reach 900 turns, so the quadratic restarts from a small prefix.

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

The seam between them is deliberately thin — **exit codes and artifact paths**.
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
- **WB decides who lands, and it already holds the facts to decide.** The
  preconditions are machine-checkable: clean merge, CI green, required phases
  green, no competing claim on the target — and, critically, no unlanded consumer
  that must follow this repo. That last one is not a guess: WB already carries the
  cross-repo dependency graph behind `wb deps` and propagates releases in order
  with `wb deps propagate`. Landing order is therefore **derivable from data WB
  owns**, not reasoned about by an agent holding the fleet's topology in its
  context. When the preconditions hold, the worker lands its own work and the
  orchestrator never spends those turns; when they do not, WB holds the run for a
  landing owner, preserving `rule:one-landing-owner-per-target` by construction.

  This is the strongest argument for putting the gate in WB rather than in a
  brief: today an orchestrator coordinating a provider-then-consumers wave keeps
  that ordering alive in its own context across hundreds of turns, and pays for it
  on every one of them. WB can answer the same question from the graph in a single
  call.

## Alternatives Considered

- **Wait for bigger context windows or cheaper orchestrator models.** Rejected as
  a strategy: the cost is quadratic in turns, so a larger window postpones the
  ceiling without changing the curve. A cheaper orchestrator is actively worse —
  brief quality is the main determinant of worker retries, and retries are the
  expensive failure mode.
- **Compact or summarise the orchestrator's context aggressively.** Rejected as
  the primary lever: it attacks prefix size, not turn count, and it is lossy
  exactly where judgement lives. It is a useful backstop and already happens
  implicitly; it is not a strategy.
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
dispatch`, and `wb agent receipt <agent-id> --format json` returning a bounded set
of facts WB observed about that run — isolation, base SHA, hooks, each phase's
command, exit status and artifact path, landing route, CI verdict, cleanup. Each
a yes/no plus an evidence pointer, none of them sourced from the worker.

The first consumer is a review phase running `specscore rehearse run` against the
feature's acceptance criteria, but WB ships knowing nothing about that: it records
a command that exited 0 and wrote an evidence file. A project with no SpecScore
gets the same receipt with `go test` in the phase list.

Certify comes before Relocate and before worker-side landing because it is what
makes both verifiable, and because it pays for itself alone: even with today's
manual pipeline it replaces a multi-turn re-derivation (`git diff`, `status
--short`, re-run tests, poll CI) with one bounded read. Building delegation first
and verification second would mean trusting exactly the claims this strategy
exists to stop trusting.

## Not Doing (and Why)

- Adding a `--follow` live-tail mode to `wb agent dispatch` — dispatch is detached by contract, the worker's `events.jsonl` is already append-only and tailable locally, and if a live mode is ever justified it belongs additively on `wb agent logs`, attachable to any run at any time
- Compaction or summarisation of orchestrator context as the primary lever — it reduces prefix size rather than turn count, and is lossy where judgement matters
- Judging whether a worker's diff is correct inside WB — WB reports observed process facts; correctness stays SpecScore's or the caller's decision
- Teaching WB what a review, an acceptance criterion or a verdict is — phases carry exit codes and artifact paths, nothing more, so WB stays useful with no spec tree present
- Making WB depend on SpecScore, or SpecScore depend on WB — either coupling costs the standalone case on both sides
- Renaming existing `--format json` fields to unify them — a machine-readable contract; any convergence must be additive, and belongs in its own task rather than bundled here

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Turn count, not payload size, dominates orchestrator cost, so removing turns has a super-linear payoff | Regress recorded `cost-state` totals against turn count and against total tool-output bytes across past sessions; the quadratic-in-turns term should dominate |
| Must-be-true | A bounded receipt of WB-observed facts is sufficient for an orchestrator to accept or reject a delegated task without reading the diff or transcript | Run delegated tasks to completion on receipt alone, and record every case where the orchestrator had to open raw evidence anyway, and why |
| Should-be-true | Total fleet cost falls, rather than moving from the orchestrator to workers and reviewers | Compare end-to-end cost per landed PR, counting worker, reviewer and retry runs, before and after — not orchestrator cost alone |
| Should-be-true | Machine-checkable preconditions, including consumer ordering read from `wb deps`, can decide worker-side landing safely with no unsupervised conflict resolution on a shared target | Count holds versus auto-lands over a sample; confirm no auto-land involved a non-trivial merge, and no provider auto-landed ahead of an unlanded consumer the graph knew about |
| Should-be-true | A semantics-free phase concept is expressive enough for a SpecScore-backed review gate, with no review knowledge added to WB | Implement the review gate purely as phases running `specscore rehearse run` / `consilium verdict`, and check whether any WB change was needed to interpret them |
| Might-be-true | Skill routing changes shift orchestrator behaviour without any CLI change | Re-run the session-log analysis a few weeks after the routing fix and compare the manual-lifecycle versus dispatch call ratio |

## SpecScore Integration

- **New Features this would create:** a semantics-free phase list on `wb agent dispatch`; an agent-run conformance receipt (`wb agent receipt`); precondition-driven landing ownership reading `wb deps`
- **Existing Features affected:** `wb agent dispatch`/`await`/`status`/`logs`, `wb pr land`, `wb worktree land`, `wb deps`, the WB skill routing surface
- **Dependencies:** [orchestrator-default-to-dispatch](orchestrator-default-to-dispatch.md) for the routing fix; [agent-lane-verbs](agent-lane-verbs.md) shares the Collapse lever. No code dependency on SpecScore — the review gate composes through phase exit codes and artifact paths only

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
