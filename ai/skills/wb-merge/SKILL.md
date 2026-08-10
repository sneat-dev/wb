---
name: wb-merge
description: Integrate compatible completed agent branches, prove remote receipt, and terminalize their WB worktrees without leaving branch or worktree debt. Use for a dedicated merger role, release handoff, or draining completed implementation branches.
---

# WB merge

This is the canonical, harness-neutral merger contract. It is an operational
skill, not a branch-prefix convention and not a model profile. Read
[ci-polling.md](references/ci-polling.md) when CI must be observed.

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
4. Fetch and fast-forward the target from `origin` before preparing the batch.
   The dedicated merger checkout must be clean before every integration and
   push; unrelated dirty state is a blocker, not an exception.
   Integrate the compatible batch through the approved target integration
   route, validate after each merge, then run the full target verification.
   WB does not yet expose a generic `merge` subcommand: do not invent one or
   represent an unverified local integration as landed.
5. Push the exact target immediately after validation. Record and verify the
   remote target SHA; a local merge or a merely queued push is not receipt.
   For a PR route, first wait for all observed CI on its exact source head
   before merging, then wait for all observed CI/release evidence on the exact
   pushed target merge SHA. For a direct route, wait for all observed CI on the
   exact target SHA. Use `wb ci wait --repo <owner/repo> --target <target> --head <exact-sha> --json`
   (for example, `wb ci wait --repo acme/app --target main --head 0123456789012345678901234567890123456789 --json`); add
   `--pr <number-or-url>` only to corroborate a PR head. Pending is
   intermediate state only: execute `resume_args` as structured JSON argv (or
   shell-quote every argument) and rerun until terminal pass or failure.
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
