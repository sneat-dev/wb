---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Code Index Freshness

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/code-index-freshness?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Related Ideas:** [graph-assisted-fleet-optimization](../../ideas/graph-assisted-fleet-optimization.md) (Draft; not promoted by this Feature)
**Depends On:** [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md)

## Summary

Every checkout an agent works in has a code index that matches its `HEAD`,
without the agent doing anything. WB emits `checkout-updated` whenever a
checkout moves, whether Git moved it (pull, merge, commit, rebase, checkout)
or a WB verb did (`wb sync`, `wb pr land`, `wb land`, `wb worktree create`,
`wb stream` operations). The existing trusted lifecycle runner executes the
user's configured indexer, coalesced, at background priority, under CPU
admission, and never in Git's path. Freshness is reported by `wb fleet status`
from lifecycle receipts, so WB stays tool-agnostic.

## Problem

The founder: "Wb should update graphs on pull, merge, commit, etc."

`checkout-updated` exists (trusted-repository-update-hooks) and, verified in
code at `9bd6a0e`, is emitted by `wb sync` (`cmd/wb/sync.go`), daemon
repository-event sync (`internal/repositoryevents/processor.go`), and the
canonical fast-forward of `wb pr land` and `wb worktree merge`/`wb land`
(`internal/orchestrate`), plus explicit `wb hooks lifecycle backfill`. It is
not emitted when:

- an agent or person runs `git pull`, `git merge`, `git commit`,
  `git rebase` or `git checkout` directly in a canonical clone;
- `wb worktree create` makes a new checkout, or any commit lands in a
  worktree;
- `wb stream` rebases a stream branch.

Worktrees are where agents work, and none has ever had an index
(REPORT.md §9b item 4). An index that is absent or stale makes `grep` the
rational choice: 3,015 symbol greps against 0 codegrapher analysis calls in
the week to 2026-09-18. Measured costs on wb: `codegrapher init` 52 s wall,
41 s CPU, 134 MB; incremental `sync` 3.3 s.

## Behavior

### REQ: git-hook-emission

A new managed hook profile, `lifecycle`, MUST install `post-merge`,
`post-checkout`, `post-commit` and `post-rewrite` shims through
`wb hooks install`. Each shim calls
`wb hooks lifecycle notify --hook <name> -- <git hook args>`, which resolves
the checkout, its canonical identity, the old SHA (`post-checkout`'s first
argument, `ORIG_HEAD` for `post-merge`, the parent of `HEAD` for
`post-commit`, the first `post-rewrite` stdin pair) and the new `HEAD`, and
enqueues `checkout-updated` with `WB_UPDATE_CAUSE=git:<hook>`.
`post-checkout` MUST emit only for branch checkouts (third argument `1`) that
changed `HEAD`. The profile composes with the existing `worktree` profile in
the same shim; excluding it (`profiles.exclude: [lifecycle]`) is visible to
`wb hooks check`.

### REQ: verb-emission

WB MUST emit `checkout-updated` after these checkout moves, with the causes
named: `wb worktree create` (`worktree-create`, empty old SHA),
`wb stream start|join|sync` when a stream branch's `HEAD` changes
(`stream-<verb>`), and every existing emitter unchanged. A verb that emits
MUST suppress the duplicate its own Git invocation would raise through the
shim (`WB_LIFECYCLE_SUPPRESS=1` in the child environment), so one move yields
one event.

### REQ: never-blocks-git

`notify` MUST do no work beyond matching bindings and one durable enqueue.
When no binding matches it MUST return without writing. It MUST exit `0` in
every case, including a missing `wb`, corrupt configuration, or an unwritable
queue: a non-zero `post-checkout` status becomes `git checkout`'s own status.
Failures are recorded as a lifecycle warning surfaced by the next
`wb hooks lifecycle status`. Its wall-clock budget is 50 ms at p95, measured
by `wb hooks measure`.

### REQ: background-execution

Executor configuration gains two optional fields:

- `priority: background` runs the executor at the lowest scheduling class
  the platform offers (`nice 19` plus idle I/O class on Linux,
  `taskpolicy -b` on macOS, `BELOW_NORMAL_PRIORITY_CLASS` on Windows);
- `quiet_period: <duration>` (default `2s`) delays a claimed execution until
  no newer event for the same executor and checkout has arrived for that
  long, so a rebase of N commits or a burst of commits runs the executor once.
  A coalesced execution's receipt MUST list every absorbed cause in
  `causes[]`.

Before starting an executor the worker MUST acquire a `wb run` host-load
admission slot in a background class that yields to foreground `wb run`
work; while admission is refused the execution stays queued, never dropped.
Coalescing across processes is unchanged
(trusted-repository-update-hooks#req:matching-and-coalescing).

### REQ: checkout-kinds

Events MUST carry `WB_CHECKOUT_KIND` (`canonical` or `worktree`) and, for a
worktree, `WB_CANONICAL_CHECKOUT` (the canonical clone's path) so an executor
can seed from the canonical index. Bindings gain
`match.checkouts: [canonical, worktree]`; an existing binding without the key
MUST keep matching canonical checkouts only, so upgrading wb changes no
behavior until the user opts in. The worktree index strategy is an open
founder decision (see Open Questions); this Feature fixes only the event
contract every strategy needs.

### REQ: disk-budget

Executor configuration gains optional `artifacts: [<relative path>...]`
(for the code index, `.codegraph`). `wb disk` MUST report the summed
artifacts of every matched checkout as category `lifecycle-artifacts`
(kind `cache`), split canonical vs worktree. A user-set
`lifecycle.artifacts_budget` (default 5 GiB) MUST raise a `wb disk` finding
when exceeded; [Disk Reclaim](../disk-reclaim/README.md) removes worktree
artifacts least-recently-updated first and never touches a canonical
checkout's artifacts.

### REQ: freshness-in-fleet-status

For every checkout with a matching binding, `wb fleet status` and
`wb fleet stats` MUST report per executor: `fresh` (the latest successful
receipt's new SHA equals `HEAD`), `stale` (an older SHA, with commits
behind), `pending` (queued or running), `failed` (latest attempt failed),
or `never`. This is derived from lifecycle receipts only; WB MUST NOT open
or interpret an executor's artifacts. `--format json` exposes it as
`lifecycle: [{executor, state, receipt_sha, head_sha}]`. A stale or failed
executor on a canonical clone MUST count as attention.

## Interaction with Other Features

| Feature | Interaction |
|---|---|
| [Trusted Repository Update Hooks](../trusted-repository-update-hooks/README.md) | Same event, trust model, queue, receipts and `failure: warn`. This Feature adds emitters, the `lifecycle` hook profile, `priority`, `quiet_period`, `artifacts`, and `match.checkouts`. |
| [Machine Setup](../machine-setup/README.md) | Installs the `lifecycle` hook profile and writes the `code-index` executor. |
| [Fleet Status](../fleet-status/README.md) | Gains the `lifecycle` freshness field and attention condition. |
| [Disk Reclaim](../disk-reclaim/README.md) | Enforces the artifacts budget. |
| [Expert Tool Routing](../expert-tool-routing/README.md) | Consumes freshness: nudges and graph-assisted test selection apply only to `fresh` checkouts. |

## Acceptance Criteria

### AC: git-pull-refreshes

**Requirements:** code-index-freshness#req:git-hook-emission

**Given** a canonical clone with the `lifecycle` profile installed and a
binding whose executor appends `$WB_OLD_SHA $WB_NEW_SHA $WB_UPDATE_CAUSE` to a
file
**When** the user runs `git pull` that fast-forwards `HEAD` from A to B
**Then** within the quiet period plus 5 s the file holds exactly one line
`A B git:post-merge`, and `wb fleet status --format json` reports that
executor `fresh` at B.

### AC: each-git-move-emits

**Requirements:** code-index-freshness#req:git-hook-emission

**Given** the same setup
**When** the user, waiting past the quiet period between steps, commits,
checks out another branch, runs `git rebase` over 5 commits, and runs
`git checkout -- file.go`
**Then** the commit, the branch checkout and the rebase each produce exactly
one execution, whose receipt `causes[]` include `git:post-commit`,
`git:post-checkout` and `git:post-rewrite` respectively; the file checkout
produces none.

### AC: rebase-burst-coalesces

**Requirements:** code-index-freshness#req:background-execution

**Given** `quiet_period: 2s` and an executor that records each invocation
**When** 20 commits are made within one second
**Then** the executor runs once with the first commit's parent as old SHA and
the last commit as new SHA.

### AC: git-never-blocked-or-failed

**Requirements:** code-index-freshness#req:never-blocks-git

**Given** a binding whose executor sleeps 60 s, and separately a `wb.yaml`
made unreadable, and separately `wb` removed from `PATH`
**When** the user runs `git checkout other-branch` in each case
**Then** `git checkout` exits `0` in each case, returns within 1 s, and
`wb hooks lifecycle status` (with `wb` restored) reports the configuration
and dispatch failures as warnings.

### AC: no-binding-no-write

**Requirements:** code-index-freshness#req:never-blocks-git

**Given** the `lifecycle` profile installed and no binding matching the
repository
**When** the user commits
**Then** no queue or receipt file is created or modified.

### AC: verbs-emit-once

**Requirements:** code-index-freshness#req:verb-emission

**Given** a binding matching worktrees and canonical clones
**When** the agent runs `wb create t1 owner/repo`, then `wb stream sync` on a
stream whose branch is rebased, then `wb land` of a worktree that
fast-forwards the canonical clone
**Then** receipts show exactly one execution per checkout move, with causes
`worktree-create`, `stream-sync` and the existing landing cause, and none
with a `git:` cause for those moves.

### AC: background-priority-and-admission

**Requirements:** code-index-freshness#req:background-execution

**Given** `priority: background` and three foreground `wb run -- go test`
jobs holding every admission slot
**When** a checkout update enqueues an execution
**Then** the execution stays `pending` in `wb hooks lifecycle status` until a
slot frees, then runs with the platform's background class (on Linux,
`/proc/<pid>/stat` nice value 19), and no foreground job waited on it.

### AC: existing-bindings-unchanged

**Requirements:** code-index-freshness#req:checkout-kinds

**Given** a `wb.yaml` binding written before this Feature, with no
`match.checkouts`
**When** the user commits in a worktree of a matched repository
**Then** no execution is enqueued; after adding
`match.checkouts: [canonical, worktree]` the next commit enqueues one with
`WB_CHECKOUT_KIND=worktree` and `WB_CANONICAL_CHECKOUT` set.

### AC: artifacts-budget-reported

**Requirements:** code-index-freshness#req:disk-budget

**Given** `artifacts: [.codegraph]`, `lifecycle.artifacts_budget: 200MiB`,
and two worktrees holding 134 MB indexes each
**When** the user runs `wb disk --format json`
**Then** category `lifecycle-artifacts` reports both, split by checkout kind,
a budget finding is raised, and the exit code is `1`.

### AC: staleness-visible

**Requirements:** code-index-freshness#req:freshness-in-fleet-status

**Given** a canonical clone whose latest successful receipt is at A while
`HEAD` is at B, three commits later
**When** the user runs `wb fleet status --format json`
**Then** the repository is listed as attention with
`lifecycle: [{"executor":"code-index","state":"stale","receipt_sha":"A","head_sha":"B"}]`
and a commits-behind count of 3; after the executor succeeds at B the
repository no longer appears.

### AC: wb-stays-tool-agnostic

**Requirements:** code-index-freshness#req:freshness-in-fleet-status, code-index-freshness#req:disk-budget

**Given** the wb source tree
**When** `grep -ri codegrapher internal/lifecyclehooks internal/disk cmd/wb/fleet*.go` runs
**Then** it finds no match; freshness and budget work for any executor name.

## Non-goals

- A long-running index daemon owned by WB. If hooks prove insufficient, a
  per-canonical-clone `codegrapher daemon` owned by the wb daemon is a later
  Feature (REPORT.md §9c item 7).
- Indexing on file save (the graph-assisted idea's edit hook).

## Open Questions

- **Worktree index strategy (founder decision 6, REPORT.md §8).** Options:
  1. *Per-worktree index* built by `sync --init`: simplest, 52 s CPU and
     134 MB per wb worktree; at 10 live worktrees, 1.3 GB and nine minutes of
     CPU on a 4-core VM already at its lane cap.
  2. *Seed from the canonical index*: on `worktree-create`, run
     `codegrapher export` in the canonical clone (or reuse the latest export
     keyed by its SHA), `import` into the worktree, then `sync` (~3 s).
     Same disk per worktree, near-zero CPU, needs no new CodeGrapher feature.
  3. *Shared central store* keyed by repository, one base graph plus
     per-worktree overlays: least disk, needs new CodeGrapher work.

  Recommendation: 2 now, because it removes the CPU cost without new tool
  work and the artifacts budget bounds the disk; 3 later if the budget is
  routinely hit. Until decided, `match.checkouts` defaults to canonical only.
- Default `lifecycle.artifacts_budget`: 5 GiB is a placeholder, about 35 wb
  worktree indexes. Should it be a share of the volume instead?
- Should `post-commit` in worktrees be excluded by default to spare the agent's
  own inner loop, relying on `quiet_period` and the next `post-merge`?

---
*This document follows the https://specscore.md/feature-specification*
