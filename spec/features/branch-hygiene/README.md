---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Branch Hygiene

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/branch-hygiene?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/branch-hygiene?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/branch-hygiene?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/branch-hygiene?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb branch` is a top-level command family that inventories and safely retires
local and remote Git branches across the fleet — including the large majority
that have no linked worktree and no WB Work Log claim, and are therefore
invisible to `wb worktree cleanup` today. `wb branch list` explains every
branch and the exact evidence behind its disposition. `wb branch cleanup` plans
by default and, only under an explicit `--apply`, deletes exactly those
branches whose content is provably contained in the freshly fetched origin
target. A dedicated `--scope remote` mode sweeps stale remote branches on their
own.

## Problem

### Cleanup is correct but far too narrow

A `wb worktree cleanup --all-merged` dry run was executed against the whole
fleet. It exited 0 and reported **39 eligible tasks**. The same fleet holds:

- **1,081 local branches.** 465 are merged ancestors of `origin/main`; 280 more
  have every patch already upstream by patch-id (`git cherry` reports no `+`
  lines — the squash-merge, rebase-merge and cherry-pick landings); 5 have trees
  identical to `main`. **750 are provably safe to delete.** Of the remaining
  331, 17 have an open pull request, 143 had a pull request merged and then
  drifted, 36 had one closed unmerged, and 135 never had a pull request at all.
- **571 linked worktrees.** 325 sit on merged branches, 34 hold uncommitted
  changes, and 13 registrations pointed at directories that no longer existed.

WB can retire 39 of them. Every skip in that run was WB being *correct*:

- `current branch head is not integrated into the exact origin target (awaiting
  push)` — by far the most common; local work that was never pushed.
- `branch still has an open pull request: <url>` — a correct refusal.
- `worktree has local changes` — a correct safety refusal.
- `coordinated task blocked by <repository>` — multi-repository task semantics
  holding a task together.

The gap is **scope, not judgement**. `wb worktree cleanup` enumerates
`<wb-home>/worktrees/<task>/...`, so a merged local branch with no worktree and
no Work Log claim is not a skipped candidate — it is not a candidate at all.
That describes the large majority of those 750 branches.

### The absence is filled by unsafe hand-rolled sweeps

Because no WB surface answers "clean up leftover branches", an agent asked to
do exactly that wrote roughly 200 lines of raw `git` analysis and was about to
delete 742 branches and 308 worktrees with it. A hand-rolled sweep has no
audit report, no lease protection, no Work Log sealing, and no way to
distinguish work that landed from work that landed and was then reverted.

### Long sweeps are indistinguishable from a hang

The fleet dry run above printed **nothing** for its entire multi-minute
execution and then emitted all 45 lines at once. An agent cannot tell that from
a hung process, and the standard reaction — kill and retry — leaves task locks
behind that then need `--resume-interrupted` to clear.

## Interaction with Other Features

[Worktree Lifecycle](../worktree-lifecycle/README.md) owns `wb worktree`: the
creation, guarding, Work Log claim, per-task lock, coordinated-task
all-or-nothing semantics, and terminal removal of a task's worktree together
with its branch. It remains the only path that may retire a branch belonging to
a WB task.

Branch Hygiene is a **sibling feature, not an extension of that one**, because:

1. Its subject has no worktree. Expressing "delete a branch that has no
   worktree" as a mode of `wb worktree cleanup` is the same category error that
   made the capability undiscoverable in the first place.
2. Its evidence model is different. Worktree cleanup is anchored on WB tasks,
   Work Log claims, task locks and coordinated siblings. A bare branch has none
   of those, so none of that machinery applies to it. Selecting between two
   different eligibility engines with a flag on one safety-critical command is
   where safety bugs hide.
3. A remote-branches-only mode has no worktree meaning at all.
4. `wb worktree cleanup` already carries nine flags and three interlocking
   conditional rules. Adding scope selection and a new evidence class to it
   makes it unreviewable.
5. WB already separates command families by subject: `wb worktree`, `wb deps`,
   `wb hooks`, `wb ci`, `wb repo`, `wb fleet`.

The two features share primitives deliberately: the same fresh-fetch exact
target resolution, the same compare-and-delete and force-with-lease ref
retirement, and the same durable audit-report discipline.

## Behavior

### Command surface

#### REQ: top-level-branch-family

WB MUST expose a top-level `wb branch` command family with exactly two public
leaves in this feature: `wb branch list` and `wb branch cleanup`. The family
MUST NOT be nested under `wb worktree`, and `wb worktree cleanup` MUST NOT gain
a branch-scope or remote-only flag.

`wb branch list` flags: `--base` (string, default `main`), `--scope` (string,
one of `local`, `remote`, `all`; default `local`), `--only` (string, one
disposition name), `--older-than` (duration, default `0`), `--format` (string,
`text` or `json`; default `text`). It MUST accept the root `--filter` and
`--projects-root` flags.

`wb branch cleanup` flags: `--base` (string, default `main`), `--scope`
(string, one of `local`, `remote`, `all`; default `local`), `--apply` (bool,
default false), `--older-than` (duration, default `24h`; `0` disables),
`--report-dir` (string, default `<wb-home>/reports/branch-cleanup/<timestamp>`),
`--format` (string, `text` or `json`; default `text`). It MUST accept the root
`--filter` and `--projects-root` flags.

`wb branch cleanup` MUST NOT define a `--remote` boolean. Remote action is
selected only by `--scope`, so `--scope remote` is unambiguous where
`wb worktree cleanup --remote` — an additional action taken during `--apply` —
is not. Both leaves MUST be added to `persistentFlagSupport` for
`projects-root` and `filter` in `cmd/wb/main.go`.

#### REQ: read-only-list

`wb branch list` MUST be read-only in every configuration. It MUST NOT create,
move, delete, or rewrite any ref, index, working tree, worktree registration,
report, or journal. Its only permitted remote interaction is fetching.

#### REQ: fresh-exact-target

Every disposition MUST be computed against the exact commit SHA obtained by
fetching `refs/heads/<base>` from `origin` during the current run. A stale
local branch, a stale remote-tracking ref, or a previously cached SHA MUST NOT
be used as the target. A repository whose target cannot be fetched or resolved
MUST yield the `unreadable` disposition for its branches and MUST NOT block
other repositories in the sweep.

### Evidence classes

#### REQ: evidence-class-taxonomy

Every reported branch MUST carry exactly one disposition drawn from this closed
set, together with the evidence string that produced it:

- `contained` — the branch SHA is an ancestor of the freshly fetched exact
  target SHA, proved by `git merge-base --is-ancestor <branch-sha>
  <target-sha>`. This is the only disposition eligible for deletion.
- `absorbed` — the branch is not an ancestor of the target, but either
  `git cherry <target-sha> <branch-sha>` emits zero `+` lines, or
  `<branch-sha>^{tree}` equals `<target-sha>^{tree}`. Report-only; see
  `#req:absorbed-is-report-only`.
- `unique` — `git cherry <target-sha> <branch-sha>` emits at least one `+`
  line. The evidence MUST include the count of unique patches.
- `protected` — the branch is `<base>` itself, is the canonical clone's current
  `HEAD`, or matches a configured protected name or pattern.
- `in-use` — the branch is checked out in any linked worktree, or is named by a
  WB Work Log claim. See `#req:delegate-wb-owned-branches`.
- `unreadable` — required evidence could not be obtained.

`protected`, `in-use`, and `unreadable` MUST be evaluated before `contained`,
so a protected or claimed branch is never reported as deletable.

#### REQ: absorbed-is-report-only

The `absorbed` class MUST NEVER be eligible for `--apply`, in any scope, under
any flag, in any configuration. No flag that makes it eligible may be added.
`wb branch cleanup --apply` MUST leave every `absorbed` branch untouched and
MUST report it with its evidence and the reason it was not deleted.

This is a deliberate design decision, not an unimplemented case. Patch-id
equality proves that patches with identical content exist upstream. It does not
prove that this branch's work is present in the target now:

- Work that landed and was then **reverted** still emits zero `+` lines,
  because the revert is a separate commit and every original patch still has an
  upstream patch-id twin. Deleting on this evidence destroys the only copy of
  work the target no longer contains.
- It cannot distinguish work that landed from an identical patch independently
  authored upstream.
- It is silently defeated by any amendment made while landing.

WB already solves the squash-merge and cherry-pick case correctly and
strictly, in
[worktree-lifecycle#req:absorbed-integration-containment-evidence][absorbed]:
a real landing receipt from GitHub's commit-to-pull-request index, plus a local
three-way merge proof that the candidate adds nothing to either the landing
commit or the fetched target. That path — not patch-id — is how a
squash-merged branch becomes deletable. `absorbed` exists to make those 280
branches **visible and actionable**, by naming the receipt-based command that
can retire them, never to retire them itself.

[absorbed]: ../worktree-lifecycle/README.md

#### REQ: absorbed-names-its-remedy

Every `absorbed` row MUST name the concrete next step in its reason text: for a
branch that belongs to a WB task, `wb worktree cleanup <task> --absorbed-by
<pr-or-commit>`; otherwise an explicit human decision. A row that merely says
"not eligible" is insufficient, because it reproduces the discoverability
failure this feature exists to remove.

#### REQ: delegate-wb-owned-branches

A branch that is checked out in any linked worktree, or that is named by a WB
Work Log claim, MUST be reported as `in-use` and MUST NEVER be deleted by
`wb branch cleanup`, even when its content is `contained`. Its reason MUST name
`wb worktree cleanup <task>` or `wb worktree abort <task>` as the correct
command. This preserves the Work Log sealing contract and prevents two commands
from racing on the same ref.

### Deletion

#### REQ: dry-run-default

`wb branch cleanup` MUST be a dry-run plan unless `--apply` is explicit. A dry
run MUST NOT create a report directory, delete a ref, or make any other
mutation. `--apply` MUST be required for every deletion in every scope.

#### REQ: compare-and-delete

A local branch MUST be deleted with a compare-and-delete operation against the
exact SHA the plan recorded — `git update-ref -d refs/heads/<branch>
<expected-sha>` — never with `git branch -d` or `git branch -D`, whose own
merge test is against `HEAD` rather than the fetched target. A remote branch
MUST be deleted with `git push --force-with-lease=refs/heads/<branch>:<observed-sha>
origin :refs/heads/<branch>`. A ref that moved between plan and apply MUST
refuse only its own branch, with the moved SHA reported, and MUST NOT abort the
sweep.

#### REQ: recheck-before-mutation

Immediately before deleting each branch, WB MUST refetch the exact target,
re-resolve the branch SHA, and re-verify containment, protection, and in-use
state. Evidence gathered at plan time MUST NOT be reused as authorization.

#### REQ: never-touch-a-working-tree

`wb branch cleanup` MUST NOT remove, move, modify, or clean any working tree,
worktree registration, index, or stash, in any configuration. A branch checked
out anywhere is `in-use` and is therefore never a deletion candidate, so no
worktree — and in particular no worktree holding uncommitted changes — can be
affected by this command.

#### REQ: durable-audit

An `--apply` attempt MUST write a machine-readable plan below `--report-dir`
before its first destructive Git operation, and MUST update that same report
with applied or failed state. Each entry MUST retain repository, branch, branch
SHA, target branch, fetched target SHA, evidence class, evidence string,
decision, and outcome. The report MUST remain readable after the branches it
describes are gone.

### Remote-branches-only mode

#### REQ: remote-scope-enumeration

`--scope remote` MUST enumerate remote branches from `refs/remotes/origin/*`
after a fetch with `--prune` in the current run, and MUST NOT report or act on
any local branch. `--scope all` MUST report both, and under `--apply` MUST
delete a local branch and its matching remote branch as two independently
evidenced decisions, never as one.

A remote branch is `contained` when its remote SHA is an ancestor of the
freshly fetched exact target SHA. `--older-than` for a remote branch MUST be
measured from that branch's committer date.

#### REQ: remote-apply-fails-closed-without-pull-request-evidence

Deleting a remote branch closes any open pull request from it, so remote
deletion MUST additionally require pull-request evidence. WB MUST refuse to
delete a remote branch that is the head of an open pull request, regardless of
containment. When pull-request evidence cannot be obtained — GitHub
unreachable, unauthenticated, or rate-limited — `--scope remote --apply` and
`--scope all --apply` MUST refuse to delete any remote branch and MUST report
the missing evidence, rather than proceeding without it. Local deletion is
unaffected, because deleting a local ref cannot alter a pull request.

### Output

#### REQ: incremental-progress-on-stderr

Any `wb branch` sweep MUST emit incremental progress to **stderr** as it works,
flushed per event rather than buffered until the end. Each event MUST name the
repository being inspected and carry a running `[n/N]` count. A final summary
line MUST report totals per disposition and elapsed time. stdout MUST remain
reserved for the report, so `--format json` stdout stays machine-parseable.

Progress MUST be plain line-buffered text unless stdout is a terminal and
`--non-interactive` is unset, in which case a live terminal UI is permitted.
Progress MUST NOT be suppressed merely because stderr is not a terminal: an
agent reads stderr and needs it to distinguish work from a hang.

#### REQ: text-output-is-decision-shaped

`wb branch list` and a `wb branch cleanup` dry run MUST group rows by
repository, and each row MUST carry branch, short SHA, disposition, age, and
evidence. The text summary MUST report the count per disposition, so
"how many are safe to delete" is answerable without reading every row.

### Discovery

#### REQ: dedicated-branch-skill

This feature is not complete until the capability is discoverable by an agent
that was asked about branches:

- `ai/skills/wb-branches/SKILL.md` and `ai/skills/wb-branches/agents/openai.yaml`
  MUST exist.
- `ai/skills/commands.json` MUST route the `branch` command to `wb-branches`.
- `ai/capabilities.json` MUST carry `wb.branch.list` and `wb.branch.cleanup`
  rows whose runtime flags match non-inherited public help exactly, with help
  anchors, skill evidence, and test evidence.
- The `wb-branches` frontmatter `description` MUST name branches, deleting
  merged branches, pruning remote branches, and fleet-wide or historic sweeps,
  and MUST state that raw `git branch -d`, `git branch --merged`, and
  `git push --delete` sweeps are not an acceptable substitute.

The skill MUST NOT be added before the commands exist. A skill that advertises
an absent surface outranks the `wb-worktrees` trigger and sends an agent to a
command that fails, which is worse than the gap it closes.

The trigger text is normative and MUST be used verbatim, because it is the only
text an agent reads before deciding whether to load the skill:

```yaml
---
name: wb-branches
description: Use WB to inventory and safely delete Git branches across the fleet — merged branches, stale branches, leftover branches that have no worktree, and stale remote branches. Use when asked to clean up branches, delete merged branches, prune remote branches, tidy leftover or historic branches, or audit branch hygiene, in one repository or across every repository. Never hand-roll `git branch -d`, `git branch --merged`, `git branch -D`, or `git push --delete` sweeps — they have no audit trail, no lease protection, and cannot tell work that landed from work that landed and was then reverted.
---
```

The body MUST name `wb branch list` and `wb branch cleanup` with their full flag
surface, MUST state that dry run is the default and `--apply` is required, MUST
state that `absorbed` is never deleted and why, MUST state that a branch owned
by a WB task is handed to `wb worktree cleanup`, and MUST cross-link
`ai/skills/wb-worktrees/references/cleanup.md`. The `wb-worktrees` skill MUST
gain a reciprocal pointer to `wb-branches` for branches with no worktree.

### Non-goals

#### REQ: bounded-scope

This feature MUST NOT add branch creation, renaming, or restoration; MUST NOT
mutate pull requests or any other GitHub state; MUST NOT change
`wb worktree cleanup` eligibility; and MUST NOT delete anything without
`--apply`.

## Acceptance Criteria

### AC: read-only-inventory-with-evidence

**Requirements:** branch-hygiene#req:top-level-branch-family, branch-hygiene#req:read-only-list, branch-hygiene#req:fresh-exact-target, branch-hygiene#req:evidence-class-taxonomy, branch-hygiene#req:text-output-is-decision-shaped

Given a fixture fleet of real bare remotes and clones containing a branch
merged into `main`, a branch squash-merged into `main`, a branch with unique
unlanded commits, a branch checked out in a linked worktree, and the `main`
branch itself, when `wb branch list --format json` runs, then each branch is
reported exactly once with the expected disposition of `contained`, `absorbed`,
`unique`, `in-use`, and `protected` respectively; every row carries its
evidence string; the fetched target SHA is recorded; and a byte-for-byte
comparison of the fixture's refs, worktrees, and working trees before and after
the run shows no change.

### AC: only-contained-branches-are-deleted

**Requirements:** branch-hygiene#req:dry-run-default, branch-hygiene#req:compare-and-delete, branch-hygiene#req:recheck-before-mutation, branch-hygiene#req:never-touch-a-working-tree, branch-hygiene#req:durable-audit, branch-hygiene#req:delegate-wb-owned-branches

Given that same fixture, when `wb branch cleanup` runs without `--apply`, then
no ref is deleted and no report directory is created. When
`wb branch cleanup --apply` then runs, then exactly the `contained` branch is
deleted; the `absorbed`, `unique`, `in-use`, and `protected` branches survive;
no worktree is removed or modified and a worktree holding uncommitted changes
is untouched; and the audit report records repository, branch, branch SHA,
target SHA, evidence class, decision, and outcome for every candidate. When a
branch's SHA is advanced between plan and apply, then that branch alone is
refused with its moved SHA reported and the remaining deletions still succeed.

### AC: patch-identical-content-is-never-deleted

**Requirements:** branch-hygiene#req:absorbed-is-report-only, branch-hygiene#req:absorbed-names-its-remedy

Given a branch whose commits were cherry-picked into `main` and then reverted
in `main`, so that `git cherry` reports zero `+` lines while the target no
longer contains the work, when `wb branch cleanup --apply` runs with every
flag combination the command accepts, then the branch is classified `absorbed`,
is never deleted, and its reason names the receipt-based
`wb worktree cleanup --absorbed-by` remedy or an explicit human decision. A
test MUST assert that no flag combination deletes an `absorbed` branch.

### AC: remote-only-mode-fails-closed

**Requirements:** branch-hygiene#req:remote-scope-enumeration, branch-hygiene#req:remote-apply-fails-closed-without-pull-request-evidence, branch-hygiene#req:compare-and-delete

Given a bare remote holding a branch contained in `main`, a branch contained in
`main` that is the head of an open pull request, and a branch with unique
commits, when `wb branch cleanup --scope remote` runs, then no local branch
appears in the report. When `--apply` is added with pull-request evidence
available, then only the contained branch without an open pull request is
deleted, using force-with-lease against its observed SHA. When pull-request
evidence is unavailable, then no remote branch is deleted, the missing evidence
is reported, and local deletion under `--scope all` still proceeds. Hosted
pull-request metadata MAY be supplied by a deterministic test double.

### AC: sweeps-report-progress-before-they-finish

**Requirements:** branch-hygiene#req:incremental-progress-on-stderr

Given a fixture fleet of several repositories, when `wb branch list --format
json` runs with stdout and stderr captured separately, then stderr receives at
least one progress event naming a repository with an `[n/N]` count before the
final report is written, plus a closing summary with per-disposition totals and
elapsed time; and stdout parses as JSON with no progress text mixed into it.

## Open Questions

- Should protected branch names and patterns be configurable per repository, or
  only fleet-wide? The initial implementation may hard-code `main`, `master`,
  and the canonical `HEAD`, and treat configurability as a follow-up.
- Should `wb branch list` join archived Work Logs so a branch retired long ago
  is explicable, or is live claim evidence sufficient?

---
*This document follows the https://specscore.md/feature-specification*
