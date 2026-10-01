---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Cockpit

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cockpit?op=request-change) |
**Status:** Approved
**Source Ideas:** wb-cockpit

## Summary

`wb cockpit` opens an operational web UI served by the local daemon. It shows
this machine's repositories, worktrees, branches, pull requests, agents and
known machines, lets the operator drill from any count to the entities behind
it, and establishes the owner session that file content and actions require.
It is the first slice of [WB Cockpit](../../ideas/wb-cockpit.md) and runs
beside the two existing read-only dashboards until a later Feature retires
them.

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

`--listen <host:port>` names the loopback address of the daemon to start (default `127.0.0.1:8766`); without it a daemon already running on this machine is used wherever it listens. A running daemon recorded on a different address is never replaced: the command refuses and names that address. Non-loopback addresses are refused before anything starts.

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

The application is an Angular 22 project using PrimeNG 22 and the Angular
CDK, kept at `cockpit/web`, with Vitest for unit tests and Playwright for
end-to-end tests. This is the stack of the CodeGrapher web UI, chosen so its
components can be reused (founder, 2026-10-01). Only PrimeNG's open-source
components are used; no paid template or block. It MUST NOT use React. Its production build is
embedded in the wb binary at release time, so running Cockpit needs neither
Node nor a network connection. A wb built from source without that build
MUST serve a one-line page saying Cockpit was not built and how to build it,
as `hub/web` does.

The release job MUST build `cockpit/web` before the binary and fail when the
build output is missing.

#### REQ: light-and-dark

The application MUST follow the browser's light or dark preference.

### Local request protection

#### REQ: host-header-check

Every request to `/cockpit/` and `/api/v1/cockpit/` MUST carry a `Host`
header naming a loopback host — `localhost`, `127.0.0.1` or `[::1]` — on any
port. Any other host name is refused with status 421 before any handler runs.
This is what stops a page that rebinds DNS to the loopback address. The port
is not checked, so an SSH forward to a different local port works.

The canonical origin is `http://127.0.0.1:<port>`, or `http://[::1]:<port>`
when the daemon listens on the IPv6 loopback address, with the port the request
arrived on. A request for a page under `/cockpit/` on another loopback name
is redirected to the same path on the canonical origin, so the session
cookie, which browsers scope by host, is always set and read on one host.

#### REQ: forwarded-requests-are-never-anonymous

A request that carries any of `Forwarded`, `X-Forwarded-For`,
`X-Forwarded-Host`, `X-Forwarded-Proto`, `X-Real-IP`, `Via` or
`CF-Connecting-IP` came through a proxy or tunnel. It is never
`anonymous-local`: without an owner session it is refused with status 401,
because arriving on the loopback listener is not proof of the local operator
([peer-connectivity](../peer-connectivity/README.md)#req:admin-requires-owner-credential).

`cockpit.anonymous_metadata: false` turns the `anonymous-local` principal off
altogether, for an operator whose proxy rewrites `Host` and strips those
headers; WB cannot detect such a proxy.

#### REQ: anonymous-local-reads-metadata-only

A request that passes the two checks above and has no owner session is the
principal `anonymous-local`. It MAY read metadata and nothing else. Metadata
is this closed set of fields:

- machine name, the machine's unique id, WB version, route and observation time;
  and, for the local machine and for another machine whose snapshot carries them,
  operating system and architecture names, CPU count and boot time (`boot_time`);
- the snapshot refresh interval in seconds (`refresh_interval_seconds`), and a
  machine's last remote-read failure as a code (`remote_error`: `ssh_unavailable`,
  `auth_failed`, `timeout`, `wb_missing`, `wb_too_old`, `daemon_not_running`,
  `export_refused` or `bad_payload`), never the remote's error text;
- a machine's resource samples, served only by the `machine-metrics` route and
  never in the fleet document, which are numbers and times only: CPU percent,
  one-minute load, memory used and total bytes, free and total bytes of the
  projects-root disk, and the sample time, with the source of the answer
  (`route`: `local`, `live-remote`, `cached` or `none`) and, for `live-remote`,
  its fetch time (`fetched_at`)
  ([cockpit-views](../cockpit-views/README.md)#req:machine-metrics-route);
- repository forge host (also on an entry mapped from another machine's snapshot
  whose name starts with a hostname), `owner/name`, default branch name, for a
  local repository the time of its newest local-branch activity, and
  `remote_url_web`, the `https://<host>/<owner>/<name>` address built from the
  host and `owner/name` alone and emitted only when the host matches a hostname
  pattern and every path segment matches `[A-Za-z0-9._-]+` and is not `.` or
  `..`;
- task name, stream name, branch name, lifecycle and owner state (`active`,
  `idle`, `orphaned` or `unknown`), last activity time, the worktree name (its
  task, never a path), and, for a worktree on the local machine only, its
  `ahead` and `behind` commit counts and its `upstream_gone` flag;
- pull request number, URL and state (`open`, `merged`, `closed` or `draft`),
  the merge state GitHub reports (`mergeable`), the counts of checks total,
  passed, failed and pending, the checks verdict (`checks_green`), the name of the
  first failing check (`failed_check`) and the time it was read (`checked_at`)
  ([cockpit-views](../cockpit-views/README.md)#req:pull-request-fields);
- agent run and session identifiers, runtime, model and state, the agent's
  `activity` (`working`, `blocked`, `idle`, `done` or `unknown`) when it is
  reported, the ids of the worktrees an agent works on, its task name, repository
  and its start time, the exit code of a finished run, and the same agent fields
  read from another machine's snapshot;
- the landed-task throughput block (`throughput`): the window in days, the
  number of tasks landed per day, and at most five of the slowest landed tasks
  with their task name, duration in seconds and landing time;
- counts, durability levels, risk reason codes, and code-index freshness: per
  configured indexer its configured name, its state, for a stale index the
  number of commits behind, and the time of the receipt it was read from;
- code-index statistics: the name of the configured code-index provider (absent
  when none is configured), and per code-index entry whether an index exists,
  its totals of files, symbols and edges, the symbols per kind (each kind a short
  lower-case word, at most 32 kinds), and a short failure code when the provider
  could not answer;
- the read model's own `error` code and `agents_truncated` flag;
- the configured code browser base (`cockpit.code_browser_url`), on the session response.

The metadata routes are `session`, `fleet`, `attention`, the action list, and
two added by [cockpit-views](../cockpit-views/README.md): `GET /api/v1/cockpit/branches?repository=<id>`
and `GET /api/v1/cockpit/machine-metrics?machine=<id>`, each of the same access
class as `fleet`.

It MUST NOT receive file content, file names, filesystem paths, diffs, commit
subjects or messages, task summaries, prompts or log bodies, and it MUST NOT
change anything. The same exclusion applies to data read from another
machine's snapshot, whose source carries several of those fields.

#### REQ: cross-origin-allowance

`/api/v1/cockpit/` MUST send `Access-Control-Allow-Origin` for exactly one
foreign origin, the origin of `cockpit.hosted_url`, only on the metadata
`GET` routes — `session`, `fleet`, `branches`, `machine-metrics`, `attention` and the action list — and
never with `Access-Control-Allow-Credentials`. Those responses carry
`Vary: Origin`. A preflight from that origin is answered with the allowance
and with `Access-Control-Allow-Private-Network: true`, and allows the request
header `If-None-Match`; the responses expose `ETag`, so that conditional
requests and `304` work from the hosted page
([cockpit-views](../cockpit-views/README.md)#req:hosted-origin-conditional-requests).
A request carrying any
other foreign `Origin`, including `null`, is refused with status 403.

A hosted page therefore reads metadata and nothing else, even in a browser
that holds an owner session cookie for the daemon. The option the founder
selected for the hosted page on 2026-10-01 stated this consequence: a
compromise of the hosted origin leaks names only.

### Owner session

#### REQ: owner-session

`wb cockpit` obtains a single-use login code over the daemon's owner-token
RPC on its unix socket. The code is valid for 60 seconds. It travels in the
query string of `/cockpit/session/login` on the canonical origin; the daemon
exchanges it for a session cookie that is `HttpOnly`, `SameSite=Strict`,
expires after 12 hours, and is named after the port the request arrived on,
so two daemons reached on one host — one of them through a forward — keep
separate sessions. The login response redirects to `/cockpit/`
so the code does not stay in the address bar. A used or expired code is
refused and establishes nothing.

Sessions are held in the daemon's memory. The cookie is scoped to the host,
not the port, so every HTTP server on the same loopback address receives it;
the session identifier is useless to them without the daemon, and it is
stored by the daemon only as a digest. `POST /cockpit/session/logout` ends
the caller's session, and every session ends when the daemon restarts.

This is the admin session
[peer-connectivity](../peer-connectivity/README.md)#req:admin-requires-owner-credential
describes; Cockpit provides it.

#### REQ: owner-routes

A route that returns content or changes state MUST require the session
cookie. A state-changing route MUST also require an `Origin` equal to the
canonical origin and a JSON content type. The login exchange is the one
exception: it is a `GET` protected by its single-use code. A request with no
session is refused with status 401, a session that lacks the required
capability with status 403, and neither has any effect.

### Principals and capabilities

#### REQ: capability-vocabulary

Every Cockpit route and every action declares one required capability. The
vocabulary in this Feature is:

- metadata: `fleet.read`, `machine.read`, `repo.read`, `worktree.read`,
  `branch.read`, `pr.read`, `agent.read`;
- content: `repo.content.read`.

`anonymous-local` holds the metadata capabilities. `owner` holds every
capability. Capabilities for actions are added by the Feature that defines
the action. Per-user grants, roles and a `peer` principal are not part of
this Feature.

#### REQ: effective-permissions-are-discoverable

`GET /api/v1/cockpit/session` MUST return the caller's principal and its
effective capabilities. The application MUST use that response, not its own
assumptions, to decide what to show, what to disable, and how to explain an
unavailable control.

### Read model

#### REQ: fleet-read-model

`GET /api/v1/cockpit/fleet` MUST return one versioned document
(`schema_version`, which is 2 since [cockpit-views](../cockpit-views/README.md)#req:schema-version-2)
with `snapshot_at` and these collections:

- **machines** — this machine, and every other machine whose snapshot is
  already in the local copy of the remote state store; the snapshot reads that
  copy and does not fetch it;
- **repositories** — with counts of worktrees, local branches, remote
  branches, open pull requests where known, and active agents;
- **worktrees** — task, repository, branch, owner state, last activity, and
  the fields [cockpit-views](../cockpit-views/README.md) adds;
- **branches** — not part of the document: they are served per repository by
  `GET /api/v1/cockpit/branches?repository=<id>`, and the document keeps each
  repository's branch counts
  ([cockpit-views](../cockpit-views/README.md)#req:lazy-branches-route);
- **pull_requests** — every open pull request recorded locally, without a
  network call, tied to
  its repository and, where one exists, its worktree;
- **agents** — registered sessions and dispatched agent runs.

Every entry carries a stable `id` unique within its collection, its
`machine`, a `route` and an `observed_at` time.

#### REQ: route-and-freshness-are-explicit

`route` is `local` for state this daemon observed itself, `live-remote` for
state it read in the background from another machine over the configured SSH route
([cockpit-views](../cockpit-views/README.md)#req:remote-ssh-fetch), and `cached` for
state read from another machine's published snapshot. A `cached` entry's
`observed_at` is the snapshot's publish time, and a `live-remote` entry's is the
remote snapshot's time. The application MUST show the
route and the age of every `cached` entry and MUST NOT render cached state as
live.

#### REQ: no-fleet-scan-on-the-request-path

The read model MUST be served from a snapshot the daemon refreshes in the
background. A request MUST NOT wait for a Git scan of every repository. A
request made before the first snapshot exists returns an empty, well-formed
document marked as warming up.

The snapshot is built from local state only and is read-only: it never contacts a network and never writes inside a repository. It is published incrementally, so the document is readable while the first pass is still running; `warming_up` stays true until that pass completes, and the document says how many repositories have been scanned.

#### REQ: snapshot-refresh

The daemon refreshes the snapshot on an interval, `cockpit.refresh_interval`.
A refresh MUST skip the Git work for a repository whose refs, index and
working-tree fingerprint are unchanged since the last one, so a quiet fleet
costs little. The default interval is set when the snapshotter is built, from
a measurement on a fleet of several hundred repositories. The daemon MUST
also be able to refresh one repository on request from inside the daemon, so
that a completed operation is reflected without waiting for the interval.

#### REQ: code-index-freshness-is-shown

Each repository and worktree entry carries `code_index`: for every configured
indexer, the state defined by
[code-index-freshness](../code-index-freshness/README.md)#req:freshness-in-fleet-status
— `fresh`, `stale` with the number of commits behind, `diverged`, `pending`,
`failed` or `never` — read from receipts as that Feature requires. The
application shows it on the Repositories and Worktrees tables. WB stays
indexer-agnostic; CodeGrapher is the indexer the founder uses.

A clone is matched to its indexer by the repository its `origin` names, derived
as the lifecycle-hook worker derives it (lower-case host/owner/name), whatever
its layout, so a flat clone with no host directory is matched like any other;
a clone with no origin, or an origin that names no forge, has no indexer. The
same origin supplies the `host` of a flat clone, which its placement lacks, so
its code-browser link works. The origin URL is never in the read model.

A shallow clone lacks history on purpose, so it gets only what it can prove: a
receipt at `HEAD` is `fresh`, a receipt commit that is present and is an
ancestor of `HEAD` is `stale` with its count, and one that is present and is not
an ancestor is `diverged`. A receipt commit the clone does not have gets no
state at all (the entry is left out and the page shows a dash), never a count it
cannot trust.

#### REQ: code-index-summary

A repository page and a worktree page MUST show a code-index panel for that
checkout: whether an index exists, its freshness, and its statistics — files,
symbols and edges, with symbols broken down by kind — as the configured
code-index provider reports them. CodeGrapher is the first provider. The
panel shows counts only, so it is metadata.

The statistics are part of the fleet read model, under each entry's
`code_index`. The background snapshotter asks the provider, through the
provider's own command, once per checkout per indexer receipt; no request
starts a provider process, and WB does not open the provider's artifacts. A
new receipt, such as the one a refresh writes, makes the snapshotter ask
again. The provider is named by `cockpit.code_index_provider` (`codegrapher`
is the one provider) and follows the indexer named by `cockpit.code_index_indexer`
(default `codegrapher`); its statistics sit on that indexer's `code_index`
entry. Statistics appear only for a checkout the configured indexer has a
receipt for: the provider's command opens the index read-write and may run Git,
which the snapshotter's read-only rule forbids for a checkout WB's hook never
indexed (it could hold an index a hostile repository committed), so such a
checkout is reported as not indexed and no process starts for it. A failed ask
is retried on later passes, at most three times per receipt. When no provider
is configured or the checkout has no index, the panel says so.

#### REQ: code-browser-link

Every repository row and repository page links to that repository in the
CodeGrapher browser, at `<base>/<host>/<owner>/<repository>` — for example
`https://codegrapher.dev/github.com/specscore/specscore-cli`. The base is the
single configuration value `cockpit.code_browser_url`, whose default is
`https://codegrapher.dev/`. The link opens in a new tab and is built from
metadata only, so it needs no owner session. A repository whose origin names
no forge host has no link. Cockpit does not check that the page exists.

### Pages and interaction

#### REQ: navigation

The application MUST provide Home (the former Dashboard, which `/dashboard`
still shows), Tasks, Repositories, Worktrees, Agents and Machines pages, and shows no visible page heading that repeats the tab.
Each list page is a table that can be filtered by machine and by a text filter
with wildcards (not by a repository dropdown). Merged repository rows and task
rows show a chip per machine that carries the age of cached data and a stale
mark, which satisfies REQ:route-and-freshness-are-explicit. The behaviour of
the pages — filtering, sorting, tabs, search, keyboard and detail pages — is
defined by [cockpit-views](../cockpit-views/README.md), which takes precedence
where it differs. The worktree page this Feature defines stays.

#### REQ: summary-hover-drill-down

Every count the application shows MUST be a link whose click opens the list
filtered to exactly those entities. A hover card that names the entities it
counts is no longer required
([cockpit-views](../cockpit-views/README.md)#req:every-number-is-a-link); one that
is shown contains no control that changes state. Where cockpit-views defines a
page's counts, such as the Home items and the tab badges, its definition takes
precedence.

#### REQ: repository-readme

A repository page MUST render the `README.md` committed at the tip of the
repository's default branch for a caller holding `repo.content.read`. Without it the page
says an owner session is needed and names the command that provides one.

The README is untrusted content shown in the origin that holds the owner
session. It MUST be rendered as sanitized Markdown: raw HTML, scripts, event
handler attributes and `javascript:` links are dropped. It is read from Git's
object store, never from the working tree; an entry that is a symbolic link or
not a regular file is not served.

#### REQ: strict-content-security-policy

Every response under `/cockpit/` MUST carry a content security policy that
allows scripts only from the daemon's own origin, with no `unsafe-inline` and
no `unsafe-eval`, and forbids framing by another origin. Styles are allowed
from the daemon's own origin and, for the style elements Angular and PrimeNG
inject at run time, through a nonce issued per response; `unsafe-inline` is
not used for styles either.

### Test coverage

#### REQ: new-code-is-fully-covered

All code this Feature adds MUST reach 100% test coverage (founder,
2026-10-01).

- **Go:** every statement in a new package and every added or changed
  statement in an existing one, measured by `wb coverage --changed`, the
  repository's existing gate.
- **`cockpit/web`:** the test run enforces thresholds of 100 for statements,
  branches, functions and lines over the application and library sources (`cockpit/web/apps/*/src`, `cockpit/web/libs/**/src`) and the build tools (`cockpit/web/tools`),
  excluding test files and the one bootstrap file. Coverage tooling does not
  measure Angular templates, so every component MUST also have a test that
  renders it.
- The `cockpit/web` check runs on every pull request and passes at once when
  nothing under `cockpit/web` changed, so it can be a required check.

## Dependencies

- [daemon-lifecycle](../daemon-lifecycle/README.md)
- [peer-connectivity](../peer-connectivity/README.md)
- [remote-state](../remote-state/README.md)
- [fleet-status](../fleet-status/README.md)
- [code-index-freshness](../code-index-freshness/README.md)

## Not Doing

- Retiring `internal/dashboard` or `hub/web`, or porting their pages — a
  later Feature, after the port is complete.
- Protecting the existing `/api/v1/*` routes — they keep today's behavior
  until that Feature. They share an origin with Cockpit and their pages allow
  inline scripts; they render no repository content. A script injected into
  one of those pages would run in the origin that holds the owner session;
  retiring them, or giving them the strict policy, closes that.
- Reaching Cockpit through a host name other than loopback. Remote access is
  an SSH port forward, to any local port, which keeps the `Host` on
  loopback. Exposure through a
  proxy or tunnel waits for the OAuth2 or OIDC provider decision 0002
  requires.
- Publishing the application at the hosted URL — this Feature makes the
  daemon accept the hosted origin; deploying the hosted copy is a later
  slice, and so is proving that each browser lets an https page read an http
  loopback address.
- A live event stream — the application re-reads the read model.
- Per-user grants, roles, and a `peer` principal.
- Directory pages. The founder asked for code-index information on
  repository, directory and worktree pages (2026-10-01); this Feature covers
  repository and worktree pages only, because the first slice has no file or
  directory browser. *Proposed* deferral, to the slice that adds one.
- Source and diff viewers, and code navigation inside them.

## Acceptance Criteria

### AC: command-opens-local-cockpit

**Requirements:** cockpit#req:cockpit-command, cockpit#req:owner-session

Scenario: First run on a machine with no daemon running
Given no WB daemon is running
When the operator runs `wb cockpit` in an interactive terminal
Then a daemon is running on the loopback address, the printed URL is on `http://127.0.0.1:<port>/cockpit/session/login` with a code, and following it once sets the session cookie and redirects to `/cockpit/` with no code in the address

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

### AC: release-build-includes-cockpit

**Requirements:** cockpit#req:embedded-application

Scenario: The release job
Given the release configuration
When its before-hooks run in a snapshot release, and again with the Cockpit build output removed after the build step
Then the first produces a binary whose `/cockpit/` serves the application, and the second fails naming the missing output

### AC: foreign-host-is-refused

**Requirements:** cockpit#req:host-header-check

Scenario: DNS rebinding, and a loopback alias
Given a daemon listening on `127.0.0.1:8766`
When a request reaches `/api/v1/cockpit/fleet` with `Host: attacker.example:8766`, and a browser requests `/cockpit/` with `Host: localhost:8766`
Then the first response has status 421 and no fleet data, and the second is redirected to `http://127.0.0.1:8766/cockpit/`

### AC: proxied-request-needs-a-session

**Requirements:** cockpit#req:forwarded-requests-are-never-anonymous

Scenario: Through a proxy that rewrites the host
Given a request with `Host: 127.0.0.1:8766`, `X-Forwarded-For: 203.0.113.9` and no session cookie, and separately a daemon configured with `cockpit.anonymous_metadata: false`
When each requests the fleet read model without a session
Then both are refused with status 401 and neither response contains fleet data

### AC: anonymous-local-gets-metadata-only

**Requirements:** cockpit#req:anonymous-local-reads-metadata-only, cockpit#req:owner-routes

Scenario: No session, with a local worktree and a remote snapshot that carry sensitive fields
Given a worktree with an untracked file and a commit whose subject is `secret subject`, and another machine's snapshot carrying paths, file names, commit subjects and a task summary
When a loopback request with no session cookie requests the fleet read model and then a repository's README
Then the read model is returned and contains none of those file names, paths, subjects or summaries, and the README request is refused with status 401

### AC: only-the-hosted-origin-may-read-cross-origin

**Requirements:** cockpit#req:cross-origin-allowance, cockpit#req:owner-routes

Scenario: Four origins
Given `cockpit.hosted_url` is `https://hosted.example.test/wb/cockpit/` and a browser holding an owner session cookie
When the fleet read model is requested with `Origin: https://hosted.example.test`, with `Origin: https://other.example.test` and with `Origin: null`, and `POST /cockpit/session/logout` is requested from the hosted origin
Then the first response allows that origin without credentials and with `Vary: Origin`, the second and third are refused with status 403, and the logout is refused and the session still works

### AC: login-code-is-single-use

**Requirements:** cockpit#req:owner-session

Scenario: Replay and expiry
Given a login code that has been exchanged once, and another issued 61 seconds ago
When each is presented to `/cockpit/session/login`
Then both are refused and no cookie is set

### AC: session-ends-on-logout-and-restart

**Requirements:** cockpit#req:owner-session

Scenario: Two ways a session ends
Given an owner session
When the session logs out, and separately when the daemon restarts
Then in both cases a following request with the old cookie is `anonymous-local`

### AC: session-reports-principal-and-capabilities

**Requirements:** cockpit#req:capability-vocabulary, cockpit#req:effective-permissions-are-discoverable

Scenario: Two principals
Given one request without a session and one with an owner session
When each requests `/api/v1/cockpit/session`
Then the first reports `anonymous-local` with only the metadata capabilities, and the second reports `owner` with `repo.content.read` as well

### AC: read-model-lists-local-state

**Requirements:** cockpit#req:fleet-read-model, cockpit#req:route-and-freshness-are-explicit

Scenario: One repository, two worktrees, a pull request, a session and a remote snapshot
Given a projects root with one repository that has two WB worktrees, one of them with recorded open pull request evidence, one registered agent session, and a remote-state snapshot published by a second machine
When the fleet read model is requested
Then it lists both machines, the repository with a worktree count of two, both worktrees and their branches with route `local`, the pull request tied to its worktree, the session among agents, every entry with a stable `id`, and the second machine's entries with route `cached` and `observed_at` equal to the snapshot's publish time

### AC: request-does-not-scan

**Requirements:** cockpit#req:no-fleet-scan-on-the-request-path, cockpit#req:snapshot-refresh

Scenario: Cold, warm and refreshed
Given a daemon that has not yet completed its first snapshot
When the fleet read model is requested, requested again after the snapshot completes, and requested a third time after a worktree is added and one refresh interval passes
Then the first response is well-formed, empty and marked as warming up, the second carries `snapshot_at`, the third lists the new worktree, and no request ran a Git command

### AC: code-index-freshness-appears

**Requirements:** cockpit#req:code-index-freshness-is-shown

Scenario: Three checkouts
Given a configured indexer, one checkout whose latest receipt is at `HEAD`, one whose receipt is three commits behind, and one with no receipt
When the fleet read model is requested and the Worktrees page is opened
Then they report `fresh`, `stale` with a count of three, and `never`, and the table shows the same three states

### AC: code-index-panel

**Requirements:** cockpit#req:code-index-summary

Scenario: Indexed, re-indexed, not indexed, and no provider
Given a fake code-index provider that reports 12 files, 40 symbols of two kinds and 90 edges for one checkout and no index for another, and a second daemon with no provider configured
When the indexed checkout's worktree page is opened twice with no owner session, a new indexer receipt is then written with the provider reporting 13 files and a snapshot refresh passes, the other checkout's page is opened, and a page on the second daemon is opened
Then the first page shows the three totals and the per-kind breakdown, the provider was asked once by the snapshotter and never by a request, after the new receipt the page shows 13 files, the other checkout's page says it is not indexed, and the second daemon's page says no provider is configured

### AC: repository-links-to-code-browser

**Requirements:** cockpit#req:code-browser-link

Scenario: Default and configured base
Given a repository `github.com/specscore/specscore-cli` in the read model
When the Repositories page is opened with no owner session, and again with `cockpit.code_browser_url` set to `https://code.example.test/`
Then the row links to `https://codegrapher.dev/github.com/specscore/specscore-cli` the first time and to `https://code.example.test/github.com/specscore/specscore-cli` the second, opening in a new tab

### AC: every-page-lists-its-collection

**Requirements:** cockpit#req:navigation, cockpit#req:route-and-freshness-are-explicit

Scenario: Six pages
Given a read model with entries in every collection, some of them cached
When each of the Dashboard, Tasks, Repositories, Worktrees, Agents and Machines pages is opened and the machine filter is applied on a list page
Then each page shows its entries, every cached row shows its route and age (a merged row through its per-machine chip), and the filter leaves only that machine's rows

### AC: counts-drill-down

**Requirements:** cockpit#req:summary-hover-drill-down, cockpit#req:light-and-dark

Scenario: From a count to its rows
Given the Repositories page showing a repository with a worktree count of two
When the operator hovers the count and then clicks it, in a browser set to dark mode
Then any hover card shown names both worktrees and has no button, the click opens the Worktrees page filtered to that repository showing exactly those two rows, and the page uses the dark theme

### AC: readme-needs-owner

**Requirements:** cockpit#req:repository-readme

Scenario: With and without a session
Given a repository whose default branch tip has a committed `README.md`
When its page is opened with an owner session and then without one
Then the first renders the README and the second shows that an owner session is needed and names `wb cockpit`

### AC: hostile-readme-is-inert

**Requirements:** cockpit#req:repository-readme, cockpit#req:strict-content-security-policy

Scenario: A README that tries to act
Given a repository whose `README.md` contains a `<script>` element, an image with an `onerror` attribute and a `javascript:` link, and another whose `README.md` is committed as a symbolic link
When each repository page is opened with an owner session
Then the first page renders the text with no script executed and no request made by the injected content, the response carries a policy without `unsafe-inline` for scripts, and the second page shows no content from outside the checkout

### AC: coverage-gates-hold

**Requirements:** cockpit#req:new-code-is-fully-covered

Scenario: The gates are configured and green
Given the repository after this Feature
When the Go changed-coverage check and the `cockpit/web` check run on a pull request that touches `cockpit/web`, and on one that does not
Then the Go check passes with every added statement covered, the web test configuration declares thresholds of 100 for statements, branches, functions and lines, every component has a rendering test, the web check passes on the first pull request and passes without running tests on the second

### AC: whole-journey-e2e

**Requirements:** cockpit#req:cockpit-command, cockpit#req:owner-session, cockpit#req:fleet-read-model, cockpit#req:summary-hover-drill-down

Scenario: The journey without crutches
Given a temporary projects root with one repository and two worktrees, and no daemon running
When one end-to-end test runs `wb cockpit`, follows the printed URL in a browser, hovers and clicks the worktree count, clears the cookie and reloads
Then the Dashboard appears signed in as owner, the filtered Worktrees table shows both rows, and after the reload the lists still load while the repository README asks for an owner session

## Open Questions

- `code-index-freshness` is a Draft Feature and its freshness report was not
  found in code on 2026-10-01. Building the part Cockpit reads is in this
  Feature's plan if it is still missing.

---
*This document follows the https://specscore.md/feature-specification*
