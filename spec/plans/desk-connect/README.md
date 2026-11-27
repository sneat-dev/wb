---
format: https://specscore.md/plan-specification
status: Draft
---
# Plan: Desk Connect (open-source client)

**Status:** Draft
**Source Feature:** desk-connect
**Date:** 2026-11-27
**Owner:** alex
**Supersedes:** —

## Summary

Implement the open-source half of [desk-connect](../../features/desk-connect/README.md): the
public `api/desk` wire contract, the machine-side detail reduction, the `wb desk` command
family, and the daemon's Desk publisher and heartbeat. The Desk server is private
(`sneat-co/sneat-go`, [decision 0005](../../decisions/0005-sneat-dev-desk.md)). Every task
here is therefore tested against an in-process fake Desk built from `api/desk` types, never
against a live service.

## Journey

On a laptop with a running daemon, the developer runs `wb desk connect`, approves the code in
the browser, and within a minute `wb desk status` reports `published` and `heartbeat` with
fresh ages. The fake Desk holds an envelope at `repositories` detail containing the
repositories and counts, but no task or branch name. Running `wb desk disconnect` revokes the
credential and stops all Desk traffic.

## Approach

Seven tasks, in three pull requests stacked on the spec PR:

1. **Contract and reduction** (tasks 1–3). This is pure code with no I/O beyond HTTP to a fake.
2. **Commands** (tasks 4–5).
3. **Daemon** (tasks 6–7).

Reuse existing code rather than parallel implementations:

| Need | Existing code |
|---|---|
| Envelope | `fleet.NewEnvelope` / `(*Snapshotter).Export` |
| Credential I/O | `internal/credentialfile` |
| Partial `wb.yaml` writes | the `wbconfig.SetRemoteHub` yaml-node pattern |
| Restart after enrollment | `remoterun.RestartDaemonAfterEnroll` |
| HTTP | the `fleet.newHTTPExporter` / `remotestate/hub` patterns, through `hubaddress.Proxy` |
| Machine id | `nodeidentity` |
| Opening the browser | `browser.Open` |

New Go code keeps 100% changed-statement coverage and the per-package ratchet.

Two facts about the existing code shape the tasks:

- `ownEntries` clears `Throughput` and `PublishError` from the export
  (`internal/cockpit/fleet/export.go`). The Desk reduction therefore adds throughput back from
  the source `Document`, rather than assuming the export carries it.
- The snapshotter takes one `Options.Publisher`. Task 6 adds a fan-out so that `remote:` and
  `desk:` publish independently (desk-connect#req:desk-configuration).

## Tasks

### Task 1: `api/desk` wire contract

**Id:** task-1
**Verifies:** desk-connect#ac:coexists-and-stays-quiet
**Depends-On:** —
**Status:** complete
**Implemented-by:** 3578edb1
**Evidence:** https://github.com/sneat-dev/wb/pull/1015

Add the public package `api/desk` with:

- `SchemaVersion`;
- the request and response types for `connect/start`, `connect/token` (including the RFC 8628
  error codes `authorization_pending`, `slow_down`, `access_denied`, `expired_token`),
  `machines/self` (GET, DELETE), `machines/self/snapshot` (PUT) and
  `machines/self/heartbeat` (POST);
- the grant outcome (`space_id`, `shared_space_ids` and share state);
- the `desk_error` code set;
- the scope constants;
- `Validate` methods with strict decoding.

The package has no dependency on `internal/`. Tests cover encoding round-trips, unknown-field
rejection, and every code.

### Task 2: Detail reduction and task keys

**Id:** task-2
**Verifies:** desk-connect#ac:detail-level-enforced-locally, desk-connect#ac:task-keys
**Depends-On:** task-1
**Status:** complete
**Implemented-by:** 3578edb1
**Evidence:** https://github.com/sneat-dev/wb/pull/1015

Add `internal/deskpublish/reduce.go`:

`Reduce(envelope fleet.Envelope, throughput *fleet.Throughput, level api/desk.Level, key TaskKey) (desk.Snapshot, error)`

- **`full`** returns the envelope as built, with `detail: full`.
- **`repositories`**:
  - replaces `Worktree.Task`, `Agent.Task` and the throughput slowest-task names with
    `task_key` (the first 16 bytes of HMAC-SHA256, base64url);
  - drops `Name`, `Stream`, non-default `Branch`, `Agent.Worktrees`, `PullRequest.Branch`,
    `PullRequest.Worktree` and `FailedCheck`;
  - keeps counts, states and pull request fields;
  - sets `task_key_id`.

Add `TaskKey` load and generate functions over `~/.config/wb/credentials/desk-task-key` (32
bytes, 0600, refusing a non-private file).

Extend the sentinel test in an external test package to marshal both levels of the reduced
`Export(false)` and assert:

- no seeded task, branch, stream, worktree, agent-task, failing-check or slowest-task value
  appears at `repositories`;
- `machine_routes`, paths and error text appear at neither level.

Pin the field sets of the new types.

### Task 3: Desk HTTP client

**Id:** task-3
**Verifies:** desk-connect#ac:publish-on-change-with-heartbeat
**Depends-On:** task-1
**Status:** complete
**Implemented-by:** 3578edb1
**Evidence:** https://github.com/sneat-dev/wb/pull/1015

Add `internal/deskclient`, a client for the six routes with:

- connect and total timeouts;
- `hubaddress.Proxy`;
- https only, or http on loopback only (`hubaddress.Valid`/`IsLoopbackHost`);
- a bearer read from a private file;
- gzip request bodies;
- a 1 MiB compressed / 8 MiB decoded bound checked before sending (`too_large`);
- capped, strictly decoded responses.

Map statuses to typed `desk_error` codes without keeping response text:

| Response | `desk_error` |
|---|---|
| 401 | `revoked` |
| 413 | `too_large` |
| 422 | `rejected_payload` |
| 426 | `schema_unsupported`, with the minimum version |
| 429 | `rate_limited`, with `Retry-After` |
| network errors | `unreachable` / `timeout` |

The tests run against an `httptest` fake Desk.

### Task 4: `desk:` configuration

**Id:** task-4
**Verifies:** desk-connect#ac:coexists-and-stays-quiet
**Depends-On:** —
**Status:** complete
**Implemented-by:** 5401554f
**Evidence:** https://github.com/sneat-dev/wb/pull/1016

Add `wbconfig/desk.go`:

- `LoadDesk` / `parseDesk`, following the `parseCockpit` pattern with `KnownFields(true)`,
  defaults, and URL validation that allows https, or http on loopback only;
- the intervals `publish.min_interval` (default 60s, floor 30s) and `heartbeat.interval`
  (default 5m, floor 1m);
- `SetDesk` and `ClearDesk`: atomic yaml-node edits of only the fields they own, preserving all
  other settings, written through a 0600 temp file, sync and rename.

Add a rotation-capable private writer next to `credentialfile.WritePrivate` (temp file plus
rename), because reconnects replace an existing credential.

### Task 5: `wb desk` command family

**Id:** task-5
**Verifies:** desk-connect#ac:one-command-connect, desk-connect#ac:reconnect-and-disconnect, desk-connect#ac:space-chosen-at-approval
**Depends-On:** task-1, task-3, task-4
**Status:** complete
**Implemented-by:** 5401554f
**Evidence:** https://github.com/sneat-dev/wb/pull/1016

Add `internal/cli/cmddesk` (Operations struct) and `internal/deskrun`.

- **`connect`**, with the flags `--desk`, `--machine`, `--detail`, `--space`, `--no-browser`,
  `--no-restart`, `--task-key-stdin` and `--json`. It runs the device flow (honouring
  `interval` and `slow_down`), opens the browser only on a terminal, then:
  1. verifies with `GET machines/self`;
  2. writes the credential and task key privately;
  3. runs `SetDesk`;
  4. restarts with `RestartDaemonAfterEnroll`.

  A failed reconnect leaves the previous configuration in place. `--detail` on a connected
  machine changes the level without re-enrolling.
- **`status`**, reading the daemon's Desk state when it is running and the config otherwise.
- **`disconnect`**: revoke, then clear, then restart. It reports `revoked: false` and exits 1
  when Desk is unreachable.
- **`task-key export`**, which refuses a terminal stdout.

Register the family in `cmd/wb/main.go` (maintain group) and `persistentFlagSupport`. Add the
rows to `ai/capabilities.json` and `ai/skills/commands.json` (`"desk": ["wb-fleet"]`), the
skill examples, and the `docs/cli-flag-matrix.md` lines. Exit codes are 0, 1 and 2.

### Task 6: Daemon publisher, heartbeat and fan-out

**Id:** task-6
**Verifies:** desk-connect#ac:publish-on-change-with-heartbeat, desk-connect#ac:reconnect-and-disconnect
**Depends-On:** task-2, task-3, task-4
**Status:** complete
**Implemented-by:** 2c915057
**Evidence:** https://github.com/sneat-dev/wb/pull/1017

Add `internal/deskpublish.Publisher`, implementing `fleet.RemotePublisher`:

- On `Publish`, it skips while warming up or `fleet.Unlistable`, reduces, canonically encodes
  and hashes, and sends only when the hash differs from the last accepted one and
  `min_interval` has elapsed.
- A separate heartbeat goroutine started in `daemonhost.servePrepared` beside the polling
  services sends the observation time, WB version and last hash on `heartbeat.interval`.
- Backoff doubles up to 15 minutes, `rate_limited` honours `Retry-After`, and
  `schema_unsupported` stops publishing until restart.
- On `revoked`, it stops all Desk requests and never re-enrolls.

Add a `fleet` fan-out publisher so that `Options.Publisher` carries both the `remote:` and the
Desk publishers, each with its own diagnostic. Add `Machine.DeskError` (a new metadata field,
added to the sentinel's metadata list and to the Cockpit spec's closed set) and surface it in
fleet health with the fix command. Add `wb desk push`, which asks the running daemon to
publish now through a daemon route and fails with a finding when the daemon isn't running.
`push` moved here from task 5 because it needs this publisher. `wb desk status` reads `wb.yaml`
and Desk's `machines/self` directly, so it needs no daemon route.

### Task 7: Journey test

**Id:** task-7
**Verifies:** desk-connect#ac:one-command-connect, desk-connect#ac:publish-on-change-with-heartbeat, desk-connect#ac:detail-level-enforced-locally, desk-connect#ac:coexists-and-stays-quiet
**Depends-On:** task-5, task-6
**Status:** complete
**Implemented-by:** 2c915057
**Evidence:** https://github.com/sneat-dev/wb/pull/1017

An end-to-end test with a fake Desk that auto-approves the grant:

1. connect;
2. the daemon publishes once after its first pass and not again while unchanged;
3. a change publishes again;
4. heartbeats arrive;
5. the captured envelope at `repositories` carries no task or branch name;
6. a 401 stops traffic;
7. `disconnect` clears the config.

A daemon without `desk:` makes zero requests to the fake.

## Open Questions

- Task 6 adds `desk_error` to the Cockpit metadata set. That is a change to the approved
  [cockpit](../../features/cockpit/README.md) Feature's closed list, and it needs the same
  review as any field added there.

---
*This document follows the https://specscore.md/plan-specification*
