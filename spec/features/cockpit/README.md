---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Cockpit

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=request-change) |
**Status:** Draft
**Source Ideas:** wb-cockpit

## Summary

`wb cockpit` opens an operational web UI served by the local daemon. It shows
this machine's repositories, worktrees, branches, agents and known machines,
lets the operator drill from any count to the entities behind it, and
establishes the owner session that file content and actions require. It is
the first slice of [WB Cockpit](../../ideas/wb-cockpit.md) and runs beside
the two existing read-only dashboards until a later Feature retires them.

## Problem

WB has two web surfaces and neither can answer "what is happening on this
machine, and what needs me?" in one place. `internal/dashboard` lists managed
worktrees with an owner and an age but carries no Git state. `hub/web` exists
only when `wb.yaml` has a `hub:` section, and `wb dashboard` never opens it.
Neither can act.

The loopback listener also has no request protection of its own today. It
sends a content security policy, but `/api/v1/*` checks neither the `Host`
header nor `Origin`, so a web page that rebinds DNS to `127.0.0.1` can read
it. An operational UI cannot be added on top of that.

## Journey

1. **Start.** I run `wb cockpit` in a terminal on my laptop. The daemon
   starts if it was not running and my browser opens.
   **Observable good result:** the Cockpit Dashboard is on screen within one
   page load, already signed in as the owner, with no prompt and no password.
2. **Middle.** I read the counts — repositories, worktrees, branches, agents,
   machines — hover one, and click through.
   **Observable good result:** the hover card names the entities behind the
   count, and the click lands on a table filtered to exactly those entities.
   Every row says where it came from (`local` or `cached`) and when it was
   observed.
3. **End.** I close the tab and, an hour later, type the Cockpit address by
   hand.
   **Observable good result:** the lists still load without a login, anything
   that shows file content or changes state says it needs an owner session,
   and running `wb cockpit` again restores it.

## Behavior

### Command surface

#### REQ: cockpit-command

`wb cockpit` MUST start or reuse this machine's loopback daemon, exactly as
`wb dashboard --local` does today, and resolve the Cockpit URL on that
daemon. It MUST obtain an owner login (REQ:owner-session) and print the login
URL. It opens the platform browser only in text format on an interactive
desktop session.

`--hosted` resolves the hosted Cockpit URL instead and starts no daemon. The
hosted URL is the single configuration value `cockpit.hosted_url`, whose
default is `https://sneat.dev/wb/cockpit/`; no other code path may spell that
address.

`--format json` and its `--json` shortcut MUST print
`{url, scope, opened}`, where `scope` is `local` or `hosted`, and MUST NOT
launch a browser. The JSON `url` never contains a login code.

#### REQ: dashboard-command-unchanged

`wb dashboard` and its `--local`, `--metrics` and `--coverage` flags MUST
keep their present behavior. Making `wb dashboard` an alias of `wb cockpit`
belongs to the Feature that retires the existing dashboards.

#### REQ: command-manifest-rows

`wb cockpit` MUST ship with its capability row, its command-coverage entry
and Agent Skill coverage, its flag-matrix line, and a persistent-flag support
declaration, as every public WB leaf does.

### Serving

#### REQ: cockpit-mount

The loopback daemon MUST serve the Cockpit application under `/cockpit/` and
its API under `/api/v1/cockpit/`, whether or not `wb.yaml` has a `hub:`
section. The existing routes — `/`, `/metrics`, `/coverage`, `/api/v1/*`,
`/workbench/` and `/v0/workbench/` — are unchanged.

#### REQ: embedded-application

The application is an Angular project using Angular Material and the CDK,
kept at `cockpit/web`. It MUST NOT use React. Its production build is
embedded in the wb binary at release time, so running Cockpit needs neither
Node nor a network connection. A wb built from source without that build
MUST serve a one-line page saying Cockpit was not built and how to build it,
as `hub/web` does.

The release job MUST build `cockpit/web` before the binary and fail when the
build output is missing. CI MUST build and test `cockpit/web` when files
under it change.

#### REQ: light-and-dark

The application MUST follow the browser's light or dark preference.

### Local request protection

#### REQ: host-header-check

Every request to `/cockpit/` and `/api/v1/cockpit/` MUST carry a `Host`
header naming a loopback host — `localhost`, `127.0.0.1` or `[::1]` — with
the daemon's listen port, or a host listed in `cockpit.trusted_hosts`. Any
other `Host` is refused with status 421 before any handler runs. This is what
stops a page that rebinds DNS to the loopback address.

#### REQ: anonymous-local-reads-metadata-only

A request with a loopback `Host` and no owner session is the principal
`anonymous-local`. It MAY read metadata: the names and counts of
repositories, worktrees, branches, agents and machines, and their state
fields. It MUST NOT receive file content, diffs, commit messages, prompts or
log bodies, and it MUST NOT change anything.

A request whose `Host` is a trusted non-loopback host is never
`anonymous-local`: it gets nothing without an owner session, because a tunnel
or proxy delivering to the loopback listener is not proof of the local
operator.

#### REQ: cross-origin-allowance

`/api/v1/cockpit/` MUST send `Access-Control-Allow-Origin` for exactly one
foreign origin, the origin of `cockpit.hosted_url`, only on metadata `GET`
routes, and never with `Access-Control-Allow-Credentials`. A preflight from
that origin is answered with the allowance and with
`Access-Control-Allow-Private-Network: true`. A request carrying any other
foreign `Origin` is refused with status 403.

A hosted page therefore reads metadata and nothing else, even in a browser
that holds an owner session cookie for the daemon.

### Owner session

#### REQ: owner-session

`wb cockpit` obtains a single-use login code over the daemon's owner-token
RPC on its unix socket. The code is valid for 60 seconds. It travels in the
query string of `/cockpit/session/login`; the daemon exchanges it for a
session cookie that is `HttpOnly`, `SameSite=Strict`, expires after 12 hours,
and is named after the listen port so two daemons on one host keep separate
sessions. A used or expired code is refused and establishes nothing.

This is the admin session
[peer-connectivity](../peer-connectivity/README.md)#req:admin-requires-owner-credential
describes; Cockpit provides it, and that Feature's `wb dashboard --admin`
becomes `wb cockpit`.

#### REQ: owner-routes

A route that returns content or changes state MUST require the session
cookie. A state-changing route MUST also require an `Origin` equal to the
daemon's own origin and a JSON content type. A request that fails any of
these is refused with status 401 or 403 and has no effect.

### Principals and capabilities

#### REQ: capability-vocabulary

Every Cockpit route and every action declares one required capability. The
vocabulary in this Feature is:

- metadata: `fleet.read`, `machine.read`, `repo.read`, `worktree.read`,
  `branch.read`, `pr.read`, `agent.read`;
- content: `repo.content.read`.

`anonymous-local` holds the metadata capabilities. `owner` holds every
capability. Capabilities for actions are added by the Feature that defines
the action. Per-user grants, roles and a third `peer` principal are not part
of this Feature.

#### REQ: effective-permissions-are-discoverable

`GET /api/v1/cockpit/session` MUST return the caller's principal and its
effective capabilities. The application MUST use that response, not its own
assumptions, to decide what to show, what to disable, and how to explain an
unavailable control.

### Read model

#### REQ: fleet-read-model

`GET /api/v1/cockpit/fleet` MUST return one versioned document
(`schema_version`) with `generated_at` and these collections:

- **machines** — this machine, and every other machine the configured remote
  provider has a snapshot for;
- **repositories** — with counts of worktrees, local branches, remote
  branches, open pull requests where known, and active agents;
- **worktrees** — task, repository, branch, owner state, last activity;
- **branches** — local and remote;
- **agents** — registered sessions and dispatched agent runs.

Every entry carries its `machine`, a `route` and an `observed_at` time.

#### REQ: route-and-freshness-are-explicit

`route` is `local` for state this daemon observed itself and `cached` for
state read from another machine's published snapshot. A `cached` entry's
`observed_at` is the snapshot's publish time. The application MUST show the
route and the age of every `cached` entry and MUST NOT render cached state as
live.

#### REQ: no-fleet-scan-on-the-request-path

The read model MUST be served from a snapshot the daemon refreshes in the
background. A request MUST NOT wait for a Git scan of every repository. The
document says when the snapshot was taken, and a request made before the
first snapshot exists returns an empty, well-formed document marked as
warming up.

### Pages and interaction

#### REQ: navigation

The application MUST provide Dashboard, Repositories, Worktrees, Agents and
Machines pages. Each list page is a table that can be filtered by machine and
by repository.

#### REQ: summary-hover-drill-down

Every count the application shows MUST have a hover card that names the
entities it counts, or the first of them with the total when there are many,
and a click that opens the list filtered to exactly those entities. A hover
card contains no control that changes state.

#### REQ: repository-readme

A repository page MUST render the `README.md` of the repository's default
branch checkout for a caller holding `repo.content.read`. Without it the page
says an owner session is needed and names the command that provides one.

### Test coverage

#### REQ: new-code-is-fully-covered

All code this Feature adds MUST reach 100% statement coverage (founder,
2026-10-01). For Go that is every statement in a new package and every added
or changed statement in an existing one, measured by `wb coverage --changed`.
For `cockpit/web` the test run MUST fail below 100% statement coverage of the
application's own source.

## Dependencies

- [daemon-lifecycle](../daemon-lifecycle/README.md)
- [peer-connectivity](../peer-connectivity/README.md)
- [remote-state](../remote-state/README.md)
- [fleet-status](../fleet-status/README.md)

## Not Doing

- Retiring `internal/dashboard` or `hub/web`, or porting their pages — a
  later Feature, after the port is complete.
- Publishing the application at the hosted URL — this Feature makes the
  daemon accept the hosted origin; deploying the hosted copy is a later
  slice.
- A live event stream — the application re-reads the read model.
- Per-user grants, roles, and binding beyond loopback — decision 0002 gates
  them on an OAuth2 or OIDC provider.

## Acceptance Criteria

### AC: command-opens-local-cockpit

**Requirements:** cockpit#req:cockpit-command, cockpit#req:owner-session

Scenario: First run on a machine with no daemon running
Given no WB daemon is running
When the operator runs `wb cockpit` in an interactive terminal
Then a daemon is running on the loopback address, the printed URL is on that address under `/cockpit/session/login` with a code, and following it once sets the session cookie and lands on `/cockpit/`

### AC: json-output-carries-no-code

**Requirements:** cockpit#req:cockpit-command

Scenario: An agent asks for the address
Given a running daemon
When `wb cockpit --format json` runs
Then stdout is one JSON object with `url`, `scope` equal to `local` and `opened` equal to false, the URL has no query string, and no browser is launched

### AC: hosted-flag-uses-configured-url

**Requirements:** cockpit#req:cockpit-command

Scenario: The hosted address is configuration
Given `cockpit.hosted_url` is set to a test address
When `wb cockpit --hosted --format json` runs
Then `url` is that address, `scope` is `hosted`, and no daemon was started

### AC: dashboard-command-is-unchanged

**Requirements:** cockpit#req:dashboard-command-unchanged, cockpit#req:cockpit-mount

Scenario: Existing surfaces keep working
Given a daemon serving Cockpit
When `wb dashboard --local --format json`, `GET /`, `GET /metrics` and `GET /api/v1/overview` are requested
Then each answers as it did before this Feature

### AC: manifest-rows-exist

**Requirements:** cockpit#req:command-manifest-rows

Scenario: The new leaf is fully declared
Given the built binary
When the capability-manifest, skill-coverage and persistent-flag tests run
Then they pass with `wb cockpit` present and its declared flags equal to its real flags

### AC: unbuilt-application-says-so

**Requirements:** cockpit#req:embedded-application

Scenario: Built from source without the frontend
Given a wb binary built with no Cockpit build output
When `GET /cockpit/` is requested
Then the response is a one-line plain-text page naming the build command, with status 200

### AC: foreign-host-is-refused

**Requirements:** cockpit#req:host-header-check

Scenario: DNS rebinding
Given a daemon listening on `127.0.0.1:8766`
When a request reaches `/api/v1/cockpit/fleet` with `Host: attacker.example:8766`
Then the response status is 421 and the body contains no fleet data

### AC: anonymous-local-gets-metadata-only

**Requirements:** cockpit#req:anonymous-local-reads-metadata-only, cockpit#req:owner-routes

Scenario: No session
Given a loopback request with no session cookie
When it requests the fleet read model and then a repository's README
Then the read model is returned and contains no file content, commit message, prompt or log body, and the README request is refused with status 401

### AC: trusted-host-needs-a-session

**Requirements:** cockpit#req:anonymous-local-reads-metadata-only, cockpit#req:host-header-check

Scenario: Through a tunnel
Given `cockpit.trusted_hosts` lists `cockpit.example.test`
When a request with that `Host` and no session cookie asks for the fleet read model
Then it is refused with status 401

### AC: only-the-hosted-origin-may-read-cross-origin

**Requirements:** cockpit#req:cross-origin-allowance

Scenario: Three origins
Given `cockpit.hosted_url` is `https://hosted.example.test/wb/cockpit/`
When the fleet read model is requested with `Origin: https://hosted.example.test`, then with `Origin: https://other.example.test`, and a state-changing route is requested from the hosted origin
Then the first response allows that origin without credentials, the second is refused with status 403, and the third is refused with no effect

### AC: login-code-is-single-use

**Requirements:** cockpit#req:owner-session

Scenario: Replay and expiry
Given a login code that has been exchanged once, and another issued 61 seconds ago
When each is presented to `/cockpit/session/login`
Then both are refused and no cookie is set

### AC: session-reports-principal-and-capabilities

**Requirements:** cockpit#req:capability-vocabulary, cockpit#req:effective-permissions-are-discoverable

Scenario: Two principals
Given one request without a session and one with an owner session
When each requests `/api/v1/cockpit/session`
Then the first reports `anonymous-local` with only the metadata capabilities, and the second reports `owner` with `repo.content.read` as well

### AC: read-model-lists-local-state

**Requirements:** cockpit#req:fleet-read-model, cockpit#req:route-and-freshness-are-explicit

Scenario: One repository with two worktrees and a remote snapshot
Given a projects root with one repository that has two WB worktrees, and a remote-state snapshot published by a second machine
When the fleet read model is requested
Then it lists both machines, the repository with a worktree count of two, both worktrees with route `local`, and the second machine's entries with route `cached` and `observed_at` equal to the snapshot's publish time

### AC: request-does-not-scan

**Requirements:** cockpit#req:no-fleet-scan-on-the-request-path

Scenario: Cold and warm
Given a daemon that has not yet completed its first snapshot
When the fleet read model is requested, and requested again after the snapshot completes
Then the first response is well-formed, empty and marked as warming up, the second carries the snapshot time, and neither request ran a Git command

### AC: counts-drill-down

**Requirements:** cockpit#req:navigation, cockpit#req:summary-hover-drill-down, cockpit#req:light-and-dark

Scenario: From a count to its rows
Given the Repositories page showing a repository with a worktree count of two
When the operator hovers the count and then clicks it, in a browser set to dark mode
Then the hover card names both worktrees and has no button, the click opens the Worktrees page filtered to that repository showing exactly those two rows, and the page uses the dark theme

### AC: readme-needs-owner

**Requirements:** cockpit#req:repository-readme

Scenario: With and without a session
Given a repository whose default branch checkout has a `README.md`
When its page is opened with an owner session and then without one
Then the first renders the README and the second shows that an owner session is needed and names `wb cockpit`

### AC: coverage-gates-hold

**Requirements:** cockpit#req:new-code-is-fully-covered

Scenario: A statement without a test
Given a change that adds one untested statement to a new Go package and one to `cockpit/web`
When the Go changed-coverage check and the `cockpit/web` test run execute in CI
Then both fail, naming the uncovered statement, and both pass once the statements are tested

### AC: whole-journey-e2e

**Requirements:** cockpit#req:cockpit-command, cockpit#req:owner-session, cockpit#req:fleet-read-model, cockpit#req:summary-hover-drill-down

Scenario: The journey without crutches
Given a temporary projects root with one repository and two worktrees, and no daemon running
When one end-to-end test runs `wb cockpit`, follows the printed URL in a browser, hovers and clicks the worktree count, clears the cookie and reloads
Then the Dashboard appears signed in as owner, the filtered Worktrees table shows both rows, and after the reload the lists still load while the repository README asks for an owner session

## Open Questions

- Which Angular Material table and overlay primitives carry the dense
  repository grid is an implementation choice; if they prove too sparse the
  choice of an open-source grid returns to the founder.

---
*This document follows the https://specscore.md/feature-specification*
