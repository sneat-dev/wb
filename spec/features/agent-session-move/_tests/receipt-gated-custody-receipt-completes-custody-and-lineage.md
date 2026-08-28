---
format: https://specscore.md/scenario-specification
---

# Scenario: Receipt completes custody and lineage

**Validates:** [agent-session-move#req:receipt-gated-custody](../README.md#req-receipt-gated-custody)

## Steps

GIVEN a target successor has registered successfully
WHEN its durable receipt reaches the source
THEN lineage and linked Work Log events are recorded before predecessor custody is sealed

## Detected Surface

event

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
