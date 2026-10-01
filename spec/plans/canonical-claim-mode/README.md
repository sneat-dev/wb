---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Canonical Claim Mode

**Status:** Draft
**Source Feature:** canonical-claim-admission
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —

## Summary

Implement [canonical-claim-admission](../../features/canonical-claim-admission/README.md)
and [canonical-claim-landing](../../features/canonical-claim-landing/README.md):
a Work Log claim with `mode: canonical` that admits the canonical clone for
feature-branch writes under a lease, a single entry call
(`wb worktree create --canonical`), and a landing path that restores the clone
to the base branch and can never remove it.

## Journey

An agent has a ten-line fix. It runs one command,
`wb worktree create fix-typo acme/app --canonical --agent a1 --agent-runtime codex --model unknown --original-prompt-file -`,
and WB checks the clone is clean on `main`, fetches, cuts `fix-typo` from the
fresh `origin/main`, checks it out in the canonical directory, records the
claim with a two-hour lease, flips `.worktree.md` to writable, and prints the
path. The agent edits, commits with explicit paths, and runs `wb pr create`.
After the pull request lands, WB itself checks out `main`, fast-forwards,
deletes the local branch, seals the claim and restores the strict guard; the
agent makes no git call at any point. If the agent crashes, the lease lapses
and the strict guard returns by itself; `wb worktree rescue` saves the leftover.

## Approach

The claim fields and a clock-injected "live canonical claim" lookup come first
because every layer consumes them. Guard, agent guard, marker, entry verb,
rescue, the never-removable predicate and the sync/fleet readers then depend
only on that lookup and parallelise. Landing restore needs the lookup and the
predicate. Skills, docs and the end-to-end test close the plan. Every task
keeps the code it adds at 100% statement coverage and must not lower the
per-package ratchet (`wb coverage --changed`); every new public command leaf or
flag gets an `ai/capabilities.json` row and a `docs/cli-flag-matrix.md` line in
the same task that adds it. Design contradictions found while planning are in
Open Questions.

## Tasks

### Task 1: Claim mode, lease fields, and the live-claim lookup

**Id:** task-1
**Verifies:** canonical-claim-admission#ac:unreadable-claim-fails-closed
**Depends-On:** —
**Status:** planning

In `internal/worktrees/worklog.go` add `mode` and
`lease_expires_at` claim fields, cover `mode` in `expectedWorkLogClaimID`, add
lease-extension evidence events, and add one exported, clock-injected lookup
(live, lapsed, sealed, none) built on `activeWorkLogClaim`. It fails closed on
any read, parse, repository, or corroboration error.

### Task 2: Guard and hook admission

**Id:** task-2
**Verifies:** canonical-claim-admission#ac:live-claim-admits-feature-branch-writes, canonical-claim-admission#ac:base-branch-and-detached-head-stay-refused
**Depends-On:** 1
**Status:** planning

In `worktrees.Guard` (`internal/worktrees/worktrees.go`, the canonical branch
before the `branch != base` and clean-tree refusals) admit a canonical clone
when the lookup reports a live claim and HEAD is the claim branch; report
`Kind: "canonical"` with task and lease. Refuse base, other branches and
detached HEAD naming the claim branch. Prove `post-checkout`, `pre-commit`,
`pre-push` (`builtin:worktree-guard`) need no hook change, with real-Git tests.

### Task 3: Agent guard admission

**Id:** task-3
**Verifies:** canonical-claim-admission#ac:live-claim-admits-feature-branch-writes, canonical-claim-admission#ac:unreadable-claim-fails-closed
**Depends-On:** 1
**Status:** planning

Add an admission-check field to `agentguard.Options`
(`internal/agentguard/guard.go`) consulted at the `KindCanonical` refusals in
`guard.go`, `bash.go`, and `git.go`, supplied from `cmd/wb/hooks_agent.go`.
Keep `internal/agentguard` free of Work Log imports; any check failure refuses.
Keep the `managedGitLocation` hook-bypass refusal in force.

### Task 4: Marker reflects the claim

**Id:** task-4
**Verifies:** canonical-claim-admission#ac:lapsed-lease-resumes-strict-guard
**Depends-On:** 1
**Status:** planning

In `internal/checkoutmarker` and `cmd/wb/worktree_marker.go` render
`kind: canonical`, `writable: true`, branch, task, expiry and the
explicit-path/landing body while a claim is live, and `writable: false`
otherwise. Rewrite on create, renew, land, release, and `wb sync`. Test that a
stale marker never admits (Guard reads the claim, not the marker).

### Task 5: `wb worktree create --canonical`

**Id:** task-5
**Verifies:** canonical-claim-admission#ac:entry-refuses-unsafe-clones-without-touching-them, canonical-claim-admission#ac:second-task-is-pointed-at-worktree-create, canonical-claim-admission#ac:lapsed-lease-resumes-strict-guard, canonical-claim-admission#ac:live-claim-admits-feature-branch-writes
**Depends-On:** 1, 2, 4
**Status:** planning

Add `--canonical` and `--lease` to the shared create constructor
(`cmd/wb/worktree.go`; the `wb create` alias inherits them). Preflight clean,
on base, not detached, not mid-operation, no other active canonical claim; use
`fetchOriginBranchToPrivateRef`, create and check out the branch in the clone
(WB, never the agent), record the claim with lease, write the marker, print the
path. Idempotent renewal capped at 8 hours; refusals leave the clone unchanged
and name `wb worktree rescue` / `wb worktree create <task> <owner/repository>`.
Add the capabilities row and flag-matrix line. Plain create must stay untouched.

### Task 6: Rescue accepts a claimed feature-branch clone

**Id:** task-6
**Verifies:** canonical-claim-admission#ac:crash-leftover-is-recoverable
**Depends-On:** 1
**Status:** planning

Teach `internal/canonicalrescue` and `cmd/wb/worktree_rescue.go` to rescue a
canonical clone on the claim's feature branch (staged, unstaged, untracked)
without touching the claim's commits and without discarding by default.

### Task 7: Skills, docs, help, capabilities, stale create wording

**Id:** task-7
**Verifies:** canonical-claim-admission#ac:live-claim-admits-feature-branch-writes
**Depends-On:** 5
**Status:** planning

Rewrite every statement that `wb worktree create` never touches the canonical
clone (the `Guard` refusal text in `internal/worktrees/worktrees.go`,
`wb worktree create --help`, and the skills) so it says plain create still
never does, while `--canonical` is the sanctioned exception. Update
`skills/wb-worktrees`, `skills/wb`, `skills/wb-merge`, `skills/wb-fleet` and the generated `ai/capabilities.json` / `skills/commands.json`, help text,
and `docs/`: when to choose canonical mode, never `git checkout -b`/`git switch
-c`, explicit-path commits for subagents, one canonical claim per repository,
rescue for crashes.

### Task 8: End-to-end admission test

**Id:** task-8
**Verifies:** canonical-claim-admission#ac:live-claim-admits-feature-branch-writes
**Depends-On:** 5, 6
**Status:** planning

One real-remote test: create, edit, explicit-path commit, push, then the crash
variant (lease lapse, strict guard resumes, rescue). Confirm plain
`wb worktree create` beside a live claim is unaffected. Landing is covered by
the companion plan.

## Open Questions

None at this time. Confirmed or decided by the founder: verb shape and lease values; the stale "create never touches canonical" wording is rewritten in task-7. Plan split: one plan per source Feature is what `specscore spec lint` (P-002) accepts; the companion plan [canonical-claim-landing](../canonical-claim-landing/README.md) depends on tasks 1 and 4 here, and its journey test on task 5.

---
*This document follows the https://specscore.md/plan-specification*
