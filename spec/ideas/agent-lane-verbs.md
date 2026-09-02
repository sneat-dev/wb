---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Agent lane verbs for token economy

**Status:** Draft
**Date:** 2026-09-02
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB let a long-running agent lane start, resume, and hand off a multi-PR effort without re-deriving the same repository, branch, gate, and convention context from scratch in every session?

## Context

### The observation

A single WB improvement lane on 2026-09-02 shipped four PRs into `sneat-dev/wb`.
Before it could write a line of code in each of them, it re-read the same
material: `AGENTS.md`, the CI workflow to learn the gate commands and the
coverage floor, `ai/capabilities.json` to learn a new command leaf needs a row,
`docs/cli-flag-matrix.md` to learn a new leaf needs a matrix line, the spec-lint
baseline to learn which violations were pre-existing, and the merge convention
(squash with an explicit `--subject`).

None of that changed between PRs. All of it was re-derived per PR, and the same
lane was killed once by a rate limit mid-effort — after implementing a feature
and *before* committing it, which is the most expensive place to stop. The next
session had to reconstruct not only the repository's conventions but the state
of the unfinished work itself.

WB already stores nearly all of the durable half. `.worktree.md` answers "where
am I and may I write here". The Work Log stores the ordered instruction
sequence, provenance, and progress. `wb worktree info` reads it back. What is
missing is the *lane*: the multi-worktree, multi-PR effort that outlives any one
worktree and any one session, and the repository facts that effort keeps paying
to rediscover.

### What already exists, and what it does not cover

| Concern | Covered today by | Gap |
|---|---|---|
| Where am I, may I write | `.worktree.md`, `wb worktree guard` | Per checkout only; says nothing about the effort spanning several |
| What was I told to do | Work Log instruction sequence | Per worktree; a lane's next PR starts a new worktree and a new log |
| What landed so far | Merge receipts, `wb worktree list` | Not aggregated as "this lane's shipped set" |
| How do I verify this repo | Re-read `.github/workflows/*` every time | Nothing caches the gate commands, the coverage floor, or the merge convention |
| What is already red | Re-run the linter against `main` every time | No recorded baseline, so "did I break this?" costs a second full run |

### Why this is a token-economy problem, not a convenience one

The re-derivation is not free reading — it is the most expensive kind. Reading a
CI workflow to extract four commands costs the whole file. Establishing a
spec-lint baseline costs a full run against `main` plus a full run against the
branch. Multiplied across the PRs of one effort and across the sessions a
long-running effort spans, this is a large fraction of an agent's budget spent
learning what the previous session already knew.

## Recommended Direction

Give WB a first-class **lane**: a named, durable effort that owns a set of
worktrees across repositories, an ordered record of what it has shipped, and a
cached, *invalidatable* set of repository facts it would otherwise re-derive.

The verbs should be few and boring. `wb lane start <name>` opens or resumes a
lane and prints exactly the brief a fresh session needs: the repositories in
scope, the worktrees it owns and their state, what has already landed, what is
in flight, and the verification commands for each repository. `wb lane record`
appends a landed PR and its merge SHA. `wb lane brief` re-prints that briefing
without changing anything, so a compacted or resumed session can re-orient in
one command instead of a dozen reads. `wb lane finish` closes it and hands back
the cleanup set.

The repository facts belong under WB's existing evidence discipline, not in a
free-text note. Gate commands, the coverage floor, the required capability and
flag-matrix surfaces, the merge convention, and a recorded lint baseline are all
*derived* from files WB can hash. Cache them keyed by the content hash of their
sources, so a stale cache is impossible rather than merely unlikely: when
`.github/workflows/go-ci.yml` changes, the cached floor is invalid by
construction and re-derived once.

The mid-effort kill is the case to design for first. A lane that knows its own
worktrees can answer "is there uncommitted work anywhere in this effort?" in one
command — and `wb worktree guard --published` already answers "did the push that
looked fine actually land?" for each of them. Together those two are the whole
recovery story for the failure that actually happened.

## Alternatives Considered

**Put it in the Work Log.** The Work Log is per worktree by design — that is what
makes it survive an interrupted session and reconcile safely. A lane spans
worktrees and outlives every one of them. Overloading the Work Log with a
cross-worktree aggregate would compromise the per-worktree guarantees that make
it trustworthy, to serve a different lifetime.

**Write a longer `AGENTS.md`.** This is the status quo and it does not work: the
cost is not that the facts are missing, it is that reading them is paid again
every session, and a prose file cannot be invalidated when the workflow it
describes changes. Documentation that drifts silently is worse than a cache that
provably cannot.

**A generic `wb kv` scratchpad.** It would remove the re-derivation cost and
every guarantee with it. Nothing would stop a lane from caching a coverage floor
that the workflow changed three commits ago, and a wrong cached gate is worse
than no cache: it produces a confident local "green" that CI then refutes.

## MVP Scope

One job: **a fresh session resumes a lane in one command.**

`wb lane start <name>` and `wb lane brief` print the lane's repositories,
worktrees and their state, landed PRs with merge SHAs, in-flight PRs, and the
per-repository verification commands derived from CI. `wb lane record` appends a
landing. Nothing else — no scheduling, no dispatch, no cross-machine state.

Timebox it to what one long lane needs to survive being killed and resumed. If
the briefing does not measurably replace the re-derivation reads, the idea is
wrong and the caching half should not be built.

## Not Doing (and Why)

- Cross-machine lane state — `wb remote` already owns fleet-wide sharing, and a lane that needs it should compose with the existing store rather than grow a second one.
- Agent dispatch or scheduling — WB records and verifies work; deciding who runs next is the harness's job, not WB's.
- Caching anything WB cannot invalidate by content hash — a cache that can silently go stale produces a confident wrong answer, which is worse than re-deriving.
- A free-text lane notes field — it would become an unverifiable second source of truth beside the Work Log.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A lane briefing actually replaces the re-derivation reads, rather than being read *in addition* to them | Instrument one real multi-PR lane: count the orientation reads per PR with and without the briefing |
| Must-be-true | Every fact worth caching has a hashable source, so staleness is structurally impossible | Enumerate the candidate facts and name each one's source file(s); drop any that cannot be keyed |
| Should-be-true | Lane state is the right lifetime — efforts really do span worktrees and sessions often enough to justify a verb family | Sample recent multi-PR efforts: how many spanned more than one worktree and more than one session? |
| Should-be-true | The recovery case (killed mid-effort, before commit) is common enough to design for first | Count interrupted efforts and where they stopped |
| Might-be-true | The same lane record is useful to a human reviewing what an agent did | Show one lane's record to a reviewer and ask whether it answered their questions |

## SpecScore Integration

- **New Features this would create:** TBD at design time — likely one Feature for the lane record and verbs, and a separate one for content-hash-keyed repository fact caching, since the second is useful without the first.
- **Existing Features affected:** work-log (adjacent lifetime, must not be overloaded), worktree-lifecycle (a lane owns worktrees), fleet-quality (the gate commands a briefing would surface).
- **Dependencies:** none

## Open Questions

- Does a lane own worktrees exclusively, or may two lanes share one worktree? Exclusive ownership is simpler and matches how efforts actually run, but it needs a stated answer before the record shape is fixed.
- Should `wb lane brief` refuse to print a briefing whose cached facts are invalid, or print them marked stale and re-derive in the background?

---
*This document follows the https://specscore.md/idea-specification*
