---
format: https://specscore.md/idea-specification
status: Specifying
---

# Idea: Integration-Branch Batch Landing

**Status:** Specifying
**Date:** 2026-10-22
**Owner:** alex
**Promotes To:** pr-land-waivers, integration-branch-batches, integration-branch-registry
**Supersedes:** —
**Related Ideas:** extends:mechanical-worktree-merge, extends:efficient-agent-pipeline, extends:ci-validation-reuse

## Problem Statement

How might WB land several independently reviewed lane PRs through one shared integration branch in one CI run, without re-reviewing their content or waiving anything a target actually requires?

## Context

The DataTug public-demo effort (datatug/datatug-apps, 2026-10) ran several
agent lanes in parallel. Every lane PR targeted the integration branch
`public-demo`, whose ruleset requires preflight, build and test, and every
lane PR carried an independent review comment ending `Reviewed-Head: <sha>`.
Runners were backlogged, so landing each lane separately stalled: each landing
moved the integration tip, invalidated the next lane's up-to-date fence, and
queued another full CI run. The founder ruled: merge the approved lane heads,
exactly as reviewed and with merge commits, into ONE batch branch off
`public-demo`, then open ONE PR into `public-demo`; `public-demo` → `main` is
gated on full e2e. That was done by hand (batch branch `public-demo-batch-1`,
datatug/datatug-apps#520 merging #510, #512, #514 and #508 at their reviewed
heads), with a reviewer checking only the conflict resolutions.

The friction, all inside WB:

- `wb worktree create --base` records a starting branch, not a landing target
  with a policy.
- `wb worktree merge prepare` builds multi-source candidates (`--target`,
  `--rebatch-receipt`), but its sources are local worktrees. It cannot take a
  PR pinned at its reviewed head or verify review evidence.
- `wb pr land --waive-check` refuses a check still pending on the PR, refuses
  a check absent or hung on the target tip (deploy-gate never ran on
  `public-demo`), cannot waive an aggregator whose inputs are all waived, and
  is impossible when the target has no server-enforced required checks.
- Nothing produces the integration delta (conflict resolutions and
  integration fixes only), nothing attributes a batch CI failure to a source
  PR, and nothing retires source worktrees against the integration branch.

## Recommended Direction

Make the integration branch a first-class WB object with a policy (its gate,
where it promotes to, and the promote gate), and make a batch a first-class,
receipted object built by the existing merge-prepare machinery from a new
source kind: a pull request pinned at its independently reviewed head. A batch
is one branch off the integration tip, one merge commit per source (never a
rebase, never a force-push), and exactly one PR whose body is generated from
the receipt. The only thing a batch reviewer reads is the integration delta:
the difference between each committed merge and the conflict-free mechanical
merge of its parents, plus any non-merge commit on the batch branch. Content
already reviewed at a pinned head is never shown again.

Fix the `wb pr land` waiver first, because it is what blocked landing even
when the batch was built by hand: settle pending checks before judging them,
accept a pinned baseline on the target's first-parent history when the
target tip's run is absent or hung, derive an aggregator's waiver from its
waived inputs, and judge waivers on an unfenced target only under
`--allow-unfenced` without arming auto-merge — while keeping "never waive a required check" as an
absolute rule.

Everything reuses existing evidence code: review binding from `wb pr land`
(`Reviewed-Head`, reviewer identity, self-review detection), candidate
construction and receipts from `wb worktree merge prepare`, check observation
from `githubchecks`, and landing from `wb pr land`.

## Alternatives Considered

- Land each lane separately and buy more runners — lost: the cost is not
  only runner minutes but the serialized fence invalidation, which turns N
  lanes into N full CI runs plus N rebases even with infinite runners.
- GitHub merge queue on the integration branch — lost: it squashes or
  rebases into its own groups, loses the "exactly the reviewed head" binding,
  and is not available on every plan or repository WB manages.
- Rebase every lane onto one branch and re-review the result — lost: it
  rewrites reviewed commits, so every lane's review is void and the reviewer
  must read all of the content again.

## MVP Scope

The waiver repairs in `wb pr land`, then `wb pr batch` (create, add, resume,
delta, land) over pinned, independently reviewed PRs, ending with one landed
batch PR whose receipt names every source pin and whose delta was reviewed.
Integration registration, `--integration` worktrees, promotion and failure
attribution follow in later small PRs.

## Not Doing (and Why)

- Merge queue integration — GitHub's queue rewrites heads; a batch exists to keep reviewed heads exact.
- Cross-repository batches — one batch is one repository and one integration branch; multi-repository work stays a stream.
- Waiving a required check under any flag — a required check is the target's own contract; WB never overrides it.
- Automatic conflict resolution — WB stops and lists files; a person or agent resolves and commits, and the reviewer reads that resolution.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | GitHub marks a source PR merged when its exact head becomes reachable from its base through a merge commit. | Land a two-source fixture batch; read both source PRs' `merged` state. |
| Must-be-true | `git merge-tree --write-tree` of a merge commit's parents reproduces the conflict-free tree, so the committed tree's difference from it is exactly the resolution. | Fixture test with one clean and one conflicted merge; the delta of the clean merge is empty. |
| Should-be-true | One batch CI run costs less wall time and runner time than N lane runs plus N fence refreshes. | Record CI minutes and elapsed time for public-demo-batch-1 vs the stalled per-lane attempts. |
| Should-be-true | A custom ref (`refs/wb/integrations/<branch>`) is a durable cross-machine home for integration policy on GitHub; safety never depends on it because the gate is server-enforced. | Push and fetch one from a fixture repository and a real one before the registry Feature is implemented. |

## SpecScore Integration

- **New Features this would create:** pr-land-waivers, integration-branch-batches, integration-branch-registry
- **Existing Features affected:** mechanical-worktree-merge (new pinned pull-request source kind), worktree-lifecycle (landing target recorded at creation)
- **Dependencies:** none

## Open Questions

- Should an empty integration delta (no conflicts, no fix commits) land
  without a batch Reviewed-Head, on WB's mechanical proof alone?
