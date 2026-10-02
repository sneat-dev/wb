---
format: https://specscore.md/plan-specification
status: Implemented
---
# Plan: Cockpit shell

**Status:** Implemented
**Source Feature:** cockpit
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** wb-cockpit

## Summary

Implement [cockpit](../../features/cockpit/README.md): serve an Angular
application from the loopback daemon under `/cockpit/`, protect it against
foreign hosts, proxies and origins, give the operator an owner session
through `wb cockpit`, and show this machine's repositories, worktrees,
branches, pull requests, agents and known machines with drill-down from every
count and a code-index panel.

## Journey

The operator runs `wb cockpit`; the daemon starts and the browser opens on
the Dashboard, signed in as owner. They hover a count and click it; the
filtered table shows exactly the entities counted, each with its route and
age. Later they return by typing the address: the lists load with no login,
and the repository README asks for an owner session.

## Approach

Nine tasks, and a tenth added after the plan was implemented. The Go side is built bottom-up — mount and guard, then the
session, then the command that issues it, then the read model — so every
route exists and is protected before any browser code calls it. The
application is split in three so no pull request carries the whole of it:
the project with its build, embedding and gates; the pages; code-index
freshness; then content and the code-index panel. The journey test closes the plan. Go packages are new
(`internal/cockpit` and its children) so they start and stay at full
coverage. Every task keeps the code it adds at 100% coverage: Go through `wb coverage --changed`, and `cockpit/web` through thresholds of 100 for statements, branches, functions and lines plus a rendering test for every component (founder, 2026-10-01).

## Tasks

### Task 1: Mount, configuration and request guard

**Id:** task-1
**Verifies:** cockpit#ac:unbuilt-application-says-so, cockpit#ac:foreign-host-is-refused, cockpit#ac:dashboard-command-is-unchanged
**Depends-On:** —
**Status:** complete

Add `cockpit.hosted_url`, `cockpit.code_browser_url`,
`cockpit.anonymous_metadata` and `cockpit.refresh_interval` to
`internal/wbconfig`. Add `cockpit/web` with a `go:embed` of its build output
behind a build that tolerates an absent output by serving the one-line "not
built" page, as `hub/web/embed.go` does. Mount `/cockpit/` and
`/api/v1/cockpit/` on the loopback listener in `serveDashboard` regardless of
`hub:`, wrapped in a guard that refuses a non-loopback host name with status 421,
whatever the port, and redirects page requests on a loopback alias to the canonical origin.
Tests: the guard table, the unbuilt page, and a regression test that `/`,
`/metrics`, `/api/v1/overview` and `wb dashboard --local` answer as before.

### Task 2: Owner session, principals and cross-origin rule

**Id:** task-2
**Verifies:** cockpit#ac:login-code-is-single-use, cockpit#ac:session-ends-on-logout-and-restart, cockpit#ac:proxied-request-needs-a-session, cockpit#ac:only-the-hosted-origin-may-read-cross-origin, cockpit#ac:session-reports-principal-and-capabilities
**Depends-On:** 1
**Status:** complete

Add a login-code operation to the owner-token service on the daemon's unix
socket: single use, 60-second life, injectable clock. Add
`/cockpit/session/login`, which exchanges a code for the `HttpOnly`,
`SameSite=Strict`, 12-hour cookie named after the listen port and redirects
to `/cockpit/`; and `POST /cockpit/session/logout`. Sessions live in memory.
Resolve each request to `anonymous-local` or `owner`; a request with a
forwarding header, or any request when `cockpit.anonymous_metadata` is false,
gets 401 without a session. Add the capability vocabulary and
`GET /api/v1/cockpit/session`. Add the cross-origin rule: allow exactly the
hosted origin on the metadata `GET` routes without credentials and with
`Vary: Origin`, answer its preflight with the private-network allowance,
refuse any other foreign origin including `null` with 403, and require the
canonical origin plus JSON on state-changing routes.

### Task 3: The `wb cockpit` command

**Id:** task-3
**Verifies:** cockpit#ac:command-opens-local-cockpit, cockpit#ac:json-output-carries-no-code, cockpit#ac:hosted-flag-uses-configured-url, cockpit#ac:manifest-rows-exist
**Depends-On:** 2
**Status:** complete

Add `cmd/wb/cockpit.go`, reusing the daemon start-or-reuse path that
`wb dashboard --local` uses. It requests a login code over the owner RPC,
prints the login URL on the canonical origin, and opens the browser only in
text format on an interactive desktop session. `--hosted` resolves
`cockpit.hosted_url` and starts nothing. `--format json` prints
`{url, scope, opened}` with no code. Add the capability row, the
`ai/skills/commands.json` entry and skill coverage in `wb-daemon`, the
flag-matrix line and the persistent-flag support declaration.

### Task 4: Fleet read model

**Id:** task-4
**Verifies:** cockpit#ac:read-model-lists-local-state, cockpit#ac:request-does-not-scan, cockpit#ac:anonymous-local-gets-metadata-only, cockpit#ac:future-dated-snapshot-is-unknown-and-stale
**Depends-On:** 2
**Status:** complete

Add a background snapshotter in the daemon that builds the fleet document
from `discover.ScanLocalIndexed`, the cheap worktree
enumeration the existing dashboard uses, one `git for-each-ref` per repository,
locally recorded pull requests, the session and agent run
records, and a local-only read of the local copy of the remote state store for
other machines, giving every entry
a stable `id`, its `machine`, `route` and `observed_at`. It refreshes on
`cockpit.refresh_interval`, skipping repositories whose fingerprint is
unchanged, and can refresh one repository on request from inside the daemon;
the default interval is set here from a measurement on the founder's
projects root. Map every source into the closed metadata field set, so
paths, file names, commit subjects and task summaries in a remote snapshot
are dropped. Serve `GET /api/v1/cockpit/fleet`; a request reads the
last snapshot and never runs Git, and before the first snapshot it returns
the empty warming-up document. Add the owner-only README route, which
resolves the file inside the checkout without following a link out of it and
answers 401 without a session.

### Task 5: Angular project, embedding and gates

**Id:** task-5
**Verifies:** cockpit#ac:release-build-includes-cockpit, cockpit#ac:coverage-gates-hold
**Depends-On:** 1
**Status:** complete

Create the Angular 22 project at `cockpit/web` with PrimeNG 22, the CDK,
Vitest and Playwright: an empty shell that builds, is embedded, and is served
under `/cockpit/` with the strict content security policy and its
per-response style nonce. Configure coverage
thresholds of 100 for statements, branches, functions and lines over
`cockpit/web/src`, and a check that every component has a rendering test.
Add the CI job, which runs on every pull request and passes at once when
nothing under `cockpit/web` changed. Add the build to the release before-hook
with an assertion that its output exists.

### Task 6: Pages, tables and drill-down

**Id:** task-6
**Verifies:** cockpit#ac:every-page-lists-its-collection, cockpit#ac:counts-drill-down, cockpit#ac:repository-links-to-code-browser
**Depends-On:** 2, 4, 5
**Status:** complete

Build the shell and navigation, and the Dashboard, Repositories, Worktrees,
Agents and Machines pages as tables filterable by machine and repository,
with route and age labels on cached rows, the hover card and drill-down for
every count, the link from each repository
to the CodeGrapher browser built from `cockpit.code_browser_url`, and light
and dark themes. Controls are driven by `GET /api/v1/cockpit/session`.
Branch and pull-request counts are not shown until they have list pages.

### Task 7: Code-index freshness

**Id:** task-7
**Verifies:** cockpit#ac:code-index-freshness-appears
**Depends-On:** 4, 6
**Status:** complete

Add `code_index` freshness to each repository and worktree in the read model,
in the six states `code-index-freshness` defines, read from indexer receipts.
If WB does not yet produce that report, build the receipt reader here as that
Feature's `freshness-in-fleet-status` requirement describes, without changing
`wb fleet status`. Add the freshness column to the Repositories and Worktrees
tables.

### Task 8: Repository content and the code-index panel

**Id:** task-8
**Verifies:** cockpit#ac:readme-needs-owner, cockpit#ac:hostile-readme-is-inert, cockpit#ac:code-index-panel
**Depends-On:** 4, 6, 7
**Status:** complete

Render the README as sanitized Markdown on the repository page, with the
owner-session notice for other callers. Add the code-index provider
interface with CodeGrapher as the first provider; the snapshotter asks it
once per checkout per indexer receipt and puts the statistics in the read
model, and no request starts a provider process. Add the code-index panel on
the repository and worktree pages.

### Task 9: Whole-journey end-to-end test

**Id:** task-9
**Verifies:** cockpit#ac:whole-journey-e2e
**Depends-On:** 3, 6, 8
**Status:** complete

One Playwright test against a real daemon on a temporary projects root: run
`wb cockpit`, follow the printed URL, assert the Dashboard as owner, hover
and click the worktree count, assert the filtered table, clear the cookie,
reload, and assert that lists load while the README asks for an owner
session. No reloads or manual steps beyond those the journey names. The journey
runs on Linux in CI only, because on macOS the daemon is a launchd service with
one fixed label per user.

### Task 10: The daemon's log is the owner's alone

**Id:** task-10
**Verifies:** cockpit#ac:daemon-log-needs-an-owner-session, cockpit#ac:daemon-log-fails-closed-without-an-owner-check, cockpit#ac:daemon-log-error-names-no-path
**Depends-On:** 2
**Status:** complete

Added 2026-10-02, after the founder's rule that anything returning the content
of a file requires authentication. The dashboard's `GET /api/v1/log` takes an
owner check through `dashboard.Options.Owner`, which the daemon fills with
`cockpit.Server.IsOwner`: a loopback `Host`, the canonical origin or none, and a
live session. Without an owner the route answers 401 before the file is opened,
without an owner check it answers 403, and its failures carry a closed code and
no path.

### Task 11: The dashboard's JSON routes answer only on a loopback host

**Id:** task-11
**Verifies:** cockpit#ac:dashboard-json-routes-answer-only-on-loopback
**Depends-On:** 10
**Status:** complete

Added 2026-10-02, from the whole-branch security review. `GET /api/v1/health`
and `GET /api/v1/overview` apply the Host check, by the rule Cockpit's guard
applies (package `internal/loopbackhost`, which both ask), and a failed overview
answers a closed code and a fixed message while its reason goes to the daemon's
log.

## Open Questions

- The Go package layout under `internal/cockpit` is settled in Task 1.

---
*This document follows the https://specscore.md/plan-specification*
