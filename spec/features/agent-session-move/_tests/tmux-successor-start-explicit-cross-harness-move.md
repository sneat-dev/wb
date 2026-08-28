---
format: https://specscore.md/scenario-specification
---

# Scenario: Explicit cross-harness move

**Validates:** [agent-session-move#req:tmux-successor-start](../README.md#req-tmux-successor-start)

## Steps

GIVEN source and target support different harnesses
WHEN the move names the target harness explicitly
THEN a fresh target session starts from the portable handover

## Detected Surface

process

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
