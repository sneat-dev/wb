---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Worktree Lifecycle

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-lifecycle?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb worktree list` reports WB-managed task worktrees from local Git data, with
optional GitHub PR evidence. `wb worktree cleanup` safely plans or applies
removal of clean task worktrees and exact merged branch refs.

## Problem

Central worktrees protect canonical clones, but completed tasks accumulate
linked checkouts and branches. Ad-hoc cleanup can discard uncommitted work,
delete a reused branch, or remove one repository while a coordinated task is
still active elsewhere.

## Behavior

### Fast local inventory

#### REQ: offline-list-default

`wb worktree list [task]` MUST inspect only the hierarchy below
`<projects-root>/.wb/worktrees` and local Git state by default. It MUST contact
GitHub and the remote only when `--github` is explicit.

#### REQ: validated-identity

Each result MUST be a real linked worktree at the expected task, owner, and
repository path, backed by the expected canonical clone. Results MUST include
task, repository, branch, head, cleanliness, lock state, last commit time, and
local merge state.

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

**Requirements:** worktree-lifecycle#req:offline-list-default, worktree-lifecycle#req:validated-identity, worktree-lifecycle#req:dry-run-default, worktree-lifecycle#req:exact-pr-evidence, worktree-lifecycle#req:coordinated-task-safety, worktree-lifecycle#req:recheck-and-compare-delete, worktree-lifecycle#req:remote-opt-in, worktree-lifecycle#req:durable-audit

Integration tests using real bare remotes, clones, commits, branches, merges,
linked worktrees, and refs prove that dry runs preserve state; exact merged
heads can be cleaned; dirty or advanced branches survive; local and optional
remote refs are removed with comparison guards; and apply writes durable
evidence. Hosted PR metadata MAY be supplied by a deterministic test double.

## Open Questions

- Should a future cleanup mode archive reports after a retention period?

---
*This document follows the https://specscore.md/feature-specification*
