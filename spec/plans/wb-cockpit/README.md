---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: WB Cockpit master plan

**Status:** Draft
**Source:** idea:wb-cockpit
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —

## Summary

Deliver the first slice of [WB Cockpit](../../ideas/wb-cockpit.md): an
operational web UI served by the local daemon that shows this machine's
repositories, worktrees, branches and agents, leads with work at risk of
being lost, and can commit, push, open and land a pull request, and discard
work with a warning first. Three Features, one sub-plan each. The discovery
this plan rests on is in [_research/README.md](_research/README.md).

## Journey

1. **Start.** I run `wb cockpit`. The daemon starts if needed and my browser
   opens. **Observable good result:** the Cockpit Dashboard is on screen,
   signed in as owner, with no prompt.
2. **Middle.** I do nothing but read. **Observable good result:** Work at
   Risk is the first section and names a worktree whose two commits exist
   only on this machine, with the reason; hovering any count names what it
   counts and clicking it opens exactly those rows.
3. **Middle.** I open that worktree and press Push. **Observable good
   result:** the push runs and reports its result in the page; with nothing
   further from me the worktree leaves Work at Risk.
4. **End.** I choose Discard on a worktree that still holds unpushed commits.
   **Observable good result:** before anything is removed Cockpit states what
   exists only there and will not proceed until I type the branch name.

Each sub-plan ends in a whole-journey end-to-end test of its own stage; the
last one, in the actions sub-plan, walks stages 1 to 4 in one run.

## Approach

Three sub-plans in dependency order. The shell comes first because it owns
the mount, the request protection, the owner session and the read model that
everything else is served through. Risk comes second because it is pure
computation over Git state plus one Dashboard section, and it is useful on
its own as soon as it lands. Actions come last because every guarded or
destructive action needs the fresh assessment from the risk sub-plan before
it may run.

Each task in a sub-plan lands as its own pull request on `sneat-dev/wb` main.
Every task keeps the code it adds at 100% statement coverage: Go through `wb coverage --changed`, and `cockpit/web` through a test run that fails below 100% of the application's own source (founder, 2026-10-01).

The two existing dashboards are not touched. `internal/dashboard`, `hub/web`
and `wb dashboard` keep working unchanged while Cockpit grows beside them.

**Not yet planned.** These slices of the idea have no Feature yet and are not
in this plan:

- Herdr dispatch and the agent actions (dispatch, steer, stop, cancel, kill,
  resume). Dispatch today is Codex-only with a detached owner process and no
  herdr, so this replaces the execution owner rather than adding a mode.
- The worktree task reference and the plans-provider adapter with SpecScore
  as the first provider.
- Fleet reach: typed actions on other machines over the peer link, and the
  SSH fallback. The peer WebSocket session answers 501 today, so this waits
  on `peer-connectivity` being built.
- Publishing the application at the hosted URL.
- Retiring the existing dashboards. That is a full cutover and needs every
  consumer of the old surfaces replaced first: the `wb dashboard` command and
  its `--local`, `--metrics` and `--coverage` flags; the `internal/dashboard`
  routes `/`, `/metrics`, `/coverage` and `/api/v1/*`; the five `hub/web`
  pages and the `/workbench/` mount; the hosted pages under
  `sneat.work/bench`; the `hub-web` CI workflow and the release build hook;
  the `wb-daemon` Agent Skill; and the README and specification text that
  names them.

## Tasks

### Task 1: Cockpit shell

**Id:** task-1
**Sub-Plan:** cockpit
**Depends-On:** —
**Status:** planning

The `wb cockpit` command, the embedded Angular application, local request protection, the owner session, capability discovery, and the fleet read model with its pages.

### Task 2: Work-loss risk

**Id:** task-2
**Sub-Plan:** work-loss-risk
**Depends-On:** 1
**Status:** planning

Durability levels and risk reasons for every worktree, branch and canonical clone, the attention list, and the Dashboard section that leads with it.

### Task 3: Cockpit actions

**Id:** task-3
**Sub-Plan:** cockpit-actions
**Depends-On:** 1, 2
**Status:** planning

The action registry, the preview-then-run protocol with a fresh risk assessment, and the first six actions.

## Open Questions

- The later slices listed under Approach each need a Feature before they can
  be planned. Their order is the founder's to set.

---
*This document follows the https://specscore.md/plan-specification*
