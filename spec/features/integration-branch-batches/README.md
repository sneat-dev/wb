---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Integration Branch Batches

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-batches?op=request-change) |
**Status:** Draft
**Source Ideas:** integration-branch-batch-landing
**Depends On:** [Mechanical Worktree Merge](../mechanical-worktree-merge/README.md), [Worktree Lifecycle](../worktree-lifecycle/README.md)

## Summary

Register integration branches with a landing policy, batch reviewed lane PRs into one integration PR, and waive only what that policy allows.

## Problem

Parallel agent lanes that target one shared integration branch each pay a full
CI run and an up-to-date-fence refresh per landing, and every landing moves
the integration tip under the next lane. When runners are backlogged this
serializes the whole effort. The workaround the founder ruled on — merge the
approved lane heads, exactly as reviewed, into one batch branch and land one
PR — had to be done by hand, because WB's merge machinery only takes local
worktrees, its waiver refuses pending, absent and aggregator checks, nothing
shows a reviewer only the conflict resolutions, and nothing records which
lane a batch failure belongs to. See the
[source Idea](../../ideas/integration-branch-batch-landing.md) for the
DataTug public-demo context.

## Behavior

### Journey

1. An operator registers `public-demo` once: its gate, where it promotes to,
   and the promote gate (`wb integration create`).
2. Each lane is created against it (`wb create <task> <repo> --integration
   public-demo`), opens its PR into it by default, and receives an
   independent review comment ending `Reviewed-Head: <sha>`.
3. The orchestrator batches the reviewed lanes (`wb pr batch create
   datatug/datatug-apps --target public-demo #510@3b32619c #512@64ed2cec`).
   WB verifies every pin and review, merges each pinned head with a merge
   commit onto a batch branch off the integration tip, pushes it, and opens
   ONE PR whose body is the receipt. A conflict stops the batch with the
   files listed; after a committed resolution `wb pr batch resume` continues.
4. A reviewer reads only `wb pr batch delta` and posts a review ending
   `Reviewed-Head: <batch-head>`.
5. `wb pr land` lands the batch PR with a merge commit under the integration
   gate, applying only the waivers REQ waiver-* permit. A red batch run is
   attributed to a source PR, the baseline, or the integration itself.
6. Source PRs are recorded merged, and their worktrees retire against the
   integration branch.
7. `wb integration promote public-demo` opens or updates the integration →
   `main` PR, which lands only under the promote gate.

### REQ: waiver-settles-pending-checks

`wb pr land --waive-check <name>` MUST NOT judge a waived check while it is
queued or in progress on the PR head. The waiver is evaluated only after the
wait reaches a terminal state for every non-waived check AND every waived
check has concluded. When the wait ends because only waived checks are still
running, the result is `checks_pending` with refusal code
`waived-check-pending`, naming each such check and the resume command — never
`waived-check-not-failed`. Waiver verification MUST NOT run on a wait result
that is still pending.

### REQ: waiver-pinned-baseline

`wb pr land` MUST accept `--waive-baseline <sha>`. When given, a waived check
is judged against that commit's check run instead of the target tip's. The
baseline MUST be the target tip or a first-parent ancestor of it (verified
through the GitHub compare API), and its run of the waived check MUST be
terminal with the same failing conclusion as the PR head. When the target tip
has no run of the check, or its run is not terminal, the refusal
(`waived-check-absent-on-target` or the new `waived-check-hung-on-target`)
MUST name, as its sanctioned command, the nearest first-parent ancestor
within 20 commits that has a terminal run of that check with the PR head's
conclusion, or say none was found. The receipt records `baseline_sha` and
whether it was the tip.

### REQ: waiver-aggregator

`wb pr land` MUST accept `--waive-aggregate <check>=<input>[,<input>...]`
(repeatable). An aggregator check (for example `deploy-gate`) is waivable when
every named input is itself waived in the same invocation, the aggregator
failed, was cancelled, or was skipped on the PR head, and the aggregator is
not required by target policy. It needs no baseline of its own. The receipt
records the aggregator, its inputs, and that it was waived by aggregation.

### REQ: waiver-never-waives-required

No waiver flag, baseline, aggregation, or integration policy MAY waive a
check that server-enforced target policy requires, or a check in the
registered integration gate (REQ integration-registry). The existing refusal
`waived-check-required` applies to all of them, including an aggregator and
each of its inputs.

### REQ: waiver-unfenced-target

On a target with no server-enforced required checks, a waiver MUST be
evaluable when either the target is a registered integration branch (its gate
is the declared required set and the waived check is not in it) or the
invocation also passes `--allow-unfenced`. Otherwise the existing
`waived-check-target-unfenced` refusal stands. The receipt records which of
the two authorized the evaluation.

### REQ: batch-source-pins

`wb pr batch create <owner/repo> --target <branch> <#pr@sha>...` MUST, for
every source, before any Git mutation: resolve the pin to the PR's exact
current 40-character head (a short pin must be a unique prefix of it); refuse
`batch-source-head-moved` when the PR head differs from the pin; refuse
`batch-source-wrong-base` when the PR's base is not `--target`; and find a
review comment on that PR whose `Reviewed-Head:` line equals the pinned head
and whose reviewer identity is not a self-review under the rules `wb pr land
--approved-by` already applies, else refuse `batch-source-unreviewed`. All
refusals are reported together, and no batch is created when any source is
refused.

### REQ: batch-construction

A batch MUST be built in a WB-managed integration worktree on branch
`<target>-batch-<n>` (the next free `n` on origin) cut from the fetched
target tip, reusing the merge-prepare candidate machinery with a new source
kind `pull_request` that fetches `refs/pull/<n>/head` and verifies the pinned
SHA. Sources are merged in the order given, each with `git merge --no-ff`
and the message `Merge #<n> (<short-sha>) into <batch-branch>`, so every
pinned head is a parent of exactly one batch merge commit. WB MUST never
rebase, squash, amend or force-push a batch branch, and MUST never modify a
source branch.

### REQ: batch-conflict-stop-resume

When a source merge conflicts, WB MUST leave that merge in progress in the
batch worktree, record the source as `conflicted` with the conflicting paths
in the receipt, list the paths and the batch worktree path, and exit `1` with
refusal `batch-conflict`. `wb pr batch resume <batch>` MUST continue only when
the batch worktree is clean and its HEAD is a merge commit whose parents are
exactly the previous batch head and the pinned source head; it then records
the source as `resolved` and continues with the next source.

### REQ: batch-receipt-and-pr

Every batch MUST have one receipt under the WB private state (the same store
as merge-prepare receipts) recording repository, target, target SHA at
creation, batch branch, each source's PR number, pinned head, review comment
URL, reviewer identity, merge commit SHA and state (`merged`, `conflicted`,
`resolved`), fix commits, and the batch PR. WB MUST push the batch branch and
open exactly one PR into the target, with merge method `merge`, whose title
names the batch and whose body is generated from the receipt (one row per
source, plus the delta summary). Every later mutation regenerates the body.
Re-running a completed step MUST NOT create a second PR or a duplicate merge.

### REQ: batch-add

`wb pr batch add <batch> <#pr@sha>...` MUST apply REQ batch-source-pins to
the new sources and merge them onto the existing batch head as further merge
commits, push without force, and regenerate the PR body. Adding to a batch
whose PR is merged or closed is refused `batch-closed`. Adding when the
target advanced is allowed; WB merges the target tip into the batch branch
first, as its own recorded merge commit, before the new sources.

### REQ: batch-delta

`wb pr batch delta <batch>` MUST print only the integration delta: for every
batch merge commit, the diff from the tree `git merge-tree --write-tree`
produces for its two parents to the committed tree, emitted only when that
diff is non-empty; plus the full diff of every non-merge commit on the batch
branch since its target base. Content brought in unchanged from a pinned
source head or the target MUST NOT appear. `--format json` lists each delta
part with its commit, kind (`resolution`, `fix`) and paths. An empty delta
prints `integration delta: empty` and exits `0`.

### REQ: batch-landing-review

`wb pr land` on a batch PR MUST require a review comment whose
`Reviewed-Head:` equals the batch PR's current head and that is not a
self-review; otherwise it refuses `batch-delta-unreviewed`, naming `wb pr
batch delta <batch>` as the sanctioned command. It lands with a merge commit,
never a squash or rebase. After landing, WB records in the receipt whether
GitHub marked each source PR merged; a source PR that is still open has a
comment added linking the batch PR and is reported, never force-closed.

### REQ: integration-registry

`wb integration create <owner/repo> <branch> --gate <check>[,...]
--promote-to <branch> --promote-gate <check>[,...]|all` MUST record the
policy (gate, promote-to, promote-gate, creator, time) under the custom ref
`refs/wb/integrations/<branch>` on origin, as a commit whose tree holds one
`policy.yaml`, so every machine reads the same policy with one fetch and no
tracked file enters any branch. It creates the integration branch from
`--promote-to` when it does not exist. `wb integration show|list` read it.
Re-running with identical arguments is a no-op; changing a policy requires
`--update` and records the previous policy commit as parent.

### REQ: integration-worktree-target

`wb create <task> <repo> --integration <branch>` (and `wb worktree create`)
MUST refuse an unregistered branch, cut the worktree from
`origin/<branch>`, and record `landing_target: <branch>` in the Work Log
claim. For such a worktree: `wb pr create` targets the landing target by
default; `wb land` / `wb pr land` on its PR, when that PR has a current
independent Reviewed-Head, enrolls it into the repository's open batch for
that target (creating one when none is open) instead of merging it directly;
cleanup proves absorption against `origin/<landing-target>`; and `wb worktree
summary` shows the lane state as one of `unreviewed`, `reviewed`, `in batch
<n>`, `landed in <target>`, `promoted to <promote-to>`.

### REQ: integration-promote

`wb integration promote <owner/repo> <branch>` MUST open, or update, the one
PR from the integration branch to its promote-to branch, with merge method
`merge`, and land it only under the promote gate: with `all`, every observed
check on the PR head must pass and no waiver flag is accepted; with a list,
each listed check must pass and waivers obey REQ waiver-*. Promotion records
the promoted integration SHA in the integration ref so `wb worktree summary`
can show `promoted`.

### REQ: batch-failure-attribution

When a batch PR's checks fail, `wb pr batch status <batch>` (and the batch PR
body) MUST classify every failed check as `pre-existing` (it fails on the
batch's target base SHA), `source #<n>` (it fails on that source PR's own run
at its pinned head and passes on the base), `integration` (it passes on the
base and on every source's pinned-head run), or `unattributed` (a needed run
is absent or non-terminal), reading only existing check runs and starting no
CI.

## Acceptance Criteria

### AC: pending-waived-check-is-waited-not-refused

**Requirements:** integration-branch-batches#req:waiver-settles-pending-checks

**Given** a PR whose only non-green check is waived `deploy-gate`, still in
progress when the wait slice ends
**When** `wb pr land <pr> --waive-check deploy-gate --waive-reason r` runs
**Then** the result is `checks_pending` with refusal `waived-check-pending`
naming `deploy-gate` and a resume command, and no waiver verdict is recorded;
**and when** the same check later concludes `failure` and the target tip has
the same failure, a rerun lands and records the waiver.

### AC: absent-target-run-uses-pinned-baseline

**Requirements:** integration-branch-batches#req:waiver-pinned-baseline

**Given** a waived check that failed on the PR head, has no run on the target
tip, and failed with the same conclusion on the tip's first-parent parent
**When** `wb pr land` runs without `--waive-baseline`
**Then** it refuses `waived-check-absent-on-target` with a sanctioned command
carrying `--waive-baseline <parent-sha>`;
**and when** that command runs, it lands and the receipt records the baseline
SHA as not the tip.

### AC: baseline-off-target-history-is-refused

**Requirements:** integration-branch-batches#req:waiver-pinned-baseline

**Given** `--waive-baseline` naming a commit that is not a first-parent
ancestor of the target tip
**When** `wb pr land` runs
**Then** it refuses with a reason naming the baseline and target, and does not
merge.

### AC: aggregator-waived-by-its-inputs

**Requirements:** integration-branch-batches#req:waiver-aggregator, integration-branch-batches#req:waiver-never-waives-required

**Given** a PR where `e2e` fails as it does on the target tip and the
aggregator `deploy-gate` fails because of it, neither required
**When** `wb pr land` runs with `--waive-check e2e --waive-aggregate
deploy-gate=e2e`
**Then** it lands and the receipt records `deploy-gate` waived by aggregation
of `e2e`;
**and given** `deploy-gate` is required by target policy, the same command
refuses `waived-check-required`.

### AC: integration-gate-authorizes-waiver-on-unfenced-target

**Requirements:** integration-branch-batches#req:waiver-unfenced-target, integration-branch-batches#req:waiver-never-waives-required

**Given** a registered integration branch with gate `preflight,build,test`
and no server-enforced required checks
**When** a PR into it waives `lint`, which fails identically on the tip
**Then** the waiver is evaluated and recorded as authorized by the
integration gate;
**and when** the PR instead waives `build`, it refuses
`waived-check-required`;
**and given** an unregistered unfenced target without `--allow-unfenced`, it
refuses `waived-check-target-unfenced`.

### AC: batch-refuses-moved-or-unreviewed-pins

**Requirements:** integration-branch-batches#req:batch-source-pins

**Given** three PRs into `public-demo`: one whose head moved past its pin, one
whose only review names an older head, one cleanly reviewed
**When** `wb pr batch create` names all three
**Then** it exits `1` reporting `batch-source-head-moved` and
`batch-source-unreviewed` together, creates no branch, worktree, receipt or
PR, and leaves every source branch unchanged.

### AC: clean-batch-has-one-pr-and-empty-delta

**Requirements:** integration-branch-batches#req:batch-construction, integration-branch-batches#req:batch-receipt-and-pr, integration-branch-batches#req:batch-delta

**Given** two reviewed, conflict-free PRs pinned at their heads
**When** `wb pr batch create` runs twice
**Then** origin has one `public-demo-batch-1` whose two merge commits have the
pinned heads as second parents, one PR into `public-demo` with a body listing
both pins and reviewers, no force-push occurred, the second run changed
nothing, and `wb pr batch delta` prints `integration delta: empty`.

### AC: conflict-stops-and-resume-continues

**Requirements:** integration-branch-batches#req:batch-conflict-stop-resume, integration-branch-batches#req:batch-delta

**Given** a batch whose second source conflicts with the first in `a.go`
**When** `wb pr batch create` runs
**Then** it exits `1` with `batch-conflict` listing `a.go` and the batch
worktree, and the receipt marks the source `conflicted`;
**and when** the conflict is resolved and committed and `wb pr batch resume`
runs, the third source merges, the receipt marks the second `resolved`, and
`wb pr batch delta` shows only the resolution hunk in `a.go`;
**and** resume after an amended commit whose parents differ is refused.

### AC: add-extends-without-force-push

**Requirements:** integration-branch-batches#req:batch-add

**Given** an open batch PR and a newly reviewed PR
**When** `wb pr batch add <batch> #n@sha` runs
**Then** the batch branch gains one merge commit as a fast-forward of its
previous head, the same PR's body lists the new source, and adding to a
merged batch refuses `batch-closed`.

### AC: batch-land-requires-delta-review

**Requirements:** integration-branch-batches#req:batch-landing-review

**Given** a batch PR whose head has no independent `Reviewed-Head`
**When** `wb pr land` runs on it
**Then** it refuses `batch-delta-unreviewed` naming `wb pr batch delta`;
**and when** an independent review of the current head exists, it lands with
a merge commit and the receipt records every source PR's merged state.

### AC: integration-policy-is-shared-through-a-ref

**Requirements:** integration-branch-batches#req:integration-registry

**Given** a fixture origin
**When** `wb integration create o/r public-demo --gate build,test
--promote-to main --promote-gate all` runs on one clone
**Then** a second clone's `wb integration show o/r public-demo` prints that
policy, no branch's tree contains `policy.yaml`, and a rerun with the same
arguments makes no new commit.

### AC: integration-lane-targets-and-enrolls

**Requirements:** integration-branch-batches#req:integration-worktree-target

**Given** a registered `public-demo`
**When** `wb create t o/r --integration public-demo` runs, its PR is opened
with `wb pr create`, reviewed, and `wb pr land` is run on it
**Then** the worktree was cut from `origin/public-demo`, the PR's base is
`public-demo`, the PR is enrolled into an open batch rather than merged, and
`wb worktree summary` shows `in batch 1`;
**and** `--integration` naming an unregistered branch is refused before any
worktree exists.

### AC: promote-all-gate-accepts-no-waiver

**Requirements:** integration-branch-batches#req:integration-promote

**Given** `public-demo` with promote gate `all`
**When** `wb integration promote o/r public-demo --waive-check e2e` runs
**Then** it exits `2` as a usage error;
**and without** the waiver it opens one PR to `main`, lands it only once every
check passed, and `wb worktree summary` shows promoted lanes.

### AC: batch-failure-is-attributed

**Requirements:** integration-branch-batches#req:batch-failure-attribution

**Given** a batch whose `test` fails, where `test` passed on the base and
failed on source #2's pinned-head run, and whose `lint` failed on the base
**When** `wb pr batch status <batch>` runs
**Then** it reports `test: source #2`, `lint: pre-existing`, and starts no CI.

## Delivery order

Small PRs, in value order: (1) waiver REQs; (2) batch create, pins,
construction, conflict/resume, receipt and PR; (3) delta and landing review,
add; (4) integration registry and `--integration` worktrees; (5) promote;
(6) failure attribution.

## Open Questions

- Should an empty integration delta land without a batch Reviewed-Head, on
  WB's mechanical proof alone? Until decided, REQ batch-landing-review
  requires one.
- Should a non-required check hung on the PR head itself (not on the target)
  be waivable after a bounded settle window? Not in this Feature.

---
*This document follows the https://specscore.md/feature-specification*
