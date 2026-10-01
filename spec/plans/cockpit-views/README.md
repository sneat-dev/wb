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

Fourteen tasks. Six Go tasks change the contract and run serially (1 to 6), because
they all touch the read model and the shared API writer, where gzip lives, and each
extends the sentinel test; task 7 (live remote machine metrics) follows tasks 2 and
5 because it needs the metrics route and the publish path. Frontend task 8 (the
`fleet-data` library, "3a") has no code dependency on the Go tasks and works from the
contract in the Feature; task 9 (the UI foundation, "3b") needs it. Tasks 10 to 13
need task 9 (13 also needs the metrics contract of task 2), run in parallel, and do
not edit `app.routes.ts`, the tab list or the shared fixtures, which tasks 8 and 9
own. Task 14 needs all and closes the plan.

All tasks land on the integration branch `cockpit-ux`; `main` receives milestone pull
requests when the branch is coherent, the first after tasks 1, 2 and 8 to 13 at the
earliest, so `main` is never between contract versions. A task updates the existing
e2e and journey tests its change breaks, in the same task.

The Cockpit is a control panel, and the views are where its actions live: task 9 owns
the action slot, the "Copy command" component, the palette's action results, the
owner-gating affordance and the operation indicator as empty-capable components with
fixtures for a fake registry, so they render nothing until a registry exists; tasks 10
to 13 place them, and the per-entity "Copy command" lists are written in tasks 10 to 13
(worktree, task, pull request, branch in 11; repository in 12; dispatched run and
session in 13; Home rows in 10). The sub-plans `work-loss-risk` and `cockpit-actions`
follow this plan; `cockpit-actions` Task 8 ("Action controls in the application") fills
these slots, and its description was adjusted to say so. This plan executes no action:
every execution path is `cockpit-actions`. There is no generic selection or bulk bar.

Every task keeps the code it adds at 100% coverage: Go through `wb coverage --changed`,
and `cockpit/web` through thresholds of 100 for statements, branches, functions and
lines plus a rendering test for every component.

## Tasks

### Task 1: Backend: schema version 2, compression, lazy branches, new fields

**Id:** task-1
**Verifies:** cockpit-views#ac:responses-are-gzip-with-etag, cockpit-views#ac:gzip-bytes-are-computed-once, cockpit-views#ac:static-assets-are-precompressed-and-immutable, cockpit-views#ac:hosted-origin-can-revalidate, cockpit-views#ac:branches-leave-the-document, cockpit-views#ac:repository-entries-carry-activity-and-web-url, cockpit-views#ac:hostile-host-has-no-web-link, cockpit-views#ac:owner-state-mapping, cockpit-views#ac:worktree-entries-carry-name-and-sync, cockpit-views#ac:machine-entries-carry-hardware-and-no-metrics, cockpit-views#ac:document-carries-refresh-interval, cockpit-views#ac:fleet-document-fits-the-budget
**Depends-On:** —
**Status:** planning

Bump the fleet document to `schema_version` 2 (the daemon half of REQ:schema-version-2; the client half is task 8). Put gzip in the shared API writer: encoding-specific strong ETags, `Vary: Origin, Accept-Encoding`, gzip bytes computed once when the snapshot is stored, build-time pre-compressed hashed static assets with `immutable` caching and a per-response compressed index, `ETag` exposed and `If-None-Match` allowed for the hosted origin. Remove `branches` from the document and add `GET /api/v1/cockpit/branches?repository=<id>` (200 with an empty list and a reason for a cached repository, 404 for an unknown id, no Git on the request path). Add the repository fields (`last_activity_at` for local repositories, the validated `remote_url_web`, `host` on cached entries), the worktree fields (`name` equal to the task, local-only `ahead`, `behind`, `upstream_gone`), the local-and-snapshot machine fields (`os`, `arch`, `cpu_count`, `boot_time`) and `refresh_interval_seconds`. Write the `owner_state` mapping of REQ:owner-state-vocabulary, reading the owner process liveness of local worktrees and measuring its cost (fall back to the heartbeat rows if too dear). Build the 500/600/4,000/3 fixture with realistic names and `code_index` statistics. Extend the sentinel test that proves no path, origin URL or credential reaches the document. Update the existing Go and web e2e tests that this contract change breaks in this task.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 2: Backend: machine metrics sampler and route

**Id:** task-2
**Verifies:** cockpit-views#ac:sampler-fills-a-ring-buffer, cockpit-views#ac:metrics-route-serves-each-source
**Depends-On:** 1
**Status:** planning

Add the sampler: every 10 seconds, off the request path, CPU percent, one-minute load, memory used and total, and free and total disk of the projects root into a 360-sample in-memory ring buffer through an injectable source and clock, with a Windows build that reports metrics as unsupported. Add `GET /api/v1/cockpit/machine-metrics?machine=<id>` returning `{machine, route, fetched_at?, samples, reason?}` with the `local` and `none` sources, 404 for an unknown id, and the hosted-origin conditional-request headers of task 1; the `live-remote` and `cached` sources are filled by tasks 5 and 7 behind the same payload. The Go tasks 1 to 6 run serially because they all touch the read model and the shared API writer; the sentinel test is extended in each.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 3: Backend: pull request state and checks on the snapshot ticker

**Id:** task-3
**Verifies:** cockpit-views#ac:pull-request-entries-carry-state-and-checks, cockpit-views#ac:pull-request-state-absent-until-observed
**Depends-On:** 2
**Status:** planning

Run the existing watcher (`internal/prwatch` over `internal/prsnapshot.Observe`) from the snapshotter's ticker for the pull requests `worktrees.ListRegisteredPullRequestBindings` returns, with the credentials WB already uses. Map the observation to the pull request entry: `state` (open, merged, closed, draft from `State`, `Merged` and `Draft`), `mergeable`, `checks_total`, `checks_passed`, `checks_failed` (fail plus cancel), `checks_pending`, `checks_green` (the observation's `Green`, never re-derived), `failed_check` (first of `Failed`) and `checked_at`. Add the config value `cockpit.pull_request_limit`, observe at most that many per tick oldest `checked_at` first, keep the previous values and `checked_at` when an observation fails, and omit the fields until one has succeeded. No request reads GitHub. Add the new fields to the sentinel test and to the `cockpit` closed field list check.

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
**Verifies:** cockpit-views#ac:periodic-publish-runs-after-a-local-scan, cockpit-views#ac:remote-snapshot-carries-optional-agents-and-metrics
**Depends-On:** 4
**Status:** planning

Publish this machine's snapshot from the daemon after a successful local scan by the existing `wb remote publish` path, at `remote.publish.interval` (minimum 5 minutes, default on when a remote store is configured), retrying at the next interval and never delaying the local snapshot. Add the optional `agents`, `metrics` and machine hardware fields to `remotestate.Snapshot` without changing `schema_version`, prove an older decoder ignores them, and extend the hub provider's snapshot model (`api/githubapp/machinesnapshot.Snapshot`, decoded with unknown fields refused) in this task to accept and store them. Show another machine's agents in the fleet document as `cached` with the snapshot's age and serve its snapshot sample as the `cached` source of the metrics route. Update `spec/features/remote-state/README.md` behaviour is already specified; this task implements it. Which remote store is the shared one is an open question and does not block.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 6: Backend: landed-task throughput collector

**Id:** task-6
**Verifies:** cockpit-views#ac:throughput-block-from-terminal-records, cockpit-views#ac:throughput-is-omitted-without-timestamps
**Depends-On:** 5
**Status:** planning

Add a collector that reads this machine's sealed terminal records (`worktreeclaims.TerminalRecord`) once, cached by claim identity, and emits the `throughput` block: per-day landed counts and the five slowest claim-to-landed durations over 30 days, counting a task as landed when `worktree_disposition` is `landed`, at `sealed_at`, with the duration from the claim's `created_at`. The Work Log retirement archive manifests carry no timestamps and are not read. Confirm the timestamps first; if no record carries both, the block and the Home charts are dropped and the task reports it, with no invented data.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 7: Backend: live remote machine metrics

**Id:** task-7
**Verifies:** cockpit-views#ac:hub-metrics-route-requires-a-machine-bearer, cockpit-views#ac:remote-metrics-are-fetched-in-the-background, cockpit-views#ac:unreachable-remote-falls-back-to-the-snapshot, cockpit-views#ac:no-credential-means-no-remote-request, cockpit-views#ac:non-loopback-metrics-request-is-refused, cockpit-views#ac:metrics-route-serves-each-source
**Depends-On:** 2, 5
**Status:** planning

Serve `GET /v0/workbench/machines/metrics` on the hub mount, authenticated by a machine bearer with the existing scope `machine_snapshot:read` and refusing a peer credential, a cookie and an anonymous caller. Add the background fetcher: when `remote.provider` is `hub` with `remote.url` and `remote.token_file`, fetch the hub host's route at most once per 10 seconds with a 3 second timeout and doubling backoff to 5 minutes, cache it with its fetch time, and serve it as `live-remote` through the local `machine-metrics` route, falling back to the snapshot sample and then `none`. Never fetch on a request path, never delay the local snapshot, and make no request without the credential. This is a separate task because it needs a hub route, a client, backoff and tests beyond the sampler; it follows tasks 2 and 5.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 8: Frontend: fleet-data library

**Id:** task-8
**Verifies:** cockpit-views#ac:matcher-grammar, cockpit-views#ac:matcher-limits-and-bare-fields, cockpit-views#ac:matcher-is-linear-time, cockpit-views#ac:filter-vocabulary-is-the-only-link-target, cockpit-views#ac:client-accepts-only-schema-2, cockpit-views#ac:derived-collections-computed-once, cockpit-views#ac:filtering-5000-rows-is-fast, cockpit-views#ac:unchanged-snapshot-does-nothing, cockpit-views#ac:repository-identity-merges-local-and-cached, cockpit-views#ac:lifecycle-at-risk, cockpit-views#ac:lifecycle-checks-failed, cockpit-views#ac:lifecycle-blocked, cockpit-views#ac:lifecycle-ready-to-land, cockpit-views#ac:lifecycle-checks-pending, cockpit-views#ac:lifecycle-working, cockpit-views#ac:lifecycle-landed, cockpit-views#ac:lifecycle-idle, cockpit-views#ac:lifecycle-not-reported, cockpit-views#ac:lifecycle-is-worst-first
**Depends-On:** —
**Status:** planning

In `cockpit/web/libs/fleet-data`: the schema version 2 types and strict client (with the two mismatch messages), the pure matcher with its step counter, caps and quoting, the filter vocabulary as data and its link builders (the only way a link or chart click is built), and the memoised view model: repositories merged by lower-cased `owner/name`, tasks with the lifecycle decision table of REQ:task-lifecycle as a pure function, the "Needs you" items, the ready-to-land list, the cleanup counts and the Home lists. Also the fixtures, including the 500/600/4,000 performance fixture, a fake registry and a fake operations route. It has no code dependency on tasks 1 to 7: it works from the contract in this Feature. This task owns the shared fixtures; later frontend tasks do not edit them.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 9: Frontend: UI foundation and control surface

**Id:** task-9
**Verifies:** cockpit-views#ac:top-bar-shows-tabs-badges-and-freshness, cockpit-views#ac:home-route-and-alias, cockpit-views#ac:warming-up-shows-progress, cockpit-views#ac:no-heading-repeats-the-tab, cockpit-views#ac:palette-groups-results, cockpit-views#ac:palette-lists-actions-after-navigation, cockpit-views#ac:shortcuts-navigate-and-respect-typing, cockpit-views#ac:filter-state-lives-in-the-address, cockpit-views#ac:rows-are-one-line-and-virtual, cockpit-views#ac:columns-are-few-and-uniform-ones-hidden, cockpit-views#ac:repository-and-time-rendering, cockpit-views#ac:empty-states-offer-clear, cockpit-views#ac:every-number-is-a-link, cockpit-views#ac:side-panel-opens-and-closes, cockpit-views#ac:side-panel-shows-summary-actions-commands-and-raw-data, cockpit-views#ac:detail-routes-render-the-same-panel, cockpit-views#ac:copy-buttons-copy-the-full-value, cockpit-views#ac:action-area-renders-the-registry-and-vanishes-without-it, cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers, cockpit-views#ac:owner-gating-is-one-affordance, cockpit-views#ac:operation-indicator-and-updating-row, cockpit-views#ac:intent-to-done-budgets-hold, cockpit-views#ac:initial-script-fits-the-budget, cockpit-views#ac:list-never-exceeds-60-row-elements, cockpit-views#ac:chart-library-is-pinned-and-tree-shaken
**Depends-On:** 8
**Status:** planning

In `cockpit/web`: the shell (top bar with signal badges, freshness, session chip, "New task" button and operation indicator, hidden `h1`, the palette with its action results, keyboard shortcuts), the shared virtual list (sticky header, sort, chips, machine chips, URL state including `sel`, default and auto-hidden columns, `j`/`k`/Enter/Esc) with the side panel and the one component that renders both panel and detail page, and the control-surface components as empty-capable pieces driven by task 8's fake registry: the action slot with its overflow menu, the "Copy command" component taking a per-entity command list, the owner-gating affordance and the operation indicator with the updating row state. Also the tokens (typography, state colours with icons, light and dark), the exact-pinned tree-shaken Chart.js wrapper as a lazy chunk, all routes pre-registered with placeholder pages (so later tasks never edit `app.routes.ts` or the tab list), and the bundle budget check in `tools/finish-build.mjs`. There is no selection model and no bulk bar. Nothing here executes an action: the slots render the registry and open previews as `cockpit-actions` defines.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 10: Frontend: Home

**Id:** task-10
**Verifies:** cockpit-views#ac:needs-you-pr-checks-failed, cockpit-views#ac:needs-you-agent-blocked, cockpit-views#ac:needs-you-run-failed, cockpit-views#ac:needs-you-work-at-risk, cockpit-views#ac:needs-you-is-capped-and-empty-line, cockpit-views#ac:ready-to-land-groups-by-task, cockpit-views#ac:in-flight-lists-agents-on-every-machine, cockpit-views#ac:in-flight-machine-load-indicator, cockpit-views#ac:resume-lists-five-recent-tasks, cockpit-views#ac:cleanup-line-counts-and-chart, cockpit-views#ac:fleet-health-only-when-not-ok, cockpit-views#ac:home-charts-from-throughput, cockpit-views#ac:home-phone-layout, cockpit-views#ac:csp-and-canvas-only
**Depends-On:** 9
**Status:** planning

Build Home: "Needs you" (at most five rows, one primary action each, "+n more", empty line), "Ready to land" with its action slot or copy command, "In flight" with machine chips and the load indicator, "Resume", the "Cleanup" line with the worktree-age chart, the "Fleet health" line, and the two throughput charts hidden on a phone, plus the phone layout. It uses only task 8's view model and task 9's components, against stubs; it does not edit the routes, tabs or shared fixtures.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 11: Frontend: Tasks and Worktrees

**Id:** task-11
**Verifies:** cockpit-views#ac:tasks-list-aggregates-worktrees, cockpit-views#ac:task-detail-shows-its-entities, cockpit-views#ac:worktree-identity-cell, cockpit-views#ac:worktrees-columns-and-badges, cockpit-views#ac:worktrees-quick-filters, cockpit-views#ac:default-sorts, cockpit-views#ac:copy-command-uses-only-existing-commands-and-identifiers
**Depends-On:** 9
**Status:** planning

Build the Tasks page (lifecycle badge column, chips) with the task panel and `/tasks/detail?task=<name>`, and the Worktrees page (identity cell, conditional Branch column, sync badges for this machine, chips including `safe` and `look`) with its panel. Place the action slots and the "Copy command" lists for worktree, task, pull request and branch (the commands of REQ:copy-the-command). These pages do not edit the routes, tabs or shared fixtures.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 12: Frontend: Repositories

**Id:** task-12
**Verifies:** cockpit-views#ac:repositories-merge-across-machines, cockpit-views#ac:repository-actions-follow-configuration, cockpit-views#ac:repositories-sort-presets, cockpit-views#ac:repositories-quick-filters, cockpit-views#ac:repository-detail-loads-branches-lazily, cockpit-views#ac:default-sorts
**Depends-On:** 9
**Status:** planning

Build the Repositories page (merged per identity, machine chips with cached age and stale mark, icon actions, the Recent, Most worktrees and Most branches sort presets, chips including `index` and `errors`), the repository panel and `/repositories/:host/:owner/:name` with lazily loaded branches from task 1's route (stubbed), and the repository "Copy command" list. Detail pages are lazy chunks; no edit to the routes, tabs or shared fixtures.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 13: Frontend: Agents, Machines and New task

**Id:** task-13
**Verifies:** cockpit-views#ac:agents-list-describes-the-work, cockpit-views#ac:agent-label-fallback, cockpit-views#ac:agent-detail-links-its-work, cockpit-views#ac:machines-table-title-and-links, cockpit-views#ac:machines-filter-and-stale-chip, cockpit-views#ac:machine-detail-metrics-charts, cockpit-views#ac:machine-without-metrics-says-so, cockpit-views#ac:metrics-poll-only-while-visible, cockpit-views#ac:new-task-form-produces-commands, cockpit-views#ac:default-sorts
**Depends-On:** 9, 2
**Status:** planning

Build the Agents page (human labels, activity badge or "state not reported", cached agents with age and no action, the dispatched-run and session "Copy command" entries), the agent panel and page, the Machines page (title column, filter box, `stale` chip, state age, version mark, CPU and Memory with route and age) and the machine panel and page with its four last-hour charts from `GET /api/v1/cockpit/machine-metrics` (polled every 10 seconds only while Home or a Machines page is visible, with the `local`, `live-remote`, `cached` and `none` states from the task 2 contract, stubbed), and the "New task" form that produces the `wb create` and `wb agent dispatch` commands to copy.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 14: Integration: journey, budgets, accessibility, phone, docs and manifests

**Id:** task-14
**Verifies:** cockpit-views#ac:every-tab-lists-its-collection, cockpit-views#ac:usable-at-360-wide, cockpit-views#ac:no-layout-shift-on-arrival, cockpit-views#ac:state-is-never-colour-only, cockpit-views#ac:views-coverage-gates-hold, cockpit-views#ac:every-number-is-a-link
**Depends-On:** 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13
**Status:** planning

Update the stubbed Playwright suite and the Linux-CI real-daemon journey for the new pages and routes (existing e2e and journey tests are already updated by the task that broke them, not deferred to this one); assert the budgets on the fixture end to end; run the accessibility pass (focus order, `aria-current`, chart text alternatives, contrast in both themes, 360 px width and the phone Home, cumulative layout shift 0); audit that every number is a link; and update the docs, command manifests and Agent Skill text that describe the Cockpit pages. It verifies, now that all pages exist, that every tab lists its collection: the views counterpart of the changed `cockpit` criterion `every-page-lists-its-collection`. Confirm every component has a rendering test and the thresholds still hold.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally. Add the Go gates of the Go tasks for any Go file this task touches.

## Open Questions

- Which remote store is the fleet's shared one (the Mac reads the git store, the VM
  publishes to its own hub) is undecided; periodic publish uses what each machine has
  configured, and live remote metrics need `remote.provider: hub`.
- The PrimeUI licence key is a pending founder decision outside this plan.
- Whether Stop and Reply for hand-started sessions should be built on herdr prompts is
  undecided.
- Whether the owner process liveness of every local worktree is cheap enough for the
  snapshot is for task 1 to measure.

---
*This document follows the https://specscore.md/plan-specification*
