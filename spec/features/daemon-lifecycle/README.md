---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Daemon Lifecycle Identity

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/daemon-lifecycle?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/daemon-lifecycle?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/daemon-lifecycle?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/daemon-lifecycle?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Depends On:** [Worktree Lifecycle](../worktree-lifecycle/README.md) (WB's home resolver)

## Summary

The WB daemon resolves its runtime directory through WB's **one** home
resolver, records *which* home it belongs to alongside the executable it is
running, and proves that identity before reporting `ready`. A daemon left over
from a previous or abandoned home can therefore never be mistaken for this
one, and a second daemon cannot be started to fight over the loopback port.

`wb daemon status` reports provenance, not reachability. Today it reports
`ready` whenever *something* answers on the port — which is why two separate
real failures, an orphaned daemon outliving its systemd unit
([#546](https://github.com/sneat-dev/wb/issues/546)) and a daemon writing into
a reverted `WB_HOME` ([#549](https://github.com/sneat-dev/wb/issues/549)), were
both invisible from WB's own output while looking healthy.

## Problem

Every WB subsystem that owns durable state resolves its directory through
`internal/wbhome`: worktrees, work logs, sessions, receipts, locks, and agent
runs. The daemon does not. It builds its socket path as
`<projects-root>/.wb/runtime/daemon.sock` and receives its lifecycle state path
as an absolute argument pinned by whatever installed its supervisor unit.

That produced a concrete, reproducible failure. `WB_HOME` was migrated from
`~/.wb` to `~/projects/.wb` by symlink and then reverted. Every other subsystem
followed both moves; the daemon did not, because it never consulted the
resolver. A live daemon kept its socket, state, and a 2.5 MB log inside the
directory the revert had abandoned, and:

- `wb daemon status` reported `state=ready, api_reachable=true`, because it was
  talking to that daemon over the loopback port;
- `~/.wb/runtime/` was empty, so nothing in the current home recorded a daemon
  at all;
- deleting the abandoned directory — the obvious cleanup — would have destroyed
  a *running* daemon's state and broken its supervisor's log path.

Why the state path was absolute is second-order but important: the supervisor
unit was written while `~/.wb` was a symlink, and WB's resolver resolves
symlinks eagerly, so the *resolved* target was baked into the unit and then
outlived the symlink. [#546](https://github.com/sneat-dev/wb/issues/546) shows
the same shape on Linux, where the pinned path is visible in the orphan's own
command line.

Three properties are missing:

1. **One home.** The daemon must resolve its runtime directory the way every
   other subsystem does, so a `WB_HOME` change moves it too.
2. **Identity.** A state record must say which home and which state path it
   belongs to, so a reader can tell "this daemon is mine" from "a daemon
   answers on my port".
3. **Authority.** A second daemon must be refused *before* it binds, by
   something other than the TCP port, and the refusal must name the holder.

## Behavior

#### REQ: runtime-home-is-the-resolved-wb-home

The daemon's runtime directory (socket, default lifecycle state, log) MUST be
resolved through WB's home resolver, exactly as every other subsystem's durable
state is. It MUST NOT be constructed from the projects root and a literal
`.wb` segment, and it MUST NOT require an operator or a supervisor unit to
supply an absolute runtime path. Moving, symlinking, or unsetting `WB_HOME`
MUST move the daemon's runtime directory with it.

Because the daemon asks the resolver rather than naming a home itself, a later
change to what that resolver returns — for example the single
`WB_PROJECTS_ROOT` layout proposed by [Projects Root Layout and Worktree
Placement](../projects-root-layout/README.md) — moves the daemon with it and
needs no daemon-side change.

#### REQ: runtime-path-is-reported-and-stable

The resolved runtime directory and the socket path within it MUST be derivable
by a reader without starting a daemon, so an operator, a supervisor installer,
or a diagnostic command can name the exact endpoint it is talking about. The
socket path MUST remain short enough for the platform's `AF_UNIX` limit, and a
path that exceeds it MUST be refused with both the limit and the path named,
before binding.

#### REQ: state-record-identifies-its-home

The lifecycle state record MUST include the resolved WB home and the state
file's own path, in addition to the executable provenance it already records.
A record whose recorded home or state path differs from the reader's current
resolution MUST be reported as belonging to a *different* daemon rather than
being presented as this machine's daemon.

#### REQ: state-record-identifies-its-process-generation

The record MUST carry enough evidence to tell a live process from a recycled
PID: the process start time alongside the PID. Liveness MUST be decided from
both. A recorded PID that exists but whose start time differs MUST be treated
as not-this-daemon, because a wrong "still running" is worse than an admitted
unknown.

#### REQ: status-verifies-provenance-not-reachability

`wb daemon status` MUST NOT report `ready` on the strength of a successful
connection alone. For the daemon it reached, it MUST establish whether that
daemon's recorded home, state path, and executable provenance match the
invoking WB build, and MUST report a distinct outcome when they do not.
Reachability of *something* on the endpoint MUST stay reportable, but as its
own named condition, never folded into `ready`.

#### REQ: foreign-or-orphaned-daemon-is-named

When a daemon answers on the configured endpoint but cannot be shown to belong
to the current home — a leftover from a previous `WB_HOME`, a different
projects root, or an unsupervised process — status MUST say so, and MUST name
what it could determine: the endpoint, and the recorded home and executable if
they were readable. It MUST NOT advise the operator to start a daemon without
first saying that one already answers.

#### REQ: legacy-runtime-endpoint-is-detected

Because daemon runtime directories written before this feature live under
`<projects-root>/.wb/runtime`, WB MUST detect a daemon serving from that legacy
endpoint and MUST report it explicitly rather than silently starting a second
daemon whose socket lives in the current home. Detection MUST NOT require the
legacy daemon to be healthy — an accepting socket is enough — and MUST NOT
delete, move, or otherwise disturb anything under the legacy path.

#### REQ: second-instance-refused-before-binding

Starting a daemon MUST be refused **before** the loopback port is bound when
another daemon of this home is already live. The refusal MUST name the holder
(recorded PID, home, and executable where known) and MUST exit with a code
distinguishable from a successful start and from an unrelated failure, so a
supervisor's restart policy cannot turn a lost race into an unbounded loop.

#### REQ: bind-failure-is-not-retried-blindly

A failure to bind the loopback endpoint because another process holds it MUST
be reported as a distinct, actionable condition naming that endpoint. WB MUST
NOT start a detached daemon of its own in response to it, and MUST NOT treat it
as a transient error to retry.

#### REQ: vanished-runtime-directory-is-fatal

A daemon whose runtime directory or state file has been removed underneath it
MUST stop rather than continue writing into a path that no longer exists as
part of this home. It MUST exit with a non-zero status so its supervisor
surfaces the condition, and it MUST record the reason where a later reader can
find it. Silently heartbeating into an unlinked log is the failure this
requirement exists to prevent.

#### REQ: runtime-directory-permissions

The runtime directory and every file the daemon creates in it MUST be private
to the user, matching WB's existing state-file discipline (directories `0700`,
files `0600`). A socket path occupied by a non-socket file MUST be refused
rather than removed.

#### REQ: unix-socket-is-the-authoritative-local-endpoint

The home-derived local socket MUST be the authoritative endpoint for
same-machine clients, because its path *is* an identity. The TCP loopback
listener MUST remain available but MUST be treated as a shared, non-identifying
endpoint: two daemons on different homes can only collide there, and status
MUST NOT use it to decide which home a daemon belongs to.

#### REQ: supervisor-units-do-not-pin-resolved-paths

Anything that writes a supervisor unit for the daemon MUST NOT persist the
daemon's runtime directory, or any path derived from a home resolved at install
time. It MUST persist the inputs the operator chose — the projects root and,
where set, an explicit `WB_HOME` — and a mode that says "this process was
started by WB", so the daemon resolves its own runtime directory at startup and
a later home move cannot leave the unit pointing at an abandoned directory. A
supervisor's own output log MAY be recorded, provided its location does not move
with `WB_HOME`. An explicitly supplied lifecycle state path MUST remain accepted
for compatibility, and its presence MUST be reported, because a pinned path is
the shape that caused both observed failures.

## Acceptance Criteria

### AC: runtime-moves-with-the-home (verifies REQ:runtime-home-is-the-resolved-wb-home, REQ:runtime-path-is-reported-and-stable)

Scenario: WB_HOME selects the runtime directory
Given `WB_HOME` set to a directory D
When the daemon's runtime directory and socket path are resolved
Then both are beneath D, and the socket path reported to a reader equals the path the daemon binds

Scenario: An over-long socket path is refused before binding
Given a resolved home whose socket path exceeds the platform's `AF_UNIX` limit
When the daemon starts
Then it refuses before binding and names both the path and the limit

### AC: status-cannot-be-fooled-by-a-stranger (verifies REQ:status-verifies-provenance-not-reachability, REQ:foreign-or-orphaned-daemon-is-named, REQ:state-record-identifies-its-home)

Scenario: Something answers, but it is not our daemon
Given a lifecycle record whose recorded home differs from the invoking resolution, and a daemon answering on the configured endpoint
When `wb daemon status` runs
Then it does not report `ready`, it reports the reachable-but-not-ours condition, and it names the endpoint and the recorded home

Scenario: Reachability alone is reported as reachability
Given a daemon answering on the endpoint whose recorded executable provenance differs from this WB build
When status runs
Then the provenance mismatch is reported as its own condition and is not folded into `ready`

### AC: recycled-pids-are-not-mistaken-for-a-live-daemon (verifies REQ:state-record-identifies-its-process-generation)

Scenario: A recorded PID now belongs to something else
Given a record whose PID exists but whose recorded process start time does not match the process now holding that PID
When liveness is evaluated
Then the daemon is treated as not running rather than as live

### AC: a-leftover-daemon-cannot-be-silently-doubled (verifies REQ:legacy-runtime-endpoint-is-detected, REQ:second-instance-refused-before-binding, REQ:bind-failure-is-not-retried-blindly)

Scenario: A daemon still serves the legacy runtime endpoint
Given an accepting socket at `<projects-root>/.wb/runtime/daemon.sock` and no daemon in the resolved home
When a start is attempted
Then it is refused before binding, the legacy endpoint is named, and nothing under the legacy path is modified or removed

Scenario: The loopback port is held
Given another process holds the configured loopback endpoint
When the daemon starts
Then it reports the bind conflict as a distinct condition naming the endpoint, starts no detached daemon of its own, and exits with a code a supervisor can distinguish from a successful start

### AC: a-daemon-outlives-its-directory-only-by-stopping (verifies REQ:vanished-runtime-directory-is-fatal)

Scenario: The runtime directory disappears underneath the daemon
Given a running daemon whose runtime directory is removed
When its next maintenance checkpoint runs
Then the daemon records the reason and exits non-zero rather than continuing to write to the removed path

### AC: runtime-files-stay-private (verifies REQ:runtime-directory-permissions)

Scenario: Runtime artefacts are created
Given a daemon started with `WB_HOME` set
Then the runtime directory is `0700`, its state file and socket are `0600`, and a non-socket file at the socket path is refused rather than removed

### AC: units-do-not-bake-in-a-resolved-path (verifies REQ:supervisor-units-do-not-pin-resolved-paths, REQ:unix-socket-is-the-authoritative-local-endpoint)

Scenario: A supervisor unit is written
Given a machine on which a supervisor owns the daemon
When WB writes that unit
Then the unit carries the projects root and, where set, the explicit `WB_HOME`, and it does not carry the daemon's runtime directory or any path derived from a home resolved at install time

## Non-goals

- A file watcher on `wb.yaml`, or hot-reloading configuration. The failures this
  feature addresses are identity failures, not staleness failures; a watcher
  would not have prevented any of them.
- Classifying which configuration keys may hot-reload. That is its own question,
  to be decided against a measured need.
- Replacing the daemon's transport, storage, or scheduling.
- Changing the restart policy a supervisor applies. Once identity and
  single-instance authority exist, restart policy stops being load-bearing.
- Confining the daemon's file access, or changing its privilege model.
- Any remote or multi-machine daemon coordination.

## Open Questions

- Whether the legacy endpoint should be actively retired — its socket removed
  once the daemon serving it is proved gone — or only reported forever.
  Reporting is the safe first step; a removal policy needs evidence about how
  often it is hit.
- Whether `wb daemon status` should be able to adopt a foreign daemon by
  signalling it to stop, or whether stopping something WB did not start must
  stay a manual operator action.
- Whether the loopback TCP listener needs to exist by default at all, given the
  home-derived socket is the authoritative local endpoint.

---
*This document follows the https://specscore.md/feature-specification*
