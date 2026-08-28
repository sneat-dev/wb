---
format: https://specscore.md/scenario-specification
---

# Scenario: Unpublished work refuses before delivery

**Validates:** [agent-session-move#req:exact-pushed-source-checkpoint](../README.md#req-exact-pushed-source-checkpoint)

## Steps

GIVEN source work is dirty, detached, empty-handed, or cannot be pushed exactly
WHEN a move is requested
THEN delivery is not attempted and the source remains active

## Detected Surface

git

## TODO

- [ ] Pick Rehearse driver
- [ ] Wire up fixtures
- [ ] Implement assertion

---
*This document follows the https://specscore.md/scenario-specification*
