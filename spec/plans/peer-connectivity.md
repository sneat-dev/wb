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
eight tasks, each of which lands as one reviewed PR on `sneat-dev/wb` main and
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
   *Good result:* the VM lists the laptop `offline` within 40 seconds of the
   last pong, with lag 2 (A's two pushes are one piece of sync work).
3. **Wake.** The lid opens and nobody types anything.
   *Good result:* within 60 seconds the laptop is `connected`, A and C have
   been fetched (A once) and fast-forwarded where clean, no other clone saw a
   fetch, and the VM shows lag 0.
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
   Then **Block**, from the dashboard in an admin session or from the CLI.
   *Good result:* redials are refused with the history kept. Then **Unblock**.
   *Good result:* within 5 minutes the laptop's own redial succeeds, resets
   from heads, and catches up.
7. **VM restart.** The hub is down while a push lands, then comes back.
   *Good result:* the missed delivery is redelivered and reaches the laptop.

Every stage is asserted by the Task 8 whole-journey e2e. Stages 1 to 4, 6
and 7 are also run by hand on the real VM and laptop in Task 8.

## Approach

The delivery core (durable store, receiver, queue, processor) already exists
and is reused. New code is:

- the peer registry and admin path;
- the session transport and its client;
- per-session wake-up subscriptions;
- coalescing, retention, journal epoch and heads;
- missed-webhook recovery;
- statistics;
- UI.

Dependency order:

- Tasks 1, 2 and 3 are a strict chain.
- Task 4 follows Task 3.
- Task 5 (missed-webhook recovery) touches only the hub's App client and can
  run in parallel from the start.
- Task 6 (statistics) fills the metrics seam that Task 2 defines, so it runs in
  parallel with Tasks 3 and 4 in its own worktree without editing the session
  read and write paths.
- Task 7 depends on Tasks 1 and 6.
- Task 8 depends on everything.

Every task:

- keeps the repository coverage floor, `--minimum=87` in `go-ci.yml`. No
  per-package floor exists, so the reviewer checks that new packages are well
  covered. An agent never cuts approved scope to satisfy a guard;
- adds `ai/capabilities.json` rows and `docs/cli-flag-matrix.md` lines for new
  command leaves;
- uses injectable clocks and dialers rather than sleeps;
- narrates through `hub/narrate`;
- declares new store collections in `hub/collections.go` so inGitDB accepts
  them;
- keeps every store transaction to at most 100 writes, because of Firestore's
  500-write limit and inGitDB's single-writer lock.

Implementation runs in local Claude Code subagents on the founder's Mac
(Sonnet), each in its own `wb worktree`. Each PR gets an adversarial review of
its diff (Opus, plus Codex when its quota allows) before `wb worktree land`.

## Prerequisites outside the repository

These are done by the lead before Task 2 lands, so that every later task can
be tried on the real VM.

- **Give the VM hub a single supervised owner (sneat-dev/wb#546).** Today a
  detached daemon holds port 8766 while `wb-daemon.service` is failed, and a
  malformed comment line in the unit file needs fixing.
- **Add the Caddy routes on the VM.**
  - `/v0/workbench/peers/connect` needs no basic auth, because the daemon
    authenticates it.
  - `/workbench/admin/login` and `POST /v0/workbench/peers/*/{disconnect,block,unblock}`
    go behind the existing basic auth, admin user only. The daemon also
    requires its own admin cookie, Origin and content type.

  The owner is recorded in a Caddyfile comment, as the existing routes are.
  The Caddy routes land with Task 7.

## Tasks

### Task 1: Peer registry, identity, owner-only admin and management CLI

**Id:** task-1
**Verifies:** peer-connectivity#ac:identity-and-admission, peer-connectivity#ac:peer-commands
**Depends-On:** —
**Status:** planning

Scope:

- **Node ID:** add the node ID file.
- **Peer store:** add a hub `workbench_peers` store keyed by `MachineID`. It
  carries trust, node binding, metadata, `reset_pending`, a queued-work count
  and lifetime counters.
- **Peer scopes:** add the peer scope set (`peer:session` plus the event poll
  and ack scopes) as the second valid set in `validScopes`.
- **Blocked peers:** after credential resolution, the machine bearer resolver
  reads the peer record and refuses a blocked peer.
- **Owner-token RPC:** add invite, rotate, block, unblock and disconnect to the
  owner-token RPC on the unix socket. Invite has the refusals from
  `invite-and-join` (existing name, the hub's own name, the memory engine).
- **Self-hosted enrollment:** move self-hosted machine enrollment to that RPC
  and unmount the HTTP enroll route on self-hosted hubs.
- **CLI:** add `wb peers invite|join|list|get|block|unblock|disconnect`.
  `join`'s handshake verification lands in Task 2; until then `join` verifies
  with an authenticated no-op on the connect route. `disconnect` answers "no
  live session" until Task 2.
- **Config:** add `peers.upstream` to `internal/wbconfig`, and refuse a
  same-origin `remote.provider: hub`.
- **Reads:** add the peers read service and its `/api/v1/peers` and
  `/v0/workbench/peers` mounts.
- **Spec amendment:** amend self-hosted-bench for the unmounted enroll route.

Tests:

- node ID persistence;
- the token printed once;
- every invite refusal;
- rotation keeping the record;
- a peer token refused on the snapshot routes;
- a blocked credential refused on the HTTP long poll;
- no admin over HTTP;
- golden text and JSON output.

### Task 2: WebSocket session, heartbeat, reconnect, supersede and inventory

**Id:** task-2
**Verifies:** peer-connectivity#ac:session-lifecycle
**Depends-On:** 1
**Status:** planning

Scope:

- **Library:** add `github.com/coder/websocket`.
- **`peersession` package:** protocol message types and validation (2 MiB read
  limit, `type` switch, protocol version), and a `Metrics` interface with a
  no-op default wired around every read and write. The interface is the seam
  Task 6 fills.
- **Hub side:** serve `GET /v0/workbench/peers/connect` with pre-upgrade
  authentication, the Origin refusal, the protocol check, the 64-session cap
  and the failed-authentication rate limit. Keep a live-session registry that
  supersedes by node ID, trips `duplicate-node` after 3 supersedes in 60
  seconds, and refuses a node mismatch. `disconnect` and `block` close
  sessions through the registry.
- **Inventory:** store it in a new peer-inventory record, using the
  normalization helper extracted from `MachineSnapshotService.Publish`.
  Routing reads snapshots and inventories together, and skips and narrates an
  invalid record of either kind.
- **Downstream client:** started by the daemon when `peers.upstream` is set.
  It runs hello, welcome and inventory, sends 20-second pings, backs off from
  1 to 60 seconds with full jitter, uses a 5-minute cap while blocked, detects
  wake from the wall clock against the monotonic clock, and stops on terminal
  codes.
- **`wb peers join`:** verifies with the real handshake.
- **Narration:** lifecycle facts.
- **Documentation:** a Tunnels note in `hub/README.md` for the connect route.

Tests use a fake clock, an `httptest` server and a fault-injecting dialer.

### Task 3: Journal delivery over the session, subscriptions and epoch

**Id:** task-3
**Verifies:** peer-connectivity#ac:journal-resume-without-loss
**Depends-On:** 2
**Status:** planning

Scope:

- **Hub side:**
  - Serve `poll`, `ack` and `acked` over the store's `Poll` and `Acknowledge`,
    with envelopes built at delivery (type from the reason).
  - `Acknowledge` succeeds without effect at or below the acknowledged
    sequence, even when the receipt is gone. It deletes receipts, queue
    documents and pending records at or below the ack, in chunks.
  - A `Notifier` gives each session and each HTTP long poll its own
    subscription (a channel with room for one signal) per peer. It is
    signalled after a committed `EnqueueForMachines`. Waiters re-read at least
    every 5 seconds, and a hub without a notifier keeps the 250 ms scan.
  - The journal ID lives in the sequence meta document; a mismatch, or a
    cursor ahead of the head, requires a reset.
- **Downstream side:**
  - A session-backed `repositoryevents.Source` drives the existing
    `Receiver`, which gains a reset hook (the reset itself lands in Task 4)
    and a flag that silences its waiting line.
  - It keeps its own cursor file, which also stores the journal ID.
- **Spec amendment:** github-app-repository-events, for idempotent ack at or
  below the acknowledged sequence.

Tests cover every scenario in AC journal-resume-without-loss.

### Task 4: Coalescing, retention, heads, reset and fetch-always sync

**Id:** task-4
**Verifies:** peer-connectivity#ac:retention-and-reset, peer-connectivity#ac:selective-sync
**Depends-On:** 3
**Status:** planning

Scope:

- **Coalescing:** a per-peer index keyed by (repository, ref) lets a new
  default-branch event replace the queued one in the enqueue transaction,
  removing its pending record. A rename is a barrier.
- **Heads:** add `workbench_repository_heads`, with forward-only updates by
  occurrence time and rename aliases.
- **Markers:** they keep the event and its type (the 14-day history). This
  must cover at least Task 5's 72-hour redelivery window plus the span
  between a delivery's original failure and its last redelivery attempt
  within that window, or a redelivered event could arrive after its own
  dedup marker has already been pruned.
- **Janitor:** hourly and at start. It walks every machine credential and
  prunes markers, heads, and queues over the 10,000-document bound (setting
  `reset_pending`), in chunks. Blocked peers are skipped at enqueue.
- **Reset exchange:** add `heads_request`, `heads` and `reset_ack`, with S read
  before heads and a dedicated monotonic `SetAcknowledgedSequence` store
  operation that clears documents at or below it.
- **Downstream reset:** the receiver's reset hook runs the comparator (local
  `origin` refs, hashed job IDs, rename jobs from aliases).
- **`wb peers reconcile [--dry-run]`.**
- **Processor:** it always runs `git fetch origin <ref>`, then fast-forwards
  only a clean, on-branch, strictly-behind canonical checkout, dispatches
  `checkout-updated` and refreshes the checkout marker when HEAD moved.
- **Local job files:** terminal files older than 7 days are deleted.
- **Spec amendment:** github-app-repository-events, for fetch-always.

The selective-sync test uses 20 bare remotes and a git wrapper that records
every invocation.

### Task 5: Missed-webhook recovery

**Id:** task-5
**Verifies:** peer-connectivity#ac:missed-webhooks-recovered
**Depends-On:** —
**Status:** planning

Scope:

- **Redelivery sweep:** in webhook mode, on start and hourly, the hub:
  - lists the App's deliveries of the last 72 hours through the App API,
    authenticated with the App JWT the hub already builds;
  - redelivers each delivery whose latest attempt failed, exactly once;
  - records redelivered IDs in the store with a 7-day retention;
  - narrates each redelivery.
- **README fix:** correct the claim in `hub/README.md` that GitHub retries
  deliveries by itself.

Tests run against a fake App API.

### Task 6: Traffic and event statistics

**Id:** task-6
**Verifies:** peer-connectivity#ac:counters-and-rates
**Depends-On:** 2
**Status:** planning

Scope:

- **`peerstats` package:** implements Task 2's `Metrics` seam:
  - session and lifetime counters for RX and TX payload bytes, data messages,
    events and events by type;
  - a 60-bucket minute ring with an injectable clock;
  - derived 1m, 5m and 1h throughput and last-hour totals.
- **Persistence:** lifetime totals every minute and at close; on the hub in the
  peer record, downstream in the upstream state file.
- **Exposure:** in the detail read routes, and in `wb peers get` with a text
  sparkline.

Tests cover exact counts, rollover over 61 minutes, rates after an idle gap
and with less than an hour of history, and lifetime surviving a restart while
buckets do not.

### Task 7: Peers dashboard, admin session and status

**Id:** task-7
**Verifies:** peer-connectivity#ac:api-and-dashboard
**Depends-On:** 1, 6
**Status:** planning

Scope:

- **Admin session:** `wb dashboard --admin` obtains a single-use code over the
  owner RPC. `/workbench/admin/login` exchanges it for an `HttpOnly`,
  `SameSite=Strict` cookie. The admin POST routes require that cookie, an
  exact `Origin` and a JSON content type, and report `admin_available`.
- **Trusted proxies:** `hub.trusted_proxies` for displaying the remote
  address.
- **`hub/web` pages:**
  - a peers overview;
  - a detail page (static route with a query parameter) with the two
    inline-SVG charts: RX and TX by minute with dynamic units, and events per
    minute stacked by type with hover and a table fallback;
  - admin buttons, or else the CLI command.
- **Laptop daemon page:** an upstream peer section with a compact chart.
- **`wb daemon status`:** one line per peer.
- **Tests:** an API secret scan across all peer responses, a check that the
  three mounts return identical JSON, and Vitest and Playwright tests,
  including a click on Block.

### Task 8: Whole-journey e2e and VM rollout

**Id:** task-8
**Verifies:** peer-connectivity#ac:whole-journey-e2e
**Depends-On:** 3, 4, 5, 7
**Status:** planning

Scope:

- **In-process e2e:** two daemons, signed fake webhooks and bare remotes. It
  walks the 16-step scenario with no prodding between steps.
- **Real rollout:**
  - release wb and upgrade it on the VM;
  - invite the founder's laptop and join it;
  - run journey stages 1 to 4, 6 and 7 by hand;
  - record receipts in the PR (list and get output, narration lines, git
    reflogs of A and C, and the dashboard screenshot).

## Follow-up outside this repository

After Task 8, the `/wb` pages in `sneat-dev/website` gain a plain-language
peer connectivity section. It covers:

- the always-on hub;
- the laptop waking and fetching only the repositories that moved;
- live events while connected;
- `wb peers` to see and control connections;
- a small diagram with the invite and join commands.

The copy describes only what Task 8 verified on real machines, and it deploys
through the site's existing Cloudflare pipeline. The founder asked for this on
2026-09-18.

## Review reconciliation

Two independent adversarial reviews ran on commit 57ddee8e: Opus and Sonnet,
each in its own context. Codex was also briefed, but its account was out of
quota until 2026-09-19. Their findings and what was done:

Fixed in the Feature, Decision and this Plan:

- **Lost `acked` wedges the receiver.** `Acknowledge` is now idempotent at or
  below the acknowledged sequence, with an AC.
- **Hub-side block was terminal, contradicting unblock.** A blocked node now
  redials with a 5-minute cap.
- **Loopback plus no proxy header is not an admin boundary.** Admin now
  requires the owner RPC, or an admin cookie obtained with a single-use code.
  The self-hosted HTTP enroll route is unmounted.
- **No journal epoch.** A journal ID was added, and a mismatch or a cursor
  ahead of the head forces a reset.
- **A push protocol cannot reuse the `Receiver`.** The protocol is now
  pull-shaped, with a reset hook in the receiver.
- **Inventory contradicted the snapshot contract.** Inventory is now a
  purpose-built record, bounded to 5,000, with shared normalization. Invalid
  records are skipped instead of failing all routing.
- **Block had no data path.** The peer record is the trust authority, and the
  resolver reads it.
- **Two receivers on one queue.** `join` refuses a same-origin
  `remote.provider: hub`, and invite refuses existing and non-peer names.
- **Queue drop lost renames, and the queue was not compacted.** Hub-side
  coalescing was added, and heads carry rename aliases replayed on reset.
- **Unbounded pending records, queue documents below the ack, and job
  files.** All three are now pruned, and the janitor walks credentials.
- **Reset ordering race.** S is read before heads, with a dedicated monotonic
  acknowledgement operation.
- **Join verification failed behind the VM's basic auth.** Join now verifies
  over the connect handshake.
- **Missed webhooks were never recovered.** Task 5 adds the sweep and the
  README is corrected.
- **Fetch vs pull was not decided.** Fetch-always, fast-forward-when-safe,
  recorded in decision 0003.
- **Copied node ID made two nodes supersede each other forever.** A
  `duplicate-node` trip was added.
- **The dashboard buttons had no API path.** An admin session and POST routes
  were added.
- **Janitor transactions were too large.** Every transaction is now at most
  100 writes.
- **Three API surfaces.** There is now one service and one schema; the mounts
  are listed explicitly.
- **Minor items fixed:**
  - per-session subscriptions;
  - the hosted scan is kept;
  - forward-only heads;
  - hashed reset job IDs;
  - the waiting line is silenced;
  - peer tokens are scoped;
  - trusted proxies for the remote address;
  - connection caps;
  - the lag definition and reset display;
  - the laptop chart;
  - the reconcile claim is softened;
  - the plan now fixes the VM supervisor and Caddy route first.

Declined or deferred, with reasons (also listed in the Feature's "Not in this
feature"):

- **`github.pull_request` and `github.check_run` now.** They need new App
  subscriptions, a generic store payload and their consumer (`wb wait`), which
  is its own feature. The envelope reserves the shape.
- **A read API over the detailed history.** The data is kept for 14 days, and
  the API waits for a consumer.
- **A 100% coverage requirement on `hub/`.** Nothing enforces it, so the claim
  was removed rather than added as a new gate.
- **Caddy WebSocket timeouts.** Not changed in the design: Caddy does not
  time out upgraded connections by default, and the node's reconnect covers
  any edge that does. Task 8 verifies it on the real edge.

### Verification pass (aa2d5691)

The Opus reviewer re-checked the revision and found nothing blocking. It
found four serious new defects, all fixed:

- **N1: enqueue could exceed the transaction write limit.** The per-peer
  write count now caps fan-out at 64 peers, and hosted non-peer machines are
  never coalesced.
- **N2: several writers shared the peer record.** It is split into trust,
  statistics and queue-state documents, with transactional merges and a
  janitor recount.
- **N3: flapping Wi-Fi tripped `duplicate-node`.** A supersede counts only
  when the old session was still answering pongs, and `duplicate-node` now
  redials on a 15-minute cap instead of being terminal.
- **N4: the epoch missed a restore.** Any cursor, or receipt-less ack, above
  the stored acknowledged sequence now forces a reset, and the HTTP long poll
  returns `409 reset_required`.

Minor fixes:

- N5: `ack` also gets `reset_required`.
- N6: redelivery retries up to 3 attempts across sweeps.
- N7: the failed-auth limit keys on the trusted-proxy forwarded address.
- N8: heads use last-committed-wins.
- N9: the login URL is printed, and the cookie is named per port.
- N10: peer tokens are session-only.
- N11: only peer queues are dropped.

Two retention gaps are closed: index documents and leftover documents at or
below the ack.

Its recommendation to open the VM edge for the admin routes was taken, so
admin from the dashboard now works on the real VM.

## Risks

- `dalgo2ingitdb` has no range query and a single-writer lock. Coalescing
  keeps each peer queue small. The 100-write chunk rule keeps the janitor from
  starving webhook enqueue.
- A missed-webhook sweep across 72 hours of deliveries may need several App
  API pages; it is rate-limited with the App's own budget, not the user's.
- The hub runs as a detached process while its systemd unit is failed
  (sneat-dev/wb#546). The prerequisite above must land before Task 2 can be
  exercised on the VM.

---
*This document follows the https://specscore.md/plan-specification*
