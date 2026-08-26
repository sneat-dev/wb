---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Park and Resume Agent Sessions

**Status:** Draft
**Source Feature:** park-and-resume-agent-sessions
**Date:** 2026-08-26
**Owner:** codex
**Supersedes:** —

## Summary

Implement phase 1 session-level parking and local resume over the existing WB
session registry and worktree inventory, with a fail-closed remote resume gate.

## Approach

First add append-only parked aggregate storage and lifecycle projection, then
wire CLI commands that inventory every owned worktree without mutating Git and
resume one immutable successor. Focused tests cover dirty preservation,
multiple worktrees, exact remote refusal, lineage, and identical retry.

## Tasks

### Task 1: Durable parked aggregate and lifecycle projection

**Id:** task-1
**Verifies:** park-and-resume-agent-sessions#ac:park-preserves-whole-bundle
**Depends-On:** —
**Status:** planning

Store immutable bundle and append-only lifecycle events. Keep the original PID
registration unchanged while parked sessions resolve as non-live.

### Task 2: Park/resume CLI and reconstructability gate

**Id:** task-2
**Verifies:** park-and-resume-agent-sessions#ac:resume-is-one-successor, park-and-resume-agent-sessions#ac:remote-dirty-refusal
**Depends-On:** 1
**Status:** planning

Add `wb session park` and `wb session resume` with bounded context, all owned
worktrees, local successor lineage, and exact pushed-commit checks before the
existing SSH/session-move boundary is considered.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/plan-specification*
