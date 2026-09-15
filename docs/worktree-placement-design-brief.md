# Agent Worktree Placement — Problem Specification and Design Space

**Status:** evidence-gathered design brief, no decision taken
**Date:** 2026-09-15
**Audience:** a frontier model asked to analyse the design space and recommend an architecture
**Scope:** where parallel-agent git worktrees live on disk, how their paths are registered, where coordination state lives, and how the sandbox boundary interacts with all three

---

## 0. How to use this document

- **§1–§2** state the system and the problems. §2 is the specification proper.
- **§3** is measured evidence. Every number is reproducible; commands are in §8.
- **§4** is the option space, split by sub-decision, each option with pros/cons.
- **§5** is the invariants any solution must hold. **§6** is suggested evaluation criteria.
- **§7** lists what is *unknown* — please treat these as explicit gaps, not as resolved.

**Evidence discipline.** Claims are tagged:
- `[MEASURED]` — observed on the machine described in §8.1, with a reproduction command.
- `[SOURCE]` — read from the tool's own source code or documentation (path/URL given).
- `[RECORDED]` — from a project's own commit message or issue, i.e. a historical report.
- `[ASSUMED]` — reasoning not verified. **Challenge these first.**

The author of this brief has a documented bias: they recently executed a relocation that had to be reverted. §3.6 records that episode because it is direct evidence about relocation cost, not because it recommends anything.

---

## 1. System context

### 1.1 The agents and the CLI

`wb` ("Workbench") coordinates many `{org}/{repo}` git repositories for many parallel AI agents. Its model:

- A **canonical clone** per repository at `<projects-root>/<org>/<repo>`. Treated as read-only: agents must never write there.
- **Worktrees** per task. One task may span several repositories (a multi-repo task has one checkout per repo).
- **Private coordination state**: an immutable per-task *claim*, an *original prompt archive*, work logs, operation locks, receipts, hook runtime state.

### 1.2 The two independent location knobs

| Knob | Meaning | Default | Configured by |
|---|---|---|---|
| `WB_HOME` | private authority: claims, locks, Work Logs, prompt archives, receipts, hook runtime | `$HOME/.wb` | env `WB_HOME` |
| `worktrees.root` | where the *checkout* physically lands | repository-local `<canonical>/.worktrees/<task>` | `~/.config/wb/worktrees.yaml` |

`[SOURCE]` `internal/worktrees/worktrees.go:447-457`, `internal/worktrees/branch_config.go:220-247`, `internal/wbhome/wbhome.go:167-185`.

**These are orthogonal and are frequently conflated.** Moving checkouts does not move coordination state, and vice versa. Much of the confusion this brief addresses came from treating them as one knob.

### 1.3 Layouts the resolver understands

`[SOURCE]` `internal/wbhome/wbhome.go`:

```go
type Layout struct {
    Home          string
    WorktreesRoot string
    Legacy        bool   // historic <projects-root>/.wb, readable only
    Local         bool   // canonical repository's own <canonical>/.worktrees
}
```

- Write layout is always exactly one (`WB_HOME`, or `$HOME/.wb` by default).
- `Resolve()` appends the historic `<projects-root>/.wb` as an *additional readable* layout **if and only if** `<projects-root>/.wb/worktrees` exists (`wbhome.go:73`, `hasWorktrees()` at `:208`).
- Consequence: an empty-but-present legacy `worktrees/` directory silently keeps the legacy root in every command's read set — and several commands that iterate the read set also clean and repair there. **A directory whose existence changes behaviour is an implicit configuration bit.**

### 1.4 Sandbox model

`[SOURCE]` `@deepseek-ai/dsh-sandbox`, `lib/index.js:155`:

```js
function writableRoots(policy) {
  if (policy.mode !== "workspace-write") return [];
  return [...new Set([policy.workspaceRoot, "/tmp", tmpdir()].map(canonicalPath))];
}
```

The `workspace-write` writable set is **hardcoded** to `{workspaceRoot, /tmp, os.tmpdir()}`. There is no documented configuration for additional writable roots. The same function feeds both the shell (Seatbelt) profile and the in-process filesystem fence.

On the affected machine the session's workspace root is `/Users/alex/projects` — **which is also the projects root** (the directory holding all canonical clones). This coincidence is the crux of §2.2.

---

## 2. Problem specification

### P1 — Recursive-tool exposure / double counting

**Statement.** Worktrees are full working copies of repositories. Any tool that walks a tree containing them sees N copies of the same repository instead of one.

**Mechanism.** No filesystem-level distinction exists between "a repository" and "a worktree of a repository" — both are directories containing a `.git` entry. Distinguishing them requires either reading the `.git` *file* (a linked worktree's `.git` is a file containing `gitdir: …/.git/worktrees/<id>`; a canonical clone's `.git` is a directory) or honouring exclusion rules.

`[RECORDED]` Commit `6513022b` in the affected project, subject *"feat(worktree): move WB's shared state to ~/.wb, off the projects tree (#32)"*:

> Grepping `~/projects` for dalgo consumers earlier this session **double-counted five sneat-go worktrees as separate repositories** — a naive recursive tool has no way to know WB's exclusion rules and walks straight into its scratch space. wb itself is immune (dot-prefix skip, .git-must-be-a-directory check), but **ripgrep, IDE indexers, backup and ad-hoc scripts are not**.

The same message records "~165 of them exist under `~/projects/.wb` at the moment".

**Correction to the historical report.** `[MEASURED]` `rg` (ripgrep) **does** skip dot-directories by default; so do BSD `ls -R` and Python `glob('**')`. The tools that descend are `grep -r`, `find`, `os.walk`, `tar`, `rsync`, `cp -R`, `du -a` (§3.2). The commit's "ripgrep" attribution is therefore loose — plausible real paths are `rg --hidden` / `rg -uu`, or a non-ripgrep tool. **A model analysing this should not accept "ripgrep" as the culprit class.**

**Why it matters.** Silent wrong answers (a "5 consumers found" that is really 1 consumer in 5 checkouts), inflated search results, and search/index time proportional to the number of live agents.

### P2 — Sandbox/workspace boundary mismatch

**Statement.** The store must be *inside* the write-allowed workspace, but the workspace is *identical to* the tree that must not be scanned. These two requirements are unsatisfiable simultaneously when `workspaceRoot == projectsRoot`.

**Mechanism.** `[MEASURED]` With `workspace-write`:
- `touch ~/.wb/.__probe` → `EPERM`
- `touch ~/projects/.__probe` → allowed
- `touch /tmp/.__probe` → allowed

So a store at `$HOME/.wb` is unwritable; a store under `/Users/alex/projects` is writable but re-enters the scanned tree (P1).

**Why it matters.** This is the forcing constraint. Any architecture that does not address it will oscillate: move the store out to satisfy P1, break writes to satisfy P2, move it back, reintroduce P1. That oscillation was observed.

### P3 — Relocation fragility

**Statement.** A linked worktree is registered in two places, both absolute by default:

1. `<checkout>/.git` (a file) → `gitdir: <canonical>/.git/worktrees/<id>`
2. `<canonical>/.git/worktrees/<id>/gitdir` → `<checkout>/.git` (**absolute**)

`[MEASURED]` sample registration: `gitdir: /Users/alex/projects/sneat-dev/wb/.git/worktrees/checkout33`

**Mechanism.** Moving a checkout leaves (2) stale. Git then reports the worktree as `prunable` at the old path, and multi-worktree operations can misbehave. Repair is `git -C <new-checkout> worktree repair`, or `git -C <canonical> worktree repair <new-path>`; both were verified to work `[MEASURED]`.

**Aggravating factors:**
- Repair is per-worktree: a move of K worktrees costs K `git` invocations (measured: 172 invocations, a few minutes).
- Repair silently fails when the canonical repository is gone (library deleted, archived, renamed). Measured: 1 of 214 worktrees was unrepairable — a quarantined `.wb-retired-stage-*/.wb-retired-checkout-*` whose admin directory had already been pruned.
- A plain `mv` gives no signal that repair is needed. The failure is deferred and visible only in later `git worktree list` output.

**Mitigation available:** Git ≥2.48 supports **relative** worktree links. `[SOURCE]` `git help config`:

> `relativeWorktrees` — If enabled, indicates at least one worktree has been linked with relative paths. Automatically set if a worktree has been created or repaired with either the `--relative-paths` option or with the `worktree.useRelativePaths` config set to `true`.

`[MEASURED]` host git is `2.54.0 (Apple Git-157)`; `extensions.relativeWorktrees` is currently unset. With relative links, moving a whole tree *together with its canonical repo* requires no repair. Moving them independently still does.

### P4 — Path pinning and symlink resolution

**Statement.** Tools bake the *resolved* home path into generated artifacts, so a symlinked home silently becomes a permanent absolute pin.

**Mechanism.** `[SOURCE]` `internal/wbhome/wbhome.go:221` `resolveAbs()` calls `filepath.EvalSymlinks`. The comment explains why: macOS `/var` → `/private/var` would otherwise make WB's bookkeeping disagree with `git rev-parse --show-toplevel` for the same directory.

Registered hook shims then contain `[MEASURED]`:

```sh
export WB_HOME='/Users/alex/.wb'
export WB_HOME_MIGRATION_COMPAT='/Users/alex/.wb'
```

**Failure observed** `[MEASURED]`: while `~/.wb` was briefly a symlink to `~/projects/.wb`, a `wb hooks repair` run baked `WB_HOME='/Users/alex/projects/.wb'` into shims for 3 repositories (out of 361). Those shims then exported the wrong home on **every commit, checkout and push**, writing state into the projects tree. This was invisible until a filesystem mtime watch caught a directory being recreated.

**Why it matters.** Configuration that is *materialised* into many generated files cannot be changed centrally. A directory move becomes a fleet-wide regeneration problem with no inventory.

### P5 — Implicit legacy discovery as a write path

**Statement.** As in §1.3: a legacy root is read-discovered purely from the existence of a subdirectory, and commands that iterate the read set also clean/repair there.

`[SOURCE]` commands iterating `resolution.Read`: `internal/worktrees/lifecycle.go:1071`, `:2207`, `:2310`, `:5120`; `internal/worktrees/orphans.go:148` explicitly builds `legacyRoot := filepath.Join(projectsRoot, ".wb", "worktrees")`.

**Failure observed** `[MEASURED]`: after emptying the legacy `worktrees/`, a 60-second mtime watch over the legacy root showed **zero** writes. Before removal, the same watch and a directory mtime showed the legacy root being repopulated.

### P6 — Read paths that write

**Statement.** A read-only-looking operation performed a metadata write, so a confined environment reported a *read* as denied.

`[SOURCE]` `internal/worktrees/worklog.go:3257` `openPrivateChild(parent, name, create bool)`:

```go
if create { fd, err = openOrCreateNoFollowDirectory(...) } else { fd, err = unix.Openat(..., O_RDONLY|O_DIRECTORY|O_NOFOLLOW, 0) }
...
if err := unix.Fchmod(fd, 0o700); err != nil {   // runs on the read path too
```

`[MEASURED]` under `workspace-write`:
- `openat ~/.wb/worklogs O_RDONLY|O_DIRECTORY|O_NOFOLLOW` → **OK**
- `fchmod(fd, 0700)` on that descriptor → **EPERM**

Resulting user-visible error: `inspect existing work-log run before mutation: operation not permitted` — reported for what is logically an existence check. **Note:** `chmod(1)` on an already-correct mode appears to succeed because the utility skips a no-op mode change; the syscall is still denied. This makes naive manual reproduction misleading.

**Why it matters.** Confinement turns "check then act" reads into hard failures; error messages misattribute the cause; and the same pattern will affect any metadata write (chmod, chown, xattr, utimes) on a read path.

### P7 — Lifecycle, locking and GC

- Claims and locks are keyed to a *task*, but the checkout is keyed to *(task, org, repo)*. Path and claim can disagree after a config change, which is why lookup is claim-first (`[SOURCE]` `locateResumableWorktree`, `worktrees.go:557`).
- Retirement uses quarantine directories in the worktrees root (`.wb-retired-stage-*`, `.wb-retired-checkout-*`), which are themselves scannable and movable — and are the source of the 1 unrepairable registration in P3.
- Concurrent creation for the same task slug is serialised by a per-task lock; losers must not durably reserve Work Log state (`[SOURCE]` `worktrees.go:478-525`).
- **Open:** no measured data on GC latency or orphan rate at fleet scale.

### P8 — Multi-repo tasks

One task = one claim = N checkouts. Placement policy must be uniform across N, and a relocation must move all N or none, or the task is left half-moved. Observed shared layout is `<root>/<task>/<org>/<repo>`; observed repository-local layout is `<canonical>/.worktrees/<task>` (one checkout per repo, so no task-level directory exists).

### P9 — Cost

`[MEASURED]`: canonical clones 388; linked worktrees 193 (repo-local) + 214 (central home) across the fleet inventory. Central home is 59 GB total, of which 53 GB is worktrees; the previous legacy home held 8.6 GB of worktrees. Working trees dominate disk; the git object store is shared. Package-manager caches (`node_modules`, Go build/module cache) are the other large term and are *not* fixed by placement — though content-addressed stores (pnpm, GOMODCACHE) make them largely shared.

### P10 — Discovery, ownership and concurrent agents

There is no single index of "which checkouts exist and who owns them" that survives a placement change. Ownership is reconstructed from claims (`[SOURCE]` `activeWorkLogClaim`), plus the checkout-local `.worktree.md` marker and `.wb/local/manifest.yaml`. Measured `[MEASURED]`: reconstructing the provenance of 214 worktrees after a move required replaying a tool's own apply log, because the filesystem held no provenance.

### P11 — Harness session-state collisions (analogous problem)

`[SOURCE]` [Codex CLI issue #11435](https://github.com/openai/codex/issues/11435), as reported in [this write-up](https://www.frr.dev/posts/codex-cli-worktrees-manual-parallelism/): parallel Codex instances share `~/.codex/` and can load each other's session context. The workaround is per-instance `CODEX_HOME`. This is the same shape as P1/P10 — *shared mutable state keyed too coarsely* — one level up from worktrees. Placement decisions should consider it.

### P12 — Backup and indexer exposure

Whole-tree copiers have no dot-awareness: `[MEASURED]` `tar`, `rsync`, `cp -R`, `du -a` all include dot-directories. Time Machine, Arq, restic and borg are in the same class `[ASSUMED]` (not tested). IDE indexers are configuration-dependent and **untested** — JetBrains indexes everything unless excluded; VS Code's search is ripgrep-backed and honours git ignore files `[ASSUMED]`.

---

## 3. Measured evidence

### 3.1 Fleet shape `[MEASURED]`

| Quantity | Value |
|---|---|
| Canonical clones under projects root | 388 |
| Linked worktrees in `<repo>/.worktrees/` | 193 |
| Working copies a non-dot-aware walk sees | 581 |
| Directories named `sneat-go` under projects root | 21 |
| Repos with `/.worktrees/` in `.git/info/exclude` | 372 |
| Repos with `/.worktree.md` excluded | 372 |
| Repos with `.codegraph/` excluded | 367 |
| Repos with `/.wb/local/` excluded | 211 |
| Repos with `/.wb-worklog/` excluded | 201 |
| Repos with `.claude/worktrees/` excluded | 5 (+2 as `**/.claude/worktrees/`) |

The exclude inventory is a fossil record of every tool that has needed to hide its scratch space from git-aware tooling.

### 3.2 Tool behaviour probe `[MEASURED]`

Probe: two identical files, one at `plain/repo/marker.txt`, one at `.hidden/repo/marker.txt`; search for a unique token.

| Tool | Descends into dot-dirs by default? |
|---|---|
| `rg` (ripgrep) | **no** |
| macOS/BSD `ls -R` | **no** |
| Python `glob('**')` | **no** |
| `grep -r` | **yes** |
| `find` | **yes** |
| Python `os.walk` | **yes** |
| `tar` | **yes** |
| `rsync` | **yes** |
| `cp -R` | **yes** |
| `du -a` | **yes** |
| `fd`, `ag`, `ack` | not installed; convention is "no" `[ASSUMED]` |

**Second fence (git excludes).** Inside a canonical clone with 42 worktrees under `.worktrees/`:

| Command | `.worktrees` entries seen |
|---|---|
| `rg --files \| grep -c '^\.worktrees/'` | **0** |
| `find .worktrees -name .git \| wc -l` | **42** |

So the exposure class is precisely **tools that are neither dot-aware nor git-aware**.

### 3.3 Harness conventions observed on this machine `[MEASURED]`

| Store | Location | Inside the canonical repo? |
|---|---|---|
| WB repository-local (default today) | `<canonical>/.worktrees/<task>` | yes |
| WB central home | `<WB_HOME>/worktrees/<task>/<org>/<repo>` | no |
| Claude Code | `<canonical>/.claude/worktrees/<agent-id>` | yes |
| A Codex integration | `<projects-root>/.codex-wb/{worktrees,prompts,reports,worklogs}` | no (WB-home-shaped, at projects root) |

`[MEASURED]` `~/.claude` config on this machine contains absolute references to `~/.wb/worktrees/<task>/<org>/<repo>/...` (in `launch.json` and `settings.local.json` read-allowlists) — i.e. **another tool has hardcoded the store path**, independent of WB's own configuration. Similar to P4.

### 3.4 The relocation episode `[MEASURED]`, recorded for cost calibration

Executed and then reverted within one session. Facts, without recommendation:

1. Moving 172 worktrees from central home into the projects tree required **172 `git worktree repair` invocations**; 0 failed. Reverting required the same again.
2. Making the home writable under `workspace-write` required a symlink at `~/.wb` pointing into the workspace.
3. While that symlink existed, one `wb hooks repair` baked the resolved path into 3 repositories' hook shims (P4), which then wrote into the projects tree on every hook run.
4. `rm -rf` of the old home failed on Go's read-only module cache; the follow-up `ln -s` then created `~/.wb/.wb` *inside* the surviving directory.
5. Splitting merged bookkeeping back apart afterwards was **impossible to do reliably**: ctime provenance had been destroyed by a daemon writing throughout, and name-based provenance failed because work-log effort IDs span ~2,500 tasks while the worktree inventory covers only 258.
6. A daemon runtime directory is hardcoded to `<projects-root>/.wb/runtime` in 10+ source locations with no relocation flag, so even a "clean" final state retained one writer in the tree.

**Load-bearing lesson for the analysis:** the expensive part was not moving bytes (renames on one filesystem, `[MEASURED]` same volume `/dev/disk3s5`). It was (a) per-worktree git repair, (b) provenance loss making the operation one-way, and (c) configuration materialised into generated files that had to be found and regenerated.

---

## 4. Design space

Five sub-decisions. They interact; a full answer must pick one option from each.

### 4.1 Where the checkout lives

#### Option A — Inside the canonical repo, dot-named: `<canonical>/.worktrees/<task>`
*Today's WB default; also Claude Code's shape.*

**Pros**
- Same filesystem as the canonical clone by construction; moves are renames.
- Unambiguous association with the repository; repo-scoped tooling can be taught one rule.
- Skipped by dot-aware tools (`rg`, `fd`, VS Code search) and by the Go toolchain (dirs beginning `.` or `_`).
- One `.git/info/exclude` line hides it from all git-aware tooling; measured effective against `rg --files`.
- No new top-level namespace; survives cloning the canonical repo.

**Cons**
- Re-enters the scanned tree → P1 for `find`/`grep -r`/`os.walk`/backup (measured: 193 checkouts, 581 working copies).
- `git clean -xfd` or `git status` mistakes can destroy in-flight work if the exclude is missing on an older clone (the exclude is written by the tool, not committed — a fresh clone without it is unprotected).
- Repository directory size balloons; IDE indexers may traverse it.
- Invisible-but-present to non-git-aware backup agents.

#### Option B — Inside the canonical repo, non-dot: `<canonical>/worktrees/<task>`
**Pros** same as A, plus visible to humans and to tools that skip dot-dirs for other reasons.
**Cons** strictly worse than A: exposed to `rg`/`fd`/VS Code by default, compiled by the Go toolchain, and would appear in a naïve `git status` unless excluded. **Include only to be rejected.**

#### Option C — Sibling of the repo: `<projects>/<org>/<repo>.worktrees/<task>` (or `../<repo>-<branch>`)
*The convention in most hand-rolled scripts, and what the [Codex worktree guide](https://www.frr.dev/posts/codex-cli-worktrees-manual-parallelism/) teaches (`git worktree add ../my-project-feat-auth -b feature/auth`).*

**Pros**
- Outside the repository: immune to `git clean`, `git status`, repo-scoped builds.
- Same filesystem; renames stay cheap.
- Still obvious to a human which repo it belongs to.
- No `.git/info/exclude` dependency.

**Cons**
- Still inside the projects root → P1 unchanged for recursive walkers.
- Pollutes the `<org>/` namespace with near-duplicate entries; any tool enumerating `<org>/<repo>` directories must now filter. (Observed class of bug: tooling that assumes exactly two levels.)
- Name-mangling for branch names with `/` (the guide's own wrapper substitutes `-`).
- Not dot-named, so it is exposed to the *widest* set of tools.

#### Option D — Hidden sibling collection: `<canonical-parent>/.worktrees/<repo>/<task>`
**Pros**
- Outside the repository (no `git clean` risk) *and* dot-named (skipped by `rg`/`fd`/Go/VS Code).
- Keeps the `<org>/` namespace clean: one hidden dir per org.
- Same filesystem; cheap renames.
- One natural place per org for a GC sweep and a lock namespace.

**Cons**
- Still under the projects root, so `find`/`grep -r`/`os.walk`/backup still descend → P1 partially unsolved (though invisible to the widest-used search tools).
- Adds a third layout to an already three-layout resolver.
- Ownership split: some worktrees at `<repo>/.worktrees`, some at `<org>/.worktrees` — a migration burden and a source of "which one is authoritative" bugs.

#### Option E — Central per-user home: `$WB_HOME/worktrees/<task>/<org>/<repo>`
*WB's historic layout; the arrangement the recorded incident's commit moved to.*

**Pros**
- Completely outside any source tree → **P1 solved** for the projects tree.
- One root to GC, audit, back up selectively, and exclude from indexers.
- Single lock/claim namespace colocated with the checkouts.
- Survives re-cloning or deleting a canonical repo (though registration still breaks — P3).

**Cons**
- **P2:** outside the workspace root → unwritable under `workspace-write`. This is the constraint that caused the whole episode.
- `<task>/<org>/<repo>` nests deeper, so naive `<org>/<repo>` assumptions inside the store need care.
- Home is user-scoped: two machines or two users do not share the store; a worktree is not portable across hosts.
- Breaks if `$HOME` is on a different volume from the clones (rename → copy).

#### Option F — Central per-workspace store, sibling of the projects root
*e.g. `/Users/alex/agent/{projects, wb-home/}` with the store at `/Users/alex/agent/wb-home/worktrees/…`*

**Pros**
- **Solves P1 and P2 simultaneously**, which no other single option does: inside the sandbox's workspace root, outside the projects tree.
- One root to GC/audit/exclude.
- Same filesystem as the clones if both live under the same workspace parent.

**Cons**
- Requires moving the projects root, or redefining the session workspace root to a common parent — a bigger change than relocating the store, touching every harness, script and hardcoded `~/projects` reference (P4 shows such references exist and are numerous).
- If the workspace root is simply widened to `$HOME`, the sandbox then grants write access to the entire home directory — a materially weaker containment story `[ASSUMED]`.
- Two-level indirection (`workspace → projects`, `workspace → store`) is harder to explain than either pure option.

#### Option F2 — Host-separated, workspace-resident store

*Proposal: canonical clones at `<projects-home>/<host>/<org>/<repo>` (e.g. `~/projects/gh/sneat-dev/wb`, where `gh` is a configured alias for `github.com`); every checkout at `<projects-home>/.worktrees/<task>/<host>/<org>/<repo>`; session workspace root = `<projects-home>`.*

```
~/projects/                        <- workspace root AND projects home
  gh/                              <- host alias  (gh = github.com)
    sneat-dev/wb/                  <- canonical clone
    sneat-co/sneat-go/
  .worktrees/                      <- every checkout: dot-named, inside the workspace,
    <task>/gh/sneat-dev/wb/           outside every repository
  .wb/                             <- optional: coordination state (WB_HOME)
```

**Why this is the strongest candidate in this brief**

- **P2 is solved without touching the sandbox boundary.** The store sits under the *existing* workspace root, so `writableRoots()` already grants it. This is the decisive difference from Option F, which required widening the workspace to a common parent and therefore granting write access to more than `~/projects`.
- **The `.git/info/exclude` dependency disappears.** The store is outside every repository, so git never sees it. Today 372 repositories carry `/.worktrees/` in `.git/info/exclude` `[MEASURED]` purely to hide it — and a fresh clone missing that line is unprotected, while `git clean -xfd` there destroys in-flight work. Both failure modes vanish by construction rather than by discipline (invariant 4).
- **`git clean`, `git status` and repo-scoped builds can never touch a checkout.**
- **One root** for GC, locks, audit, and backup/indexer policy.
- **`<host>` fixes a latent collision.** `<org>/<repo>` is unique only within one forge. Today the projects root is flat, so `github.com/acme/api` and `gitlab.com/acme/api` cannot coexist. A host level also makes the tree self-describing.

**What it does not fix**

| Problem | Effect |
|---|---|
| P1 — recursive tools | **Unchanged for tools that are neither dot-aware nor git-aware.** A walk of `<projects-home>` still sees every checkout. Because the store leaves the repository it also *loses* the git-exclude fence: protection drops from two fences (dot + git exclude) to one (dot). `rg`/`fd`/VS Code are still safe; `find`/`grep -r`/`os.walk`/`tar`/`rsync`/backup are still not. |
| P3 — registration repair | Unchanged. The migration itself must repair every existing checkout (~407 measured). Relative paths (§4.2 R-rel) would make *subsequent* moves cheap but not this one. |
| P4 — path pinning | **Worse during migration.** A host level changes every canonical path, so every generated artifact that hardcodes `~/projects/<org>/<repo>` must be regenerated. Measured instances exist in `~/.claude/launch.json` and `settings.local.json`. |
| P9 — cost | Unchanged; working trees still dominate. |

**WB_HOME is a separate knob and must be moved too.** Making checkouts workspace-resident does *not* make coordination state workspace-resident. Hook shims pin `WB_HOME` `[MEASURED]`, and hook-runtime writes go to `$WB_HOME/hook-runtime`; if that stays at `~/.wb`, hooks still fail under `workspace-write`. Placing `WB_HOME` at `<projects-home>/.wb` completes the picture — and does so with a bonus:

- It **decouples** the small irreplaceable state (Work Logs measured at ~0.2% of the disk footprint) from the large disposable checkouts (§4.3).
- It **disarms P5.** Legacy auto-discovery triggers on `<projects-root>/.wb/worktrees` *existing* (`wbhome.go:73`). With checkouts at `<projects-home>/.worktrees`, that directory never exists, so the implicit configuration bit can never fire.

Complete form: clones at `<projects-home>/<host>/<org>/<repo>`, checkouts at `<projects-home>/.worktrees/…`, state at `<projects-home>/.wb`, workspace root = `<projects-home>`.

**Cost of the `<host>` level** `[MEASURED]`, `[SOURCE]`

- `canonicalRepositoryPath(projectsRoot, repository)` derives exactly two levels (`internal/worktrees/worktrees.go:1671`); `splitRepository` (`:1645`) mirrors it. **35 call sites** depend on that shape.
- `--projects-root` is documented as "root dir containing `{org}/{repo}`" (`cmd/wb/main.go:118`).
- `wb layout audit|clean` exists specifically to "report non-canonical clone placement", documented as "canonical fleet members are owner/repository directories" (`cmd/wb/layout.go:19-25`). A third level makes **every existing clone read as misowned** to that command until it is taught the host level.
- 388 clones move; every absolute worktree registration breaks (P3); harness, script and IDE configs holding `~/projects/<org>/<repo>` must be regenerated.
- **Mitigation:** perform it **once, together with the store move**, not as two migrations. If `<host>` is deferred, the store move alone is strictly additive and much lower risk.

**Two sub-decisions to pin while designing this**

1. **Task-first inside the store**: `<projects-home>/.worktrees/<task>/<host>/<org>/<repo>`, not host-first. A multi-repo task must keep its checkouts together (P8), and this matches wb's existing shared convention `<root>/<task>/<owner>/<repository>`.
2. **One hidden namespace, not N.** The projects home already hosts `.claude/worktrees` (117 dirs `[MEASURED]`), `.codex-wb`, `.codegraph` and `.wb`. Consolidating every agent store under a single dot-named namespace — `.worktrees` for checkouts, one `.wb` for state — removes the need for per-tool exclusion rules, which is precisely the failure mode the recorded incident's commit was reacting to.

**Residual risk to state explicitly:** the store becomes a *single* directory holding every agent's checkout. Whatever backup, indexer or cleanup policy applies to `<projects-home>` now applies to all of it at once, and one bad recursive `rm` or `git clean` invoked at the wrong level takes out every task. Option D and today's default at least partition the blast radius by repository.

#### Option G — Ephemeral: `/tmp`, `$TMPDIR`, `os.tmpdir()`
**Pros** harness sandboxes nearly always allow temp; OS cleans up; zero disk growth.
**Cons** **lost work on reboot/tmpwatch**; frequently a different filesystem (macOS `/tmp` → `/private/var`), turning renames into copies; concurrent cleanup can delete an active checkout. Usable only for genuinely disposable builds. **Include to be rejected for agent work.**

#### Option H — Dedicated volume or mount
**Pros** isolates I/O and quota; backup policy can differ; capacity independent of `$HOME`.
**Cons** cross-device means `git worktree add` copies and any `mv` copies — measured same-volume renames were cheap and this removes that property; adds a mount to manage; on macOS a separate APFS volume shares the same physical device unless the hardware differs.

#### Option I — No worktrees: container/VM or full clone per task
**Pros** strongest isolation; the store can be ephemeral by construction; no git registration coupling at all (P3 disappears).
**Cons** heaviest; loses shared object store (P9); image/startup latency; still needs a host-side path, so P1/P2 reappear for any volume mount; poor fit for "many tasks on one laptop".

### 4.2 How registration paths are stored

#### Option R-ab — Absolute (git default, today)
`[MEASURED]` `<canonical>/.git/worktrees/<id>/gitdir` holds an absolute path.
**Pros** unambiguous under symlinks and bind mounts; no surprises if a tree is moved partially.
**Cons** any move requires `worktree repair`; repair silently degrades when the canonical repo is gone (P3).

#### Option R-rel — Relative: `worktree.useRelativePaths=true` / `git worktree add --relative-paths`
`[SOURCE]` available in git 2.54 `[MEASURED]`; currently unset.
**Pros** a worktree tree moved *together with* its canonical repo needs no repair; makes whole-store relocation a pure rename; dramatically reduces the cost of §3.4 step 1.
**Cons** does not help when the store and the canonical repo move *independently* (exactly the cross-boundary case in P2); relative paths interact with symlinked ancestors; `extensions.relativeWorktrees` is a repo extension, so enabling it writes to the canonical repo's config and older git versions will refuse to operate.

### 4.3 Where coordination state lives

| Option | Pros | Cons |
|---|---|---|
| **Same home as the store** (today) | one root to back up/GC; single lock namespace | couples two orthogonal knobs (P2 applies to both); a store move drags state along |
| **Separate state home** | state can stay outside the sandbox-exposed tree while the store moves inside; state is small (measured: Work Logs 90 MB vs 53 GB of worktrees) | two roots to configure, two to back up; risk of split-brain if only one is moved |
| **In-repo `.wb/`** | colocated with the checkout; travels with it | pollutes the repo; needs excludes (measured: 211 repos already carry `/.wb/local/`); a canonical clone must stay clean, so state must never land there |
| **Database / external service** | queryable inventory; multi-machine | new failure domain; offline problem; heavier than the problem warrants on one host |

**Observation:** coordination state is ~0.2% of the disk footprint but 100% of the irreplaceability and most of the relocation risk (§3.4 step 5). Coupling it to the checkout store means every move of the large, disposable thing also risks the small, vital thing.

### 4.4 How the sandbox boundary is drawn

| Option | Pros | Cons |
|---|---|---|
| **K. Widen workspace root to a common parent** | satisfies P2 without putting the store in the scanned tree (enables Option F) | grants write access to more than intended; requires redefining the workspace for every harness; host-specific |
| **L. Store inside the workspace** | no sandbox change; works today (Option A/D) | reintroduces P1 |
| **M. Extra writable roots in the sandbox** | surgical; keeps Option E | **not supported** by the sandbox in use `[SOURCE]` `writableRoots()`; would require an upstream change |
| **N. Run the tool outside the sandbox** | unblocks immediately | discards the containment property entirely; not a design, a bypass |
| **O. Make the store *appear* inside the workspace** (bind mount / hardlink farm / symlink) | no config change | symlinks are resolved by the tool (P4) and refused for the home in one code path `[SOURCE]` `EnsureHome` rejects a symlinked home; bind mounts are not user-available on macOS without privileges |

### 4.5 Orthogonal: avoid the problem instead of placing it

| Option | Pros | Cons |
|---|---|---|
| **P. Delegate to harness-native worktrees** (e.g. Claude Code `--worktree`) | zero bespoke mechanism; upstream maintains placement, GC and merge | each harness differs (Codex CLI has no native worktrees — [issue #12862](https://github.com/openai/codex/issues/12862) `[SOURCE]`); no cross-harness inventory; conflicts with a tool that needs its own claim/lock semantics |
| **Q. Content-addressed / overlay working trees** (read-only base + per-task overlay) | near-zero duplication; no second checkout to scan | heavy engineering; poor tooling compatibility (build systems, watchers); not git-native |
| **R. Accept duplication, mitigate exposure only** (ignore files + `.git/info/exclude` + documented excludes for backup/indexers) | cheapest; no relocation risk; keeps today's paths stable | does not fix `find`/`grep -r`/`os.walk`/backup (measured); relies on every tool being taught the rule — the failure mode the recorded commit explicitly calls out |

---

## 5. Invariants any solution should hold

1. **Same filesystem** as the canonical clone, or an explicit, checked assertion that renames degrade to copies.
2. **Deterministic, collision-free path** derivable from `(task, org, repo)` alone — no sequence numbers, no timestamps in the primary path.
3. **Atomic per task**: a multi-repo task's checkouts move together or not at all.
4. **Never inside a canonical clone's tracked tree**; the canonical clone must remain clean by construction, not by discipline.
5. **Excluded from both tool classes** where possible: git-aware (`.git/info/exclude`) *and* non-git-aware (dot prefix). Accept that neither covers backup agents.
6. **Relocatable without manual repair**, or the repair set must be derivable mechanically from the store itself (no external log).
7. **Provenance survives**: given a checkout, the system can answer "which task, which claim, which canonical repo" without replaying a tool's logs. (Today it cannot — §3.4 step 5.)
8. **Single writer per checkout**, enforced by lock, not convention.
9. **No generated artifact bakes an absolute path** that is not regenerated on change (P4).
10. **Read-only code paths perform no metadata writes** (P6).
11. **One explicit bit** decides layout; no layout is inferred from the mere existence of a directory (P5).
12. **Reversible**: a relocation decision must be undoable without loss, which requires (6) and (7).

---

## 6. Suggested evaluation criteria

Score each candidate architecture 1–5 on each axis, and state the evidence used:

| # | Criterion | Question |
|---|---|---|
| 1 | Isolation strength | Can two agents on the same task/repo interfere at all? |
| 2 | Tool-exposure surface | How many measured tool classes (dot-aware, git-aware, neither) see duplicates? |
| 3 | Sandbox compatibility | Does it work under the deployment's `workspace-write` set with no upstream change? |
| 4 | Relocation robustness | Cost and failure modes of moving the store; is it reversible? |
| 5 | Discovery / GC | Can an operator enumerate, audit and reclaim without external logs? |
| 6 | Cost | Disk and build-cache behaviour; rename vs copy. |
| 7 | Multi-repo tasks | Uniformity and atomicity across N repos. |
| 8 | Portability | Linux CI, containers, multiple machines, multiple users. |
| 9 | Operational complexity | Files to configure, artifacts to regenerate, invariants to remember. |
| 10 | Failure observability | When it breaks, is the signal immediate and correctly attributed? (Cf. P6.) |

**Weighting suggestion:** criterion 3 is a hard gate (a solution failing it cannot ship in the current deployment); criterion 4 and 10 are where the observed episode incurred its real cost; criterion 2 is the stated motivation but turned out to be the easiest to *partially* mitigate.

---

## 7. Open questions (please treat as unknowns)

1. Can the sandbox's `workspaceRoot` be set to a parent of the projects root in this deployment, and what is the containment cost? `[UNKNOWN]` — **partially obviated by Option F2**, which needs no widening, so this is no longer on the critical path.
2. Is `/Users/alex/projects` fixed, or is the projects root itself configurable in practice? `[UNKNOWN]` — WB has `--projects-root`, but every harness, script and tool would need to agree. Option F2 depends only on it being choosable *once* (the workspace root must equal the projects home).
3. How many tools in the wider environment (IDEs, backup, Spotlight, CI) actually walk these trees? Only `rg`/`find`/`grep`/`os.walk`/`tar`/`rsync`/`cp`/`du` were tested. `[UNKNOWN]`
4. Is Git's relative-worktree mode viable when the store and canonical repo must be moved independently, as P2 forces? `[ASSUMED: no]`
5. What is the real orphan/GC rate, and what is the cost of a wrong GC? `[UNKNOWN]`
6. Does the fleet need multi-user or multi-machine worktree sharing? `[UNKNOWN]` — a central per-user home cannot provide it; Option F2 is better here, since the store travels with the projects home rather than with `$HOME`.
7. Is there appetite for a one-way migration plus a deprecation window, given §3.4 showed the operation is effectively one-way once provenance is lost? `[UNKNOWN]`
8. Should coordination state and checkout store be decoupled (§4.3)? The state is small and irreplaceable; the store is large and disposable. Decoupling looks strictly beneficial; **Option F2 makes the decoupling free**, because the two roots are already distinct.
9. **Should `<host>` be adopted, and in the same migration or a later one?** `[UNKNOWN]` — it fixes a real latent collision (`<org>/<repo>` is forge-scoped, not global) but changes every canonical path, invalidating 407 worktree registrations and every hardcoded path in harness configs. Deferring it keeps the store move strictly additive.
10. **Is `WB_HOME` free to move to `<projects-home>/.wb`, or do installed shims and daemons pin it?** `[UNKNOWN]` — measured: 361 repositories carry a pinned `WB_HOME` inside managed hook shims, so this is a fleet-wide regeneration, not a config edit (P4).
11. **Does a single shared store concentrate risk unacceptably?** Option F2 puts every agent's checkout under one directory, so one careless recursive delete at the wrong level loses every task at once. Option D and the current default partition the blast radius by repository. `[UNKNOWN]` — depends on backup policy and how destructive commands are guarded.

---

## 8. Appendix

### 8.1 Environment

| Property | Value |
|---|---|
| OS | macOS (Darwin), APFS |
| Volume | `/dev/disk3s5` mounted at `/System/Volumes/Data` — clones and both homes on one filesystem |
| git | `2.54.0 (Apple Git-157)` |
| Projects root | `/Users/alex/projects` |
| Sandbox workspace root | `/Users/alex/projects` (identical to projects root) |
| Sandbox mode observed | `workspace-write` (later `danger-full-access` for the revert) |
| WB home after revert | `/Users/alex/.wb` — 59 GB, 214 worktrees (213 live + 1 quarantined artifact) |
| Legacy projects-root home | `/Users/alex/projects/.wb` — now only `runtime/` (3.4 MB, daemon), `README.md`, `reviews/` |

### 8.2 Reproduction commands

```sh
# Fleet shape
find /Users/alex/projects -maxdepth 3 -name .git -type d | grep -v '/\.wb/\|/\.claude/\|/\.worktrees/' | wc -l
find /Users/alex/projects -maxdepth 4 -type d -name '.worktrees' | while read d; do find "$d" -maxdepth 3 -name .git -type f; done | wc -l

# Exclusion fossil record
cat /Users/alex/projects/*/*/.git/info/exclude | grep -v '^#' | grep . | sort | uniq -c | sort -rn

# Dot-awareness probe
P=/tmp/dotprobe; rm -rf $P; mkdir -p $P/plain/r $P/.hidden/r
printf 'NEEDLE\n' > $P/plain/r/m.txt; printf 'NEEDLE\n' > $P/.hidden/r/m.txt
rg -l NEEDLE $P          # dot-aware: only plain/
grep -rl NEEDLE $P       # not: both
find $P -name m.txt      # not: both
python3 -c "import os;[print(os.path.join(r,n)) for r,_,fs in os.walk('$P') for n in fs]"

# Git-exclude vs find
cd <a-canonical-clone>
rg --files | grep -c '^\.worktrees/'      # 0
find .worktrees -name .git | wc -l        # >0

# Sandbox write set
touch ~/.wb/.__probe; echo $?             # EPERM under workspace-write
touch /Users/alex/projects/.__probe; echo $?   # 0

# fchmod-on-read (P6)
python3 -c "import os;fd=os.open('/Users/alex/.wb/worklogs',os.O_RDONLY|os.O_DIRECTORY);os.fchmod(fd,0o700)"

# Worktree registration shape
cat <canonical>/.git/worktrees/<id>/gitdir
git -C <moved-checkout> worktree repair     # fixes the absolute back-pointer
```

### 8.3 Glossary

| Term | Meaning |
|---|---|
| Canonical clone | the shared, read-only `{org}/{repo}` checkout every worktree is cut from |
| Linked worktree | a `git worktree` checkout: its own HEAD/index, sharing the canonical object store |
| WB_HOME | private coordination state root (claims, locks, Work Logs, prompt archives, receipts) |
| `worktrees.root` | configured physical root for checkouts; default repository-local |
| Legacy layout | historic `<projects-root>/.wb`, readable but never a write fallback |
| Dot-aware | tool that skips dot-directories unless told otherwise |
| Git-aware | tool that honours `.gitignore` / `.git/info/exclude` |
| Quarantine artifact | `.wb-retired-stage-*` / `.wb-retired-checkout-*` retirement directory |

### 8.4 Sources

- Claude Code worktrees documentation — https://code.claude.com/docs/en/worktrees
- Codex CLI worktrees (manual approach, and shared-session bug) — https://www.frr.dev/posts/codex-cli-worktrees-manual-parallelism/
- Codex native-worktree request — https://github.com/openai/codex/issues/12862
- Codex shared-session bug — https://github.com/openai/codex/issues/11435
- Third-party parallel-agent runners: [orchestra](https://github.com/lcsmas/orchestra), [maestro](https://github.com/PlathsOven/maestro), [parallel-code](https://github.com/johannesjo/parallel-code), [ccmanager](https://github.com/kbwo/ccmanager), [parallel-worktrees skill](https://github.com/SpillwaveSolutions/parallel-worktrees)
- Local source: `internal/wbhome/wbhome.go`, `internal/worktrees/worktrees.go`, `internal/worktrees/worklog.go`, `internal/worktrees/branch_config.go` in the `sneat-dev/wb` checkout
- Historical commit: `6513022b` "move WB's shared state to ~/.wb, off the projects tree (#32)"
