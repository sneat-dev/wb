---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Desk Connect

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/desk-connect?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/desk-connect?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/desk-connect?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/desk-connect?op=request-change) |

**Status:** Draft
**Source Ideas:** —
**Decision:** [0005](../../decisions/0005-sneat-dev-desk.md)

## Summary

`wb desk` connects a machine's WB daemon to [Sneat Dev Desk](https://desk.sneat.dev) and keeps
Desk's copy of that machine's state current:

- `wb desk connect`: enrolls this machine through a browser-approved device code and installs
  its Desk credential privately.
- `wb desk status`: shows the connection, the detail level, and the last publish and heartbeat,
  with any typed error.
- `wb desk push`: publishes once, now.
- `wb desk disconnect`: revokes the credential at Desk and removes it locally.
- `wb desk task-key export`: hands this person's task key to their next machine.

Once connected, the daemon publishes the machine's export envelope, reduced to the machine's
detail level, when it changes. It also sends a small heartbeat on a fixed cadence. All traffic
is outbound HTTPS from the machine. This Feature specifies the open-source client and the
public wire contract (`api/desk`). How Desk stores, shares, and shows the data is specified in
the private Desk repository.

## Problem

A developer works on a laptop and several VMs, with repositories on GitHub, Bitbucket, and
GitLab. Cockpit shows one machine deeply, and `wb remote` shares single-identity snapshots, but
there is no place to see "every machine, every repository, what needs me" at a glance. There is
also no way to show that state to a team. Getting a machine into that view must:

- take one command and one browser click;
- work behind NAT and corporate proxies;
- never send more than the person chose to send.

## Behavior

### Connection

#### REQ: device-code-enrollment

`wb desk connect` MUST enroll through a device authorization grant shaped after RFC 8628:

1. It requests a grant from `<desk.url>/v0/desk/connect/start` and sends the machine name,
   machine id, WB version, operating system, and architecture.
2. It receives a secret `device_code`, a short `user_code` (eight characters from an
   unambiguous alphabet, shown as `XXXX-XXXX`), a `verification_uri`, an `expires_in` of at most
   600 seconds, and a polling `interval`.
3. It prints the `user_code` and the verification address to stderr. It opens the browser at
   the address that carries the code unless `--no-browser` is given or no display is available.
4. It polls `<desk.url>/v0/desk/connect/token` with the `device_code` at the given interval. It
   adds 5 seconds on `slow_down` and stops with a clear message on `access_denied` or
   `expired_token`.

The `device_code` MUST never be printed, logged, or put in argv. The command exits 0 only
after a credential has been received, verified, and installed (REQ:credential-installed-privately).

#### REQ: approval-shows-what-is-connecting

The approval page is served by Desk and is out of scope here. The wire contract MUST still
give it what a person needs to recognise their own request:

- the machine name and id;
- the WB version, operating system, and architecture;
- the time of the request.

If the person's identity already has a machine with that name, the contract MUST also say so,
with that machine's last-seen time. Approving such a request replaces the existing machine's
credential (REQ:reconnect-rotates). The approval binds the grant to the person who approved it.
An approval never comes from anything except a signed-in person in a browser.

#### REQ: space-chosen-at-approval

The approval MUST ask where the machine is shown. The choices are:

- **Personal only** (the default);
- **Personal and a space**: one `team` or `company` space in which the approving person may
  add machines, with that space's sharing level (`summary`, `repositories`, or `full`). The
  level takes effect at no more than the machine's detail level.

The machine is always owned by the approving person and always lives in their personal space.
Choosing a space adds a share, which the person can revoke later in Desk without reconnecting
the machine. A space can require that a space admin confirms new machines. Until an admin
does, the share is pending and the space shows nothing from the machine.

`wb desk connect --space <space-id>` preselects a space on the approval page. It is a hint
only: the browser choice decides, and the CLI cannot add a share by itself. The grant's
outcome returns the personal space id and the shared space id, if any, with the share state
(`active` or `pending`). WB writes them to `desk.space_id` and `desk.shared_space_ids`, and
`wb desk status` reports them.

#### REQ: credential-installed-privately

On success, WB MUST do all of the following before it reports success:

1. Verify the credential with one authenticated `GET <desk.url>/v0/desk/machines/self`.
2. Write the credential to `~/.config/wb/credentials/desk-<machine>.token` with mode 0600,
   atomically.
3. Atomically update only the `desk:` fields it owns in `wb.yaml` (`url`, `machine`, `machine_id`,
   `space_id`, `shared_space_ids`, `credential_file`, `detail`), preserving every other setting.
4. Restart a running daemon so that it starts publishing, unless `--no-restart` is given. In
   that case it prints the restart command.

The credential MUST never appear on stdout, in configuration, or in WB telemetry. stdout
carries the outcome, in text or `--json`: the machine name and id, the space id, the Desk URL,
the detail level, and the credential file path.

#### REQ: credential-scopes

A Desk machine credential MUST carry only the scopes this Feature uses:

- `desk_snapshot:publish`
- `desk_heartbeat:write`
- `desk_machine:read_self`

It reads no other machine and receives no events or actions. A later Feature that needs more
scopes asks for them through a new enrollment, never by widening an issued credential.

#### REQ: reconnect-rotates

Running `wb desk connect` again for an already connected machine, from this machine or after
approving a same-name machine, MUST rotate the credential. Desk invalidates the previous
credential as it issues the new one, and the local file is replaced atomically. A failed
reconnect leaves the previous working configuration in place.

#### REQ: disconnect-revokes

`wb desk disconnect` MUST first revoke the credential at
`DELETE <desk.url>/v0/desk/machines/self`, then remove the local credential file and the
`desk:` fields it owns, then restart a running daemon. If Desk cannot be reached, it removes the
local credential anyway. It then reports `revoked: false` and says that the machine must also
be removed in Desk, and it exits 1.

#### REQ: revocation-is-respected

When Desk answers a publish or heartbeat with 401, the daemon MUST stop publishing and record
`desk_error: revoked`. This happens when the machine was deleted in Desk or the credential
was rotated elsewhere. Leaving a space, or losing a share in one, never revokes the machine's
credential: the machine keeps publishing to its personal space. The daemon MUST NOT retry with that credential
or re-enroll on its own. `wb desk status` and Cockpit's fleet health show the error with the
command that fixes it (`wb desk connect`).

Only 401 means revoked. A 403 can come from a proxy or firewall on the way, so it is treated as
`unreachable` and retried with backoff.

### Publishing

#### REQ: publish-on-change

While connected, the daemon MUST publish its export envelope to
`PUT <desk.url>/v0/desk/machines/self/snapshot` after a successful local refresh, when the
reduced and canonically encoded envelope's SHA-256 differs from the last one Desk accepted.
Publishes happen no more often than `desk.publish.min_interval` (default 60 seconds, floor 30
seconds). The envelope is produced by the same function the hub export route uses
([cockpit-views](../cockpit-views/README.md)#req:hub-export-route), so Desk and a peer
machine see the same projection.

A daemon whose first pass has not ended (`warming_up`), or that cannot list its repositories,
MUST NOT publish. Desk keeps the last good snapshot rather than receiving a partial or empty
one.

#### REQ: heartbeat

While connected, the daemon MUST send `POST <desk.url>/v0/desk/machines/self/heartbeat`
every `desk.heartbeat.interval` (default 5 minutes, floor 1 minute). The body is the
observation time, the WB version, and the hash of the last accepted snapshot, nothing else.
Desk derives online, stale, and offline from heartbeats, independently of whether the fleet
changed. Heartbeats start when the daemon starts, not after its first full pass, so a machine
whose passes do not complete is still seen as running.

#### REQ: publishing-never-blocks-local-work

A failed or slow publish or heartbeat MUST never delay the daemon's local refresh, Cockpit, or
any command. Each failure is recorded as a typed `desk_error`:

- `unreachable`
- `timeout`
- `revoked`
- `rejected_payload`
- `schema_unsupported`
- `too_large`
- `rate_limited`

Desk's response text is never recorded. Failures back off exponentially from the configured
interval to at most 15 minutes. `rate_limited` honours `Retry-After`, and `schema_unsupported`
stops publishing until WB is updated.

#### REQ: bounded-payload

A published envelope MUST be sent gzip-encoded, with at most 1 MiB compressed and 8 MiB
decoded. When the reduced envelope exceeds that, the export's existing caps decide what is
left out, the count appears in `dropped`, and the publish proceeds. Only an envelope that
cannot be brought under the limit is refused locally as `too_large`.

#### REQ: outbound-only-transport

Every request MUST be outbound HTTPS from the machine to `desk.url`. `http://` is accepted only
for a loopback host, which is used for tests and local development. The client honours the
standard proxy environment (`HTTPS_PROXY`, `NO_PROXY`). Desk never connects to the machine.

### Detail level

#### REQ: detail-level-applied-on-the-machine

The daemon MUST reduce the envelope to the machine's detail level before encoding, hashing, or
sending it. The level comes from `desk.detail`, chosen with `wb desk connect --detail` and
changed with `wb desk connect --detail <level>` on a connected machine.

- **`repositories`** (default) keeps:
  - machine facts: name, id, WB version, operating system, architecture, CPU count, boot time,
    and refresh interval;
  - repository forge host, `owner/name`, default branch name, `remote_url_web`, and newest
    local-branch activity time;
  - per repository, the counts of worktrees by lifecycle and owner state, ahead and behind
    totals, durability levels, risk reason codes, and code-index freshness;
  - pull request number, URL, state, merge state, and check counts and verdict;
  - agent counts by state and activity;
  - the `throughput` counts and durations.

  It replaces each task name with a `task_key` (REQ:task-keys-correlate-without-names), and it
  removes:
  - task, stream, branch, and worktree names (except the default branch name);
  - agent task names and worktree ids;
  - the name of the first failing check;
  - the names of the slowest tasks.

  A worktree's entry id is an unkeyed hash that covers its task and branch, so Desk could
  confirm a guessed name by hashing it. At this level, each such id is therefore replaced by an
  id keyed under the task key, with its kind prefix kept.
- **`full`** sends the export envelope as the hub export route builds it, which is at most
  Cockpit's anonymous metadata set
  ([cockpit](../cockpit/README.md)#req:anonymous-local-reads-metadata-only).

At both levels, the daemon adds the machine's `throughput` block back. The export itself
leaves it out, and it is in the metadata set.

At both levels, the snapshot also leaves out what changes on every pass although the fleet did
not, so that its hash changes only when the fleet does and an idle machine publishes nothing
(REQ:publish-on-change):

- the export time (`exported_at`) and the document's `snapshot_at`;
- each entry's `observed_at`;
- each pull request's `checked_at`;
- the machine's resource samples (`metrics`).

Desk takes the time of a snapshot from the request's `observed_at`, and a machine's liveness
from its heartbeats.

No level MUST ever send anything outside that set. That includes file content, file names,
filesystem paths, diffs, commit subjects or messages, task summaries, prompts, log bodies,
environment values, command lines, error text, and the owner-only `machine_routes`. The level
in force is sent in the envelope (`detail`), and Desk MUST reject an envelope whose content
exceeds its declared level.

#### REQ: task-keys-correlate-without-names

At `repositories` detail, each task name MUST be sent as a `task_key`: the first 16 bytes of
HMAC-SHA256 over the task name, base64url-encoded, under the person's **task key**. This lets
Desk show "the same task on the Mac and on vm1" without learning what the task is called.

The task key is the whole guarantee, so Desk MUST never hold it. A key Desk knew would let it
recover common task names by guessing.

- The first `wb desk connect` on a machine with no task key generates 32 random bytes and
  stores them in `~/.config/wb/credentials/desk-task-key` with mode 0600.
- Another machine of the same person adopts that key with
  `wb desk connect --task-key-stdin`, for example
  `ssh mac wb desk task-key export | wb desk connect --task-key-stdin`. The key travels only
  over a channel the person controls, never through Desk.
- `wb desk task-key export` writes the key to stdout only when stdout is not a terminal, and
  refuses otherwise.
- A machine with a different key still publishes. Its task keys simply do not match the other
  machines', and Desk cannot tell why. The envelope carries the first 8 bytes of SHA-256 of the
  key (`task_key_id`) so that Desk can say that two machines use different keys and show the
  fix.
- Task keys are per person. Two people's keys differ, so a space never correlates tasks across
  people at `repositories` detail.
- At `full` detail the task name is sent and no `task_key` is needed.

### Configuration and coexistence

#### REQ: desk-configuration

Desk is configured in its own `desk:` section of `wb.yaml`:

- `url` (default `https://desk.sneat.dev`)
- `machine`
- `machine_id`
- `space_id`
- `shared_space_ids`
- `credential_file`
- `detail`
- `publish.min_interval`
- `heartbeat.interval`
- `enabled` (default `true` once connected)

`desk.machine` records the connected machine name and is used as the daemon's fleet machine name
when no `remote:` section is configured (`wb desk connect` refuses an explicit `--machine` that
differs from `remote.machine`).

A daemon with no `desk:` section, or with `enabled: false`, makes no Desk request at all.
`desk:` is independent of `remote:`, so a machine may publish to a `git` or `hub` remote store
and to Desk at the same time ([remote-state](../remote-state/README.md)).

#### REQ: wire-contract-versioned

The request and response types, the envelope reduction, and the `schema_version` of every
Desk payload MUST live in the public package `api/desk`. The daemon sends its
`schema_version`. Desk answers a version it no longer accepts with 426 and the minimum
version, which WB records as `schema_unsupported` and reports with the update command. A
published version keeps decoding at Desk for at least 90 days after its successor ships.

#### REQ: push-publishes-now

`wb desk push` MUST read this machine's export from its running daemon, together with the
daemon's throughput block, which it reads with the fleet route's small `scope=throughput`
answer. A daemon too old to know that scope answers the whole document, read within the
export's bound; past that bound the push goes without throughput. It reduces the export exactly as the daemon's publisher does
(REQ:detail-level-applied-on-the-machine), publishes it, and sends one heartbeat. It exits 1
in each of these cases:

- the machine is not connected;
- the daemon is not running, in which case it names `wb daemon start`;
- the daemon has nothing to export yet;
- Desk refuses the snapshot.

A failed heartbeat after an accepted publish is reported in the result but is not a failure.
The daemon publishes on its own after every change, so `push` only saves the wait.

#### REQ: commands-are-documented

Each of `wb desk connect`, `status`, `push`, `disconnect`, and `task-key export` MUST have a row in
`ai/capabilities.json` and a line in `docs/cli-flag-matrix.md`. Exit codes follow WB's
contract:

- `0`: success.
- `1`: a finding, such as not connected, revoked, a failed publish, or a disconnect that could
  not reach Desk.
- `2`: usage.

`wb desk status --json` is a stable machine-readable document.

## Acceptance Criteria

### AC: one-command-connect

**Requirements:** desk-connect#req:device-code-enrollment, desk-connect#req:approval-shows-what-is-connecting, desk-connect#req:credential-installed-privately, desk-connect#req:credential-scopes

On a machine with a running daemon, `wb desk connect` prints a user code and the Desk address,
and opens the browser where a display is available. After the person approves in the browser,
the command:

- verifies the credential;
- writes it to a 0600 file;
- updates only the `desk:` fields of `wb.yaml`;
- restarts the daemon;
- exits 0.

The device code and the credential appear nowhere on stdout or stderr, in argv, in
configuration, or in logs. A denied or expired grant exits 1 with a message naming which, and
it changes no file.

### AC: space-chosen-at-approval

**Requirements:** desk-connect#req:space-chosen-at-approval

Approving with "Personal only" leaves the machine visible only in the person's personal space.
Approving with a team space adds a share at the chosen level, or a pending share where that
space requires an admin, and `wb desk status` reports both space ids and the share state.
`--space` preselects the choice but cannot by itself create a share.

### AC: task-keys

**Requirements:** desk-connect#req:task-keys-correlate-without-names

At `repositories` detail, two machines sharing a task key publish equal `task_key` values for
equal task names and no task name. A machine with another key publishes non-matching keys and
a different `task_key_id`. The key file is mode 0600 and the key appears in no request to Desk.
`wb desk task-key export` refuses a terminal stdout.

### AC: reconnect-and-disconnect

**Requirements:** desk-connect#req:reconnect-rotates, desk-connect#req:disconnect-revokes, desk-connect#req:revocation-is-respected

Reconnecting a connected machine leaves the old credential refused by Desk and the new one in
use, and a reconnect that fails leaves the old configuration working. `wb desk disconnect`
revokes at Desk and removes the local credential. Against an unreachable Desk, it removes the
local credential, reports `revoked: false`, and exits 1. A daemon whose credential Desk revoked
stops publishing, shows `desk_error: revoked` in `wb desk status` and Cockpit's fleet health,
and makes no further authenticated request with that credential.

### AC: publish-on-change-with-heartbeat

**Requirements:** desk-connect#req:publish-on-change, desk-connect#req:heartbeat, desk-connect#req:publishing-never-blocks-local-work, desk-connect#req:bounded-payload, desk-connect#req:outbound-only-transport

A connected daemon:

- publishes after its first full pass and again only when the reduced envelope's hash changes;
- never publishes more often than the minimum interval;
- never publishes while warming up;
- sends a heartbeat on its interval whether or not anything changed.

With Desk unreachable, the local refresh and Cockpit keep their cadence, the error is a typed
code with backoff capped at 15 minutes, and publishing resumes on its own when Desk answers
again. All requests go to `desk.url` over HTTPS through the configured proxy.

### AC: detail-level-enforced-locally

**Requirements:** desk-connect#req:detail-level-applied-on-the-machine

For a fleet whose worktrees have task and branch names, an envelope captured at the transport
boundary at `repositories` detail contains the repositories, counts, risk codes, and pull
request states, and no task, stream, branch (other than default), worktree, agent-task, or
failing-check name. At `full` detail, it contains exactly the hub export route's envelope. At
no level does it contain any path, file name, commit message, error text, or `machine_routes`
field. A sentinel test extends `internal/cockpit/fleet/sentinel_test.go` to the Desk reduction.

### AC: coexists-and-stays-quiet

**Requirements:** desk-connect#req:desk-configuration, desk-connect#req:wire-contract-versioned, desk-connect#req:commands-are-documented

A daemon without a `desk:` section makes zero Desk requests. A daemon with both `remote:` and
`desk:` publishes to both independently. An unsupported schema answer stops publishing with
`schema_unsupported` and the update command. Every `wb desk` leaf passes
`cmd/wb/skills_test.go`.

## Open Questions

- Should `wb desk task-key export | ...` move over the peer transport
  ([peer-connectivity](../peer-connectivity/README.md)) so that connecting a second machine
  adopts the key without a manual pipe?

---
*This document follows the https://specscore.md/feature-specification*
