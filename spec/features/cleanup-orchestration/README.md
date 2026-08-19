---
format: https://specscore.md/feature-specification
status: In Review
---

# Feature: Cleanup Orchestration

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=request-change) |
**Status:** In Review
**Source Ideas:** —

## Summary

`wb cleanup` is one top-level entry point that retires a repository's
worktrees, local branch refs, and remote branch refs as a single ordered
lifecycle. It owns no eligibility rules of its own: it delegates to the scoped
expert commands — [`wb worktree cleanup`](../worktree-lifecycle/README.md) and
[`wb branch cleanup`](../branch-hygiene/README.md) — which remain available and
unchanged. Its contribution is **sequencing and scale**: strictly serial inside
one repository, because a checked-out branch cannot be deleted and a remote ref
must not be deleted before dependent pull requests are retargeted; and bounded
concurrency across repositories, because a serial fleet sweep is unusable.

This feature also fixes the surrounding discovery and observability failure
that made an agent hand-roll a 200-line raw-`git` sweep rather than use WB: one
obvious verb, and an output contract in which the answer is not buried in
hundreds of lines of internal classification noise.

## Contents

| Child | Description |
|---|---|
| [unpushed-work](unpushed-work/README.md) | wb unpushed finds branches whose commits exist nowhere but this machine. |
| [cleanup-preconditions](cleanup-preconditions/README.md) | Mandatory preservation capture and stacked-pull-request pre-flight before any destructive cleanup step. |
| [fleet-audit](fleet-audit/README.md) | wb audit reports uncommitted work, stashes, worktrees and branches across every repository by evidence class. |
| [pr-recovery](pr-recovery/README.md) | wb recover finds pull requests closed by a deleted base ref and reports whether their content reached the target. |

## Problem

### The lifecycle is real; the command surface is not

Retiring finished work is one lifecycle with three ordered deletions —
worktree, local ref, remote ref — and WB exposes it as two commands that each
see part of it, plus a body of work (preservation, stacked-pull-request
retargeting) that has no command at all and is therefore done by hand or not at
all. An agent asked to "clean up" has no verb to reach for.

The observed consequence, on 2026-08-19: an agent asked to clean the fleet
wrote roughly 200 lines of raw-`git` Python instead of using WB, and was about
to delete 742 branches and 308 worktrees with no audit trail, no lease
protection, and no ability to distinguish work that landed from work that
landed and was then reverted.

### The scoped commands are correct and far too narrow

`wb worktree cleanup` enumerates `<wb-home>/worktrees/<task>/...`, so anything
without a WB task claim is not a skipped candidate — it is not a candidate.
[Branch Hygiene](../branch-hygiene/README.md) closes the branch half of that
gap. Nothing closes the ordering, the concurrency, or the preservation half.

### Measured evidence

Every number below was measured on 2026-08-19 against the founder's fleet, by
the method stated. The method matters: several of these counts move by hundreds
depending on which clones and which refs are enumerated, which is itself an
argument for a single WB command that answers the question the same way twice.
Where a re-measurement disagreed with the figure this feature was commissioned
on, the re-measured figure is the one recorded.

Scope: **401 canonical clones** under `<projects-root>`, enumerated by walking
for a real `.git` directory and excluding WB linked worktrees.

| Observation | Measured | Method |
|---|---|---|
| Local branches | **1,524** | `git branch --list` summed over all 401 clones |
| …of which local `main`/`master` | 334 | protected by name |
| Merged ancestors of the exact target | **519** | `git merge-base --is-ancestor <branch> origin/<default>` |
| Tree-identical to the target, excluding trivial local `main`/`master` | **43** | tree-SHA comparison |
| Patch-equivalent only (the `absorbed` class) | **288** | `git cherry <target> <branch>` emits zero `+` lines |
| Carrying unique content | **340** | `git cherry` emits at least one `+` line |
| Branches whose commits are on no remote ref at all | **279** | `git branch -r --contains <sha>` is empty |
| Linked worktrees known to Git | **401** | `git worktree list --porcelain` over the 401 clones |
| Task directories in the WB write home | **751** | directory count under `~/.wb/worktrees` |
| Stale task directories in the legacy home | **512** | directory count under `<projects-root>/.wb/worktrees` |
| Worktrees holding uncommitted work | **36** | `git status --porcelain` is non-empty |
| Registrations Git reports prunable or missing | **1** | `git worktree list --porcelain` |

Three of those rows are findings in their own right, not background:

- **279 branches exist only on this machine.** That is the data-loss surface,
  and no WB command reports it. See [Unpushed Work
  Detection](unpushed-work/README.md).
- **288 branches are patch-equivalent only.** Classifying those as safe is the
  mistake made by hand on the same day; see
  `#req:never-report-a-summed-safe-count`.
- **Git's worktree registrations and WB's inventory disagree badly.** Git's
  bookkeeping in the canonical clones still points into the legacy
  `<projects-root>/.wb/worktrees` hierarchy — 512 directories untouched since
  2026-08-12 — while WB's own write home holds 751 task directories. See
  `#req:reconcile-git-and-wb-worktree-views`.

### A serial sweep and a silent one are the same failure

`wb worktree cleanup --all-merged` was started as a dry run against the whole
fleet and, after **nine minutes and forty-one seconds**, had written **zero
bytes to stdout and zero bytes to stderr** while consuming under 1% CPU. It had
not finished. There is no observation an operator or an agent can make that
distinguishes that from a hang, and the standard reaction — kill and retry —
leaves task locks that then need `--resume-interrupted` to clear. Near-zero CPU
also suggests the run was blocked on hosted evidence with no deadline of its
own, which is why this feature requires both incremental progress and a bounded
per-unit deadline.

`wb worktree list --filter chessraiders` produced **58 rows on stdout and 254
lines on stderr**. 252 of those 254 lines were
`info: inventory classified WB internal secure_worktree_stage as quarantined`.
They named **251 distinct task directories, of which only 53 were chessraiders
tasks**: 200 belonged to entirely unrelated efforts in other repositories.
`--filter` scopes the result rows and does not scope the inventory pass at all
— `inspectLifecycleArtifact` in `internal/worktrees/lifecycle.go` appends every
artifact it meets with no `filterMatches` guard, and `cmd/wb/worktree.go`
prints every one. An agent cannot find the answer in that, so it writes its own
tool. Output discipline is a correctness property of this feature, not a polish
item.

The same run surfaced two worktrees — `engine-stack-regression` and
`review-task5` — reported only as `is not on a feature branch`. That string is
an inspection *error*, so those worktrees become diagnostics rather than
inventory rows: they are invisible to cleanup, and no remedy is named.

### Nothing makes deletion reversible

The safe fleet triage performed by hand on 2026-08-19 worked only because
patches and untracked-file archives — 21 MB across 15 canonical-clone patches
and 34 dirty worktrees — were captured first. Nothing in WB does that, and
safety a human has to remember is not safety.

## Interaction with Other Features

[Worktree Lifecycle](../worktree-lifecycle/README.md) owns worktree creation,
guarding, the Work Log claim, the per-task lock, coordinated-task
all-or-nothing semantics, and the terminal removal of a task's worktree with
its branch. It remains the only path that may retire a branch belonging to a WB
task.

[Branch Hygiene](../branch-hygiene/README.md) owns `wb branch list` and
`wb branch cleanup`, the branch evidence taxonomy, and the report-only
permanence of the `absorbed` class. A separate lane is implementing it at the
time of writing; **this feature must not modify it** and takes its taxonomy and
its refusals as given.

Cleanup Orchestration is a **parent of four children and a sibling of those
two**. It adds no eligibility rule of its own and MUST NOT weaken either
sibling's refusals; see `#req:no-independent-eligibility`.

### Why one parent with children rather than five siblings

The five commands share one output contract, one unit-partitioning rule, one
concurrency model, and one preservation gate; specifying those five times
guarantees they drift. They do **not** share an evidence model — `wb unpushed`
classifies reachability, `wb audit` classifies disposition, `wb recover`
classifies fate — so folding them into one document would produce exactly the
unreviewable single command that
[branch-hygiene](../branch-hygiene/README.md) refused to create when it
declined to become a mode of `wb worktree cleanup`. Parent holds the contract,
child holds the evidence: each document stays reviewable and each rule is
stated once.

## Behavior

### Command surface

#### REQ: top-level-cleanup-verb

WB MUST expose `wb cleanup` as a top-level command, not as `wb git cleanup`,
and not as a subcommand of `wb worktree` or `wb branch`. Nearly everything WB
does is Git at some level — `sync`, `status`, `deps`, `migrate` — so a `git`
group would describe the substrate rather than select a subset, and a third
`cleanup` leaf alongside `wb worktree cleanup` and `wb branch cleanup` would
make the discovery failure in the Problem section worse rather than better.

`wb worktree cleanup` and `wb branch cleanup` MUST remain available with
unchanged names, flags, and semantics. `wb cleanup` MUST NOT be introduced as a
deprecation of either.

Flags, all of which MUST be accepted exactly as named:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--stages` | string list | `worktree,local-branch,remote-branch` | which deletion stages may run |
| `--base` | string | `main` | exact origin target that must contain the work |
| `--apply` | bool | `false` | perform deletions; without it the run is a plan |
| `--older-than` | duration | `24h` | minimum age of landing evidence; `0` disables |
| `--parallel` | int | `4` | maximum cleanup units in flight; `<1` is a usage error |
| `--fetch-parallel` | int | `8` | maximum canonical clones fetched concurrently |
| `--api-parallel` | int | `4` | maximum concurrent hosted API requests |
| `--api-reserve` | int | `1000` | hosted core-quota floor below which the run refuses to start |
| `--timeout` | duration | `5m` | deadline for one unit; `0` is a usage error |
| `--preserve-dir` | string | `<wb-home>/preserved/<run-id>` | where preservation artifacts are written |
| `--report-dir` | string | `<wb-home>/reports/cleanup/<run-id>` | where the audit report is written |
| `--format` | string | `text` | `text`, `json`, or `ndjson` on stdout |
| `--verbose` | bool | `false` | emit per-artifact informational lines on stderr |
| `--fail-on` | string | `none` | `none` or `refusals`; selects the exit-code contract |

`wb cleanup` MUST accept the root `--filter`, `--projects-root`, and
`--non-interactive` flags, and MUST be registered for `filter` and
`projects-root` in `persistentFlagSupport` in `cmd/wb/main.go` and in
`docs/cli-flag-matrix.md`.

`--stages` MUST be named `--stages` and MUST NOT be spelled `--scope`.
`wb branch cleanup --scope` already means `local|remote|all`; reusing that
spelling for a different value set on a safety-critical sibling is where a
mistyped sweep becomes a deletion.

`wb cleanup` MUST NOT define a bare `--remote` boolean. Remote deletion is
selected only by including `remote-branch` in `--stages`, so the selection is
visible in the invocation rather than implied by a second flag.

#### REQ: dry-run-default

`wb cleanup` MUST be a plan unless `--apply` is explicit. A plan MUST NOT
delete a ref, remove a worktree, mutate a pull request, create a report
directory, or write a preservation artifact. It MUST perform every read-only
step of the run, including the stacked-pull-request enumeration required by
`cleanup-preconditions#req:preflight-runs-in-dry-run`, so the plan states
exactly what `--apply` would do.

#### REQ: no-independent-eligibility

`wb cleanup` MUST NOT contain its own eligibility, containment, protection, or
age logic. Every worktree decision MUST be produced by the same engine that
backs `wb worktree cleanup`, and every branch decision by the same engine that
backs `wb branch cleanup`, called as libraries rather than reimplemented or
shelled out to.

Consequently `wb cleanup` MUST NOT be able to delete anything a scoped command
would refuse, in any flag combination. In particular it MUST NOT delete an
`absorbed` branch, MUST NOT remove a worktree with uncommitted changes, MUST
NOT delete a branch whose content is not provably contained in the freshly
fetched exact target, and MUST NOT delete a remote branch without pull-request
evidence. A divergence between `wb cleanup` and a scoped command on the same
fixture is a defect, not a configuration.

#### REQ: fetch-before-every-decision

Every decision MUST be computed against the exact commit SHA obtained by
fetching `refs/heads/<base>` from `origin` during the current run, exactly as
[branch-hygiene#req:fresh-exact-target](../branch-hygiene/README.md) and
[worktree-lifecycle#req:exact-remote-target-evidence](../worktree-lifecycle/README.md)
require. A stale remote-tracking ref MUST NOT be used. A repository whose
target cannot be fetched MUST refuse its own destructive stages and MUST NOT
block any other unit.

This is the opposite default from `unpushed-work#req:no-fetch-by-default`, and
deliberately so: for a read-only risk report a stale ref over-reports risk and
is fail-safe, while for a deletion a stale ref under-reports risk and is
fail-dangerous.

### Units, ordering, and concurrency

#### REQ: cleanup-unit-partitioning

WB MUST partition the selected work into **cleanup units** before any
inspection, and MUST guarantee that no two units share a canonical clone.

A unit is a connected component of the bipartite graph whose vertices are WB
tasks and canonical clones, with an edge from a task to every canonical clone
in which it holds a linked worktree. A canonical clone touched by no task forms
a unit of its own. Selecting the connected component — rather than "one task"
or "one repository" — is what makes both of the following true at once:

1. **Coordinated-task safety survives.** Every repository of a coordinated task
   lands in the same unit, so
   [worktree-lifecycle#req:coordinated-task-safety](../worktree-lifecycle/README.md)
   still sees them together and can mark them all ineligible together.
2. **One canonical clone has exactly one writer.** Two workers running
   `git fetch`, `git update-ref`, and `git worktree remove` against the same
   clone race on Git's own ref and index locks.

The partition MUST be computed deterministically and MUST be reported in the
plan, so a stranger can tell why two apparently unrelated repositories were
processed together.

#### REQ: parallel-across-units

Units MUST be processed concurrently by a bounded worker pool of size
`--parallel` (default `4`). A unit that fails for any reason MUST NOT abort the
run, MUST NOT abort another unit, and MUST be reported with its failure as its
own outcome.

Hosted pull-request evidence MUST additionally be bounded independently of
`--parallel` and MUST back off on a rate-limit or secondary-rate-limit
response. A repository whose pull-request evidence is refused by rate limiting
MUST fail its remote stage closed, exactly as
[branch-hygiene#req:remote-apply-fails-closed-without-pull-request-evidence](../branch-hygiene/README.md)
requires, and MUST NOT proceed without evidence.

Results MUST be emitted in a deterministic order — repository, then branch —
regardless of completion order, so two runs over unchanged state produce
byte-identical `--format json` stdout.

#### REQ: concurrency-lives-in-the-shared-inspection-path

The concurrency this feature depends on MUST be implemented in the shared
inspection layer in `internal/worktrees`, not in the `wb cleanup` command. A
parallel orchestrator layered over a serial inspection engine interleaves two
slow serial sweeps and buys nothing.

The layer beneath is serial today, verified on 2026-08-19: the non-test sources
of `internal/worktrees` contain no `errgroup`, no `sync.WaitGroup`, and no
`go func(`. The inventory walk in `internal/worktrees/lifecycle.go` is a plain
nested loop over task directories and the repositories inside them, calling
`inspectLifecycleWorktree` and blocking on each. Each such inspection performs
real network work — a `git fetch --no-tags origin refs/heads/<base>` and, with
`--github`, a hosted pull-request query for the landing receipt. Over the
founder's worktree population that exceeded a 120-second foreground timeout.
`wb branch list` has the same shape and meets the same wall.

Implementing it there means `wb worktree cleanup`, `wb worktree list`, and
`wb branch list` inherit the improvement rather than each growing its own.

**Inspection parallelises. Mutation does not.** These are separate phases with
separate rules:

| Phase | Concurrency | Rule |
|---|---|---|
| Inspection — fetch, resolve, classify, query pull requests | bounded pool | parallel across canonical clones; never two workers in one clone |
| Mutation — preserve, retarget, remove worktree, delete refs | none | strictly serial and ordered, exactly as `#req:serial-within-a-unit` states |

No worker pool MUST be introduced anywhere in a mutation path. The
happens-before ordering that makes `--apply` safe — durable Work Log
archive/outbox evidence written before any local or remote deletion, per
[worktree-lifecycle#req:recheck-and-compare-delete](../worktree-lifecycle/README.md)
— is a property of that serial ordering, and concurrency would destroy it while
appearing to work.

The unit of inspection parallelism MUST be the **canonical clone**, not the
worktree. Two worktrees backed by the same clone share one object store and one
set of ref locks; fetching into it concurrently races Git's own locking and can
corrupt or lose a ref. This is the same disjointness `#req:cleanup-unit-partitioning`
guarantees for units, applied to the inspection phase.

#### REQ: two-independent-concurrency-bounds

Fetching and hosted queries throttle different resources and MUST be bounded by
**two separate semaphores that never share a counter**:

| Bound | Flag | Default | Keyed on | Constrains |
|---|---|---|---|---|
| Fetch pool | `--fetch-parallel` | `8` | canonical clone | network and disk |
| Hosted API pool | `--api-parallel` | `4` | the shared hosted token | the hosted request budget |

Both MUST be configurable flags with these defaults, so a slower link or a
tighter token can be dialled down without a rebuild. Neither MUST be derived
from `--parallel`, which bounds cleanup units.

The measurements that produced these defaults, taken on 2026-08-19: the
machine has 18 CPUs and 38.7 GB of RAM and is not the binding constraint,
because `git fetch` is network- and disk-bound rather than CPU-bound; and the
fetch unit is the canonical clone, not the worktree, where the observed
deduplication is roughly four worktrees per distinct clone. Sizing one shared
pool for the hosted budget would needlessly serialise fetches; sizing it for
fetches would hammer the API.

The fetch pool MUST be keyed on the canonical clone so the same `.git` is never
fetched concurrently, as `#req:concurrency-lives-in-the-shared-inspection-path`
requires.

#### REQ: hosted-budget-preflight

Concurrency limits the request **rate**; it does not limit the **total**. The
hosted quota is shared, and that is the non-obvious hazard: the same token
serves every concurrent agent lane, every `gh` call in
`internal/orchestrate/ciwait.go`, and this sweep. A bounded sweep can still
starve the agent fleet mid-flight, or be starved by it.

Measured on 2026-08-19: the REST core quota is 5,000 per hour, of which roughly
1,000 had already been consumed within the hour by the concurrent agent lanes
sharing the token — 3,973 remained with 44 minutes left in the window. WB's
own per-candidate cost is confirmed in code: `githubPullRequests` in
`internal/worktrees/lifecycle.go` runs `gh api --paginate
repos/{repo}/commits/{head}/pulls` for every candidate, and a second call to
`repos/{slug}/pulls/{number}` runs whenever a pull request is found. That is
one to two-plus requests per candidate before pagination, so a single
several-hundred-candidate sweep costs on the order of 6–11% of the hourly
budget.

Therefore, before starting any run that will query the host, WB MUST:

1. read the current remaining core quota and its reset time;
2. estimate the run's cost as at least two requests per selected candidate;
3. refuse to start if the estimate would take the remaining quota below a
   reserve floor (`--api-reserve`, default `1000`), and instead print the
   remaining quota, the estimate, the reserve floor, and the reset time, and
   name the degraded no-hosted-evidence mode as the alternative.

Refusing up front is required rather than proceeding: a sweep that exhausts the
budget half way through leaves an arbitrary subset of candidates classified and
the rest `unreadable`, while also breaking every other lane on the token. A
clear refusal naming the reset time is a better outcome than a half-complete
sweep.

#### REQ: secondary-rate-limits-fail-to-unreadable

Secondary rate limits, not the hourly quota, are the failure mode a bounded
sweep actually meets: GitHub answers a burst of concurrent requests with `403`
and a `Retry-After` header well before 5,000 requests are spent.

WB MUST honour `Retry-After`, MUST back off exponentially on repeated
throttling rather than retrying immediately, and MUST surface throttling in the
streamed progress so an operator can see it happening rather than infer it from
slowness.

A candidate whose hosted evidence was not obtained — rate-limited, timed out,
unauthenticated, or abandoned after retries — MUST surface as `unreadable`. It
MUST NEVER surface as `eligible`, and it MUST NEVER be silently omitted from
the report.

The specific defect to prevent is a throttled query degrading into "no merged
pull request found", which is indistinguishable from a real negative answer: it
would turn a transient throttling event into a wrong disposition, and in the
worst direction a deletion.

#### REQ: partial-failure-is-per-candidate

A repository or candidate that fails to fetch, resolve, or classify MUST
degrade to a diagnostic for itself alone, exactly as the serial path does
today. It MUST NOT abort the sweep, MUST NOT abort its sibling repositories,
and MUST NOT change any other candidate's disposition. Introducing concurrency
MUST NOT weaken this: a worker that panics or times out MUST be contained to
its own clone and reported.

#### REQ: serial-within-a-unit

Inside one unit every operation MUST be serial, and the three deletion stages
MUST run in exactly this order:

1. **`worktree`** — remove the linked worktree.
2. **`local-branch`** — delete the local ref with compare-and-delete against
   the exact planned SHA.
3. **`remote-branch`** — delete the remote ref with force-with-lease against
   the observed SHA.

The order is a correctness requirement, not a preference:

- A branch checked out in a linked worktree cannot be safely deleted.
  `git branch -d` refuses; `git update-ref -d` succeeds and leaves that
  worktree with a dangling `HEAD`. The worktree stage must therefore complete
  first.
- The remote stage closes any open pull request based on the branch, so it MUST
  run after the stacked-pull-request pre-flight has retargeted and *verified*
  every dependent pull request
  (`cleanup-preconditions#req:stacked-pr-preflight`).
- The local stage precedes the remote stage because it is the cheaper and
  wholly local one: a failure there aborts the unit before the irreversible
  hosted mutation. The reverse order buys nothing and risks a hosted deletion
  followed by a local failure.

A stage refused for one branch MUST refuse only that branch's later stages and
MUST NOT stop the unit's other branches. A stage excluded by `--stages` MUST
still report every branch that a later included stage must therefore skip, with
the reason naming the excluded stage — excluding `worktree`, for example, makes
every branch checked out in a worktree ineligible for `local-branch`, and that
MUST be stated rather than silently produce a smaller result.

#### REQ: bounded-per-unit-time

Every unit MUST run under a deadline, `--timeout`, default five minutes. A unit
that reaches its deadline MUST be abandoned with the outcome `timeout`, MUST
have performed no partial destructive stage — a stage is abandoned only at a
boundary between stages, never mid-operation — and MUST NOT stop the run. The
run MUST always terminate.

No WB command in this feature may be capable of the behaviour measured on
2026-08-19, where a dry run consumed over nine minutes at under 1% CPU with no
output and no completion. A deadline is what converts that from an unbounded
hang into a reported refusal an operator can act on.

Every hosted request MUST additionally carry its own shorter deadline, so a
single unresponsive API call cannot consume the whole unit budget.

#### REQ: reconcile-git-and-wb-worktree-views

Two independent records describe linked worktrees, and on 2026-08-19 they
disagreed: Git's own `.git/worktrees` bookkeeping inside the 401 canonical
clones registered 401 linked worktrees still pointing into the legacy
`<projects-root>/.wb/worktrees` hierarchy, whose 512 directories had not been
touched since 2026-08-12, while WB's authoritative write home `~/.wb/worktrees`
held 751 task directories.

Neither record alone is sufficient. Git's registration set is the authority on
what Git will refuse to operate on and on what leaves a dangling registration
when removed; WB's inventory is the authority on task identity and Work Log
claims.

Therefore:

1. `wb cleanup` and `wb audit` MUST enumerate the **union** of both records,
   across every hierarchy the layout resolver recognises
   ([worktree-lifecycle#req:migration-layout-compatibility](../worktree-lifecycle/README.md)).
2. Every worktree row MUST carry a `known_by` field drawn from the closed set
   `both`, `git-only` (Git holds a registration WB's inventory does not know,
   including registrations whose directory is gone), `wb-only` (a directory in
   the WB home with no Git registration).
3. `wb cleanup` MUST decide whether a branch is checked out from Git's
   registration set, never from WB's inventory alone. A `git-only` registration
   MUST make its branch `in-use` and therefore undeletable, and MUST carry the
   remedy that clears the registration.
4. A `git-only` or `wb-only` row MUST be reported as a finding, never silently
   reconciled, because a disagreement between the two records is exactly the
   state in which an unsupervised sweep does the wrong thing.

#### REQ: preconditions-gate-apply

Before the first destructive operation **in a unit**, `wb cleanup --apply` MUST
complete and verify that unit's preservation capture, and MUST complete the
stacked-pull-request pre-flight for every branch that unit will retire, exactly
as [Preservation and Pre-Flight](cleanup-preconditions/README.md) specifies. A
failed or unverifiable precondition MUST refuse that unit's destructive stages
and MUST NOT be overridable by any flag.

Before the run performs anything destructive anywhere, it MUST print the
preservation root to stderr, so an interrupted run still tells its operator
where the recoverable copy is.

### Output contract

This section is normative for `wb cleanup`, `wb unpushed`, `wb audit`, and
`wb recover` alike. Each child feature adds its own row schema; none restates
these rules.

#### REQ: stdout-is-the-report-only

stdout MUST carry the report and nothing else. Progress, warnings, and
informational classification MUST go to stderr. `--format json` stdout MUST
parse as a single JSON document; `--format ndjson` stdout MUST parse as one
JSON object per line; neither MUST ever contain a progress or warning line.

`--format json` MUST emit a versioned envelope carrying at least
`schema_version`, `run_id`, `command`, `started_at`, `finished_at`, `selection`
(the resolved filter, projects root, stages, and base), `units`, `results`,
`diagnostics`, `summary`, and — for an `--apply` run — `preservation_root` and
`report_path`.

`--format ndjson` MUST stream one object per line, flushed as each is produced,
with a `type` field drawn from the closed set `run_started`, `unit_started`,
`decision`, `stage_applied`, `unit_finished`, `run_summary`. The union of the
streamed records MUST carry the same information as the `json` envelope, so an
agent that needs incremental output is not forced to trade fields for it.

#### REQ: filter-scopes-work-not-output

`--filter` MUST scope the work performed, not merely the rows printed. A
repository, task, worktree, or internal artifact excluded by `--filter` MUST
NOT be inspected, MUST NOT cause a Git subprocess to start, and MUST NOT emit
any row, diagnostic, warning, or informational line on any stream.

This is currently violated: `inspectLifecycleArtifact` in
`internal/worktrees/lifecycle.go` appends every WB-internal artifact it meets
with no `filterMatches` guard, and both `wb worktree list` and
`wb worktree summary` in `cmd/wb/worktree.go` print all of them. No command in
this feature MUST reproduce that, and the shared inventory path MUST be fixed
rather than worked around, so `wb worktree list` inherits the fix.

#### REQ: bounded-default-stderr

By default a run's stderr MUST consist only of:

- one run-start line naming the selection and the unit count;
- one progress line per unit, carrying the unit identity and a running `[n/N]`
  count, flushed as the event happens rather than buffered to the end;
- one line per **candidate decision**, emitted at the moment that candidate is
  decided rather than held until the run finishes, carrying the candidate
  identity and its disposition, plus any throttling wait in progress;
- one warning line per genuine anomaly, each naming a remedy
  (`#req:every-warning-names-a-remedy`);
- **one aggregate line** summarising WB-internal artifact classification for
  the whole run — counts by kind and state, never one line per artifact;
- one closing summary carrying per-disposition totals and elapsed time.

Per-artifact informational lines MUST be emitted only under `--verbose`, and
MUST always be present in the `--format json` envelope regardless of
`--verbose`, so suppressing the noise never loses the data.

The per-candidate decision line is bounded by the size of the report itself,
not by the number of internal artifacts, so it is signal rather than the noise
`#req:filter-scopes-work-not-output` removes. It is required because the
current command buffers its entire plan until completion: an observed run
produced zero bytes for over four minutes and then blew a 120-second foreground
timeout with nothing to show for the work it had done. Streaming decisions also
gives an operator a live view of throttling
(`#req:secondary-rate-limits-fail-to-unreadable`).

Progress MUST NOT be suppressed because stderr is not a terminal. An agent
reads stderr, and a multi-minute silence is indistinguishable from a hang; the
observed consequence is a killed and retried sweep leaving task locks that then
need `--resume-interrupted` to clear. A live terminal UI is permitted only when
stdout is a terminal and `--non-interactive` is unset.

#### REQ: every-warning-names-a-remedy

Every warning MUST name the exact command or decision that resolves it. A
warning that only states a condition reproduces the discoverability failure
this feature exists to remove.

In particular, a linked worktree that fails identity validation — today
reported only as `WB worktree <path> is not on a feature branch`, observed for
`engine-stack-regression` and `review-task5` — MUST additionally:

1. appear as a row in [`wb audit`](fleet-audit/README.md) with the
   `unclassified` disposition rather than being dropped from the inventory as
   an inspection error, so it is countable and visible;
2. carry a remedy naming `wb worktree rename`, `wb worktree abort`, or the
   explicit human decision required; and
3. block its own unit's destructive stages, because WB cannot reason about a
   checkout whose identity it could not establish.

#### REQ: never-report-a-summed-safe-count

No command in this feature MUST print, in any format, an aggregate that adds
the `contained` count to the `absorbed` count, or any single number described
as "safe to delete" that includes `absorbed`. `absorbed` MUST be rendered
distinctly from `contained` and MUST carry its report-only reason and its
remedy on every row.

This is the exact mistake made by hand on 2026-08-19, when 280 branches were
classified safe on `git cherry` patch-id evidence. Patch-id equality cannot
distinguish work that landed from work that landed and was then reverted: the
revert is a separate commit, every original patch still has an upstream
patch-id twin, and `git cherry` still emits zero `+` lines. A summed headline
number is how that mistake is made, so the summary must make it unavailable.

### Exit codes

#### REQ: documented-exit-codes

Every command in this feature MUST use WB's documented codes from
`cmd/wb/main.go`: `0` the command ran and found nothing needing attention, `1`
the command ran and reported findings or failures, `2` the invocation was
rejected before work started.

`wb cleanup` MUST exit `0` when the run completed and every selected unit
reached a terminal decision, including a plan in which everything was refused —
a refusal is a correct answer, not a failure. `--fail-on refusals` MUST make
any refusal exit `1`, so a merger lane can gate on it. A unit that failed to
reach a decision MUST exit `1` regardless of `--fail-on`.

### Discovery

#### REQ: dedicated-cleanup-skill

This feature is not complete until an agent asked to "clean up" finds it:

- `ai/skills/wb-cleanup/SKILL.md` and `ai/skills/wb-cleanup/agents/openai.yaml`
  MUST exist.
- `ai/skills/commands.json` MUST route `cleanup`, `audit`, `unpushed`, and
  `recover` to `wb-cleanup`.
- `ai/capabilities.json` MUST carry `wb.audit`, `wb.cleanup`, `wb.recover`, and
  `wb.unpushed` rows whose runtime flags match non-inherited public help
  exactly, with help anchors, skill evidence, and test evidence.
- The `wb-worktrees` and `wb-branches` skills MUST gain a reciprocal pointer to
  `wb-cleanup` for the whole-lifecycle case.

The skill MUST NOT be added before the commands exist: a skill advertising an
absent surface outranks the working triggers and sends an agent to a command
that fails, which is worse than the gap it closes.

The trigger text is normative and MUST be used verbatim, because it is the only
text an agent reads before deciding whether to load the skill:

```yaml
---
name: wb-cleanup
description: Use WB to clean up after finished work in one repository or across the whole fleet — retiring worktrees, local branches and remote branches in one ordered pass (wb cleanup), auditing what is left (wb audit), finding work that exists only on this machine (wb unpushed), and investigating pull requests orphaned by a deleted base branch (wb recover). Use when asked to clean up, tidy up, retire finished work, find unpushed or at-risk work, or check what a disk failure would destroy. Never hand-roll a raw git sweep over branches and worktrees — it has no preservation, no audit trail, no stacked-pull-request retargeting, and cannot tell work that landed from work that landed and was then reverted.
---
```

### Non-goals

#### REQ: bounded-scope

This feature MUST NOT change `wb worktree cleanup` or `wb branch cleanup`
eligibility; MUST NOT create, rename, or restore branches; MUST NOT merge pull
requests; MUST NOT delete a preservation artifact; MUST NOT introduce any flag
that disables preservation or that makes `absorbed` deletable; and MUST NOT act
at all without `--apply`.

## Acceptance Criteria

### AC: orchestrator-sequences-three-stages-in-order

**Requirements:** cleanup-orchestration#req:top-level-cleanup-verb, cleanup-orchestration#req:dry-run-default, cleanup-orchestration#req:serial-within-a-unit, cleanup-orchestration#req:fetch-before-every-decision

Given a fixture repository with a real bare remote holding `main`, a linked
worktree on a branch whose head is an ancestor of `origin/main`, that branch's
local ref, and its remote ref, when `wb cleanup` runs without `--apply`, then
nothing is removed, no report or preservation directory is created, and the
plan lists all three stages for that branch in the order worktree,
local-branch, remote-branch. When `wb cleanup --apply` then runs against a
recording Git wrapper, then the recorded operation order for that branch is
worktree removal, then `git update-ref -d` against the exact planned SHA, then
`git push --force-with-lease` against the observed remote SHA; the target SHA
used for every decision equals the SHA fetched during that run and not a
pre-seeded stale remote-tracking ref; and `wb cleanup --stages
local-branch,remote-branch --apply` on the same fixture deletes nothing and
reports the branch ineligible with a reason naming the excluded `worktree`
stage.

### AC: units-are-parallel-and-never-share-a-clone

**Requirements:** cleanup-orchestration#req:cleanup-unit-partitioning, cleanup-orchestration#req:parallel-across-units

Given a fixture fleet of eight canonical clones in which two clones are joined
by one coordinated WB task and one of those clones additionally holds bare
branches with no task, when `wb cleanup --parallel 4` runs, then the reported
partition places both coordinated clones in one unit together with those bare
branches; no two units name the same canonical clone; a probe recording the
unit identity active against each clone shows at most one at any instant; at
most four units are in flight at any instant; a unit made to fail leaves every
other unit's decisions unchanged and the run still processes them all; and two
consecutive runs over unchanged state produce byte-identical `--format json`
stdout despite differing completion order.

### AC: inspection-is-parallel-and-mutation-is-not

**Requirements:** cleanup-orchestration#req:concurrency-lives-in-the-shared-inspection-path, cleanup-orchestration#req:two-independent-concurrency-bounds, cleanup-orchestration#req:secondary-rate-limits-fail-to-unreadable, cleanup-orchestration#req:partial-failure-is-per-candidate

Given a fixture fleet of twelve canonical clones, several of them backing more
than one linked worktree, and a Git wrapper and hosted double that both record
the clone and timestamp of every call, when `wb cleanup --parallel 4` and
`wb worktree list --github` each run, then the recording shows fetches from
distinct clones overlapping in time — proving inspection is parallel and that
the parallelism is inherited from the shared layer rather than added in the
command — while no two fetches into the same clone ever overlap. When
`wb cleanup --apply` then runs, the recording shows every preservation write,
Work Log archive write, worktree removal, and ref deletion strictly serialized,
with each unit's Work Log archive write ordered before that unit's first
deletion, and no two mutating operations overlapping anywhere in the run.

Given the same fixture with the hosted double returning `403` with a
`Retry-After` header for two candidates until it has been retried several
times, when the run completes, then those two candidates are reported
`unreadable`, neither is reported `eligible`, neither is omitted, neither is
reported as "no merged pull request found"; the double records a wait of at
least `Retry-After` and increasing delays across retries rather than immediate
ones; a throttling event appears in the streamed stderr progress while the run
is still going; and running with `--fetch-parallel 8 --api-parallel 2` shows
concurrent fetches reaching eight while concurrent hosted requests never exceed
two, proving the two bounds are independent counters and that neither is
derived from `--parallel`. Given one clone whose fetch fails and one worker
made to panic, then each is reported as a diagnostic against its own clone
only, every other clone reaches its normal disposition, and the process exits
having reported them all.

### AC: a-sweep-refuses-rather-than-exhausting-the-shared-quota

**Requirements:** cleanup-orchestration#req:hosted-budget-preflight

Given a hosted double reporting 3,973 remaining core requests with a reset 44
minutes away, and a selection of 269 candidates, when `wb cleanup` runs with
the default `--api-reserve 1000`, then WB reads the rate limit before issuing
any candidate query; with an estimate of at least two requests per candidate
the run proceeds, because the projected remainder stays above the floor. When
the double instead reports 1,200 remaining, then the run refuses before issuing
any candidate query, exits `2`, and prints the remaining quota, the estimate,
the reserve floor, the reset time, and the no-hosted-evidence mode as the
alternative; a request counter confirms no candidate query was made. When
`--api-reserve 0` is given, then the same run proceeds. The refusal path MUST
be asserted to happen before the first candidate query, not part way through.

### AC: orchestrator-cannot-outreach-the-scoped-commands

**Requirements:** cleanup-orchestration#req:no-independent-eligibility, cleanup-orchestration#req:bounded-scope

Given a fixture containing one branch of each disposition in the branch-hygiene
taxonomy — `contained`, `absorbed`, `unique`, `protected`, `in-use`,
`unreadable` — plus a worktree holding uncommitted changes and a remote branch
that is the head of an open pull request, when `wb cleanup --apply` runs once
for every combination of its flags, then the set of refs and worktrees deleted
is identical in every combination to the set deleted by `wb worktree cleanup
--apply --remote` plus `wb branch cleanup --scope all --apply` over the same
fixture; the `absorbed` branch is never deleted in any combination; the dirty
worktree is never removed; and the pull-request-backed remote branch is never
deleted.

### AC: a-run-always-terminates-and-sees-both-worktree-records

**Requirements:** cleanup-orchestration#req:bounded-per-unit-time, cleanup-orchestration#req:reconcile-git-and-wb-worktree-views

Given a fixture fleet containing a unit whose hosted evidence call never
returns, when `wb cleanup --timeout 2s --apply` runs, then that unit is
reported with the `timeout` outcome, no ref or worktree in it was deleted, no
stage was left half-applied, every other unit still reached a terminal
decision, and the process exited. Given a second fixture in which one canonical
clone holds a Git worktree registration pointing at a directory outside the WB
write home, one registration whose directory has been deleted, and one
directory inside the WB write home with no Git registration, when `wb cleanup`
and `wb audit` run, then all three appear as rows with `known_by` values
`git-only`, `git-only`, and `wb-only` respectively; the branch named by each
`git-only` registration is reported `in-use` and is not deleted under `--apply`
in any flag combination; and each row carries a remedy command.

### AC: apply-is-gated-on-verified-preconditions

**Requirements:** cleanup-orchestration#req:preconditions-gate-apply

Given a unit holding a dirty worktree, an unpushed stash, and a branch that is
the base of one open pull request, when `wb cleanup --apply` runs, then the
preservation root is printed to stderr before any destructive operation; the
unit's preservation artifacts exist and verify before its first destructive
operation; the dependent pull request is retargeted, and the retarget re-read
and confirmed, before the remote ref is deleted; and when preservation
verification is made to fail, or the retarget is made to fail, then that unit
performs no destructive operation at all, reports the refusal, and no flag
exists that permits the run to proceed.

### AC: output-is-usable-by-an-agent

**Requirements:** cleanup-orchestration#req:stdout-is-the-report-only, cleanup-orchestration#req:filter-scopes-work-not-output, cleanup-orchestration#req:bounded-default-stderr, cleanup-orchestration#req:every-warning-names-a-remedy, cleanup-orchestration#req:never-report-a-summed-safe-count, cleanup-orchestration#req:documented-exit-codes

Given a fixture fleet of twenty canonical clones of which three match
`--filter`, and in which the non-matching clones hold WB-internal
`.wb-retired-stage-*` artifacts and a worktree that fails identity validation,
when `wb cleanup --filter <substring> --format json` runs with stdout and
stderr captured separately, then stdout parses as one JSON document with no
progress or warning text in it; a Git-subprocess probe shows no process started
against any non-matching clone; stderr contains no line naming a non-matching
clone; stderr contains at most one progress line per selected unit plus the
fixed run-level lines, and exactly one aggregate line for WB-internal artifact
classification rather than one line per artifact, while the JSON envelope still
carries every individual artifact; the same run with `--verbose` emits the
per-artifact lines; at least one progress event appears on stderr before the
report is written; the identity-validation failure appears as an
`unclassified` row whose warning names a remedy command; no output field in any
format sums `contained` and `absorbed`; and the exit code is `0` for a
completed plan containing refusals and `1` for the same run with
`--fail-on refusals`. Repeating the run with `--format ndjson` yields one JSON
object per stdout line whose union carries the same fields as the envelope.

### AC: the-capability-is-discoverable

**Requirements:** cleanup-orchestration#req:dedicated-cleanup-skill

Given the four commands exist and pass their own acceptance criteria, when the
repository's capability validator runs, then `ai/skills/wb-cleanup/SKILL.md`
and `ai/skills/wb-cleanup/agents/openai.yaml` exist; the `SKILL.md`
frontmatter `description` equals the normative trigger text byte for byte;
`ai/skills/commands.json` routes `audit`, `cleanup`, `recover`, and `unpushed`
to `wb-cleanup`; `ai/capabilities.json` carries `wb.audit`, `wb.cleanup`,
`wb.recover`, and `wb.unpushed` rows whose declared flags equal each command's
non-inherited public help exactly, each with a resolving help anchor, a skill
example that parses, and an executable test reference; and the `wb-worktrees`
and `wb-branches` skills each contain a pointer to `wb-cleanup`. A test MUST
assert that the skill files are absent while any of the four commands is
absent.

## Open Questions

- Should `wb cleanup` gain a `--since <marker>` mode that limits the sweep to
  repositories whose canonical clone changed since the last run, so a daily
  sweep costs proportional to the day's work rather than to the fleet?
- Should the connected-component unit partition be exposed as its own read-only
  verb so a merger lane can schedule units itself, or is the in-run partition
  report sufficient?

---
*This document follows the https://specscore.md/feature-specification*
