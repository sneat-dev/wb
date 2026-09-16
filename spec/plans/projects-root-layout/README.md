---
format: https://specscore.md/plan-specification
status: In Review
---
# Plan: Projects Root Layout: implementation and migration

**Status:** In Review
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
layout auditor (`cmd/wb/layout.go`), and a one-off migration of this machine.

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

The migration (task 8) is deliberately last and deliberately one-off. There are
no external operators to keep compatible, and the previous relocation
demonstrated that provenance is not recoverable after a move — so the migration
plans against a captured manifest, uses the existing `wb worktree relocate`
verb for checkouts rather than a bespoke script, and verifies zero stale Git
registrations before it reports success.

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
**Status:** planning

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
**Status:** planning

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
**Status:** planning

Add a store mode to the machine-local worktrees configuration. The default mode
places a checkout at `<root>/.worktrees/<task>/<host>/<org>/<repo>`; the
repository-local mode places it at `<canonical>/.worktrees/<task>`. Reject any
repository-tracked attempt to select or override either, and keep the claim,
branch and Work Log identity untouched across a mode change.

### Task 4: Retire WB_HOME with a diagnostic

**Id:** task-4
**Verifies:** projects-root-layout#ac:wb-home-ignored-with-diagnostic
**Depends-On:** task-1
**Status:** planning

Remove `WB_HOME` as a selector for the state directory. When it is set to a
non-empty value, emit a diagnostic naming the variable, the value ignored, and
the state directory in use, then continue with `<root>/.wb`. This must be a
warning rather than a silent ignore, because 361 repositories currently pin
`WB_HOME` inside WB-generated hook shims.

### Task 5: Declared writable-path preflight and actionable errors

**Id:** task-5
**Verifies:** projects-root-layout#ac:denied-write-names-the-path-and-remedy
**Depends-On:** task-1, task-3
**Status:** planning

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
**Status:** planning

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
**Status:** planning

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
**Depends-On:** task-1, task-2, task-3, task-4, task-5, task-6, task-7
**Status:** planning

One-off migration of the operator's machine, since there are no external
operators to keep compatible. Capture a manifest of every canonical clone and
managed checkout before touching anything, then: move the clones to
`<root>/github.com/<org>/<repo>`; relocate task checkouts with
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
**Status:** planning

Document the root schema, the two store modes and the writable-path contract,
including the fact that the root is the sandbox workspace root expected by the
common harnesses. Add or refresh the `ai/capabilities.json` row and the
`docs/cli-flag-matrix.md` line for every changed command leaf, as
`cmd/wb/skills_test.go` requires.

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
