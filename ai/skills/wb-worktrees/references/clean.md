# Clean up finished worktrees and branches

`wb clean` is the one fleet verb for hygiene: it classifies **every** WB
checkout, **every** branch that no longer has a checkout, and every linked
worktree WB does not manage, then retires the ones that are provably finished.
`wb worktree retire <task|branch>` is the per-item transaction it delegates to.
Both are dry runs until `--apply` is explicit. Read the plan before applying it,
every time.

Never hand-roll this with raw Git. A loop over `git branch --merged`,
`git worktree list`, `git branch -D`, or `git push --delete` has no Work Log,
no audit report, no lease protection, and no coordinated-task safety, and it
cannot tell content that landed from content that was reverted after landing.

A rising count of retirable items means a landing verb stopped cleaning up after
itself. `wb clean` is the safety net, not the mechanism: fix the verb, do not
schedule the sweep.

## Pick the command by what you were asked

| The request | Start here |
| --- | --- |
| "clean up worktrees", "clean up leftovers", "the disk is full" | `wb clean` |
| "what is safe to delete?", "audit worktree/branch hygiene" | `wb clean` (the dry run is the audit) |
| "delete every merged branch and worktree" | `wb clean --scope all`, then `--apply` |
| "only this owner / repository" | `wb clean --filter 'datatug/*'`, `--org datatug`, or `--repo datatug/websites` |
| "this task's PRs merged, tidy it up" | `wb worktree retire <task>` (a branch name works too) |
| "cleanup refused, but I know this landed" | `wb clean --task <task>`: it decides merged-ness by commit identity |
| "the sweep sees nothing, these are not WB worktrees" | `wb worktree adopt --all-external` |
| "the work is unfinished / never merged" | `wb worktree abort <task>`, or `wb worktree retire <task> --preserve branch` |
| "reuse this worktree for the next task" | `wb worktree rename <old> <new>` |

## Run it

```sh
# Plan a fleet-wide sweep. Dry run is the default; nothing is removed.
wb clean

# One owner, landed work only.
wb clean --filter 'datatug/*' --only landed

# Retire everything provably finished, including origin branches.
wb clean --scope all --apply

# Machine-readable plan for an agent.
wb clean --org datatug --format json
```

Selectors narrow the dry run and `--apply` identically, through one predicate:

- `--filter <s>`: owner/name substring, or a glob such as `'datatug/*'`.
- `--org <owner>` (repeatable), `--repo <owner/name>`: exact selectors.
- `--branch <name>`: one exact branch, of a checkout or of a bare branch ref.
- `--task <name>` (repeatable): exact WB tasks; selects checkouts only.
- `--scope local|remote|all`: `local` (default) is checkouts plus local
  branches; `remote` is origin branches only; `all` is both, and a retired
  checkout's unchanged origin branch is retired with it.
- `--only <class>`: one class from the tables below, or `landed` for every
  class that proves a landing.
- `--older-than` (default 24h): keep work whose pull request merged, or whose
  branch was last committed, more recently than this. `0` disables it.

A selection that matches no local repository exits 1 with
`0 repositories matched --filter <s>`, never an empty success: that silent
empty result hid a typo'd glob for a whole audit.

`--parallel N` (default 8) bounds both phases. Repositories are inspected that
many at a time, and the apply retires that many at once, never two in one clone
or one task, because Git allows one writer per clone. Each repository's target
is fetched once per walk however many tasks sit on it.

## Why the obvious signal is wrong

A **squash merge produces a new commit**, so the source branch is not an
ancestor of `main` and `git` reports every landed branch as unmerged, forever.
Add one ordinary post-merge commit and even GitHub's commit index for the head
returns nothing, because that head was never pushed. On one measured sweep this
made 7 of 11 refusals wrong: every one of them a merged branch, all reported as
"awaiting push".

`wb clean` therefore decides merged-ness by **commit identity** — GitHub's
commit-to-pull-request index for the head, then for its ancestors — never by
branch-name ancestry, and never by the branch's name at all.

## Run it

```sh
# Plan a fleet-wide sweep. Dry run is the default; nothing is removed.
wb clean

# Retire everything provably finished.
wb clean --apply

# Machine-readable plan for an agent.
wb clean --format json
```

Exit codes: `0` nothing needed attention, `1` something was kept, `2` usage.

## The classes

| Class | Evidence | Disposition |
| --- | --- | --- |
| `contained` | head is in the freshly fetched origin target | retire |
| `landed-clean` | landed by receipt: squash, rebase, or absorbed by an integration branch | retire |
| `landed-residue` | landed, plus local commits past the landed head | retire **only** with `--allow-residue` |
| `detached-review` | detached, and its head is contained in the target or named by a merged pull request — the row says which | retire |
| `detached-unknown` | detached, no landing association | keep |
| `open-pr` | a pull request is still open | keep |
| `dirty` | uncommitted changes | keep |
| `claimed-live` | a live operation or session holds it | keep |
| `unpushed` | GitHub's commit index has never seen this head | keep, always |
| `unmerged` | pushed, not landed, no open pull request | keep |


Branches without a checkout are classified by Branch Hygiene with landing
receipts on:

| Class | Evidence | Disposition |
| --- | --- | --- |
| `contained` | ancestor of the freshly fetched target | retire |
| `receipted` | a merged pull request into the target proves the landing; that includes a squash-merged branch whose tip is exactly the merged pull request's head, after the target moved on, unless the target reverted the landing | retire |
| `superseded` | a trusted reviewer receipt (`--superseded-by`) replaces it | retire |
| `absorbed` | equal to the target by patch-id or tree **only** | keep, always |
| `unique` | carries work the target does not | keep |
| `unreadable` | required evidence could not be read | keep |

A branch with an open pull request is never retired, and a branch a checkout
holds is that checkout's row, not a branch row.

Linked worktrees WB does not manage are listed as `unmanaged` and never
removed. Every WB-managed checkout is classified by the one landing classifier
above, so two verbs can never again disagree about the same head. Adopt an
unmanaged worktree (`wb worktree adopt`) to bring it under that classifier.

## Refusals and what resolves each

Every kept row prints its reason and the exact command that satisfies it. Run
that command; do not work around the refusal with raw Git.

Every refusal names a command that **exists and works on the shape it was named
for**. That is the whole point: a refusal an agent cannot resolve becomes a
hand-written workaround, and a named command that fails on the shape it was
named for is worse than no name at all.

Two of these depend on whether WB created the checkout, which the row reports as
`management`: **managed** (WB wrote its manifest and it reads), **unmanaged**
(a manifest is there and does not read or does not name its repository and
worktree), or **unknown** (no manifest at all — a checkout made with
`git worktree add` or `gh pr checkout`). Absence of evidence is
`unknown`, never `unmanaged`, and an unknown checkout gets the same care as one
WB owns: not knowing must never widen what the tool is willing to suggest.

**A dirty checkout is captured before anything is removed.** The changes are
uncommitted, so whatever the capture writes is their only copy. The row for a
dirty checkout WB does not own therefore names the capture and nothing else;
once the tree is clean, rerun `wb clean` and it classifies the checkout on its own
evidence.

| Refusal | Sanctioned next step |
| --- | --- |
| `dirty`, managed | `wb worktree abort <task> --apply` — seals the Work Log and captures the dirty bytes into a private archive before deleting anything |
| `dirty`, unmanaged or unknown | the exact `git -C <path> stash push --include-untracked -m "wb clean <task>"` the row prints. That is the whole instruction: **no removal is named**, and the row's warning states that removing the checkout without the capture destroys the changes and nothing in WB can bring them back |
| `claimed-live` — a live operation holds the task lock | `wb worktree retire <task> --resume-interrupted` once the operation is really dead |
| `claimed-live` — the checkout was used inside `--session-freshness` (default 6 h) | `wb worktree end <task>` when you are done with it. This outranks **every** landed class, and it applies to every checkout — including a detached review checkout with no owner registration at all, which is exactly the population a sweep once removed out from under a reviewer |
| `open-pr` — the pull request is still open | `wb worktree merge <task> --route auto` |
| `landed-residue` — landed, holding local commits | `wb clean --task <task> --allow-residue --apply`, after reading the residual commits it lists |
| `detached-unknown`, managed | `wb worktree abort <task> --disposition discarded --apply` |
| `detached-unknown`, unmanaged or unknown | the exact `git -C <canonical> worktree remove <path>` the row prints — deliberately **without** `--force`, so Git itself refuses if the tree turns out to hold changes. The row also warns that the commit is unreferenced once the checkout is gone, and prints the `git branch` invocation that keeps it. `wb worktree rescue` refuses a linked worktree by design, and `wb worktree adopt` cannot reconstruct a manifest for a detached HEAD — naming either would hand you a command that fails. `wb worktree review` will close this gap by creating review checkouts tracked and claimed |
| `unpushed` — the head was never pushed | `wb worktree merge <task> --route auto` to land it. **Nothing retires this class**; it is the only one that can lose work |
| `unmerged` — pushed but not landed, or the landing walk hit `--residue-depth` | `wb worktree merge <task> --route auto`, or rerun with a larger `--residue-depth` when the row says the walk was truncated |

There is **no force flag**, and asking for one is the wrong move.
`--allow-residue` and `--superseded-by` are the only two widenings; neither
skips a proof. `--allow-residue` prints the commits it is about to discard;
`--superseded-by <receipt.json>` names one trusted-reviewer receipt for one
named task, and every head that receipt binds is re-verified before anything is
retired.

## Retire one item

```sh
wb worktree retire <task>                          # plan
wb worktree retire <task> --apply --remote         # retire worktrees and branches
wb worktree retire batch5 --apply --remote         # a branch name resolves to its task
wb worktree retire <branch> --repo owner/name      # a bare branch no checkout holds
wb worktree retire <task> --preserve branch --apply  # work that never landed
```

A name that is not a task is resolved as a branch: the task whose checkout has
that branch is retired (stderr names it), and a branch no checkout holds is
retired as a bare ref on the same evidence `wb clean` uses for branches. An
origin-only branch needs `--remote`.

Without `--preserve`, retirement requires landing evidence and keeps every
guard below. `--preserve branch|tag` is the audited way to retire one task whose
work did **not** land: it commits remaining changes with normal hooks,
publishes the exact commit as `retired/*`, archives the Work Log in the private
retirement repository, and only then removes the checkout. See
[lifecycle.md](lifecycle.md).

## Trap 1: cleanup only sees WB-managed tasks

`wb worktree retire`/`abort` discover tasks in the configured shared store and
in existing repository-local `.worktrees/<task>` stores. A linked worktree
created outside those WB task stores, such as a plain `git worktree add`, is not
a candidate at all — it is silently outside the sweep, not skipped with a
reason. This is the single most common reason a fleet-wide sweep reports far
fewer retirable checkouts than the `unmanaged` rows suggest.

Give those worktrees an identity, then a task, before expecting `retire`/
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
`retire`/`abort` a task to resolve; they still refuse with `task ... was not
found` afterward. `adopt` writes that task directory, manifest, and Work Log
claim, reusing `backfill`'s reconstruction rather than duplicating it. Both
are additive by construction — nothing moves, no working tree is touched — so
either is safe to run against a fleet with live agents and safe to re-run
after an interruption; an already-adopted worktree is reported
`already_adopted` and left alone. Neither fabricates a prompt; `wb worktree
set --prompt` gives a worktree its first real one.

`adopt` selects with either exactly one worktree path, or `--all-external` to
sweep every worktree `wb clean` reports as an `unmanaged` row with `layout: "external"`; honour
`--filter` to narrow a sweep. After `adopt --apply`, `wb worktree retire
<task>` and `wb worktree abort <task>` apply their full existing safety checks
to that worktree unchanged — dirty, unlanded, an open pull request, a held
lock, and `awaiting_push` are all still refused exactly as for a worktree WB
created directly.

Applying adoption is a mutation: `--mode agent` requires a live registered
session, while `--mode manual --initiator <human>` is the explicit audited
operator path. The default dry run does not require session admission. Task
recycling through `wb worktree rename <old-task> <new-task>` follows the same
contract when `--apply` is selected.

Flags: `wb worktree backfill` — `--base` (default `main`), `--apply`, `--format
text|json`. `wb worktree adopt` — `--base` (default `main`), `--all-external`,
`--apply`, `--mode`, `--initiator`, `--format text|json`, plus root `--filter`.
Both are dry runs by default.

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
wb worktree retire <task> --absorbed-by <pr-number-or-landing-commit>
```

That flag only selects which receipt to verify. Every proof still runs, and the
named commit must be exactly where the work entered the target, so it can never
make unlanded work eligible.

For a reviewed split branch whose original head intentionally cannot land as a
single unit, use the separate supersession receipt path:

```sh
wb worktree retire <task> --superseded-by supersession.json
wb worktree retire <task> --superseded-by supersession.json --apply --remote --older-than 0
```

The JSON receipt is the only authority for this disposition. It must bind the
exact original source head and freshly fetched target head, list landed
replacement PRs or commits, enumerate every source commit outside the target,
classify each residual as replaced, obsolete, regressive, or cosmetic, mark
each residual reviewed, and carry a trusted approving actor and receipt ID.
This path is named-item only; missing or unclassified
residuals, an untrusted approval, changed source/target refs, or a replacement
not contained in the target refuse without deleting state. The terminal Work
Log embeds the verified receipt before remote or local deletion.

A `wb worktree merge` integration candidate is a special case of "never
pushed": its own branch (`wb/integration/...`) is deliberately never published
under its own name, so the ordinary "was never pushed" refusal always fires
for it once the merge lane is done with it. Cleanup (and `wb worktree end`,
which retires through the same transaction) recognizes one narrow extra proof
for exactly this shape: a worktree-merge receipt under `~/.wb/reports/
worktree-merge/` naming this task and worktree as its candidate, carrying a
validated `wb worktree merge acknowledge-absorbed-conflict` acknowledgement —
bound by a plain SHA-256 of the receipt's own unchanged bytes, and recording a
target head that is an ancestor of the freshly fetched `origin/<target>`. A
missing, tampered, or stale-target acknowledgement changes nothing: the
original refusal stands, now naming the exact receipt to run `acknowledge-
absorbed-conflict` against. A valid one reports `proof:
absorbed_conflict_acknowledgement` on the cleanup result, together with the
acknowledgement path and the receipted source SHAs it proved already reached
the target.

## A branch with no worktree at all

`wb clean` covers it: a local or remote branch with no linked worktree and no
Work Log claim is a branch row, classified by Branch Hygiene's evidence engine
(see the `wb-branches` skill and `spec/features/branch-hygiene/`). On a real
fleet that is the large majority: a sweep measured 1,081 local branches, of
which 750 were provably safe to delete. A branch that *does* have a worktree or
a live claim is never a branch row; its checkout's row decides it. To retire
one bare branch, name it: `wb worktree retire <branch> --repo <owner/name>`.

## Trap 3: which target a head is judged against

`wb worktree retire` judges each repository against the base recorded when its
worktree was created, not against `main`. Two things change that, and the plan
names the target that proved the work whenever it is not the recorded base
(`integration_proof` in JSON, in parentheses on the `would remove` line):

- **The recorded base cannot answer.** Origin no longer has it (an integration
  branch that landed and was deleted), or it is still there and has itself
  landed in the default branch (a task stacked on another task's branch). The
  head is then judged against the freshly fetched default branch and is
  eligible only when it is a plain Git ancestor of it:
  `contained in origin/main at 0123456789ab, via recorded base cockpit-ux (absent)`.
  A head that is not contained stays in the plan with the reason; an absent
  base never hides the task from `list`, `end`, `abort`, or `wb clean`.
- **You name `--base <branch>`.** That is then the exact origin target, and
  nothing else: the recorded base is only reported (`recorded_base`), no other
  branch is substituted, and a branch origin does not have is an error. The
  plan line then reads `contained in origin/<branch> at <sha>, the base named
  with --base`.

```sh
wb worktree retire <task>                    # recorded base, or the default branch as above
wb worktree retire <task> --base <branch>    # exactly this branch
```

`--base` left at its default is only the fallback for a worktree with no
recorded base. `--absorbed-by <pr|commit>` is verified against the target
actually judged, so a pull request or merge commit into the default branch is
accepted for a task recorded against an integration branch.

One run that retires a stack (`wb clean`, or several
named tasks) finishes each stacked task before the task whose branch it is
stacked on, so no hand-rolled leaf-first loop is needed. To clean every
integrated task of one repository in one call:

```sh
wb clean --repo <owner/repository> --older-than 0
wb clean --repo <owner/repository> --older-than 0 --scope all --apply
```

## Trap 4: dry run is the default, and `--apply` is not enough

```sh
wb worktree retire <task>
wb worktree retire <task-a> <task-b> --apply --remote --parallel 2
wb clean
wb clean --older-than 0
wb clean --format json
wb clean --filter acme
```

`wb worktree retire` requires one or more named tasks or branches; the fleet
sweep is `wb clean`. Named tasks default to immediate eligibility
(`--older-than 0`) because that is their terminalization journey; `wb clean`
keeps a 24-hour grace window unless `--older-than` overrides it.

For named tasks, `--apply` requires `--remote` only while the source branch
still exists on origin. If the remote branch is already gone, cleanup can retire
the local worktree and branch without it.

```sh
wb worktree retire <task> --apply --remote --older-than 0
wb worktree retire <task-a> <task-b> --apply --remote --parallel 2
```

`wb worktree retire` flags: `--base`, `--apply`, `--remote`, `--older-than`,
`--parallel`, `--report-dir`, `--absorbed-by`, `--superseded-by`,
`--allow-residue`, `--resume-interrupted`, `--recover-stages`, `--repo`,
`--peer-evidence`, `--require-host`, `--preserve`, `--message`, `--format`,
`--json`, `--mode`, `--initiator`, and `--verbose` (`-v`). Inherited root
flags: `--filter`, `--projects-root`, `--quiet`, and `--non-interactive`.

`--recover-stages` is named-task-only and cannot be combined with
`--preserve`. It inventories exact non-empty retired stages; `--apply` archives
them and writes a private receipt. Retire the task again after recovery. Empty
task shells need no flag: `wb clean` plans them and retires them under
`--apply`.

### Legacy task with missing Work Log history

An older repository-local task can still be cleaned up when its private Work Log
claim is missing, but only through a narrow recovery path. Cleanup requires a
consistent immutable created manifest and projection, their deterministic claim
ID, a clean attached checkout whose exact base-to-head-to-fetched-target
ancestry verifies, and the existing open-PR preflight to pass. Unverifiable or
changed evidence remains blocked. No new flag enables this path; ordinary
`wb worktree retire` and `wb clean` safety checks still apply.

Before destructive apply, WB writes an immutable
`legacy_missing_history_retirement` receipt under
`<projects-root>/.wb/reports/worktree-cleanup/backlog/missing-history/`. It
records that original history is unavailable; it does not invent the original
claim, terminal, or prompt archive. The normal cleanup backlog handles an
interrupted apply.

`--report-dir` overrides the audit directory, which defaults to
`<projects-root>/.wb/reports/worktree-cleanup/<timestamp>`. A plan is read-only even when
`--report-dir` is supplied; artifacts are written only for an apply attempt,
before its first destructive Git operation, and updated with applied or failed
state.

`--resume-interrupted` is named-task-only recovery. It validates the retained
`.lock` as this operation with a conclusively dead PID before holding it
through a normal cleanup.

## Reading a skip reason

A fleet sweep reports far more skipped tasks than eligible ones, and every skip
is WB being correct rather than WB being stuck:

- `current branch head <sha> is not integrated into the exact origin target
  origin/<base> at <sha> (awaiting push)` — the source branch is not on origin
  at this head: the work exists only locally. Land it, do not delete it.
- `... (pushed to origin/<branch>, awaiting merge)` — the branch is pushed and
  the named target does not contain it. Merge it, or check that the target
  named is the one you meant (Trap 3). `; recorded base <branch> is absent`
  says the default branch was judged because the recorded base is gone.
- `held: branch <b> is the recorded base of <task> (<repo>), which is not
  eligible: ...` — another listed task is recorded against this task's branch
  and cannot be retired yet. Retiring the base would delete that branch and
  close any pull request into it, so the base waits; resolve the named task.
  `held: <task> is stacked on a branch of <base> and was not retired in this
  run` is the same hold when the stacked task failed during apply.
- `branch still has an open pull request: <url>` — close or merge the PR first.
- `worktree has local changes` — uncommitted work. WB never removes it.
- `coordinated task blocked by <repository>` — one repository in a
  multi-repository task is ineligible, so every repository in that task is held.
  Resolve the named sibling.

Preserve skipped work and report the reason. Never reset, clean, stash, or
delete it manually to make a sweep pass.

## When the work never landed

`wb worktree retire` must refuse an unmerged worktree without `--preserve`, because there is no receipt. That
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

For tasks with an available original claim, cleanup seals one of two
dispositions based on what it proved. A worktree whose branch has commits of
its own that cleanup proved are on the target is sealed `landed`, with a
`landed` object in
the terminal record and its outbox receipt: `target`, `landed_sha`, `proof`
(`contained`, `merged_pull_request`, `rebase_merged` or `absorbed`) and the
`pull_request` number when known. Everything else it removes (a worktree that
never committed, a detached review checkout, a `wb/integration/` merge
candidate) is sealed `removed`. A claim `wb worktree log finalize --apply`
already sealed keeps that terminal. Records sealed by an older `wb` are not
rewritten. The missing-history path above records its retirement receipt without
synthesizing a terminal.

## Finish the sweep

A sweep is done when a re-run reports nothing, not when the first pass exits:

```sh
wb clean
wb clean
wb worktree list
```

Resolve every live entry and every durable backlog record. The normal terminal
state is zero cleanup backlog, not apparently-finished branches.

Long fleet sweeps are quiet: `wb clean` fetches and
queries GitHub for every candidate and prints its whole report at the end, so
several minutes of silence is expected progress, not a hang. Run it in the
foreground and let it finish; do not kill and retry it, which leaves task locks
behind for `--resume-interrupted` to clear.

## Residue

```sh
wb clean --task my-task            # reports: landed + residue, and lists the commits
wb clean --task my-task --allow-residue --apply
```

`landed + residue` means: this branch's work is in `main` — here is the pull
request and the commit that carried it — and the checkout holds N commits past
that point which are **not** anywhere else. Read them. They are almost always a
post-merge `git merge origin/main` or a review fixup that was squashed
differently, and discarding them is correct; occasionally one is real work, and
then the answer is to land it rather than to widen past it.

## Detached review checkouts

Every pull-request review creates a detached checkout, and WB used to warn about
it and then drop it from the inventory — so nothing could ever retire one. They
now appear in `wb worktree list` and in `wb clean`'s plan. A detached checkout whose
head is a merged pull request's head is `detached-review` and removable; one
with no landing association is refused and prints whether the commit exists on
origin at all.

## Sizes tell the truth twice

Every size is reported **apparent and unshared**, because pnpm hard-links
`node_modules` into its store: one sweep measured 11.7 GB apparent against 5.9
GB unshared, and only the unshared figure comes back when the tree is deleted.
The reclaim footer counts unshared bytes only. Pass `--skip-sizes` to skip the
measurement entirely.

The footer is **one accounting unit over the whole sweep**, not a sum of the
rows. Two worktrees that link the same store file would otherwise have it
counted twice in the apparent total and dropped from the unshared one, even
though removing both does return its blocks.

## What counts as "in use"

Four signals, and the newest of them wins, because a lane may be doing only one
kind of work:

- a **heartbeat** every `wb` command run *inside* the checkout refreshes — this
  is what keeps a lane that is only reading alive. It is keyed to the working
  directory, so a fleet sweep run from somewhere else refreshes nothing;
- the newest **modification time among the files Git reports as changed** — a
  lane that is only editing;
- the newest **Work Log event**;
- the newest **commit** on the branch.

This guard applies to **every** checkout, not only to ones carrying a live
owner registration. A review checkout has none, a `git worktree add` checkout
has none, and a checkout whose registering process has exited has none — and
those are precisely the checkouts a sweep once removed while they were in use.

A live process id counts for **nothing** on its own. Process ids are recycled,
and the question is whether a worktree is in use, not whether some process
exists. Two earlier versions of this rule got it wrong in both directions: one
kept ten finished checkouts open for seventeen hours on a recycled id, and the
next reported a lane three hours into its work as recycled because the owner
record is written once at creation and deduplicated thereafter.

`--session-freshness 0` disables the rule, the same spelling as
`--older-than 0`. A negative value is refused. A timestamp in the future is
ignored rather than treated as activity: a clock skew must not pin a checkout
open forever while reporting that it was used no time ago.

## Empty task husks

Removing a checkout leaves an empty `<task>/<owner>/<repository>` namespace
behind, including when the removal was one `wb clean`'s own advice named. It reports
those in the dry run and retires them under `--apply`, scoped to the tasks the
run selected — a named-task sweep never retires shells across the fleet on your
behalf.

## Terminal artefacts

Empty `.wb-retired-stage-*` directories and inert `.wb-retired-lock-*` files are
purged unconditionally and silently on any `wb worktree` read path, `wb clean`
included, and counted in the footer. They are not backlog and not worth a log
line — one workstation had 55 stage directories producing 55 `info:` lines
before every single `wb worktree list`.

## What wb clean never touches

- **Work Logs and event logs.** They are the evidence base for every WB report
  and are never deleted, compressed away, or pruned.
- **A non-empty retired stage.** That is audited recovery backlog; use
  `wb worktree retire <task> --recover-stages`.
- **Anything unpushed.** No flag exists to retire it.

## Lane contract

Consume a library through `wb deps propagate local`; the orchestrator runs
`remote` at the end. End with `wb worktree end`.
