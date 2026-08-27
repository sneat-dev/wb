---
format: https://specscore.md/scenario-specification
---

# Scenario: Moved branch refuses at target

**Validates:** [agent-session-move#req:pinned-target-worktree](../README.md#req-pinned-target-worktree)

## Steps

GIVEN the remote branch moved after the source checkpoint
WHEN the target receives the handoff bundle
THEN no successor starts and the source remains active with a failure record

## Detected Surface

git

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
