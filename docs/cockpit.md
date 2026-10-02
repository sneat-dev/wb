# Cockpit architecture

Cockpit is the web application the WB daemon serves under `/cockpit/`: an Angular
single-page application in `cockpit/web`, embedded in the `wb` binary (`cockpit/web/embed.go`,
from the production build in `cockpit/web/dist`). This page is the durable description of
how it is put together. The behaviour is specified in
[`cockpit`](../spec/features/cockpit/README.md) and
[`cockpit-views`](../spec/features/cockpit-views/README.md); the building blocks have their
own READMEs (`cockpit/web/libs/fleet-data/README.md`, `libs/ui/README.md`,
`libs/ui/README.control.md`).

## Pages and routes

| Tab (key after `g`) | Route | Detail route |
|---|---|---|
| Home (`h`) | `/` (`/dashboard` is an alias) | none |
| Tasks (`t`) | `/tasks`, `/tasks/new` (the "New task" form) | `/tasks/detail?task=<name>` |
| Repositories (`r`) | `/repositories` | `/repositories/<host>/<owner>/<name>` (`-` for no host), `/repositories/<id>` |
| Worktrees (`w`) | `/worktrees` | `/worktrees/<id>` |
| Agents (`a`) | `/agents` | `/agents/<id>` |
| Machines (`m`) | `/machines` | `/machines/<id>` |

Every list is state in the address: `q` (the filter), `sort` and `dir`, `machine`, `chips`
and `sel` (the row whose side panel is open). A detail route renders the same component as
the panel. The Home tab badge counts the tasks in "Needs you" and links to Tasks with the
chip `needs-you`; the Agents badge counts running agents and links to Agents with the chip
`running`.

## Data flow

```
wb daemon  --GET /api/v1/cockpit/fleet (ETag, gzip)-->  FleetStore  -->  FleetModel  -->  pages
           --GET /api/v1/cockpit/session----------->   (signals)       (memoised          (lists, panels,
           --GET /api/v1/cockpit/branches?repository=   per document)   derivations)       Home sections)
           --GET /api/v1/cockpit/machine-metrics?machine=
```

- The **fleet document** (schema version 2) holds machines, repositories, worktrees, pull
  requests and agents, local and cached or live-remote, and no branches or metrics. The store
  re-reads it with `If-None-Match`; an identical body keeps the document object, so nothing
  downstream recomputes. A document of another schema version is not shown: the page says
  whether to update `wb` or reload.
- **Branches** are read lazily per repository, **machine metrics** every 10 seconds only while
  a page that shows them exists (`MetricsPoller`), a **README** only for an owner session.
- `FleetModel` (in `@cockpit/fleet-data`) derives merged repositories, tasks with their state,
  "Needs you", "Ready to land", cleanup counts and fleet health once per document. Pages never
  re-derive them.
- Nothing the application does changes state. A "Copy command" entry is text for the operator;
  actions are the registry of `cockpit-actions`, which fills the action slots.

## Trust rule

Anonymous-local readers get only the closed list of metadata in the `cockpit` Feature: no
file content, paths, environment values, command lines or free-text error output, in any
response. An owner session (`wb cockpit` mints a single-use login code) adds `repo.content.read`
and the session response's `machine_routes` (the `host`, `user` and `wb_path` of
`session_move.targets.<machine>.ssh`, for copied `ssh ...` commands). The application:

- uses `machine_routes` only when the session's principal is `owner` (`ownerRoutes` in
  `fleet-view.ts`), whatever a response carried;
- asks the README route only with `repo.content.read`, and the action registry only with an
  action capability;
- shows no action it cannot run, not even disabled, and offers one affordance: the session chip
  reading "Sign in as owner: run `wb cockpit`".

`owner-gating.spec.ts` (every page) and `anonymous.e2e.ts` (every route, in the built
application) prove it. A new field or route also extends `internal/cockpit/fleet/sentinel_test.go`.

## Transports and cadence

| What | How | Cadence |
|---|---|---|
| This machine's fleet | the daemon's snapshotter | `cockpit.refresh_interval` (default 1 minute) |
| Another machine, live | its daemon's hub export route (HTTP); `wb cockpit export` over SSH is the fallback for a machine with no HTTP route (it is being added: `cockpit.remote_ssh` is already accepted) | at each refresh while fresh; an export stays live for 2 refresh intervals; a failing machine backs off to at most 5 minutes |
| Another machine, metrics only | the same, metrics only | every 30 seconds while a client asked in the last minute |
| Another machine, cached | the snapshot it published to the remote store | `remote.publish.interval` (minimum 5 minutes; unset means by hand only) |
| This machine's metrics | the daemon's sampler (`/proc` on Linux, sysctl and gopsutil on macOS) | every 10 seconds, 360 samples, in memory |
| Pull requests | GitHub, for this machine's open pull requests | `cockpit.pull_request_limit` per pass, `cockpit.pull_request_hourly_budget` per rolling hour |

A live read replaces the cached entries of a machine while it is fresh; a failed read shows
the typed `remote_error` of that machine (never the remote's text) and the cached entries stay,
marked with their age. A machine's `export_dropped` and `agents_truncated` say what its export
or this daemon's caps left out. This machine's own failed periodic publish is `publish_error`
(`collect_failed`, `store_unavailable`, `publish_failed`, `optional_fields_dropped`), shown in
"Fleet health" and on its Machines row and panel with the command that fixes it.

### Configuration

| Key (`wb.yaml`) | Meaning |
|---|---|
| `cockpit.hosted_url`, `cockpit.code_browser_url` | the hosted Cockpit origin, and the code browser the links point to |
| `cockpit.anonymous_metadata` | `false`: no anonymous reader and no transport gets a machine's metadata |
| `cockpit.refresh_interval` | snapshot refresh interval (default `1m`) |
| `cockpit.pull_request_limit` | most pull requests observed per pass (default 10, at most 200) |
| `cockpit.pull_request_hourly_budget` | most observations per rolling hour (default 120, between 10 and 400) |
| `cockpit.remote_http` | `false`: read no machine over HTTP |
| `cockpit.remote_ssh` | `false`: read no machine over SSH (the fallback for a machine with no HTTP route) |
| `session_move.targets.<machine>.http` | `url` and `token_file` of that machine's hub: its live Cockpit export |
| `session_move.targets.<machine>.ssh` | `host` and `wb_path` of that machine: the SSH route, and the host of copied commands |
| `remote.publish.interval`, `.agents`, `.metrics` | opt-in periodic publish, and whether it carries this machine's agents and latest sample |

`wb cockpit export --format json [--metrics-only]` prints this machine's metadata envelope from
its running daemon as the anonymous-local reader; it never starts a daemon.

## Budgets

The build (`pnpm build` in `cockpit/web`, `tools/finish-build.mjs`) measures the first-page
JavaScript of every lazy page route the same way, raw bytes over JavaScript only: the scripts
the entry document loads with what they import statically, plus the route's own page chunks.
Home may take 350 kB, every other route 500 kB. The table is printed at the end of the build and
the build fails when a route is over. Chart.js and the other lazy chunks are not part of it; nothing
heavy belongs on Home's path. Layout shift from skeleton to data stays under 0.01 on every route.

## Tests

`pnpm test` (component-spec check plus Vitest with thresholds of 100 for statements, branches,
functions and lines), `pnpm lint`, `pnpm test:e2e` (stubbed Playwright: every page's journey,
the anonymous-route and count-link audits, an axe run over every route in both themes, keyboard,
phone and layout-shift checks). `pnpm test:journey` is the real-daemon journey and runs only on
Linux under GitHub Actions (`.github/workflows/cockpit-web.yml`): on macOS the daemon is one
fixed launchd label per user, so a test daemon would stop the developer's own. Its page steps are
in `apps/cockpit-e2e/src/journey/steps.ts`, which the stubbed suite also runs against answers
shaped like the daemon's.

## How to add a page

1. Add the route to `apps/cockpit/src/app/app.routes.ts` as a lazy `page(...)`, and, for a tab,
   to `PAGE_LINKS` in `nav.ts` with its `g` key. The build measures the route's budget from that
   table; nothing else lists routes.
2. Build the page from `@cockpit/ui/list` (`<app-list>` with `columns`, `priority` and `value`),
   `<app-panel-content>` for the panel and the detail route, and the cells and badges of
   `@cockpit/ui/control`. Rows come from `@cockpit/fleet-data/list` (`build*Rows`), panels from
   `/panel`, copy commands from `/commands`, never hand-built text, links from the link helpers.
3. Declare the page in `PAGE_RULES` (`fleet-data/src/lib/vocabulary.ts`): its filter fields, chips and sorts.
4. Write unit specs at 100 percent, a stubbed e2e of the journey (filter, chip, select, panel,
   detail route, Back), and add the route to `BUSY_ROUTES` in `apps/cockpit-e2e/src/support.ts`, so
   the anonymous, axe, phone and layout-shift checks cover it.
5. Check the page at 390 and 360 px and in both themes (`pnpm shots`), and read the budget table.
