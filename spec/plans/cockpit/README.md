---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Cockpit shell

**Status:** Draft
**Source Feature:** cockpit
**Date:** 2026-10-01
**Owner:** alex
**Supersedes:** —
**Parent:** wb-cockpit

## Summary

Implement [cockpit](../../features/cockpit/README.md): serve an Angular
application from the loopback daemon under `/cockpit/`, protect it against
foreign hosts and origins, give the operator an owner session through
`wb cockpit`, and show this machine's repositories, worktrees, branches,
agents and known machines with drill-down from every count.

## Journey

The operator runs `wb cockpit`; the daemon starts and the browser opens on
the Dashboard, signed in as owner. They hover a count and click it; the
filtered table shows exactly the entities counted, each with its route and
age. Later they return by typing the address: the lists load with no login,
and the repository README asks for an owner session.

## Approach

Six tasks. The Go side is built bottom-up — mount and guard, then the
session, then the command that issues it, then the read model — so every
route exists and is protected before any browser code calls it. The
application follows in one task once its API is stable, and the journey test
closes the plan. Go packages are new (`internal/cockpit` and its children) so
they start and stay at full coverage. Every task keeps the code it adds at 100% statement coverage: Go through `wb coverage --changed`, and `cockpit/web` through a test run that fails below 100% of the application's own source (founder, 2026-10-01).

## Tasks

### Task 1: Mount, configuration and request guard

**Id:** task-1
**Verifies:** cockpit#ac:unbuilt-application-says-so, cockpit#ac:foreign-host-is-refused, cockpit#ac:dashboard-command-is-unchanged
**Depends-On:** —
**Status:** planning

Add `cockpit.hosted_url` (default `https://sneat.dev/wb/cockpit/`) and
`cockpit.trusted_hosts` to `internal/wbconfig`. Add `cockpit/web` with a
`go:embed` of its build output behind a build that tolerates an absent
output by serving the one-line "not built" page, as `hub/web/embed.go` does.
Mount `/cockpit/` and `/api/v1/cockpit/` on the loopback listener in
`serveDashboard` regardless of `hub:`, wrapped in a guard that refuses any
`Host` that is neither loopback with the listen port nor a trusted host with
status 421. Tests: the guard table, the unbuilt page, and a regression test
that `/`, `/metrics`, `/api/v1/overview` and `wb dashboard --local` answer as
before.

### Task 2: Owner session, cross-origin rule and capabilities

**Id:** task-2
**Verifies:** cockpit#ac:login-code-is-single-use, cockpit#ac:trusted-host-needs-a-session, cockpit#ac:only-the-hosted-origin-may-read-cross-origin, cockpit#ac:session-reports-principal-and-capabilities
**Depends-On:** 1
**Status:** planning

Add a login-code operation to the owner-token service on the daemon's unix
socket: single use, 60-second life, injectable clock. Add
`/cockpit/session/login`, which exchanges a code for the `HttpOnly`,
`SameSite=Strict`, 12-hour cookie named after the listen port. Resolve each
request to `anonymous-local` or `owner`; a trusted non-loopback host without
a session gets 401. Add the capability vocabulary and
`GET /api/v1/cockpit/session`. Add the cross-origin rule: allow exactly the
hosted origin on metadata `GET` routes without credentials, answer its
preflight with the private-network allowance, refuse any other foreign origin
with 403, and require own-origin plus JSON on state-changing routes.

### Task 3: The `wb cockpit` command

**Id:** task-3
**Verifies:** cockpit#ac:command-opens-local-cockpit, cockpit#ac:json-output-carries-no-code, cockpit#ac:hosted-flag-uses-configured-url, cockpit#ac:manifest-rows-exist
**Depends-On:** 2
**Status:** planning

Add `cmd/wb/cockpit.go`, reusing the daemon start-or-reuse path that
`wb dashboard --local` uses. It requests a login code over the owner RPC,
prints the login URL, and opens the browser only in text format on an
interactive desktop session. `--hosted` resolves `cockpit.hosted_url` and
starts nothing. `--format json` prints `{url, scope, opened}` with no code.
Add the capability row, the `ai/skills/commands.json` entry and skill
coverage in `wb-daemon`, the flag-matrix line and the persistent-flag support
declaration.

### Task 4: Fleet read model

**Id:** task-4
**Verifies:** cockpit#ac:read-model-lists-local-state, cockpit#ac:request-does-not-scan, cockpit#ac:anonymous-local-gets-metadata-only
**Depends-On:** 2
**Status:** planning

Add a background snapshotter in the daemon that builds the fleet document
from `discover.ScanLocalIndexed`, `worktrees.ListWithDiagnostics`,
`worktrees.BranchList`, the session and agent run records, and
`remotestate.ReadStatus` for other machines, tagging every entry with
`machine`, `route` and `observed_at`. Serve it at
`GET /api/v1/cockpit/fleet`; a request reads the last snapshot and never runs
Git, and before the first snapshot it returns the empty warming-up document.
Add the owner-only README route. Tests assert that the anonymous document
contains no file content, commit message, prompt or log body.

### Task 5: Angular application

**Id:** task-5
**Verifies:** cockpit#ac:counts-drill-down, cockpit#ac:readme-needs-owner, cockpit#ac:coverage-gates-hold
**Depends-On:** 2, 4
**Status:** planning

Create the Angular project at `cockpit/web` with Angular Material and the
CDK: the shell and navigation, the Dashboard, Repositories, Worktrees, Agents
and Machines pages as filterable tables, the hover card and drill-down for
every count, route and age labels on cached rows, the repository page with
its README or the owner-session notice, and light and dark themes. Controls
are driven by `GET /api/v1/cockpit/session`. Add a path-filtered CI workflow
that builds and tests it with a 100% statement threshold, and add its build
to the release before-hook with an assertion that the output exists.

### Task 6: Whole-journey end-to-end test

**Id:** task-6
**Verifies:** cockpit#ac:whole-journey-e2e
**Depends-On:** 3, 5
**Status:** planning

One Playwright test against a real daemon on a temporary projects root: run
`wb cockpit`, follow the printed URL, assert the Dashboard as owner, hover
and click the worktree count, assert the filtered table, clear the cookie,
reload, and assert that lists load while the README asks for an owner
session. No reloads or manual steps beyond those the journey names.

## Open Questions

- The Go package layout under `internal/cockpit` is settled in Task 1.
- Whether the snapshotter refreshes on a timer, on Git hook events, or both
  is settled in Task 4; the requirement is only that requests never scan.

---
*This document follows the https://specscore.md/plan-specification*
