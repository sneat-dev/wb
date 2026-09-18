---
format: https://specscore.md/plan-specification
status: Blocked
---
# Plan: Projects Root Layout: implementation and migration

**Status:** Blocked
**Source Feature:** projects-root-layout
**Date:** 2026-09-16
**Owner:** trakhimenok
**Supersedes:** —

## Summary

Implements the one-root schema: `WB_PROJECTS_ROOT` becomes the single root from
which private state (`<root>/.wb`), the checkout store (`<root>/.worktrees`) and
the canonical clones (`<root>/<host>/<org>/<repo>`) are derived; `WB_HOME` is
retired; the store gains a central default plus a repository-local mode; and WB
declares and preflights the paths it needs writable. The work spans the path
resolver (`internal/wbhome`), the worktree subsystem (`internal/worktrees`,
including the existing `relocate` verb), the daemon's runtime derivation, the
layout auditor (`cmd/wb/layout.go`), a `wb layout migrate` verb for legacy
clones, and the migration of this machine.

## Approach

Six implementation tasks land the behaviour, then the specification tree is
amended, then the machine is migrated. The ordering is dictated by four
constraints:

1. **Derivation first.** Every other requirement resolves paths through the root,
   so the root derivation (task 1) must exist before anything can be tested
   against it.
2. **The host level before any clone moves.** Introducing `<host>` changes every
   canonical path. It must land — together with the layout auditor that learns
   the new shape (task 2) — before the migration moves a single clone, or the
   auditor would report the whole fleet as misowned mid-move.
3. **Modes before the permission contract.** The third remedy in the actionable
   permission error is "select repository-local mode", so both modes (task 3)
   must exist before the preflight can offer it (task 5).
4. **Specification amendments after the behaviour, never before.** Worktree
   Lifecycle's four layout requirements are accurate until the schema ships.
   Amending them earlier would make the spec tree describe a system that does
   not exist, so task 7 is gated on tasks 1–3 and 6.

The migration of this machine (task 8) is deliberately last, and it runs a verb
rather than a script (task 10): any operator adopting WB on existing checkouts
needs the same move, and the previous relocation demonstrated that provenance
is not recoverable after a move — so `wb layout migrate` plans against a
captured manifest, repoints linked worktrees with Git's own repair, and verifies
zero stale Git registrations before it reports a clone done; task checkouts
still move with the existing `wb worktree relocate` verb.

`wb worktree relocate` is already implemented and already specifies plan/apply,
task locking, descriptor-anchored no-replace move, Git repair, append-only
receipts and recovery. This plan retargets it; it does not rebuild it.

Statement coverage must stay at or above the floor in
`.github/workflows/go-ci.yml`. Every task that changes a public command leaf
also needs its `ai/capabilities.json` row and `docs/cli-flag-matrix.md` line
updated — `cmd/wb/skills_test.go` enforces both.

## Tasks

### Task 1: Derive state and store from one root

**Id:** task-1
**Verifies:** projects-root-layout#ac:one-root-no-second-knob, projects-root-layout#ac:root-namespace-stays-clean
**Depends-On:** —
**Status:** complete

Replace `writeHome()`'s `WB_HOME` branch with a root derivation: read
`WB_PROJECTS_ROOT` (env) and `--projects-root` (flag wins), then derive state at
`<root>/.wb` and the store at `<root>/.worktrees`. Point the daemon's runtime
paths at `<root>/.wb/runtime`, and delete the legacy `<projects-root>/.wb`
write-home fallback so no command can silently adopt it. Tests must prove that a
`*` glob and a bare `ls` of the root return host directories only.

### Task 2: Add the literal host level to clone derivation

**Id:** task-2
**Verifies:** projects-root-layout#ac:clone-path-inverts-to-url
**Depends-On:** task-1
**Status:** complete

Extend `canonicalRepositoryPath` and `splitRepository` to derive
`<root>/<host>/<org>/<repo>`, taking the host from the repository's `origin`
remote. Rewire the 35 call sites, update the `--projects-root` help text from
`{org}/{repo}` to `{host}/{org}/{repo}`, and teach `wb layout audit|clean` the
new shape so a correctly-place clone is not reported as misowned. Add the
inverse mapping (local path to remote URL) and report a first-level entry that
is not a valid hostname as a layout finding rather than treating it as a forge.

### Task 3: Store modes — central by default, repository-local opt-in

**Id:** task-3
**Verifies:** projects-root-layout#ac:central-store-is-default, projects-root-layout#ac:repo-local-mode-selected
**Depends-On:** task-1
**Status:** complete

Add a store mode to the machine-local worktrees configuration. The default mode
places a checkout at `<root>/.worktrees/<task>/<host>/<org>/<repo>`; the
repository-local mode places it at `<canonical>/.worktrees/<task>`. Reject any
repository-tracked attempt to select or override either, and keep the claim,
branch and Work Log identity untouched across a mode change.

### Task 4: Retire WB_HOME with a diagnostic

**Id:** task-4
**Verifies:** projects-root-layout#ac:wb-home-ignored-with-diagnostic
**Depends-On:** task-1
**Status:** complete

Remove `WB_HOME` as a selector for the state directory. When it is set to a
non-empty value, emit a diagnostic naming the variable, the value ignored, and
the state directory in use, then continue with `<root>/.wb`. This must be a
warning rather than a silent ignore, because 361 repositories currently pin
`WB_HOME` inside WB-generated hook shims.

### Task 5: Declared writable-path preflight and actionable errors

**Id:** task-5
**Verifies:** projects-root-layout#ac:denied-write-names-the-path-and-remedy
**Depends-On:** task-1, task-3
**Status:** complete

Preflight the declared writable set — `<root>/.wb`, `<root>/.worktrees`,
`<canonical>/.git` and the platform temporary area — and fail with a
diagnostic that names the unwritable path, its role, and the remedies (widen
the workspace, add the path as an allowed writable root, or select
repository-local mode). Stop `openPrivateChild` issuing `fchmod` on a
descriptor it opened read-only, so a read path can no longer be reported as a
denied write. Tests must reproduce the denial without relying on a real
sandbox, and must assert the diagnostic is not a bare `operation not
permitted`.

### Task 6: Retarget relocate to the central store

**Id:** task-6
**Verifies:** projects-root-layout#ac:relocate-targets-the-store
**Depends-On:** task-1, task-2, task-3
**Status:** complete

Point `wb worktree relocate --to=shared` at `<root>/.worktrees` and make its
destination resolution understand the host level. Record the destination in the
relocation receipt in a form that stays valid if the store root is later
reconfigured, so a receipt written before a reconfigure is not invalidated by
it. The existing plan/apply, locking, no-replace move, repair and recovery
behaviour is unchanged.

### Task 7: Amend Worktree Lifecycle's four layout requirements

**Id:** task-7
**Verifies:** projects-root-layout#ac:existing-placements-remain-operable
**Depends-On:** task-1, task-2, task-3, task-6
**Status:** complete

Revise `authoritative-write-home`, `local-default-and-user-shared-root`,
`migration-layout-compatibility` and `legacy-mixed-inventory` in
`spec/features/worktree-lifecycle/README.md` to defer to this Feature's root,
store and placement rules, and add the host level to the inventory's recognized
placements. That Feature is `Implementing`, so re-run `specscore spec lint` and
re-gate it after the edit. Also confirm guard, inventory, cleanup and relocate
still operate on checkouts at `~/.wb/worktrees/...`, repository-local and
configured-shared placements without moving them, and that inventory reports
each checkout's placement alongside its task identity.

### Task 8: Migrate this machine — clones, checkouts, hooks, daemon

**Id:** task-8
**Verifies:** projects-root-layout#ac:existing-placements-remain-operable, projects-root-layout#ac:wb-home-ignored-with-diagnostic
**Depends-On:** task-1, task-2, task-3, task-4, task-5, task-6, task-7, task-10, task-11, task-12, task-13
**Status:** blocked

**Blocked by:** the operator deferred this one-off machine migration until the
active agent sessions that depend on the current checkout paths have been shut
down. Running it now would move canonical clones and task checkouts out from
under live sessions. Tasks 1-7 and 9 land the behaviour and the dry-runnable
plan; this task is the only one that touches this machine.

Migration of the operator's machine, run with the task-10 verb rather than a
bespoke script. `wb layout migrate` (dry run, then `--apply`) captures the
manifest, moves the clones to `<root>/github.com/<org>/<repo>` and repoints
their linked worktrees; then relocate task checkouts with
`wb worktree relocate --to=shared` rather than a bespoke script; regenerate the
hook shims that pin `WB_HOME`; update the daemon's launchd plist and the
harness configuration files that hardcode `~/.wb/...` paths; and verify zero
stale Git registrations, zero unreadable checkouts, and that no writer other
than the daemon's own runtime remains inside the root. Because provenance is not
recoverable after a move, the manifest is the rollback artifact and the
migration must be dry-runnable.

### Task 9: Document the schema and update capability surfaces

**Id:** task-9
**Verifies:** projects-root-layout#ac:one-root-no-second-knob, projects-root-layout#ac:clone-path-inverts-to-url
**Depends-On:** task-1, task-2, task-3
**Status:** complete

Document the root schema, the two store modes and the writable-path contract,
including the fact that the root is the sandbox workspace root expected by the
common harnesses. Add or refresh the `ai/capabilities.json` row and the
`docs/cli-flag-matrix.md` line for every changed command leaf, as
`cmd/wb/skills_test.go` requires.

### Task 10: `wb layout migrate` — move legacy clones and repoint worktrees

**Id:** task-10
**Verifies:** projects-root-layout#ac:migrate-plans-then-moves-and-repoints, projects-root-layout#ac:migrate-skips-unsafe-clones, projects-root-layout#ac:migrate-is-reversible
**Depends-On:** task-2, task-6
**Status:** complete

Add `wb layout migrate [owner/repo...] [--apply] [--undo <id>]` beside `audit`
and `clean`, implemented in `internal/layout` on the existing `repopath`
host-level resolution and the no-replace move primitive `wb worktree relocate`
already uses. The verb exists for any operator adopting WB on existing
checkouts, not only for this machine. Per clone: refuse the unsafe cases the
Feature lists, rename the clone to its host-level path, run `git worktree
repair` with every linked worktree's post-move path, verify the worktree list
and each worktree's common directory, regenerate `.worktree.md` markers, update
the WB records that locate active tasks, and append the outcome to the manifest
under `<root>/.wb/layout-migrations/<id>/`. Remove emptied legacy owner
directories, invalidate the cached repository-path index, and report when the
daemon needs a restart. Tests build real Git repositories with in-clone and
external linked worktrees.

### Task 11: `wb layout migrate` also relocates managed worktrees

**Id:** task-11
**Verifies:** projects-root-layout#ac:migrate-relocates-managed-worktrees
**Depends-On:** task-10
**Status:** complete

Make `wb layout migrate` the one command for the unified layout: after the clone
step, relocate each managed task checkout to its store-mode placement by calling
the `wb worktree relocate` implementation, not a copy of it. Add `--clones-only`
to skip relocation. Relocations appear in the dry run and in the manifest, and
`--undo` reverses them. Unmanaged worktrees are repointed and reported, never
relocated. Repository-local store mode leaves in-clone checkouts where they are.

### Task 12: `--include-task` / `--include-active-tasks`, and markers after relocation

**Id:** task-12
**Verifies:** projects-root-layout#ac:migrate-includes-named-active-tasks
**Depends-On:** task-11
**Status:** complete

Let the operator lift only the live-claim refusal of `wb layout migrate`, per
task or for all tasks. Unknown task names are a usage error. The busy-process,
parked-session and Git-operation checks still apply. Included claims get
relocation receipts, so the task resolves at its new path. Also regenerate a
relocated checkout's `.worktree.md` after the relocation: the vm1 test on
2026-09-18 left it naming the pre-relocation path.

### Task 13: Parked sessions resolve members by identity; migrate stops refusing them

**Id:** task-13
**Verifies:** projects-root-layout#ac:migrate-moves-clones-referenced-by-parked-sessions
**Depends-On:** task-12
**Status:** queued

Parked-session resume identifies each member by repository, branch and Work Log
reference, and resolves the canonical clone through `repopath` and moved
checkouts through relocation receipts, instead of comparing the stored absolute
`canonical_dir`, `worktree_dir` and `worktrees_root`
(`internal/worktrees/session_park_local.go`, `acquire`). Migrate drops the
parked-session clone refusal and records a relocation receipt for each parked
member worktree it moves. On vm1 on 2026-09-18 this refusal was the only thing
keeping dal-go/dalgo, dalgo2firestore, dalgo2sql and dalgo2sqlite from
migrating.

## Open Questions

- Should the host level be enforced for a repository whose `origin` is a host
  that cannot be a directory name, such as a self-hosted forge with a port? If
  so, task 2 needs an adoption rule for those repositories.
- Should `WB_HOME` escalate from a warning to a hard failure once the retirement
  has shipped? Task 4 implements the warning; the escalation is a follow-up.
- Does the store root need an override for operators who must place checkouts on
  a different volume, accepting that a rename degrades to a copy? This would
  reintroduce a second location knob in a limited form.

---
*This document follows the https://specscore.md/plan-specification*
