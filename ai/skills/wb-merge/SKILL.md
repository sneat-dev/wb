---
name: wb-merge
description: Integrate compatible completed agent branches, prove remote receipt, and terminalize their WB worktrees without leaving branch or worktree debt. Use for a dedicated merger role, release handoff, or draining completed implementation branches.
---

# WB merge

This is the canonical, harness-neutral merger contract. It is an operational
skill, not a branch-prefix convention and not a model profile. Read
[ci-polling.md](references/ci-polling.md) when CI must be observed and
[adapters.md](references/adapters.md) when installing or migrating a harness.

The dedicated merger agent captures current `main` and selected-target failures
as baseline diagnostics but never waits for current target CI to turn green;
the candidate may fix a red target. The merger owns fetching and
fast-forwarding, integration validation, exact-head CI, the merge and immediate
push, post-merge target CI, release/install evidence, and cleanup. Main and
planning agents hand work to the merger and receive only behavioral, design,
or authority blockers. The invoking harness assigns this mechanical role to a
faster, lower-cost model with adequate repository and CI capability; the Work
Log records a model ID only when the runtime explicitly exposes it. Omit
`--model` when it is not exposed; never infer or guess a model ID.

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
4. Capture current target failures as a diagnostic baseline, not a green gate.
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
   WB does not yet expose a generic `merge` subcommand: do not invent one or
   represent an unverified local integration as landed.
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
   terminal reread of all checks observed in that bounded window. In every
   mode, an App-pinned required context must be produced by that exact GitHub
   App; a same-named PR summary or legacy status is insufficient. The receipt
   does not prove that no optional workflow can register later, so it never
   replaces the next release-evidence step.
   The strict server policy closes the final target-movement race; if GitHub
   rejects the merge after the local rereads, keep the PR unmerged and
   reintegrate instead of reporting completion.
6. Collect required release evidence before terminalization. Then, for every
   landed task, inspect `wb worktree cleanup <task>` and apply
   `wb worktree cleanup <task> --apply --remote --older-than 0`. WB seals the
   Work Log, archives lifecycle evidence, removes the exact local worktree and
   branch, and retires an exact unchanged remote source branch.
7. Re-list the tasks and resolve every live entry or durable cleanup backlog.
   Completion requires remote receipt plus audited cleanup/recycle, not a
   green local test or an apparently finished branch.

Direct pushes are eligible only when WB corroborates the exact remote target;
[`TestCleanupAcceptsExactDirectPushIntegrationWithoutPullRequest`](../../../internal/worktrees/lifecycle_integration_test.go)
is the executable proof. Keep adapters thin: they select this contract, not
their own merger workflow.
