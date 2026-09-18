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
often asleep connects outbound to an always-on WB hub (a VM running
`wb daemon serve` with the GitHub App), resumes from the last event it
acknowledged, and fetches only the repositories that changed while it was away.
While it stays connected, new GitHub events reach it within a second, with no
polling of GitHub and no fleet-wide `git fetch`.

## Problem

The hub on the VM already receives every GitHub webhook, verifies it, and
durably queues a privacy-safe event per enrolled machine
([github-app-repository-events](../github-app-repository-events/README.md),
[self-hosted-bench](../self-hosted-bench/README.md)). Nothing connects a laptop
to it:

- The laptop's event receiver starts only when `remote.provider` is `hub`, but
  the laptop keeps `provider: git` because claims work only on the git store.
  So the laptop hears nothing and the founder runs `wb sync` over ~380
  repositories to find the three that moved.
- A self-hosted hub has no way to admit another machine. Its enrollment route
  trusts whoever reaches the listener, and nothing lists, blocks or revokes a
  machine credential.
- Delivery is a 25-second HTTP long poll that re-reads the whole per-machine
  queue every 250 ms. "Is my laptop connected?" has no answer, a slow machine
  is invisible, and there is no traffic or event observability.
- Storage is unbounded: poll receipts and deduplication markers are never
  deleted, and a machine that never acknowledges keeps its queue forever.

## Journey

I run the hub on my VM as today. On the VM I type `wb peers invite laptop` and
get a one-time token. On my laptop I run
`wb peers join https://vm1.sneat.dev --token-stdin`, paste it, and the laptop
daemon restarts. `wb peers list` on either machine shows the other one as
`connected`.

I close the lid. Pushes land on repositories A, C and then A again. The VM
records all three, and `wb peers list` on the VM shows the laptop `offline`
with a lag of 3.

I open the lid and do nothing else. Within a minute the laptop reconnects by
itself, receives the three events it missed, and fast-forwards the canonical
clones of A and C: A once, not twice, and no other repository is fetched.
`wb peers list` on the VM shows lag 0. A dirty or diverged clone is left
untouched and reported, as today.

While the laptop is connected, someone merges into repository F. Within a
second the laptop has the event and F's clone moves, with nothing polling.

On the VM dashboard I open the laptop's peer page. It shows the peer as
connected, how long it has been connected, its WB version and platform, its
cursor and lag, and two charts for the last 60 minutes: traffic by minute
(received and sent) and events by minute broken down by type. The spike at
14:02 is visibly the burst of `github.push` events from a merge train.

If I lose the laptop, `wb peers block laptop` on the VM cuts its connection at
once and refuses every later attempt, but keeps its history. `wb peers unblock
laptop` lets it back in, and it catches up.

## Behavior

### Nodes, peers and identity

#### REQ: node-identity

Every daemon MUST have a stable node ID: 128 random bits, generated on first
start and stored under the private WB state directory (`<wbhome>/state/node-id`,
mode 0600). The hostname is only a default display name. Renaming the host
changes nothing. Copying the state directory to another machine copies the
identity, and the hub detects that as a node mismatch (see
`single-live-session`).

#### REQ: peer-is-persistent-state

The hub MUST keep one durable peer record per admitted machine. The record
holds the hub-issued peer ID (the existing `MachineID`), display name, owning
identity, bound node ID, trust state (`active` or `blocked`), created, last-seen
and last-connected times, the reported WB version, OS, architecture and
protocol version, the acknowledged cursor, a `reset_pending` flag, and lifetime
statistics. Status is derived, not stored: `blocked` if trust is blocked,
`connected` while a session is live, otherwise `offline`. A peer stays listed
while offline, whatever the duration.

A node with an upstream (the laptop) MUST keep the mirror-image record for its
hub: URL, peer ID and name as the hub reported them, last seen, acknowledged
cursor and statistics. `wb peers` on the laptop lists that hub as a peer with
role `upstream`. On the hub, admitted machines have role `downstream`.

#### REQ: invite-and-join

`wb peers invite <name>` MUST mint a machine credential with the existing
enrollment scopes for a new or existing peer name on the local hub, and print
the token once (or write it to `--token-file <path>`, mode 0600). It MUST NOT
be retrievable later. Re-inviting an existing name rotates its credential and
keeps the peer record, statistics and cursor.

`wb peers join <hub-url>` MUST read the token from stdin or `--token-file`,
verify it against the hub, store it as a private credential file, write the
`peers.upstream` configuration, and restart the daemon. It MUST NOT change
`remote:`: remote state, claims and peer events are independent.

```yaml
peers:
  upstream:
    url: https://vm1.sneat.dev          # https, or http only for loopback
    token_file: /abs/path/peer-vm1.token
```

#### REQ: admin-is-loopback-only

Admission and trust changes (invite, block, unblock, disconnect, and the
existing `POST /v0/workbench/machines/enroll` on a self-hosted hub) MUST be
served only to the local operator. The request must arrive over the loopback
listener with no `Forwarded` or `X-Forwarded-*` header, so a reverse proxy or
tunnel can never reach them. The CLI reaches them through the daemon's
existing owner-authenticated local API. This closes the self-hosted
enrollment gap described in the Problem section.

#### REQ: peer-management-commands

The CLI MUST provide:

- `wb peers list`: name, role, status, last seen, connected-for, cursor, lag.
- `wb peers get <peer>`: the full record, current connection and statistics.
- `wb peers disconnect <peer>`: closes the live session only. The peer stays
  trusted and reconnects by itself.
- `wb peers block <peer>`: closes the live session and refuses future
  sessions and HTTP calls with that credential. History, cursor and
  statistics are kept.
- `wb peers unblock <peer>`: allows sessions again.
- `wb peers invite <name>` and `wb peers join <url>`, as above.

Each supports `--format json`. `<peer>` accepts a name or peer ID. Block and
unblock on a laptop act on its upstream locally: the laptop stops or resumes
dialing. Default text output looks like:

```text
NAME          ROLE        STATUS     LAST SEEN  CONNECTED  CURSOR  LAG
alex-macbook  downstream  connected  now        2h 14m     5948    0
dev-mac       downstream  offline    3d ago     -          5711    237
old-laptop    downstream  blocked    41d ago    -          1022    -
```

`CURSOR` is the acknowledged journal sequence. `LAG` is the number of events
queued for that peer and not yet acknowledged, not the distance to the global
head, because most global events are not routed to a given peer.

### Session transport

#### REQ: outbound-websocket-session

The downstream node MUST open one long-lived WebSocket session to
`wss://<hub>/v0/workbench/peers/connect`. It authenticates with its machine
bearer token in the `Authorization` header of the upgrade request. TLS is
required except to a loopback host. The hub MUST refuse an upgrade that
carries a browser `Origin` header, a blocked or unknown credential, or an
unsupported protocol version, before it accepts the WebSocket. The existing
HTTP long poll stays available and unchanged for the hosted instance and for
the hub's own local machine.

#### REQ: session-protocol

Messages are JSON text frames, each at most 1 MiB, with a `type` field. The
exchange is:

1. downstream → `hello`: protocol version, node ID, display name, WB version,
   OS, architecture, last acknowledged cursor, repository-inventory digest.
2. hub → `welcome`: peer ID and name, session ID, heartbeat interval, whether
   an inventory upload is needed, and whether a reset is pending.
3. downstream → `inventory`: the canonical repository identities in its
   projects root, bounded to 10,000. Sent when requested or whenever the local
   digest changes. It replaces that peer's routing entry exactly as
   `wb remote publish` does for a hub machine.
4. hub → `events`: one batch of up to 100 events with its cursor and next
   cursor. At most one batch is in flight per session.
5. downstream → `ack`: the batch's next cursor and event IDs, the same
   content as today's `AckRequest`. The hub → `acked` reply confirms it.
6. The reset exchange is described under `stale-cursor-reset`.

An unknown message type, an oversized frame, or a malformed message closes the
session with a protocol-error code. A peer that has not acknowledged an
in-flight batch within 120 seconds is closed as slow and resumes on reconnect.

#### REQ: heartbeat-and-liveness

Both ends MUST send WebSocket pings every 20 seconds. A missing pong within 20
seconds closes the session. The hub updates `last_seen` on every received
message or pong, persisted at most once a minute. Heartbeats MUST NOT be
logged at the normal level.

#### REQ: reconnect-with-backoff

The downstream node MUST reconnect after any close or dial failure (DNS, TLS,
refused, reset, VPN change) with exponential backoff from 1 second, doubling
to a cap of 60 seconds, with full jitter. The backoff resets after a session
has stayed up for 60 seconds. A wall-clock jump of more than 60 seconds
beyond the monotonic clock (sleep and wake) MUST trigger an immediate redial.
Close codes for `blocked`, `node-mismatch` and `unsupported-protocol` MUST
stop redialing until the daemon restarts or the operator runs `wb peers
unblock` locally, and each MUST be reported in `wb peers get` and
`wb daemon status`.

#### REQ: single-live-session

A hub MUST keep at most one live session per peer. A new authenticated
session for the same peer and node ID supersedes the old one, which is closed
with a `superseded` code (the common case after sleep, when the old TCP
connection is half-open). A session whose node ID differs from the peer's
bound node ID MUST be refused with `node-mismatch`. The first session after an
invite binds the node ID. Re-inviting the name clears the binding.

### Journal, cursor and delivery

#### REQ: existing-journal-is-the-journal

The durable journal is the hub's existing repository-event store: a global
monotonic sequence, a per-machine queue fanned out at enqueue time, per-machine
acknowledged sequence, and ID deduplication markers. The cursor a peer
persists is the existing opaque cursor. The session transport MUST read and
acknowledge through the same store operations as the HTTP long poll (`Poll`,
`Acknowledge`), so there is exactly one delivery semantics.

#### REQ: at-least-once-and-idempotent

Delivery is at-least-once. The downstream node MUST durably enqueue every
event of a batch into its local repository-event queue before sending `ack`,
and MUST persist the pending acknowledgement before sending it, exactly as the
existing receiver does. A duplicate event ID is harmless: the local queue
deduplicates it. A reconnect during catch-up redelivers from the last
acknowledged cursor. Acknowledgement is idempotent for the same peer, cursor
and ID set. A failed local enqueue MUST leave the batch unacknowledged and
close the session, so it is redelivered after reconnect.

#### REQ: persist-then-notify

A GitHub delivery MUST be translated, entitlement-checked and committed to the
store before any session is told about it. After a successful commit the hub
MUST signal each affected peer's live session through a non-blocking,
coalescing wake-up (at most one pending signal per session). The session then
reads the next batch from the store. There MUST be no per-peer in-memory event
queue. A slow, stalled or disconnected peer MUST NOT delay webhook handling,
the HTTP response to GitHub, or any other peer. Each session also re-reads the
store every 30 seconds as a safety net against a lost signal.

#### REQ: canonical-event-envelope

Each element of an `events` batch MUST be an envelope `{sequence, type, id,
occurred_at, repository_event}`, where `type` is a namespaced canonical name
and exactly one typed payload field is present. Version 1 defines
`github.push` (payload: the existing `repositoryevent.Event` with reason
`default_branch_updated`) and `github.repository` (reason
`repository_renamed`). New families add a new type and payload field and never
change existing ones. Payloads stay trigger-only under the existing privacy
allowlist: no webhook bodies, actors, commit messages, paths, credentials or
installation IDs.

### Retention and reconciliation

#### REQ: bounded-retention

The hub MUST bound every collection that grows with events:

- a poll receipt is deleted when its cursor, or any later one, is
  acknowledged;
- deduplication markers older than 14 days are pruned;
- a peer's queue that exceeds 5,000 events, or whose oldest unacknowledged
  event is older than 30 days, is dropped and the peer's `reset_pending` is
  set;
- events are not enqueued for a blocked peer, and unblocking sets
  `reset_pending`.

Pruning runs in the daemon at start and then hourly, bounded per run, and is
narrated. Peer records themselves are small and kept.

#### REQ: latest-known-heads

The hub MUST keep the latest known state per repository: the canonical
identity, the default ref, the target object ID and the sequence of the event
that set it. It is updated in the same store transaction that enqueues a
default-branch or rename event. It holds one record per repository and is the
source for reconciliation.

#### REQ: stale-cursor-reset

When `reset_pending` is set (queue dropped, unblocked, or first session after
an invite), `welcome` reports it, and the hub sends no `events` until the reset
completes. The downstream node sends `heads_request` for its inventory. The hub
answers `heads` with a reset cursor (the peer's queue position at that moment)
and the head of every requested repository it knows, in pages of at most 1,000.
The downstream node compares each head with the canonical clone's
`refs/remotes/origin/<default branch>`, and durably enqueues a local sync job
only for repositories whose head differs or is missing locally. It then sends
`reset_ack` with the reset cursor. The hub clears
`reset_pending` and resumes normal delivery after that cursor. Events that
arrive during the reset stay queued and are delivered afterwards. A repository
the hub has no head for is left alone and counted in the reset report.

### Selective repository sync

#### REQ: sync-only-named-repositories

Received events MUST enter the existing local repository-event queue and
processor. Same repository and ref coalesce to the newest event, renames stay
ordered, and writers for the same repository are serialized. Only those
repositories are touched, and nothing enumerates the fleet. The processor's
existing safety rules apply unchanged: a clean canonical checkout on the
event's branch fast-forwards (`pull --ff-only`). A dirty, diverged, detached
or other-branch canonical checkout, or any feature worktree, is left
unchanged and reported. A repository the peer has not cloned never receives
events, because routing uses the inventory it published.

#### REQ: reconcile-command

`wb peers reconcile [--dry-run]` MUST run the heads comparison on demand
against the upstream and print which repositories differ. Without
`--dry-run` it enqueues their sync jobs. This is the targeted replacement for
a fleet-wide `wb sync` when a laptop returns.

### Statistics and dashboard

#### REQ: traffic-and-event-counters

Both ends MUST count, per session and as persisted lifetime totals, separately
for received (RX) and sent (TX): payload bytes (the length of WebSocket
message payloads, which is not TCP or TLS wire bytes; the UI and CLI say
"payload bytes"), data messages, and events. Also counted: events by
canonical type, connection count and total connected duration. Lifetime
totals are persisted at least once a minute and at session close.

#### REQ: minute-buckets-and-rates

Each end MUST keep 60 one-minute buckets per peer, with RX and TX payload
bytes, messages, events and events by type, driven by an injectable clock.
From them it derives 1-minute, 5-minute and 1-hour throughput for RX and TX,
last-hour byte and event totals, and event rates. These are called traffic
rate or throughput, never bandwidth or speed. Buckets are in memory and do not
survive a daemon restart. The charts say "since restart" when they cover less
than an hour. Correctness state and lifetime totals are durable.

#### REQ: peers-api

The hub MUST serve read routes `GET /v0/workbench/peers` and
`GET /v0/workbench/peers/{id}` (viewer-authorized, like the other dashboard
reads), and every node serves `GET /api/v1/peers` and `/api/v1/peers/{id}` on
its loopback daemon API, with `schema_version`. The detail response carries
the record, current session (connected-at, last heartbeat, remote address as
seen by the hub, protocol), counters, 1m/5m/1h throughput and the 60 buckets.
Tokens, token digests and node IDs in full are never returned. Node IDs are
shown truncated to 8 characters. Trust changes are loopback-only
(`admin-is-loopback-only`).

#### REQ: peers-dashboard

The embedded dashboard (`hub/web`) MUST add a peers overview (every peer with
status, last seen, connected-for and lag) and a peer detail page. The detail
page shows name, status, connected since, last heartbeat, WB version,
OS/architecture, protocol, remote origin, cursor and lag, payload
bytes/messages/events RX and TX (session and lifetime), and 1m/5m/1h
throughput. It also shows two last-60-minutes charts: traffic RX and TX by
minute with dynamic units, and events per minute stacked by type with a
per-minute breakdown on hover and in a table fallback. It follows the
existing design language (inline SVG, `global.css` tokens, no chart
library). Block, unblock and disconnect buttons appear only on a loopback
view. Behind a proxy they are replaced by the CLI command to run on the hub.
`wb daemon status` adds one line per peer.

### Diagnostics

#### REQ: peer-narration

Session lifecycle MUST be narrated through the existing `hub/narrate` line
format on both ends, one line per fact. Narrated facts: connect (version,
platform, cursor), disconnect (reason, duration), auth failure (reason
category only), block and unblock, supersede, reconnect attempts after the
third consecutive failure, catch-up start and finish (event count), cursor
advance per batch, reset and reconciliation (repositories compared, differing,
unknown), slow-peer close, and retention and journal errors. Heartbeats and
individual pongs are never narrated. No token, digest or payload body appears.

## Dependencies

- github-app-repository-events
- self-hosted-bench
- remote-state

## Acceptance Criteria

### AC: identity-and-admission

**Requirements:** peer-connectivity#req:node-identity, peer-connectivity#req:peer-is-persistent-state, peer-connectivity#req:invite-and-join, peer-connectivity#req:admin-is-loopback-only

A node ID survives daemon restart and host rename. `wb peers invite` prints a
token once, and a second read is impossible. `wb peers join` writes
`peers.upstream` without touching `remote:`. Invite, block and enroll requests
that carry an `X-Forwarded-For` header, or arrive through a reverse proxy
fixture, are refused, and the same requests over plain loopback succeed.

### AC: peer-commands

**Requirements:** peer-connectivity#req:peer-management-commands, peer-connectivity#req:peer-is-persistent-state

`wb peers list/get/disconnect/block/unblock` produce the documented text and
JSON shapes against a daemon test fixture. `disconnect` ends the session and
the peer is `connected` again after its next dial. `block` ends it and the
redial is refused with `blocked` while the peer record, cursor and lifetime
counters are unchanged. `unblock` lets it in, and it catches up.

### AC: session-lifecycle

**Requirements:** peer-connectivity#req:outbound-websocket-session, peer-connectivity#req:session-protocol, peer-connectivity#req:heartbeat-and-liveness, peer-connectivity#req:reconnect-with-backoff, peer-connectivity#req:single-live-session

With a fake clock and a fault-injecting dialer, tests cover hello/welcome,
refused Origin, bad token and unsupported protocol before upgrade, a missed
pong closing the session, backoff doubling to 60 seconds with jitter and
resetting after a stable minute, an immediate redial after a simulated wake,
a second session superseding the first, a node-ID mismatch refused, an
oversized frame closing with a protocol error, and terminal close codes
stopping the redial.

### AC: journal-resume-without-loss

**Requirements:** peer-connectivity#req:existing-journal-is-the-journal, peer-connectivity#req:at-least-once-and-idempotent, peer-connectivity#req:persist-then-notify

Tests cover each of these: a disconnect mid-catch-up, a hub restart, a
downstream restart with a persisted pending acknowledgement, a local enqueue
failure mid-batch, and a duplicated batch. Afterwards every event is in the
local queue exactly once and the cursor equals the last acknowledged batch. A
peer that never reads does not delay a webhook response (measured against a
bound) or delivery to a second peer. No per-peer queue grows in memory.

### AC: retention-and-reset

**Requirements:** peer-connectivity#req:bounded-retention, peer-connectivity#req:latest-known-heads, peer-connectivity#req:stale-cursor-reset, peer-connectivity#req:reconcile-command

After acknowledgement, no poll receipt for that cursor remains. Markers older
than 14 days are pruned. A queue pushed past its bound is dropped and the peer
reconnects into a reset that enqueues sync jobs only for repositories whose
local origin ref differs from the hub head. `wb peers reconcile --dry-run`
lists the same repositories and changes nothing.

### AC: selective-sync

**Requirements:** peer-connectivity#req:sync-only-named-repositories, peer-connectivity#req:canonical-event-envelope

With 20 local canonical clones backed by bare remotes, events for A, C and
A again produce fetch activity (observed through a git wrapper) for A and C
only, and A's two events coalesce into one sync. A dirty clone of C is left
byte-identical and reported. Envelope tests reject an unknown type, a missing
or duplicated payload field, and any field outside the privacy allowlist.

### AC: counters-and-rates

**Requirements:** peer-connectivity#req:traffic-and-event-counters, peer-connectivity#req:minute-buckets-and-rates

With an injected clock, known message sizes give exact per-session and
lifetime RX and TX payload bytes, messages, events and events by type on both
ends. Bucket rollover over 61 minutes keeps exactly 60 buckets. 1m, 5m and 1h
throughput equals hand-computed values, including after an idle gap and with
fewer than 60 minutes of history. Lifetime totals survive a restart and
buckets do not.

### AC: api-and-dashboard

**Requirements:** peer-connectivity#req:peers-api, peer-connectivity#req:peers-dashboard, peer-connectivity#req:peer-narration

API responses never contain a token, digest or full node ID (asserted by
scanning every response in the test suite). Dashboard component tests render
the overview and a detail page from a fixture with two charts and the table
fallback, and hide trust buttons when the fixture marks the view as proxied.
Narration tests assert one line per lifecycle fact and none for heartbeats.

### AC: whole-journey-e2e

**Requirements:** peer-connectivity#req:outbound-websocket-session, peer-connectivity#req:persist-then-notify, peer-connectivity#req:stale-cursor-reset, peer-connectivity#req:sync-only-named-repositories, peer-connectivity#req:peer-management-commands, peer-connectivity#req:peers-api

One end-to-end test runs a hub daemon and a downstream daemon in-process, with
signed fake webhook deliveries and local bare remotes. It walks the
canonical scenario with no step prodding the next:

1. The downstream node is offline.
2. Pushes arrive for A, C, then A.
3. The hub persists them.
4. The downstream node starts and authenticates.
5. It resumes from its cursor.
6. Only A and C are reconciled.
7. Only A and C are fetched.
8. Lag reaches 0.
9. A push to F arrives while the downstream node is connected.
10. F is received with no poll interval elapsed.
11. F is processed.
12. `peers list/get` show the connection, cursor, lag and counters.
13. The peers API reports the buckets.
14. `disconnect` is followed by a push and an automatic reconnect.
15. Catch-up finishes with no loss.
16. `block` refuses the next dial, and `unblock` restores it and catches up.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
