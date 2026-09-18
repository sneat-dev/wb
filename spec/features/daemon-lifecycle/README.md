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

#### REQ: supervisor-is-recorded-at-startup

At `daemon serve` startup, the process MUST detect whether a supervisor
started it — systemd or launchd — through an injectable environment seam, and
MUST record the result (`none`, `systemd`, or `launchd`) in its own lifecycle
record. Detection MUST run in the process a supervisor actually started: only
that process observes the variables a supervisor sets before it execs its
child ([#617](https://github.com/sneat-dev/wb/issues/617)).

A bare environment variable is not sufficient evidence: both are observed to
survive into a process that merely inherited its parent's environment without
being started by the supervisor at all. systemd sets `INVOCATION_ID` on every
unit it starts, and (systemd >= 248) `SYSTEMD_EXEC_PID` naming the exact PID
it exec'd; detection MUST require `SYSTEMD_EXEC_PID` to be present *and* equal
to this process's own PID — `INVOCATION_ID` alone, or a non-matching
`SYSTEMD_EXEC_PID`, MUST report `none` (confirmed live: a shell or an agent
process started inside a systemd-supervised session inherits `INVOCATION_ID`
without `SYSTEMD_EXEC_PID` ever being set for it). A daemon run under systemd
older than 248 is therefore always recorded as `none`: that systemd cannot
supply the evidence this requirement demands. launchd sets `XPC_SERVICE_NAME`
to the job label for a job it manages, and to the literal string `"0"` for a
process it did not launch directly (treated as absent); detection MUST
additionally require launchd to be this process's direct parent (PPID == 1,
which is launchd on every targeted macOS version), the equivalent inheritance
guard for a process that inherited the variable from a launchd-managed parent
without being launched by launchd itself. A label prefixed `application.`
MUST also be treated as absent regardless of parentage: that is macOS's own
label for an ordinary foreground GUI application (an IDE, a terminal app),
not a launch agent, and a `daemon serve` run from an IDE's integrated
terminal MUST NOT be mistaken for one wb should try to kickstart or refuse
under. When launchd is detected, the job label MUST also be recorded, because
it is what distinguishes wb's own self-managed job (see REQ:
restart-hands-off-to-a-live-supervisor) from a foreign one.

A detached child this build starts itself (`wb daemon start`'s unsupervised
path) MUST NOT inherit any of `INVOCATION_ID`, `SYSTEMD_EXEC_PID`,
`JOURNAL_STREAM`, or `XPC_SERVICE_NAME` from the process that starts it, so it
can never misdetect a supervision it does not actually have.

#### REQ: status-reports-the-recorded-supervisor

`wb daemon status` MUST report the managed daemon's recorded supervisor as
`supervisor=systemd|launchd|none`, in both text and JSON, for every record —
including one written before this field existed, which MUST be reported as
`none` rather than left blank or guessed at.

#### REQ: restart-hands-off-to-a-live-supervisor

Every path that stops a running daemon in order to start its replacement —
`wb daemon restart`, the self-update after-update hook, and `wb daemon
start`'s executable-handoff branch — MUST consult the running daemon's
recorded supervisor before choosing how to bring the replacement up. When it
is `none`, the existing detached-start behavior is unchanged.

wb's own self-managed launchd job (the fixed label its own installer uses)
MUST also be treated as `none` for this decision, not as a supervisor to hand
off to: wb's own re-bootstrap-and-kickstart cycle is what already correctly
restarts it with a new binary, and there is no separate external supervisor
in that case. Only a *foreign* launchd label — one wb did not install — and
systemd are handed off to.

For those, the path MUST first confirm that the daemon's recorded executable
path, read from disk right now, matches this build's own content: a caller
MUST NOT stop or replace a supervised daemon merely because its own binary
differs from the recorded provenance, since that difference is equally true
of a caller with no business touching that daemon at all. It is only safe to
proceed when the installed executable at the recorded path has itself become
this build's content — the shape a genuine self-update produces by replacing
that path in place. When it has not, the running daemon MUST be left
untouched, and how the mismatch is reported depends on how this path was
reached: an EXPLICIT `wb daemon restart` or the self-update after-update hook
— both asking, on purpose, to bring up a specific new binary — MUST report
the mismatch as a failure with a non-success exit code, since the operator or
the self-update explicitly asked for a restart that did not happen. An
IMPLICIT `Start` call from a caller that never asked to manage this daemon's
lifecycle at all (`wb dashboard --local`, or an RPC client's own automatic
bootstrap) MUST NOT fail outright merely because THIS invocation's own binary
differs from a live, healthy, supervised daemon's: the caller only wanted a
working connection, and a healthy live daemon already answers that need. It
MUST instead get the live daemon back, with the mismatch reported as a
non-fatal warning rather than an error.

Otherwise the path MUST stop the running process and then wait, bounded, with
progress, and honoring context cancellation, for the supervisor's own
replacement process to report ready under the new executable's provenance, an
unrecycled process generation, and an owned health check — and MUST NOT start
a detached daemon of its own while waiting or after the bound is reached. A
ready, alive replacement running a *different* executable than targeted MUST
fail fast with a message naming both, rather than waiting out the full bound.
A bound reached without the supervisor's own replacement becoming ready MUST
be reported as a distinct, actionable condition and exit with a non-success
code, never silently fall back to a detached start
([#617](https://github.com/sneat-dev/wb/issues/617), recurrence of
[#546](https://github.com/sneat-dev/wb/issues/546)).

#### REQ: detached-start-is-refused-under-a-supervisor

`wb daemon start` MUST refuse to launch a detached process when the runtime's
last recorded state names a supervisor (`systemd`, or `launchd` under a
*foreign* label — wb's own self-managed launchd job MUST NOT be refused,
since `wb daemon start` is itself how that job is (re)installed) and no live
process of this build is already being handed off from. The refusal MUST name
the recorded supervisor and say to restart the daemon through it, rather than
through `wb daemon start`, and MUST name the specific unit when it is known
(a launchd job label; nothing here records a systemd unit name).

Before refusing, an independent check MUST confirm that the recorded
supervisor can still be shown to exist at all; when it cannot, the record MUST
be treated as stale and the start MUST proceed rather than refuse forever. An
explicit override flag MUST also exist for an operator to bypass the refusal
in a case that check cannot resolve either way, and the refusal message MUST
name it.

A refusal for a daemon that is confirmed alive, merely on a different
`--listen` than requested, MUST say so plainly and MUST NOT be phrased as
uncertainty about whether it is running at all.

#### REQ: double-owner-state-is-detected

`wb daemon status` MUST attempt to detect the double-owner state in which a
live, otherwise-healthy daemon of this home's recorded supervisor disagrees
with what this build can independently observe about it right now — evidence
that a supervisor may exist for this runtime and be racing an unsupervised
process for it.

On a platform where it is possible, the check SHOULD confirm a *specific*
supervisor unit's own existence and failure state directly, rather than only
a coarser comparison: on Linux, when the daemon recorded `supervisor=none`,
querying the configured or default systemd user unit's own state
(`ActiveState`/`Result`/`NRestarts`) and flagging it as still fighting for the
runtime when it is `failed`, or `activating` after at least one restart, is
the precise detector this feature exists for — confirmed live: an orphaned,
unsupervised `daemon serve` (PPID 1, running in a session scope, not the
unit's own cgroup) served the port while its systemd unit sat crash-looping.
This is a strictly narrower question than the coarser comparison below can
answer, because that daemon's own cgroup shows no service membership at all.

Confirming a specific supervisor unit's existence and failure state is not
required to satisfy this requirement when doing so has no portable
implementation, or when the specific check above is unavailable or does not
apply (the recorded supervisor is not `none`): a narrower, best-effort
comparison between the recorded supervisor and independently observed
evidence about the process itself is an acceptable fallback implementation,
in either direction (recorded `none` while independent evidence shows a
systemd unit, and recorded `systemd` while it does not), provided its
platform coverage and its limits — including any platform on which it can
only report "unknown" — are documented where the comparison is implemented
and in the change that introduces it.

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

### AC: supervisor-is-detected-and-reported (verifies REQ:supervisor-is-recorded-at-startup, REQ:status-reports-the-recorded-supervisor)

Scenario: A systemd-started daemon records and reports it
Given `daemon serve` starts with `INVOCATION_ID` set and `SYSTEMD_EXEC_PID` naming this process's own PID
When `wb daemon status` runs against that daemon's lifecycle record
Then it reports `supervisor=systemd`

Scenario: An inherited `INVOCATION_ID` alone is not mistaken for supervision
Given `daemon serve` starts with `INVOCATION_ID` set but no `SYSTEMD_EXEC_PID` naming this process, because it merely inherited the variable from a systemd-managed parent
When `wb daemon status` runs against that daemon's lifecycle record
Then it reports `supervisor=none`

Scenario: An inherited `XPC_SERVICE_NAME` without launchd as the direct parent is not mistaken for supervision
Given `daemon serve` starts with `XPC_SERVICE_NAME` set to a job label but its own parent process is not launchd (PID 1)
When `wb daemon status` runs against that daemon's lifecycle record
Then it reports `supervisor=none`

Scenario: A daemon predating this field reports none
Given a lifecycle record with no recorded supervisor
When `wb daemon status` runs
Then it reports `supervisor=none`

### AC: a-supervised-restart-hands-off-not-doubles (verifies REQ:restart-hands-off-to-a-live-supervisor)

Scenario: A supervised daemon is restarted
Given a running daemon whose lifecycle record names a supervisor other than wb's own self-managed launchd job
When `wb daemon restart` (or the self-update after-update hook) runs
Then the running process is stopped, no detached replacement is started, and the command waits for a process matching the new executable's provenance to report ready before succeeding

Scenario: The supervisor does not bring the daemon back
Given a stopped, supervised daemon whose supervisor does not restart it within the bound
When the restart path's wait expires
Then it reports the timeout as a distinct condition, exits with a non-success code, and has started no detached daemon of its own

Scenario: The supervisor's replacement is a different build than targeted
Given a supervised daemon that was stopped so a specific new executable could take over
When the supervisor's own replacement becomes ready first, running a different executable than the one targeted
Then the restart path fails fast, naming both executables, rather than waiting out the full bound

Scenario: The recorded executable does not yet match this build's own content, on an explicit restart
Given a supervised daemon whose recorded executable path, read from disk right now, does not match this build's content
When an EXPLICIT `wb daemon restart`, or the self-update after-update hook, considers stopping it
Then it reports the mismatch as a failure with a non-success exit code and leaves the running daemon untouched, rather than stopping and replacing it

Scenario: The recorded executable does not yet match this build's own content, on an implicit start
Given a live, healthy, supervised daemon whose recorded executable path, read from disk right now, does not match this build's content
When an IMPLICIT `Start` call from a caller that never asked to manage this daemon's lifecycle (`wb dashboard --local`, or an RPC client's own automatic bootstrap) reaches it
Then it returns the live daemon with the mismatch reported as a warning, not an error, and leaves the running daemon untouched

Scenario: wb's own self-managed launchd job is not treated as a foreign supervisor
Given a running daemon whose lifecycle record names launchd under wb's own fixed job label
When `wb daemon restart` runs
Then it uses wb's ordinary bootstrap-and-kickstart cycle rather than waiting for a foreign supervisor to replace it

### AC: a-detached-start-is-refused-under-a-supervisor (verifies REQ:detached-start-is-refused-under-a-supervisor)

Scenario: `wb daemon start` is run while a supervisor owns the runtime
Given a lifecycle record whose last recorded supervisor is `systemd`, or `launchd` under a foreign label, and no live process to hand off from, and that supervisor is independently confirmed to still exist
When `wb daemon start` runs
Then it refuses, names the recorded supervisor and its unit when known, and says to restart through it instead, or to pass `--force-detached` to override

Scenario: The recorded supervisor can no longer be confirmed
Given a lifecycle record naming a supervisor that an independent existence check can no longer find
When `wb daemon start` runs
Then it treats the record as stale and proceeds with a detached start rather than refusing forever

Scenario: `wb daemon start` is run for wb's own self-managed launchd job
Given a lifecycle record naming launchd under wb's own fixed job label
When `wb daemon start` runs
Then it is not refused, since `wb daemon start` is itself how that job is installed and kept current

Scenario: The daemon is confirmed alive on a different `--listen`
Given a lifecycle record naming a supervisor and a live, healthy process bound to a different `--listen` than requested
When `wb daemon start` runs with the new `--listen`
Then the refusal says the daemon is alive on the other address, rather than phrasing it as uncertainty about whether it is running at all

### AC: a-double-owner-is-flagged-when-observable (verifies REQ:double-owner-state-is-detected)

Scenario: A specific systemd unit is confirmed failed or crash-looping
Given a live, otherwise-healthy daemon of this home whose recorded supervisor is `none`, and its configured or default systemd user unit queried directly as `failed`, or `activating` with at least one restart
When `wb daemon status` runs
Then it reports the disagreement as a distinct condition, naming the unit and its queried state

Scenario: A live daemon's independently observed cgroup membership disagrees with what it recorded
Given a live, otherwise-healthy daemon of this home whose recorded supervisor is `none`, and independently observed evidence (its cgroup membership, where available) that it is in fact systemd-managed
When `wb daemon status` runs
Then it reports the disagreement as a distinct condition, naming both what was recorded and what was observed

Scenario: A recorded supervisor is no longer corroborated
Given a live, otherwise-healthy daemon of this home whose recorded supervisor is `systemd`, and independently observed evidence that it is not currently systemd-managed
When `wb daemon status` runs
Then it reports the disagreement as a distinct condition

Scenario: The platform offers no independent evidence
Given a platform on which the independent check has no implementation
When `wb daemon status` runs
Then it reports the comparison as unknown rather than asserting agreement or disagreement

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
