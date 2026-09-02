---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Claim-at-push visibility for same-machine agents

**Status:** Draft
**Date:** 2026-09-02
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB stop two agents on the same machine from silently working the same claim, when the claim store is machine-scoped and therefore cannot tell them apart?

## Context

### The blind spot is by design, and that is the problem

`wb remote claim` reserves a task fleet-wide. Its identity is `login/machine`:
a claim held by the same login on a *different* machine is correctly treated as
another holder's claim, and taking it needs `--force`. That is the right
boundary for the problem it was built for — two people, or one person on two
laptops, starting the same task without knowing about each other.

It gives no protection at all for the case that now dominates. Several agent
sessions run concurrently on one VM, under one login. To the claim store they
are one holder. Agent A claims `wb-improvements`; Agent B, minutes later on the
same VM, claims it too — and succeeds, because the store cannot tell them apart
and correctly concludes the holder already holds it.

### What this actually costs

Two consequences, and the second is worse:

1. **Duplicated effort.** Two agents do the same work, and the second discovers
   it only when its push is rejected or its PR conflicts.
2. **Racing on the same files.** WB worktree paths are deterministic
   (`<wb-home>/worktrees/<task>/<owner>/<repository>`), so two dispatches of the
   same task name do not get two isolated checkouts — they get *the same
   directory*. Two agents then edit, stage, and commit in one working tree. The
   isolation guarantee that makes worktrees safe is silently absent exactly when
   two agents believe they each have their own.

Related evidence from the same period: a lane's unpushed branch was merged to
`main` by a *different* agent's merge drain, because nothing tied that branch to
the session still working on it. The branch was visible fleet-wide; the fact
that someone was mid-effort on it was not.

### Why the push is the right moment

A claim is taken at the start of an effort, when there is nothing to compare —
two agents intending the same work look identical. A push is the first moment
with *evidence*: a specific branch, at a specific commit, from a specific
checkout, by a specific session. That is when a collision becomes a fact rather
than a possibility, and it is early enough to matter — before a PR, before a
merge, before a review.

`wb worktree guard --published` already fetches the worktree's branch and
compares it to `HEAD` after a push. The observation this idea needs is adjacent
to one WB now makes.

## Recommended Direction

Make the claim record what is actually happening, not merely who intends it, and
make the push the moment that record is written and checked.

At push time, WB should attach the pushed branch, its commit, the checkout path,
and the session identity to the claim. That turns the claim from a start-of-work
intention into a live statement: *this session, in this checkout, has this
branch at this commit.* A second agent pushing a different commit to the same
claimed task is then a fact WB can name, with both sides' evidence, instead of a
race nobody observes.

Session identity is the missing axis, and WB already has it — `wb worktree
create` requires a Work Log claim with model and provenance, and the session
registry knows which sessions are live. The claim should carry it so
`login/machine` stops being the finest grain WB can see. Crucially, adding the
axis must not weaken the existing boundary: same-login-different-machine stays
another holder's claim, and `--force` keeps meaning what it means.

The deterministic-path collision deserves a direct answer rather than a warning.
Two live sessions resolving to the same worktree path is not a policy question —
it is the isolation guarantee being violated. WB should refuse it at `wb worktree
create` when the existing checkout carries an active claim from a *different*
live session, and say which session holds it, rather than handing back a
directory someone else is committing in.

## Alternatives Considered

**Make claims process-scoped instead of machine-scoped.** This would catch the
same-VM case directly, and break the case claims exist for: a crashed process
would strand its claim with no holder to release it, and the heartbeat-staleness
rule that makes claims recoverable is built on machine liveness. Replacing the
identity is a much larger change than extending it.

**Warn instead of refuse on a colliding worktree path.** An agent that has been
warned still proceeds — that is what agents do with warnings. Two sessions
committing in one working tree corrupts both efforts, and the corruption is
discovered late. This is one of the few places where refusing is clearly right.

**Detect collisions at PR time.** Too late. By then both agents have spent their
work, both have run CI, and one of them must be discarded. The push is the
earliest moment with real evidence, and the cheapest one to act on.

**Rely on unique task names by convention.** This is the status quo, and it
failed: task names are chosen by whoever dispatches, and two dispatches of the
same effort naturally pick the same name. A convention that must never be
violated by an automated caller is not a control.

## MVP Scope

One job: **a second agent pushing to a claimed task learns about the first one,
at push time, with both sides' evidence.**

Attach branch, commit, checkout path, and session identity to the claim on push,
and report a collision naming the other session, its branch, and its commit.
Refuse `wb worktree create` when the deterministic path already holds a
different live session's active claim.

Nothing else: no arbitration, no automatic handoff, no queueing. Naming the
collision is the whole MVP; deciding what to do about it stays with the operator.

## Not Doing (and Why)

- Automatic arbitration between two colliding agents — WB's job is to make the collision visible with evidence; choosing a winner is a judgment call that needs the operator.
- Replacing machine-scoped claim identity — the existing boundary is correct for the cross-machine case and its staleness rules depend on machine liveness; this extends the identity rather than swapping it.
- Locking a branch against all other writers — the merge drain that landed an unpushed branch is a coordination problem, and a hard lock would break legitimate mechanical merges.
- Per-commit claim heartbeats — pushes are the natural, already-instrumented checkpoint; a finer cadence buys little and costs a write per commit.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Same-machine collisions actually occur often enough to justify a control, rather than being a one-off | Count them: scan claim history and worktree paths for two live sessions on one task |
| Must-be-true | Session identity is available and stable at push time in every path that pushes | Enumerate the push paths (hook, `wb worktree merge`, `deps bump`, a bare `git push`) and check each can name its session |
| Must-be-true | Adding a session axis does not weaken same-login-different-machine, which must stay another holder's claim | Extend the existing claim race tests with a session axis and assert the machine boundary is unchanged |
| Should-be-true | Refusing a colliding worktree path is safe — it never blocks a legitimate resume of one's own worktree | Exercise resume, handoff, and park/resume flows against the refusal |
| Might-be-true | The same evidence would let a merge drain skip a branch a live session is still working on | Prototype the check against a real drain and see whether it has the evidence it needs |

## SpecScore Integration

- **New Features this would create:** TBD at design time — likely a change to remote-claims rather than a new Feature, plus a worktree-lifecycle rule for the colliding-path refusal.
- **Existing Features affected:** remote-claims (claim identity and collision reporting), worktree-lifecycle (deterministic path collision), work-log (session identity source), mechanical-worktree-merge (a drain that could consult the same evidence).
- **Dependencies:** none

## Open Questions

- Should a collision at push time be a refusal or a reported finding? A refusal risks blocking a legitimate second push after a handoff; a finding risks being ignored. The handoff case probably decides it.
- Does the session axis belong in the claim file itself, or in a separate live-session index the claim references? The first is simpler; the second keeps the claim file stable across a session's lifetime.

---
*This document follows the https://specscore.md/idea-specification*
