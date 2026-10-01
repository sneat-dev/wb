---
format: https://specscore.md/idea-specification
status: Draft
---
# Idea: WB Cockpit — the operational control plane for an AI-assisted development fleet

**Status:** Draft
**Date:** 2026-10-01
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:daemon-as-coordinator

## Problem Statement

How might we give a developer one place to see what is happening across every
machine, repository, worktree and agent in their fleet, know that no work is
silently left behind, and take the obvious next action without leaving it?

## Context

WB has two web surfaces today, and neither is operational:

- `internal/dashboard` — pure-Go embedded HTML on the loopback daemon
  (`http://127.0.0.1:8766`, `/metrics`, `/coverage`). Read-only.
- `hub/web` — an Astro site embedded in the binary at `/workbench/` and hosted
  at `https://sneat.work/bench/dashboard`. Read-only apart from the peers admin
  buttons specified in `peer-connectivity`.

The founder's engineering prompt for WB Cockpit (2026-10-01) asks for an
operational UI: select, inspect, act. It was compared against the specs on
`origin/main` at d6614365. The comparison was against specs, not against the
code. It found three contradictions and several overlaps, and the founder
answered each one in discussion on 2026-10-01. Those answers are recorded
under [Decisions](#decisions) so they can be reviewed; they are inputs to the
Feature specs, not a substitute for them.

Central promise of the prompt:

> **No work gets silently left behind.**

Design principles the prompt asks to record explicitly:

1. Fleet first, machine drill-down.
2. No work silently left behind.
3. Operational truth works without a planning system.
4. Planning integrations add intent and context, not dependency.
5. Summary → hover detail → click drill-down.
6. Every aggregate exposes its underlying entities.
7. Select → inspect → act.
8. Transport is transparent to product features.
9. Cached state is visibly stale and timestamped.
10. Permission and safety are separate.
11. Actions are extensible; the MVP still ships the obvious actions.
12. Self-hostable in full; a hosted page exists for a first look.
13. Reuse existing WB capabilities before adding parallel systems.

Principle 12 is narrower than the prompt's "hosted by default"; see the
*Hosted reach* and *Default open* decisions.

## Decisions

Each row is a founder answer from the 2026-10-01 discussion unless marked
*proposed*, which means the agent proposed it and the founder replied "Looks
good" to the whole set without discussing that row on its own.

| Topic | Decision |
|---|---|
| Local auth | Listing repositories, agents, worktrees and machines needs no login for a genuinely local request. File content and every mutation need the owner-credential session. |
| Local request check | *Proposed.* "Genuinely local" means the daemon verifies a loopback `Host` header and sends no permissive CORS headers. |
| Hosted reach | The hosted page may read metadata only from a local daemon, from exactly the configured Cockpit origin. File content and actions happen in the daemon-served Cockpit. |
| Existing UIs | Cockpit replaces both `hub/web` and `internal/dashboard`. |
| Bench scope | Every existing page and every spec'd view is ported before the old UI is removed. |
| Hosted home | `https://sneat.dev/wb/cockpit/`, configurable in one place; `sneat.work/bench/*` redirects. The founder leaned this way and the agent agreed. |
| Topology | The peer link and hub snapshots are the primary path for remote state and typed actions. SSH from the local daemon is a fallback adapter for a machine with WB installed and no peer link. Cached snapshots cover offline machines. |
| Cloud data | No cloud transit by default. Publishing allowlisted snapshots to the hosted hub is an explicit opt-in; the snapshot privacy allowlist is unchanged. |
| Frontend | Angular with Material/CDK. No React. An open-source chart library is still to be chosen. |
| Permissions | Every action declares a required capability from the first slice, and Cockpit discovers effective permissions. The MVP has three fixed principals: anonymous-local (metadata read), owner session (everything), peer (typed remote operations). Per-user grants and non-loopback binding wait for the OAuth2/OIDC feature in decision 0002. |
| Unit of work | No new Goal entity and no task storage in WB. A worktree may carry one task reference: a URI that points at a task in an external plan. Its scheme selects the plans provider, for example `specscore://github.com/sneat-dev/wb/spec/plans/cockpit`; a plain `https://` URI is a link-only reference with no provider behind it. A task can be a directory or a document, and a task can itself be a plan: the top-level task is the plan, and its worktree links to the plan. A fragment is optional and names a task embedded in a document. The hierarchy and any not-yet-started tasks live in the plan, not in WB. The reference is recorded in the Work Log creation manifest. WB accepts any valid URI and does not care about its scheme. It groups worktrees by the URI's own structure: a reference is an ancestor of another when its path is a path-prefix of it, and a fragment makes a child of its document. A provider adds what the URI alone cannot show, such as titles, status and tasks nobody has started. Without a reference a task is the worktree's name, as today. Plans are read through a plans-provider adapter; SpecScore is the first provider and ships out of the box. Streams stay orthogonal. This replaces an earlier answer in the same discussion that WB would own a native task hierarchy. |
| Dispatch | Every dispatched agent runs in a herdr pane. The detached headless mode is removed from `agent-dispatch`. |
| Steer | Target: queued by default, with an explicit "send now" that interrupts. MVP: the simplest safe form — queue only, delivered when herdr reports the agent idle, blocked or done. If that status is not reliable enough to gate delivery, the MVP falls back to record-only through the existing read verb. |
| Stop / Cancel / Kill | *Proposed.* Stop queues a wrap-up instruction through the Steer path. Cancel marks the task's assignment cancelled so no successor picks it up, then stops the agent. Kill terminates the pane's process tree after a work-loss check. None of the three touches files; discarding a worktree is a separate action. |
| Command | `wb cockpit` starts or reuses the daemon and opens its own Cockpit with an owner session. `wb cockpit --hosted` opens the hosted page. `wb dashboard` becomes an alias. |

## Recommended Direction

Build Cockpit as one Angular application served two ways: embedded in the wb
binary and served by the daemon, and published at the hosted home. The
daemon-served copy is the full operational surface. The hosted copy is the
same application with a narrower reach: it can list what a local daemon
exposes without a login, and it shows an opted-in cross-machine view from hub
snapshots. Anything that reads a file or changes state opens the daemon-served
copy, where the existing owner-credential rule
(`peer-connectivity#req:admin-requires-owner-credential`) applies unchanged.

Keep one aggregation model. Remote machines are reached through the peer
session and hub snapshots that `peer-connectivity` and `remote-state` already
define, with an SSH adapter for machines that are not peers, reusing the
courier `wb session move --via ssh` already has. Reads and actions go through
one abstraction that names its route (`local`, `peer`, `SSH via local WB`,
`cached`) so the UI can show it and never present cached state as live.

Make work-loss risk the organising signal. Cockpit computes a durability level
per worktree and branch (local changes → local commits → remote branch → pull
request → merged), combines it with staleness and machine reachability, and
leads the Dashboard with what needs attention. Actions come from a registry in
which each action declares its target types, preconditions, required
capability and safety class, so the UI discovers what applies rather than
hardcoding it, and destructive actions always show what unique work would be
lost first.

## Alternatives Considered

- **Hosted page with full control after pairing.** One UI everywhere, with a
  bearer token obtained from a one-time code. It lost because a compromise of
  the hosted origin could then discard, push and kill on the operator's
  machines.
- **Local gateway that pulls every machine live, as the prompt describes.** It
  works with no always-on hub. It lost because it builds a second aggregation
  path beside `peer-connectivity` and makes the laptop the single point for
  fleet views.
- **A new Goal entity above tasks.** More expressive for multi-task efforts.
  It lost because SpecScore's task and plan hierarchy already covers the need,
  and a second unit of work would have to be stored, synced and explained.
- **A native parent/child task hierarchy stored by WB.** It would show planned
  work with no planning system present. It lost because a parent task has no
  worktree, Work Log or claim to live in, so WB would need task storage and
  cross-machine sync of its own; a reference to an external plan needs neither.
- **Keeping headless dispatch beside herdr.** It avoids a herdr prerequisite on
  every machine. It lost to having one dispatch path and one run lifecycle.
- **Porting only the operational pages.** A smaller MVP. It lost because the
  founder wants a full cutover with no page left behind on the old UI.
- **PrimeNG, or Vue.** PrimeNG is liked but raises a contributor-licence
  question for an open-source repository; Vue shares nothing with the rest of
  the Sneat code. Both lost to Angular with Material/CDK.

## MVP Scope

A developer runs `wb cockpit` on one machine and, in the daemon-served Cockpit,
sees every repository, worktree, branch and agent on that machine with a
work-at-risk list on top, drills from any count to the entities behind it, and
can commit, push or discard a worktree and land a pull request with a
work-loss warning before anything destructive. Other machines appear through
existing peer snapshots, labelled with their route and age.

The old UIs stay in place until the port is complete; removing them is the
last slice, not the first.

## Not Doing (and Why)

- Per-user grants, roles and a permission management UI — wait for the OAuth2/OIDC feature; only capability names and three fixed principals ship first.
- Binding the daemon beyond loopback — decision 0002 gates it on OAuth2/OIDC.
- "Send now" interrupting steer — the MVP ships the queued form only.
- Daemon-originated text submitted into a pane — stays record-only as `herdr-session-transport` specifies; only operator-initiated steering is reopened.
- General infrastructure monitoring — machine telemetry is limited to reachability, CPU, memory, disk and WB version.
- Token and cost metrics beyond what the ported views already show — the prompt defers them until the data exists.
- Multi-segment worktree names such as `goal/task/subtask` — not yet; pending a brainstorming session (founder, 2026-10-01). Claims refuse a slash today, and the name is also a branch and a directory level of the worktree store.
- A raw command endpoint — remote and browser actions stay typed and allow-listed, as `agent-sdlc-throughput` requires.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | Herdr's agent status (idle / working / blocked / done) is reliable enough to gate queued steer delivery. | Drive a scripted agent through each state and compare `agent get` with the pane; any delivery into a working pane fails the test. |
| Must-be-true | Herdr can be installed and checked by `wb setup` on every machine that receives dispatched work, including SSH-only VMs. | Run `wb setup --check` on the laptop and the Hetzner VM and dispatch one task to each. |
| Must-be-true | A loopback `Host` check with no CORS allowance, plus one exact allowed origin, keeps unauthenticated metadata unreadable by other websites. | Browser tests from a foreign origin, a DNS-rebinding fixture and a forwarded-header proxy fixture, all expecting refusal. |
| Should-be-true | Angular Material/CDK tables and overlays are dense enough for the repository and worktree grids without a commercial grid. | Build the repository table with hover cards against a 200-repository fixture. |
| Should-be-true | Durability can be computed for a whole fleet from the existing fingerprinted index without a fresh scan per page load. | Measure against the current local index on the founder's projects root. |
| Might-be-true | Peer snapshots carry enough to show work at risk on an unreachable machine without widening the privacy allowlist. | Compare the hosted snapshot schema with the fields the risk view needs. |

## SpecScore Integration

- **New Features this would create:** TBD at spec time. Likely: the Cockpit
  shell and command; work-loss risk and durability; the action registry and
  safety classes; capability-based permissions; worktree task reference; herdr
  dispatch and agent controls; SSH fallback adapter; planning-reference
  integration.
- **Existing Features affected:**
  - `agent-sdlc-throughput` — "the loopback HTTP dashboard remains read-only"
    and the `https://sneat.work/bench/dashboard` surface.
  - `fleet-quality` and the `fleet-metrics-web` plan — the pure-Go embedded
    HTML decision and the `/metrics` and `/coverage` pages.
  - `peer-connectivity` — `peers-dashboard` ("inline SVG, `global.css` tokens,
    no chart library") and the `wb dashboard --admin` command name. Its
    owner-credential rule stands.
  - `self-hosted-bench` — the Astro `hub/web` build and the `/workbench/`
    mount.
  - `agent-dispatch` — the detached-execution-owner requirement is removed in
    favour of herdr.
  - `herdr-session-transport` — the steering deferral narrows to
    daemon-originated events.
  - `machine-setup` — herdr becomes a checked prerequisite.
  - `remote-state` — hosted snapshot publishing becomes opt-in.
- **Dependencies:** decision 0002 (OAuth2/OIDC before non-loopback exposure);
  decision 0003 and `peer-connectivity`, both Draft, which the topology
  decision builds on.

## Open Questions

- The task reference lives in the Work Log creation manifest, which is inside
  the worktree. What carries it to other machines and keeps it after cleanup —
  the claim, the published snapshot, or neither — was not confirmed.
- May the task reference enter an opt-in cloud snapshot? A plan URL names a
  repository and a plan, and the snapshot allowlist currently excludes
  anything of that kind.
- SpecScore plan tasks are believed to have stable references a fragment can
  name (founder, 2026-10-01); the exact form was not confirmed.
- Who defines the `specscore://host/org/repo/path` form? It should be
  SpecScore's own canonical reference that WB consumes, not a format WB
  invents. Whether SpecScore already has one was not checked.
- How do Stop, Cancel and Kill map onto herdr's actual pane controls? No
  existing semantics were found in `agent-dispatch` or
  `herdr-session-transport`, by keyword search rather than a full read.
- Which chart library? It must be open source and contributor-friendly.
- What replaces the public repository, organisation and leaderboard pages'
  dependence on Firebase sign-in once they are served from the hosted Cockpit?
- Does the free-relay README attribution link in `agent-sdlc-throughput` move
  from `sneat.work/bench` to the new hosted home?
- How complete is the existing `wb agent dispatch` implementation, and how
  much of its run record survives the move to herdr?

---
*This document follows the https://specscore.md/idea-specification*
