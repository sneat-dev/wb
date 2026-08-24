# Design: `wb remote claim` — fleet-wide task claims

**Date:** 2026-08-24
**Status:** Approved (design discussed in conversation, including the
best-effort auto-claim middle ground; implementation plan pending)
**Builds on:** `docs/superpowers/specs/2026-08-23-remote-state-design.md`
(the state store, machine snapshots, and the staleness heartbeat)

## Problem

Two machines — or two people — can start the same task without knowing about
each other. The remote state store (PR #139) answers "who *has* worktrees on
task X" after the fact; nothing answers "may I start task X" before work
begins. The store already reserved `claims/<task>.yaml` for exactly this.

## Goals

- Claim a task fleet-wide with one command; see all claims from any machine.
- Correct under concurrency with no server: two machines claiming the same
  task at once must resolve to exactly one holder, via git push rejection as
  the compare-and-swap.
- No TTL bookkeeping: claim staleness derives from the claimant machine's
  existing snapshot heartbeat.
- Make the common case automatic: `wb worktree create` claims best-effort,
  `wb worktree cleanup`/`abort` release best-effort — without ever making a
  local command fail because the network did.
- Keep the provider seam: claims must be implementable by a future hub
  provider as conditional writes, without touching commands.

## Non-goals

- Enforcement (`worktree create` refusing to run on someone else's claim) —
  future `remote.claims: enforce` config, not built now.
- Claim queues, handoff negotiation, or per-repository claims.
- Auto-claiming from anything other than `wb worktree create`.

## Claim record

`claims/<task>.yaml` at the state-repo root:

```yaml
schema_version: 1
task: task-7
login: trakhimenok
machine: vm-hetzner
claimed_at: 2026-08-24T09:15:00Z   # UTC; refreshed on re-claim by the holder
note: rehearsal bootstrap           # optional, free text
```

- One file per task; the file's existence IS the claim.
- **Release deletes the file.** Git history is the audit trail (who claimed,
  who released, who took over — each is a commit), so there is no `state:`
  field and no tombstones.
- `<task>` must satisfy the same name rule as `machine`
  (`^[A-Za-z0-9][A-Za-z0-9._-]*$`), which also keeps the path safe.
- Commit messages: `wb: claim <task> by <login>/<machine>`,
  `wb: release <task> by <login>/<machine>`,
  `wb: take over <task> from <old-login>/<old-machine> by <login>/<machine>`.

## Compare-and-swap over git

Claiming:

1. Fetch (rebase) the store clone.
2. Read `claims/<task>.yaml`.
   - Absent → write it, commit, push.
   - Held by this exact `login/machine` → refresh `claimed_at`, commit, push
     (a no-op refresh with an unchanged file still commits nothing; the
     refresh commit exists because `claimed_at` changed).
   - Held by anyone else → refuse (see takeover below); never overwrite
     silently.
3. Push rejected → rebase → **re-read the file**:
   - The rebase brought someone else's claim for the same task → abort with
     `lost the race for <task> to <login>/<machine>` and roll back the local
     commit (`git reset --hard @{u}` on the state clone only). Exit 1.
   - Still ours/absent-conflict-free → push again, once; a second rejection
     fails like publish does (local commit kept).

This is the same publish machinery with one addition: the post-rebase
re-read. The race window between re-read and push is closed by the next
rejection cycle, not by locking. Observed behaviour: because every
claim/release commit touches only the claimant's own `claims/<task>.yaml`,
a same-task race surfaces as an actual rebase conflict on that path (a
different-task commit rebases cleanly instead).

### Same login, different machine

A claim held by the same `login` on another machine is **still another
holder** — mutual exclusion is per machine, because the point is to stop two
concurrent worktrees, not two people. Messages soften to "claimed by you on
<machine>", but refresh applies only to the exact `login/machine`, and
takeover rules apply unchanged.

## Staleness and takeover

- A claim has no expiry. It is **stale exactly when its claimant machine's
  snapshot is stale** — `published_at` older than the same `--stale` window
  (default 24h) that `wb remote status` uses. A machine that keeps
  publishing keeps all its claims fresh; a machine that died goes stale as
  one unit. A claimant with **no snapshot at all** is treated as stale (it
  never published, so it has no heartbeat to be fresh).
- `wb remote claim <task> --take-over` succeeds only if the current claim is
  stale; the takeover commit records the previous holder.
- `--force` replaces even a fresh claim, printing loudly whose claim is
  being overridden. It exists for the "colleague on vacation" case and is
  never used by any automatic path.

## Commands

- `wb remote claim <task> [--note <text>] [--take-over | --force] [--json]`
  Exit 0 on acquired/refreshed; exit 1 on held-by-other (the refusal names
  the holder, their machine's heartbeat age, and whether `--take-over`
  would work); exit 2 unconfigured (same snippet as publish).
- `wb remote release <task> [--json]`
  Releases only a claim held by this `login/machine`; releasing someone
  else's claim requires `--force` (loud). Releasing a non-existent claim is
  exit 0 with a note (idempotent).
- `wb remote claims [--json] [--stale <dur>]`
  Lists all claims: task, holder, `claimed_at`, holder-heartbeat age, STALE
  flag. Exit 0 always; malformed claim files render as error rows (same
  philosophy as `machines`).
- `wb remote status` gains a `claims` line per machine section and includes
  claims in its `--json` output.

`--projects-root` is accepted by all three (it locates the store clone);
`--filter`/`--org` are rejected. All three need entries in
`persistentFlagSupport`, `ai/skills` coverage, `ai/capabilities.json`, and
the flag matrix — same gates as the existing remote commands.

## Auto-claim and auto-release (best-effort, never blocking)

When a `remote:` store is configured:

- **`wb worktree create <task>`** attempts a claim before creating
  worktrees, with outcome one of:
  - `acquired` / `refreshed` — proceed.
  - `held by <login>/<machine>` (fresh) — **warn and proceed**; the claim is
    not touched. (Enforcement is future work.)
  - `held by you on <machine>` — warn ("you already work on this
    elsewhere"), proceed, claim untouched.
  - `held (stale)` — take over automatically (this is the safe case: the
    holder's machine stopped publishing), noting the previous holder.
  - `skipped: store unreachable` — printed clearly, command proceeds.
    Offline never blocks starting work.
  The outcome is printed and included in `worktree create --format json`
  output; the store's git history is the durable audit (the Work Log stays
  a prompt journal and is not written).
  `--no-claim` skips the attempt (scratch/experimental worktrees).
  When no `remote:` store is configured, nothing changes at all.
- **`wb worktree cleanup <task> --apply`** and **`wb worktree abort <task>`**
  (discard or handoff outcomes) release the claim best-effort — only if it
  is held by this exact `login/machine`; failures print a
  `release skipped: …` line and never change the command's exit code.
- Auto paths never use `--force`, and auto-takeover applies only to stale
  claims.

## Provider seam

`internal/remotestate.Provider` grows:

```go
Claim(ctx context.Context, claim Claim, mode ClaimMode) (ClaimOutcome, error)
Release(ctx context.Context, task, login, machine string, force bool) (ReleaseOutcome, error)
Claims(ctx context.Context) ([]ClaimEntry, error)
```

- `ClaimMode`: `ClaimNormal | ClaimTakeOverStale | ClaimForce`.
- `ClaimOutcome` reports `Acquired | Refreshed | Held{By} | TookOver{From}`
  so commands own all messaging; the provider owns none.
- Staleness evaluation lives in the **command layer** (it joins `Claims`
  with `List` heartbeats); the provider only refuses non-force overwrites of
  existing claims and reports who holds them. A hub provider maps `Claim` to
  a conditional PUT and `Release` to a conditional DELETE.
- The git provider implements CAS as described; `Claims` walks
  `claims/*.yaml` (wrong-depth and undecodable files become error entries).

## Failure handling

| Situation | Behaviour |
|---|---|
| Unconfigured | exit 2 with the YAML snippet (all claim commands) |
| Claim held by another (no flag) | exit 1, names holder + heartbeat age |
| `--take-over` on a fresh claim | exit 1: "claim is fresh; ask <holder> to release, or use --force" |
| Lost the CAS race | exit 1, local commit rolled back on the state clone |
| Push rejected twice | claim/release: exit 1; the local change is discarded (claims are re-creatable) so the clone stays healthy; publish keeps its commit as before |
| Malformed claim file | error row in `claims`; `claim` on that task refuses without `--force` (unreadable ≠ unheld) |
| Store unreachable in auto paths | printed `skipped`/`release skipped` line; exit code of the host command unchanged |
| Store unreachable in explicit commands | exit 1 (an explicit claim that didn't happen must fail loudly) |

## Testing

- `gitrepo` CAS: two providers race for the same task via the existing
  pre-receive-hook rejection fixture → exactly one `Acquired`, the loser
  gets `Held` after its rebase re-read and the store shows one claim.
- Refresh, release (idempotent, wrong-holder refusal, `--force`), takeover
  (stale-only vs `--force`), malformed-file refusal.
- Staleness join: claims listed against machine snapshots with old/absent
  `published_at`; claimant-without-snapshot is stale.
- `cmd/wb`: exit codes per the table; `claims`/`status` rendering; JSON
  shapes; auto-claim outcomes in `worktree create` including the
  unreachable-store path (point the store URL at a nonexistent path) and
  `--no-claim`; auto-release in cleanup/abort; outcome asserted on stdout and in `--format json` (the Work Log is not written).
- All tests hermetic: bare local origins, `t.Setenv` git identity, no `gh`.

## Open questions

None — resolved in design review: release semantics (delete file), staleness
source (machine heartbeat, no TTL), same-login-different-machine (another
holder with softened wording), auto-claim default (on, best-effort,
non-blocking) and its opt-out (`--no-claim`).
