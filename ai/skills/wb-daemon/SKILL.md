---
name: wb-daemon
description: Serve and inspect WB's local operations API and web dashboard. Use when reviewing worktree activity and governed command cost, or when publishing a loopback WB service through an authenticated Cloudflare Tunnel.
---

# WB daemon

Open the hosted cross-machine dashboard in the platform browser:

```sh
wb dashboard
```

Use `wb dashboard --local` to start or reuse this machine's daemon and open its
loopback view. `wb dashboard --format=json` and non-interactive invocations
return the resolved URL without launching a browser.

Find Cockpit's address, or open it. Agents run the JSON form:

```sh
wb cockpit --format=json
wb cockpit
wb cockpit --hosted
```

`wb cockpit --format=json` (or `--json`) starts or reuses the loopback daemon and
prints `{url, scope, opened}` with the plain Cockpit URL: no login code, no
browser. Bare `wb cockpit` is for the human operator: it requests a login code
over the owner channel and prints `/cockpit/session/login?code=...#key=...`. The
code is a single-use owner credential valid for 60 seconds and the key in the
fragment is the session key that stays valid for the session, and an agent
running it would put both in its transcript. The browser opens only in text format, on an
interactive session whose stdout is a terminal. `--hosted` uses
`cockpit.hosted_url` from wb.yaml and starts no daemon. Without `--listen` it
uses a daemon already running here wherever it listens, else starts one on
`127.0.0.1:8766`. `--listen <host:port>` (loopback only) names the address to start
on; it never moves a running daemon: if one is recorded on another address the
command refuses and names it (use `--listen` with that address, or stop it first).

Read this machine's Cockpit metadata as one JSON envelope, which is what another
machine's daemon runs over SSH:

```sh
wb cockpit export --format json
wb cockpit export --format json --metrics-only
```

It prints `{schema_version, machine, exported_at, fleet, metrics}` (without `fleet`
for `--metrics-only`) read from this machine's running daemon over its loopback
listener as the anonymous-local reader, so it carries only the metadata set. It never
starts a daemon, opens a browser or mints a login code: with no daemon running it prints
`{schema_version, error}` with `daemon_not_running`, with a daemon that refuses
anonymous reads (`cockpit.anonymous_metadata: false`) `export_refused`, with a daemon
whose first scan has not finished `warming_up` (`--metrics-only` is not affected), and for
any other failure `export_failed`, all with exit code 1 and fixed text. An entry the
envelope's rules refuse is left out and counted in the envelope's `dropped` field. On macOS a daemon is
found through launchd, so one started by hand in the foreground reads as not running.
Run `wb daemon start` first if the daemon is not running.

The page Cockpit opens is Home; its tabs are Home (`/`), Tasks (`/tasks`, with `/tasks/new`),
Repositories, Worktrees, Agents and Machines, each a filterable list whose address holds `q`,
`sort`, `machine`, `chips` and `sel`, with a detail route (`/worktrees/<id>`, `/machines/<id>`, ...).
Cockpit runs nothing: a row offers "Copy command" text, and an owner session (`wb cockpit`) is what
file content (a README) and the SSH form of a copied command need. Its configuration in wb.yaml is
`cockpit.refresh_interval`, `cockpit.anonymous_metadata`, `cockpit.pull_request_limit`,
`cockpit.pull_request_hourly_budget`, `cockpit.remote_http` and `cockpit.remote_ssh` (read other machines'
exports over HTTP or SSH), `session_move.targets.<machine>.http` (`url`, `token_file`) and `.ssh` (`host`,
`wb_path`) for each machine's route, and `remote.publish.interval`, `.agents` and `.metrics` for the opt-in
periodic publish. The architecture, trust rule, cadences and budgets are in `docs/cockpit.md`.

Start and inspect the local read-only API and embedded dashboard:

```sh
wb daemon start
wb daemon status --format json
wb daemon stop
wb daemon restart --if-running
```

On macOS the daemon's launchd service has one fixed label per user, so starting
the daemon for one projects root would silently remove a service registered
for another. `wb daemon start` and `wb daemon restart` therefore read the
installed plist first and refuse, changing nothing, when it serves a different
projects root or cannot be read; the error names that root and its listen
address. Only `--replace-other-root` (never an environment variable) lets the
replacement proceed. `wb cockpit`, `wb dashboard --local` and the commands
that start the daemon implicitly have no such flag: they refuse and tell you to
run `wb daemon start --replace-other-root` first.

`wb daemon status` reports identity, not just reachability: read `identity`,
`ready_verified` and `reported_state` before believing `state=ready`. A daemon
that answers on the loopback port but belongs to another WB home, or that was
recorded before the home was, is reported as `identity=foreign_home`,
`identity=unrecorded` or `identity=process_recycled` with `reported_state`
`unverified`. `legacy_runtime` names a daemon still serving this build's
pre-resolver runtime directory, and `wb daemon start` refuses while one is live
rather than starting a second daemon on another home. Stop a leftover daemon
under the WB home it belongs to; never delete the directory out from under it.

If a lifecycle command reports an interrupted transition, inspect it before
retrying:

```sh
wb daemon recover --format json
wb daemon recover --apply --format json
```

Recovery is a dry-run unless `--apply` is explicit. It refuses a live or
ambiguous owner; a non-terminal startup or drain is recoverable only when WB
can prove and fence the interruption. Apply clears stale ownership and also
reconciles durable lifecycle state: it promotes an exactly verified healthy
child to ready, or marks a proven interrupted start or drain stopped. The
lifecycle lock uses one stable
kernel-locked inode plus an atomically replaced owner record, so a killed WB
process releases exclusion without corrupting its evidence. Never delete
`daemon.lifecycle.lock` by hand. Windows remains fail-closed until WB can
verify owner-only ACLs for these records.

For foreground debugging, run `wb daemon serve`.

Start a normal execution worker inside the caller or harness sandbox. Give it a
stable ID and only the canonical roots it may execute within:

```sh
wb worker connect --id codex-local --root /absolute/projects/root
```

`wb run --async --worker codex-local -- <command>` submits long-running work
without blocking the caller and binds it to that stable worker ID. The daemon
never chooses another worker even when roots and capacity match. It journals
argv, so secrets belong in the worker's inherited environment and never in
argv. The worker independently checks the cwd, executes with its inherited
sandbox and environment, and returns a bounded receipt. A reconnect creates a
new generation of the same stable identity. Queued work survives daemon
restart; an interrupted running lease becomes `recovery_required`.

WB tries the protected local socket first. If the harness sandbox returns a
permission or reachability error, the client reports that it is using the
project-root file bridge. Do not move the worker outside the sandbox. The bridge
uses owner-only atomic request and response envelopes under the `runtime/`
directory of this WB home (`~/.wb/runtime/file-bridge` by default; run
`wb daemon status` for the path this invocation resolves); every envelope is
authenticated and fenced to the scheduler generation and explicit worker ID. It
carries no environment overrides. Authentication, protocol, or generation
failures never trigger fallback or local execution.

Raw command submission from the daemon process is a trusted fallback and is
disabled by default. An administrator may opt in by creating
`~/.config/wb/daemon-raw-exec.json` outside the projects root with mode `0600`
and exactly this policy:

```json
{"version":1,"allow_raw_daemon_execution":true}
```

Agents and WB commands must never create or enable this policy. The daemon
revalidates it for every submission and immediately before launching queued
work, so deleting or invalidating it revokes permission without a restart.

```sh
wb daemon operation submit --format json -- go test ./internal/worktrees
wb daemon operation get <operation-id> --format json
wb daemon operation wait <operation-id> --timeout 15m
wb daemon operation cancel <operation-id>
```

The raw policy applies only to `wb daemon operation submit`. Normal `wb run
--async` jobs do not need it and never persist the worker's environment.

The default URL is `http://127.0.0.1:8766`. Keep the daemon on loopback. To
reach it from another registered machine, route that local endpoint through a
Cloudflare Tunnel protected by Cloudflare Access service authentication.
`/api/v1/health` and `/api/v1/overview` answer only a request whose `Host`
names a loopback host (421 `misdirected_request` otherwise), so have the tunnel
send one (cloudflared: `httpHostHeader: 127.0.0.1:8766`).

`wb daemon start` is idempotent. If the managed listener belongs to an older
installed WB executable, it drains the old generation and hands the durable
queue owner record to the installed executable. `stop` and `restart` preserve
that handoff record; `restart --if-running` is safe for the verified
self-update path because it never starts a daemon that was absent. Every
lifecycle transition keeps the same lock inode and atomically records its owner
PID in a sidecar while holding a kernel lock; process death releases the kernel
lock without losing or partially rewriting the recovery evidence.

The dashboard stays on read-only loopback HTTP. Mutating operation RPCs prefer
a separate mode-0600 Unix socket plus the private lifecycle owner token.
Sandbox-denied socket calls use the authenticated owner-only project-root file
bridge without moving execution outside the sandbox. Windows builds expose the
equivalent named-pipe endpoint contract and fail closed until either its native
adapter or owner-only bridge ACL verification is available; WB never falls back
to TCP.

The bridge uses a separate owner-only integrity key that survives daemon owner
token and generation rotation. A lost Submit response is replayed only with the
same envelope-bound idempotency key. Ambiguous worker mutation RPCs require
reconnect and are never replayed; bounded stale envelope cleanup preserves a
seven-day Submit recovery window.

There is no remote mutation endpoint. Normal jobs are leased only to compatible
registered workers over the protected local Connect RPC transport. The queue
persists bounded output and command digests and rejects environment overrides.
Reusing an idempotency key
with different cwd, argv, environment, or CPU units is rejected. Use the
dashboard and `/api/v1/*` read models for machine, worktree, and
governed-command visibility.

`GET /api/v1/log` (the tail of the daemon's runtime log) is file content and is
served to an owner session only. An owner session is two halves, the cookie
`wb_cockpit_session_<port>` and the session key in the request header
`X-Wb-Cockpit-Session-Key` (`wb cockpit` prints the key in the login URL's
fragment and the Cockpit page sends it); the cookie alone, an address typed
into a browser included, answers `401 {"error":"owner_session_required"}`. An agent that needs the log reads the
file on the machine (`~/Library/Logs/wb/daemon.log` under launchd, `daemon.log`
in the daemon's runtime directory elsewhere); it does not fetch the route.
