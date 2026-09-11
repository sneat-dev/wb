---
format: https://specscore.md/plan-specification
status: Implemented
---
# Plan: Self-hosted bench implementation plan

**Status:** Implemented
**Reconciled:** 2026-09-11
**Source Feature:** self-hosted-bench
**Date:** 2026-09-11
**Owner:** alex
**Supersedes:** —

## Summary

Implement the self-hosted bench journey inside `wb daemon serve`: a `hub:`
configuration section, DALgo store engines, the hub API and embedded
dashboard on the loopback server, local machine identity, the GitHub
polling ingester, console narration of every event, webhook mode, the
release-time dashboard build, and a whole-journey end-to-end test.

## Journey

The operator adds `hub.github.token_file` to `wb.yaml`, restarts the daemon,
opens the loopback dashboard with no sign-in, and sees their machine and
worktrees. A push to a repository's default branch from elsewhere is noticed
within the polling interval, narrated on the daemon's console, queued for
the machine, and the canonical clone fast-forwards with worktrees untouched.
A rename is detected and the clone relocated. Configuring a GitHub App with
a tunnel switches covered repositories to webhooks; stopping the tunnel
falls back to polling with nothing lost.

## Approach

Three tasks, each landing as one PR on `sneat-dev/wb` main, in order. Task
1 makes the hub reachable locally on a durable store so the dashboard works
and everything after has a place to write. Task 2 adds the only ingestion a
self-hoster needs by default, plus the console lines the founder asked for,
so the journey is complete without an App. Task 3 adds the App path, the
release build and the e2e that walks the whole journey. Every task keeps
`hub/` at 100% statement coverage and the repository floor intact, adds a
capability row and flag-matrix line for any command surface it touches, and
narrates through the daemon's existing logger rather than a new one.

## Tasks

### Task 1: Hub and dashboard on the loopback daemon

**Id:** task-1
**Verifies:** self-hosted-bench#ac:serve-without-app-or-public-url, self-hosted-bench#ac:store-engine-is-configuration
**Depends-On:** —
**Status:** complete

Add the `hub:` section to `internal/wbconfig` with `store.engine` (memory,
ingitdb, openvaultdb; default ingitdb under `~/.wb/hub`), `github.token_file`,
`github.poll_interval` (default 60s, floor 30s) and the optional
`github.app` block; secrets are file paths and never inline. Build a
`dal.DB` per engine (dalgo2memory, dalgo2ingitdb, dalgo2openvaultdb) and
wrap it with `api/githubapp/dalgostore`. When `hub:` is present,
`wb daemon serve` mounts `hub.NewHandler` under `/v0/workbench/` and the
embedded `hub/web` dist under `/bench/` next to the existing `/api/v1/`
routes; without it nothing changes. Embed `hub/web/dist` with `go:embed`
behind a build that tolerates an absent dist by serving a one-line "not
built" page. Enrol the local machine against the local hub on first start
with a generated credential in the private state directory, allow plain
`http://` for loopback in `remote.url` validation, and point the in-process
remote provider at the loopback hub. The dashboard viewer resolver in local
mode returns one fixed identity. Refuse a non-loopback listen address while
no identity provider is configured. `wb daemon status` reports engine,
listen address and whether the hub is mounted. Tests: config parsing and
defaults, engine selection, handler mounting, enrolment idempotence, the
refusal, and a serve-and-fetch test that reads `/bench/dashboard/` and
`/v0/workbench/github/status` from the loopback server.

### Task 2: Polling ingester and console narration

**Id:** task-2
**Verifies:** self-hosted-bench#ac:poll-detects-default-branch-update, self-hosted-bench#ac:poll-detects-rename, self-hosted-bench#ac:every-event-is-narrated-on-the-console
**Depends-On:** 1
**Status:** complete

Add `hub/poller`: for each repository in the machine's published inventory,
read `GET /repos/{owner}/{repo}` and the default branch head, compare with
the last observed values kept in the store, and enqueue
`default_branch_updated` (with `TargetSHA`) or `repository_renamed` (with
`PreviousRepository`) through the repository-event store for the local
machine, with event IDs derived from repository, reason and target so a
replay deduplicates. Honour rate-limit headers, back off on errors, one
request per repository per interval, skip repositories an installed App
covers in webhook mode. Narrate: one stderr line per webhook delivery and
per poll observation with local time, event name (`poll` for the poller),
org/repo, and the action in plain words (queued for which machines, ignored
and why, duplicate dropped, rejected with reason), written before the
webhook response is sent, mirrored to the daemon log, silenced by a new
`--quiet` flag on `wb daemon serve`, never containing a token, secret or
payload body. `wb daemon status` adds polling interval, repositories polled,
last event received and acknowledged. Tests: a fake GitHub API server
driving both reasons, dedup on replay, backoff, the skip rule, and golden
console lines for every outcome.

### Task 3: Webhook mode, release build, whole-journey e2e

**Id:** task-3
**Verifies:** self-hosted-bench#ac:whole-journey-e2e, self-hosted-bench#ac:serve-without-app-or-public-url
**Depends-On:** 2
**Status:** complete

Wire `hub.github.app` into the mounted handler so signed deliveries at
`/v0/workbench/github/webhook` are verified and enqueued exactly as the
hosted instance does, and mark covered repositories so the poller skips
them; document the cloudflared and ngrok tunnel recipes in `hub/README.md`
without spawning either. Add the Node build of `hub/web` to the release
workflow before goreleaser so the embedded dist is always current, and fail
the release if the dist is missing. Add the end-to-end test: start the
daemon with `engine: memory`, a fake GitHub API and a temporary canonical
clone, open the embedded dashboard, move the fake default branch, and
observe the console line, the fast-forward of the canonical clone with an
untouched worktree, and the dashboard status. Update `docs/cli-flag-matrix.md`
and `ai/capabilities.json` for `--quiet` and the status additions.

## Risks

- dalgo2ingitdb transaction semantics differ from Firestore; the parity
  tests in `hub/` run under the strict profile and must also pass on
  ingitdb before Task 1 lands.
- GitHub's unauthenticated and per-token rate limits bound how many
  repositories one token can poll at 60s; the poller must degrade to a
  longer interval rather than fail.
- `go:embed` of a directory that may not exist needs a placeholder file
  committed under `hub/web/dist/` and ignored by the Astro build, or a
  build tag; choose in Task 1 and document it.

---

## Resolution

**Reconciled Approved → Implemented outside the tracked `change-status` flow** (3 task(s) marked complete; this did not walk the legal-transition matrix).

Tasks 1 and 2 landed on main as 8a26d81 and 1b7c3f7; Task 3 (webhook mode, release build of the dashboard, whole-journey e2e) is implemented on self-hosted-bench-task-3.
*This document follows the https://specscore.md/plan-specification*
