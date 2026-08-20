---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Worktree Lifecycle

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb worktree` creates, guards, inventories, and safely cleans task worktrees
while a workstation moves from the historic `<projects-root>/.wb` layout to
the user-scoped `~/.wb` home. `wb worktree list` reports local Git state with
optional GitHub PR evidence; `wb worktree cleanup` safely plans or applies
removal of clean task worktrees and exact merged branch refs.

## Problem

Central worktrees protect canonical clones, but completed tasks accumulate
linked checkouts and branches. Ad-hoc cleanup can discard uncommitted work,
delete a reused branch, or remove one repository while a coordinated task is
still active elsewhere. A default-layout migration must not either continue
creating work under an obsolete projects-root directory or strand linked
worktrees and pre-existing hooks. The guard must also distinguish a real,
short-lived Git rebase from an arbitrary detached development checkout.

## Behavior

### Fast local inventory

#### REQ: nonmutating-verified-base

Before creating a new worktree, WB MUST verify the requested
`refs/heads/<base>` by fetching it from `origin`, without requiring the
canonical clone to be clean or on the base branch. It MUST create the feature
branch from the exact verified commit, not an unverified or moving
local/remote ref. Creation MUST NOT switch, pull, reset, fast-forward, stage,
or otherwise alter the canonical checkout, any local base branch, or a nested
linked worktree. A stale local base branch, active local canonical changes, or
one checked out in another linked worktree is not a blocker; an inaccessible,
missing, non-commit, or otherwise unverifiable remote base MUST fail before WB
creates a branch or worktree.

#### REQ: offline-list-default

`wb worktree list [task]` MUST inspect only the local, resolver-recognized
worktree hierarchies and local Git state by default. It MUST contact GitHub and
the remote only when `--github` is explicit.

#### REQ: authoritative-write-home

New worktree creation, locks, and new cleanup reports MUST use the resolver's
write home: `~/.wb` by default, or the exact directory named by `WB_HOME` when
that variable is set. A populated `<projects-root>/.wb` MUST NOT silently
become the write home. `WB_HOME` MUST remain authoritative for commands later
started by a managed hook installed from that environment.

#### REQ: migration-layout-compatibility

Without an explicit `WB_HOME`, the shared layout resolver MUST recognize an
existing legacy `<projects-root>/.wb/worktrees` hierarchy in addition to the
new write layout. Guard, inventory, and cleanup MUST continue to validate and
operate on those linked worktrees using their actual layout. An explicit
`WB_HOME` selects only that layout so a caller can intentionally isolate a
session or fixture. A managed hook that pins the normal default home MUST mark
that fact so it retains this migration compatibility without treating a
user-selected `WB_HOME` as non-authoritative.

#### REQ: legacy-mixed-inventory

Inventory MUST recognize both historic direct-repository task entries
`<task>/<repository>` and current `<task>/<owner>/<repository>` entries.
Once a Git root is recognized, traversal MUST stop below it. Malformed
candidates MUST yield deterministic diagnostics without hiding valid sibling
repositories whenever the command's result API permits.

#### REQ: validated-identity

Each result MUST be a real linked worktree at the expected task, owner, and
repository path for either supported layout, backed by the expected canonical
clone. Results MUST include task, repository, branch, head, cleanliness, lock
state, last commit time, and local merge state.

### Guard and hooks

#### REQ: guarded-transient-rebase

The guard MUST reject detached development by default. It MAY allow a detached
linked worktree only while Git proves a live rebase through its real
`rebase-merge` or `rebase-apply` state. The transient allowance MUST retain all
canonical/common-directory and resolver-layout checks, and MUST end when that
Git state ends.

#### REQ: hook-home-stability

New managed hook shims MUST persist the resolved WB home as well as the
projects root, so their guard invocation uses the same authoritative layout as
installation. Hooks installed by the prior release MUST remain usable after
upgrade through the migration-compatible resolver.

#### REQ: hook-executable-stability

Hook installation or automatic refresh MUST reject a transient executable such
as the binary produced by `go run`. A managed shim MUST point only to a durable
candidate or installed WB executable; otherwise a successful repair can leave
the next Git operation unable to run its guard.

### Conservative cleanup plan

#### REQ: dry-run-default

`wb worktree cleanup` MUST require one task or `--all-merged` and MUST be a dry
run unless `--apply` is explicit. A 24-hour merged-PR safety window MUST apply
by default; zero MUST explicitly disable it.

#### REQ: exact-remote-target-evidence

A repository MUST be eligible only when it is clean and unlocked, has no open
PR for its branch, and the current local branch head is an ancestor of the
exact freshly fetched `origin/<target>` SHA. A matching merged GitHub PR MAY
supply merge-time evidence for the age window, but a direct push to the target
MUST be a supported integration path. A local-only merge is `awaiting_push`
and ineligible. An existing remote source branch MUST still point to the exact
local head.

#### REQ: resumable-interrupted-operation-lock

A killed operation leaves its task lock behind, because no deferred release
runs. That remnant MUST NOT be indistinguishable from a live operation: WB MUST
decide by whether a process still holds the lock, not by whether the lock entry
exists. A lock held by a live process MUST refuse every caller, and MUST be
reported distinctly from an abandoned one.

Reclaiming an abandoned lock MUST be restricted to resuming a durable backlog
record, which MUST independently revalidate that the worktree path is gone, its
registration is gone, the remote branch is gone, and the local branch still
points at the exact recorded head. A stray lock on a task whose checkout is
still present MUST keep that task reported as locked and ineligible, because no
record describes what remains to finish. A reclaimed lock MUST be retired on
completion exactly as a normally acquired one is.

#### REQ: absorbed-integration-containment-evidence

A target branch that requires linear history forces a merger to batch several
completed candidates onto one integration branch and land that branch once, so
a candidate's own head is absent from the target by construction. Such a
candidate MUST still be eligible, on evidence only.

WB MUST accept a landing receipt from GitHub's own commit-to-pull-request index
for the immutable source commit, and MUST NOT require that pull request's head
to equal the candidate head; a branch name MUST NOT be treated as evidence. The
receipt MUST name a merged pull request into the exact base whose merge commit
is contained in the freshly fetched exact origin target.

Every receipt MUST additionally be proved locally: a three-way merge of the
candidate into the landing commit MUST succeed and produce exactly that
commit's tree, and the same merge into the fetched target MUST produce exactly
the target's tree. Work that landed and was later reverted, or that landed only
in part, MUST therefore remain `awaiting_push`. The proof MUST NOT mutate any
ref, index, or working tree.

`--absorbed-by` MAY name the merged pull request or exact landing commit for an
absorption GitHub cannot associate, such as content cherry-picked rather than
merged into the integration branch. It MUST select which receipt to verify and
MUST NOT substitute for one: every proof above still applies, and the named
commit MUST additionally be exactly where the work entered the target, so the
flag cannot degrade into a bare content assertion. A pointer that fails any
verification MUST refuse only its own candidate, with the failing verification
reported as that candidate's reason, and MUST NOT be reported as a malformed
worktree or abort a fleet sweep.

#### REQ: coordinated-task-safety

If any repository in a task is ineligible, cleanup MUST mark every repository
in that task ineligible. It MUST preserve skipped work and explain the
blocking evidence.

### Long sweep feedback

#### REQ: incremental-sweep-progress

A fleet-wide sweep — `wb worktree cleanup --all-merged`, or any inventory run
with `--github` — fetches and queries a hosted API once per candidate and can
run for minutes. Such a run MUST emit incremental progress to **stderr**,
flushed per event rather than buffered until the report is complete. Each event
MUST name the repository or task being inspected and carry a running `[n/N]`
count, and the run MUST close with a summary carrying totals and elapsed time.

stdout MUST remain reserved for the report, so `--format json` stdout stays
machine-parseable. Progress MUST NOT be suppressed merely because stderr is not
a terminal: an agent reads stderr, and a multi-minute silence is
indistinguishable from a hang. The observed consequence is concrete — a killed
and retried sweep leaves task locks behind that then require
`--resume-interrupted` to clear.

### Audited application

#### REQ: recheck-and-compare-delete

Before mutation, WB MUST refetch the exact remote target, reacquire optional PR
and source-branch evidence, recheck cleanliness, and refuse a moved head. It
MUST durably seal/archive the Work Log before any remote or local deletion,
remove only the identified linked worktree, and use a compare-and-delete
operation for the exact local branch ref.

#### REQ: remote-opt-in

Remote deletion MUST require both `--apply` and `--remote`. It MUST use
force-with-lease against the observed head so an advanced branch is preserved.

#### REQ: durable-audit

An apply attempt MUST write a machine-readable plan before its first
destructive Git operation and update the same report with applied or failed
state. The report MUST retain repository, branch, head, PR URL, decision, and
outcome evidence.

#### REQ: resumable-post-removal-backlog

Before Git removes a linked checkout, WB MUST persist a private machine-readable
recovery stage carrying the exact task, repository, worktree registration,
branch, head, remote-ref evidence, and disposition. If the process stops after
worktree removal but before compare-and-delete of the local branch, the same
named cleanup or discarded-abort journey MUST expose that backlog and resume
only after proving the worktree registration is absent, the remote source
branch is absent, and the local ref is either absent or still equals the
recorded head. A worktree path that still exists MUST NOT by itself withhold
the backlog from resumption: registration, not the path, distinguishes a
refused removal from residue (see
worktree-lifecycle#req:unregistered-residue-removal). Completion MUST remain
discoverable even when live worktree inventory no longer contains the task.

A record whose task namespace no longer exists cannot be locked at all. When
every subject it names is also already absent — no checkout at the recorded
path, no registration, no remote source branch, no local ref — WB MUST close
the record without that lock, because the only remaining operation is a write
to WB's own private journal rather than a deletion. If any subject is still
present WB MUST refuse, so that one unresumable record cannot fail every later
fleet sweep and no record still owing a deletion is quietly marked done.

#### REQ: unregistered-residue-removal

Git removes a linked worktree's registration even when it fails partway through
deleting the working tree, and exits non-zero. On a failed removal WB MUST
distinguish the two outcomes by the registration: a worktree Git still
registers was refused and MUST still fail the task, and one Git no longer
registers MUST be treated as WB's own residue and removed by WB itself, after
which the lifecycle MUST continue to the exact local branch deletion rather
than stranding the task.

Residue removal derives its authority from the gates the task already passed —
clean checkout, head integrated into the exact origin target, and a path WB
created below its own worktrees root and still holds a validated descriptor
for. WB MUST NOT gate it on the names of directories inside the checkout;
ignored build output is in scope exactly as it is when Git's own removal
succeeds. WB MUST descend through retained descriptors, MUST NOT follow a
symlink out of the checkout, and MAY grant itself write permission on a
directory of the residue that denies an unlink. A residue directory WB cannot
open MUST be reported with the permission needed, never worked around by name.
An apply report MUST record that WB, rather than Git, removed the checkout.

#### REQ: internal-stage-terminalization

Inventory MUST classify reserved `.wb-stage-*` and `.wb-retired-stage-*`
entries as WB control-plane artifacts before considering legacy dot-prefixed
Git worktrees. A dry run MUST expose their exact disposition. Under the held
task descriptor/lock, apply MAY atomically archive only the exact recognized
stage that is still empty at the retirement boundary. A non-empty, symlinked,
replaced, or invalid stage MUST remain explicit blocking cleanup backlog and
MUST NOT be silently ignored, deleted, or treated as a repository. A terminal
task MUST have no such artifact left in its active task namespace.

### Audited abort and recycle

#### REQ: discarded-abort-boundary

`wb worktree abort --disposition discarded --apply` MUST also require
`--remote`. Before the first deletion across a coordinated task, WB MUST
corroborate every immutable Work Log claim and live checkout. Immediately
before each removal it MUST repeat clean/head/registration checks, seal the
local archive/outbox, retire only an exact unchanged remote source ref with
force-with-lease, and remove the worktree/local ref through descriptor-anchored
Git operations. Handoff and `not_landed` MUST retain dirty resumable state and
bind exactly one successor instead of deleting it. Before an applied transfer
publishes anything, its creator MUST supply the successor's exact model or
explicit `unknown`, plus independently known optional CLI/provider route
identifiers; WB MUST NOT copy the predecessor's route. Automatic recycle
rollback recovery MUST use explicit unknown model/provenance and no route.

#### REQ: recycle-transaction

`wb worktree rename --apply` MUST require `--remote`, fetch and pin the fresh
base, preflight every repository before terminalizing the first, retire old
local and exact remote source branches, reset the Work Log projection, and
carry only explicitly allow-listed cache paths. An ordinary runtime failure on
repository N MUST roll repositories 1..N back to their old paths/branches and
active recovery claims so the same coordinated rename is retryable. Durable
terminal/outbox history MUST remain append-only. A process crash MAY require
recovery from those records until automatic journal replay is implemented.

## Interaction with Other Features

[Fleet Status](../fleet-status/README.md) reports canonical repository health.
Worktree Lifecycle owns the separate inventory and cleanup rules for linked
task checkouts.

## Acceptance Criteria

### AC: safe-real-git-lifecycle

**Requirements:** worktree-lifecycle#req:offline-list-default, worktree-lifecycle#req:nonmutating-verified-base, worktree-lifecycle#req:authoritative-write-home, worktree-lifecycle#req:migration-layout-compatibility, worktree-lifecycle#req:legacy-mixed-inventory, worktree-lifecycle#req:validated-identity, worktree-lifecycle#req:guarded-transient-rebase, worktree-lifecycle#req:hook-home-stability, worktree-lifecycle#req:hook-executable-stability, worktree-lifecycle#req:dry-run-default, worktree-lifecycle#req:exact-remote-target-evidence, worktree-lifecycle#req:resumable-interrupted-operation-lock, worktree-lifecycle#req:absorbed-integration-containment-evidence, worktree-lifecycle#req:coordinated-task-safety, worktree-lifecycle#req:incremental-sweep-progress, worktree-lifecycle#req:recheck-and-compare-delete, worktree-lifecycle#req:remote-opt-in, worktree-lifecycle#req:durable-audit, worktree-lifecycle#req:resumable-post-removal-backlog, worktree-lifecycle#req:unregistered-residue-removal, worktree-lifecycle#req:internal-stage-terminalization, worktree-lifecycle#req:discarded-abort-boundary, worktree-lifecycle#req:recycle-transaction

Integration tests using real bare remotes, clones, commits, branches, merges,
linked worktrees, rebases, and refs prove that creation fetches and pins the
remote base without changing a dirty, off-base canonical checkout, its staged
index, unstaged and untracked files, or a nested live linked worktree; new creation uses the
authoritative home even when legacy state exists; legacy and current worktrees
remain guardable, listable, and safely cleanable; direct legacy repository
roots do not recurse into source directories; arbitrary detached work is
rejected while a live rebase is accepted only transiently; prior-release hooks
remain compatible without persisting an ephemeral executable; dry runs preserve state; exact merged heads can be cleaned;
dirty or advanced branches survive; a fleet sweep writes incremental per-repository progress to stderr before its report and leaves stdout parseable as JSON; local and optional remote refs are removed
with comparison guards; interruption after worktree removal is resumed from a
durable exact-ref backlog; a removal Git unregisters but cannot finish deleting
is completed by WB and the task still reaches its branch deletion, while a
removal Git refused still fails; exact empty internal stages are archived while
non-empty ones remain blocking backlog; and apply writes durable evidence. Hosted PR metadata
MAY be supplied by a deterministic test double.

## Open Questions

- Should a future cleanup mode archive reports after a retention period?
- **Should there be an explicit, evidence-recording "retired as superseded"
  disposition for a branch whose intent landed but whose commits never can?**
  Worked example: `specscore/specscore-cli`, branch
  `codex/specscore-task-annotation-amend`
  (`/Users/alex/.wb/worktrees/specscore-task-annotation-amend/specscore/specscore-cli`),
  two commits. One commit's valuable half (the `pkg/lifecycle` primitives) was
  ported by hand into `origin/main` at `7c71397` via PR #152 — not
  patch-equivalent and not an ancestor, so no existing evidence class proves
  it. The other commit (1029 lines across `internal/cli/exclusive_publish.go`
  and `task.go`) is superseded by a different design `main` evolved
  independently (`ownedMarkerOps`, absent from the branch); the branch is 71
  commits behind and unrebasable against the design that replaced it.
  `wb worktree cleanup` correctly refuses today — "current branch head is not
  integrated into the exact origin target (awaiting push)" — and that refusal
  must not weaken. `--absorbed-by` does not cover this case either: it still
  requires the named commit to be exactly where the work entered the target,
  and here nothing entered the target from this branch at all. The gap is
  real and currently unaddressed: the only exits today are a raw
  `git branch -D` behind WB's back, or leaving the branch to rot. A candidate
  shape, informed by manually archiving this exact branch (a patch file plus
  local tag `archive/superseded/specscore-task-annotation-amend`): an
  explicit, opt-in-per-branch `retire-as-superseded` disposition that never
  claims the work landed, requires a human-supplied reason and a durable
  archive receipt (patch + tag) written *before* deletion, reports visibly
  distinct from a merged retirement, and must never be reachable from a
  fleet-wide `--all-merged`-style sweep. Not implemented; a follow-up.

---
*This document follows the https://specscore.md/feature-specification*
