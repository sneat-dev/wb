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

#### REQ: exact-pr-evidence

A repository MUST be eligible only when it is clean and unlocked, has no open
PR for its branch, and has a merged GitHub PR whose base and recorded head
exactly match the requested base and current local branch head. An existing
remote branch MUST still point to that exact head.

#### REQ: coordinated-task-safety

If any repository in a task is ineligible, cleanup MUST mark every repository
in that task ineligible. It MUST preserve skipped work and explain the
blocking evidence.

### Audited application

#### REQ: recheck-and-compare-delete

Before mutation, WB MUST reacquire PR and remote evidence, recheck cleanliness,
and refuse a moved head. It MUST remove only the identified linked worktree
and use a compare-and-delete operation for the exact local branch ref.

#### REQ: remote-opt-in

Remote deletion MUST require both `--apply` and `--remote`. It MUST use
force-with-lease against the observed head so an advanced branch is preserved.

#### REQ: durable-audit

An apply attempt MUST write a machine-readable plan before its first
destructive Git operation and update the same report with applied or failed
state. The report MUST retain repository, branch, head, PR URL, decision, and
outcome evidence.

## Interaction with Other Features

[Fleet Status](../fleet-status/README.md) reports canonical repository health.
Worktree Lifecycle owns the separate inventory and cleanup rules for linked
task checkouts.

## Acceptance Criteria

### AC: safe-real-git-lifecycle

**Requirements:** worktree-lifecycle#req:offline-list-default, worktree-lifecycle#req:authoritative-write-home, worktree-lifecycle#req:migration-layout-compatibility, worktree-lifecycle#req:legacy-mixed-inventory, worktree-lifecycle#req:validated-identity, worktree-lifecycle#req:guarded-transient-rebase, worktree-lifecycle#req:hook-home-stability, worktree-lifecycle#req:hook-executable-stability, worktree-lifecycle#req:dry-run-default, worktree-lifecycle#req:exact-pr-evidence, worktree-lifecycle#req:coordinated-task-safety, worktree-lifecycle#req:recheck-and-compare-delete, worktree-lifecycle#req:remote-opt-in, worktree-lifecycle#req:durable-audit

Integration tests using real bare remotes, clones, commits, branches, merges,
linked worktrees, rebases, and refs prove that new creation uses the
authoritative home even when legacy state exists; legacy and current worktrees
remain guardable, listable, and safely cleanable; direct legacy repository
roots do not recurse into source directories; arbitrary detached work is
rejected while a live rebase is accepted only transiently; prior-release hooks
remain compatible without persisting an ephemeral executable; dry runs preserve state; exact merged heads can be cleaned;
dirty or advanced branches survive; local and optional remote refs are removed
with comparison guards; and apply writes durable evidence. Hosted PR metadata
MAY be supplied by a deterministic test double.

## Open Questions

- Should a future cleanup mode archive reports after a retention period?

---
*This document follows the https://specscore.md/feature-specification*
