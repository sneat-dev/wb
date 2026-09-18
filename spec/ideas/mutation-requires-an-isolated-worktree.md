---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Mutation requires an isolated worktree, acquired in one call

**Status:** Draft
**Date:** 2026-09-17
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:efficient-agent-pipeline

## Problem Statement

How might we make worktree isolation the unavoidable first step of any agent-run change, without replacing the harness-native delegation agents already use?

## Context

An earlier draft of this idea proposed making `wb agent dispatch` + `wb agent
await` the taught default for handing off bounded implementation work. The
founder declined that on 2026-09-17, and the reasoning holds up: a harness-native
subagent already runs in a fresh context and returns only a summary, so it
delivers the token relocation dispatch was being credited with. Dispatch's
genuinely unique properties are worktree isolation, CPU admission, model routing,
cross-machine execution, and a durable run record — and of those, only isolation
is load-bearing for everyday work. Adversarial review also found the durable-record
property weaker than assumed: `internal/runlog` resolves its store per managed
worktree (`internal/runlog/runlog.go:310`), records nothing outside one, and the
store is deleted with the worktree.

So the intervention worth making is much smaller than a delegation default:
**keep harness-native delegation, and make isolation mandatory and cheap.**

### Why isolation is the thing that actually fails

A harness-native subagent shares the parent's filesystem and working directory.
Nothing stops two of them editing the same checkout, and nothing stops either of
them writing into a canonical clone — which `rule:parallel-agents-need-worktree-isolation`
exists to prevent and which this repository's own `CLAUDE.md` documents as having
destroyed unlanded work on 2026-08-27. It happened again while the first draft of
this very idea was being written: the authoring agent ran `specscore idea new`
inside the canonical clone and needed several recovery calls.

The failure is not that agents disagree with the rule. It is that obeying it
costs several calls — register a session, write a prompt file, create the
worktree, read the path out of the output, change directory — at exactly the
moment an agent is trying to start work. A rule that costs five calls loses to a
habit that costs zero.

### The one-call form, verified

Establishing the exact invocation took several attempts, which is itself the
evidence that it needs writing down. Once per session:

```sh
wb session register --pid $PPID --runtime <harness> --model <exact-model>
```

Then, for each repository about to be mutated, in a single call:

```sh
cd "$(wb worktree create <task> <owner/repository> --resume \
  --agent <agent> --agent-runtime <runtime> --model <exact-model> \
  --original-prompt-file <path> --format json \
  | jq -r '.worktrees[0].worktree_dir')"
```

Four things about that line are not guessable and were each found by failing:

- **The envelope key is `worktrees[]`, not `results[]`.** `wb worktree list`
  returns `results[]`; `wb worktree create` returns `worktrees[]`. A `jq -r
  .worktree_dir` reads `null` and the `cd` silently fails.
- **`--original-prompt-file` is mandatory in agent mode**, so the Work Log can
  retain the originating request.
- **The session must be registered first**, or creation is refused outright.
- **`--resume` makes it idempotent**, so a retry after any failure re-enters the
  same worktree instead of erroring.

The informational preamble (`remote claim skipped: …`) goes to stderr, so the
pipe is safe without filtering.

## Recommended Direction

Teach one rule, in the routing surface rather than in prose nobody loads:

> **If the work mutates a repository, acquire an isolated worktree first, in one
> call. If it does not, use a harness-native subagent and change nothing.**

Read-only work — research, measurement, review, answering a question — needs no
worktree and should not pay for one. That split is cheap to evaluate and it is
the whole rule.

Reserve `wb agent dispatch` for the cases where its other properties are the
point: work that must outlive this session, run on another machine, or run on a
different vendor's model. It stays available; it stops being the default anyone
is told to reach for.

## Alternatives Considered

- **Make `wb agent dispatch` the taught default for bounded implementation work.**
  Declined by the founder. It replaces harness-native delegation wholesale to
  obtain a benefit (context relocation) that a subagent already provides, at the
  cost of a full written brief per task — and a thin brief to a cheap worker is
  the expensive failure mode.
- **Add a `--cd` flag to `wb worktree create`.** Not possible: a subprocess
  cannot mutate its parent shell's working directory. The command-substitution
  form above is the achievable equivalent.
- **Rely on `wb worktree guard` to catch canonical-clone writes after the fact.**
  Insufficient on its own: it reports a violation once work is already misplaced,
  and recovery via `wb worktree rescue` costs more calls than prevention.
- **Rewrite the `wb-worktrees` and `wb-agents` skill bodies.** Necessary but not
  sufficient, and not the root cause — see below.

## MVP Scope

Put the one-call form and the mutation/read-only split into the WB routing
surface, so an agent meets it at the moment it decides how to start work:

1. The verified `cd "$(wb worktree create … | jq -r '.worktrees[0].worktree_dir')"`
   line as the first-shown path in `wb-worktrees`, with its four gotchas.
2. A stated fork at the head of that skill: mutating work acquires a worktree;
   read-only work uses a harness-native subagent and needs nothing.
3. A row in the root `wb` skill's routing table for the situation "about to make
   a change in a repository", phrased from the agent's own situation rather than
   from a user's request.

No CLI change, no new flags.

## Not Doing (and Why)

- Making `wb agent dispatch` the delegation default — declined by the founder; a harness-native subagent already relocates context, and dispatch's other properties are not needed for everyday work
- Adding a literal `--cd` flag — a subprocess cannot change its parent shell's working directory
- Converging the `worktrees[]` / `results[]` JSON envelopes — a real inconsistency and a genuine trap, but a breaking change to a machine-readable contract; it belongs in its own additive task
- Any CLI change at all — this phase is deliberately zero-code

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A one-call worktree acquisition is cheap enough that agents stop skipping it | Count canonical-clone `wb worktree rescue` invocations and `wb worktree guard` findings before and after |
| Must-be-true | The mutation / read-only split is unambiguous enough to apply without judgement | Sample delegated tasks and record any where the classification was genuinely unclear |
| Should-be-true | Harness-native subagents plus mandatory isolation avoid the collisions `wb agent dispatch` was going to prevent | Watch for two concurrent agents touching one checkout once the rule is in the routing surface |
| Might-be-true | A routing-table row phrased as a situation beats `wb-worktrees`' unconditional "before editing or branching" trigger | Replay representative session openings and record which skill loads |

## SpecScore Integration

- **New Features this would create:** none — a revision to the WB skill routing surface
- **Existing Features affected:** the `wb`, `wb-worktrees` and `wb-agents` skills
- **Dependencies:** none; [efficient-agent-pipeline](efficient-agent-pipeline.md) builds on it

## Open Questions

- `wb-agents` is absent from the root `wb` skill's routing table, but that table
  does route delegation through `$offload`, which dispatches a WB worker. Should
  the isolation rule live on the `$offload` row, on a new row, or on both?
- Harness-native subagents bypass WB's CPU admission entirely. Does the
  instruction also need to require `wb run --` for test commands inside a
  worktree, or is that a separate concern?
