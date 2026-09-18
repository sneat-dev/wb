---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Peer connectivity implementation plan

**Status:** Draft
**Source Feature:** peer-connectivity
**Date:** 2026-09-18
**Owner:** alex
**Supersedes:** —

## Summary

Implement [peer connectivity](../features/peer-connectivity/README.md) as
decided in [decision 0003](../decisions/0003-peer-transport-and-journal.md), in
seven tasks, each of which lands as one reviewed PR on `sneat-dev/wb` main and
leaves WB runnable. A follow-up updates the WB pages on sneat.dev once the
journey works on the real VM and laptop.

## Journey

Stage by stage, each with its observable good result:

1. **Admit.** On the VM, `wb peers invite laptop` prints a token once. On the
   laptop, `wb peers join https://vm1.sneat.dev --token-stdin` restarts the
   daemon.
   *Good result:* both `wb peers list` outputs show the other node
   `connected`, and the laptop's `remote:` block is byte-identical.
2. **Sleep.** The lid closes and nobody does anything. Pushes land on A, C,
   then A.
   *Good result:* the VM lists the laptop `offline` with lag 3 within 40
   seconds of the last pong.
3. **Wake.** The lid opens and nobody types anything.
   *Good result:* within 60 seconds the laptop is `connected`, A and C have
   fast-forwarded (A once), no other clone saw a fetch, and the VM shows
   lag 0.
4. **Live.** A push lands on F while the laptop is connected, with no action
   on the laptop.
   *Good result:* F's event is in the laptop queue within 1 second of the
   webhook's 2xx response, and F fast-forwards.
5. **Observe.** The operator opens the VM dashboard peer page.
   *Good result:* the page shows status, connected-for, version, platform,
   cursor, lag, counters, and two 60-minute charts whose spike lines up with
   the push burst.
6. **Control.** `disconnect`, then a push, then nothing.
   *Good result:* the laptop is back and caught up without anyone acting.
   Then `block`.
   *Good result:* redials are refused with the history kept. Then `unblock`.
   *Good result:* it reconnects, resets from heads, and catches up.

Every stage is asserted by the Task 7 whole-journey e2e, and stages 1 to 4 and
6 are also run by hand on the real VM and laptop in Task 7.

## Approach

The delivery core (durable store, receiver, queue, processor) already exists
and is reused as is. New code is:

- the peer registry;
- the session transport and its client;
- a wake-up signal;
- retention and heads;
- statistics;
- UI.

Tasks 1 to 4 are a strict chain. Task 5 (statistics) depends only on Task 2's
session seam, so it can run in parallel with Tasks 3 and 4 in its own
worktree. Every task:

- keeps the repository coverage floor (`--minimum=88`) and 100% on `hub/`;
- adds `ai/capabilities.json` rows and `docs/cli-flag-matrix.md` lines for
  new command leaves;
- uses injectable clocks and dialers rather than sleeps;
- narrates through `hub/narrate`;
- declares new store collections in `hub/collections.go` so inGitDB accepts
  them.

Implementation agents are local Claude Code subagents on the founder's Mac
(Sonnet), each in its own `wb worktree`. Each PR gets an adversarial review
(Opus) of the diff before `wb worktree land`.

## Tasks

### Task 1: Peer registry, identity, admission and management CLI

**Id:** task-1
**Verifies:** peer-connectivity#ac:identity-and-admission, peer-connectivity#ac:peer-commands
**Depends-On:** —
**Status:** planning

Scope:

- Add the node ID file under the private state directory.
- Add a hub `workbench_peers` store keyed by `MachineID`, created on invite
  and carrying trust, node binding, metadata, `reset_pending` and lifetime
  counters.
- Make `machineBearerResolver` refuse blocked peers.
- Add `wb peers invite|join|list|get|block|unblock|disconnect`. `disconnect`
  answers "no live session" until Task 2.
- Add the `peers.upstream` config in `internal/wbconfig` with the same URL
  rules as `remotestate.ValidateHubURL`, and absolute `token_file`.
- Serve the admin operations through the daemon's owner-authenticated local
  service, reject any request with `Forwarded`/`X-Forwarded-*` headers, and
  apply the same guard to the self-hosted `POST /v0/workbench/machines/enroll`.
- Add read routes `/api/v1/peers[/{id}]` and `/v0/workbench/peers[/{id}]`.

Tests: node ID persistence, token printed once, re-invite rotating while
keeping the record, blocked credential refused on the HTTP long poll too,
proxy-header refusal, and golden text and JSON output.

### Task 2: WebSocket session, heartbeat, reconnect and supersede

**Id:** task-2
**Verifies:** peer-connectivity#ac:session-lifecycle
**Depends-On:** 1
**Status:** planning

Scope:

- Add `github.com/coder/websocket`.
- Add a `peersession` package holding the protocol message types and
  validation (1 MiB read limit, `type` switch, protocol version).
- Hub side: `GET /v0/workbench/peers/connect` with pre-upgrade auth, the
  Origin refusal, the protocol check, and a per-peer live-session registry
  that supersedes by node ID and refuses a node mismatch. `disconnect` and
  `block` close through it.
- Downstream side: a client started by the daemon when `peers.upstream` is
  set, with the hello, welcome and inventory exchange (inventory from the
  local canonical-clone scan, stored as that machine's routing snapshot),
  20-second pings, backoff from 1 to 60 seconds with full jitter, wake
  detection from the wall and monotonic clocks, and terminal close codes that
  stop redialing.
- Narrate lifecycle facts.
- Record in `hub/README.md` "Tunnels" that the proxy must pass
  `/v0/workbench/peers/connect` without basic auth.

Tests use a fake clock, an in-memory `net.Pipe`/`httptest` server and a
fault-injecting dialer.

### Task 3: Journal delivery over the session, live wake-up, retention

**Id:** task-3
**Verifies:** peer-connectivity#ac:journal-resume-without-loss, peer-connectivity#ac:selective-sync
**Depends-On:** 2
**Status:** planning

Scope:

- Hub session loop: when no batch is in flight, on a wake-up or a 30-second
  tick, `Store.Poll(limit 100)` and send `events` envelopes (type from the
  reason). On `ack`, `Store.Acknowledge`, then send `acked`. Close as slow
  after 120 seconds unacknowledged.
- Add a `Notifier` (map from machine ID to a `chan struct{}` of capacity 1)
  that `RepositoryEventService` signals after a successful
  `EnqueueForMachines` commit. The HTTP long poll waits on the same signal
  instead of the 250 ms scan, keeping a floor re-read.
- Downstream side: implement `repositoryevents.Source` over the session, so
  the existing `Receiver` (durable enqueue, pending acknowledgement, cursor
  file) is reused unchanged, with its own cursor path.
- Retention: delete poll receipts at or below an acknowledged cursor; an
  hourly bounded janitor prunes markers older than 14 days and drops queues
  over 5,000 events or older than 30 days (setting `reset_pending`); skip
  blocked peers at enqueue.

Tests cover resume after disconnect mid-batch, hub and downstream restart,
enqueue failure, a duplicate batch, a slow peer not delaying the webhook or a
second peer, and every retention rule.

### Task 4: Latest heads, reset and targeted reconciliation

**Id:** task-4
**Verifies:** peer-connectivity#ac:retention-and-reset, peer-connectivity#ac:selective-sync
**Depends-On:** 3
**Status:** planning

Scope:

- Add a `workbench_repository_heads` record written in the enqueue
  transaction.
- Add the `heads_request`, `heads` (paged, carrying the reset cursor) and
  `reset_ack` exchange, and hold `events` while a reset is pending.
- Downstream comparator: read `refs/remotes/origin/<default>` for each
  inventory clone without fetching, and enqueue a local sync job for each
  difference with an ID derived from repository and target, so it
  deduplicates.
- Add `wb peers reconcile [--dry-run]`.

The selective-sync test uses 20 bare remotes and a git wrapper that records
every `fetch` or `pull` per repository, including a dirty clone left
byte-identical.

### Task 5: Traffic and event statistics

**Id:** task-5
**Verifies:** peer-connectivity#ac:counters-and-rates
**Depends-On:** 2
**Status:** planning

Scope:

- Add a `peerstats` package: session and lifetime counters for RX and TX
  payload bytes, data messages, events and events by type; a 60-bucket
  minute ring with an injectable clock; derived 1m, 5m and 1h throughput and
  last-hour totals.
- Wire it into both session ends through a counting wrapper around
  read/write.
- Persist lifetime totals every minute and at close (hub: peer record;
  downstream: upstream state file).
- Expose it in the detail read routes.

Tests: exact counts for known frames, rollover over 61 minutes, rates after
an idle gap and with less than an hour of history, and lifetime surviving a
restart while buckets do not.

### Task 6: Peers dashboard and status

**Id:** task-6
**Verifies:** peer-connectivity#ac:api-and-dashboard
**Depends-On:** 1, 5
**Status:** planning

Scope:

- `hub/web`: a peers overview page and a detail page (static route with a
  query parameter) with the two inline-SVG charts: RX/TX by minute with
  dynamic units, and events per minute stacked by type with hover and a
  table fallback.
- Loopback-only trust buttons, detected by the API's `admin_available` flag.
  When they are unavailable, the page shows the CLI command instead.
- A compact peers section on the daemon page in `internal/dashboard`, and one
  line per peer in `wb daemon status`.
- An API secret scan across all peer responses in tests.
- Vitest and Playwright fixtures.

### Task 7: Whole-journey e2e and VM rollout

**Id:** task-7
**Verifies:** peer-connectivity#ac:whole-journey-e2e
**Depends-On:** 4, 6
**Status:** planning

Scope:

- Add the in-process two-daemon e2e with signed fake webhooks and bare
  remotes, walking the 16-step scenario with no prodding between steps.
- Roll out on the real hub:
  - upgrade wb on the VM;
  - add the Caddy route for `/v0/workbench/peers/connect` (daemon-authenticated,
    no basic auth);
  - resolve the orphaned-daemon versus systemd conflict on the VM
    (sneat-dev/wb#546) so the hub has one supervised owner.
- Invite the founder's laptop, join, and run journey stages 1 to 4 and 6 by
  hand, recording receipts in the PR.

## Follow-up outside this repository

After Task 7, the `/wb` pages in `sneat-dev/website` gain a plain-language
peer connectivity section. It covers the always-on hub, the laptop waking and
fetching only the repositories that moved, live events while connected,
`wb peers` to see and control connections, and a small diagram with the
invite/join commands. The copy describes only what Task 7 verified on real
machines, and it deploys through the site's existing Cloudflare pipeline.
The founder asked for this on 2026-09-18.

## Risks

- `dalgo2ingitdb` has no range query, so `Poll` reads the whole per-machine
  queue. The 5,000-event bound keeps that cheap, and removing the 250 ms scan
  removes most reads.
- The `inventory` message must produce a snapshot that passes
  `machinesnapshot.Validate` while carrying only repositories. If the
  contract requires fields the peer cannot supply, Task 2 adds a
  repository-only snapshot constructor rather than weakening validation.
- Caddy's default timeouts must keep a 20-second-ping WebSocket open. Task 7
  verifies this on the real edge.
- The VM hub currently runs as a detached process while its systemd unit is
  failed (sneat-dev/wb#546). Until Task 7 fixes that, a VM reboot drops the
  hub.

---
*This document follows the https://specscore.md/plan-specification*
