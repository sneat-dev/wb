---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Canonical Clone Branch Independence

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-clone-branch-independence?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-clone-branch-independence?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-clone-branch-independence?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-clone-branch-independence?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Landing, cleanup, worktree creation and the managed Git hooks never depend on
which branch the canonical clone has checked out, and a landing whose merge
succeeded but whose follow-up did not finish exits with its own status and
prints the exact resume command.

## Problem

On 2026-10-02 `wb pr land sneat-co/sneat-core-modules#271` merged and the merge
was verified on the remote, then its cleanup failed and the verb exited 1: the
canonical clone was on another agent's branch, and the managed pre-push guard
refused wb's own branch-deletion push with "canonical clone … is on
`feat/coverage-100`; it must stay on `main`". The merged branch and the task's
worktree were left behind, and exit 1 reads as "not landed". The founder ruled
that this is fixed on the wb side: cleanup must not be blocked because of the
canonical clone's state, and checking out a different branch on the canonical
clone is now a normal situation (sneat-dev/wb#824).

## Behavior

The canonical clone's checked-out branch is not a policy concern. What WB
protects there is data: uncommitted work. Where a guard still inspects the
clone, it refuses a dirty tree and a detached HEAD (a commit there is reachable
from no branch), and never the branch name alone.

A `git push` that only deletes remote refs sends nothing from the checkout it
runs in, so the managed pre-push guard passes it without inspecting the
checkout, whatever the clone's branch or state. That is the push `wb pr land`,
`wb worktree land` and `wb worktree cleanup` use to retire a merged branch.

A landing whose merge is verified on the base branch but whose canonical sync,
branch retirement or cleanup did not finish is not "not landed". `wb pr land`,
`wb pr create --land` and `wb worktree land` exit 3, say that the change is
landed, name the failing step, and print the exact resume command (`wb pr land
<owner/repo#n>` on the merged pull request, `wb worktree merge resume
<receipt>`), which finishes the tail.

### Audit of every on-base enforcement point

| Place | What it did | Now |
|---|---|---|
| `worktrees.Guard`, canonical branch check (`internal/worktrees/worktrees.go`) | refused a canonical clone not on the base branch | removed; dirty tree and detached HEAD still refuse |
| managed `worktree-guard` hook, pre-push (`internal/hooks/config.go`) | ran the guard on every push | a delete-only push passes without inspecting the checkout |
| managed `worktree-guard` hook, post-checkout | warned "outside WB's managed worktree hierarchy" on any branch switch in the clone | silent for a clean clone on any branch |
| `worktrees.Guard` freshness receipt | compared HEAD to `origin/<base>` and warned "diverged" off the base branch | computed only when the clone is on the base branch |
| `wb worktree guard` help, `.worktree.md` canonical body, `AGENTS.md`, `wb-fleet` sync skill | said the clone must stay on the base branch | say it must stay clean; its branch is not policed |
| `syncCanonicalMergeTarget` (landing's canonical sync) | already skips with `not_checked_out` when the clone is on another branch | unchanged; a dirty clone on the target branch still blocks the fast-forward (data protection) |
| `wb worktree create` | already fetches `origin/<base>` and never needs the base checked out | unchanged |
| agent pre-tool-use hook (`internal/agentguard`) | refuses agent writes inside a canonical clone | unchanged: it protects uncommitted data, not a branch name |
| `wb sync` | pushes and pulls the checked-out branch fast-forward only | unchanged: it reports unpushed or diverged work, never a branch name |

## Requirements

#### REQ: guard-ignores-the-canonical-branch

`wb worktree guard` and every managed hook that runs it MUST accept a clean
canonical clone on any named branch. A dirty canonical clone and a detached HEAD
MUST still be refused. The canonical freshness receipt MUST be computed only
when the clone is on the base branch.

#### REQ: delete-only-push-passes-the-guard

The managed pre-push guard MUST pass a push whose every ref update deletes a
remote ref without inspecting the checkout: whatever the canonical clone's
branch, clean or dirty. A malformed or unreadable ref list, an interactive
invocation and any push that updates a ref MUST keep the full guard.

#### REQ: cleanup-independent-of-the-canonical-branch

`wb pr land`, `wb worktree land` and `wb worktree cleanup` MUST delete the
merged remote branch, retire the worktree and release the claim when the
canonical clone has any branch checked out, clean or dirty, and MUST leave the
clone's branch and uncommitted work unchanged.

#### REQ: landed-incomplete-exit-status

A landing whose merge is verified on the base branch but whose canonical sync,
branch retirement or cleanup failed MUST exit 3 (never 1), report outcome
`landed-incomplete` with the failing step, and print `resume with: <command>`
naming the exact command that finishes it; running that command MUST complete
the cleanup. Exit code 3 MUST be documented in the root help.

## Acceptance Criteria

### AC: guard-accepts-any-branch-and-refuses-dirty

**Requirements:** canonical-clone-branch-independence#req:guard-ignores-the-canonical-branch

**Verifies:** `TestGuardAcceptsAnyBranchInACanonicalCloneAndRejectsChanges`,
`TestGuardCanonicalRefusesFailedQueriesAndUnsafeState`, and the freshness test
`TestGuardCanonicalFreshnessIsNotReportedOffTheBaseBranch`.

### AC: deletion-push-from-a-canonical-clone-on-another-branch

**Requirements:** canonical-clone-branch-independence#req:delete-only-push-passes-the-guard

With real Git hooks and the built binary, a lease-checked deletion push from a
canonical clone on another branch, clean or dirty, succeeds; publishing the
branch from a dirty clone is still refused. **Verifies:**
`TestPrePushGuardInACanonicalCloneOnAnotherBranch`,
`TestCheckoutOfAnotherBranchInACanonicalCloneRaisesNoGuardWarning`,
`TestOnlyRemoteRefDeletionsAcceptsAPushThatSendsNothingFromTheCheckout`.

### AC: landing-cleanup-with-the-canonical-clone-on-a-feature-branch

**Requirements:** canonical-clone-branch-independence#req:cleanup-independent-of-the-canonical-branch

**Verifies:** `TestLandCleansUpWhileTheCanonicalCloneIsOnAnotherBranch` (clean and
dirty; remote branch deleted, worktree retired, task released, canonical branch
and uncommitted work untouched) and
`TestLandedWorktreeIsRetiredWhileTheCanonicalCloneIsOnAnotherBranch` (real hooks,
real binary).

### AC: landed-but-incomplete-has-its-own-exit-status

**Requirements:** canonical-clone-branch-independence#req:landed-incomplete-exit-status

**Verifies:** `TestLandWhoseCleanupFailedReportsLandedIncompleteAndResumes`,
`TestCreateLandMapsALandedIncompleteLandingToItsOwnOutcome`,
`TestWorktreeLandExitsDistinctlyWhenTheMergeLandedButTheTailDidNot`,
`TestRootHelpDocumentsTheLandedIncompleteExitCode`.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
