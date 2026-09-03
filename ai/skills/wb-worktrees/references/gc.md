# Worktree garbage collection

`wb worktree gc` is the safety net for worktree hygiene: one verb that
classifies **every** WB-managed checkout by landing evidence and retires the
ones that are provably finished.

Use it when you are asked to "clean up worktrees", "the disk is full", "what is
safe to delete?", or at the end of a session sweep. Use
[`cleanup.md`](cleanup.md) when you already know the one task to retire; `gc` is
what you run when you do not, or when `cleanup` refused something you believe
landed.

A rising number of checkouts here means a landing verb stopped cleaning up after
itself. `gc` is the safety net, not the mechanism: fix the verb, do not schedule
the sweep.

## Why the obvious signal is wrong

A **squash merge produces a new commit**, so the source branch is not an
ancestor of `main` and `git` reports every landed branch as unmerged, forever.
Add one ordinary post-merge commit and even GitHub's commit index for the head
returns nothing, because that head was never pushed. On one measured sweep this
made 7 of 11 refusals wrong: every one of them a merged branch, all reported as
"awaiting push".

`gc` therefore decides merged-ness by **commit identity** — GitHub's
commit-to-pull-request index for the head, then for its ancestors — never by
branch-name ancestry, and never by the branch's name at all.

## Run it

```sh
# Plan a fleet-wide sweep. Dry run is the default; nothing is removed.
wb worktree gc

# Retire everything provably finished.
wb worktree gc --apply

# Machine-readable plan for an agent.
wb worktree gc --format json
```

Exit codes: `0` nothing needed attention, `1` something was kept, `2` usage.

## The classes

| Class | Evidence | Disposition |
| --- | --- | --- |
| `contained` | head is in the freshly fetched origin target | retire |
| `landed-clean` | landed by receipt: squash, rebase, or absorbed by an integration branch | retire |
| `landed-residue` | landed, plus local commits past the landed head | retire **only** with `--allow-residue` |
| `detached-review` | detached at a landed pull request's head | retire |
| `detached-unknown` | detached, no landing association | keep |
| `open-pr` | a pull request is still open | keep |
| `dirty` | uncommitted changes | keep |
| `claimed-live` | a live operation or session holds it | keep |
| `unpushed` | GitHub's commit index has never seen this head | keep, always |
| `unmerged` | pushed, not landed, no open pull request | keep |

## Refusals and what resolves each

Every kept row prints its reason and the exact command that satisfies it. Run
that command; do not work around the refusal with raw Git.

| Refusal | Sanctioned next step |
| --- | --- |
| `dirty` — the checkout has local changes | `wb worktree abort <task> --apply` (captures the dirty bytes durably first), or finish and land the work |
| `claimed-live` — a live operation holds the task lock | `wb worktree cleanup <task> --resume-interrupted` once the operation is really dead |
| `claimed-live` — a live session still owns it | `wb worktree end <task>` from that session |
| `open-pr` — the pull request is still open | `wb worktree merge <task> --route auto` |
| `landed-residue` — landed, holding local commits | `wb worktree gc <task> --allow-residue --apply`, after reading the residual commits it lists |
| `detached-unknown` — detached with no landing | `wb worktree rescue <path>` to put the content on a branch |
| `unpushed` — the head was never pushed | `wb worktree merge <task> --route auto` to land it. **Nothing retires this class**; it is the only one that can lose work |
| `unmerged` — pushed but not landed | `wb worktree merge <task> --route auto` |

There is **no force flag**, and asking for one is the wrong move.
`--allow-residue` is the only widening, it skips no proof, and it prints the
commits it is about to discard before discarding them.

## Residue

```sh
wb worktree gc my-task            # reports: landed + residue, and lists the commits
wb worktree gc my-task --allow-residue --apply
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
now appear in `wb worktree list` and in `gc`'s plan. A detached checkout whose
head is a merged pull request's head is `detached-review` and removable; one
with no landing association is refused and prints whether the commit exists on
origin at all.

## Sizes tell the truth twice

Every size is reported **apparent and unshared**, because pnpm hard-links
`node_modules` into its store: one sweep measured 11.7 GB apparent against 5.9
GB unshared, and only the unshared figure comes back when the tree is deleted.
The reclaim footer counts unshared bytes only. Pass `--skip-sizes` to skip the
measurement entirely.

## Terminal artefacts

Empty `.wb-retired-stage-*` directories and inert `.wb-retired-lock-*` files are
purged unconditionally and silently on any `wb worktree` read path, `gc`
included, and counted in the footer. They are not backlog and not worth a log
line — one workstation had 55 stage directories producing 55 `info:` lines
before every single `wb worktree list`.

## What gc never touches

- **Work Logs and event logs.** They are the evidence base for every WB report
  and are never deleted, compressed away, or pruned.
- **A non-empty retired stage.** That is audited recovery backlog; use
  `wb worktree cleanup <task> --recover-stages`.
- **Anything unpushed.** No flag exists to retire it.

## Lane contract

Consume a library through `wb deps propagate local`; the orchestrator runs
`remote` at the end. End with `wb worktree end`.
