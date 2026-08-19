---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Cleanup Orchestration

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

wb cleanup orchestrates worktree, local-branch and remote-branch retirement as one lifecycle.

## Contents

| Child | Description |
|---|---|
| [unpushed-work](unpushed-work/README.md) | wb unpushed finds branches whose commits exist nowhere but this machine. |
| [cleanup-preconditions](cleanup-preconditions/README.md) | Mandatory preservation capture and stacked-pull-request pre-flight before any destructive cleanup step. |
| [fleet-audit](fleet-audit/README.md) | wb audit reports uncommitted work, stashes, worktrees and branches across every repository by evidence class. |
| [pr-recovery](pr-recovery/README.md) | wb recover finds pull requests closed by a deleted base ref and reports whether their content reached the target. |

## Problem

TODO: What problem does this feature solve?

## Behavior

TODO: How does this feature work?

## Acceptance Criteria

TODO: Define acceptance criteria.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
