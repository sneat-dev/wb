---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Retire the legacy ~/.wb home

**Status:** Draft
**Date:** 2026-11-27
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB stop using the retired $HOME/.wb entirely, so state lives in one place and nothing new lands where no verb manages it?

## Context

Since v0.135.0 (2026-09-16, 3d34dc8b) WB state lives at <projects-root>/.wb and checkouts at <projects-root>/.worktrees. $HOME/.wb survives as a read-only legacy layout, but it is not fully retired: the hub store still defaults to ~/.wb/hub (internal/hubconfig/config.go:30), and README.md still says reports are normally under ~/.wb/reports. On the 2026-11-27 incident host ~/.wb held 10 GB: 3.5 GB of legacy-layout worktrees, about 5.5 GB of hand-made report directories no WB code writes, 607 MB of hand-made backups, and the live hub store. The stale docs are the likely reason agents kept writing there in November. See the disk-prune Feature, which reports this directory as legacy-wb-home.

## Recommended Direction

Move the hub store default to <projects-root>/.wb/hub with a one-time, verified migration that leaves the old store readable until the new one is proven. Fix every doc and help string that names ~/.wb. Make wb clean drain the legacy worktrees through its normal landed-work checks. Once ~/.wb holds only unknown-origin artifacts, report them for a person to move or delete, then stop reading the legacy layout.

## Alternatives Considered

- **Leave ~/.wb as is and only report it.** Lost because the hub keeps writing there and the docs keep sending people there, so it grows again.
- **Delete ~/.wb after the move.** Lost because it holds legacy worktrees that may contain unlanded work, and artifacts WB cannot vouch for.

## MVP Scope

Docs fixed and the hub default moved with a migration that is proven by a round-trip test; the legacy layout stays readable.

## Not Doing (and Why)

- Deleting unknown-origin artifacts automatically — WB cannot prove they are finished
- Dropping the legacy read layout before its worktrees are drained — would hide unlanded work

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | The hub store can be moved without losing events or queues | Round-trip test: migrate, read every collection back, compare |
| Should-be-true | No other tool hard-codes ~/.wb/hub | grep the fleet's repositories and skills |
| Might-be-true | Agents stop writing hand-made reports to ~/.wb once the docs change | Re-measure ~/.wb a month after |


## SpecScore Integration

- **New Features this would create:** TBD at design time
- **Existing Features affected:** none
- **Dependencies:** none

## Open Questions

None at this time.
