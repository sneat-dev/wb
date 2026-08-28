---
format: https://specscore.md/plan-specification
status: Approved
---
# Plan: WB Home Migration and Worktree Guard Hardening

**Status:** Approved
**Source Feature:** worktree-lifecycle
**Date:** 2026-07-28
**Owner:** codex
**Supersedes:** —

**Issue Cross-References:** [#33](https://github.com/sneat-dev/wb/issues/33), [#34](https://github.com/sneat-dev/wb/issues/34)

## Summary

Make `~/.wb` the unambiguous default write location while keeping existing
legacy worktrees safe and operable during migration. Harden the worktree guard
and its managed hooks so only a verified in-progress rebase may transiently be
detached, and make mixed historic inventory deterministic.

## Approach

Start with one resolver that exposes a single write layout plus compatible read
layouts. Use it everywhere that creates, guards, inventories, cleans, or
installs a hook. Then replace directory-shape assumptions with Git-root-aware
inventory, add real-Git transient-rebase tests, and finish with an upgrade
fixture and user-facing documentation. This sequence keeps compatibility logic
central rather than teaching each command its own migration rule.

## Tasks

### Task 1: Centralize home and migration layouts

**Id:** task-1
**Verifies:** worktree-lifecycle#ac:safe-real-git-lifecycle
**Depends-On:** —
**Status:** planning

Introduce a shared resolver whose write home is `~/.wb` by default or exact
`WB_HOME` when explicit, and whose compatible layouts include legacy state only
for the default migration path. Route create, guard, list, cleanup, reports,
and hook installation through it. Cover a populated legacy directory and an
explicit-home override; addresses #33.

### Task 2: Make inventory and cleanup layout-aware

**Id:** task-2
**Verifies:** worktree-lifecycle#ac:safe-real-git-lifecycle
**Depends-On:** task-1
**Status:** planning

Recognize both `<task>/<repository>` and `<task>/<owner>/<repository>` legacy
shapes using Git-root-aware traversal that stops at a repository root. Preserve
valid siblings and deterministic diagnostics when a candidate is malformed,
then prove legacy linked worktrees can still be listed and cleaned; addresses
#34.

### Task 3: Preserve guard safety through transient rebases and hooks

**Id:** task-3
**Verifies:** worktree-lifecycle#ac:safe-real-git-lifecycle
**Depends-On:** task-1
**Status:** planning

Allow detached HEAD only when an actual `rebase-merge` or `rebase-apply` state
is active in a correctly located linked worktree, while retaining all
canonical/common-directory checks. Persist the resolved WB home in new managed
hook shims, reject transient `go run` executables, and prove old shims remain
compatible after upgrade. Prove canonical rescue publishes only an attested
single rescue ref whose exact commit captures the complete dirty clone through
the real installed pre-push hook; addresses #33.

### Task 4: Verify prior-release migration end to end

**Id:** task-4
**Verifies:** worktree-lifecycle#ac:safe-real-git-lifecycle
**Depends-On:** task-1, task-2, task-3
**Status:** planning

Build a real-Git fixture representing the previous release's legacy home and
hook profile, then exercise candidate create, guard, list, transient rebase,
commit, and safe cleanup. Update README and WB worktree/hook skills with the
authoritative-home and compatibility behavior; addresses #33 and #34.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
