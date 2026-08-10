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

Before creating a new worktree, WB MUST require the canonical clone to be
clean and verify the requested `refs/heads/<base>` by fetching it from
`origin`. It MUST create the feature branch from the exact verified commit,
not an unverified or moving local/remote ref. Creation MUST NOT switch, pull,
reset, or fast-forward the canonical checkout or any local base branch. A
stale local base branch or one checked out in another linked worktree is not a
blocker; an inaccessible, missing, non-commit, or otherwise unverifiable
remote base MUST fail before WB creates a branch or worktree.

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

#### REQ: coordinated-task-safety

If any repository in a task is ineligible, cleanup MUST mark every repository
in that task ineligible. It MUST preserve skipped work and explain the
blocking evidence.

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
only after proving the worktree path and registration are absent, the remote
source branch is absent, and the local ref is either absent or still equals the
recorded head. Completion MUST remain discoverable even when live worktree
inventory no longer contains the task.

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
bind exactly one successor instead of deleting it.

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

**Requirements:** worktree-lifecycle#req:offline-list-default, worktree-lifecycle#req:nonmutating-verified-base, worktree-lifecycle#req:authoritative-write-home, worktree-lifecycle#req:migration-layout-compatibility, worktree-lifecycle#req:legacy-mixed-inventory, worktree-lifecycle#req:validated-identity, worktree-lifecycle#req:guarded-transient-rebase, worktree-lifecycle#req:hook-home-stability, worktree-lifecycle#req:hook-executable-stability, worktree-lifecycle#req:dry-run-default, worktree-lifecycle#req:exact-remote-target-evidence, worktree-lifecycle#req:coordinated-task-safety, worktree-lifecycle#req:recheck-and-compare-delete, worktree-lifecycle#req:remote-opt-in, worktree-lifecycle#req:durable-audit, worktree-lifecycle#req:resumable-post-removal-backlog, worktree-lifecycle#req:internal-stage-terminalization, worktree-lifecycle#req:discarded-abort-boundary, worktree-lifecycle#req:recycle-transaction

Integration tests using real bare remotes, clones, commits, branches, merges,
linked worktrees, rebases, and refs prove that creation fetches and pins the
remote base without changing a clean canonical feature checkout or a stale
local base checked out elsewhere; new creation uses the
authoritative home even when legacy state exists; legacy and current worktrees
remain guardable, listable, and safely cleanable; direct legacy repository
roots do not recurse into source directories; arbitrary detached work is
rejected while a live rebase is accepted only transiently; prior-release hooks
remain compatible without persisting an ephemeral executable; dry runs preserve state; exact merged heads can be cleaned;
dirty or advanced branches survive; local and optional remote refs are removed
with comparison guards; interruption after worktree removal is resumed from a
durable exact-ref backlog; exact empty internal stages are archived while
non-empty ones remain blocking backlog; and apply writes durable evidence. Hosted PR metadata
MAY be supplied by a deterministic test double.

## Open Questions

- Should a future cleanup mode archive reports after a retention period?

---
*This document follows the https://specscore.md/feature-specification*
