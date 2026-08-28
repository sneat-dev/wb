---
format: https://specscore.md/scenario-specification
---

# Scenario: SSH move starts same-harness successor

**Validates:** [agent-session-move#req:session-target-configuration](../README.md#req-session-target-configuration)

## Steps

GIVEN a clean pushed branch and an SSH target configured as `hetzner-vm1`
WHEN a registered agent moves the session through SSH
THEN one same-harness successor starts in tmux at the exact pushed commit

## Detected Surface

cli

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
