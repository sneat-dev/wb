---
name: wb-merge
description: Mechanically land one or many compatible completed WB branches/worktrees into the default or explicit target branch, choosing a verified direct-push or pull-request route, waiting for CI, synchronizing the canonical clone, and optionally cleaning up. Use whenever work is ready to merge, integrate, land, finish, deliver, drain, batch, push to main, open/merge a PR, resume an interrupted merge, repair failed post-target CI, or prepare a forward revert—especially for repeated conflict-free AI-agent handoffs where no judgment call is needed.
---

# WB merge

For a clean conflict-free handoff, start with `wb worktree merge`; this is the
default AI-agent landing path and is expected to be used at least once for most
created worktrees, often multiple times for a long-lived target. Use the manual
steps below only for unsupported policy, conflicts, or behavioral judgment.

```sh
wb worktree merge <source-worktree...> --route auto --cleanup --format json
```

## A live local link is a refusal

`wb worktree merge`, `wb worktree merge prepare`, `wb worktree merge land` and
`wb worktree merge resume` all **refuse before any push**
a worktree that holds a live local link — a link recorded in stream state, or a
`go.work` carrying a `use` entry. Such a worktree builds against an
*unpublished* working tree, so landing it would publish a commit whose CI ran
against something the registry never carried.

The two signals are checked independently: state alone would miss a hand-written
`go.work`, and `go.work` alone would miss an npm link. Either one refuses, with
exit code `2`.

The land verbs take a **receipt** rather than a worktree path, so they resolve
the worktrees to guard out of it — preparing before linking and then landing the
receipt used to walk a linked worktree straight past the guard.

The refusal names the exact command that clears it:

```sh
wb deps propagate local <library-worktree> --to <consumer-worktree> --undo
```

**There is no flag that both bypasses this guard and pushes.** Do not hand-roll
`git push` around it.

Inside a stream, agent pull requests target `stream/<name>`, never `main`, and
landing the stream itself is rebase-and-merge. See the `wb-streams` skill.

This is the canonical, harness-neutral merger contract. It is an operational
skill, not a branch-prefix convention and not a model profile. Read
[ci-polling.md](references/ci-polling.md) when CI must be observed and
[adapters.md](references/adapters.md) when installing or migrating a harness.
For conflict-free receipt-backed automation, read
[worktree-merge.md](references/worktree-merge.md).

**When the work is already on GitHub as one pull request and it is ready to
land, use [`wb pr land`](references/pr-land.md) instead** — it verifies the head
and its checks, squashes with an aggregated message, proves the merge reached
the base, deletes the branch, and retires the worktree, all as one verb.
**Never run `gh pr merge` by hand**: that is the measured root cause of sixty
abandoned worktrees, because the cleanup that should follow it never ran.

The dedicated merger agent validates the candidate first. A passing candidate
records that a target baseline was not needed; a failing candidate triggers an
exact target-snapshot validation so unchanged target failures remain diagnostic
rather than blocking a fix. It never waits for current target CI to turn green;
the candidate may fix a red target. The merger owns fetching and
fast-forwarding, integration validation, exact-head CI, the merge and immediate
push, post-merge target CI, release/install evidence, and cleanup. Main and
planning agents hand work to the merger and receive only behavioral, design,
or authority blockers. The invoking harness assigns this mechanical role to a
faster, lower-cost model with adequate repository and CI capability. Every
Work Log creator MUST pass the exact model identifier when the runtime exposes
it, or the literal `unknown` when it does not; never infer or guess a model ID
and never omit `--model`.

## Mechanical integration-only boundary

The dedicated merger is **mechanical integration-only**. It must not author,
change, or repair implementation code, tests, specs, generated artifacts, or
fixtures to satisfy build, test, coverage, lint, spec, or generated gates.
Those failures belong to the originating implementation agent; return gate
repairs to a distinct implementation agent while the merger retains the branch
in its queue. It may modify a conflicted file only to perform a mechanical
merge-conflict resolution that reconciles already-approved branch content and
introduces no new behavior. A conflict requiring a product or design decision,
or any novel code or test change, must be returned to implementation rather
than resolved by the merger.

This boundary does not weaken full-cycle ownership: the merger still fetches
and fast-forwards, queues and batches compatible branches, mechanically
integrates, validates, operates the PR/merge, observes exact-head and
post-merge CI, collects release/install/distribution evidence, and performs
audited cleanup.

Coordination uses one exclusive logical merger lane per
`(repository, target branch)`, independent of the calling session. Main agents
submit manual handoffs; the lane owner batches compatible work, orders
dependencies, resolves mechanical conflicts, and owns the full delivery cycle.
If its runtime dies, another session resumes the same logical lane and Work Log
instead of opening a competing lane. In the founder MVP one agent may own
multiple lanes; at scale, different repository/target lanes may run
concurrently. WB does not yet provide durable merger submit, queue, claim, run,
or status commands, so this skill is the manual-handoff vertical slice rather
than a fictional queue.

1. Inventory every relevant effort from WB claims/list and fetched Git refs:
   use `wb worktree list --github --format json` (and each named task where
   known). Inventory without a branch prefix, agent runtime, or one repository;
   inspect diagnostics and lifecycle artifacts as well as live results.
2. Decide the exact target — `main`, a feature branch, or a task branch — and
   apply that target's definition of done. Group only mutually compatible
   branches. Give overlapping changes one owner or an explicit dependency
   order before integration. Preserve both stated intents; escalate a genuine
   behavior disagreement rather than silently choosing deletion. A branch that
   is not ready remains handed off, never silently skipped or discarded.
3. Use WB-managed worktrees only. Run `wb worktree guard` before writing and
   `wb worktree create` with its required private prompt when a merger checkout
   is needed. Never create, repair, or substitute a checkout with raw Git
   worktree commands.
4. Treat current target failures as a diagnostic baseline, not a green gate.
   WB runs the exact target snapshot only when the candidate itself fails and
   failure-equivalence evidence is needed; a fully passing candidate cannot
   regress an already-red target and records the skipped baseline explicitly.
   Fetch and fast-forward the target from `origin` before preparing the batch.
   The dedicated merger checkout must be clean before every integration and
   push; unrelated dirty state is a blocker, not an exception.
   Integrate the compatible batch through the approved target integration
   route, validate after each merge, then run the full target verification.
   Before candidate CI, prove the candidate head contains the freshly fetched
   exact target SHA and that the target has a nonempty server-enforced strict
   required-status-check policy. If the target advances, rebase or reintegrate
   and obtain a new receipt. Merge-group observation is planned; this
   source-head workflow must fail closed for a merge queue rather than claim
   synthetic-SHA support.
   `wb worktree merge` now composes this conflict-free mechanical path. Its
   prepare receipt remains explicitly local and not landed until Phase 2
   proves the exact remote target.
5. Push whichever ref was integrated immediately after validation. For a
   direct route, push the exact target and verify its remote SHA. For a PR route,
   push the source branch, wait for its exact source-head and PR receipt, then
   merge through the PR with that exact-head guard. **Immediately after a PR
   into `main` reports merged, and before any release/tag advancement,
   installation evidence, cleanup, or other merge-cycle action,**
   resolve that repository's canonical checkout through WB's canonical
   repository layout. Require it to be checked out on local `main` with clean
   staged and unstaged tracked state; reject ordinary untracked paths. The only
   untracked exception is an exact registered nested linked-worktree root:
   first prove `origin/main` tracks no conflicting path there, snapshot that
   nested worktree's branch, `HEAD`, and status, and recheck every snapshot is
   unchanged after canonical synchronization. All other dirty or non-`main`
   states fail closed. Run `git fetch origin`, capture the exact `origin/main`
   SHA, then run
   `git merge --ff-only origin/main` in that canonical checkout. Its `HEAD`
   must equal both that fetched `origin/main` SHA and the PR's exact server
   merge-result SHA before continuing. If the canonical checkout is missing,
   cannot fast-forward, or has a different exact SHA, fail closed and hand the
   condition to its owner. Do not advance a
   release or tag, collect installation evidence, terminalize cleanup, or
   start the next batch before this gate passes. Never reset, switch over
   changes, stash, discard, or otherwise repair the canonical checkout.
   Fetch again, fast-forward the local target to `origin/<target>`, and prove
   that the fetched remote target contains the exact merge SHA. A local merge,
   source push, or merely queued target push is not target receipt. After
   either route, wait for all
   observed CI and required release evidence on the exact remote target SHA.
   Use `wb ci wait --repo <owner/repo> --target <target> --head <exact-sha>
   --json` (for example, `wb ci wait --repo acme/app --target main --head 0123456789012345678901234567890123456789 --json`);
   add `--pr <number-or-url>` only to corroborate a PR head. Pending is
   intermediate state only: execute `resume_args` as structured JSON argv (or
   shell-quote every argument) and rerun until terminal pass or failure. A pass
   records the target's enumerated required-check policy and an unchanged
   terminal reread of all checks observed in that bounded window. For a direct
   target only, an enumerated empty policy plus complete empty check-run and
   status receipts is a terminal no-applicable-check receipt after that same
   reread; never extrapolate it to a PR merge. In every
   mode, an App-pinned required context must be produced by that exact GitHub
   App; a same-named PR summary or legacy status is insufficient. The receipt
   does not prove that no optional workflow can register later, so it never
   replaces the next release-evidence step.
   The strict server policy closes the final target-movement race; if GitHub
   rejects the merge after the local rereads, keep the PR unmerged and
   reintegrate instead of reporting completion.
   If exact post-target CI fails after the remote landing, preserve the receipt,
   candidate, and sources. Return implementation work to its owner. Once the
   same clean source advances additively with a forward repair, rerun
   `wb worktree merge prepare <source>`: WB verifies the prior landing is still
   contained by the fetched target and verifies the prior candidate by graph
   ancestry or exact tree equality with its receipted squash landing. It then
   advances the retained candidate without rewriting published history,
   appends the failed landing to `forward_repairs`, and creates a new PR without
   force-pushing or hand-editing the old receipt.
6. Before collecting installation evidence, read the owning product's
   release/distribution contract. **Never use a distribution channel that the
   owning product marks blocked or unverified** for installation, upgrade, or
   runtime evidence. Where that contract explicitly permits an exact
   source-built artifact, use only that exact source-built artifact instead.
   Otherwise report release evidence blocked and leave the task queued; do not
   substitute another channel or relax the product's gate. A blocked channel can
   return only after the owning product records an explicit verified-status
   change.
7. Collect required release evidence before terminalization. Then, for every
   landed task, inspect `wb worktree cleanup <task>` and apply
   `wb worktree cleanup <task> --apply --remote --older-than 0`. WB seals the
   Work Log, archives lifecycle evidence, removes the exact local worktree and
   branch, and retires an exact unchanged remote source branch. This applies
   unchanged to the source candidates a batch absorbed: when the target rejects
   merge commits and one integration branch lands them all, cleanup reads the
   merged pull request GitHub associates with each candidate's own commit and
   proves containment locally, so batching never converts finished work into
   permanent worktree debt. Add `--absorbed-by <pr|commit>` only when the batch
   cherry-picked rather than merged a candidate, leaving GitHub nothing to
   associate.
8. Re-list the tasks and resolve every live entry or durable cleanup backlog.
   Completion requires remote receipt plus audited cleanup/recycle, not a
   green local test or an apparently finished branch.

Direct pushes are eligible only when WB corroborates the exact remote target;
[`TestCleanupAcceptsExactDirectPushIntegrationWithoutPullRequest`](../../../internal/worktrees/lifecycle_integration_test.go)
is the executable proof, and
[`TestCleanupAcceptsAbsorbedIntegrationBranchSquashReceipt`](../../../internal/worktrees/lifecycle_integration_test.go)
is the same proof for a batch absorbed into one squash landing. Keep adapters thin: they select this contract, not
their own merger workflow.
