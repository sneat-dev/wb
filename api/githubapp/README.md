# Workbench GitHub App control-plane contract

`github.com/sneat-dev/wb/api/githubapp` owns the typed HTTP contract for the
Workbench dashboard at `https://sneat.work/bench`. The service is mounted by
the existing Sneat Go Cloud Run executable at `https://wb-github-app.sneat.dev`.
It is not a separate service.

The host supplies three narrow ports:

1. `ReadModel`, which records a repository's explicit public opt-in (including
   its README-linked free-eligibility declaration) before it returns anonymous
   data. Private subjects require an authenticated member and are rendered as
   `404` for every other caller.
2. `DeliveryStore`, backed by durable storage, which atomically claims GitHub
   delivery IDs and persists coalesced wakeups.
3. `AuthoritativeReader`, which refreshes GitHub App state before a webhook can
   enqueue work. Cached data is never enough to authorize an action.

The API provides the dashboard summary, repository/organization/user stats,
time series usable as tables or graphs, leaderboards, and latest merges with
pull request, issue, merge commit, release, and Workbench receipt URLs.

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
