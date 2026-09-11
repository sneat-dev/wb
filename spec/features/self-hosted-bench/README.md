---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Self-hosted bench

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-hosted-bench?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-hosted-bench?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-hosted-bench?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/self-hosted-bench?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

`wb daemon serve` becomes the self-hosting unit for bench, as decided in
[decision 0002](../../decisions/0002-bench-open-source-and-self-hosting.md).
On a loopback address it hosts the hub (the GitHub App, installation,
repository-event and status services from `hub/`) and the embedded dashboard
from `hub/web/`, on a DALgo store chosen by configuration. By default it needs
no GitHub App and no public URL: a poller with the operator's own GitHub
token detects default-branch pushes and repository renames and enqueues them
for the local machine. Registering a GitHub App and receiving webhooks is
opt-in, through a tunnel the operator provides.

## Problem

Bench exists today only as the hosted instance at sneat.work/bench, mounted
inside sneat-co/sneat-go on sneat identity and Firestore. A developer who
wants the operational view and the automatic canonical-clone refresh without
a hosted account has nothing to run. The hub code is public and the daemon
already consumes hub events, but the two only meet across the internet and
only with a GitHub App installed. Self-hosting must mean "run wb", not
"deploy a service", or nobody will do it.

## Journey

I run `wb daemon start` on my laptop as I do today. I add one line to
`~/.config/wb/wb.yaml` giving the daemon a GitHub token that can read my
repositories, and restart it. Nothing else is registered anywhere.

I open http://127.0.0.1:<port>/bench/dashboard/ in a browser and see my
machine, my repositories and my worktrees, the same dashboard the hosted
instance shows, with no sign-in step because only this machine can reach it.

I push to main on one of my repositories from another computer. Within the
polling interval the daemon notices the default branch moved, and the
canonical clone on my laptop fast-forwards while every feature worktree stays
untouched. `wb daemon status` shows the event received and acknowledged. If I
rename the repository on GitHub, the daemon relocates the canonical clone the
way it does for hosted events today.

If I later want push latency instead of polling, I register a GitHub App,
point its webhook at a tunnel I run with my own cloudflared or ngrok
credentials, and give the daemon the App's secrets. Events then arrive by
webhook and the daemon polls nothing: only the repository an event names is
pulled. If I stop the tunnel, deliveries wait in GitHub's retry queue and
nothing is lost; there is no polling fallback because it would spend the
per-user API budget every tool shares (founder, 2026-09-11).

## Behavior

### Command surface

- `wb daemon serve` gains the hub. When `hub:` is present in `wb.yaml` the
  loopback server mounts the hub API under `/v0/workbench/` (the existing
  `hub.HandlerOptions` routes) and the dashboard under `/bench/`, next to
  the existing `/api/v1/` read-only API. Without `hub:` the command behaves
  exactly as today.
- `wb daemon status` reports the hub: store engine, listen address, polling
  interval, repositories polled, webhook mode on or off, last event received
  and acknowledged.
- No new top-level command. `wb daemon start`, `stop`, `restart` manage the
  same process. The capability entries for `wb daemon serve` and
  `wb daemon status` describe the additions.

### Configuration

```yaml
hub:
  store:
    engine: memory | ingitdb | openvaultdb        # default: ingitdb
    path: ~/.wb/hub                               # ingitdb directory
    url: http://127.0.0.1:8080                    # openvaultdb server
  github:
    token_file: <absolute private path>           # required for polling
    poll_interval: 20m                            # bounded, default 20m
    app:                                          # optional: webhook mode
      app_id: 123
      private_key_file: <absolute private path>
      webhook_secret_file: <absolute private path>
      public_url: https://<tunnel-host>           # where GitHub delivers
```

Secrets are read from files, never from the config value or argv, matching
`remote.token_file`. `engine: memory` is for tests and throwaway runs and
says so in `wb daemon status`. The store is a DALgo `dal.DB` wrapped by
`api/githubapp/dalgostore`; the hosted instance keeps dalgo2firestore and is
unaffected.

### Local machine identity

The loopback hub trusts only the loopback. On first start with `hub:`
present, the daemon enrolls the local machine against its own hub with a
generated credential kept under the private state directory, and configures
the in-process `remote` provider to `provider: hub` at the loopback URL. The
`remote.url` validation accepts plain `http://` only for loopback hosts. The
dashboard's viewer resolver in local mode returns a single fixed identity;
there is no sign-in. Binding the hub to a non-loopback address is refused
until an OAuth2 or OIDC provider is configured, which is a later feature.

### Polling ingester

For every repository in the machine's published inventory the poller reads
`GET /repos/{owner}/{repo}` and, when `default_branch` or `full_name` differ
from the last observed values, or the default branch's head SHA differs, it
enqueues the same `repositoryevent.Event` the webhook path would produce
(`default_branch_updated` with `TargetSHA`, or `repository_renamed` with
`PreviousRepository`) through the hub's repository-event store for the local
machine. Event IDs are derived from repository, reason and target so replays
deduplicate. The poller respects GitHub's rate-limit headers, backs off on
errors, and never runs more than one request per repository per interval.
The poller only exists without an App: in webhook mode it is not started.

### Webhook mode

When `hub.github.app` is configured the hub verifies deliveries exactly as
the hosted instance does and the operator's tunnel or reverse proxy forwards
`<public_url>/v0/workbench/github/webhook` to the loopback port. The poller
is not started in this mode, whether or not a token file is set: the App
reports every default-branch push and rename, and the daemon pulls only the
repository an event names. The daemon
does not manage the tunnel process in this feature; a later feature may spawn
cloudflared or ngrok with operator credentials.

### Console log of every event

The operator runs `wb daemon serve` in a terminal and wants to see the hub
work without opening the dashboard. Every event the hub handles, whether it
arrived by webhook or was produced by the poller, writes one line to the
daemon's console (stderr, stdout stays reserved for command output) at the
moment the hub decides what to do with it:

```
14:02:11 push            github.com/sneat-dev/wb          default branch main -> 3f1c2a9; queued for laptop
14:02:11 push            github.com/sneat-dev/wb-state    ignored: not on default branch
14:05:40 repository      github.com/sneat-dev/wb-hub      renamed from sneat-dev/workbench-gh-app; queued for laptop
14:06:02 installation    sneat-dev                        repositories added: 2; entitlements refreshed
14:06:30 poll            github.com/sneat-dev/wb          no change
14:07:15 push            github.com/sneat-dev/wb          duplicate delivery 8a1f...; dropped
```

Columns are: local time, event name as GitHub names it (`poll` for the
poller), the organisation or repository the event is about, and the action
taken in plain words: queued for which machines, ignored and why, dropped as
a duplicate, or rejected with the reason (bad signature, unknown
installation). Secrets, payload bodies and tokens never appear. The same
lines are kept in the daemon log file the existing `wb daemon` already
writes, so a detached daemon can be inspected with `wb daemon status` and
the log. A `--quiet` flag on `wb daemon serve` silences the console lines
without touching the log file.

### Dashboard

`hub/web` is built by `pnpm build` and its `dist/` is embedded in the wb
binary at release time. The pages already fetch the API by path, so the
embedded dashboard talks to the loopback hub with no configuration. A wb
built from source without a `dist/` serves a one-line page saying the
dashboard was not built and how to build it.

## Dependencies

- github-app-repository-events
- remote-state

## Acceptance Criteria

### AC: serve-without-app-or-public-url

Given `hub.github.token_file` and no `hub.github.app`, `wb daemon start`
serves the hub and dashboard on the loopback address, `wb daemon status`
shows polling active, and no inbound connection from outside the loopback is
accepted.

### AC: poll-detects-default-branch-update

Given a repository in the machine inventory whose default branch head
changes on GitHub, within two polling intervals the daemon has received and
acknowledged a `default_branch_updated` event for it and the canonical clone
is fast-forwarded, with no change to any worktree. The same push observed
twice enqueues one event.

### AC: poll-detects-rename

Given a repository renamed on GitHub, the daemon receives a
`repository_renamed` event carrying the previous name and relocates the
canonical clone as the hosted path does today.

### AC: store-engine-is-configuration

The same journeys pass with `engine: memory` and `engine: ingitdb`; the
hosted instance's dalgo2firestore path is exercised by the existing sneat-go
wiring and is not changed by this feature.

### AC: every-event-is-narrated-on-the-console

Given `wb daemon serve` running in a terminal, each webhook delivery and each
poll observation produces exactly one console line naming the event, the
organisation or repository, and the action taken, including ignored,
duplicate and rejected outcomes; the line appears before the HTTP response
to GitHub is sent for webhooks. No token, secret or payload body appears in
any line. `--quiet` suppresses the console lines and the log file still
records them.

### AC: whole-journey-e2e

One end-to-end test starts the daemon with a fake GitHub API, opens the
embedded dashboard, changes the fake default branch, and observes the
canonical clone fast-forward and the dashboard status update.

## Open Questions

None at this time. Resolved 2026-09-11 with the founder's approval:

- Default store engine for self-hosting is inGitDB: durable, inspectable
  files under `~/.wb/hub`, no server, no CGO.
- Polling interval defaults to 20 minutes with a 30s floor (founder,
  2026-09-11: a one-minute default drained the shared per-user API budget
  on a 379-repository fleet within minutes); GitHub's rate-limit headers
  cap it further when needed.
- The dashboard is embedded in the binary; the release job builds `hub/web`
  with Node before goreleaser runs. The founder confirmed the size cost
  (about 0.5 MB on a 28 MB binary) is acceptable.

---
*This document follows the https://specscore.md/feature-specification*
