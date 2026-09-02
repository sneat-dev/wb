---
format: https://specscore.md/idea-specification
status: Specified
---

# Idea: Mechanical worktree merge

**Status:** Specified
**Date:** 2026-08-27
**Owner:** alex
**Promotes To:** mechanical-worktree-merge
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB prepare, validate, land, recover, and clean conflict-free worktree changes without spending AI tokens on mechanical Git and GitHub operations?

## Context

WB already inventories managed worktrees, records Work Logs, waits for exact-head CI, and verifies cleanup receipts, but it has no command that composes those primitives into a resumable merge journey.

## Recommended Direction

Provide a two-phase worktree merge state machine: prepare a dedicated local integration candidate, then land it through a verified direct or pull-request route, with a one-command composition and receipt-driven recovery.

## Alternatives Considered

- Keep using a merger agent for every landing. This preserves judgment but
  spends model time on fetch, merge, push, CI polling, PR creation, canonical
  synchronization, and cleanup even when every decision is mechanical.
- Always require a pull request. This is simpler but adds ceremony where the
  repository explicitly permits reviewed direct landing, and it does not solve
  the local-candidate handoff needed to unblock dependent agents before CI.
- Merge directly in the canonical target checkout. This makes local `main`
  vulnerable to an interrupted `awaiting_push` state and prevents the canonical
  clone from remaining a clean synchronization surface.

## MVP Scope

One repository and one target per invocation; prepare and land phases; auto, direct, and pull-request delivery routes; exact-SHA receipts; foreground CI waiting; optional cleanup; resume and revert preparation.

## Not Doing (and Why)

- Resolving merge or revert conflicts — those require human or AI judgment
- Merge-queue support — WB does not yet observe authoritative merge-group SHAs
- Force-pushing or resetting a remote target — restoration uses forward revert commits

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A dedicated integration worktree can preserve every source and target commit while leaving the canonical clone and source worktrees untouched. | Real-Git journey test with multiple linked worktrees and a bare remote. |
| Must-be-true | GitHub policy can be interpreted without treating administrator bypass as permission for direct landing. | Adapter fixtures for required-PR, allowed-direct, unknown, and merge-queue rules. |
| Should-be-true | One persisted candidate receipt can resume safely after interruption at every remote boundary. | Kill-and-resume tests after prepare, push, PR creation, CI pass, merge, and canonical sync. |
| Might-be-true | Direct landing is cheaper than a PR for repositories without required remote checks. | Measure command duration and hosted check runs by route after release. |


## SpecScore Integration

- **New Features this would create:** `mechanical-worktree-merge`
- **Existing Features affected:** `work-log`, `worktree-lifecycle`,
  `cleanup-orchestration`
- **Dependencies:** exact-head CI receipts, Work Log claims, lifecycle cleanup

## Open Questions

None at this time.
