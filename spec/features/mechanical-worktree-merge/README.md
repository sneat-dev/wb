---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: Mechanical Worktree Merge

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/mechanical-worktree-merge?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/mechanical-worktree-merge?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/mechanical-worktree-merge?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/mechanical-worktree-merge?op=request-change) |
**Status:** Stable
**Source Ideas:** mechanical-worktree-merge

## Summary

Prepare a WB-managed integration candidate and mechanically land it through a verified direct or pull-request route, with resumable receipts, safe recovery, and optional cleanup.

## Problem

WB already has the individual safety primitives for managed worktrees, exact
remote target fetching, required-check observation, pull-request receipts, and
cleanup. It does not compose them into a landing command. Agents therefore
spend context and tokens repeatedly performing the same mechanical sequence,
and interrupted attempts leave no single receipt that says which phase is safe
to resume.

The missing composition also delays dependent agents. They cannot consume a
validated local integration candidate while GitHub checks the eventual landing
route, so independent branches either wait or guess which moving branch to use.

## Behavior

### Journey

The operator names one or more ready WB source worktrees and runs one command.
WB fetches the repository's exact remote target (the remote default branch when
`--target` is omitted), creates or resumes a dedicated WB-managed integration
worktree, and mechanically merges every source in the requested order.

**Observable good result at prepare:** the source worktrees and canonical clone
are unchanged; the candidate worktree is clean; its recorded commit contains
the fetched target and every recorded source head; and other agents can rebase
onto the immutable candidate SHA while the operator does nothing else.

WB then lands the same candidate through `--route auto`, `direct`, or `pr`.
With `auto`, direct landing is selected only when authoritative repository
policy permits it without administrator bypass; uncertainty selects a pull
request. WB pushes the candidate when necessary, derives the pull-request title
and body from the candidate commits, waits in bounded foreground slices for an
exact-head receipt, and updates the remote target. If the target advanced after
prepare, WB first rebases the isolated, unpublished candidate onto the exact new
target and reruns validation. A rebase conflict aborts cleanly with sources
untouched. WB never rewrites an already-published candidate with force-push.

**Observable good result at land:** `origin/<target>` contains the exact landing
receipt; required post-target checks have terminated successfully; a clean
canonical checkout already on the target is fast-forwarded to the same SHA; and
no unrelated checkout was switched, stashed, reset, or overwritten.

The journey has two explicit epilogues. Without `--cleanup`, all managed source
and candidate assets remain and the result is `landed_cleanup_pending`. With
`--cleanup`, WB removes only assets proved absorbed by the exact remote target
and the result is `complete`. If anything fails before landing, every source is
preserved and rerunning resumes from the journaled phase. If target or
post-target CI fails after landing, WB preserves the before/after target receipt
and can prepare a forward revert candidate; it never rewrites remote history.
If the same source instead advances additively with a forward repair, prepare
proves the target still contains the failed landing, fast-forwards the retained
candidate to that target, appends the failed attempt to the receipt, and lands
the repair through a fresh route and pull request.

### Command surface

```text
wb worktree merge <source-worktree...> [--target <branch>]
  [--route auto|direct|pr] [--cleanup] [--on-failure stop|revert]

wb worktree merge prepare <source-worktree...> [--target <branch>]
wb worktree merge land <candidate-worktree-or-receipt>
  [--route auto|direct|pr] [--cleanup] [--on-failure stop|revert]
wb worktree merge resume <candidate-worktree-or-receipt>
wb worktree merge revert <landing-receipt> [--route auto|direct|pr]
```

Bare `wb worktree merge` composes prepare and land. `prepare` is the deliberate
pause that unblocks dependent agents. `land` and `resume` consume the persisted
receipt and do not reconstruct state from branch naming.

WB's AI skill surfaces treat merge as the normal repeated completion
counterpart to worktree creation. The worktree creation skill and built-in
`wb worktree create --help` point agents directly to the one-command journey;
merge, land, integrate, finish, deliver, main/target push, PR, checks, cleanup,
resume, and revert intent all route to the merge/worktree skills. Every new
managed worktree also receives a locally ignored `.worktree.md` reminder with
the one-command, two-phase, resume, and forward-revert paths; a repository-owned
file with that name is preserved unchanged.

### Safety and state

- One exclusive local merger lane exists per repository and remote target.
- The integration worktree owns a distinct integration branch; local `main` or
  another final target is never allowed to become an unpushed candidate.
- The receipt records repository identity, target ref and fetched SHA, ordered
  source worktree/branch/head triples, candidate worktree/branch/head, route,
  pull request, remote landing SHA, canonical synchronization, checks, cleanup,
  and recovery state.
- A clean retry is idempotent. Mutable remote facts are re-read before mutation;
  immutable completed phase receipts are replayed without repeating effects.
- Merge, revert, validation, policy, authentication, target-drift, CI, and
  canonical synchronization failures are typed non-terminal states with an
  exact resume command.
- Merge or revert conflicts are never resolved by this command.
- `--cleanup` is ignored until remote receipt and required canonical
  synchronization have succeeded. Cleanup retains the landing receipt needed
  to prepare a later revert.

## Acceptance Criteria

### AC: prepare-produces-an-immutable-consumable-candidate

Given two clean WB source worktrees in one repository, when `merge prepare`
runs, then a dedicated integration worktree contains the exact fetched target
and both ordered source heads, the receipt names all four immutable identities,
and neither source nor canonical checkout changes.

### AC: combined-command-walks-the-whole-journey

Given a conflict-free source and a test GitHub adapter, when bare `merge` runs,
then it prepares, validates, lands, verifies the exact remote target, performs
the requested canonical synchronization and cleanup, and terminates without a
manual Git or GitHub step.

### AC: dependent-agent-can-use-phase-one-without-waiting

Given a prepared candidate whose landing checks remain pending, another
worktree can rebase onto the receipt's candidate SHA and continue without
mutating the candidate lane or waiting for Phase 2.

### AC: auto-route-never-confuses-bypass-with-permission

Given required-pull-request policy, merge-queue policy, incomplete policy
evidence, or administrator-only bypass, `--route auto` does not direct-push.
It chooses a supported PR route or refuses when no supported safe route exists.

### AC: exact-head-pr-lands-and-synchronizes-canonical

Given a PR route with stable required checks, WB merges only the recorded head,
proves the remote target contains the server landing SHA, then fast-forwards a
clean canonical checkout already on that target to that exact SHA before
cleanup.

### AC: unpublished-candidate-rebases-over-target-drift

Given an advanced remote target and an unpublished prepared candidate, landing
rebases the isolated candidate onto the exact new target, records the before and
after target and candidate SHAs, reruns validation, and continues only when the
rebase is clean. A conflict aborts the rebase and preserves the candidate and
every source worktree. A published PR candidate is never force-pushed.

### AC: retry-resumes-without-duplicate-effects

Given interruption after candidate creation, source push, PR creation, CI pass,
remote merge, or canonical synchronization, rerunning the receipt's resume
command continues from the first incomplete boundary without duplicating a
worktree, push, pull request, merge, or cleanup.

### AC: every-pre-landing-failure-preserves-work

Given a dirty source, merge conflict, validation failure, unknown policy,
authentication failure, failed check, conflicting target rebase, or target
drift after PR publication, WB leaves every source commit and managed worktree
recoverable, records the exact failed phase, and prints an exact resume or
remediation command.

### AC: landed-failure-has-a-forward-revert-path

Given an exact landing receipt whose post-target checks fail, `merge revert`
creates a new candidate that reverses the before/after target tree without
resetting or force-pushing. A conflict refuses with all evidence preserved.

### AC: landed-target-ci-failure-accepts-an-audited-forward-repair

Given a post-target CI failure and the same clean source advanced by descendant
repair commits, rerunning `merge prepare` retains the exclusive lane, proves
the fetched target contains the prior landing, and proves the prior candidate
by graph ancestry or exact tree equality with its receipted squash landing. It
advances the candidate without rewriting published history, records every
failed landing, and prepares a fresh repair PR. A changed source identity,
non-descendant advance, moved candidate, tree mismatch, or missing target
containment refuses without state mutation.

### AC: cleanup-is-explicit-and-receipt-gated

Given a remotely receipted landing, omission of `--cleanup` retains all assets
and reports cleanup pending; inclusion removes only the exact absorbed source
and candidate assets after canonical synchronization and leaves the durable
landing/revert receipt readable.

### AC: agents-discover-merge-from-creation-and-completion-intent

Given an AI agent that is creating a WB worktree or trying to merge, integrate,
land, finish, deliver, push a target, operate a PR, clean completed work, resume,
or revert, the installed WB skill metadata and creation reference route it to
`wb worktree merge`, `wb worktree create --help` names the paired command, and
a newly created worktree's ignored `.worktree.md` repeats the completion path.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
