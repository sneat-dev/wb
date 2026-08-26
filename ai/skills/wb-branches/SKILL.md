---
name: wb-branches
description: Use WB to inventory and safely delete Git branches across the fleet — merged branches, stale branches, leftover branches that have no worktree, and stale remote branches. Use when asked to clean up branches, delete merged branches, prune remote branches, tidy leftover or historic branches, or audit branch hygiene, in one repository or across every repository. Never hand-roll `git branch -d`, `git branch --merged`, `git branch -D`, or `git push --delete` sweeps — they have no audit trail, no lease protection, and cannot tell work that landed from work that landed and was then reverted.
---

# WB branches

`wb branch` is the command family for a branch that has no linked worktree —
the large majority on a real fleet. A worktree-scoped hygiene request still
belongs to `$wb-worktrees` (`wb worktree cleanup`/`orphans`); reach for this
skill whenever the branch in question either has no worktree at all, or you
do not yet know whether it does.

## Commands

```sh
wb branch list
wb branch list --scope remote
wb branch list --scope all --only unique
wb branch cleanup
wb branch cleanup --scope remote --apply
wb branch cleanup --scope all --apply --older-than 0
wb branch cleanup --scope remote --absorbed-by 123
```

`wb branch list` is read-only in every configuration; its only remote
interaction is fetching. `wb branch cleanup` plans by default — `--apply` is
required for every deletion, in every scope.

Flags on both: `--base` (default `main`), `--scope` (`local`, `remote`, or
`all`; default `local`), `--format` (`text` or `json`), plus root `--filter`
and `--projects-root`. `wb branch list` adds `--only <disposition>` and
`--older-than` (default `0`, shows every age). `wb branch cleanup` adds
`--apply`, `--older-than` (default `24h`, `0` disables the grace window),
`--report-dir`, `--receipts`, and `--absorbed-by <pr-or-commit>`. Scope is
selected only by `--scope` — there is no `--remote` boolean here, unlike
`wb worktree cleanup --remote`.

## The evidence taxonomy

Every branch gets exactly one disposition, computed against the exact commit
SHA WB fetches from `origin/<base>` during the run — never a stale local
branch or tracking ref:

| Disposition | Meaning | Eligible for `--apply`? |
| --- | --- | --- |
| `contained` | ancestor of the fetched exact target | **yes** |
| `receipted` | a proved landing receipt — GitHub's own commit-to-pull-request index, or an operator-attested `--absorbed-by <pr-or-commit>` — shows the work is in the target | **yes, only under `--receipts` or `--absorbed-by`** |
| `absorbed` | patch-id or tree equal to the target, but not an ancestor | **never** |
| `unique` | `git cherry` proves it has content not upstream | no |
| `protected` | is `--base`, the canonical clone's current HEAD, or a protected name | no |
| `in-use` | checked out in a linked worktree, or claimed by a live WB Work Log | no |
| `unreadable` | required evidence could not be obtained | no |

## `absorbed` is not a bug — never delete on it

A branch that was squash-merged, rebase-merged, or cherry-picked so that all
of its patches are already upstream by patch-id is `absorbed`, not
`contained`, and `wb branch cleanup --apply` leaves it untouched under every
flag combination that exists. This is deliberate, not an unimplemented case:
patch-id equality proves identical content exists upstream, it does not prove
this branch's work is still present in the target *now*. A branch that landed
and was later **reverted** still reports zero unique patches — the revert is
a separate commit, and every original patch still has an upstream patch-id
twin. Deleting on that evidence would destroy the only remaining copy of work
the target no longer contains.

Do not second-guess this with raw `git cherry` output and delete by hand. Each
`absorbed` row names its own remedy:

- A WB-owned branch (it has a task): `wb worktree cleanup <task> --absorbed-by <pr-or-commit>` —
  the receipt-based path that proves containment through a real GitHub
  landing receipt plus a local three-way merge, not through patch-id alone.
- A branch with no worktree left — the common case, and the one that motivated
  this flag (a squash-landed branch whose worktree and local branch were
  already cleaned up): `wb branch cleanup --absorbed-by <pr-or-commit>`. It
  performs the exact same attested-absorption proof as the worktree-cleanup
  flag above — the named pull request or commit must contain the branch's
  content, the fetched target must still contain it, and the named commit
  must be exactly where the work entered the target, not merely somewhere
  downstream of it. A branch that passes is reclassified `receipted`, never
  `absorbed`; one that fails is reported with the failing check named and
  nothing is deleted.
- Otherwise: re-run with `--receipts`. WB then asks GitHub's own
  commit-to-pull-request index for a merged pull request into the exact base,
  verifies its merge commit is contained in the fetched target, and proves
  locally — with a three-way merge that mutates nothing — that the branch adds
  nothing to the landing commit or the target. A branch that passes every
  check is reclassified `receipted` and becomes eligible; one that fails any
  check keeps its disposition with the failing check named in its evidence.
  The reverted-work case fails the proof by construction, because merging the
  branch back would change the target's tree. This costs one GitHub query per
  candidate, which is why it is opt-in.
- A branch neither WB-owned nor receipt-provable stays where it is: an
  explicit human decision. No flag makes `absorbed` itself eligible.

`--receipts` also rescues `unique`-classified squash landings: a multi-commit
squash leaves no upstream twin for any individual patch-id, so a fully landed
branch can report unique patches while every byte of it is in the target.

## A branch that has a worktree or a live claim: `in-use`

`wb branch cleanup` never deletes a branch checked out in any linked
worktree, or named by a live WB Work Log claim, even when its content is
`contained` — deleting it there would race `wb worktree cleanup`/`abort` and
break the Work Log sealing contract. Its row names the correct command:
`wb worktree cleanup <task>` or `wb worktree abort <task>`. See
[`wb-worktrees`' cleanup reference](../wb-worktrees/references/cleanup.md) for
that side of the split.

## Remote deletion fails closed

`--scope remote` (and the remote half of `--scope all`) additionally requires
pull-request evidence before `--apply` deletes anything: a branch that is the
head of an open pull request is refused regardless of containment, and when
PR evidence cannot be obtained at all — GitHub unreachable, unauthenticated,
rate-limited — **no remote branch is deleted in that run**, reported as
missing evidence rather than silently skipped. Local deletion is unaffected,
because deleting a local ref cannot close a pull request. A remote branch is
deleted with `git push --force-with-lease` against its observed SHA, never a
bare `git push --delete`.

## Compare-and-delete, never `git branch -d`/`-D`

A local branch is deleted with `git update-ref -d refs/heads/<branch>
<expected-sha>` — a compare-and-delete against the exact SHA the plan
recorded — never `git branch -d`/`-D`, whose own merge test is against `HEAD`
rather than the freshly fetched target. Immediately before every deletion WB
refetches the exact target, re-resolves the branch, and re-verifies
containment; a branch that moved between plan and apply refuses only itself,
with the moved SHA reported, and never aborts the rest of the sweep. An
`--apply` attempt writes a durable machine-readable plan below
`<wb-home>/reports/branch-cleanup/<timestamp>` before its first destructive
Git operation, and updates that same report with each candidate's outcome.

## Fleet sweeps report progress

A `wb branch` sweep across many repositories emits `[n/N] repository`
progress to stderr as it works, plus a closing summary with totals per
disposition — stdout stays reserved for the report, so `--format json` stays
machine-parseable. A multi-minute silence is indistinguishable from a hang; do
not kill and retry a sweep that has not printed anything yet on stderr —
check stderr first.
