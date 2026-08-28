# Clean up leftover branches and worktrees

This is the entry point for any hygiene request — one finished task, or a
historic sweep over every repository in the fleet. It covers **branches as well
as worktrees**, because WB retires them together: a worktree and the branch it
holds are removed in one audited transaction.

Never hand-roll this with raw Git. A loop over `git branch --merged`,
`git worktree list`, `git branch -D`, or `git push --delete` has no Work Log,
no audit report, no lease protection, and no coordinated-task safety, and it
cannot tell content that landed from content that was reverted after landing.

## Pick the command by what you were asked

| The request | Start here |
| --- | --- |
| "clean up leftovers", "what is safe to delete?" | `wb worktree orphans` |
| "audit worktree/branch hygiene" | `wb worktree orphans --only review` |
| "this task's PRs merged, tidy it up" | `wb worktree cleanup <task>` |
| "delete every merged branch and worktree" | `wb worktree cleanup --all-merged` |
| "the sweep sees nothing, these are not WB worktrees" | `wb worktree adopt --all-external` |
| "the work is unfinished / never merged" | `wb worktree abort <task>` |
| "reuse this worktree for the next task" | `wb worktree rename <old> <new>` |

Every one of those is a dry run until `--apply` is explicit. Read the plan
before applying it, every time.

## Triage first: what is actually out there

```sh
wb worktree orphans
wb worktree orphans --only remove
wb worktree orphans --only review
wb worktree orphans --base main --stale-days 14 --format json
```

`orphans` is read-only in every configuration and is the widest view WB has. It
discovers through each canonical clone's own Git worktree registry, so it sees
all three layout generations at once: WB's current `~/.wb` home, the legacy
`<projects-root>/.wb` hierarchy, and pre-WB checkouts living anywhere else.
Rows group by root effort, so a family of sub-agent worktrees is one subject,
and a family is recommended for removal only when every worktree in it landed.

Flags: `--base` (target a branch must be contained in to count as landed,
default `main`), `--stale-days` (days without a commit before unmerged work
needs a decision, default 14), `--only active|remove|review|decide`,
`--format text|json`. `--filter` is not accepted here; scope with
`--projects-root`.

Dispositions mean: `active` — leave alone; `remove` — landed, hand to
`cleanup`; `review` — unmerged and stale, a human decides; `decide` — WB cannot
tell; `unreadable` — inspect by hand.

For WB-managed tasks specifically, the narrower inventories are:

```sh
wb worktree list
wb worktree list <task> --github
wb worktree summary <task> --github
```

## Trap 1: cleanup only sees WB-managed tasks

`wb worktree cleanup`/`abort` inventory `<wb-home>/worktrees/<task>/...`. A
linked worktree created by `git worktree add`, or by an older tool, is not a
candidate at all — it is silently outside the sweep, not skipped with a
reason. This is the single most common reason a fleet-wide sweep reports far
fewer eligible tasks than `orphans` reports removable worktrees.

Give those worktrees an identity, then a task, before expecting `cleanup`/
`abort` to see them:

```sh
wb worktree backfill
wb worktree backfill --base main --format json
wb worktree backfill --apply
wb worktree adopt --all-external
wb worktree adopt --all-external --filter acme/ --apply
wb worktree adopt <path> --apply
```

`backfill` writes a reconstructed manifest into every reachable worktree that
lacks one — the identity half of adoption alone. It does **not** give
`cleanup`/`abort` a task to resolve; they still refuse with `task ... was not
found` afterward. `adopt` writes that task directory, manifest, and Work Log
claim, reusing `backfill`'s reconstruction rather than duplicating it. Both
are additive by construction — nothing moves, no working tree is touched — so
either is safe to run against a fleet with live agents and safe to re-run
after an interruption; an already-adopted worktree is reported
`already_adopted` and left alone. Neither fabricates a prompt; `wb worktree
set --prompt` gives a worktree its first real one.

`adopt` selects with either exactly one worktree path, or `--all-external` to
sweep every worktree `orphans` classifies `layout: "external"`; honour
`--filter` to narrow a sweep. After `adopt --apply`, `wb worktree cleanup
<task>` and `wb worktree abort <task>` apply their full existing safety checks
to that worktree unchanged — dirty, unlanded, an open pull request, a held
lock, and `awaiting_push` are all still refused exactly as for a worktree WB
created directly.

Flags: `wb worktree backfill` — `--base` (default `main`), `--apply`, `--format
text|json`. `wb worktree adopt` — `--base` (default `main`), `--all-external`,
`--apply`, `--format text|json`, plus root `--filter`. Both are dry runs by
default.

## Trap 2: cleanup requires a merge receipt, not merged-looking content

Eligibility is evidence-gated, and the evidence is a *receipt*:

- the branch head is an ancestor of the freshly fetched exact `origin/<base>`
  SHA — a verified direct push counts, a local-only merge is `awaiting_push`
  and ineligible; **or**
- GitHub's own commit-to-pull-request index names a merged PR into that exact
  base whose merge commit is contained in the fetched target, proved locally by
  a three-way merge that adds nothing to either the landing commit or the
  target.

A branch that was squash-merged, rebase-merged, or cherry-picked, and whose
patches are therefore all upstream by patch-id but whose head is not an
ancestor of the target and whose PR record GitHub cannot associate, is **not** a
candidate. `git cherry <base> <branch>` reporting no `+` lines is not a receipt
WB accepts today: it cannot distinguish work that landed from work that landed
and was then reverted. Do not delete such branches by hand on the strength of a
`git cherry` run — report them and get a decision.

Use `--absorbed-by` when a merger batched the branch onto a differently named
integration branch and cherry-picked rather than merged it:

```sh
wb worktree cleanup <task> --absorbed-by <pr-number-or-landing-commit>
```

That flag only selects which receipt to verify. Every proof still runs, and the
named commit must be exactly where the work entered the target, so it can never
make unlanded work eligible.

## A branch with no worktree at all: hand off to wb branch

Every command above is scoped to worktrees. A local or remote branch that has
no linked worktree and no Work Log claim is not a `wb worktree cleanup`
candidate in any mode — `backfill` cannot help, because there is no worktree
to give a manifest to. On a real fleet that is the large majority: a sweep
measured 1,081 local branches, of which 750 were provably safe to delete,
while `wb worktree cleanup --all-merged` found 39 eligible tasks.

That gap is closed by a sibling command family, not by this one:
`wb branch list` and `wb branch cleanup` (see the `wb-branches` skill and
`spec/features/branch-hygiene/`). Reach for them for exactly this request —
"clean up leftover branches", "delete merged branches", "prune remote
branches" — whenever the branch you were asked about has no worktree. They
share this feature's fresh-fetch-and-prove-containment discipline and the
same dry-run-by-default, `--apply`-required contract, but they are a
different evidence engine on purpose: a bare branch has no Work Log claim, no
task lock, and no coordinated-task semantics for this feature's machinery to
apply to. `wb branch cleanup` in turn defers back here — a branch that *does*
have a worktree or a live WB Work Log claim is reported `in-use` and left for
`wb worktree cleanup <task>` or `wb worktree abort <task>` to retire. Do not
hand-roll either sweep: a `git branch --merged` or `git push --delete` loop
has no audit trail, no lease protection, and cannot distinguish work that
landed from work that landed and was then reverted.

## Trap 3: `--base` defaults to `main`

`--base` defaults to `main` on `cleanup`, `list`, `orphans`, and `backfill`. A
task branched from a feature branch is `awaiting_push` against `main` forever
until you say so:

```sh
wb worktree cleanup <task> --base <feature-branch>
```

This is the same rule as `wb worktree cleanup --base`: the receipt is checked
against the exact origin target you name, and nothing else.

## Trap 4: dry run is the default, and `--apply` is not enough

```sh
wb worktree cleanup <task>
wb worktree cleanup <task-a> <task-b> --apply --remote --parallel 2
wb worktree cleanup --all-merged
wb worktree cleanup --all-merged --older-than 0
wb worktree cleanup --all-merged --format json
wb --filter acme worktree cleanup --all-merged
```

`cleanup` requires one or more named tasks or `--all-merged`; the two cannot
be combined. Named tasks default to immediate eligibility
(`--older-than 0`) because that is its terminalization journey; `--all-merged`
keeps a 24-hour merged-PR grace window unless `--older-than` overrides it.

For named tasks, `--apply` **refuses without `--remote`**: definition of done
includes retiring the source remote branch, not only the local worktree and
branch.

```sh
wb worktree cleanup <task> --apply --remote --older-than 0
wb worktree cleanup <task-a> <task-b> --apply --remote --parallel 2
```

Full flag surface: `--base`, `--all-merged`, `--apply`, `--remote`,
`--older-than`, `--report-dir`, `--absorbed-by`, `--resume-interrupted`,
`--format`, plus the root `--filter` and `--projects-root`.

`--report-dir` overrides the audit directory, which defaults to
`<wb-home>/reports/worktree-cleanup/<timestamp>`. A plan is read-only even when
`--report-dir` is supplied; artifacts are written only for an apply attempt,
before its first destructive Git operation, and updated with applied or failed
state.

`--resume-interrupted` is named-task-only recovery. It validates the retained
`.lock` as this operation with a conclusively dead PID before holding it
through a normal cleanup.

## Reading a skip reason

A fleet sweep reports far more skipped tasks than eligible ones, and every skip
is WB being correct rather than WB being stuck:

- `current branch head is not integrated into the exact origin target
  (awaiting push)` — the work exists only locally. Land it, do not delete it.
- `branch still has an open pull request: <url>` — close or merge the PR first.
- `worktree has local changes` — uncommitted work. WB never removes it.
- `coordinated task blocked by <repository>` — one repository in a
  multi-repository task is ineligible, so every repository in that task is held.
  Resolve the named sibling.

Preserve skipped work and report the reason. Never reset, clean, stash, or
delete it manually to make a sweep pass.

## When the work never landed

`cleanup` must refuse an unmerged worktree, because there is no receipt. That
is what `abort` is for — it is the audited alternative to deleting an
unfinished worktree by hand:

```sh
wb worktree abort <task> --disposition handoff --successor <agent-or-session> \
  --model <exact-successor-model-or-unknown>
wb worktree abort <task> --disposition discarded --apply --remote
```

`handoff` and `not_landed` keep even a dirty worktree and branch resumable and
bind exactly one successor. `discarded --apply --remote` is the explicit
authorization to seal the Work Log first, retire an exact unchanged remote
source branch with force-with-lease, then remove a clean unlocked worktree and
its exact local branch. See [lifecycle.md](lifecycle.md) for the full
disposition contract, and for `wb worktree rename` recycling.

## Finish the sweep

A sweep is done when a re-run reports nothing, not when the first pass exits:

```sh
wb worktree orphans
wb worktree cleanup --all-merged
wb worktree list
```

Resolve every live entry and every durable backlog record. The normal terminal
state is zero cleanup backlog, not apparently-finished branches.

Long fleet sweeps are quiet: `wb worktree cleanup --all-merged` fetches and
queries GitHub for every candidate and prints its whole report at the end, so
several minutes of silence is expected progress, not a hang. Run it in the
foreground and let it finish; do not kill and retry it, which leaves task locks
behind for `--resume-interrupted` to clear.

## Bounding the network cost of a fleet sweep

`wb worktree cleanup --all-merged` and `wb worktree list --github` build an
inventory that resolves each repository's exact `origin/<target>` over the
network. On a real fleet that walk was 262 worktrees across 71 repositories and
spent 1.7s of CPU across six minutes — it is almost entirely waiting.

`--parallel N` bounds how many repositories are inspected at once (default `8`,
matching `wb sync`, which does the same `git`/`gh` work per candidate). Add
`--verbose` to stream per-candidate progress instead of waiting in silence.

```sh
wb worktree cleanup --all-merged --parallel 16 --verbose
wb worktree list --github --parallel 4
wb worktree cleanup --all-merged --parallel 1   # fully sequential
```

Do not treat a large value as free: unbounded inspection opens one SSH
connection per repository at once and trades a slow sweep for a rate-limited
one. `--workers`/`-j` is a deprecated alias kept only for existing scripts.

Three properties hold regardless of the value chosen:

- **One fetch per repository, but repositories still overlap.** The exact target
  is memoised per `(repository, base)`, and that single-flight is per key — so
  51 worktrees in one repository cost one fetch, while distinct repositories
  fetch concurrently. A cache-wide lock here would preserve the dedupe and
  silently destroy the concurrency.
- **No fetch can hang the sweep.** Each is bounded by a 90-second deadline; a
  remote that never answers is reported as unreachable and the walk continues.
  A sweep once sat 38 minutes on a single unanswered fetch.
- **Output is deterministic.** Discovery is local filesystem work and stays
  sequential; results keep walk order regardless of which worker finishes first.

### `--parallel` bounds the apply phase too

Once inspection is concurrent, removal is where the wall clock goes. The
2026-08-24 fleet sweep inspected 262 candidates in about two minutes and then
spent ten more removing 86 of them, one at a time.

The unit that can overlap is the **repository**, not the task. Git allows one
writer per clone — `worktree remove`, `update-ref -d` and the ref updates a
push implies all mutate the same `.git` — so two tasks in `sneat-co/sneat-go`
still take turns while a task in `sneat-games/chess` proceeds beside them.

That bounds the gain to the largest per-repository group. Of those 86 removals
across 34 repositories the biggest single repository held 14, so the realistic
improvement is around 3x — roughly twelve minutes down to four. Raising
`--parallel` past that buys nothing; the extra workers wait on the same clone.

- A **coordinated task** spanning several repositories holds all of them for
  its whole transaction, acquired in one global order so two such tasks over
  the same pair cannot deadlock.
- **Remote branch deletions** are bounded more tightly than `--parallel`,
  because they contend for GitHub's per-account secondary rate limit rather
  than for a local clone.
- A **per-task failure** still costs only that task, exactly as in the serial
  sweep, and the report reads in walk order whatever order tasks finish in.
- `--parallel 1` restores the fully serial apply.

### `--parallel` bounds the apply phase too

Once inspection is concurrent, removal is where the wall clock goes. The
2026-08-24 fleet sweep inspected 262 candidates in about two minutes and then
spent ten more removing 86 of them, one at a time.

The unit that can overlap is the **repository**, not the task. Git allows one
writer per clone — `worktree remove`, `update-ref -d` and the ref updates a
push implies all mutate the same `.git` — so two tasks in `sneat-co/sneat-go`
still take turns while a task in `sneat-games/chess` proceeds beside them.

That bounds the gain to the largest per-repository group. Of those 86 removals
across 34 repositories the biggest single repository held 14, so the realistic
improvement is around 3x — roughly twelve minutes down to four. Raising
`--parallel` past that buys nothing; the extra workers wait on the same clone.

- A **coordinated task** spanning several repositories holds all of them for
  its whole transaction, acquired in one global order so two such tasks over
  the same pair cannot deadlock.
- **Remote branch deletions** are bounded more tightly than `--parallel`,
  because they contend for GitHub's per-account secondary rate limit rather
  than for a local clone.
- A **per-task failure** still costs only that task, exactly as in the serial
  sweep, and the report reads in walk order whatever order tasks finish in.
- `--parallel 1` restores the fully serial apply.
