---
format: https://specscore.md/scenario-specification
---

# Scenario: Successor can be messaged and asked back

**Validates:** [agent-session-move#req:successor-messaging](../README.md#req-successor-messaging)

## Steps

GIVEN a completed handoff with a live tmux successor
WHEN the predecessor sends a message and requests handoff back
THEN both typed messages are recorded, safely delivered, and acknowledged with lineage intact

## Detected Surface

event

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
