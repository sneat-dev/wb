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

The login URL is a credential: it carries the single-use code and the session
key (cockpit#req:session-key). The command MUST request and print it only when
stdout is a terminal, or when `--print-url` asks for it. Otherwise it requests
no login code, prints the plain Cockpit URL and says on stderr that
`--print-url` prints the login URL; it never writes the login URL to stderr or
to a log. `--print-url` with `--hosted` is a usage error.

A running daemon that answers a login code with no session key is an older wb.
The command MUST refuse the login with exit code 1 and the advice to run
`wb daemon restart`, and print and open nothing: a session that daemon starts
would make the cookie alone an owner.

`--listen <host:port>` names the loopback address of the daemon to start (default `127.0.0.1:8766`); without it a daemon already running on this machine is used wherever it listens. A running daemon recorded on a different address is never replaced: the command refuses and names that address. Non-loopback addresses are refused before anything starts.

`--hosted` resolves the hosted Cockpit URL instead and starts no daemon. The
hosted URL is the single configuration value `cockpit.hosted_url`, whose
default is `https://sneat.dev/wb/cockpit/`; no other code path may spell that
address.

`--format json` and its `--json` shortcut MUST print
`{url, scope, opened}`, where `scope` is `local` or `hosted`, and MUST NOT
launch a browser. The JSON `url` never contains a login code or a session key
(cockpit#req:session-key): it has no query string and no fragment. With
`--print-url`, and only then, the object also has `login_url`.

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
`/workbench/` and `/v0/workbench/` — are unchanged, with these exceptions.
`GET /api/v1/log` requires an owner session (cockpit#req:daemon-log-is-owner-only).
`GET /api/v1/health` and `GET /api/v1/overview`, which answer the machine's name, the daemon's
process id and the names of its worktrees (metadata, which a local reader may read without a
session), MUST apply the Host check (cockpit#req:host-header-check, the same rule, from one shared
function): a request whose `Host` does not name a loopback host is refused with status 421 and the
JSON body `{schema_version, error: "misdirected_request", message}` with a fixed message, so a page
that rebinds DNS to the loopback address reads neither. A daemon published through a tunnel must
therefore have the tunnel send a loopback `Host` to reach them; the static pages `/` and `/metrics`
hold nothing of the machine and are not checked. An overview that cannot be built is answered with
status 500, the closed code `overview_unavailable` and one fixed message; the error itself, which
names a path under the projects root, goes to the daemon's log (once for a failure that repeats)
and to no reader of the route, and the page shows a fixed text of its own.

#### REQ: embedded-application

The application is an Angular 22 project using the Angular
CDK, kept at `cockpit/web`, with Vitest for unit tests and Playwright for
end-to-end tests. This is the stack of the CodeGrapher web UI, chosen so its
components can be reused (founder, 2026-10-01). It has no component library
of its own, and no paid template or block (PrimeNG and its licence banner were
removed once no page used it). It MUST NOT use React. Its production build is
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
headers; WB cannot detect such a proxy. It also means this machine's metadata is
exported to another machine by no transport
([cockpit-views](../cockpit-views/README.md)#req:hub-export-route).

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
  `export_refused`, `http_unavailable`, `http_auth_failed`, `bad_payload`,
  `remote_warming_up`, `export_too_large`, `self_export` or `clock_skew`), never the
  remote's error text, the transport that supplied a machine's live entries
  (`transport`: `http` or `ssh`), the number of another machine's entries that were
  left out of its export or cut at this daemon's caps (`export_dropped`) and whether
  its agents were cut (`agents_truncated`), and, on the local machine's entry only,
  the code of its last failed or degraded periodic publish (`publish_error`:
  `collect_failed`, `store_unavailable`, `publish_failed` or
  `optional_fields_dropped`), never an error text;
- a machine's resource samples, served only by the `machine-metrics` route and
  never in the fleet document, which are numbers and times only: CPU percent,
  one-minute load, memory used and total bytes, free and total bytes of the
  projects-root disk, and the sample time, with the source of the answer
  (`route`: `local`, `live-remote`, `cached` or `none`) and, for `live-remote`,
  its fetch time (`fetched_at`)
  ([cockpit-views](../cockpit-views/README.md)#req:machine-metrics-route);
- repository forge host (split from the name of an entry mapped from another
  machine's snapshot when that name has three or more segments and the first
  contains a dot), `owner/name` as the name, default branch name, for a
  local repository the time of its newest local-branch activity, and
  `remote_url_web`, the `https://<host>/<owner>/<name>` address built from the
  host and `owner/name` alone and emitted only when the host matches a hostname
  pattern and every path segment matches `[A-Za-z0-9._-]+` and is not `.` or
  `..`;
- task name, stream name, branch name, lifecycle and owner state (`active`,
  `idle`, `orphaned` or `unknown`), last activity time, the worktree name (its
  task, never a path), and, for a worktree on the local machine only, its
  `ahead` and `behind` commit counts, its `upstream_gone` flag and whether it
  has an upstream (`has_upstream`); an owner state is one of those four values
  or absent, and a value outside them from another machine is dropped;
- pull request number, URL (only when it is `https` with a host of ASCII
  letters, digits, dots and hyphens, no port and no user information) and state
  (`open`, `merged`, `closed` or `draft`), the merge state GitHub reports as a
  closed set (`mergeable`), the counts of checks total, passed, failed, skipped
  and pending, the checks verdict (`checks_green`), the name of the first failing
  check (`failed_check`, at most 100 characters, control and bidirectional
  characters removed) and the time it was read (`checked_at`)
  ([cockpit-views](../cockpit-views/README.md)#req:pull-request-fields);
- agent run and session identifiers, runtime, model and state, the agent's
  `activity` (`working`, `blocked`, `idle`, `done` or `unknown`) when it is
  reported, the ids of the worktrees an agent works on, its task name, repository
  and its start time, the finish time and exit code of a finished run, a
  session's state (`live` or `parked`), and the same agent fields read from
  another machine's snapshot, at most 200 agents per machine;
- the sealed-work throughput block (`throughput`): the window in days, the
  number of tasks finished and dropped per day (and how many of the finished were
  sealed `landed`), at most five of the slowest finished tasks with their task name,
  duration in seconds and sealing time, the median and 90th-percentile finished
  durations, and whether the collector's bounds cut the scan;
- counts, durability levels, risk reason codes, and code-index freshness: per
  configured indexer its configured name, its state, for a stale index the
  number of commits behind, and the time of the receipt it was read from;
- code-index statistics: the name of the configured code-index provider (absent
  when none is configured), and per code-index entry whether an index exists,
  its totals of files, symbols and edges, the symbols per kind (each kind a short
  lower-case word, at most 32 kinds), and a short failure code when the provider
  could not answer;
- the read model's own `error` code and `agents_truncated` flag and `pull_requests_throttled` flag;
- the configured code browser base (`cockpit.code_browser_url`), on the session response.
- NOT in this list, and never sent to `anonymous-local`: the session response's field
  `machine_routes` (per machine with an SSH route, its `machine_id` and the `host`, optional
  `user` and `wb_path` of `session_move.targets.<machine>.ssh`) is OWNER-ONLY, emitted only to a
  session that holds the `owner` principal, because it names hosts and users that the metadata
  set does not
  ([cockpit-views](../cockpit-views/README.md)#req:copy-the-command).

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
not the port, so every HTTP server on the same loopback address receives it.
The cookie alone therefore proves nothing: an owner session is the cookie
together with the session key (cockpit#req:session-key). The session
identifier is stored by the daemon only as a digest.
`POST /cockpit/session/logout` ends the caller's session, and every session
ends when the daemon restarts.

This is the admin session
[peer-connectivity](../peer-connectivity/README.md)#req:admin-requires-owner-credential
describes; Cockpit provides it.

#### REQ: session-key

A browser sends a cookie of `127.0.0.1` to every server on that host, whatever
its port. Any other local server the owner visits in the same browser is
therefore sent the session cookie, and could replay it to the daemon from
outside the browser, with no `Origin` and any header it chooses. No check of a
header can stop that, so the cookie alone MUST NOT make a request the owner's.

With each login code the daemon mints a session key: 256 random bits, in
unpadded URL-safe base64. It answers the key beside the code on the owner
channel (the unix socket behind the owner token), which is the only place the
daemon ever writes it, and keeps only its SHA-256 digest: with the pending code,
and then with the session that code starts. A new login has a new key; logout,
expiry and a daemon restart drop the digest with the session.

`wb cockpit` puts the key in the fragment of the login URL it prints and opens:
`/cockpit/session/login?code=<code>#key=<key>`. A browser sends a fragment to no
server (and never in a `Referer`), and carries it over the login redirect to
`/cockpit/`. The page MUST take the key out of the address before anything else
reads the address, with `history.replaceState`, so it is not in the address bar
or in the history entry the page leaves; it MUST keep the key in the local
storage of its own origin, and MUST send it in the request header
`X-Wb-Cockpit-Session-Key` on every request to its own origin's API, and to no
other address. An origin includes the port, so a page served by another local
server cannot read that storage.

The owner principal is: a local reader (cockpit#req:host-header-check, the
canonical origin or none), AND a live session cookie, AND exactly one
`X-Wb-Cockpit-Session-Key` header whose digest equals the session's, compared in
constant time. A request with the cookie and no key, a wrong key, another
session's key or the header twice is not an error: it is treated exactly like a
request with no cookie. It is `anonymous-local` where that principal exists and
is refused with status 401 on an owner route, through a proxy, and where
`cockpit.anonymous_metadata` is off. The key MUST NOT appear in any response of
the loopback listener (body or header), in any log line, or in JSON output.

The page takes a key from the fragment only when it is served on a loopback
host, so the hosted page never receives or uses one, and it keeps an offered key
only after the daemon has answered that the key opens an owner session: an
address anybody can write (`/cockpit/#key=...`) cannot replace the key of a
signed-in owner. It forgets the key it holds when the daemon answers that the
key no longer opens one.

A reload and a second tab are the owner's without a new login. The local storage
of the origin is shared by its tabs and survives a reload, and it is as safe
against another port as per-tab storage is, because the boundary is the origin
in both; the alternative, per-tab storage, would ask for `wb cockpit` again in
every new tab for no gain. Where the browser gives the page no storage, the key
is held by the tab alone and a reload asks for `wb cockpit` again. A tab that
has the cookie and no key shows the anonymous session chip, whose card says
"Sign in as owner: run `wb cockpit`" with its copy button.

A client that is not the Cockpit page and wants an owner-only route sends both
halves itself: the `Cookie: wb_cockpit_session_<port>=<id>` the login set and
`X-Wb-Cockpit-Session-Key: <key>`. An address typed into the browser carries the
cookie and cannot carry the header, so it is an anonymous reader's.

What this does not cover. The login URL, with its key, is printed on the
terminal (and only there, unless `--print-url` asks: cockpit#req:cockpit-command),
is an argument of the command that opens the browser, and may be kept by the
browser's history from the moment it is opened until the page replaces it. Each
of those copies is one half. The key opens nothing until the single-use code
beside it has been redeemed, which must happen within 60 seconds, and then only
together with the cookie, which the browser that redeemed the code alone holds.
A process that runs as the owner on the machine can read both halves, as it can
read the owner token; that is outside what a loopback service defends.

#### REQ: same-origin-pages-run-no-injected-script

The key is in storage that every page of the daemon's origin can read, so a
script injected into any of them would have it. Every page the daemon's listener
serves is therefore held to three rules.

- No data becomes markup. The dashboard's pages (`/` and `/metrics`) MUST build
  what they show with `createElement` and `textContent`: no value read from a
  route is concatenated into HTML, a class name is chosen from a closed set (an
  unknown status is shown as `neutral`), a link's address is either built by the
  page as `https://` and a repository name or accepted only when it parses as
  `https`, and there is no inline event handler: a control names what it acts on
  in `data-` attributes that one listener reads. This holds for any stored
  value, including records written before the write side validated anything.
- No inline script runs. Every response of the listener that does not set its
  own policy carries `script-src 'self'` with no `'unsafe-inline'`,
  `object-src 'none'` and `base-uri 'self'`; the dashboard's scripts are files
  of the origin under `/dashboard-assets/`. The GitHub installation opener page
  runs its one script by a nonce minted per response. The one exception is the
  bench dashboard under `/workbench/`, whose Astro build emits inline scripts:
  it sets its own policy, which still allows them, and it renders stored values
  with `textContent` and takes a link's address only from a parser that accepted
  it as an `http(s)` address.
- The write side refuses markup. A metric or coverage record whose field is
  outside its form is refused with status 400 and the closed code
  `invalid_metric_record` or `invalid_coverage_record`, which repeats nothing of
  the record, and is not stored: the repository is `owner/name`, the status one
  of its closed set, the metric type, owner, name, ref and commit short
  identifiers, the formatted value a number with a unit, the workflow run
  address `https`, and metadata values and dimension details numbers, booleans
  or short text with no angle bracket, quote, backtick, backslash or control
  character and no nested value.

#### REQ: owner-routes

A route that returns content or changes state MUST require the owner session:
the session cookie and its session key (cockpit#req:session-key). A
state-changing route MUST also require an `Origin` equal to the
canonical origin and a JSON content type. The login exchange is the one
exception: it is a `GET` protected by its single-use code. A request with no
session is refused with status 401, a session that lacks the required
capability with status 403, and neither has any effect.

#### REQ: daemon-log-is-owner-only

The rule for everything the daemon's listener serves is the founder's
(2026-10-02): anything that changes state or returns the content of a file
requires authentication; a list of repositories, agents and the like, read on
the loopback address, does not. The daemon's runtime log is file content, so
`GET /api/v1/log`, which the dashboard serves outside Cockpit's two subtrees,
MUST be served to an owner session and to nobody else. It asks Cockpit who the
owner is and has no second mechanism: the request's `Host` MUST name a loopback
host (cockpit#req:host-header-check), it MUST come from the canonical origin or
carry no `Origin` at all, never from the hosted origin or any other, and it MUST
carry a live session cookie (cockpit#req:owner-session) and that session's key
(cockpit#req:session-key). Neither the daemon's owner token, nor the cookie
alone, nor arriving on the loopback address makes a request the owner's.

Any other request is refused with status 401 and the JSON body
`{schema_version, error: "owner_session_required", message}`, where `message`
is a fixed text naming `wb cockpit`. The refusal comes before anything else:
the log file is not opened, the `tail` parameter is not examined, and the
answer does not say whether a log path is configured. A daemon built with no
owner check refuses every request with status 403 and
`error: "log_owner_check_unavailable"`; it never serves the log without one.
Every answer of the route, the log itself included, carries
`Cache-Control: no-store`.

To the owner, a log that cannot be opened, inspected or positioned is status
503 with `error: "log_unavailable"` and a fixed `message`. The error text of the
operating system, which names the file's path, MUST NOT be in the body.

The operator reads the log by reading the file on the machine itself
(`~/Library/Logs/wb/daemon.log` under launchd, `daemon.log` in the daemon's
runtime directory elsewhere). The route is for a client that holds both halves
of an owner session (cockpit#req:session-key): a request from the signed-in
Cockpit page, which adds the key, or another client that sends the session
cookie and the `X-Wb-Cockpit-Session-Key` header itself. The address typed into
a browser, `http://127.0.0.1:<port>/api/v1/log`, carries the cookie and no key
and is refused like any request with no session. A reverse proxy or script that
fetched the route with no session is refused; there is no unattended credential
for it.

Because the log is the owner's alone, it may hold text the fleet document may
not, such as the end of a failed `ssh` call's stderr
([cockpit-views](../cockpit-views/README.md)#req:remote-ssh-fetch).

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
state it read in the background from another machine over its configured HTTP route, or
over SSH as the fallback
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

The snapshot is built from local state only and is read-only: it never contacts a network and never writes inside a repository. It is published incrementally, so the document is readable while the first pass is still running; `warming_up` stays true until that pass completes, and the document says how many repositories have been scanned. A warm-up always ends. When the repositories cannot be listed and no listing has ever worked, the first pass has nothing more to learn: `warming_up` becomes false and the document, which is empty, carries the closed code `repositories_unreadable` in `error`, so that a client that polls faster while the document warms up stops, and a reader of this machine is told that its export failed instead of being told `warming_up` for ever. The first listing that works then starts the first pass, and the document warms up again until that pass completes.

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
from the daemon's own origin and, for the style elements Angular injects
at run time, through a nonce issued per response; `unsafe-inline` is
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
- Protecting the existing `/api/v1/*` routes other than `GET /api/v1/log`
  (cockpit#req:daemon-log-is-owner-only) — they keep today's behavior
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
Then stdout is one JSON object with `url`, `scope` equal to `local` and `opened` equal to false, the URL has no query string and no fragment, there is no `login_url`, no login code is requested, and no browser is launched

### AC: login-url-is-printed-only-where-asked

**Requirements:** cockpit#req:cockpit-command, cockpit#req:session-key

Scenario: A terminal, a pipe and a request
Given a running daemon that issues session keys, and one that is an older wb and issues none
When `wb cockpit` runs with stdout a terminal, with stdout a pipe, with `--print-url` to a pipe, with `--format json`, with `--format json --print-url`, with `--hosted --print-url`, and against the older daemon
Then the terminal and `--print-url` runs request one login code and print the login URL with its key in the fragment, the pipe run requests none, prints the plain Cockpit URL and names `--print-url` on stderr, the JSON run has no `login_url` and the JSON `--print-url` run has it, stderr never holds the code or the key, `--hosted --print-url` is a usage error with exit code 2, and the older daemon is refused with exit code 1 and the advice `wb daemon restart`, with nothing printed or opened

### AC: dashboard-pages-create-nothing-from-data

**Requirements:** cockpit#req:same-origin-pages-run-no-injected-script

Scenario: A hostile value in every field
Given the dashboard's `/` and `/metrics` pages as the daemon serves them, and routes that answer metric, coverage, metric type, worktree, command-cost and machine records with `<img src=x onerror=…>`, `'");alert(1)//`, an attribute break, a class break, a closing tag with a script and a `javascript:` address in every field, of every type the field could have
When the pages load, every metric type tab is opened, every breakdown is expanded, a repository is opened from its Breakdown button and the filter is typed into
Then every value is on the page as text, the document holds no element, attribute, class or address that the page's own markup and script do not create, the only script element is the page's own file, a status outside the closed set is the neutral pill, an address that is not `https` is not a link, each page's markup has no inline script and no inline event handler, and every response carries a policy whose `script-src` is `'self'` alone

### AC: records-with-markup-are-refused

**Requirements:** cockpit#req:same-origin-pages-run-no-injected-script

Scenario: Markup in a metric or a coverage report
Given a hub with a metrics store and a coverage store
When a metric and a coverage report are posted with markup, a quote break, a backslash, a line break or a nested value in each field in turn, and the stores' save is called with the same records directly
Then each post is answered status 400 with exactly `{"error":"invalid_metric_record"}` or `{"error":"invalid_coverage_record"}`, each save returns a refusal that names the field and never its value, nothing is stored, and a record as a real reporter sends it is stored

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

### AC: dashboard-json-routes-answer-only-on-loopback

**Requirements:** cockpit#req:cockpit-mount, cockpit#req:host-header-check

Scenario: A rebinding page, and an overview that fails
Given a daemon serving its dashboard routes, and a worktree whose run telemetry cannot be read
When `GET /api/v1/health` and `GET /api/v1/overview` are requested with a `Host` that names another host, with each of the three loopback names, and the overview is requested twice
Then a foreign `Host` is refused with status 421 and `misdirected_request` and is told nothing of the machine, the loopback names are served, and the failed overview answers `overview_unavailable` with one fixed message that names no path while the reason is logged once

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

### AC: replayed-cookie-is-not-the-owner

**Requirements:** cockpit#req:session-key, cockpit#req:owner-routes, cockpit#req:daemon-log-is-owner-only

Scenario: Another local server replays the session cookie
Given a browser that signed in with `wb cockpit`, a daemon whose session response has `machine_routes`, an owner content route and a runtime log, each holding a marker
When the session route, the owner content route, `GET /api/v1/log` and the logout route are requested with the live session cookie and no `X-Wb-Cockpit-Session-Key`, with a wrong key, with the key cut short, with another session's key, with the key twice, with the key as a bearer or in a cookie, and with the key and no cookie, with and without the canonical `Origin`
Then the session route answers status 200 with the principal `anonymous-local` and no `machine_routes`, the owner routes and the log answer status 401, no response contains a marker, the session is not ended, and the same cookie with its key is then the owner on every one of those routes

### AC: session-key-reaches-the-page-in-the-fragment

**Requirements:** cockpit#req:session-key, cockpit#req:cockpit-command

Scenario: Following the printed login URL
Given `wb cockpit` in text format on a running daemon
When the printed URL is opened in a browser
Then the URL is `/cockpit/session/login?code=<code>#key=<key>` with the key in the fragment and nowhere in the path or query, no request is sent the key in its address, the page is an owner session, the address bar and the history entry show `/cockpit/` with no fragment, and every later request to the origin's API carries the key in `X-Wb-Cockpit-Session-Key`; a fragment whose key the daemon does not take is removed from the address, is not kept, and does not end a signed-in owner's session

### AC: a-reload-and-a-second-tab-keep-the-owner-session

**Requirements:** cockpit#req:session-key

Scenario: The same origin, again
Given a browser tab that signed in with the printed login URL
When the tab is reloaded, and a second tab of the same origin is opened on an address with no fragment
Then both are an owner session, and a browser that holds the session cookie but no key shows the anonymous session chip with "Sign in as owner: run `wb cockpit`" and its copy button, and no error

### AC: session-key-ends-with-its-session

**Requirements:** cockpit#req:session-key, cockpit#req:owner-session

Scenario: Rotation, logout and expiry
Given an owner session and its key
When a second login is made, the second session is logged out, and twelve hours pass
Then the second session has a different key and the first key does not open it, the logged-out and the expired session answer 401 on every owner-only route with their cookie and key, the daemon holds no digest of an ended session after the next login, and the page forgets a key the daemon no longer takes

### AC: session-key-is-never-served

**Requirements:** cockpit#req:session-key

Scenario: Searching every answer for the key
Given a minted login code and its key
When the code is exchanged and the session, fleet, page, owner, log, logout and preflight routes are requested as the owner, with the cookie alone, with a wrong key, anonymously, from the hosted origin, from a foreign origin, through a proxy and on a foreign host
Then no response body or header of the loopback listener and no log line contains the key or its digest, the login redirect names `/cockpit/` with no fragment, the daemon's pending codes and sessions hold the digest and never the key, and `wb cockpit --format json` prints neither a code nor a key

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

### AC: daemon-log-needs-an-owner-session

**Requirements:** cockpit#req:daemon-log-is-owner-only, cockpit#req:owner-session, cockpit#req:host-header-check

Scenario: The daemon's log holds a marker
Given a daemon whose runtime log holds a marker line, and a browser that signed in with `wb cockpit`
When `GET /api/v1/log` is requested with no cookie, with an unknown session cookie, with the session cookie and its key and `Host: attacker.example:8766`, with the session cookie and its key and the hosted or another foreign `Origin`, with the daemon's owner token as a bearer, after the session expired or was logged out, and then with the live session cookie and its key on the canonical origin
Then every request but the last is refused with status 401, `error: "owner_session_required"` and `Cache-Control: no-store`, none of those responses contains the marker or the log's path, the log file is not opened for them, and the last response is the log's tail with the marker and `Cache-Control: no-store`

### AC: daemon-log-fails-closed-without-an-owner-check

**Requirements:** cockpit#req:daemon-log-is-owner-only

Scenario: A handler built without the owner check
Given the dashboard handler built with a log path and no owner check
When `GET /api/v1/log` is requested
Then the response has status 403 with `error: "log_owner_check_unavailable"`, the log file is not opened, and the response contains nothing of the log

### AC: daemon-log-error-names-no-path

**Requirements:** cockpit#req:daemon-log-is-owner-only

Scenario: A log that cannot be read
Given an owner session and a log path whose file is missing, cannot be opened, or cannot be inspected
When `GET /api/v1/log` is requested
Then the response has status 503 with `error: "log_unavailable"` and a fixed message, and the body contains no part of the file's path

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
When one end-to-end test runs `wb cockpit`, follows the printed URL in a browser, walks Home, the five tabs, a list's filter, chip, panel and detail route, the palette and "New task", clicks the local machine's worktree count, clears the cookie and reloads
Then Home appears signed in as owner with its sections, each tab lists the rows of the real fleet, the filtered Worktrees list shows both rows, the local machine's metrics are drawn as charts or said not to be reported, and after the reload the lists still load while the repository README asks for an owner session (the page steps are `cockpit/web/apps/cockpit-e2e/src/journey/steps.ts`, the journey runs on Linux CI only)

## Open Questions

- `code-index-freshness` is a Draft Feature and its freshness report was not
  found in code on 2026-10-01. Building the part Cockpit reads is in this
  Feature's plan if it is still missing.

---
*This document follows the https://specscore.md/feature-specification*
