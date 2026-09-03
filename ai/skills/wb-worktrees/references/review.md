# Review a pull request in a tracked checkout

**Never `gh pr checkout`. Never `git worktree add --detach`.** Use:

```sh
wb worktree review <owner/repository>#<number> --model <your model>
```

## Why this matters more than it looks

A reviewer needs the pull request's head, so `gh pr checkout` is the natural
move — and from that moment WB has no manifest, no claim, no owner and no Work
Log for the checkout. Nothing in WB can retire a checkout WB did not create.

One night's sweep on this fleet found **ten** of them: four to seventeen hours
old, every pull request already merged, about 1.2 GB, and unreachable by any
verb. `wb worktree list` showed 50 rows for 60 checkouts. That is the single
largest source of permanent worktree debt here, and it is created one review at
a time by people doing the obvious thing.

`wb worktree review` makes the tracked path the easy path.

## What you get

- a WB worktree at `~/.wb/worktrees/review-<owner>-<repo>-<n>/<owner>/<repo>`
- on a **local branch** `review/<owner>-<repo>-<n>` at the pull request's head —
  a branch rather than a detached HEAD, because detached is exactly the shape
  nothing can retire. The branch is never pushed.
- an immutable manifest recording `purpose: review`, the pull request, and a TTL
- a Work Log claim, an owner, and the fleet-wide remote claim
- an inventory row whose class is `review`, so no landing verb offers to merge
  someone else's work

The task name is derived from the pull request, so a second reviewer of the same
pull request collides with the first — which is the right outcome, and far
better than two checkouts nobody can tell apart.

## Finish

```sh
wb worktree review end review-<owner>-<repo>-<n> --apply
```

It seals the Work Log and removes the checkout **even when it is dirty**, after
capturing the uncommitted bytes into the private archive. A review leaves
scratch files — notes, a coverage profile, an experiment — and none of that is
work anyone intends to keep. A checkout that survives because someone left a
note in it is the same permanent debt in a new costume.

You do not have to remember: `wb worktree gc` retires a review checkout once its
pull request lands, on the pull request's own evidence.

## Reviewing something that is not open

A closed or merged pull request's head is a historical fact rather than a live
one, so it is refused by default. Reviewing it is deliberate:

```sh
wb worktree review acme/app#41 --sha 4f2a1c9
```

## Refusals and what resolves each

| Refusal | Sanctioned next step |
| --- | --- |
| `pull-request-not-open` | `wb worktree review <selector> --sha <head>` to review an exact commit deliberately |
| `pull-request-has-no-head` | open the pull request in the browser; GitHub reports no head commit for it |
| the branch already exists | a review of this pull request is already checked out — that is the collision working; use it, or end it first |

## While reviewing

The checkout is an ordinary WB worktree, so `wb worktree log steer`,
`wb worktree info` and the rest all work on it. Do not commit to the review
branch: it is a reading position, not a place to write. If a review produces a
fix, create a normal worktree for it with `wb worktree create`.

## Lane contract

Consume a library through `wb deps propagate local`; the orchestrator runs
`remote` at the end. End with `wb worktree review end`.
