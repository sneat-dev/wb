# Design: `wb session list` effort / worktree / branch columns

**Date:** 2026-08-24
**Status:** Approved (design discussed in conversation; the original two-step
proposal collapsed to one step — see Discovery)
**Builds on:** the session registry (`internal/session`, `wb session
register|list|prune`) and the worktree owners registry
(`internal/worktrees/owners.go`, `worktrees.ListResult.Owners`)

## Problem

`wb session list` shows who registered (PID, runtime, model, liveness) but
not what each session is working on. Alex's question: can it also show the
effort — and since a session can run several efforts, show the single effort
or a count, and the same for worktrees and branches.

## Discovery (why this is one step, not two)

The attribution link already exists on main: every worktree write records an
`OwnerRegistration{Agent, Model, Effort, PID, WBVersion, Command, At}` in
the worktree's local work log, with `PID` resolved from the registered
session via `worktrees.CurrentIdentity()` → `session.ResolveForProcess`
(explicit `WB_AGENT_*` env declarations win). `worktrees.List` exposes these
as `ListResult.Owners []OwnerView` alongside `Branch` and `WorktreeDir`.
So no new persistence is needed — only the join and the columns.

## Facts the design leans on

- A session ↔ efforts is N:M: one session can create many efforts, and one
  effort can outlive or span sessions (resume/handoff). A scalar column is
  impossible in general; single-value-or-count is the right display.
- Owner custody records are deduplicated per (Agent, PID, Model, WBVersion)
  per worktree — one entry per session per worktree in the common case.
- `OwnerRegistration.At` is when the entry was recorded; `session.Record.
  StartedAt` is when the session registered. PIDs are recycled by the OS.

## Goals

- `wb session list` gains three derived columns: `EFFORTS`, `WORKTREES`,
  `BRANCHES` — one effort ID when the session has exactly one distinct
  effort, else a count; one branch name when exactly one, else a count;
  worktrees always a count (paths are too long to inline).
- `--format json` carries the full lists, not the condensed display.
- Derivation failure must not break `session list`: rows fall back to `-`
  and one warning goes to stderr.
- Read-only: listing sessions must not create WB's home, worktrees state,
  or mutate anything (the existing `sessionDirForRead` philosophy).

## Non-goals

- No changes to what is persisted (owners registry is already sufficient).
- No cross-machine view (this is per-machine, like `wb session list` today;
  the remote-state store is a separate feature).
- No filtering flags beyond the existing `--live` (YAGNI).

## Matching rule

An owner entry belongs to a session when **both**:

1. `owner.PID == session.PID`, and
2. `!owner.At.Before(session.StartedAt)` — the PID-reuse guard: an entry
   recorded before the session registered was written by a previous holder
   of that PID and must not be attributed to the new session.

Re-registering a session (allowed, e.g. to correct its model) re-stamps
`StartedAt`, so the guard also resets the attribution window: entries the
same session wrote before re-registering stop counting. Under-attribution
is the safe direction; preserving the old start would over-attribute after
PID reuse.

Entries written from explicit `WB_AGENT_PID` env declarations join the same
way — they are declarations too. Owner entries whose PID matches no listed
session simply don't surface here (orphan triage is `wb worktree`'s job).

## Derivation

For the sessions being rendered (after the `--live` filter, so a filtered
list never pays for worktrees it won't show):

- Run `worktrees.List` once over WB's home (all tasks, no owner-state
  filter — an orphaned worktree's history still tells what the session
  worked on; its own PID liveness is already the session's `STATE`).
- For each session, over matching owner entries across all results:
  - `Efforts`: sorted distinct non-empty `Effort` values.
  - `Worktrees`: sorted distinct `WorktreeDir`s of results with a match.
  - `Branches`: sorted distinct `Branch`es of results with a match.

Display: empty → `-`; exactly one effort → its ID; exactly one branch → its
name; more → the count as a plain number. `WORKTREES` shows the count (`-`
for zero). Long single values are truncated to 24 runes with `…` in text
mode only.

## Output

Text (new columns appended before STATE):

```
PID     RUNTIME      MODEL   WB       STARTED           EFFORTS       WORKTREES  BRANCHES  STATE
41231   claude-code  fable   0.45.0   2026-08-24 09:10  wb-claims-e2  1          2         live
41777   claude-code  opus    0.45.0   2026-08-24 08:55  3             4          4         live
```

JSON: each element becomes

```json
{
  "pid": 41231, "runtime": "claude-code", "...": "...", "state": "live",
  "efforts": ["wb-claims-e2e"],
  "worktrees": ["/home/ai/.wb/worktrees/task-7/acme/x"],
  "branches": ["agent/task-7", "agent/task-9"]
}
```

(`session.View` stays untouched in `internal/session`; the enriched row is a
cmd-layer struct embedding it, so the session package keeps no worktrees
dependency and the JSON shape is additive.)

## Structure

- `internal/session` and `internal/worktrees` remain independent; the join
  lives in `cmd/wb` (a `session_attribution.go` helper beside
  `session_list.go`), mirroring how claims staleness joins live in the
  command layer.
- The helper takes `[]session.View` and `[]worktrees.ListResult` and returns
  the enriched rows — pure function, unit-testable without fixtures.

## Failure handling

| Situation | Behaviour |
|---|---|
| `worktrees.List` errors (corrupt home, permissions) | all rows render `-` in the three columns; one `derive worktree attribution: <err>` line on stderr; exit code unchanged |
| WB home absent (fresh machine) | same as empty: `-` columns, no error |
| No sessions registered | existing "no session has registered" message, worktrees never scanned |
| Owner entry with zero/absent At | fails the reuse guard only if session started later; treated normally otherwise |

## Testing

- Pure-function tests for the join: multiple sessions, PID reuse (older
  `At` excluded), effort dedup across worktrees, single-vs-count display,
  empty effort strings ignored, truncation.
- Command test: register two fake session records in a temp WB_HOME, create
  work-log owner entries via the worktrees package's own write path (or
  fixture files matching its format if the write path needs a full
  worktree), assert text columns and JSON lists; `--live` interplay;
  worktrees.List failure degrades to `-` + stderr warning.
- All hermetic (temp dirs, no git remotes needed).

## Open questions

None — resolved in design review: single-value-or-count display (Alex's
suggestion), counts-only for worktrees, join in the command layer, PID-reuse
guard via `StartedAt`/`At`.
