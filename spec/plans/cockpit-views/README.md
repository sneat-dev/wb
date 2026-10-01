---
format: https://specscore.md/plan-specification
status: Approved
---
# Plan: Cockpit Views

**Status:** Approved
**Source Feature:** cockpit-views
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** wb-cockpit

## Summary

Implement [cockpit-views](../../features/cockpit-views/README.md): the Cockpit
redesign for a dispatcher, organised by jobs to be done, with the task as the
primary object and the control surface in which the actions of `cockpit-actions`
live. Schema version 2 of the fleet read model with compression, lazy branches and
new fields, machine metrics (local, live remote and cached), pull request state and
checks, agent activity, periodic remote publish with agents and metrics, and
landed-task throughput; a `fleet-data` library with the matcher, filter vocabulary,
task lifecycle and view model; a shell with palette and side panel; and Home, Tasks,
Repositories, Worktrees, Agents and Machines pages.

Approval: approved by delegation, the founder said "work autonomously" on
2026-10-01.

## Journey

The operator opens the Cockpit and sees on Home what is blocked on them, what is
ready to land and what is in flight on every machine. They expand nothing: they
select a row and read its panel, press `/` and filter with a wildcard, choose "Land
task" (or copy its command when they hold no owner session), start new work from
"New task", and check a machine's load before dispatching another agent. Every view
is in the address, and nothing renders more than 60 rows.

## Approach

Twenty tasks. Nine Go tasks change the contract and run mostly serially, because they all touch
the read model and the shared API writer: task 1 (transport 1a and contract 1b, deliverable on
one branch in two commits), then the metrics sampler (2), pull request state (3), agent activity
(4), periodic publish (5) and throughput (6). The live remote route is three tasks behind one
`RemoteExporter` interface: task 7 adds the read-only `wb cockpit export` verb and its envelope
(needs tasks 1 and 2), task 8 the default HTTP transport with its hub route, client, strict decode,
scheduler and merge (needs task 7), and task 9 the SSH fallback, cool-down, typed errors and SSH
copy commands (needs task 8). They share the read model with tasks 3 to 6, so whichever lands
later rebases. Periodic publish (task 5) stays as the opt-in fallback for machines with neither
route.

Task 1 touches nothing under `cockpit/web/libs/fleet-data` or `cockpit/web/apps`; in `cockpit/web`
it owns only the build-time pre-compression in `tools/finish-build.mjs` and `embed.go`. The
real-daemon journey test is expected to be red on `cockpit-ux` after task 1 until the UI
foundation tasks (11 to 13) land.

Frontend: task 10 (the `fleet-data` library) has no code dependency on the Go tasks and works from
the contract in the Feature. The UI foundation is three tasks: 11 (shell, palette, shortcuts, the
metrics polling service, all routes pre-registered, the JavaScript-only bundle budget), 12 (virtual
list, page-address state, side panel) and 13 (control-surface components, tokens, Chart.js
wrapper); 12 and 13 need 11. Pages 14 to 19 need 12 and 13 (18 also needs the metrics contract of
task 2), run in parallel, and do not edit `app.routes.ts`, the tab list or the shared fixtures,
which tasks 10 and 11 own. Task 20 needs all and closes the plan.

All tasks land on the integration branch `cockpit-ux`; `main` receives milestone pull requests when
the branch is coherent, the first after tasks 1, 2 and 10 to 19 at the earliest, so `main` is never
between contract versions. A task updates the existing e2e and journey tests its change breaks, in
the same task, except the real-daemon journey described above. No test may use a real SSH or HTTP
connection to another machine.

The Cockpit is a control panel, and the views are where its actions live: task 13 owns the action
slot (one per pull request or worktree), the "Copy command" component and the owner-gating
affordance as empty-capable components with fixtures for a fake registry, so they render nothing
until a registry exists; pages 14 to 18 place them, and the per-entity "Copy command" lists are
written in them (worktree, task, pull request, branch in 15; repository in 16; dispatched run and
session in 17; Home rows in 14). The sub-plans `work-loss-risk` and `cockpit-actions` follow this
plan; `cockpit-actions` Task 8 ("Action controls in the application") fills the slots and owns
operation feedback and the palette's action results, and its description was adjusted to say so.
This plan executes no action: every execution path is `cockpit-actions`. There is no generic
selection or bulk bar.

Every task keeps the code it adds at 100% coverage: Go through `wb coverage --changed`, and
`cockpit/web` through thresholds of 100 for statements, branches, functions and lines plus a
rendering test for every component.

## Tasks

### Task 1: Backend: transport (1a) and contract (1b), schema version 2

**Id:** task-1
**Verifies:** cockpit-views#ac:responses-are-gzip-with-etag, cockpit-views#ac:gzip-bytes-are-computed-once, cockpit-views#ac:static-assets-are-precompressed-and-immutable, cockpit-views#ac:hosted-origin-can-revalidate, cockpit-views#ac:branches-leave-the-document, cockpit-views#ac:repository-entries-carry-activity-and-web-url, cockpit-views#ac:hostile-host-has-no-web-link, cockpit-views#ac:owner-state-mapping, cockpit-views#ac:worktree-entries-carry-name-and-sync, cockpit-views#ac:machine-entries-carry-hardware-and-no-metrics, cockpit-views#ac:document-carries-refresh-interval, cockpit-views#ac:fleet-document-fits-the-budget, cockpit-views#ac:field-tables-hold-in-the-document, cockpit-views#ac:cached-repository-names-are-split-into-host-and-name
**Depends-On:** —
**Status:** complete

Two parts that may be delivered on one branch in separate commits. **1a, transport:** gzip in the shared API writer (encoding-specific strong ETags, `Vary: Origin, Accept-Encoding`, gzip bytes computed once when the snapshot is stored), build-time pre-compression of the hashed static assets with `immutable` caching and a per-response compressed index, and `ETag` exposed and `If-None-Match` allowed for the hosted origin on the routes that exist now (the fleet document and the branches route; the metrics route's clauses are verified in task 2). **1b, contract:** `schema_version` 2 (the daemon half of REQ:schema-version-2; the client half is task 10); remove `branches` from the document and add `GET /api/v1/cockpit/branches?repository=<id>` (200 with an empty list and a reason for a cached repository, 404 for an unknown id, no Git on the request path); the field tables of REQ:field-tables, without renaming any existing field: `name` as `owner/name` with `host` separate for every entry (a snapshot name is split when it has three or more segments and the first contains a dot), `last_activity_at` and the validated `remote_url_web` for local repositories only, worktree `name` equal to the task, local-only `ahead`, `behind`, `upstream_gone` and the new `has_upstream`, the machine fields `os`, `arch`, `cpu_count`, `boot_time` and `refresh_interval_seconds`. Implement REQ:owner-state-vocabulary: the owner-process-liveness derivation (`worktreeclaims.WorktreeOwnerState`) for local worktrees, cached per snapshot, normalised to the four values or omitted on every route with out-of-set remote values dropped, and no heartbeat fallback; report the measured cost. Populate a local worktree's `lifecycle` only if the claim or record already holds it, otherwise leave it absent and report that the second arm of row 7 of REQ:task-state is dropped. Build the 500/600/4,000/3 fixture with realistic names and `code_index` statistics. Extend the sentinel test that proves no path, origin URL or credential reaches the document. Update the existing Go tests that this contract change breaks in this task. This task touches nothing under `cockpit/web/libs/fleet-data` or `cockpit/web/apps`; in `cockpit/web` it owns only build-time pre-compression in `tools/finish-build.mjs` and `embed.go`, and task 11 later adds the JavaScript-only bundle budget to the same script. The real-daemon journey test is expected to be red on `cockpit-ux` until the UI foundation tasks (11 to 13) land, because the page still reads schema version 1; the stubbed Playwright suite is updated here only where it breaks.

The real-daemon journey (`apps/cockpit-e2e/src/journey`, Linux CI only) is expected to be red on `cockpit-ux` from this task until the frontend tasks 10 and 11 land, because the shipped client requires schema 1 and a `branches` collection; this task does not touch `cockpit/web/libs/fleet-data` or `cockpit/web/apps`, and the stubbed web tests are unaffected.

Verification (allmust pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 2: Backend: machine metrics sampler and route

**Id:** task-2
**Verifies:** cockpit-views#ac:sampler-fills-a-ring-buffer, cockpit-views#ac:metrics-route-serves-each-source, cockpit-views#ac:metrics-route-is-compressed-and-revalidatable
**Depends-On:** 1
**Status:** complete

Add the sampler: every 10 seconds, off the request path, CPU percent, one-minute load, memory used and total, and free and total disk of the projects root into a 360-sample in-memory ring buffer through an injectable source and clock, with a Windows build that reports metrics as unsupported. Add `GET /api/v1/cockpit/machine-metrics?machine=<id>` returning `{machine, route, fetched_at?, samples, reason?}` with the `local` and `none` sources, 404 for an unknown id, and the hosted-origin conditional-request headers of task 1; the `live-remote` and `cached` sources are filled by tasks 5 and 8 behind the same payload. Compression and the hosted-origin conditional-request headers of task 1 apply to this route and are verified here. The Go tasks 1 to 6 run serially because they all touch the read model and the shared API writer; the sentinel test is extended in each.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 3: Backend: pull request state and checks on the snapshot ticker

**Id:** task-3
**Verifies:** cockpit-views#ac:pull-request-entries-carry-state-and-checks, cockpit-views#ac:pull-request-state-absent-until-observed, cockpit-views#ac:pull-request-strings-are-hostile-safe
**Depends-On:** 2
**Status:** complete

Run the existing watcher (`internal/prwatch` over `internal/prsnapshot.Observe`) from the snapshotter's ticker for the pull requests `worktrees.ListRegisteredPullRequestBindings` returns, with the credentials WB already uses. Map the observation to the pull request entry: `state` (open, merged, closed, draft from `State`, `Merged` and `Draft`), `mergeable`, `checks_total`, `checks_passed`, `checks_failed` (fail plus cancel), `checks_skipped` (counted separately from passed), `checks_pending`, `checks_green` (the observation's `Green`, never re-derived), `failed_check` (first of `Failed`) and `checked_at`, with `mergeable` and `state` as closed enums, `url` only when `https` with a plain hostname, and `failed_check` capped at 100 characters with control and bidirectional characters removed. A merged pull request leaves the watch set after one confirmed observation. Add the config value `cockpit.pull_request_limit`, observe at most that many per tick oldest `checked_at` first, keep the previous values and `checked_at` when an observation fails, and omit the fields until one has succeeded. No request reads GitHub. Add the new fields to the sentinel test and to the `cockpit` closed field list check.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 4: Backend: agent activity from herdr and run fields

**Id:** task-4
**Verifies:** cockpit-views#ac:agent-activity-joins-herdr-by-session, cockpit-views#ac:agent-entries-carry-run-links
**Depends-On:** 3
**Status:** planning

Once per refresh, when herdr is available, list its agents (`herdr.Client.AgentList`) and join each to a registered session by the harness session id, setting `activity` to the herdr status (`working`, `blocked`, `idle`, `done`, `unknown`) and omitting it with no herdr or no match; never read screen text. Populate `worktrees`, `task`, `repository`, `started_at` and the exit code for dispatched runs from the run record (`agents.Result`), and for a session only `started_at` plus the worktree and task a worktree's owner or claim names. Never emit a run's free-text failure.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 5: Backend: periodic remote publish with agents, metrics and hardware

**Id:** task-5
**Verifies:** cockpit-views#ac:periodic-publish-runs-after-a-local-scan, cockpit-views#ac:remote-snapshot-carries-optional-agents-and-metrics, cockpit-views#ac:remote-agents-are-capped
**Depends-On:** 4
**Status:** planning

Nothing publishes `os`, `arch`, `cpu_count` or `boot_time` yet (task 1 added the optional `remotestate.Snapshot` fields and the mapping only), and the hub conversion (`internal/remotestate/hub/conversion.go`) and `api/githubapp/machinesnapshot.Snapshot` drop or refuse them: the cached half of `machine-entries-carry-hardware-and-no-metrics` is proven only at the mapping level until this task fills them in a publisher and in the hub model. Publish this machine's snapshot from the daemon after a successful local scan by the existing `wb remote publish` path, only when `remote.publish.interval` is set (opt-in, minimum 5 minutes), retrying at the next interval and never delaying the local snapshot. Publish agents (at most 200) only with `remote.publish.agents: true` and metrics only with `remote.publish.metrics: true`; a publisher refused with 400 by an older hub retries once without the optional fields and records a diagnostic. Add the optional `agents`, `metrics` and machine hardware fields to `remotestate.Snapshot` without changing `schema_version`, prove an older decoder ignores them, and extend the hub provider's snapshot model (`api/githubapp/machinesnapshot.Snapshot`, decoded with unknown fields refused) in this task to accept and store them. Show another machine's agents in the fleet document as `cached` with the snapshot's age and serve its snapshot sample as the `cached` source of the metrics route. Update `spec/features/remote-state/README.md` behaviour is already specified; this task implements it. Which remote store is the shared one is an open question and does not block.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 6: Backend: landed-task throughput collector

**Id:** task-6
**Verifies:** cockpit-views#ac:throughput-block-from-terminal-records, cockpit-views#ac:throughput-is-omitted-without-timestamps
**Depends-On:** 5
**Status:** complete

Add a collector that reads this machine's sealed terminal records (`worktreeclaims.TerminalRecord`) once, cached by claim identity, and emits the `throughput` block: per-day landed counts and the five slowest claim-to-landed durations over 30 days, counting a task as landed when `worktree_disposition` is `landed`, at `sealed_at`, with the duration from the claim's `recorded_at` (the claim record has no `created_at`). The Work Log retirement archive manifests carry no timestamps and are not read. Confirm the timestamps first; if no record carries both, the block and the Home charts are dropped and the task reports it, with no invented data.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 7: Backend: `wb cockpit export` verb and envelope

**Id:** task-7
**Verifies:** cockpit-views#ac:export-without-a-daemon-fails-and-starts-nothing, cockpit-views#ac:export-carries-only-the-metadata-set
**Depends-On:** 1, 2
**Status:** complete

Add the read-only verb `wb cockpit export --format json` (with `--metrics-only`) to the `wb cockpit` command tree in `cmd/wb/cockpit.go`. It finds the running daemon from the daemon record without starting it (it must not use `cockpitLocalFromDaemon`, which starts a daemon and can mint a login code), reads the fleet document and machine-metrics over the daemon's loopback transport as `anonymous-local`, and prints one envelope `{schema_version, machine, exported_at, fleet, metrics}` bounded at 8 MiB and limited to the anonymous-readable metadata set. With no daemon, or a daemon refusing anonymous reads, it prints `{schema_version, error}` with `daemon_not_running` or `export_refused` and exits 1. Add its `ai/capabilities.json` row, command-coverage entry, Agent Skill coverage in `wb-daemon`, the `docs/cli-flag-matrix.md` line and the persistent-flag support declaration, because a CLI verb is added. Unit tier only: a fake daemon transport; no real daemon and no ssh. It may run in parallel with tasks 3 to 6; all touch the read model, so the later to land rebases and extends the sentinel test.

Verification (allmust pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 8: Backend: HTTP route, HTTP client, strict decode and merge

**Id:** task-8
**Verifies:** cockpit-views#ac:configured-target-appears-live-remote, cockpit-views#ac:hub-export-route-requires-a-machine-bearer, cockpit-views#ac:bearer-stays-with-the-configured-host, cockpit-views#ac:no-route-configured-makes-no-request, cockpit-views#ac:response-machine-name-is-ignored-for-placement, cockpit-views#ac:hostile-payload-is-refused, cockpit-views#ac:metrics-only-export-is-demand-driven, cockpit-views#ac:non-loopback-metrics-request-is-refused, cockpit-views#ac:metrics-route-serves-each-source
**Depends-On:** 7
**Status:** planning

Behind a `RemoteExporter` interface, add the HTTP transport. Serve `GET /v0/workbench/machines/export` (and `?metrics_only=1`) on the hub mount, building the envelope with the function of task 7 in process, served only by a daemon-hosted hub (never the hosted multi-identity service), authenticated only by a machine bearer with the existing scope `machine_snapshot:read` whose identity equals the host owner's, exposing only this machine's own export, (a peer credential, a cookie or no credential is refused with 401; nothing under `/api/v1/cockpit/`). Add the HTTP client: address and credential only from local configuration (the hub client's `remote.url` and `remote.token_file` for the configured hub, otherwise the new optional `http` section with `url` and `token_file` beside `ssh` in `session_move.targets.<machine>`, validated in `internal/sessionmove/config.go` with `remotestate.ValidateHubURL` and without becoming a `Courier`), HTTPS unless loopback, no redirects, 8 MiB cap, 3 second connect and 10 second total timeout, the bearer sent only to the configured host. Add the background scheduler (fleet export once per refresh interval, demand-driven metrics-only every 30 seconds while a client asked within 60 seconds, doubling backoff to 5 minutes), the strict envelope decoder (unknown fields rejected, caps, entries placed by the configured machine key whatever machine the response names, non-finite or negative numbers and future times refused, at most 360 samples), the merge as `route` `live-remote` with `transport`, and the `live-remote`, `cached` and `none` sources of `machine-metrics`; add `cockpit.remote_http`, the opt-out. With neither transport configured nothing is sent. Unit tier only: fake HTTP servers and a fake clock; nothing may call a real host.

Verification (allmust pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 9: Backend: SSH fallback, cool-down, typed errors and copy commands

**Id:** task-9
**Verifies:** cockpit-views#ac:http-failure-falls-back-to-ssh, cockpit-views#ac:fallback-cool-down-is-honoured, cockpit-views#ac:failed-export-shows-a-typed-error, cockpit-views#ac:ssh-argument-vector-contains-only-configured-values, cockpit-views#ac:copy-command-for-an-ssh-machine
**Depends-On:** 8
**Status:** planning

Add the SSH transport behind the same `RemoteExporter`: run `ssh` through `internal/remotessh` (`Resolve`, `Build`, an injectable `Runner`, `NewLimitedBuffer`, `SanitizeDiagnostic`) with an argument vector built from `session_move.targets.<machine>.ssh` only (`agents.LoadRemoteTargets`, `sessionmove.SSHConfig`) to execute `<wb_path> cockpit export --format json` (with `--metrics-only` for metrics), with a 15 second total timeout and the 8 MiB stdout cap, making the `remotessh.Build` connect timeout a parameter (it is fixed at 10 seconds today), and `cockpit.remote_ssh: false` as the opt-out. Implement the fallback rule of REQ:remote-exporter-transports (connection error, timeout, 401, 403, 404, 429, 5xx and redirect fall back; a valid-shaped envelope that fails validation does not), the 5 minute cool-down on SSH before HTTP is tried again, the typed `remote_error` vocabulary and the `transport` field, and the Fleet health data for them. Build the `ssh <user>@<host> <wb_path> <command>` text source for the "Copy command" entries from the same configuration. The source is the session response: add the owner-only `machine_routes` field (`machine_id` and `ssh` with `host`, optional `user` and `wb_path`) to `GET /api/v1/cockpit/session`, emitted only to an owner session and never to `anonymous-local`, with a sentinel test; the `fleet-data` library (Task 10) already reads it and threads it into the copy-command builders. Real-SSH tests are not allowed locally: the unit tier uses a fake `remotessh.Runner`, and nothing in any test may ssh to the founder's VM or any host.

Verification (allmust pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 10: Frontend: fleet-data library

**Id:** task-10
**Verifies:** cockpit-views#ac:matcher-grammar, cockpit-views#ac:matcher-limits-and-bare-fields, cockpit-views#ac:matcher-is-linear-time, cockpit-views#ac:filter-vocabulary-is-the-only-link-target, cockpit-views#ac:client-accepts-only-schema-2, cockpit-views#ac:derived-collections-computed-once, cockpit-views#ac:filtering-5000-rows-is-fast, cockpit-views#ac:unchanged-snapshot-does-nothing, cockpit-views#ac:repository-identity-merges-local-and-cached, cockpit-views#ac:task-state-at-risk, cockpit-views#ac:task-state-checks-failed, cockpit-views#ac:task-state-blocked, cockpit-views#ac:task-state-ready-to-land, cockpit-views#ac:task-state-not-ready, cockpit-views#ac:task-state-working, cockpit-views#ac:task-state-landed, cockpit-views#ac:task-state-idle, cockpit-views#ac:task-state-not-reported, cockpit-views#ac:task-state-is-worst-first, cockpit-views#ac:task-state-ignores-unobserved-pull-requests
**Depends-On:** —
**Status:** complete

In `cockpit/web/libs/fleet-data`: the schema version 2 types and strict client from the field tables of REQ:field-tables (with the two mismatch messages), the pure matcher with its step counter, caps and quoting, the filter vocabulary as data (chips, `state:` values, `sel` keys and sort column ids per page) and its link builders (the only way a link is built), and the memoised view model: repositories merged by lower-cased `owner/name`, tasks with the state decision table of REQ:task-state as a pure function (one per-state test and the unobserved-pull-request rule), the "Needs you" items (one row per task, kinds in rank order, blocked agents without a task), the ready-to-land list and the cleanup counts. Also the fixtures, including the 500/600/4,000 performance fixture, a fake registry and a fake operations route. It has no code dependency on tasks 1 to 9: it works from the contract in this Feature. This task owns the shared fixtures; later frontend tasks do not edit them.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 11: Frontend: shell, palette, shortcuts and metrics polling

**Id:** task-11
**Verifies:** cockpit-views#ac:top-bar-shows-tabs-badges-and-freshness, cockpit-views#ac:home-route-and-alias, cockpit-views#ac:warming-up-shows-progress, cockpit-views#ac:no-heading-repeats-the-tab, cockpit-views#ac:palette-groups-results, cockpit-views#ac:shortcuts-navigate-and-respect-typing, cockpit-views#ac:initial-script-fits-the-budget, cockpit-views#ac:metrics-poll-only-while-visible
**Depends-On:** 10
**Status:** complete

Task 11 proves the shortcut AC (`cockpit-views#ac:shortcuts-navigate-and-respect-typing`) for these clauses: `g` then `w` opens Worktrees, keys typed into an input (the palette's) stay text and no tab switches, `?` shows the sheet, Esc closes the palette and the sheet. The clauses that need a list page, `/` focusing the page filter box with the typed `g w` staying text in it, and Esc closing the side panel, are proven by Task 12, which wires `Shortcuts.registerFilter` and `Shortcuts.registerPanel` (their APIs and unit tests are Task 11's). In `cockpit/web`: the application shell (top bar with signal badges, freshness chip, session chip and "New task" button, the hidden `h1`), the palette over the fleet document (navigation results only; action results are `cockpit-actions`), the keyboard shortcuts, the metrics polling service (every 10 seconds only while Home or a Machines page is visible), all routes pre-registered with placeholder pages so later tasks never edit `app.routes.ts` or the tab list, and the JavaScript-only bundle budget in `tools/finish-build.mjs` (at most 350 kB). It uses task 10's library and fixtures and does not edit them.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 12: Frontend: virtual list, URL state and side panel

**Id:** task-12
**Verifies:** cockpit-views#ac:filter-state-lives-in-the-address, cockpit-views#ac:rows-are-one-line-and-virtual, cockpit-views#ac:columns-are-few-and-uniform-ones-hidden, cockpit-views#ac:repository-and-time-rendering, cockpit-views#ac:empty-states-offer-clear, cockpit-views#ac:side-panel-opens-and-closes, cockpit-views#ac:side-panel-shows-summary-actions-commands-and-raw-data, cockpit-views#ac:detail-routes-render-the-same-panel, cockpit-views#ac:copy-buttons-copy-the-full-value, cockpit-views#ac:list-never-exceeds-60-row-elements, cockpit-views#ac:shortcuts-navigate-and-respect-typing
**Depends-On:** 11
**Status:** planning

The shared virtual list (sticky header, sort, chips, machine chips, name and time rendering, auto-hidden columns, empty states, `j`/`k`/Enter/Esc), the page-address state (`q`, `sort`, `dir`, `machine`, `chips`, `sel`) with bad values ignored, the side panel and the one component that renders both the panel and the detail page, with its collapsed "Raw data" block, and the copy buttons. The panel hosts the action slot and "Copy command" components of task 13 through their interfaces and renders nothing for them until they exist. It wires `Shortcuts.registerFilter` and `Shortcuts.registerPanel` (Task 11) on the lists, and so proves the clauses of `cockpit-views#ac:shortcuts-navigate-and-respect-typing` that Task 11 leaves to it: `/` on a list focuses the filter box, a typed `g w` stays text in it and no tab switches, and Esc with the side panel open closes it (an empty focused filter lets Esc through to the panel).

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 13: Frontend: control-surface components, tokens and chart wrapper

**Id:** task-13
**Verifies:** cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it, cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers, cockpit-views#ac:copy-command-templates-match-the-manifest, cockpit-views#ac:copy-command-refuses-hostile-values, cockpit-views#ac:owner-gating-is-one-affordance, cockpit-views#ac:intent-to-done-budgets-hold, cockpit-views#ac:chart-library-is-pinned-and-tree-shaken
**Depends-On:** 11
**Status:** complete

The control-surface components as empty-capable pieces driven by task 10's fake registry: the action slot (one per pull request or worktree; its direct-button and overflow choices follow `cockpit-actions`), the "Copy command" component with POSIX single-quoting, `--flag=value` and the refusal of control characters and leading dashes, and a unit test that parses every command template against `ai/capabilities.json`; the owner-gating affordance. Also the design tokens (typography, state colours with icons, light and dark) and the exact-pinned tree-shaken Chart.js wrapper as a lazy chunk. Nothing here executes an action; operation feedback and palette action results are `cockpit-actions` Task 8.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 14: Frontend: Home

**Id:** task-14
**Verifies:** cockpit-views#ac:needs-you-pr-checks-failed, cockpit-views#ac:needs-you-agent-blocked, cockpit-views#ac:needs-you-run-failed, cockpit-views#ac:needs-you-work-at-risk, cockpit-views#ac:needs-you-pr-needs-you, cockpit-views#ac:needs-you-agent-finished, cockpit-views#ac:needs-you-is-capped-and-empty-line, cockpit-views#ac:ready-to-land-groups-by-task, cockpit-views#ac:in-flight-lists-agents-on-every-machine, cockpit-views#ac:in-flight-machine-load-indicator, cockpit-views#ac:resume-lists-five-recent-tasks, cockpit-views#ac:cleanup-line-counts-and-chart, cockpit-views#ac:fleet-health-only-when-not-ok, cockpit-views#ac:home-charts-from-throughput, cockpit-views#ac:home-phone-layout, cockpit-views#ac:csp-and-canvas-only
**Depends-On:** 12, 13
**Status:** planning

Build Home: "Needs you" (one row per task, at most five, one primary action each, "+n more", blocked agents without a task, the empty line), "Ready to land" with its per-pull-request slots or copy commands and the age of the observation, "In flight" with machine chips and the load indicator, "Resume", the "Cleanup" line with the worktree-age chart, the "Fleet health" line, the two non-linking throughput charts hidden on a phone, and the phone layout. It uses only task 10's view model and tasks 12 and 13's components, against stubs; it does not edit the routes, tabs or shared fixtures.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 15: Frontend: Tasks and Worktrees

**Id:** task-15
**Verifies:** cockpit-views#ac:tasks-list-aggregates-worktrees, cockpit-views#ac:task-detail-shows-its-entities, cockpit-views#ac:worktree-identity-cell, cockpit-views#ac:worktrees-columns-and-badges, cockpit-views#ac:worktrees-quick-filters, cockpit-views#ac:default-sorts
**Depends-On:** 12, 13
**Status:** planning

Build the Tasks page (state badge column, chips) with the task panel and `/tasks/detail?task=<name>`, and the Worktrees page (identity cell, conditional Branch column, sync badges for this machine, chips including `safe` and `look`) with its panel. Place the action slots and the "Copy command" lists for worktree, task, pull request and branch (the commands of REQ:copy-the-command). These pages do not edit the routes, tabs or shared fixtures.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 16: Frontend: Repositories

**Id:** task-16
**Verifies:** cockpit-views#ac:repositories-merge-across-machines, cockpit-views#ac:repository-actions-follow-configuration, cockpit-views#ac:repositories-sort-presets, cockpit-views#ac:repositories-quick-filters, cockpit-views#ac:repository-detail-loads-branches-lazily, cockpit-views#ac:default-sorts
**Depends-On:** 12, 13
**Status:** planning

Build the Repositories page (merged per identity, machine chips with cached age and stale mark, icon actions, the Recent, Most worktrees and Most branches sort presets, chips including `index` and `errors`), the repository panel and `/repositories/:host/:owner/:name` with lazily loaded branches from task 1's route (stubbed), and the repository "Copy command" list. Detail pages are lazy chunks; no edit to the routes, tabs or shared fixtures.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 17: Frontend: Agents

**Id:** task-17
**Verifies:** cockpit-views#ac:agents-list-describes-the-work, cockpit-views#ac:agent-label-fallback, cockpit-views#ac:agent-detail-links-its-work, cockpit-views#ac:default-sorts
**Depends-On:** 12, 13
**Status:** planning

Build the Agents page (human labels, activity badge or "state not reported", cached agents with age and no action, the dispatched-run and session "Copy command" entries), and the agent panel and `/agents/:id`.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 18: Frontend: Machines with metrics

**Id:** task-18
**Verifies:** cockpit-views#ac:machines-table-title-and-links, cockpit-views#ac:machines-filter-and-stale-chip, cockpit-views#ac:machine-detail-metrics-charts, cockpit-views#ac:machine-without-metrics-says-so, cockpit-views#ac:default-sorts
**Depends-On:** 12, 13, 2
**Status:** planning

Build the Machines page (title column, filter box, `stale` and `outdated` chips, state age, version mark, CPU and Memory with route and age) and the machine panel and `/machines/:id` with its four last-hour charts from `GET /api/v1/cockpit/machine-metrics` through task 11's polling service, with the `local`, `live-remote`, `cached` and `none` states from the task 2 contract (stubbed).

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 19: Frontend: New task form

**Id:** task-19
**Verifies:** cockpit-views#ac:new-task-form-produces-commands
**Depends-On:** 12, 13
**Status:** planning

Build the "New task" form: the repository picker (wildcard matcher, only names matching `[A-Za-z0-9._-]+/[A-Za-z0-9._-]+`), task name, optional base branch, required model, and the two commands it produces (`wb worktree create` with `--model` and `--original-prompt-file`, and `wb agent dispatch`), with the quoting and refusal rules of task 13. It runs nothing.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 20: Integration: journey, budgets, accessibility, phone, docs and manifests

**Id:** task-20
**Verifies:** cockpit-views#ac:usable-at-360-wide, cockpit-views#ac:no-layout-shift-on-arrival, cockpit-views#ac:state-is-never-colour-only, cockpit-views#ac:views-coverage-gates-hold, cockpit-views#ac:every-number-is-a-link, cockpit-views#ac:every-tab-lists-its-collection
**Depends-On:** 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19
**Status:** planning

Update the stubbed Playwright suite and the Linux-CI real-daemon journey for the new pages and routes (the journey is expected red from task 1 until the UI foundation tasks land and is made green here at the latest; existing e2e tests are otherwise updated by the task that breaks them); assert the budgets on the fixture end to end; run the accessibility pass (focus order, `aria-current`, chart text alternatives, contrast in both themes, 360 px width and the phone Home, cumulative layout shift under 0.01); audit the enumerated count cells of REQ:every-number-is-a-link; and update the docs, command manifests and Agent Skill text that describe the Cockpit pages. Confirm every component has a rendering test and the thresholds still hold.

Verification (allmust pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally. Add the Go gates of the Go tasks for any Go file this task touches.

## Open Questions

- Which remote store is the fleet's shared one (the Mac reads the git store, the VM
  publishes to its own hub) is undecided; periodic publish uses what each machine has
  configured, as the fallback for machines without an SSH route.
- The PrimeUI licence key is a pending founder decision outside this plan.
- Whether Stop and Reply for hand-started sessions should be built on herdr prompts is
  undecided.
- Whether the owner process liveness of every local worktree is cheap enough for the
  snapshot is for task 1 to measure.
- A launchd or systemd daemon may have no SSH agent socket, so the key for a
  `session_move` target must work non-interactively; `auth_failed` covers it.

---
*This document follows the https://specscore.md/plan-specification*
