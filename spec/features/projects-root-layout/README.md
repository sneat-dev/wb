---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Projects Root Layout and Worktree Placement

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/projects-root-layout?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/projects-root-layout?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/projects-root-layout?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/projects-root-layout?op=request-change) |
**Status:** Approved
**Source Ideas:** —
**Supersedes:** —
**Grade:** B

## Summary

One root, `WB_PROJECTS_ROOT`, owns everything WB needs on a machine: private
state at `<root>/.wb`, every task checkout at `<root>/.worktrees`, and the
canonical clones at `<root>/<host>/<org>/<repo>`. The two independent location
knobs (`WB_HOME` and `worktrees.root`) collapse into this single root, `WB_HOME`
is retired, and the checkout store offers two modes — central by default,
repository-local on request — so a deployment can match its sandbox boundary.
WB declares the paths it needs writable and names them when a write is denied.

## Problem

- **Two location knobs, one confusion.** `WB_HOME` names the private state
  directory while `worktrees.root` independently names the checkout root. They
  are orthogonal, but nothing in the CLI says so. A relocation that satisfied
  the sandbox constraint violated the search-hygiene constraint, and had to be
  reverted within the same session.
- **Private state sits outside the sandbox workspace.** `WB_HOME` defaults to
  `~/.wb`, which is outside a session workspace rooted at the projects
  directory. Under a `workspace-write` policy every mutating command fails:
  `openPrivateChild` issues `fchmod` on a descriptor it opened read-only, so
  even the pre-mutation "does a Work Log run exist?" check dies with
  `operation not permitted`, and the deliberate writes that follow
  (`<home>/worktrees/<task>`, `<home>/worklogs/<effort>/runs/<run>/`) would fail
  too. Worktree creation is not a pure Git operation — it takes a task lock and
  archives the original prompt before any checkout exists.
- **Repository-local checkouts poison repository-scoped search.** Measured on
  one actively-worked repository: a `find` for `.go` files returns 36,354
  against 2,273 in its canonical tree — a 15x inflation — and 97 of 374
  canonical clones carry a `.worktrees/` inside them. The polluted repositories
  are exactly the ones under active work, so the most common search scope is
  the worst affected.
- **`<org>/<repo>` is forge-scoped, not global.** The flat projects root cannot
  represent the same owner and repository name on two forges.
- **Relocation is one-way.** Moving a checkout costs one `git worktree repair`
  per worktree (measured: 172 invocations), and provenance is not recoverable
  afterwards: a live daemon rewrote directory ctimes during the move, and
  name-based attribution failed because Work Log effort IDs span roughly 2,500
  tasks against 258 worktree tasks.

## Behavior

### Root derivation

#### REQ: single-root-derivation

`WB_PROJECTS_ROOT` (env) and `--projects-root` (flag, same value, flag wins)
MUST be the single root from which every WB path is derived: private state at
`<root>/.wb`, the checkout store at `<root>/.worktrees`, and the canonical clone
of `{org}/{repo}` on host `{host}` at `<root>/{host}/{org}/{repo}`. No second
environment variable or config key may select the state directory. The daemon
runtime path MUST resolve inside `<root>/.wb/runtime`, so the same root serves
the CLI, the hooks and the daemon.

#### REQ: dot-state-and-store

The state directory and the store MUST be dot-named direct children of the
root, and MUST be siblings of the host directories rather than children of any
repository. A single-level enumeration of the root (a `*` glob, a bare `ls`, a
naive `for entry in */`) MUST therefore yield host directories only.

#### REQ: literal-host-clone-path

The first path level under the root MUST be the literal forge hostname, not an
alias, and no alias map MUST be required to interpret a path. For every
canonical clone the local path MUST be invertible to its remote URL without
reading repository config: `<root>/github.com/dal-go/dalgo` corresponds to
`https://github.com/dal-go/dalgo`.

### Store mode

#### REQ: central-store-default

Absent explicit configuration, a new checkout MUST be created at
`<root>/.worktrees/<task>/<host>/<org>/<repo>`. The task MUST be the first
level so that every checkout of a multi-repository task stays together, and a
search scoped to one task, one host, one org, or one repository across all
tasks MUST be expressible as a path.

#### REQ: repo-local-store-mode

A second supported mode MUST place the checkout at
`<canonical>/.worktrees/<task>`, sharing the canonical clone's own Git exclude.
The mode MUST exist for deployments whose sandbox grants only a single
repository directory and cannot grant `<root>/.worktrees`; selecting it MUST NOT
require any change to a task's claim, branch, or Work Log identity.

#### REQ: store-mode-is-user-policy

The store mode and, in central mode, the store root MUST be machine-local user
policy. Repository-tracked policy MUST NOT select or override either, and no
configuration change MUST move, hide, or implicitly re-select an existing
checkout.

### WB_HOME retirement

#### REQ: wb-home-retired

`WB_HOME` MUST no longer select the state directory. When it is set to a
non-empty value, WB MUST emit a diagnostic naming the variable, the value it
ignored, and the state directory actually in use, and MUST continue with
`<root>/.wb`. WB MUST NOT fall back to a legacy `<projects-root>/.wb` write
home under any circumstance.

### Writable-path contract

#### REQ: declared-writable-paths

WB MUST treat the following as its declared writable set and MUST be correct
when all of them are writable: `<root>/.wb` (state), `<root>/.worktrees`
(checkouts in central mode), `<canonical>/.git` (worktree registration — Git
writes `gitdir`, `commondir`, `HEAD`, `index`, `logs`, `refs`, `ORIG_HEAD` and
`COMMIT_EDITMSG` there on create, repair and remove), and the platform
temporary area.

#### REQ: actionable-permission-error

When a required path is not writable, WB MUST fail with a diagnostic that names
the exact path, the role it plays, and at least one remedy — widen the workspace
to `<root>`, add that path as an allowed writable root, or select
repository-local store mode. It MUST NOT surface a bare `operation not
permitted`, and it MUST NOT report a read as the failing operation when the
denied call was a metadata write on an open descriptor.

### Relocation alignment

#### REQ: relocation-targets-store

`wb worktree relocate` MUST remain the sole supported way to move an active
managed checkout, as specified by
[`worktree-lifecycle#req:explicit-layout-relocation`](../worktree-lifecycle/README.md).
Its `--to=shared` destination MUST resolve to the central store root
`<root>/.worktrees` under this layout, and its receipt MUST record the
destination in a form that stays valid if the store root is later
reconfigured.

### Compatibility

#### REQ: existing-checkouts-operable

Guard, inventory, cleanup and relocate MUST continue to validate and operate on
checkouts at previously used placements — `~/.wb/worktrees/...`, the
repository-local `<canonical>/.worktrees/<task>`, and a configured absolute
shared root — using their actual on-disk placement, without requiring that they
be moved first. Inventory MUST report placement alongside task identity so an
operator can see which layout each checkout uses.

## Acceptance Criteria

### AC: one-root-no-second-knob

**Requirements:** projects-root-layout#req:single-root-derivation

**Given** a machine with `WB_HOME` set and no `WB_PROJECTS_ROOT`
**When** any state-creating command runs
**Then** the command uses `--projects-root` when given, else
`WB_PROJECTS_ROOT`, else the default root; state is written under
`<root>/.wb`; no path is derived from `WB_HOME`; and the daemon state file
resolves to `<root>/.wb/runtime/daemon-state.json`.

### AC: root-namespace-stays-clean

**Requirements:** projects-root-layout#req:dot-state-and-store

**Given** a root containing host directories plus `.wb` and `.worktrees`
**When** the root is enumerated one level deep with a `*` glob and with a bare
`ls`
**Then** only host directories are returned, and neither `.wb` nor
`.worktrees` appears.

### AC: clone-path-inverts-to-url

**Requirements:** projects-root-layout#req:literal-host-clone-path

**Given** a canonical clone at `<root>/github.com/dal-go/dalgo`
**When** WB derives the expected remote for that path
**Then** it yields `https://github.com/dal-go/dalgo` without reading any
configuration or repository remote, and a first-level entry that is not a valid
hostname is reported as a layout finding rather than silently treated as a
forge.

### AC: central-store-is-default

**Requirements:** projects-root-layout#req:central-store-default,
projects-root-layout#req:store-mode-is-user-policy

**Given** no `worktrees.root` configuration
**When** a worktree is created for a task spanning two repositories
**Then** both checkouts land under
`<root>/.worktrees/<task>/<host>/<org>/<repo>`, the task directory contains both
hosts/orgs/repos, and a search scoped to that task directory traverses only
that task's checkouts.

### AC: repo-local-mode-selected

**Requirements:** projects-root-layout#req:repo-local-store-mode,
projects-root-layout#req:store-mode-is-user-policy

**Given** the user selects the repository-local store mode
**When** a worktree is created
**Then** the checkout lands at `<canonical>/.worktrees/<task>`, the task's
branch, immutable claim ID and Work Log identity are unchanged from central
mode, and a repository-tracked file attempting to select the mode is rejected
with an error naming the user-only configuration path.

### AC: wb-home-ignored-with-diagnostic

**Requirements:** projects-root-layout#req:wb-home-retired

**Given** `WB_HOME` set to a directory that exists and contains state
**When** a state-creating command runs
**Then** the command ignores the variable, writes under `<root>/.wb`, and emits
a diagnostic naming the ignored variable, its value, and the state directory in
use; and no command ever writes to `<projects-root>/.wb` as a fallback home.

### AC: denied-write-names-the-path-and-remedy

**Requirements:** projects-root-layout#req:declared-writable-paths,
projects-root-layout#req:actionable-permission-error

**Given** a sandbox that grants write access only to `<canonical>` and not to
`<root>/.wb` or `<root>/.worktrees`
**When** a worktree is created
**Then** the command fails with a diagnostic naming the specific unwritable
path and its role, offering the remedies of widening the workspace, adding the
path as an allowed writable root, or selecting repository-local mode; the
diagnostic does not consist solely of `operation not permitted`; and no command
reports a read failure when the denied operation was a metadata write on an
already-open descriptor.

### AC: relocate-targets-the-store

**Requirements:** projects-root-layout#req:relocation-targets-store

**Given** a managed task whose checkout is at a legacy or repository-local
placement, and a central store root of `<root>/.worktrees`
**When** `wb worktree relocate <task> --to=shared` is planned and then applied
**Then** the plan names the exact source and destination under the central store
root, the apply performs the descriptor-anchored no-replace move with Git
repair and registration verification, and the resulting receipt records a
destination that remains correct after a later reconfigure of the store root.

### AC: existing-placements-remain-operable

**Requirements:** projects-root-layout#req:existing-checkouts-operable

**Given** checkouts at `~/.wb/worktrees/...`, at
`<canonical>/.worktrees/<task>`, and at a configured absolute shared root
**When** guard, inventory, cleanup and relocate run
**Then** each checkout is found and operated on at its actual placement without
being moved first, and inventory output reports each checkout's placement
alongside its task identity.

## Open Questions

- Should the host level be enforced for a repository whose `origin` is a host
  that cannot be a directory name (for example a self-hosted forge with a port),
  or should such repositories be adoptable at an operator-chosen level?
- Should `WB_HOME` produce a warning or a hard failure once the retirement has
  shipped, given that 361 repositories currently pin it inside managed hook
  shims that WB itself generated?
- Does the store root need an override for operators who must place checkouts on
  a different volume, accepting that a rename becomes a copy?

## Interaction with Other Features

[Worktree Lifecycle](../worktree-lifecycle/README.md) owns worktree creation,
guard, inventory, cleanup and the `relocate` verb; this Feature changes the root
and store that those operations derive their paths from, and amends that
Feature's layout requirements in place. [Fleet Status](../fleet-status/README.md)
reports canonical repository health and therefore depends on the clone path
shape defined here.

---
*This document follows the https://specscore.md/feature-specification*
