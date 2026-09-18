---
format: https://specscore.md/decision-specification
status: Draft
---

# Decision: Peers connect over a WebSocket session on the hub's existing event journal

**Status:** Draft
**Date:** 2026-09-18
**Owner:** alex
**Tags:** peers,transport,websocket,journal,hub
**Source Idea:** —
**Supersedes:** —
**Superseded By:** —

## Context

The founder asked for a laptop WB to connect outbound to an always-on WB hub on
a VM, resume from a durable cursor, get live GitHub-derived events, and fetch
only the repositories that changed. The brief prefers a WebSocket and allows
an alternative ("I have nothing against them"). It asks for a comparison with
SSE, gRPC streaming, polling and brokers, and for the smallest robust
foundation.

Known at decision time (repository survey, 2026-09-18):

- The hub already owns a durable journal. It has a global monotonic sequence,
  a per-machine queue fanned out in the enqueue transaction, a per-machine
  acknowledged sequence, ID deduplication markers, and idempotent
  acknowledgement by exact cursor and ID set. The daemon side already enqueues
  durably before acknowledging, persists pending acknowledgements, coalesces
  per repository and ref, and syncs one repository at a time through the safe
  fast-forward path.
- Delivery today is an HTTP long poll (wait ≤ 30 s) that the hub serves by
  re-reading the whole per-machine queue every 250 ms. A connected machine has
  no identity as "connected".
- The repository has no WebSocket library. It has an unused SSE endpoint in
  `api/githubapp` and a connect-go service with unary RPCs on a local socket.
- The VM fronts the hub with Caddy, which proxies WebSocket upgrades and SSE
  transparently.
- The daemon has no structured logger. Operator lines go through `hub/narrate`.

## Decision

A downstream node holds one outbound WebSocket session
(`wss://<hub>/v0/workbench/peers/connect`, peer-token authenticated, JSON text
frames). It carries a pull-shaped protocol: a long poll over the socket, where
the node sends `poll{cursor}` and the hub answers as soon as events exist. The
session is served from the hub's existing repository-event store through the
same `Poll` and `Acknowledge` operations as the HTTP long poll. On the laptop,
the existing `Receiver` drives it, extended only with a reset hook.

After each committed enqueue, the hub signals every live subscription of the
affected peers. Each session holds its own subscription, and there is no
per-peer memory queue. The hub coalesces a peer's queued default-branch work
per repository and ref, so a queue is bounded by repositories rather than by
time offline. The store gains a journal ID (epoch), a latest-heads record per
repository, and idempotent acknowledgement at or below the acknowledged
sequence.

A reset reconciles from the heads instead of replaying history. The local
processor always runs `git fetch` for a named repository and fast-forwards
only a clean canonical checkout on the branch.

The library is `github.com/coder/websocket`. It has no transitive
dependencies, supports `context`, handles ping/pong and close codes, and
exposes a read limit.

## Rationale

The correctness problem is already solved by the store and the receiver. What
is missing is a connection that the operator can see and control, plus
immediacy and observability. A WebSocket is the one option where the
acknowledgement travels on the same connection as the events. That gives
four things at once, with no correlation layer:

- "connected" is a fact of that connection;
- `disconnect` and `block` take effect the moment the hub closes it;
- heartbeats are native ping/pong;
- RX and TX counters describe one session.

One small, dependency-free library is the whole price.

The protocol is pull-shaped, a long poll over the socket, because that keeps
the existing `Receiver` loop, its cursor file and its pending-acknowledgement
replay intact. That code already carries the durability guarantees and their
tests. A push-shaped protocol would have needed a second receiver with the
same guarantees rebuilt. The pull costs nothing in latency: the hub holds the
`poll` open and answers the moment a wake-up arrives.

Reusing the store rather than adding a second journal means the two transports
cannot disagree about what was delivered. Coalescing in the enqueue
transaction turns the per-peer queue into the "compacted sync work" of the
brief. The deduplication markers, kept for 14 days, become the "detailed
history". The heads record becomes the "latest known state". This is the
brief's three-way split with one new collection.

Fetch-always, fast-forward-when-safe answers the brief's "`git fetch` plus
WB-state refresh may be safer than mutating checked-out branches; decide
explicitly". A fetch never touches a working tree, index or local branch, and
it keeps `origin/<default>` truthful even for a dirty or diverged clone.
Without it, every later reset would flag that clone again. The fast-forward is
kept for the clean, on-branch, strictly-behind canonical checkout because
decision 0002 already chose it and it is what makes the laptop feel current;
nothing else is mutated.

## Declined Alternatives

### Keep the HTTP long poll and add a wake-up signal

This is the cheapest option: zero new dependencies, and it already works
through Caddy with near-immediate delivery once the 250 ms scan becomes a
wake-up. It lost because there is no connection for the operator to
see or control. "Connected" would be inferred from the last poll, and
`disconnect` would take effect only at the next poll (up to 30 s). Traffic
would be split across many short requests. Heartbeat, supersede and
node-mismatch handling would each need their own bookkeeping. The wake-up
signal from this option is kept, and it also speeds up the long poll.

### SSE stream with acknowledgements over separate POSTs

This option is plain HTTP, and the repository already has an SSE endpoint to
copy. It lost narrowly. Acknowledgements arrive on other connections through
the proxy, so session identity, per-session counters and "which session
acknowledged this" need a session-ID correlation layer. A client-side
heartbeat also has no native channel. The code saved by avoiding a WebSocket
library is spent on that correlation layer.

### gRPC or connect-go bidirectional streaming

The repository already uses connect-go. It lost because connect-go's
bidirectional streams need HTTP/2 end to end, which means h2c between Caddy
and the loopback daemon. It would also bring a protobuf contract for a JSON
product surface and harder debugging with curl-like tools, and it offers no
benefit over a WebSocket for one stream per peer.

### An external broker (NATS, Redis streams, a hosted queue)

It lost immediately. It is new infrastructure to run on every self-hosted
hub, and it would duplicate the durable store the hub already has.

### A new append-only global journal with per-peer cursors

This is the textbook shape from the brief. It lost because the hub already
fans out per machine in the same transaction that assigns the global sequence,
and deletes acknowledged work. A second journal would double the write path
and make the two delivery paths diverge. Instead the coalesced per-peer queue
is the "compacted sync work", the heads record is the "latest known state",
and the 14-day deduplication markers are the "detailed history". A read API
over that history waits until a consumer needs one.

### Unlimited retention for offline peers

It lost because an offline peer would hold storage hostage. Hub-side
coalescing bounds a queue by repositories. A hard bound of 10,000 documents
remains as a safety net, and past it the queue is dropped and the peer
reconciles from heads.

### A push-shaped session protocol

In a push-shaped protocol the hub sends batches unasked and the node
acknowledges them. It lost in adversarial review:

- the existing `Receiver` could not drive it, because its cursor validation
  expects the response cursor to equal the request cursor;
- an enqueue failure would stall the session until a slow-peer timeout;
- a reset would have to rewrite receiver state from a second goroutine.

### Admin authorised by "loopback and no forwarding header"

It lost in adversarial review. SSH reverse tunnels, socat and a proxy that
strips headers all deliver to the loopback listener without forwarding
headers, and a browser can POST to 127.0.0.1 across origins. Admin requires
the owner credential instead: the unix-socket RPC for the CLI, and a
single-use-code session cookie for the dashboard.

### Fetch-only, never fast-forward

This is the safest reading of the brief. It lost narrowly. It would reverse
decision 0002's fast-forward of clean canonical clones, and every canonical
checkout would then fall behind by default. The chosen rule adds the fetch the
old path lacked and keeps the fast-forward only where it cannot lose work.

## Consequences at Decision Time

Expected positive:

- delivery to a connected peer within one store read of the webhook commit;
- one delivery semantics for both transports;
- no unbounded storage or memory per peer;
- "connected", `disconnect` and `block` are immediate and visible;
- the laptop needs no `remote:` change and keeps git-backed claims.

Expected negative:

- a new dependency (`coder/websocket`) under the repository's coverage floor;
- the hub proxy needs a route for `/v0/workbench/peers/connect` without basic
  auth (the daemon authenticates it);
- minute buckets are lost on restart;
- a reset trusts the hub's head knowledge, so a repository the hub never saw
  an event for is left alone until the next event or a manual `wb sync`;
- coalescing adds a per-peer index read to the enqueue transaction, which
  matters on inGitDB's single-writer lock at large fan-out;
- webhooks missed while the hub is down need an explicit redelivery sweep
  through the App API, because GitHub does not redeliver on its own.

## Observed Consequences

None observed yet.

## Affected Features

- [Peer connectivity](../features/peer-connectivity/README.md)
- [GitHub App Repository Events](../features/github-app-repository-events/README.md)
- [Self-hosted bench](../features/self-hosted-bench/README.md)

---
*This document follows the https://specscore.md/decision-specification*
