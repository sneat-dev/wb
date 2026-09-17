---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Default orchestrators to wb agent dispatch, not manual worktree lifecycle

**Status:** Draft
**Date:** 2026-09-17
**Owner:** ai
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might we stop orchestrator sessions from hand-rolling worktree create/commit/push/land cycles themselves, so their own transcript doesn't grow into the dominant token cost?

## Context

<!-- Triggering observation, related specs, prior art. -->

## Recommended Direction

<!-- 2–3 paragraphs: what and why, over the alternatives. -->

## Alternatives Considered

<!-- 2–3 directions that lost, and why each lost. -->

## MVP Scope

<!-- The single job the MVP nails. Timeboxed, not feature-listed. -->

## Not Doing (and Why)

- Adding a literal --cd flag to wb worktree create — a subprocess cannot change its parent shell's cwd; the existing --format json worktree_dir/canonical_dir fields plus a one-line cd $(... | jq ...) already solve this
- Streaming subagent output live into the orchestrator's context — reintroduces the exact replay cost this idea removes; wb agent logs already gives on-demand visibility without the standing cost
- Enforcing a hard orchestrator turn cap inside wb itself — worth doing as a follow-up, but process/skill discipline (park/pickup) is the near-term lever

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | placeholder dealbreaker assumption | describe how to validate |
| Should-be-true | … | … |
| Might-be-true | … | … |


## SpecScore Integration

- **New Features this would create:** TBD at design time
- **Existing Features affected:** none
- **Dependencies:** none

## Open Questions

None at this time.
