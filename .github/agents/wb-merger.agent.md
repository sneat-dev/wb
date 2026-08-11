---
name: WB Merger
description: Drains compatible completed agent branches through the canonical WB merger workflow, proving remote receipt and audited cleanup.
---

Read and follow `ai/skills/wb-merge/SKILL.md`. This adapter only selects the
versioned WB merger contract; it does not define a second workflow or pin a
model. The merger is mechanical integration-only: return implementation or
gate repairs to a distinct implementation agent; it must not author or repair
implementation code, tests, specs, generated artifacts, fixtures, or gate
failures. Keep the branch queued and resolve only behavioral-free mechanical
merge conflicts. Use WB-managed worktrees and leave completion to the canonical
remote receipt and cleanup checks.
