---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Peer connectivity

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/peer-connectivity?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/peer-connectivity?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/peer-connectivity?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/peer-connectivity?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Every WB daemon is a node, and nodes know each other as peers. A laptop that is
often asleep connects outbound to an always-on WB hub: a VM running
`wb daemon serve` with the GitHub App. The laptop resumes from the last event
it acknowledged and fetches only the repositories that changed while it was
away. While it stays connected, new GitHub events reach it within a second,
with no polling of GitHub and no fleet-wide `git fetch`.

## Problem

The hub on the VM already receives every GitHub webhook, verifies it, and
durably queues a privacy-safe event per enrolled machine
([github-app-repository-events](../github-app-repository-events/README.md),
[self-hosted-bench](../self-hosted-bench/README.md)). Nothing connects a laptop
to it:

- **The laptop never hears from the hub.** The laptop's event receiver starts
  only when `remote.provider` is `hub`, but the laptop keeps `provider: git`
  because claims work only on the git store. So the laptop hears nothing, and
  the founder runs `wb sync` over ~380 repositories to find the three that
  moved.
- **A self-hosted hub cannot admit another machine safely.** Its HTTP
  enrollment route treats every caller as the owner, including a caller that
  arrives through a tunnel. Nothing lists, blocks or revokes a machine
  credential.
- **Delivery is invisible.** It is a 25-second HTTP long poll that re-reads the
  whole per-machine queue every 250 ms. "Is my laptop connected?" has no
  answer, and there is no traffic or event observability.
- **Storage is unbounded.** Poll receipts, deduplication markers and pending
  refresh records are never deleted, and a machine that never acknowledges
  keeps its queue forever.
- **Webhooks missed during downtime are lost.** GitHub does not redeliver a
  failed webhook by itself, so every event missed while the hub was down (the
  VM daemon's supervisor has been failing since 2026-09-16, sneat-dev/wb#546)
  is never delivered.

## Journey

I run the hub on my VM as today. On the VM I type `wb peers invite laptop` and
get a one-time token. On my laptop I run
`wb peers join https://vm1.sneat.dev --token-stdin` and paste the token. The
laptop checks it by opening a session and then restarts its daemon.
`wb peers list` on either machine shows the other one as `connected`.

I close the lid. Pushes land on repositories A, C and then A again. The VM
records all three, and `wb peers list` on the VM shows the laptop `offline`
with a lag of 2 (A's two pushes are one piece of sync work).

I open the lid and do nothing else. Within a minute the laptop reconnects by
itself and receives what it missed. It fetches A and C, and no other
repository. Each clean canonical clone on its default branch fast-forwards. A
dirty or diverged clone still gets its `origin` ref updated but its checkout
is left untouched and reported. `wb peers list` on the VM shows lag 0.

While the laptop is connected, someone merges into repository F. Within a
second the laptop has the event and F is fetched, with nothing polling.

On the VM dashboard I open the laptop's peer page. It shows the peer as
connected, how long it has been connected, its WB version and platform, its
cursor and lag, and two charts for the last 60 minutes: traffic by minute
(received and sent) and events by minute broken down by type. The spike at
14:02 is visibly the burst of `github.push` events from a merge train.

If I lose the laptop, I click **Block** on its page, or run
`wb peers block laptop` on the VM. That cuts its connection at once and
refuses every later attempt, but keeps its history. **Unblock** lets it back
in. Within five minutes the laptop's next redial succeeds, and it reconciles
and catches up.

If the VM itself restarts, the hub asks GitHub for the deliveries that failed
while it was down and redelivers them, so nothing is missing afterwards.

## Behavior

### Nodes, peers and identity

#### REQ: node-identity

Every daemon MUST have a stable node ID: 128 random bits, generated on first
start and stored under the private WB state directory (`<wbhome>/state/node-id`,
mode 0600). The hostname is only a default display name. Renaming the host
changes nothing. The node ID is an identifier, not a secret. A copied state
directory yields two nodes with one ID; `single-live-session` detects this.

#### REQ: peer-is-persistent-state

The hub MUST keep one durable peer record per peer, keyed by the hub-issued
peer ID (the existing `MachineID`). It is split into documents with separate
writers, so that no writer can overwrite another writer's field:

- a **trust** document, written only by admin operations;
- a **statistics** document, written by the persisters;
- the existing per-machine **queue-state** document, written only inside the
  store's enqueue, acknowledge and reset transactions. It carries the
  queued-work count.

Every write is a field merge inside a transaction. The count is changed only
for queue documents read as present in that same transaction, and the janitor
recomputes it.

Together the three documents hold:

- display name and owning identity;
- the bound node ID;
- trust state (`active` or `blocked`);
- created, last-seen and last-connected times;
- the reported WB version, OS, architecture and protocol version;
- `reset_pending`;
- a queued-work count, maintained by the store operations that change the
  queue;
- lifetime statistics.

The peer record is the single authority for trust. After resolving a
credential, the machine bearer resolver reads the peer record by `MachineID`
and refuses a blocked peer. That is one extra document read per authenticated
call, applied to the HTTP long poll and snapshot routes too. Credentials
without a record are not peers and are unaffected. The hub's own local
machine is not a peer and is never listed.

Status is derived, not stored: `blocked` if trust is blocked, `connected`
while a session is live, otherwise `offline`. A peer stays listed while
offline, whatever the duration.

A node with an upstream (the laptop) MUST keep the mirror-image record for its
hub: URL, peer ID and name as the hub reported them, journal ID, last seen,
acknowledged cursor, the lag the hub last reported, and statistics.
`wb peers` on the laptop lists that hub with role `upstream`. On the hub,
peers have role `downstream`.

#### REQ: invite-and-join

`wb peers invite <name>` MUST create a peer and mint its credential on the
local hub. It prints the token once, or writes it to `--token-file <path>`
with mode 0600. The token is never retrievable later.

- An existing peer name is refused unless `--rotate` is given. Rotation keeps
  the peer record, statistics and cursor, clears the node binding and sets
  `reset_pending`.
- The hub's own machine name and any name held by a non-peer credential are
  always refused.
- Invite is refused while the hub's store engine is `memory`.

The peer credential carries only the `peer:session` scope. The session polls
and acknowledges on the peer's behalf. A peer token is refused on the HTTP
long-poll, ack and snapshot routes, so it can never open a second consumer of
its own queue.

`wb peers join <hub-url>` MUST read the token from stdin or `--token-file`,
verify it by completing a `hello`/`welcome` exchange on the session route,
store it as a private credential file, write the `peers.upstream`
configuration, and restart the daemon. It MUST NOT change `remote:`: remote
state, claims and peer events stay independent. It MUST refuse when
`remote.provider` is `hub` with the same origin, because two receivers would
consume one queue.

```yaml
peers:
  upstream:
    url: https://vm1.sneat.dev          # https, or http only for loopback
    token_file: /abs/path/peer-vm1.token
```

#### REQ: admin-requires-owner-credential

Every admission and trust change MUST require proof that the caller is the
local operator. This covers invite, rotate, block, unblock, disconnect and
the self-hosted machine enrollment. Arriving on the loopback listener is not
proof, because tunnels and proxies deliver there.

- **The CLI** calls these operations through the daemon's owner-token RPC
  service on its unix socket.
- **Self-hosted machine enrollment** moves to that RPC service. The HTTP
  route `POST /v0/workbench/machines/enroll` is no longer mounted on a
  self-hosted hub; the hosted instance keeps it with its OAuth viewer.
- **The dashboard** gets an admin session through
  `wb dashboard --admin`. That command obtains a single-use code over the
  owner RPC, valid for 60 seconds. It prints the login URL for the loopback
  address and, when configured, for the hub's public URL, and opens a browser
  only on a desktop session.
- **Admin login.** The code travels in the query string of
  `/workbench/admin/login` because it is single-use and short-lived. The daemon
  exchanges it for a session cookie that is `HttpOnly`, `SameSite=Strict`,
  expires after 12 hours, and is named after the listen port so that two
  daemons on one host keep separate sessions.
- **Admin HTTP routes** require that cookie, an `Origin` equal to the page's
  origin, and a JSON content type.
- **Remote access** works through any path to the dashboard: an SSH port
  forward, or the operator's proxy. The VM's Caddy edge passes
  `/workbench/admin/login` and the peers admin POST routes behind its existing
  basic auth, for the admin user only. It keeps refusing every other write.

#### REQ: peer-management-commands

The CLI MUST provide:

- `wb peers list`: name, role, status, last seen, connected-for, cursor, lag.
- `wb peers get <peer>`: the full record, current session, counters, 1m/5m/1h
  throughput and a one-line text sparkline of the last 60 minutes.
- `wb peers disconnect <peer>`: closes the live session only. The peer stays
  trusted and reconnects by itself.
- `wb peers block <peer>`: closes the live session and refuses future
  sessions and HTTP calls with that credential. The record, cursor and
  statistics are kept.
- `wb peers unblock <peer>`: allows sessions again and sets `reset_pending`.
- `wb peers invite`, `join` and `reconcile`, as specified in this document.

Every command supports `--format json`. `<peer>` accepts a name or peer ID.
On a laptop, `block` and `unblock` act on its upstream locally: the laptop
stops or resumes dialing. Default text output looks like:

```text
NAME          ROLE        STATUS     LAST SEEN  CONNECTED  CURSOR  LAG
alex-macbook  downstream  connected  now        2h 14m     5948    0
dev-mac       downstream  offline    3d ago     -          5711    37
old-laptop    downstream  blocked    41d ago    -          1022    -
```

`CURSOR` is the acknowledged journal sequence, decoded from the opaque cursor
for display. `LAG` is the peer's queued-work count: pieces of sync work, after
coalescing, not yet acknowledged. It is not the distance to the global head,
because most global events are not routed to a given peer. `LAG` shows
`reset` while a reset is pending.

### Session transport

#### REQ: outbound-websocket-session

The downstream node MUST keep one long-lived WebSocket session to
`wss://<hub>/v0/workbench/peers/connect`. It authenticates with its peer token
in the `Authorization` header of the upgrade request. TLS is required except
to a loopback host.

Before accepting the WebSocket, the hub MUST refuse any of the following:

- a browser `Origin` header;
- an unknown credential, a non-peer credential, or a blocked peer;
- an unsupported protocol version;
- more than 64 live sessions in total;
- a source address that has had more than 10 failed authentications in the
  last minute. The source is the socket peer, or the first `X-Forwarded-For`
  hop when the socket peer is in `hub.trusted_proxies`. Behind a proxy, a
  per-socket limit would otherwise be global.

The existing HTTP long poll stays available for the hosted instance and for
the hub's own machine.

#### REQ: session-protocol

Messages are JSON text frames with a `type` field. A frame is at most 2 MiB,
enough for a full 5,000-repository inventory. The protocol is pull-shaped, a
long poll carried over the socket, so the existing receiver drives it
unchanged in shape:

1. downstream → `hello`: protocol version, node ID, display name, WB version,
   OS, architecture, journal ID and last acknowledged cursor, and
   repository-inventory digest.
2. hub → `welcome`: peer ID and name, session ID, journal ID, heartbeat
   interval, current lag, whether an inventory upload is needed, and whether
   a reset is required (see `journal-epoch` and `stale-cursor-reset`).
3. downstream → `inventory`: the canonical repository identities of its
   canonical clones, normalized (trimmed, lower-cased, sorted, deduplicated)
   by the shared helper that snapshot publishing uses, and bounded to 5,000.
   Sent when requested or when the local digest changes. The hub stores it in
   a purpose-built peer inventory record, not as a machine snapshot, so the
   laptop does not appear as a zero-worktree machine in the fleet view.
   Routing reads machine snapshots and peer inventories together. An invalid
   record of either kind is skipped and narrated; it no longer fails routing
   for every other machine.
4. downstream → `poll{cursor, limit ≤ 100, wait ≤ 30 s}` → hub `events`:
   `cursor`, `next_cursor`, `lag` and up to `limit` envelopes. The hub answers
   as soon as events exist, or with an empty batch at `wait`. One poll is
   outstanding per session.
5. downstream → `ack{cursor, event_ids}` → hub `acked{cursor, lag}`. The ack
   has the same content as today's `AckRequest`.
6. The `heads_request`, `heads` and `reset_ack` messages of
   `stale-cursor-reset`.

An unknown message type, an oversized frame or a malformed message closes the
session with a protocol-error code. While a reset is required, the hub
answers every `poll` and every `ack` with `reset_required`. The receiver's
reset hook then clears any persisted pending acknowledgement before it runs
the reset.

#### REQ: heartbeat-and-liveness

Both ends MUST send WebSocket pings every 20 seconds. A missing pong within 20
seconds closes the session. The hub updates `last_seen` on every received
message or pong, and persists it at most once a minute. Heartbeats MUST NOT be
logged at the normal level. The receiver's periodic "waiting for provider
events" progress line is not emitted for peer sessions.

#### REQ: reconnect-with-backoff

The downstream node MUST reconnect after any close or dial failure: DNS, TLS,
refused, reset or a VPN change.

- **Backoff:** exponential from 1 second, doubling to a cap of 60 seconds, with
  full jitter. It resets after a session has stayed up for 60 seconds.
- **Wake:** a wall-clock jump of more than 60 seconds beyond the monotonic
  clock (sleep and wake) triggers an immediate redial.
- **Blocked:** a `blocked` refusal keeps the node redialing with a 5-minute
  cap, so a hub-side `unblock` brings it back without any action on the
  laptop.
- **`duplicate-node`:** the node keeps redialing with a 15-minute cap, so one
  copy keeps working while the operator notices the report.
- **Terminal code:** `unsupported-protocol` stops redialing until the daemon
  restarts or the operator runs `wb peers unblock` on the laptop.

Every refusal and terminal code is reported in `wb peers get` and
`wb daemon status`.

#### REQ: single-live-session

A hub MUST keep at most one live session per peer.

- A new session with the bound node ID supersedes the old one, which is
  closed with `superseded`. This is the common case after sleep, when the old
  TCP connection is half-open.
- A supersede counts as a conflict only when the old session answered a pong
  within the last 10 seconds, which a half-open connection cannot do. More
  than 3 conflicts within 60 seconds means two live nodes share one identity,
  for example a copied state directory. The hub then closes the session with
  `duplicate-node` and narrates it. A laptop on flapping Wi-Fi only ever
  supersedes dead sessions, so it never trips this.
- A session whose node ID differs from the bound node ID is refused with
  `node-mismatch`, which the downstream treats like `blocked` for redial
  purposes.
- The first session after an invite or rotation binds the node ID.

### Journal, cursor and delivery

#### REQ: existing-journal-is-the-journal

The durable journal is the hub's existing repository-event store: a global
monotonic sequence, a per-machine queue written in the enqueue transaction, a
per-machine acknowledged sequence, and ID deduplication markers. The cursor a
peer persists is the existing opaque cursor. The session and the HTTP long
poll MUST read and acknowledge through the same store operations (`Poll`,
`Acknowledge`), so there is exactly one delivery semantics. On the
downstream node, the existing `Receiver` drives the session through a
session-backed `Source`. It is extended with one reset hook, and nothing else
changes.

#### REQ: journal-epoch

The store MUST hold a random journal ID. It is created with the sequence, or
inside a transaction on the first start of a hub that predates it. `welcome`
carries the ID, and the downstream node stores it next to its cursor.

A cursor, or an ack without a receipt, that is above the machine's stored
acknowledged sequence means the store went backwards, for example a restored
directory. A differing journal ID means the store was replaced. In either
case the hub requires a reset instead of delivering from that cursor:

- **On the session,** the hub sends `reset_required`.
- **On the HTTP long poll** (the hub's own machine), the hub answers
  `409 reset_required`. The existing receiver then adopts the hub's
  acknowledged cursor and runs the same heads comparison.

#### REQ: at-least-once-and-idempotent

Delivery is at-least-once.

- **Ack ordering:** the downstream node MUST durably enqueue every event of a
  batch into its local repository-event queue before sending `ack`, and MUST
  persist the pending acknowledgement before sending it, as the existing
  receiver does.
- **Duplicates:** a duplicate event ID is harmless, because the local queue
  deduplicates it.
- **Replayed acks:** `Acknowledge` MUST succeed without effect for any cursor
  at or below the peer's acknowledged sequence, whether or not its poll
  receipt still exists. A lost `acked` reply followed by a replayed ack
  therefore never wedges the receiver.
- **Enqueue failure:** a failed local enqueue leaves the batch unacknowledged.
  The next `poll` from the same cursor redelivers it.

#### REQ: hub-side-coalescing

In the enqueue transaction, a new default-branch event for a peer (never for
a hosted non-peer machine) MUST replace
that peer's still-queued default-branch event for the same repository and ref
(tracked by a per-peer index document). The replaced event's pending-refresh
record is removed with it. A rename is an ordering barrier: nothing coalesces
across it. The queue is therefore bounded by the number of repositories in the
peer's inventory plus renames, not by time offline.

Replacing an event that was already delivered but not yet acknowledged is
safe: the newer event supersedes it, and the old receipt still acknowledges by
its own IDs.

The per-peer work in one enqueue transaction is about 6 writes, so an enqueue
fans out to at most 64 peers, the session cap. That stays under Firestore's
500-write transaction limit. Other transactions (janitor, reset,
acknowledgement cleanup) are chunked to at most 100 writes.

#### REQ: persist-then-notify

A GitHub delivery MUST be translated, entitlement-checked and committed to the
store before any session is told about it. After the commit, the hub signals
every live subscription of each affected peer. Each session and each HTTP long
poll holds its own subscription, a coalescing channel with room for one
signal. A waiting `poll` then reads the store.

- There is no per-peer in-memory event queue.
- A slow, stalled or disconnected peer MUST NOT delay webhook handling, the
  HTTP response to GitHub, or any other peer.
- Waiters re-read the store at least every 5 seconds as a safety net against
  a lost signal.
- A hub with no notifier wired in (the hosted multi-instance deployment) keeps
  the existing 250 ms scan.

#### REQ: canonical-event-envelope

Each element of an `events` batch MUST be an envelope
`{sequence, type, id, occurred_at, repository_event}`. `type` is a namespaced
canonical name, and exactly one typed payload field is present. Version 1
defines two types:

- `github.push`: payload is the existing `repositoryevent.Event` with reason
  `default_branch_updated`;
- `github.repository`: reason `repository_renamed`.

New families add a new type and payload field and never change existing ones.
The store keeps storing `repositoryevent.Event`, and the hub builds the
envelope at delivery. Payloads stay trigger-only under the existing privacy
allowlist: no webhook bodies, actors, commit messages, paths, credentials or
installation IDs.

#### REQ: missed-webhook-recovery

On start and then hourly, a hub in webhook mode MUST do the following:

1. List the App's webhook deliveries of the last 72 hours through the GitHub
   App API. This window is a hard bound: nothing widens it.
2. Select the deliveries whose latest attempt failed, and redeliver each one.
3. Record each redelivery attempt, spaced at least one sweep interval apart
   per delivery, so a crash-loop restart cannot ask GitHub for the same
   delivery faster than the sweep's own cadence.
4. Count an attempt against the 3-attempt limit only when the same listing
   shows evidence the operator's endpoint is currently reachable: some other
   delivery, any GUID, that succeeded more recently than this delivery's own
   last attempt (or, for a delivery never attempted before, any success at
   all within the window). Without that evidence the hub still redelivers —
   the delivery may succeed even though nothing else recently has — but does
   not spend an attempt on it, so an outage longer than 3 sweep intervals
   cannot exhaust the budget by itself. A delivery is retried again on later
   sweeps while its latest attempt still fails, up to 3 counted attempts, and
   is then narrated as abandoned.

Redelivered events deduplicate by delivery ID as usual. Each redelivery is
narrated.

### Retention and reconciliation

#### REQ: bounded-retention

The hub MUST bound every collection that grows with events or peers:

- **Poll receipts** are deleted when a cursor at or above theirs is
  acknowledged.
- **Queue documents** at or below the acknowledged sequence are deleted by
  `Acknowledge` and by `reset_ack`, in chunks of at most 100 writes per
  transaction.
- **Pending-refresh records** are deleted with the queue document they
  describe: on acknowledge, on coalescing, and on a queue drop.
- **Deduplication markers** older than 14 days are pruned. The markers also
  carry the event and its type, which makes them a 14-day detailed event
  history.
- **Coalescing index documents** are deleted with the queue document they
  point to.
- **Leftover queue documents:** a janitor pass deletes any queue, pending or
  index document at or below the acknowledged sequence, which also finishes a
  chunked `reset_ack` delete that crashed midway.
- **Oversized queues:** a peer queue that still exceeds 10,000 documents
  (renames only, in practice) is dropped in chunks, and the peer's
  `reset_pending` is set. A non-peer machine has no reset path, so its queue
  is never dropped: an oversized one is only narrated as a warning.
- **Blocked peers** receive no events at enqueue.
- **Heads** are pruned when no peer inventory has listed the repository for
  90 days.

Pruning runs in the daemon at start and then hourly. It walks every machine
credential, peer or not, so a machine that never acknowledged is still found.
It works in chunks of 100 writes per transaction, and it is narrated.

On the downstream node, local job files in the terminal states `succeeded` and
`superseded` are deleted after 7 days.

#### REQ: latest-known-heads

The hub MUST keep the latest known state per repository in the same
transaction that enqueues a default-branch or rename event. The state is the
canonical identity, the default ref, the target object ID, the event's
occurrence time and its sequence.

- The last committed event wins. Commit timestamps are author-controlled and
  a force-push can move a branch backwards, so they are not trusted for
  ordering. A stale head costs at most one extra fetch, because fetch always
  runs.
- A rename moves the record to the new identity and records the previous
  identity as an alias.

#### REQ: stale-cursor-reset

A reset is required when any of these holds:

- `reset_pending` is set: the queue was dropped, the peer was unblocked or
  rotated, or this is the first session after an invite;
- the journal ID differs;
- the cursor is ahead of the journal.

The reset exchange, in order:

1. The hub reads the global sequence S first and then the heads. It answers
   `heads_request` with `heads{reset_cursor(S), heads[], renames[]}`, in pages
   of at most 1,000 over the inventory. `renames[]` lists every alias that
   maps an inventory identity to its current identity.
2. The downstream node durably enqueues a local rename job for each alias.
3. It compares each head with the canonical clone's
   `refs/remotes/origin/<default branch>`, read locally without a fetch, and
   enqueues a local sync job only where the head differs or is missing
   locally. Job IDs are a hash of the repository and target, so they are safe
   under the event-ID rules and deduplicate.
4. It then sends `reset_ack{reset_cursor}`.
5. The hub sets the acknowledged sequence to S through a dedicated monotonic
   store operation, deletes queue and pending documents at or below S in
   chunks, and clears `reset_pending`.

Events committed after S stay queued and are delivered by the next `poll`. A
repository the hub has no head for is left alone and counted in the reset
report.

### Selective repository sync

#### REQ: fetch-always-fast-forward-when-safe

For each repository named by an event or a reset, the local processor MUST
always run `git fetch origin <ref>`. This updates `origin/<default>` and WB's
view of the remote, and it changes no working tree, index or local branch. It
then fast-forwards the canonical checkout only when that checkout is clean,
on the event's branch, and strictly behind.

- A dirty, diverged, detached or other-branch canonical checkout keeps its
  working tree unchanged and is reported.
- Feature worktrees are never touched.
- When HEAD moved, the processor dispatches the existing `checkout-updated`
  lifecycle hook and refreshes the checkout marker.

This amends the existing safe-sync path of
[github-app-repository-events](../github-app-repository-events/README.md),
which fetched nothing for a dirty clone and so left its remote view stale.

#### REQ: sync-only-named-repositories

Received events MUST enter the existing local repository-event queue and
processor. Same repository and ref coalesce, renames stay ordered, and writers
for the same repository are serialized. Only those repositories are touched.
Nothing enumerates the fleet, and nothing runs `gh repo list`.

#### REQ: reconcile-command

`wb peers reconcile [--dry-run]` MUST run the reset comparison against the
upstream on demand, without changing the hub cursor. It prints which of the
laptop's existing canonical clones differ from the hub's heads, and without
`--dry-run` it enqueues their sync jobs. It never discovers repositories the
laptop has not cloned; `wb sync` remains the tool for that.

### Statistics and dashboard

#### REQ: traffic-and-event-counters

Both ends MUST keep counters per session and as persisted lifetime totals,
separately for received (RX) and sent (TX):

- payload bytes: the length of WebSocket message payloads. This is not TCP or
  TLS wire bytes, and the UI and CLI say "payload bytes";
- data messages;
- events;
- events by canonical type;
- connection count and total connected duration.

Lifetime totals are persisted at least once a minute and at session close.

#### REQ: minute-buckets-and-rates

Each end MUST keep 60 one-minute buckets per peer, driven by an injectable
clock. Each bucket holds RX and TX payload bytes, messages, events, and events
by type.

From the buckets it derives:

- 1-minute, 5-minute and 1-hour throughput for RX and TX;
- last-hour byte and event totals;
- event rates.

These are called traffic rate or throughput, never bandwidth or speed. Buckets
live in memory and do not survive a daemon restart; the charts say "since
restart" when they cover less than an hour. Correctness state and lifetime
totals are durable.

#### REQ: peers-api

Peers MUST be served by one service with one versioned JSON schema
(`schema_version`), mounted in three places:

- **Local daemon API:** `GET /api/v1/peers` and `/api/v1/peers/{id}` on every
  node.
- **Hub dashboard reads:** the same handlers at `GET /v0/workbench/peers` and
  `/v0/workbench/peers/{id}`, authorized like the other dashboard reads.
- **Admin writes:** `POST /v0/workbench/peers/{id}/{disconnect|block|unblock}`
  behind `admin-requires-owner-credential`, backed by the same service
  functions as the CLI's owner RPC.

The detail response carries:

- the record;
- the current session: connected-at, last heartbeat, remote address and
  protocol;
- the counters, the 1m/5m/1h throughput and the 60 buckets;
- `admin_available`.

The remote address is the socket peer, or the first `X-Forwarded-For` hop only
when the connection comes from an address in `hub.trusted_proxies`.

Tokens and token digests MUST never be returned. Node IDs are shown truncated
to 8 characters for readability; they are not secret.

#### REQ: peers-dashboard

The embedded dashboard (`hub/web`) MUST add a peers overview listing every
peer with its status, last seen, connected-for and lag.

It MUST also add a peer detail page showing:

- name, status, connected since, last heartbeat;
- WB version, OS/architecture, protocol and remote origin;
- cursor and lag;
- payload bytes, messages and events, RX and TX, for the session and lifetime;
- 1m/5m/1h throughput;
- two last-60-minutes charts: traffic RX and TX by minute with dynamic units,
  and events per minute stacked by type, with a per-minute breakdown on hover
  and a table fallback.

The pages follow the existing design language: inline SVG, `global.css`
tokens, no chart library.

**Disconnect**, **Block** and **Unblock** buttons appear when
`admin_available` is true. Otherwise the page shows the exact CLI command and
the `wb dashboard --admin` hint.

The laptop's own daemon page (`internal/dashboard`) shows its upstream peer
with the same counters and a compact traffic chart. `wb daemon status` adds
one line per peer.

### Diagnostics

#### REQ: peer-narration

Session lifecycle MUST be narrated on both ends through the existing
`hub/narrate` line format, one line per fact. The narrated facts are:

- connect, with version, platform and cursor;
- disconnect, with reason and duration;
- authentication failure, with the reason category only;
- block and unblock;
- supersede, and duplicate-node;
- a reconnect attempt, from the third consecutive failure onward;
- catch-up start and finish, with the event count;
- cursor advance, once per batch;
- reset and reconciliation: repositories compared, differing, unknown and
  renamed;
- missed-webhook redelivery;
- retention pruning and journal errors.

Heartbeats and individual pongs are never narrated. No token, digest or
payload body appears in any line.

## Not in this feature

These parts of the founder's brief are deliberately deferred, each with its
reason:

- **`github.pull_request` and `github.check_run` events.** The envelope
  reserves the shape. Relaying them needs new App webhook subscriptions and a
  generic store payload, and their consumer, the bounded `wb wait` verb, is
  its own feature. A follow-up feature adds both together.
- **A queryable detailed-history API.** The 14-day history is stored in the
  deduplication markers and not yet exposed; a future consumer adds a read
  route.
- **Mesh routing, several upstreams per node, key-pair or mTLS
  authentication.** The peer record and scopes leave room for all three.

## Dependencies

- github-app-repository-events
- self-hosted-bench
- remote-state

## Acceptance Criteria

### AC: identity-and-admission

**Requirements:** peer-connectivity#req:node-identity, peer-connectivity#req:peer-is-persistent-state, peer-connectivity#req:invite-and-join, peer-connectivity#req:admin-requires-owner-credential

These must hold:

- **Node ID:** survives a daemon restart and a host rename.
- **Invite:** `wb peers invite` prints the token once. It refuses an existing
  name without `--rotate`, the hub's own name, and the memory engine.
- **Peer token scope:** a peer token cannot read machine snapshots.
- **Join:** `wb peers join` verifies over `hello`/`welcome`, writes
  `peers.upstream` with `remote:` byte-identical, and refuses a same-origin
  `remote.provider: hub`.
- **Unauthorised admin:** no admin operation succeeds over HTTP without the
  admin cookie. This covers a loopback request with no proxy headers, a
  cross-origin simple POST, and a request through a proxy fixture.
- **Enrollment route:** on a self-hosted hub,
  `POST /v0/workbench/machines/enroll` is not mounted.
- **Admin login:** the single-use code works once, only within 60 seconds.

### AC: peer-commands

**Requirements:** peer-connectivity#req:peer-management-commands, peer-connectivity#req:peer-is-persistent-state, peer-connectivity#req:reconnect-with-backoff

Against a daemon fixture, `wb peers list/get/disconnect/block/unblock` produce
the documented text and JSON shapes. Then:

- `disconnect` ends the session, and the peer is `connected` again after its
  next dial.
- `block` ends the session, and redials are refused with `blocked`. The peer
  record, cursor and lifetime counters are unchanged.
- A blocked credential is also refused on the HTTP long poll.
- After `unblock`, the downstream node's next redial (within its 5-minute cap,
  on a fake clock) succeeds. It runs a reset and catches up.

### AC: session-lifecycle

**Requirements:** peer-connectivity#req:outbound-websocket-session, peer-connectivity#req:session-protocol, peer-connectivity#req:heartbeat-and-liveness, peer-connectivity#req:reconnect-with-backoff, peer-connectivity#req:single-live-session

Tests run with a fake clock and a fault-injecting dialer. They cover:

- **Handshake:** hello, welcome and inventory.
- **Pre-upgrade refusals:** a browser Origin, a bad token, a snapshot-scoped
  token, an unsupported protocol, the session cap, and the failed-auth rate
  limit, each refused before the upgrade.
- **Liveness:** a missed pong closes the session.
- **Backoff:** doubles to 60 seconds with jitter, and resets after a stable
  minute.
- **Wake:** a simulated wake triggers an immediate redial.
- **Blocked redial:** a `blocked` refusal redials with a 5-minute cap.
- **Supersede:** a second session supersedes the first.
- **Duplicate node:** 4 conflicting supersedes within 60 seconds (old sessions
  still answering pongs) end in `duplicate-node` with a 15-minute redial cap.
  Rapid redials over a dead link never trip it.
- **Node mismatch:** refused.
- **Oversized frame:** closes with a protocol error.
- **Before a reset:** a `poll` or an `ack` sent before a required reset gets
  `reset_required`.
- **Rate-limit key:** the failed-auth limit keys on the trusted-proxy
  forwarded address.

### AC: journal-resume-without-loss

**Requirements:** peer-connectivity#req:existing-journal-is-the-journal, peer-connectivity#req:at-least-once-and-idempotent, peer-connectivity#req:persist-then-notify, peer-connectivity#req:journal-epoch

Each of these runs as its own test:

- a disconnect mid-catch-up;
- a hub restart;
- a downstream restart with a persisted pending acknowledgement;
- an `acked` reply lost after the hub committed and pruned the receipt;
- a local enqueue failure mid-batch;
- a duplicated batch;
- a replayed ack while a reset is pending.

After each, every event is in the local queue exactly once, the cursor equals
the last acknowledged batch, and the receiver keeps making progress.

The epoch tests cover a store replaced under the same URL, a store restored
from an older copy, and a cursor above the stored acknowledged sequence.
Each yields a reset, never silent skipping, on the session and on the HTTP
long poll alike.

A peer token is refused on the HTTP long poll.

Concurrency tests run `block` against the statistics persister, and a reset
against an acknowledgement. Trust is never reverted, and the queued-work
count matches a recount.

The live and backpressure tests show:

- two subscriptions for one peer (a superseded session plus its successor)
  both wake;
- a peer that never polls does not delay a webhook response (measured against
  a bound) or delivery to a second peer;
- no per-peer memory grows.

### AC: retention-and-reset

**Requirements:** peer-connectivity#req:bounded-retention, peer-connectivity#req:hub-side-coalescing, peer-connectivity#req:latest-known-heads, peer-connectivity#req:stale-cursor-reset, peer-connectivity#req:reconcile-command

Retention:

- Ten pushes to one repository leave one queued document and one pending
  record.
- After an ack, no receipt, queue document or pending record at or below the
  ack remains.
- Markers older than 14 days are pruned.
- A queue forced past its bound is dropped in chunks, with no transaction over
  100 writes.
- Terminal local job files older than 7 days are deleted.

Heads:

- The head always equals the last committed event, including a force-push to
  an older commit.

Reset and reconciliation:

- A reset that races a new commit loses nothing: the event committed between
  the S read and the heads read is delivered after `reset_ack`.
- A rename dropped with the queue is replayed from the heads aliases.
- `wb peers reconcile --dry-run` lists exactly the differing repositories and
  changes nothing.

### AC: selective-sync

**Requirements:** peer-connectivity#req:sync-only-named-repositories, peer-connectivity#req:fetch-always-fast-forward-when-safe, peer-connectivity#req:canonical-event-envelope

The test uses 20 local canonical clones backed by bare remotes, with a git
wrapper that records every invocation per repository. Events for A, C and A
again then give these results:

- only A and C see `fetch`;
- A syncs once;
- a clean A fast-forwards;
- a dirty C gets an updated `origin/main` while its working tree and index
  stay byte-identical, and C is reported;
- no `gh` or fleet-discovery call happens.

The envelope tests reject an unknown type, a missing or duplicated payload
field, and any field outside the privacy allowlist.

### AC: missed-webhooks-recovered

**Requirements:** peer-connectivity#req:missed-webhook-recovery

Against a fake GitHub App API listing three failed deliveries and one
successful one, the hub redelivers exactly the three. A delivery whose
redelivery also fails is retried on later sweeps, up to 3 attempts. The resulting events deduplicate against any that
had already arrived.

### AC: counters-and-rates

**Requirements:** peer-connectivity#req:traffic-and-event-counters, peer-connectivity#req:minute-buckets-and-rates

With an injected clock:

- **Counts:** known message sizes give exact session and lifetime RX and TX
  payload bytes, messages, events and events by type, on both ends.
- **Rollover:** 61 minutes of rollover keep exactly 60 buckets.
- **Rates:** 1m, 5m and 1h throughput equal hand-computed values, including
  after an idle gap and with less than 60 minutes of history.
- **Persistence:** lifetime totals survive a restart; buckets do not.

### AC: api-and-dashboard

**Requirements:** peer-connectivity#req:peers-api, peer-connectivity#req:peers-dashboard, peer-connectivity#req:peer-narration

These must hold:

- **No secrets:** no peers API response contains a token or digest. This is
  asserted by scanning every response in the suite.
- **Same data on every mount:** the three mounts return identical JSON for
  the same peer.
- **Remote address:** the displayed address uses `X-Forwarded-For` only from
  a trusted proxy.
- **Dashboard rendering:** component tests render the overview and a detail
  page from a fixture with both charts and the table fallback. The admin
  buttons appear only when `admin_available` is true; a Playwright test
  clicks Block and observes the status change.
- **Narration:** tests assert one line per lifecycle fact, and none for
  heartbeats or receiver waiting.

### AC: whole-journey-e2e

**Requirements:** peer-connectivity#req:outbound-websocket-session, peer-connectivity#req:persist-then-notify, peer-connectivity#req:stale-cursor-reset, peer-connectivity#req:sync-only-named-repositories, peer-connectivity#req:peer-management-commands, peer-connectivity#req:peers-api

One end-to-end test runs a hub daemon and a downstream daemon in-process, with
signed fake webhook deliveries and local bare remotes. It walks the canonical
scenario from the brief, and no step prods the next:

1. The downstream node is offline.
2. Pushes arrive for A, C, then A.
3. The hub persists them and coalesces A.
4. The downstream node starts and authenticates.
5. It resumes from its cursor.
6. Only A and C are reconciled.
7. Only A and C are fetched.
8. Lag reaches 0.
9. A push to F arrives while the downstream node is connected.
10. F is delivered with no poll interval elapsed.
11. F is processed.
12. `peers list/get` show the connection, cursor, lag and counters.
13. The peers API reports the buckets.
14. `disconnect` is followed by a push and an automatic reconnect.
15. Catch-up completes with no loss.
16. `block` refuses the next dial, and `unblock` lets the next redial
    reconcile and catch up.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
