---
format: https://specscore.md/decision-specification
status: Draft
---

# Decision: Sneat Dev Desk is the hosted fleet overview; Cockpit stays the one-machine view

**Status:** Draft
**Date:** 2026-11-27
**Owner:** alex
**Tags:** desk,cockpit,bench,hosted,teams,spaces,privacy,open-source
**Source Idea:** —
**Amends:** [0002](0002-bench-open-source-and-self-hosting.md) (hosted dashboard and hosted multi-identity provider only; see Decision 4)
**Supersedes:** —
**Superseded By:** —

## Context

WB has grown three browser surfaces with overlapping jobs:

- **Cockpit** (`cockpit/web`, Angular), served by the daemon at `/cockpit/`. It is the
  operational view: tasks, worktrees, agents, machines, "Needs you". It already shows other
  machines, either live over the hub export route or SSH, or cached from published snapshots.
  The same build can also be hosted (`cockpit.hosted_url`) and read the local daemon over CORS.
- **Bench dashboard** (`hub/web`, Astro), served by the daemon at `/workbench/dashboard/` and
  hosted at sneat.work/bench. It covers the GitHub connection, org and repository pages, stats,
  leaderboards, and latest merges.
- **The hosted hub** (`wb-github-app.sneat.dev`, mounted by the private `sneat-co/sneat-go`
  host on Firestore). It stores machine snapshots keyed by one identity
  (`<login>/<machine>`).

Known at decision time:

- None of these has a notion of a team. Every read resolves to one identity.
- Forge support is GitHub-only, although local clones of other forges are already in the fleet.
- Decision 0002 put every part of bench, hosted or not, in this public repository.
- Sneat's platform already models teams as spaces in `sneat-go-core` (`team`, `company`,
  `personal`), with membership and invitations in shared modules.

The founder wants one product, **Sneat Dev Desk**, that shows the state of a developer's
fleet of repositories across forges and machines, and, in team mode, across an organisation.

## Decision

1. **Desk is the hosted, high-level overview.** The product page is `sneat.dev/desk/` and the
   app is `desk.sneat.dev`. It answers questions about many machines, many forges, and many
   people. `wb` stays the CLI and daemon name.
2. **Cockpit is the one-machine view.** It stays the deep, local, owner-capable surface:
   owner actions, README and content reads, session usage, and copied commands. Its existing
   view of other machines keeps working but gains no new cross-machine features. Each new
   fleet-wide or team-wide view is built in Desk.
3. **The bench dashboard retires.** Its pages (GitHub connection, org and repository pages,
   stats, leaderboards, latest merges) are rebuilt in Desk. Once Desk has parity,
   `sneat.work/bench/dashboard` redirects to `desk.sneat.dev`, and `hub/web` and its embed
   are removed. The hosted Cockpit (`cockpit.hosted_url`) is likewise superseded by Desk.
4. **The code splits by audience: one owner is open source, and the cloud is private.**

   | Open source, in this repository | Private |
   |---|---|
   | `wb` CLI and daemon, including `wb desk` and the daemon's Desk publisher | Desk server, a `sneat-go` module |
   | The Desk wire contract (`api/desk`): enrollment, publish, heartbeat, schema versions | The Desk web app |
   | The self-hosted hub (`wb daemon serve` with `hub:`), single owner, as 0002 specified | Spaces, membership, roles, sharing policy, audit |
   | Cockpit and its published UI and data libraries | Hosted forge connectors (GitHub App, Bitbucket, GitLab) |

   0002 still governs the self-hosted hub. It no longer requires the hosted service or its
   dashboard to live here.
5. **Teams are Sneat spaces.** Every machine belongs to a person and lives in that person's
   personal space. The owner can share it into a `team` or `company` space at a per-space
   level, either when approving the connection or later. Membership, roles, and invitations
   come from the Sneat space modules. External directories such as GitHub organisations or
   enterprises and Entra ID / Active Directory are mirrored into space membership later. The
   space is the source of truth and is never replaced by the directory.
6. **The machine decides what leaves it.** The daemon applies the machine's detail level
   before anything is sent ([desk-connect](../features/desk-connect/README.md)#req:detail-level-applied-on-the-machine).
   The default level, `repositories`, sends machine facts, repository identities, counts, risk
   codes, and pull request states, but no task, branch, stream, or worktree names. `full` is a
   per-machine opt-in and sends at most Cockpit's anonymous-metadata set. At `repositories`,
   task names travel as keyed hashes under a per-person key that Desk never holds. Nothing
   outside that set is ever sent: no paths, file content, commit messages, prompts, or
   owner-only routes. A space can narrow what it shows but can never widen what a machine sent.
7. **The public/private boundary is a versioned wire contract, not a Go module pin.** The
   daemon and the Desk server agree on `api/desk` schema versions. The Desk web app consumes
   Cockpit's libraries as published npm packages with semver ranges. The private server
   doesn't import WB internals.
8. **The connection is outbound only.** A daemon connects with a browser-approved device code
   and publishes over outbound HTTPS. Desk never opens a connection to a machine, so it works
   behind NAT and corporate networks, as the GitHub App protocol already does.

## Rationale

- **Two jobs, two surfaces.** "What is happening on this machine, and let me act on it" needs
  local reach, owner sessions, and content access. "What is the state of everything, and of
  everyone" needs a server that holds every machine's latest state and knows who may see what.
  Cockpit already does the first well. Stretching it into a team product would put tenancy
  into a loopback daemon.
- **Retiring `hub/web` ends a second front-end stack** (Astro beside Angular) that mostly
  duplicates what Cockpit's data layer already derives.
- **Splitting by audience keeps the self-hosted promise of 0002 for single owners.** It also
  gives the team product, which only makes sense hosted, a natural paid boundary (SSO and
  directory sync were the expected first paid features).
- **Sneat spaces already exist and are shared across Sneat products.** Using them means
  membership, invitations, and later directory sync are built once.
- **The privacy guarantee lives on the machine** because a machine can enforce it. A server-side
  filter would mean trusting the server with data the person never meant to send.
- **A wire contract instead of module pins avoids repeating the cross-repository pin churn**
  recorded in 0002 (a full day on 2026-09-10).

## Declined Alternatives

### Desk as a new application, separate from Cockpit's code
This would mean a third UI stack and a second derivation of tasks, risk, and "Needs you" from
the same fleet document, which would drift. Desk reuses Cockpit's published data and UI
libraries instead.

### Cockpit grows into the team product
Tenancy, membership, and per-space sharing would run inside a loopback daemon. Every viewer
would need a reachable daemon, and a team view would depend on the machines being online
rather than on their last published state.

### Everything open source, as 0002 decided for bench
This was declined for the multi-tenant service and team features: there is no external
self-hosting demand for teams, and it removes the paid boundary. The single-owner path stays
fully open.

### Desk-native organisations, or a GitHub organisation as the tenant
Desk-native organisations would duplicate Sneat's spaces. A GitHub organisation as the tenant
would exclude Bitbucket, GitLab, and directory-managed companies, and it would make team
membership depend on one forge's API. Both remain possible as membership sources mirrored into
a space.

### Full detail by default
Task and branch names often carry customer or project names. A person connecting a machine for
the first time should not publish them without choosing to.

### Task hashes keyed by Desk
This is simpler because no key moves between machines. But Desk could recover common task
names by guessing and hashing them, so the hash would hide nothing from the party it is meant
to hide them from.

### Desk pulls from machines through tunnels
Tunnels would need inbound reachability, per-machine tunnel credentials, and a server holding
a live connection to every machine. Outbound publish works everywhere and also gives Desk the
last known state of an offline machine.

## Consequences at Decision Time

Expected positive:

- One answer to "where do I look": Cockpit for this machine, Desk for everything.
- Team mode reuses Sneat spaces, invitations, and later directory sync.
- A machine's owner controls, on the machine, what any space can see.
- Bitbucket and GitLab clones appear in Desk from machine-side state before any server-side
  connector exists.
- The self-hosted single-owner path is unchanged.

Expected negative:

- Bench pages are rebuilt in Angular, and `hub/web` stays in maintenance until Desk has parity.
- The `api/desk` contract becomes a compatibility promise: a published schema version must
  keep decoding across a deprecation window.
- At `repositories` detail, Desk correlates a task across machines only through keyed hashes
  whose key the person carries between their own machines. A machine with another key does not
  correlate, and a space never correlates tasks across people unless they use `full`.
- Two hosted configurations coexist for a while: `remote:` with the `hub` provider, and
  `desk:`.

## Observed Consequences

None observed yet.

## Affected Features

- [Desk Connect](../features/desk-connect/README.md)
- [Remote State](../features/remote-state/README.md)
- [Cockpit](../features/cockpit/README.md)
- [Cockpit Views](../features/cockpit-views/README.md)
- [Self-hosted bench](../features/self-hosted-bench/README.md)

## Open Questions

- Does `remote:` with the `hub` provider converge into `desk:` (one hosted configuration), or
  stay as the transport for repository events and claims? The current lean is to converge:
  `remote.provider: git` stays for people who want no cloud, and repository events move under
  Desk as a later scope with a fresh approval.

---
*This document follows the https://specscore.md/decision-specification*
