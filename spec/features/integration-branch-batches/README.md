---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Integration Branch Batches

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=request-change) |
**Status:** Draft
**Source Ideas:** integration-branch-batch-landing
**Depends On:** [PR Land Waivers](../pr-land-waivers/README.md), [Mechanical Worktree Merge](../mechanical-worktree-merge/README.md)

## Summary

Merge independently reviewed lane commits, pinned at their reviewed SHAs, into a long-lived integration branch locally with merge commits, check the result locally, push it, and promote the integration branch to its target through one fully checked pull request whose reviewer reads only the integration delta.

## Problem

Lanes that target one integration branch each pay a CI run and a fence
refresh per landing; with backlogged runners this serializes the effort.
The founder's remedy for datatug/datatug-apps `public-demo` (2026-10) —
merge the approved lane heads exactly as reviewed, with merge commits — had
to be done by hand: WB's merge machinery takes only local worktrees, rebases
on drift, and nothing showed the reviewer only the conflict resolutions.

The process this Feature serves (founder, 2026-10-23: "I want fast efficient
process with full CI checks on PR to main and local check on merge to local
integration branches"):

1. a lane pushes its branch and opens no pull request;
2. a reviewer posts `Reviewed-Head: <sha>` for the lane SHA, as a commit
   comment on that SHA;
3. the operator merges that exact SHA into the integration branch locally
   (one merge commit), runs the repository's local checks, and pushes — no
   CI runs on that push, and the integration branch carries no ruleset;
4. one integration → target pull request runs full CI, gets a fresh review
   of its exact head, and lands under the repository's merge policy (through
   the target's merge queue when it has one).

## Behavior

### Command surface

```text
wb pr batch add        <owner/repo> --target <integration> <branch@sha=review-url>...
wb pr batch delta      <owner/repo> --target <integration> [--to <branch>] [--overlap]
wb integration merge   <owner/repo> <integration> [<branch@sha=review-url>...] [--checks <list>]
wb integration promote <owner/repo> <integration> [--to <branch>]
wb pr land <promote-pr> --approved-by <delta-review-url>   # REQ promote-landing
```

Every verb runs in a checkout of the repository (`--worktree`, default the
current directory) and takes `--format text|json`. A pin is
`<branch>@<sha>=<review-url>`, where the review URL is a commit comment
(`https://github.com/<owner>/<repo>/commit/<sha>#commitcomment-<id>`); the
review is named explicitly, never discovered. `--to` defaults to the
repository's default branch. Every command WB prints quotes its pins.

### What is reused, and what is not

Reused: WB worktree creation (the operator runs `wb worktree create` for the
integration worktree); review binding (`parseReviewedHeadLine`,
`ReviewDigest`, `ParseReviewerIdentity`); the repository's local-check recipe
(`quality.RepositoryRunOptions` and `quality.VerifyWithOptions`, as `wb
worktree merge` validates a candidate); `wb pr land`'s wait, merge queue,
waivers ([PR Land Waivers](../pr-land-waivers/README.md)) and merge. NOT
reused: merge-prepare's source merge step (no `--no-ff`, aborts on
conflict), its drift `git rebase --rebase-merges`, and its squash/rebase
merge-method fallback. Batch code MUST NOT call them. Nothing here requires a
server-enforced check, ruleset or pull request on the integration branch.

### REQ: batch-source-pins

Before any Git mutation, every pin MUST be verified, and all failures
reported together (exit `2`, `Outcome: refused`) with nothing changed:

1. the pinned SHA is a full or uniquely abbreviated commit reachable from
   `origin/<branch>` (`batch-source-not-on-branch`);
2. the review URL is a commit comment on exactly that SHA whose
   `Reviewed-Head:` line equals it (`batch-source-unreviewed`);
3. the comment's declared reviewer identity has no `unknown` part and its
   session differs from the session of the lane's Work Log claim for that
   branch when the claim is readable, and from the operator's own session
   (`batch-source-self-review`).

A pin already contained in the integration worktree's HEAD is skipped and
reported `already merged`, so a rerun is idempotent.

### REQ: batch-construction

`wb pr batch add` MUST run in a clean checkout whose HEAD contains
`origin/<integration>` (otherwise `batch-worktree-invalid`, printing `wb
worktree create <task> <repo> --base <integration>`). For each pin in order,
having fetched `origin/<branch>` and proved the pinned SHA is on it, `git merge --no-ff
--no-edit` with WB's fixed merge options (`-c merge.renames=true -c
merge.conflictStyle=merge -c rerere.enabled=false`) and the message
`Merge <branch> (<short-sha>) into <integration>` carrying the trailers
`Wb-Lane: <branch>@<full-sha>`, `Wb-Review: <url>`, `Wb-Review-Digest:
<digest>`, `Wb-Reviewer: <identity>` and `Wb-Merged-By: <operator identity>`.
Every pinned SHA becomes the second parent of exactly one merge commit. WB
MUST never rebase, squash, amend or force-push, nor touch a lane branch. The
merge commits are the batch's only state: there is no batch pull request,
body block or private cache.

### REQ: batch-conflict-stop-rerun

On a conflicting merge WB MUST leave the merge in progress, print the paths
and the worktree, and exit `1` with `batch-conflict`. The operator resolves
and runs `git commit --no-edit`, which keeps the trailers, then reruns the
same command: merged pins are skipped and the rest continue. A pin is skipped
only when it is on `origin/<integration>` or a local merge whose `Wb-Lane`
trailer names that merge's own second parent contains it; otherwise (a
resolution committed without the trailers) the rerun refuses
`batch-unrecorded-merge`. A rerun while a merge is still in progress refuses
`batch-worktree-invalid`.

### REQ: integration-merge-local-check-push

`wb integration merge` MUST perform `wb pr batch add` for any pins given,
then refuse `batch-unrecorded-merge` when any local first-parent merge not
yet on `origin/<integration>` lacks trailers naming its own second parent,
then run the repository's local-check recipe (default every configured
check; `--checks lint,test,build,spec` selects) on the resulting HEAD, and
push exactly the checked commit to `refs/heads/<integration>` without force
only when the checks passed, so agents never hand-roll the check. A failed
check stops with `integration-check-failed` (exit `1`) and the report, and
pushes nothing; a recipe that ran no check at all refuses
`integration-check-skipped` and pushes nothing. A rerun that has nothing new
to push runs no check. A non-fast-forward rejection refuses `integration-moved`;
the operator reruns in a fresh worktree, and merged pins are skipped there.

### REQ: batch-merge-provenance

Before printing a delta, opening a promotion and landing it, WB MUST verify
every commit on `git rev-list --first-parent origin/<to>..<integration
head>`. A merge commit's second parent must be either (a) the `Wb-Lane` SHA
of its own trailers, whose `Wb-Review` comment, re-read by id from the
repository named on the command line, is still on that SHA with the recorded
`Wb-Review-Digest` and a `Reviewed-Head:` equal to it, and declares a
complete identity different from `Wb-Merged-By`, or (b) an ancestor of
`origin/<to>` (a target refresh). Any other merge commit is `unrecorded`:
delta prints its full diff against its first parent (`kind: unrecorded`), and
promotion and landing refuse `batch-unrecorded-merge` naming the commit.
Non-merge commits are fixes and appear in the delta.

### REQ: batch-delta

`wb pr batch delta` MUST print only the integration delta of the integration
branch against `--to`:

1. for every merge commit on that first-parent walk: the diff from `git
   merge-tree --write-tree` of its two parents, run with the fixed merge
   options, to the committed tree — emitted only when non-empty (`kind:
   resolution`);
2. the diff of every non-merge commit on the walk (`kind: fix`).

Content brought in unchanged from a pinned SHA or the target never appears.
An empty delta prints `integration delta: empty` and exits `0`; it still
needs the promotion review of REQ promote-landing, because semantic
conflicts produce no textual one (decided 2026-10-23). `--overlap` adds, for
every file two or more lanes changed, each such lane's own diff of those
files (`kind: overlap`). `--format json` lists each part with commit, kind
and paths.

### REQ: integration-promote

`wb integration promote` MUST pass REQ batch-merge-provenance, then open, or
refresh the body of, the one pull request from `<integration>` to `--to`.
The body lists every recorded lane merge (branch, SHA, review) on the walk,
carries the marker `<!-- wb-integration-promote -->`, and names the `wb pr
batch delta` and `wb pr land` commands to run next. A rerun with nothing new
changes nothing.

### REQ: promote-landing

`wb pr land` MUST treat a pull request as a promotion when its body carries
the marker, and then, before merging or enqueueing:

1. require `--approved-by <comment-url>` naming a comment on that pull
   request whose `Reviewed-Head:` is its current head (`batch-delta-unreviewed`
   naming `wb pr batch delta`); the review's declared identity must have no
   `unknown` part and a session different from the operator's and from every
   `Wb-Merged-By` session on the walk (`batch-delta-self-review`);
2. pass REQ batch-merge-provenance;
3. land under the repository's own merge policy for its target (the merge
   queue's method when the target has one, otherwise `wb pr land`'s usual
   method selection), with full CI on the target, never updating the branch
   server-side and never arming auto-merge, so what lands is the reviewed
   head (inside the queue's merge-group commit under a merge queue). When that policy squashes, the integration branch is
   refreshed from the target (a target-refresh merge) or recreated before
   the next promotion.

A pull request without the marker any of whose commits carries a `Wb-Lane`
trailer refuses `batch-block-invalid` (naming `wb integration promote`, which
restores the body) instead of landing under the ordinary rules.

## Acceptance Criteria

### AC: bad-pins-refused-together-nothing-changed

**Requirements:** integration-branch-batches#req:batch-source-pins

**Given** three pins: one SHA not on its branch, one whose commit comment
names another SHA, and one whose comment is on another commit
**When** `wb pr batch add` runs
**Then** it exits `2` listing all three refusals and the worktree is
unchanged.

### AC: lane-author-review-is-self-review

**Requirements:** integration-branch-batches#req:batch-source-pins

**Given** a lane whose Work Log claim was made by session S and whose review
comment declares session S
**When** `wb pr batch add` runs
**Then** it refuses `batch-source-self-review`.

### AC: clean-merge-checked-and-pushed

**Requirements:** integration-branch-batches#req:batch-construction, integration-branch-batches#req:integration-merge-local-check-push, integration-branch-batches#req:batch-delta

**Given** two reviewed conflict-free lanes
**When** `wb integration merge` runs twice
**Then** origin's integration branch gained two first-parent merge commits
whose second parents are the pinned SHAs and whose trailers name each lane
and review, the local checks ran once, no force-push occurred, the second run
changed nothing, and `wb pr batch delta` prints `integration delta: empty`.

### AC: failed-local-check-pushes-nothing

**Requirements:** integration-branch-batches#req:integration-merge-local-check-push

**Given** a lane whose merge fails the local test check
**When** `wb integration merge` runs
**Then** it stops with `integration-check-failed` and origin is unchanged.

### AC: conflict-stops-and-rerun-continues

**Requirements:** integration-branch-batches#req:batch-conflict-stop-rerun, integration-branch-batches#req:batch-delta

**Given** three lanes where the second conflicts with the first in `a.go`
**When** `add` stops with `batch-conflict` listing `a.go`, the conflict is
resolved and committed with `git commit --no-edit`, and the command reruns
**Then** the first is skipped as already merged, the third merges, and
`delta` shows only the resolution hunk in `a.go`.

### AC: rerun-refuses-a-resolution-without-trailers

**Requirements:** integration-branch-batches#req:batch-conflict-stop-rerun

**Given** a conflicted lane merge left in progress
**When** the command reruns before the resolution is committed, and again
after it was committed with `git commit -m resolve` (dropping the trailers)
**Then** the first rerun refuses `batch-worktree-invalid` and the second
refuses `batch-unrecorded-merge`; nothing is pushed.

### AC: moved-integration-branch-is-never-overwritten

**Requirements:** integration-branch-batches#req:integration-merge-local-check-push

**Given** origin's integration branch moves while the local checks run
**When** `wb integration merge` pushes
**Then** it refuses `integration-moved` and origin keeps the other commit.

### AC: a-recipe-that-ran-nothing-pushes-nothing

**Requirements:** integration-branch-batches#req:integration-merge-local-check-push

**Given** a repository whose local-check recipe runs no check
**When** `wb integration merge` runs with a new lane
**Then** it refuses `integration-check-skipped` and origin is unchanged.

### AC: fix-commit-appears-in-delta

**Requirements:** integration-branch-batches#req:batch-delta

**Given** an integration branch with one non-merge fix commit
**When** `delta` runs
**Then** it shows exactly that commit as `kind: fix`.

### AC: hand-pushed-merge-is-unrecorded

**Requirements:** integration-branch-batches#req:batch-merge-provenance

**Given** an integration branch to which someone pushed a clean `git merge
<unreviewed-sha>`, and separately a lane merge whose review comment was
edited after the merge
**When** `wb pr batch delta`, `wb integration promote` and `wb pr land` run
**Then** delta prints each such merge in full as `kind: unrecorded`, and
promotion and landing refuse `batch-unrecorded-merge` naming the commit.

### AC: promote-opens-one-pr

**Requirements:** integration-branch-batches#req:integration-promote

**Given** an integration branch with two recorded lane merges
**When** `wb integration promote` runs twice
**Then** one pull request into `main` exists whose body lists both lanes,
and the second run changed nothing.

### AC: promote-land-binds-review-and-merge-method

**Requirements:** integration-branch-batches#req:promote-landing

**Given** a promotion pull request
**When** `wb pr land` runs (a) with no `--approved-by`, (b) with a review by
a session named in a `Wb-Merged-By` trailer, (c) after its marker was
deleted, (d) with an independent review of the current head
**Then** (a) refuses `batch-delta-unreviewed`, (b) refuses
`batch-delta-self-review`, (c) refuses `batch-block-invalid`, and (d) lands
it under the repository's merge policy, without arming auto-merge.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
