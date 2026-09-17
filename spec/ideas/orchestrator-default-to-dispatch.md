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

Traced a real token-cost spike (46% of a weekly Claude budget burned in under
24 hours) to three long-lived orchestrator sessions. Each ran 800-900 turns
and cost $79-$294 (confirmed from Claude Code's own `cost-state` accounting,
not an estimate), dominated by cache-read replay: every turn resends the
entire prior transcript, so cost grows worse than linearly with turn count.

Bash tool-result payloads were small (250-411KB total per session) — the
driver is not big command output, it's turn *count*. Breaking down each
session's Bash calls: `wb worktree create` was called manually 15-30 times
per session; `wb agent dispatch` / `wb agent await` — which already exist and
already collapse the create-and-run-worker sequence into one governed call —
were used only 1-3 times total across the three sessions.

`wb pr land` was, by contrast, used correctly and heavily (24-35 calls per
session) — its own doc string already explains it replaces a hand-run
poll/merge/verify/cleanup sequence with one verb. `wb agent dispatch` /
`wb agent await` promise the same collapse for the worktree-creation and
worker-execution side, but orchestrators aren't reaching for it. This looks
like a discovery/habit gap (the skill docs still teach or default to manual
`wb worktree create` + direct git/gh + Task-tool delegation) rather than a
missing capability.

A smaller instance of the same failure shape happened while drafting this
very idea: the authoring agent ran `specscore idea new` directly in the
canonical clone of this repo without first checking `.worktree.md`, in
violation of this repo's own `CLAUDE.md`. Recovery (`wb worktree rescue
--apply --push`, then `--restore`, then a proper `wb worktree create`) cost
several extra Bash turns that a correct first move would not have needed.
This is the same class of problem as the core idea, at a smaller scale: an
agent doing mechanical repo-lifecycle steps by hand, one call at a time,
instead of going through a path structured so the mistake is not available in
the first place. It strengthens the case for defaulting to `wb agent
dispatch`, which always creates its own worktree and never touches the
canonical clone, over any manual sequence — however well the agent is
supposed to know better.

## Recommended Direction

Make `wb agent dispatch --new-worktree <name> --profile <profile> --task
"..."` followed by `wb agent await` the taught default for orchestrators
handing off a bounded implementation task, and treat the manual
create-then-drive-it-yourself pattern as the deliberate exception (e.g. when
a human wants to watch a specific task live), not the default path.

This is a structural fix, not a tuning knob: today, the orchestrator's own
transcript grows with every `wb worktree create`, every commit/push check,
every `wb worktree land` call it runs directly — all replayed on every
subsequent turn. Delegating the full lifecycle to a dispatched agent moves
that work into a separate, disposable transcript. The dispatched agent's own
work still costs tokens, but it is not replayed hundreds of times inside the
orchestrator's growing context.

Do this in two parts: (1) fix the `wb-worktrees` / `wb-agents` skill
documentation so the taught default is dispatch-and-await, with the manual
path documented as an explicit opt-in for live-visibility cases, using
`wb agent logs` for on-demand debugging rather than standing visibility; (2)
audit `wb worktree create`, `wb worktree end`, and `wb pr land`'s
`--format json` output for consistent field names (confirmed `worktree_dir` /
`canonical_dir` on `wb worktree create` and `wb worktree list`; needs the same
check on `end` and `pr land`) so that any orchestrator that does still need to
`cd` into a worktree or back to canonical can do it in one Bash call:
`cd "$(wb worktree create ... --format json | jq -r .worktree_dir)"`.

## Alternatives Considered

- **Add a `--cd` flag to `wb worktree create`/`wb worktree end` that changes
  the caller's working directory.** Rejected: a subprocess cannot mutate its
  parent shell's cwd — this is a hard OS-level limitation, not a missing
  feature. The `--format json` + `jq` one-liner already achieves the same
  practical effect in a single Bash call, so no CLI change is needed there,
  only a documentation/skill fix teaching the pattern.
- **Stream subagent/dispatched-worker output live into the orchestrator's
  context** (to preserve today's visibility). Rejected: this reintroduces the
  exact replay cost the fix is meant to remove. `wb agent logs` already gives
  on-demand access to a dispatched worker's activity when something looks
  wrong, without paying to replay it on every future orchestrator turn.
- **Enforce a hard turn cap on orchestrator sessions inside `wb` itself**
  (e.g. `wb` refuses/warns past N turns). Plausible follow-up, but out of
  scope for this idea: even with perfect delegation, an orchestrator
  coordinating many tasks over hours will still accumulate turns from
  decisions and status traffic, so a cap is a separate, session-lifecycle
  concern (candidate for `park`/`pickup` discipline) rather than a dispatch
  fix.

## MVP Scope

Rewrite the `wb-worktrees` and/or `wb-agents` skill(s) so that a bounded
implementation task's documented, first-shown path is
`wb agent dispatch --new-worktree ... --profile ... --task ...` followed by
`wb agent await`, with the manual `wb worktree create` + direct git/gh path
demoted to an explicitly-named "when you need to watch this live" section.
Confirm (and if needed fix) `--format json` field-name consistency across
`wb worktree create`, `wb worktree end`, and `wb pr land` so the
`cd "$(wb ... --format json | jq -r .worktree_dir)"` pattern is documented as
the standard way to navigate into/out of a worktree from a single Bash call.

## Not Doing (and Why)

- Adding a literal --cd flag to wb worktree create — a subprocess cannot change its parent shell's cwd; the existing --format json worktree_dir/canonical_dir fields plus a one-line cd $(... | jq ...) already solve this
- Streaming subagent output live into the orchestrator's context — reintroduces the exact replay cost this idea removes; wb agent logs already gives on-demand visibility without the standing cost
- Enforcing a hard orchestrator turn cap inside wb itself — worth doing as a follow-up, but process/skill discipline (park/pickup) is the near-term lever

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Delegating the create/implement/land cycle to `wb agent dispatch` materially reduces orchestrator turn count for equivalent work, not just moves the cost elsewhere | Compare orchestrator turn count and total `cost-state` totals for a matched task run the old way vs. via dispatch+await |
| Should-be-true | `wb agent logs` gives enough post-hoc visibility that orchestrators/humans don't need live streaming to catch problems in practice | Run a handful of dispatched tasks and check whether `wb agent logs` was sufficient every time something went wrong |
| Might-be-true | The skill-doc fix alone (no CLI change) is sufficient to shift orchestrator behavior | Re-run the same session-log analysis a few weeks after the skill fix ships and check the manual-create vs. dispatch call ratio |


## SpecScore Integration

- **New Features this would create:** a `wb-worktrees`/`wb-agents` skill-doc
  revision (default-to-dispatch guidance); a small CLI consistency check/fix
  for `--format json` field names on `wb worktree end` and `wb pr land`
- **Existing Features affected:** `wb agent dispatch`, `wb agent await`,
  `wb worktree create`/`end`, `wb pr land`, the `wb-worktrees`/`wb-agents`
  skills
- **Dependencies:** none

## Open Questions

- Should `wb agent dispatch` gain an optional `--follow`/live-tail mode for
  the explicit case where a human wants to watch a dispatched task in real
  time, or is `wb agent logs` sufficient and a live mode not worth the
  complexity?
- Does `wb worktree end`/`wb pr land --format json` already use
  `worktree_dir`/`canonical_dir`, or do they need a fix for consistency with
  `wb worktree create`/`list`?
