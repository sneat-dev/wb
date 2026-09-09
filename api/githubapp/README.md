# Workbench GitHub App control-plane contract

`github.com/sneat-dev/wb/api/githubapp` owns the typed HTTP contract for the
Workbench dashboard at `https://sneat.work/bench`. The service is mounted by
the existing Sneat Go Cloud Run executable at `https://wb-github-app.sneat.dev`.
It is not a separate service.

The host supplies narrow ports:

1. `ReadModel`, which records a repository's explicit public opt-in (including
   its README-linked free-eligibility declaration) before it returns anonymous
   data. Private subjects require an authenticated member and are rendered as
   `404` for every other caller.
2. `WorktreeReadModel`, whose remote-state provider requires an authenticated
   member plus `MachineAccessResolver` approval for every exact publisher.
3. `DeliveryStore`, backed by durable storage, which atomically claims GitHub
   delivery IDs and persists coalesced wakeups.
4. `AuthoritativeReader`, which refreshes GitHub App state before a webhook can
   enqueue work. Cached data is never enough to authorize an action.
5. `MachinePublisherResolver`, which resolves an authenticated daemon
   credential to one exact login/machine pair, and a durable
   `hub.SnapshotStore`, which atomically retains the latest validated record for
   that pair.

The API provides the dashboard summary, repository/organization/user stats,
time series usable as tables or graphs, leaderboards, and latest merges with
pull request, issue, merge commit, release, and Workbench receipt URLs.
The stats route uses a remainder wildcard so canonical IDs such as
`github.com/acme/app` round-trip without dropping path segments.

`GET /v0/workbench/worktrees` returns one private row per published worktree on
the exact machines authorized for the viewer. Query parameters are `machine`,
`repository`, `status`, `stream`, `task`, and boolean `needs_attention`.
`status` matches the displayed combined status, lifecycle, or owner status so
operators can select `attention`, `review`, `merged`, `active`, or `orphaned`
without knowing which underlying state supplied it. Rows include the machine,
repository, task and stream, branch, lifecycle and owner state, optional pull
request, publish and last-activity times, and an attention reason. They never
include the snapshot's local path, projects root, prompt, or commit subject.
Each row also repeats the machine's effective heartbeat and staleness. An
offline machine's last authorized rows remain visible; the default stale window
is 24 hours and a provider may configure it without changing stored snapshots.

`POST /v0/workbench/machines/snapshot` accepts only the separate hosted
snapshot schema. The resolver authenticates first, and the request login and
machine must exactly match its result. JSON decoding rejects unknown fields and
the body is capped at 1 MiB. The service validates bounded strings and at most
5,000 worktrees, computes a payload digest, stamps server receipt time, and
calls the store's atomic latest-record operation. Identical retries return the
original receipt without writing; older and same-time conflicting payloads
cannot replace current state. `GET` on the same path returns only snapshots for
the authenticated login.

The hosted model is an allowlist containing the table's repository, task,
stream, branch, lifecycle/owner, pull-request, activity, and attention fields.
It cannot encode local paths, projects roots, commit SHAs or subjects, prompts,
credentials, repository diagnostics, or command output.
`RemoteStateWorktreeReadModel` consumes the public
`machinesnapshot.SnapshotStore` directly and projects rows without importing
or reconstructing any WB CLI remote-state type.
The durable adapter uses
`machinesnapshot.Collection/{machinesnapshot.SnapshotKey(login,machine)}`.
Each document is exactly `machinesnapshot.StoredSnapshot`: the allowlisted
`snapshot` map plus server `received_at` and payload `digest`. The key is a
`machine_`-prefixed SHA-256 of the validated login, a NUL delimiter, and the
validated machine; the source identity remains inside the document for
transaction verification.

`GET /v0/workbench/events` is the default server-to-browser transport. It is
resumable SSE: `after` (or `Last-Event-ID`) replays durable events with strictly
monotonic IDs before the browser receives the live subscription. The source and
service filter private events before serialization. Event types are `queue`,
`job.phase`, `job.progress`, `ci`, `cleanup`, `sync`, and `daemon.generation`.
WebSocket is reserved for later bidirectional controls such as cancellation and
reprioritization.

The sequenced `EventSource` is also the terminal-monitoring source: filter by
`repo`, `task`, `operation`, `session`, `severity`, `after`, and RFC 3339
`since`. `wb monitor --format=jsonl` consumes this same sequence; `wb log tail` can be
an alias, but immutable Work Logs are never used as a mutable event queue.

## Provider storage boundary

The host-neutral provider in `provider.go` implements `ReadModel` over a
`ProjectionStore`; the host supplies the durable adapter. Projection documents
use the `workbench_projections` collection, with `scope` and the canonical
GitHub subject ID (`github.com/<org>` or `github.com/<org>/<repo>`) retained in
the document. Webhook envelopes are normalized to the same
`github.com/<org>/<repo>` form before wakeups or projection refreshes.
`ProjectionKey` derives a stable SHA-256 document ID so slashes
cannot alter storage hierarchy. Series, leaderboard, and latest-merge records
use the corresponding named collections and typed store methods. The public
latest-merges method is intentionally typed to return public-only entries.
Leaderboard documents likewise require `public_only`; private boards are
refused until a subject-scoped board contract exists.

Repository projections are anonymous only when `public_opt_in` is true. For a
private projection the host's `MembershipResolver` must prove that the
Firebase-authenticated `Viewer.UserID` maps to an installed GitHub identity
with access to that exact organization or repository. Browser headers and
GitHub repository visibility are never accepted as membership evidence. A
missing resolver or failed membership check fails closed as private data.

The provider does not choose Firestore paths, Firebase projects, GitHub
credentials, or aggregation credentials. Sneat Go can bind those through its
wire-only adapter once the corresponding durable store and membership service
are configured.

## Authoritative GitHub reader

`GitHubRESTProjectionReader` is the host-neutral REST adapter for webhook
refreshes. The host injects an HTTP transport and installation token source;
the reader parses the canonical repository from the delivery, reads the
repository, counts pull requests and releases through the GitHub API, and
reads the root `README.md` at the exact commit returned by the README commit
query. Only a verified `## WB` or `## Workbench` opt-in produces public
eligibility evidence. A reader implementing `AuthoritativeProjectionReader`
hands the same request-scoped snapshot to the projection engine, so the
freshness barrier and projection build do not issue duplicate GitHub reads.
Organization projections are omitted until a host supplies an
installation-scoped complete aggregation; a public repository count from
`/orgs/{owner}` is not treated as an exact organization summary.

## GitHub App installation tokens

`InstallationTokenSource` is the host-neutral credential boundary used by an
authoritative GitHub reader. The host supplies its numeric GitHub App ID, PEM
private-key bytes, an `http.RoundTripper`, and a clock. `APIBase` is optional
and defaults to `https://api.github.com`; a host can set it for a GitHub
Enterprise API or an isolated transport test.

For each verified `WebhookDelivery`, the source reads only the exact positive
`installation.id` from the JSON payload. It backdates the RS256 GitHub App JWT
by one minute for clock skew, expires it nine minutes after the supplied clock,
and posts `{}` to
`/app/installations/{installation.id}/access_tokens`. The exchange accepts only
GitHub's `201 Created` response with a non-empty, whitespace-safe token and an
`expires_at` later than the same clock reading. Malformed payloads, unsupported
or invalid PKCS1/PKCS8 RSA keys, transport failures, oversized or malformed
responses, unexpected status codes, and expired tokens fail closed. Errors do
not include the PEM key, JWT, token, or response body.

## Firestore adapter schema

The host may bind `FirestoreProjectionStore`, `FirestoreProjectionWriter`, and
`FirestoreProjectionDeliveryStore` through the small `FirestoreBackend` seam.
Projection documents live in `workbench_projections/{ProjectionKey(scope,id)}`;
series and leaderboards use `workbench_series` and `workbench_leaderboards`;
the public merge snapshot is `workbench_latest_merges/public`. Delivery state
uses `workbench_deliveries/{deliveryID}`, and coalesced wakeups use
`workbench_wakeups/{sha256(wakeupKey)}` while retaining the canonical key in
the wakeup body. Delivery claims carry a bounded lease and expire into
retryable work. Hosts supply the actual Firestore client and
transaction implementation; this package contains no Firebase or Firestore
SDK dependency.

The public merge document is a bounded, newest-first aggregate. Each eligible
repository refresh atomically replaces only that repository's contribution and
retains other eligible repositories. A refresh without verified public opt-in
removes the repository's earlier contribution without fetching or persisting
its private merge details.

## Projection delivery boundary

The projector uses a `ProjectionDeliveryStore` claim before refresh. The claim
is atomic across concurrent workers, and `ReleaseDelivery` makes failed
refreshes or writes retryable while preserving the append-only delivery audit.
The writer receives the delivery ID with each repository, organization, and
latest-merge batch and must make those operations idempotent. The final
`CommitDeliveryAndWakeup` call records the terminal delivery and coalesced
wakeup only after all projection writes succeed. A Firestore backend may retry
an atomic callback after a conflict; claim and commit outcomes therefore track
the final callback attempt rather than an aborted predecessor.
