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

`wb worktree relocate`'s implementation MUST remain the sole supported way to move an active
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

### Clone migration

An operator adopting WB on an existing set of checkouts — or a machine laid out
before the host level existed — has canonical clones at the legacy
`<root>/<org>/<repo>` placement. WB reads that placement in place, but the
unified layout is only reached by moving the clones, and a clone cannot be moved
safely by hand: every linked worktree records the clone's absolute `.git` path.
The move is one-way by design; there is no mode that keeps the legacy placement
as a supported target.

#### REQ: clone-migration-verb

`wb layout migrate [owner/repo...]` MUST move each canonical clone found at the
legacy `<root>/<org>/<repo>` placement to `<root>/<host>/<org>/<repo>`, taking
`<host>` from the clone's `origin` remote URL. With no arguments it MUST cover
every legacy clone under the root; with arguments, only the named repositories.
It MUST be a dry run by default, printing the planned source and destination of
every clone and every linked worktree it would repoint, and MUST act only with
`--apply`. A clone already at the host level MUST be re-verified — its worktree
registry has no missing or prunable entry and every registered worktree's
common directory resolves to its `.git` — before being reported as done and
left untouched; one whose registration is broken (a directory rename outside
WB, or an earlier migration interrupted before repair) MUST be repaired and
reported `repaired`, or reported `failed` naming what is still wrong, never
silently reported done. This is how an interrupted or partial migration is
completed by running the same command again. The move MUST be a
same-filesystem rename that refuses to replace an existing destination; a
cross-device move MUST be refused, never degraded to a copy. An unborn `HEAD`
(a freshly initialized repository with no commit yet) MUST NOT be a refusal
reason, because a rename is exactly as safe for it as for any other worktree.

#### REQ: clone-migration-refusals

A clone MUST be skipped, with a finding naming the reason, and the command MUST
exit with the findings code when any of these hold: it has no usable `origin`;
the `origin` owner/repository differs from its path; the `origin` host is not a
valid directory name; the destination already exists; a Git operation is in
progress, or cannot be inspected (an index lock, or a merge, rebase,
cherry-pick or revert state); a live Work Log claim holds the clone or any of
its linked worktrees — checked across every home WB resolves for the root, not
only its current write home, since a claim recorded under a retired legacy
home is still a live task; or an un-picked-up parked session (one saved by `wb
session park` and not yet resumed) names the clone or one of its linked
worktrees as a member worktree. On an OS that exposes live process working
directories (Linux, via `/proc`), a clone MUST also be skipped when a
readable process's current working directory is inside the clone or one of
its linked worktrees, naming the PID and the command in the reason; a process
whose working directory cannot be read (another user's, or one that has since
exited) is not evidence and MUST NOT cause a refusal. On an OS with no such
mechanism, this check MUST be skipped and the run MUST say so once. Every
refusal condition MUST be re-checked immediately before each clone's move, not
only when the whole run was planned, since the interval between planning and
moving is exactly when another agent on a shared machine can start a claim, a
Git operation, a parked session, or a shell inside the clone. Uncommitted
changes MUST NOT be a refusal reason, because a rename preserves them. The
remaining clones MUST still be migrated.

#### REQ: clone-migration-repoints-worktrees

After moving a clone, every linked worktree registered with it MUST resolve in
both directions — worktrees inside the clone, which move with it, and worktrees
outside it, which stay where they are. WB MUST repoint them with Git's own
repair, then verify that the clone's worktree list has no missing or prunable
entry and that every worktree's common directory is the clone's new `.git`; a
clone that fails verification MUST be reported as failed, not done. WB MUST
regenerate the `.worktree.md` marker of the clone and of each linked worktree,
and MUST update the WB records that locate an active task's canonical clone or
checkout so guard, inventory, land and cleanup work at the new path: for each
moved worktree that carries an active Work Log claim, WB records a relocation
intent before the move and a completion receipt after it is verified — the
same append-only relocation-receipt journal `wb worktree relocate` and a
repository transfer use — so a claim's immutable, frozen `Worktree` path
resolves through that journal to its current location. Append-only historical
records (claims, receipts) MUST NOT be rewritten.

#### REQ: migration-relocates-managed-worktrees

`wb layout migrate` MUST be the single command that brings a machine to the
unified layout: by default, for every in-scope clone at the host level (moved
by this run or earlier), it MUST also relocate each WB-managed task checkout
whose placement differs from the one the user's store mode assigns — in the
default central mode, `<root>/.worktrees/<task>/<host>/<org>/<repo>`. It MUST
perform that move through the same implementation as `wb worktree relocate`
(task lock, descriptor-anchored no-replace move, Git repair, registration
verification, relocation receipt), so the two commands cannot diverge. The dry
run MUST list each planned relocation with its source and destination.
Uncommitted changes and unpushed commits MUST NOT be a reason to leave a
checkout behind — the rename preserves them exactly, the same principle
[`clone-migration-refusals`](#req-clone-migration-refusals) already applies to
a clone move.

A checkout whose Work Log claim has reached a terminal (sealed, finished-task)
lifecycle MUST be relocated: the task is done, no live session depends on its
current path, and this is the safe case, not an unsafe one. A checkout whose
claim is still active MUST be left in place, with a finding reading "active
task — relocate after it finishes" (a finding, not a failure) — a live session
may still be using it. Every relocation candidate, whichever its claim's
lifecycle, MUST be re-checked immediately before its own move, independently
of the clone-level refusal check above: a Git operation in progress, a live
process (on an OS that exposes one) with its working directory inside it, an
un-picked-up parked session naming it, its task lock held, or its relocation
destination already existing each leave that one checkout in place, with a
finding naming the reason, while its clone and every other checkout still
migrate. Every clone refusal condition in
[`clone-migration-refusals`](#req-clone-migration-refusals) still skips the
whole clone, including all of its checkouts, in every mode — this per-checkout
recheck is additional to that, never a substitute for it. A linked worktree
with no WB task identity MUST be repointed but never relocated, and MUST be
listed in the report as unmanaged. In repository-local store mode a checkout
already at `<canonical>/.worktrees/<task>` MUST NOT be moved, per
[`store-mode-is-user-policy`](#req-store-mode-is-user-policy). `--clones-only`
MUST restrict the run to clone moves and worktree repointing, and `wb worktree
relocate <task>` MUST remain available for moving one task at a time, whether
its claim is active or terminal.

#### REQ: clone-migration-manifest-and-undo

Before the first move, `--apply` MUST write a manifest of every planned clone —
source, destination, HEAD commit and linked worktree paths — under
`<root>/.wb/layout-migrations/<id>/`, and MUST append each clone's outcome as it
completes, including each worktree relocation. `wb layout migrate --undo <id>`
MUST reverse the moves that manifest records as done, worktree relocations
before the clone moves they depend on, with the same repair and verification, appending each
clone's reversal outcome to the manifest as it completes, and re-running every
refusal check against the clone's current location immediately before each
move back. A legacy owner directory, and a host-level owner or host directory
a reversed move leaves empty, MUST be removed only when empty; one that still
holds anything, including its own `.git`, MUST be kept and reported by path
and reason — every kept owner directory, not only the ones a run happens to
mention elsewhere. `--undo <id>` MUST treat `<id>` as exactly one path segment
and MUST reject anything else (empty, `.`, `..`, or containing a path
separator) before it is used to build any filesystem path. `--apply` (migrate
or undo) MUST take a single exclusive lock under `<root>/.wb` for the run's
duration; a second concurrent `--apply` against the same root MUST fail
immediately with a message naming the conflict, rather than blocking or
interleaving its moves with the first run's. The command MUST invalidate WB's
cached repository-path index, and MUST report when a running daemon has to be
restarted to pick up the new paths.

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

### AC: migrate-plans-then-moves-and-repoints

**Requirements:** projects-root-layout#req:clone-migration-verb,
projects-root-layout#req:clone-migration-repoints-worktrees

**Given** a legacy clone at `<root>/dal-go/dalgo` whose `origin` is
`github.com/dal-go/dalgo`, with one linked worktree at
`<root>/dal-go/dalgo/.worktrees/t1` and another at
`<root>/worktrees/t2/dal-go/dalgo`, and uncommitted changes in the clone
**When** `wb layout migrate` runs without `--apply`
**Then** nothing on disk changes, and the plan names the destination
`<root>/github.com/dal-go/dalgo` and both worktrees
**When** `wb layout migrate --apply` runs
**Then** the clone is at `<root>/github.com/dal-go/dalgo` with its uncommitted
changes intact; `git status` succeeds in the clone and in both worktrees;
`git worktree list` shows no missing or prunable entry; every `.worktree.md`
names the new canonical path; `<root>/dal-go` no longer exists; `wb layout
audit` exits 0; and running `wb layout migrate --apply` again reports the clone
as done and changes nothing.

### AC: migrate-skips-unsafe-clones

**Requirements:** projects-root-layout#req:clone-migration-refusals

**Given** five legacy clones — one whose `origin` owner differs from its path,
one whose destination already exists, one with a rebase in progress, one with
a live Work Log claim on a linked worktree, and one with a live process whose
working directory is inside it — plus one clean legacy clone
**When** `wb layout migrate --apply` runs
**Then** each of the five is left in place with a finding naming its reason
(the busy clone's reason naming the process's PID and command), the clean
clone is migrated, and the command exits with the findings code.

### AC: migrate-relocates-managed-worktrees

**Requirements:** projects-root-layout#req:migration-relocates-managed-worktrees

**Given** a legacy clone `<root>/dal-go/dalgo` in central store mode, with a
**finished** (terminal Work Log claim) task checkout `t1` at
`<root>/dal-go/dalgo/.worktrees/t1`, a **finished** task checkout `t2` at
`~/.wb/worktrees/t2/dal-go/dalgo`, an **active** (unsealed claim) task checkout
`t3` at `<root>/dal-go/dalgo/.worktrees/t3`, and an unmanaged linked worktree
created with plain `git worktree add`
**When** `wb layout migrate --apply` runs
**Then** the clone is at `<root>/github.com/dal-go/dalgo`, `t1` and `t2` are at
`<root>/.worktrees/<task>/github.com/dal-go/dalgo` with a relocation receipt each,
`t3` is left at its pre-migration path with a finding reading "active task —
relocate after it finishes", the unmanaged worktree is repointed in place and
listed as unmanaged, `git status` succeeds in all four, and
`<root>/github.com/dal-go/dalgo/.worktrees` no longer holds `t1` or `t2`
**When** the same setup runs with `--clones-only`
**Then** the clone moves and every worktree is repointed, and no checkout is
relocated.

### AC: migrate-is-reversible

**Requirements:** projects-root-layout#req:clone-migration-manifest-and-undo

**Given** a completed `wb layout migrate --apply` whose manifest id is `<id>`
**When** `wb layout migrate --undo <id>` runs
**Then** every clone the manifest records as done is back at its legacy path,
`git status` succeeds in each clone and each of its linked worktrees, the
manifest records the reversal as each clone completes, and any host-level
owner or host directory the reversal leaves empty is removed
**When** `wb layout migrate --undo` is given anything other than a single safe
path segment (empty, `.`, `..`, or a value containing a path separator)
**Then** the command rejects it before touching the filesystem.

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
