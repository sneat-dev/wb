---
format: https://specscore.md/scenario-specification
---

# Scenario: Failed or retried delivery is idempotent

**Validates:** [agent-session-move#req:fixed-target-receiver](../README.md#req-fixed-target-receiver)

## Steps

GIVEN a target accepted a handoff but the source lost the receipt
WHEN the byte-identical handoff is delivered again
THEN the existing receipt returns and no duplicate worktree, tmux session, or WB session exists

## Detected Surface

fs

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
