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

Merge several independently reviewed PRs, pinned at their reviewed heads, into one batch branch with merge commits, open one PR, show the reviewer only the integration delta, and attribute any batch CI failure.

## Problem

Lanes that target one integration branch each pay a CI run and a fence
refresh per landing; with backlogged runners this serializes the effort.
The founder's remedy for datatug/datatug-apps `public-demo` (2026-10) —
merge the approved lane heads exactly as reviewed, with merge commits, into
one batch branch and land one PR (#520) — had to be done by hand: WB's merge
machinery takes only local worktrees, rebases on drift, and nothing showed
the reviewer only the conflict resolutions or attributed a failure to a lane.

## Behavior

### Command surface

```text
wb pr batch create <owner/repo> --target <branch> <pr@sha=review-url>...
wb pr batch add    <owner/repo> --target <branch> [<pr@sha=review-url>...]
wb pr batch resume <owner/repo> --target <branch>
wb pr batch delta  <owner/repo> --target <branch> [--overlap]
wb pr batch status <owner/repo> --target <branch>
wb pr land <batch-pr>          # unchanged verb; batch rules apply (REQ batch-landing)
```

Every verb runs in the batch worktree (`--worktree`, default the current
directory) and takes `--format text|json`. A pin
is `<number>@<sha>=<review-comment-url>` (no `#`, which starts a shell
comment; a quoted leading `#` is accepted); the review is named explicitly,
never discovered. Every command WB prints quotes its pins.

### What is reused, and what is not

Reused: WB worktree creation (the operator runs `wb worktree create` for the
batch worktree, so its Work Log claim and lifecycle are the ordinary ones); review binding
(`namedReviewedHead`, `parseReviewedHeadLine`, `ReviewDigest`,
`ParseReviewerIdentity`); `wb pr create`'s push and PR open/adopt;
`wb pr land`'s wait, waivers ([PR Land Waivers](../pr-land-waivers/README.md))
and merge; `reconcileAbsorbedSourcePullRequests` for source PRs after
landing. NOT reused: merge-prepare's source merge step (no `--no-ff`, aborts
on conflict), its drift `git rebase --rebase-merges`, and its squash/rebase
merge-method fallback. Batch code MUST NOT call them.

### REQ: batch-source-pins

Before any Git or GitHub mutation, every pin MUST be verified, and all
failures reported together (exit `2`, `Outcome: refused`) with nothing
created:

1. the PR is open, not draft, and its base is `--target`
   (`batch-source-wrong-base`, `batch-source-not-open`);
2. its current head equals the pin; a short pin must be a unique prefix of
   it (`batch-source-head-moved`);
3. the pinned head is not already contained in the target
   (`batch-source-already-landed`), nor in the batch or in another pin of the
   same invocation (`batch-source-already-included`), so every pin gets a
   merge commit of its own;
4. the review URL is a comment on that PR whose `Reviewed-Head:` line equals
   the pinned head (`batch-source-unreviewed`);
5. the comment's declared reviewer identity has no `unknown` part and its
   session differs from the session of the lane's Work Log claim for that
   branch when the claim is readable, and from the operator's own session
   (`batch-source-self-review`).

The receipt records, per source, the comment id, `ReviewDigest` of its body,
and the identity with `identity: declared` — WB does not claim to prove it.
The batch also records the session of every WB invocation that mutated it
(`built_by`).

### REQ: batch-construction

A batch MUST run in a clean WB-managed worktree of the repository, created
from the target on a branch `<target>-batch-<n>` (`wb worktree create
<target>-batch-<n> <repo> --base <target> --branch <target>-batch-<n>`) and
still at the fetched target tip; otherwise every verb refuses
`batch-worktree-invalid` and prints that command. Then, for each source in
order: `git fetch origin <pinned-sha>` (the commit
itself, never `refs/pull/<n>/head`), and `git merge --no-ff --no-edit -m
"Merge #<n> (<short-sha>) into <batch-branch>" <pinned-sha>` with WB's fixed
merge options (`-c merge.renames=true -c merge.conflictStyle=merge -c
rerere.enabled=false`), recorded in the receipt. Every pinned head becomes the
second parent of exactly one batch merge commit. WB MUST never rebase,
squash, amend or force-push a batch branch, nor touch a source branch.

### REQ: batch-refuses-non-merge-targets

`create`, `add` and landing MUST refuse `batch-merge-commits-disabled` when
the repository disallows merge commits, and `batch-target-merge-queue` when
the target requires a merge queue. Landing a batch PR MUST refuse an explicit
`--merge-method squash|rebase` (`batch-merge-method`).

### REQ: batch-conflict-stop-resume

On a conflicting source merge WB MUST leave the merge in progress, record the
source as `conflicted` with its paths, print the paths and worktree, and exit
`1` with `batch-conflict`. `wb pr batch resume` MUST continue only when the
worktree is clean and HEAD is a merge commit whose parents are exactly
`[previous batch head, pinned head]`; it records `resolved` and continues.
Otherwise it refuses `batch-resolution-invalid`.

### REQ: batch-state-on-origin

The batch's authoritative state MUST live on origin, so any machine can
continue it: the batch branch itself, plus a fenced YAML block in the batch
PR body (between `<!-- wb-batch:begin -->` and `<!-- wb-batch:end -->`)
holding repository, target, base SHA, merge options, and per source: PR,
pinned head, review comment id and digest, declared reviewer, merge commit,
state, and the builders' sessions (`built_by`). The block omits the PR
number and the cache revision, so an unchanged batch renders an unchanged
body. WB's private-state copy is an append-only cache: each mutation writes
a new revision, never rewriting an old one. It is preferred over the body
only while its steps extend the body's (a batch stopped on a conflict is not
published until it resolves); a missing cache is rebuilt from origin. Because anyone with write access can edit the body
block or push to the branch, nothing in them is trusted as given: REQ
batch-merge-provenance re-verifies it before delta and landing. A push
rejected as non-fast-forward (another machine moved the batch) is refused
`batch-moved`; WB rebuilds its state from origin and the operator reruns —
it never force-pushes. Pushing the batch branch and opening its one PR
(merge method `merge`) happens after the last source merges; every later
mutation regenerates the PR body. A rerun of a completed step creates no
second PR and no duplicate merge.

### REQ: batch-add-and-refresh

`wb pr batch add` MUST verify new pins (REQ batch-source-pins), then, when the
target advanced past the batch's recorded base, first merge the target tip
into the batch as its own `Merge <target> (<short-sha>) into <batch>`
commit (conflicts handled as in REQ batch-conflict-stop-resume), then merge
the new sources, push without force, and regenerate the body. Adding to a
merged or closed batch refuses `batch-closed`. `wb pr batch add <batch>` with
no pins performs only the target refresh.

### REQ: batch-merge-provenance

Before printing a delta and before landing, WB MUST first require the
recorded `base` to be an ancestor of `origin/<target>` (else refuse
`batch-base-invalid`), then verify every merge commit on `git rev-list --first-parent <base>..<head>`. Its second parent
must be either (a) a pin recorded in the body block whose review comment,
re-read from GitHub by id, still has the recorded `ReviewDigest` and a
`Reviewed-Head:` equal to that pin, or (b) an ancestor of `origin/<target>`
(a target refresh). Any other merge commit — one WB did not make, or a pin
whose review comment changed or vanished — is `unrecorded`: delta prints its
full diff against its first parent (`kind: unrecorded`), and landing refuses
`batch-unrecorded-merge` naming the commit.

### REQ: batch-delta

`wb pr batch delta` MUST print only the integration delta, computed from
origin's batch branch:

1. for every merge commit on `git rev-list --first-parent <base>..<head>`:
   the diff from `git merge-tree --write-tree` of its two parents, run with
   the receipt's merge options, to the committed tree — emitted only when
   non-empty (`kind: resolution`);
2. the diff of every commit in `git rev-list --first-parent --no-merges
   <base>..<head>` (`kind: fix`).

Content brought in unchanged from a pinned head or the target never appears.
An empty delta prints `integration delta: empty` and exits `0`; it still
needs the batch review of REQ batch-landing, because semantic conflicts
produce no textual one (decided 2026-10-23, after a batch whose two semantic
conflicts had no textual conflict). `--overlap` makes that review cheaper:
it adds, for every file two or more sources changed, each such source's own
diff of those files (`kind: overlap`). `--format json` lists each part with
commit, kind and paths.

### REQ: batch-landing

`wb pr land` MUST recognise a batch PR by its body block (REQ
batch-state-on-origin) and, before arming auto-merge:

1. require `--approved-by <comment-url>` whose `Reviewed-Head:` is a batch
   head H such that every first-parent commit from H to the current head is a
   target-refresh merge whose delta part is empty; otherwise refuse
   `batch-delta-unreviewed` naming `wb pr batch delta <batch>`. The review's
   declared identity must have no `unknown` part and a session different from
   the operator's and from every `built_by` session
   (`batch-delta-self-review`);
2. pass REQ batch-merge-provenance;
3. not use server-side update-branch: a batch behind its target refuses
   `batch-behind-target` naming `wb pr batch add <batch>`;
4. land with merge method `merge` under REQ batch-refuses-non-merge-targets.

After landing, `reconcileAbsorbedSourcePullRequests` comments on and closes
every source PR GitHub did not already mark merged, as it does for
merge-prepare batches (a source whose head advanced after its pin stays open
with a comment, as that reconciler already decides), and the receipt records
each source PR's final state.

### REQ: batch-failure-attribution

`wb pr batch status` MUST list, for each failed check on the batch head, one
class: `pre-existing` (it failed on the batch base SHA), `source #<n>` (it
failed on that source's pinned head and passed on the base), `integration`
(passed on the base and on every source's pinned head), or `unattributed`
(a needed run is absent or not terminal). When several sources fail the
check on their pinned heads and it passed on the base, the class lists all of
them (`source #2, #5`). It reads only existing runs — one
check read per distinct SHA, cached for the invocation — and starts no CI.
The batch PR body carries the same table when a batch PR has failed checks
and the body is regenerated.

## Acceptance Criteria

### AC: bad-pins-refused-together-nothing-created

**Requirements:** integration-branch-batches#req:batch-source-pins

**Given** four PRs into `public-demo`: one whose head moved past its pin, one
whose review comment names an older head, one draft, one whose review
identity is `unknown@unknown@unknown`
**When** `wb pr batch create` names all four
**Then** it exits `2` listing `batch-source-head-moved`,
`batch-source-unreviewed`, `batch-source-not-open` and
`batch-source-self-review`, and no branch, worktree, receipt or PR exists;
**and** a pin that is an ancestor of another pin in the same invocation
refuses `batch-source-already-included`.

### AC: lane-author-review-is-self-review

**Requirements:** integration-branch-batches#req:batch-source-pins

**Given** a source whose lane claim was made by session S and whose review
comment declares session S
**When** `wb pr batch create` runs
**Then** it refuses `batch-source-self-review`.

### AC: clean-batch-one-pr-empty-delta

**Requirements:** integration-branch-batches#req:batch-construction, integration-branch-batches#req:batch-state-on-origin, integration-branch-batches#req:batch-delta

**Given** two reviewed conflict-free PRs
**When** `wb pr batch create` runs twice
**Then** origin has one `public-demo-batch-1` whose two first-parent merge
commits have the pinned heads as second parents, one PR into `public-demo`
whose body block lists both, no force-push occurred, the second run changed
nothing, and `wb pr batch delta` prints `integration delta: empty`.

### AC: delta-ignores-source-commits-and-shows-resolution

**Requirements:** integration-branch-batches#req:batch-conflict-stop-resume, integration-branch-batches#req:batch-delta

**Given** three sources where the second conflicts with the first in `a.go`
and each source has its own non-merge commits
**When** `create` stops with `batch-conflict` listing `a.go`, the conflict is
resolved and committed, and `resume` runs
**Then** the third source merges, the second is `resolved`, and `delta` shows
only the resolution hunk in `a.go` — none of the sources' own commits;
**and** a resume after a commit whose parents are not `[batch head, pin]`
refuses `batch-resolution-invalid`.

### AC: fix-commit-appears-in-delta

**Requirements:** integration-branch-batches#req:batch-delta

**Given** a clean batch to which the operator adds one non-merge fix commit
**When** `delta` runs
**Then** it shows exactly that commit as `kind: fix`.

### AC: add-refreshes-target-then-merges

**Requirements:** integration-branch-batches#req:batch-add-and-refresh

**Given** an open batch and a target that advanced by one commit
**When** `wb pr batch add <batch> '<n>@<sha>=<url>'` runs
**Then** the branch gains a target-refresh merge then a source merge, as a
fast-forward of its previous head, the same PR's body lists the new source,
and adding to a merged batch refuses `batch-closed`.

### AC: batch-land-binds-review-and-refuses-update-branch

**Requirements:** integration-branch-batches#req:batch-landing

**Given** a batch PR
**When** `wb pr land` runs (a) with no `--approved-by`, (b) with a review of
the current head while the batch is behind the target, (c) with a review of
head H followed only by an empty-delta target refresh
**Then** (a) refuses `batch-delta-unreviewed` before auto-merge is armed,
(b) refuses `batch-behind-target` naming `wb pr batch add`, (c) lands with a
merge commit and every source PR ends merged, closed with a comment, or —
when its head advanced after the pin — open with a comment.

### AC: non-merge-targets-refused

**Requirements:** integration-branch-batches#req:batch-refuses-non-merge-targets

**Given** a repository that disallows merge commits, and separately a target
with a merge queue
**When** `wb pr batch create` runs
**Then** it refuses `batch-merge-commits-disabled` or
`batch-target-merge-queue`; and `wb pr land <batch-pr> --merge-method squash`
refuses `batch-merge-method`.

### AC: hand-pushed-merge-is-unrecorded

**Requirements:** integration-branch-batches#req:batch-merge-provenance

**Given** a batch to which someone pushed a clean `git merge <unreviewed-sha>`,
and separately a batch whose body block names a pin whose review comment was
edited after the batch was built
**When** `wb pr batch delta` and `wb pr land` run
**Then** delta prints each such merge in full as `kind: unrecorded`, and
landing refuses `batch-unrecorded-merge` naming the commit;
**and given** the body block's `base` was edited to that hand-pushed merge
(not an ancestor of `origin/<target>`), delta and landing refuse
`batch-base-invalid`.

### AC: refresh-only-add-and-refresh-conflict

**Requirements:** integration-branch-batches#req:batch-add-and-refresh, integration-branch-batches#req:batch-conflict-stop-resume

**Given** an open batch whose target advanced with a change conflicting with
the batch in `b.go`
**When** `wb pr batch add <batch>` runs with no pins
**Then** it stops with `batch-conflict` listing `b.go`; after a committed
resolution `wb pr batch resume` records the refresh as `resolved`, and delta
shows only that resolution.

### AC: concurrent-add-never-force-pushes

**Requirements:** integration-branch-batches#req:batch-state-on-origin

**Given** two clones adding to the same batch at once
**When** the second push is rejected as non-fast-forward
**Then** that clone refuses `batch-moved`, origin keeps the first clone's
head, and a rerun on the second clone rebuilds from origin and succeeds.

### AC: batch-continues-on-another-machine

**Requirements:** integration-branch-batches#req:batch-state-on-origin

**Given** a batch created on one clone
**When** `wb pr batch add` runs on a second clone with no local receipt
**Then** it rebuilds the state from origin and adds the source.

### AC: failures-attributed-without-new-ci

**Requirements:** integration-branch-batches#req:batch-failure-attribution

**Given** a batch head where `test` failed (passed on base, failed on source
#2's pin), `lint` failed (failed on base), `e2e` failed (passed everywhere
else), and `build` failed with no run on source #1's pin
**When** `wb pr batch status` runs
**Then** it reports `test: source #2`, `lint: pre-existing`, `e2e:
integration`, `build: unattributed`, and dispatches no workflow; when `test`
also failed on source #3's pin, it reports `test: source #2, #3`.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
