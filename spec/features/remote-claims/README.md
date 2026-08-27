---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Remote Claims

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-claims?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-claims?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-claims?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/remote-claims?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb remote claim`/`release`/`claims` reserve a task fleet-wide in the same
`git`-backed store that `wb remote publish` writes:

- `wb remote claim <task> [--note <text>] [--take-over | --force] [--json]` —
  acquire, refresh, or take over a task claim.
- `wb remote release <task> [--force] [--json]` — release a claim this
  `login/machine` holds (or, with `--force`, anyone's).
- `wb remote claims [--json] [--stale <dur>]` — list every claim with holder,
  claim time, and staleness.
- `wb remote status` includes a `claims` line per machine and in `--json`.
- `wb worktree create` attempts a best-effort claim automatically;
  `wb worktree cleanup --apply`/`wb worktree abort --apply` release
  best-effort. `--no-claim` opts a worktree out of the automatic claim.

Design: `docs/superpowers/specs/2026-08-24-remote-claims-design.md` (builds on
`docs/superpowers/specs/2026-08-23-remote-state-design.md`).

## Problem

Two machines — or two people — can start the same task without knowing about
each other. `wb remote status` answers "who *has* worktrees on task X" after
the fact; nothing answers "may I start task X" before work begins.

## Behavior

### Claim record

#### REQ: claim-file-is-the-claim

A claim MUST be represented by exactly one file,
`claims/<task>.yaml`, at the state-repo root; the file's existence IS the
claim. `<task>` MUST satisfy the same name rule as `machine`,
`^[A-Za-z0-9][A-Za-z0-9._-]*$`.

#### REQ: claim-release-deletes-file

Releasing a claim MUST delete `claims/<task>.yaml`. There MUST be no
`state:` field or tombstone; the store's git history (one commit per claim,
release, and takeover) MUST be the audit trail.

### Compare-and-swap over git

#### REQ: claim-cas-sequence

Claiming MUST fetch/rebase the store clone, read the claim file, then
write-or-refuse, commit, and push. A push rejected by a concurrent write
MUST be retried exactly once after a rebase and a re-read of the claim file.

#### REQ: claim-race-resolution

Because every claim/release commit touches exactly one file (the claimant's
own claim path), a rebase after a rejected push that hits a genuine conflict
on that path MUST be treated as a competing claim for the same task: the
conflicted rebase is aborted, the local commit is rolled back
(`git reset --hard @{u}` on the state clone only), and the command fails
with `lost the race for <task> to <login>/<machine>`. A rebase that
completes cleanly (a concurrent commit for a different task) MUST NOT be
treated as a race.

#### REQ: claim-same-login-different-machine

A claim held by the same `login` on a different `machine` MUST be treated
as another holder for mutual-exclusion purposes; only the message MUST
soften to reference "you". Refreshing a claim MUST apply only to the exact
`login/machine` that holds it.

### Staleness and takeover

#### REQ: claim-staleness-from-heartbeat

A claim MUST be stale exactly when its holder machine's effective
heartbeat — the later of its snapshot's `published_at` and `last_seen_at`
(stamped by the claim, refresh, release, and take-over mutations
themselves) — is older than the `--stale` window (default `24h`), or when
the holder has no snapshot at all. A claim MUST have no separate expiry or
TTL.

#### REQ: claim-take-over-stale-only

`--take-over` MUST replace a claim only when it is currently stale; the
takeover commit MUST record the previous holder. `--force` MUST replace any
claim, stale or fresh, and MUST print who is being overridden.

### Commands

#### REQ: claim-command-claim

`wb remote claim <task>` MUST exit 0 on acquire or refresh, exit 1 when held
by another holder without `--take-over`/`--force` succeeding (naming the
holder, their effective-heartbeat age, and whether `--take-over` would
work), and exit 2 when `remote:` is unconfigured.

#### REQ: claim-command-release

`wb remote release <task>` MUST release only a claim held by this exact
`login/machine`; releasing another holder's claim MUST require `--force`.
Releasing a task with no claim MUST be a no-op that exits 0.

#### REQ: claim-command-claims

`wb remote claims` MUST list every claim (task, holder, `claimed_at`,
holder's effective-heartbeat age, `STALE` flag) and MUST exit 0 even when
some claim files cannot be decoded, rendering those as error rows instead
of dropping them.

#### REQ: claim-command-flag-scope

All three claim commands MUST accept `--projects-root` (it locates the
state clone) and MUST reject `--filter` and `--org`.

#### REQ: claim-malformed-file-refused

A malformed claim file MUST render as an error row in `wb remote claims`
and MUST cause `wb remote claim` on that task to refuse without `--force`;
an unreadable claim file is never treated as unheld.

### Auto-claim and auto-release

#### REQ: claim-auto-on-worktree-create

When a `remote:` store is configured, `wb worktree create <task>` MUST
attempt a best-effort claim before creating worktrees, with outcome one of:
acquired, refreshed, held-by-another (fresh — warn and proceed, claim
untouched), held-by-you-elsewhere (warn and proceed), took-over-stale
(automatic, only when the existing claim is stale), or
skipped-store-unreachable. The attempt MUST never fail the command; a
network problem MUST only be reported, not enforced. `--no-claim` MUST skip
the attempt. When no `remote:` store is configured, behavior MUST be
unchanged.

#### REQ: claim-auto-outcome-visibility

The auto-claim outcome MUST be printed to stdout and MUST be included in
`wb worktree create --format json` output as the `remote_claim` field. It
MUST NOT be written into the task's Work Log; the Work Log stays a private
prompt journal. The state repository's git history is the durable audit
trail for the claim itself.

#### REQ: claim-auto-release-on-cleanup-and-abort

`wb worktree cleanup <task> --apply` and `wb worktree abort <task> --apply`
(discarded or handoff outcomes) MUST attempt to release the claim
best-effort, only when it is held by this exact `login/machine`. A release
failure MUST print a `release skipped: …` line and MUST NOT change the
host command's exit code.

#### REQ: claim-auto-never-forces

No automatic path MUST ever pass `--force`, and automatic takeover MUST
apply only to a claim that is currently stale.

## Acceptance Criteria

### AC: claim-file-is-a-cas-over-git

**Requirements:** remote-claims#req:claim-file-is-the-claim, remote-claims#req:claim-release-deletes-file, remote-claims#req:claim-cas-sequence, remote-claims#req:claim-race-resolution, remote-claims#req:claim-same-login-different-machine

A claim is exactly one file whose existence is the claim, release deletes
it, and mutual exclusion under concurrent claimants is resolved through
git's own push-rejection-then-rebase mechanics rather than a lock: a
genuine rebase conflict on the claimant's own path is the observed signal
of a race, and same-login-different-machine is still treated as another
holder.

### AC: staleness-derives-from-heartbeat-not-a-timer

**Requirements:** remote-claims#req:claim-staleness-from-heartbeat, remote-claims#req:claim-take-over-stale-only

A claim carries no expiry of its own; it is stale exactly when its holder's
effective heartbeat — the later of publish and claim activity
(`last_seen_at`) — is stale (or absent). `--take-over` only ever replaces a
stale claim; only `--force` replaces a fresh one, loudly.

### AC: three-commands-share-consistent-flags-and-exit-codes

**Requirements:** remote-claims#req:claim-command-claim, remote-claims#req:claim-command-release, remote-claims#req:claim-command-claims, remote-claims#req:claim-command-flag-scope, remote-claims#req:claim-malformed-file-refused

`wb remote claim`, `wb remote release`, and `wb remote claims` all accept
`--projects-root` and reject `--filter`/`--org`, share the exit-code
conventions (0 success/idempotent, 1 refusal/race, 2 unconfigured), and
treat a malformed claim file as visible-but-refusing rather than silently
absent.

### AC: auto-claim-and-auto-release-never-block-the-host-command

**Requirements:** remote-claims#req:claim-auto-on-worktree-create, remote-claims#req:claim-auto-outcome-visibility, remote-claims#req:claim-auto-release-on-cleanup-and-abort, remote-claims#req:claim-auto-never-forces

`wb worktree create` attempts a best-effort claim (opt out with
`--no-claim`) and `wb worktree cleanup`/`abort --apply` attempt a
best-effort release; both report their outcome — the create outcome lands
in stdout and `--format json`'s `remote_claim` field, never in the Work
Log — and neither ever changes the host command's exit code or reaches for
`--force`.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
