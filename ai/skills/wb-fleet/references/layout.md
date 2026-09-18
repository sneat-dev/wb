# Clone layout

Canonical clones live at `{projects-root}/{host}/{owner}/{repository}` with a
real `.git` directory, where `{host}` is the literal forge hostname
(`github.com`). The local path inverts to the remote URL with no configuration
read: `{projects-root}/github.com/dal-go/dalgo` is
`https://github.com/dal-go/dalgo`. Linked worktrees are ignored.

```sh
wb layout audit --format json
wb layout clean
wb layout clean --apply
wb layout migrate
wb layout migrate --apply
```

`audit` reports, for every clone it inspects, the remote URL its path
corresponds to (`remote_url` in `--format json`/`yaml`): a clone at the host
level inverts to `https://{host}/{owner}/{repository}` by path arithmetic alone,
without reading any configuration or repository remote, while a legacy
two-level clone reports the host its origin already names — the host level it
must move under.

- `ok` — path matches origin
- `top_level` — clone sits directly under the projects root
- `misowned` — path does not match origin, or a host level is missing its
  `{owner}/{repository}`
- `bad_host` — a first-level entry that is not a valid hostname carries clones
  at the legacy `{owner}/{repository}` placement; they are still read in place
  and never removed
- `no_origin` / `unreadable` — cannot classify safely

`clean` only considers `top_level` clones. It requires a clean working tree and,
by default, an existing canonical copy. Pass `--apply` to delete; default is
dry-run. Use `--allow-missing-canonical` only when removing the sole local copy
is intentional. A legacy `{owner}/{repository}` first level is never treated as
a removable top-level clone.

`migrate [owner/repo...]` moves each legacy `{owner}/{repository}` clone under
the root to its host-level `{host}/{owner}/{repository}` placement, taking
`{host}` from the clone's `origin` remote, and repoints every linked worktree
Git has registered against it — one inside the clone (which moves with it) and
one anywhere else (which is repointed in place) alike. Dry-run by default; pass
`--apply` to move. With no arguments every legacy clone under the root is
covered; name `owner/repository` arguments to migrate only those. A clone
already at the host level is re-verified — its worktree registration must have
no missing or prunable entry — before being reported `already_done` and left
untouched; a stranded one found broken (a manual rename, or an earlier
migration interrupted before repair) is repaired and reported `repaired`, or
`failed` naming what is still wrong. This is how an interrupted or repeated
`--apply` finishes the job.

A clone is `skipped`, with a reason, when it has no usable origin, its origin
owner/repository differs from its path, its origin host is not a valid
directory name, its destination already exists, a Git operation (merge,
rebase, cherry-pick, revert, or a held index lock) is in progress or cannot be
inspected, a live Work Log claim holds it or a linked worktree (checked across
every home WB resolves, including a retired legacy one), or (Linux only;
skipped elsewhere, noted once in the report) a live process has its working
directory inside it or a linked worktree, named by PID and command. Every
refusal is re-checked immediately before that clone's actual move, not only
when the run was planned. Uncommitted changes are never a refusal reason — the
rename preserves them, and neither is an unborn `HEAD` (a repository with no
commit yet). The command exits with the findings code whenever any clone is
skipped or fails.

An un-picked-up `wb session park` bundle naming a clone or a linked worktree
as a member does NOT by itself refuse the move: `wb session resume` resolves
each member by identity (repository, branch, Work Log reference), not by its
recorded absolute paths, so it finds the member at its new location after the
clone moves. A parked member worktree inside the clone that this move
physically relocates still gets a relocation intent/receipt recorded, exactly
like any other active task's in-clone checkout (see below) — that is what lets
resume find it at its new path.

`--include-task <task>` (repeatable) and `--include-active-tasks` each lift
only the live-Work-Log-claim refusal — for the named tasks, or for every
active task. Task names are matched exactly (case-sensitive). Every other
refusal above still applies to an included clone, including the re-check
just before its move. An `--include-task` name that matches no live claim in
any home WB resolves is a usage error before anything moves, so a typo
cannot silently include nothing. The dry run marks a clone planned only
because of an inclusion with a reason like `included: active task <task>`.
When an included clone moves, its claim's relocation intent and receipt are
recorded (the same journal a finished task's relocation uses), so `land`,
`guard` and `cleanup` resolve it at its new path. An included active task's
in-clone checkout moves and repoints with its clone but is not relocated to
the store — relocation stays limited to finished tasks (see below) — and is
reported `moved-with-clone`, not `skipped`, so this is not a finding.
`--apply` records which tasks (if any) an inclusion lifted the refusal for in
its manifest; `--undo` honours exactly those, and refuses `--include-task`/
`--include-active-tasks` passed alongside it as a usage error, since undo
never accepts a new inclusion.

After moving clones (and for every clone already at the host level, moved
this run or earlier), `migrate` relocates each managed task checkout whose
placement differs from the store-mode placement — one checkout at a time, by
its exact path, using the same no-replace move, Git repair, registration
verification and relocation receipt primitives as `wb worktree relocate`,
whose own behaviour is unchanged. Central store mode
relocates to `{root}/.worktrees/{task}/{host}/{owner}/{repository}`;
repository-local store mode leaves in-clone checkouts where they are, so
nothing is relocated for such a clone. A checkout whose Work Log claim has
gone terminal (the task finished) is relocated — no live session depends on
its path any more, so this is the safe case. A checkout whose claim is still
active is left in place, with a finding reading "active task — relocate
after it finishes" (a finding, not a failure) — a live session may still be
using it. Every relocation candidate, whichever its claim's lifecycle, is
re-checked immediately before its own move: a Git operation in progress, a
live process with its working directory inside it, an un-picked-up parked
session naming it, its task lock held, or its destination already existing
each leave that one checkout in place with a finding, while its clone and
every other checkout still migrate. Uncommitted changes and unpushed commits
are never a reason to leave a checkout behind — the rename preserves them.
That per-checkout recheck is additional to, never a substitute for, the
clone-level refusal above: every clone-refusal condition (a live Work Log
claim on the clone or any of its linked worktrees, foremost) still skips the
*whole* clone, including every one of its checkouts, in every mode — a clone
move carries its in-clone checkouts with it, so moving it out from under a
live task would pull the directory out from under a running session. A
linked worktree with no WB task identity is repointed only (never relocated)
and reported `unmanaged`. Pass `--clones-only` to skip relocation and get
exactly the clone-move-and-repoint behaviour `migrate` had before this
existed.

`--apply` (migrate or undo) takes a single exclusive lock under `<root>/.wb`
for the run; a second concurrent `--apply` against the same root fails
immediately naming the conflict instead of interleaving moves. `--apply`
writes a manifest under `<root>/.wb/layout-migrations/<id>/` before its first
move, appending each clone's outcome as it completes; `--undo <id>` reverses
every clone that manifest records done, with the same repair, verification
and refusal re-checks, appending each reversal as it completes and removing
any host-level owner or host directory a reversal leaves empty. `<id>` must be
a single path segment — `.`, `..`, empty, or anything containing a path
separator is rejected before touching the filesystem. It also invalidates
WB's cached repository-path index and reports when a running daemon must be
restarted to see the moved paths.

`--undo <id>` reverses every relocation the manifest recorded as `done`
before it reverses the clone move that relocation depended on. If a
relocation cannot be safely reversed (its own refusal condition now applies,
or the reversal itself fails), that clone's move is left in place too and the
finding names why.

```sh
wb layout migrate --apply
wb layout migrate --undo 20260918T120000Z-ab12cd34
```

Fleet rollups include layout counts automatically:

```sh
wb fleet stats --format json
```
