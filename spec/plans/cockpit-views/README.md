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
redesign for a fleet of hundreds of repositories and worktrees. Schema version 2
of the fleet read model with compression, lazy branches and new fields, local
machine metrics, a shell with global search and keyboard navigation, a shared
matcher and virtual table, Tasks, Repositories, Worktrees, Agents and Machines
pages with detail pages, and an attention-first Dashboard with charts.

Approval: approved by delegation, the founder said "work autonomously" on
2026-10-01.

## Journey

The operator opens the Cockpit and sees on the Dashboard which agents run and
what needs attention. They press `/`, filter with a wildcard, sort, and open a
task that spans repositories, then a machine with its last-hour metrics. Every
view is in the address, and nothing renders more than 60 rows.

## Approach

Seven tasks. The two Go tasks change the contract and need no frontend; the
frontend foundation (task 3) is built against fixtures of that contract, so it
runs in parallel with them. The three page tasks (4, 5, 6) depend only on the
foundation, apart from the machine charts, which need the metrics contract of
task 2, and can run in parallel. The last task closes the plan with the
end-to-end, budget and accessibility checks. Chart.js is introduced in task 3 as
a shared lazy chunk so tasks 5 and 6 do not both add it. Every task keeps the
code it adds at 100% coverage: Go through `wb coverage --changed`, and
`cockpit/web` through thresholds of 100 for statements, branches, functions and
lines plus a rendering test for every component.

## Tasks

### Task 1: Schema version 2: compression, lazy branches, new fields

**Id:** task-1
**Verifies:** cockpit-views#ac:responses-are-gzip-with-etag, cockpit-views#ac:branches-leave-the-document, cockpit-views#ac:repository-entries-carry-activity-and-web-url, cockpit-views#ac:worktree-entries-carry-name-and-sync, cockpit-views#ac:agent-entries-carry-worktrees-task-start, cockpit-views#ac:fleet-document-fits-the-budget
**Depends-On:** —
**Status:** planning

Bump the fleet document to `schema_version` 2 (REQ:schema-version-2, daemon side; the client side is task 3). Add gzip for the fleet document, the new JSON routes and the static assets with correct ETag and `Vary: Accept-Encoding` behaviour. Remove `branches` from the document, keeping per-repository counts, and add `GET /api/v1/cockpit/branches?repository=<id>` (anonymous-local, `branch.read`, 404 for an unknown id). Add `last_activity_at` and `remote_url_web` to repositories, `name`, `ahead`, `behind` and `upstream_gone` to worktrees, and `worktrees`, `task` and `started_at` to agents, and document the `owner_state` vocabulary. Build the 500/600/4,000/3 fixture and a test asserting at most 150 kB gzipped. Map each new field into the closed metadata set and check no path, origin URL or credential reaches the document.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 2: Machine metrics

**Id:** task-2
**Verifies:** cockpit-views#ac:sampler-fills-a-ring-buffer, cockpit-views#ac:metrics-route-returns-history, cockpit-views#ac:machine-entries-carry-hardware-and-metrics
**Depends-On:** —
**Status:** planning

Add the sampler: every 10 seconds, off the request path, CPU percent, one-minute load, memory used and total, and free and total disk of the projects root into a 360-sample in-memory ring buffer, through an injectable source and clock, with a Windows build that reports metrics as unsupported. Add `os`, `arch`, `cpu_count` to machine entries and `metrics` (the latest sample) to the local machine's entry, and `GET /api/v1/cockpit/machine-metrics?machine=<id>` (same access class as the fleet document, empty history with a reason when unsupported, 404 for an unknown id). This task is independent of task 1 and may run in parallel with it; both touch the read-model mapper, so the second to land rebases.

Verification (all must pass before the task is complete): targeted `wb run -- go test <touched packages> -count=1`; `wb run -- go run ./cmd/wb coverage --changed` with 100% of the new code covered; `golangci-lint run <touched packages>`; `GOOS=windows go build ./... && GOOS=windows go vet ./...`; `wb run -- go test ./internal/quality/... -count=1`; `go run ./cmd/wb ci audit . --target main --strict`; `specscore spec lint`. The full Go suite runs in CI, never locally.

### Task 3: Frontend foundation: shell, matcher, shared table, tokens, view model

**Id:** task-3
**Verifies:** cockpit-views#ac:top-bar-shows-tabs-badges-and-freshness, cockpit-views#ac:warming-up-shows-progress, cockpit-views#ac:no-heading-repeats-the-tab, cockpit-views#ac:global-search-groups-results, cockpit-views#ac:shortcuts-navigate-and-respect-typing, cockpit-views#ac:matcher-grammar, cockpit-views#ac:matcher-is-linear-time, cockpit-views#ac:filter-state-lives-in-the-address, cockpit-views#ac:rows-are-one-line-and-virtual, cockpit-views#ac:uniform-columns-are-hidden, cockpit-views#ac:repository-and-time-rendering, cockpit-views#ac:empty-states-offer-clear, cockpit-views#ac:client-accepts-only-schema-2, cockpit-views#ac:derived-collections-computed-once, cockpit-views#ac:unchanged-snapshot-does-nothing, cockpit-views#ac:list-never-exceeds-60-row-elements, cockpit-views#ac:filtering-5000-rows-is-fast, cockpit-views#ac:initial-script-fits-the-budget, cockpit-views#ac:chart-library-is-pinned-and-tree-shaken
**Depends-On:** —
**Status:** planning

In `cockpit/web`: the shell (top bar, tabs with badges, freshness and session chips, hidden `h1`, global search, keyboard shortcuts), the pure matcher as a shared library, the shared virtual-scrolling table (sticky header, sort, quick-filter chips, machine chips, URL-held state, auto-hidden columns, name and time rendering, empty states), design tokens (typography, state colours with icons, light and dark), the derived view model (merged repositories, tasks, attention counts) memoised on the document identity, the fleet client that accepts only schema version 2, lazy routes with the initial-bundle budget, and the exact-pinned tree-shaken Chart.js wrapper as a lazy chunk. It works from fixtures of the v2 contract, so it does not wait for task 1's code; an unchanged `304` poll must not recompute or re-render.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 4: Frontend: Repositories, Worktrees, Tasks

**Id:** task-4
**Verifies:** cockpit-views#ac:repositories-merge-across-machines, cockpit-views#ac:repository-actions-follow-configuration, cockpit-views#ac:repositories-quick-filters, cockpit-views#ac:repository-detail-loads-branches-lazily, cockpit-views#ac:worktrees-columns-and-badges, cockpit-views#ac:worktrees-quick-filters, cockpit-views#ac:tasks-list-aggregates-worktrees, cockpit-views#ac:task-detail-shows-its-entities
**Depends-On:** 3
**Status:** planning

Build the Repositories page (merged per identity, machine chips, icon actions, quick filters) and the repository detail page with its lazily loaded branches, the Worktrees page (name column, hidden Branch column, sync badges, quick filters, default sort), and the Tasks page and task detail page at `/tasks/:task`. Detail pages are lazy chunks. Branches come from the route of task 1, stubbed in tests.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 5: Frontend: Agents and Machines

**Id:** task-5
**Verifies:** cockpit-views#ac:agents-list-describes-the-work, cockpit-views#ac:agent-detail-links-its-work, cockpit-views#ac:machines-table-title-and-links, cockpit-views#ac:machine-detail-metrics-charts, cockpit-views#ac:machine-without-metrics-says-so
**Depends-On:** 3, 2
**Status:** planning

Build the Agents page with human labels and a copy button, the agent detail page, the Machines page (the "Machines" column header as section title, state age, version mark, CPU and Memory) and the machine detail page with its four last-hour charts from `GET /api/v1/cockpit/machine-metrics`, using the contract of task 2 through stubs, and the plain "metrics are reported only by the machine this Cockpit runs on" state.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 6: Frontend: Dashboard

**Id:** task-6
**Verifies:** cockpit-views#ac:dashboard-now-shows-agents-and-machines, cockpit-views#ac:attention-lists-nonzero-items, cockpit-views#ac:attention-covers-every-item-kind, cockpit-views#ac:charts-are-lazy-linked-and-accessible, cockpit-views#ac:dashboard-panels-switch-and-remember, cockpit-views#ac:csp-and-canvas-only
**Depends-On:** 3
**Status:** planning

Build the Dashboard: the Now row with agent cards and the machine strip, the Needs attention row with the seven item kinds and its slot for work-loss risk, the four Chart.js charts with click-through and visually hidden tables, and the Repositories panel with its remembered segmented switch beside Recent tasks. Links target the list pages of task 4 by address, so they can be tested against stubs before those pages land.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally.

### Task 7: Journey, performance budgets, accessibility, docs and manifests

**Id:** task-7
**Verifies:** cockpit-views#ac:usable-at-360-wide, cockpit-views#ac:no-layout-shift-on-arrival, cockpit-views#ac:state-is-never-colour-only, cockpit-views#ac:views-coverage-gates-hold
**Depends-On:** 1, 2, 3, 4, 5, 6
**Status:** planning

Update the stubbed Playwright suite and the Linux-CI real-daemon journey for the new pages and routes; assert the budgets on the fixture (document size, initial script, 60 row elements, 30 ms filtering) end to end; run the accessibility pass (focus order, `aria-current`, chart text alternatives, contrast in both themes, 360 px width, layout shift); and update the docs, command manifests and the Agent Skill text that describe the Cockpit pages. Confirm every component has a rendering test and the thresholds still hold.

Verification (all must pass before the task is complete), in `cockpit/web`: `pnpm test` (the component-spec check plus Vitest with thresholds of 100 for statements, branches, functions and lines); `pnpm lint`; `pnpm build` with the bundle budget (initial JavaScript at most 350 kB raw); the stubbed Playwright run `pnpm test:e2e`; `specscore spec lint`. The real-daemon journey (`pnpm test:journey`) runs only on Linux CI. The full Go suite runs in CI, never locally. Add the Go gates of tasks 1 and 2 for any Go file this task touches.

## Open Questions

- Pull request state is `unknown` for every pull request because only local
  bindings are read; reading it from the remote is out of scope here and has
  no owner yet.
- The PrimeUI licence key is a pending founder decision outside this plan.

---
*This document follows the https://specscore.md/plan-specification*
