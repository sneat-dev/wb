---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Code Index Freshness

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Related Ideas:** [graph-assisted-fleet-optimization](../../ideas/graph-assisted-fleet-optimization.md) (Draft; not promoted by this Feature)
**Depends On:** [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md) (amended there by `version-2-extensions`)

## Summary

A checkout's code index follows its `HEAD` without the agent doing anything.
WB emits `checkout-updated` when a Git hook fires for a move (merge, pull,
commit, rebase, amend, branch checkout) or a WB verb moves a checkout. The
existing trusted lifecycle runner executes the user's configured indexer,
coalesced, at background priority, under CPU admission, and never in Git's
path. `wb fleet status` reports freshness from receipts, so WB stays
tool-agnostic. Moves that fire no Git hook (`git reset`, `git update-ref`,
`git am`) show as `stale` or `diverged` until the next emitting move.

## Problem

The founder: "Wb should update graphs on pull, merge, commit, etc."

`checkout-updated` is emitted today by `wb sync`, daemon repository-event
sync, the canonical fast-forward of `wb pr land`, `wb pr create --land` and
`wb worktree merge`/`wb land`, and explicit `wb hooks lifecycle backfill`. It
is not emitted when an agent or person runs `git pull`, `merge`, `commit`,
`rebase` or `checkout` directly, when `wb worktree create` makes a checkout,
or when `wb stream` rebases a stream branch. No worktree has ever had an
index. The 2026-09-18 SDLC logging-gap analysis counted 3,015 symbol greps
against 0 codegrapher analysis calls in a week. On wb, `codegrapher init`
costs 52 s wall, 41 s CPU and 134 MB; an incremental `sync` 3.3 s.

**Sequencing.** WB's own verbs already keep canonical clones fresh, and a
worktree can query its canonical clone's index at no cost (option 0 below).
So option 0 ships first, through the router in
[Expert Tool Routing](../expert-tool-routing/README.md). The Git-hook profile
mainly adds freshness after a person's direct `git pull` in a canonical clone
until worktree indexing is decided.

## Behavior

### REQ: git-hook-emission

A new built-in hook profile, `lifecycle`, MUST be selected by default by
`wb hooks install`, as `worktree` is, so enabling it writes no hooks-policy
key an older wb could not decode. It installs `post-merge`, `post-checkout`,
`post-commit` and `post-rewrite` shims whose profile step runs
`wb hooks lifecycle notify --hook <name> -- <git hook args>`. `notify`
resolves the checkout, its canonical identity, the old SHA and the new
`HEAD`, and enqueues `checkout-updated` with cause `git:<hook>`. The old SHA
is advisory: `post-checkout`'s first argument, `ORIG_HEAD` for `post-merge`,
the first parent of `HEAD` for `post-commit` (empty for a root commit; not
the pre-move `HEAD` during amend or rebase), the first `post-rewrite` stdin
pair's old SHA. `post-checkout` emits only for branch checkouts (third
argument `1`) that changed `HEAD`. Opting out (`profiles.exclude:
[lifecycle]`) is the user's own policy write and is visible to
`wb hooks check`.

### REQ: verb-emission

WB MUST emit after `wb worktree create` (cause `worktree-create`, empty old
SHA) and after `wb stream start|join|sync` changes a stream branch's `HEAD`
(`stream-<verb>`), and keep every existing emitter. A verb that emits MUST
suppress the duplicate its own Git child would raise through the shim
(`WB_LIFECYCLE_SUPPRESS=1` in the child environment).

### REQ: never-blocks-git

For `post-merge`, `post-checkout`, `post-commit` and `post-rewrite`, every
managed shim, including the `worktree` profile's `post-checkout`, MUST map
any non-zero status — a resolver failure (wb not found, not absolute, not
trusted), a policy-load failure, or a failing step — to a stderr warning and
exit `0`, as the worktree-guard template already does for its own step,
because a non-zero `post-checkout` becomes `git checkout`'s status.
`pre-commit` and `pre-push` keep failing closed. `wb hooks run` for those four
hooks, and `wb hooks lifecycle notify`, MUST be exempt from the worktree
heartbeat and the invoked-command record (as `wb version` is); `notify` MUST
exit `0` and MUST NOT claim unseen lifecycle warnings. With no matching
binding, nothing is written. With one, the only writes are the parent
Feature's enqueue-and-wake (queue entry, worker lock and health record) and,
on failure, one lifecycle warning. Budget: 50 ms at p95 for the whole shim,
measured by `wb hooks measure`.

### REQ: background-execution

Version-2 executor fields (trusted-repository-update-hooks#req:version-2-extensions):

- `priority: background` runs the executor at the platform's lowest class
  (`nice 19` and idle I/O class on Linux, `taskpolicy -b` on macOS,
  `BELOW_NORMAL_PRIORITY_CLASS` on Windows);
- `quiet_period` (default `2s`) delays a claimed execution until no newer
  event for the same executor and checkout has arrived for that long; the
  receipt lists every absorbed cause in `causes[]`.

Before starting an executor the worker MUST acquire a `wb run` host-load
admission slot in a background class that yields to foreground work; while
refused, the execution stays queued, never dropped.

### REQ: checkout-kinds

Events MUST carry `WB_CHECKOUT_KIND` (`canonical` or `worktree`) and, for a
worktree, `WB_CANONICAL_CHECKOUT`. Bindings gain
`match.checkouts: [canonical, worktree]`; a binding without it matches
canonical checkouts only, so upgrading wb changes nothing until the user opts
in. The worktree strategy is an open decision (see Open Questions).

### REQ: disk-budget

Executors gain `artifacts: [<relative path>...]`. `wb disk` MUST report the
artifacts of every matched checkout as category `lifecycle-artifacts` (kind
`cache`), split by checkout kind, and raise a finding when
`lifecycle.artifacts_budget` (default 5 GiB) is exceeded.
[Disk Reclaim](../disk-reclaim/README.md) enforces it.

### REQ: freshness-in-fleet-status

For every checkout with a matching binding, `wb fleet status` and
`wb fleet stats` MUST report per executor: `fresh` (latest successful
receipt's new SHA equals `HEAD`), `stale` (it is an ancestor of `HEAD`;
`behind` counts commits), `diverged` (it is not an ancestor, as after
`git reset` or a force-moved branch), `pending` (queued or running),
`failed`, or `never`, from receipts only; WB MUST NOT open an executor's
artifacts. JSON: `lifecycle: [{executor, state, receipt_sha, head_sha,
behind}]`. A stale, diverged or failed executor on a canonical clone counts
as attention.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md) | Same event, trust model, queue and receipts; this Feature specifies the version-2 fields that Feature's amendment admits. |
| [Machine Setup](../machine-setup/README.md) | Installs the `lifecycle` profile and writes the executor from the tool's catalog-declared template, with bounded backfill. |
| [Disk Reclaim](../disk-reclaim/README.md) | Enforces the artifacts budget. |
| [Expert Tool Routing](../expert-tool-routing/README.md) | Consumes freshness for nudges, the `wb create` Tools block and test selection. |

## Acceptance Criteria

### AC: git-pull-refreshes

**Requirements:** code-index-freshness#req:git-hook-emission

**Given** a canonical clone with the `lifecycle` profile and a binding whose
executor appends `$WB_OLD_SHA $WB_NEW_SHA $WB_UPDATE_CAUSE` to a file
**When** `git pull` fast-forwards `HEAD` from A to B
**Then** within the quiet period plus 5 s the file holds exactly the line
`A B git:post-merge`, and `wb fleet status --format json` reports the
executor `fresh` at B.

### AC: each-git-move-emits

**Requirements:** code-index-freshness#req:git-hook-emission

**Given** the same setup
**When** the user, waiting past the quiet period between steps, commits,
checks out another branch, rebases 5 commits, runs
`git checkout -- file.go`, and runs `git reset --hard HEAD~1`
**Then** the commit, branch checkout and rebase each produce exactly one
execution with `causes[]` including `git:post-commit`, `git:post-checkout`
and `git:post-rewrite`; the file checkout and the reset produce none, and
after the reset the executor reports `diverged`.

### AC: root-commit-has-empty-old-sha

**Requirements:** code-index-freshness#req:git-hook-emission

**Given** a new repository with the profile and a matching binding
**When** the first commit is made
**Then** the execution's `WB_OLD_SHA` is empty and `WB_NEW_SHA` is the commit.

### AC: burst-coalesces

**Requirements:** code-index-freshness#req:background-execution

**Given** `quiet_period: 2s` and an executor that records each invocation
**When** 20 commits are made within one second
**Then** it runs once, with the first commit's parent as old SHA and the last
commit as new SHA.

### AC: git-never-blocked-or-failed

**Requirements:** code-index-freshness#req:never-blocks-git

**Given** the `lifecycle` and `worktree` profiles installed, and in four runs
respectively: an executor that sleeps 60 s, an unreadable `wb.yaml`, an
invalid hooks policy, and `wb` absent from `PATH` with `WB_EXECUTABLE` unset
**When** the user runs `git checkout other-branch` in each
**Then** `git checkout` exits `0` and returns within 1 s each time; the third
and fourth print a warning on stderr; with `wb` restored,
`wb hooks lifecycle status` reports the configuration failure.

### AC: notify-writes-at-most-the-enqueue

**Requirements:** code-index-freshness#req:never-blocks-git

**Given** the profile installed in a worktree and no matching binding
**When** the user commits
**Then** no file under the projects root's `.wb` directory, the XDG state
directory, or the worktree's `.wb` changes, including the heartbeat; with a
matching binding, only the queue entry, worker lock and health record change.

### AC: verbs-emit-once

**Requirements:** code-index-freshness#req:verb-emission

**Given** a binding with `match.checkouts: [canonical, worktree]`
**When** the agent runs `wb create t1 owner/repo`, then `wb stream sync`
rebasing its stream branch, then `wb land` fast-forwarding the canonical clone
**Then** each move yields exactly one execution, with causes
`worktree-create`, `stream-sync` and the existing landing cause, and none
with a `git:` cause.

### AC: background-priority-and-admission

**Requirements:** code-index-freshness#req:background-execution

**Given** `priority: background` and foreground `wb run -- go test` jobs
holding every admission slot
**When** a checkout update enqueues an execution
**Then** it stays `pending` in `wb hooks lifecycle status` until a slot
frees, then runs at nice 19 on Linux (`/proc/<pid>/stat`), and no foreground
job waited on it.

### AC: existing-bindings-unchanged

**Requirements:** code-index-freshness#req:checkout-kinds

**Given** a version-1 binding without `match.checkouts`
**When** the user commits in a worktree of a matched repository
**Then** nothing is enqueued; after upgrading the section to version 2 with
`match.checkouts: [canonical, worktree]`, the next commit enqueues one
execution with `WB_CHECKOUT_KIND=worktree` and `WB_CANONICAL_CHECKOUT` set.

### AC: budget-and-staleness-visible

**Requirements:** code-index-freshness#req:disk-budget, code-index-freshness#req:freshness-in-fleet-status

**Given** `artifacts: [.codegraph]`, a 200 MiB budget, two 134 MB worktree
indexes, and a canonical clone whose latest receipt is at A with `HEAD` three
commits later at B
**When** the user runs `wb disk --format json` and
`wb fleet status --format json`
**Then** `wb disk` reports both indexes under `lifecycle-artifacts` with a
budget finding and exits `1`; `wb fleet status` lists the clone as attention
with `{"state":"stale","receipt_sha":"A","head_sha":"B","behind":3}`.

### AC: wb-stays-tool-agnostic

**Requirements:** code-index-freshness#req:freshness-in-fleet-status

**Given** the wb source tree
**When** `grep -rli codegrapher internal/lifecyclehooks internal/hooks internal/disk` runs over non-test Go files
**Then** it finds no match.

## Delivery Slices

Each slice is one PR and ships with the ACs named.

1. Freshness in `wb fleet status`, which is all option 0 needs —
   wb-stays-tool-agnostic and the staleness half of
   budget-and-staleness-visible.
2. The `lifecycle` profile, the post-* exit-0 mapping and `notify` —
   git-pull-refreshes, each-git-move-emits, root-commit-has-empty-old-sha,
   git-never-blocked-or-failed, notify-writes-at-most-the-enqueue.
3. Version-2 fields, admission, checkout kinds, the artifacts budget and verb
   emission — burst-coalesces, background-priority-and-admission,
   existing-bindings-unchanged, verbs-emit-once, the budget half of
   budget-and-staleness-visible.

## Open Questions

- **Worktree index strategy (open decision 6 of the 2026-09-18 analysis).**
  0. *Query the canonical index.* A worktree runs
     `codegrapher query|callers|impact -p <canonical clone>`. Zero disk and
     CPU; the answer reflects the base branch, not the worktree's own edits.
     **Recommended interim**, and the router's fallback in
     [Expert Tool Routing](../expert-tool-routing/README.md).
  1. *Per-worktree index* by `sync --init`: 52 s CPU and 134 MB per wb
     worktree; ten live worktrees cost 1.3 GB and nine CPU-minutes on a
     4-core VM.
  2. *Seed from the canonical index*: `export` in the canonical clone (cached
     per SHA), `import` into the worktree, then `sync` (about 3 s). Same disk,
     near-zero CPU, no new CodeGrapher work.
  3. *Shared central store* with per-worktree overlays: least disk, needs new
     CodeGrapher work.

  Until decided, bindings match canonical checkouts only.
- Is 5 GiB the right default artifacts budget (about 35 wb indexes), or
  should it be a share of the volume?
- Should worktree `post-commit` be excluded by default, relying on
  `quiet_period` and the next merge?

---
*This document follows the https://specscore.md/feature-specification*
