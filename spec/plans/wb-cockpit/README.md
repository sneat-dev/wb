---
format: https://specscore.md/plan-specification
status: Approved
---
# Plan: WB Cockpit master plan

**Status:** Approved
**Source:** idea:wb-cockpit
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —

## Summary

Deliver the first slice of [WB Cockpit](../../ideas/wb-cockpit.md): an
operational web UI served by the local daemon that shows this machine's
repositories, worktrees, branches, pull requests and agents, leads with work
at risk of being lost, and can commit, push, open and land a pull request,
discard work with a check first, and refresh the code index. Four Features,
one sub-plan each (the fourth, cockpit views, was added on 2026-10-01). The discovery this plan rests on is in
[_research/README.md](_research/README.md).

## Journey

1. **Start.** I run `wb cockpit`. The daemon starts if needed and my browser
   opens. **Observable good result:** the Cockpit Dashboard is on screen,
   signed in as owner, with no prompt.
2. **Middle.** I do nothing but read. **Observable good result:** Work at
   Risk is the first section and names a worktree whose two commits exist
   only on this machine, with the reason; hovering any count names what it
   counts and clicking it opens exactly those rows.
3. **Middle.** I open that worktree. Discard is disabled and says the two
   commits must be pushed first. I press Push. **Observable good result:**
   the push runs and reports its result in the page; with nothing further
   from me the worktree leaves Work at Risk and Discard becomes available.
4. **End.** I edit a file there, leave it uncommitted, and choose Discard.
   **Observable good result:** before anything is removed Cockpit states that
   the commits are on the remote and that one file exists only here and will
   be archived, and will not proceed until I type the branch name.

Each sub-plan ends in a whole-journey end-to-end test of its own stage. The
last one, in the actions sub-plan, starts from `wb cockpit` and walks stages
1 to 4 in one run.

## Approach

Four sub-plans in dependency order; cockpit views comes after the shell. The shell comes first because it owns
the mount, the request protection, the owner session and the read model that
everything else is served through. Risk comes second because it is
computation over Git state plus one Dashboard section, and it is useful on
its own as soon as it lands. Actions come last because every guarded or
destructive action needs the fresh assessment from the risk sub-plan before
it may run.

Each task in a sub-plan lands as its own pull request on `sneat-dev/wb` main.
Every task keeps the code it adds at 100% coverage: Go through `wb coverage --changed`, and `cockpit/web` through thresholds of 100 for statements, branches, functions and lines plus a rendering test for every component (founder, 2026-10-01).

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
- Source and diff viewers and directory pages, reusing the CodeGrapher web
  UI's viewer components, with CodeGrapher navigation inside them: a symbol's
  definition, callers and callees, and the impact and affected tests of a
  change.
- A Dependencies page: the fleet dependency graph from `wb deps graph` with
  real data, in the three projections — repositories, dependencies, versions
  — that the illustrative section on `sneat.dev/wb` shows today.
- Discarding a worktree that holds commits on no remote, once such commits
  can be preserved first.
- Publishing the application at the hosted URL, and proving in real browsers
  that an https page may read the http loopback daemon.
- Retiring the existing dashboards. (The `internal/dashboard` pages, `/api/v1/overview` and `wb dashboard`
  `--metrics`/`--coverage` were retired on 2026-10-02: cockpit#req:legacy-dashboard-retired.) That is a full cutover and needs every
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

The `wb cockpit` command, the embedded Angular application, local request protection, the owner session, capability discovery, the fleet read model with its pages, and the code-index panel and link.

### Task 2: Work-loss risk

**Id:** task-2
**Sub-Plan:** work-loss-risk
**Depends-On:** 1
**Status:** planning

Durability levels and risk reasons for every worktree, branch and canonical clone, the attention list, the fresh assessment that verifies the remote, and the Dashboard section that leads with it.

### Task 3: Cockpit actions

**Id:** task-3
**Sub-Plan:** cockpit-actions
**Depends-On:** 1, 2
**Status:** planning

The action registry, the preview-then-run protocol with a fresh risk assessment, typed daemon operations, and the first seven actions.

### Task 4: Cockpit views

**Id:** task-4
**Sub-Plan:** cockpit-views
**Depends-On:** 1
**Status:** planning

The Cockpit UX redesign for a dispatcher: fleet read model schema version 2 with pull request state, agent activity, periodic remote publish, machine metrics and throughput; a shell with palette and side panel; the task lifecycle; and Home, Tasks, Repositories, Worktrees, Agents and Machines pages.

## Open Questions

- The later slices listed under Approach each need a Feature before they can
  be planned. Their order is the founder's to set.

---
*This document follows the https://specscore.md/plan-specification*
