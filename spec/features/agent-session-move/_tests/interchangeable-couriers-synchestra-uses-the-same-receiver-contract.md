---
format: https://specscore.md/scenario-specification
---

# Scenario: Synchestra uses the same receiver contract

**Validates:** [agent-session-move#req:interchangeable-couriers](../README.md#req-interchangeable-couriers)

## Steps

GIVEN an eligible Synchestra runner exposes the fixed WB handler
WHEN a move selects the Synchestra courier
THEN the byte-identical bundle reaches the common receiver and returns the common receipt shape

## Detected Surface

cli

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
