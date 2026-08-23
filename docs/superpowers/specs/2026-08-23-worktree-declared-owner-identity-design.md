# Design: declared agent identity on worktree writes

**Date:** 2026-08-23
**Status:** Approved

## Problem

WB records who owns a worktree in an append-only `owner_attached` event
(`internal/worktrees/owners.go`). Three things make that record unable to
answer the one question it exists for — *is this worktree still being worked
on?*

**1. The recorded PID is WB's own, and WB is a short-lived CLI.**
All three call sites pass `currentProcessID()`, i.e. `os.Getpid()`
(`worktrees.go:681`, `journal.go:472`, `log_verbs.go:190`). WB exits within
seconds, so `ownerPIDStatus` reads a dead PID on every subsequent look.
Worse, PIDs are recycled: a dead WB PID can later match an unrelated live
process and report `active` for a worktree nobody is touching. The field is
not merely useless, it is occasionally wrong in the dangerous direction.

**2. Ownership is only ever recorded at create/attach time.** Nothing
re-records it, so a worktree handed from one session to another keeps naming
the first. There is no chain of custody.

**3. Worktrees WB did not create carry nothing at all.** Every worktree made
with plain `git worktree add` has no `.wb/local/` state, so there is no owner,
no PID, and no way for WB to know it is alive. This is not hypothetical: at
the time of writing, all four linked worktrees in the fleet are in exactly
that state, and `wb worktree orphans` classifies them purely on commit age
(`orphanDisposition`, `orphans.go:354` — `now.Sub(LastCommit) < staleAfter`
means `active`). The word "likely" in its output is load-bearing.

## Goals

- Make an owner record able to answer "is the owning session still alive?"
- Give a worktree a chain of custody across sessions, not just a creator.
- Record which WB version wrote each entry.
- Tell an agent, at the moment it matters, how to identify itself.

## Non-goals

- Inferring identity by walking the process tree from `os.Getppid()`. It
  breaks under shell wrappers, tmux, and `nohup`, differs by platform, and
  would contradict the convention below.
- Migrating existing records. Every WB-created worktree in the fleet was
  cleaned up on 2026-08-23, so there are none carrying legacy WB PIDs.
  Entries without `wb_version` are treated as pre-declaration and their PID
  is ignored rather than trusted.

## Governing convention

`journal.go:49` already states WB's rule for this class of data: *"PromptSource
is recorded, never inferred."* Prompts are `harness_observed`,
`agent_declared`, or `human_declared`. Owner identity follows the same rule.
WB records what it is told and what it can see for itself, and never guesses
the rest.

## Design

### 1. Schema

`OwnerRegistration` gains two fields, and `PID` changes meaning:

```go
type OwnerRegistration struct {
    Agent     string    `json:"agent,omitempty"`
    Model     string    `json:"model,omitempty"`
    Effort    string    `json:"effort,omitempty"`
    PID       int       `json:"pid,omitempty"`        // the AGENT session, declared
    WBVersion string    `json:"wb_version,omitempty"` // new
    Command   string    `json:"command,omitempty"`    // new
    At        time.Time `json:"at"`
}
```

`PID` is the declared agent session PID. WB never writes its own PID into it;
`currentProcessID` is removed. An undeclared PID is absent, so
`ownerPIDStatus` returns `unknown` — an honest answer, rather than a dead or
recycled one.

`WBVersion` and `Command` are always populated, because WB always knows them.
They give provenance even when no agent identity is declared.

### 2. Declaring identity

Two routes, both explicit.

**Environment, for a whole session** (preferred — set once, applies to every
later command):

| Variable | Meaning |
|---|---|
| `WB_AGENT_PID` | the agent session's process id |
| `WB_AGENT_RUNTIME` | harness, e.g. `claude-code`, `copilot-cli`, `codex` |
| `WB_AGENT_MODEL` | model identifier |
| `WB_AGENT_ID` | session id, optional |

**Command, for a one-shot**: `wb worktree own [path]` with `--pid`,
`--runtime`, `--model`, `--agent-id`. This is what the warning points at.

`Agent` is composed as `runtime/agent-id`, or just `runtime` when no id is
given, matching the existing `ownerAgent` helper.

### 3. When WB appends an entry

On **mutating** worktree operations only — those that write into the worktree
or its `.wb/local/` state (create, set, log, rename, abort, backfill, own).
Read-only commands (`info`, `list`, `orphans`, `summary`) never write; a
command that reports on a worktree must not mutate it.

Before appending, WB compares the current identity against the **last**
`owner_attached` entry. It appends only when they differ, keyed on
`(Agent, PID, Model, WBVersion)`. A session performing ten writes therefore
adds one record, not ten, and the log stays a custody chain rather than a
command trace.

An entry is written **even when no agent identity is declared** — carrying
`WBVersion`, `Command` and `At`. That is real provenance, and its absent PID
correctly reads as `unknown`.

### 4. The warning

Emitted on a mutating operation when the declared identity is missing, or
names a different session than the last recorded owner:

```
warning: this worktree has no declared agent owner, so WB cannot tell whether
work here is still live.
  Register:  wb worktree own . --pid <agent-pid> --runtime claude-code --model <model>
  Or export: WB_AGENT_PID WB_AGENT_RUNTIME WB_AGENT_MODEL [WB_AGENT_ID]
```

**Always to stderr**, never stdout, so `--format json` and `--format yaml`
output stays machine-parseable. Silent once a matching identity is recorded,
so it stays a signal rather than noise an agent learns to skip.

### 5. Version plumbing

`version` currently lives in `package main` (`cmd/wb/version.go:17`), which
internal packages cannot import. Extract the resolution — the ldflags-stamped
var, falling back to `debug.ReadBuildInfo()`, then `"unknown"` — into
`internal/buildinfo`, with `buildinfo.Set()` called from `cmd/wb` so the
release stamp still wins. `cmd/wb/version.go` then consumes the same helper,
keeping one definition of what version means.

## Consequences

`wb worktree orphans` is not changed here, but this makes a later improvement
possible: with a declared PID it could report `active (PID n live)` versus
`orphaned (PID n gone)` instead of guessing from commit age, and `unknown`
for worktrees carrying no declaration — which is the honest answer for the
four currently in the fleet.

## Testing

- `internal/buildinfo`: resolution order — explicit `Set` wins; empty falls
  back to build info; ultimately `"unknown"`.
- `internal/worktrees`: `OwnerRegistration` round-trips the new fields;
  `ownerPIDStatus` returns `unknown` for an absent PID.
- Identity from environment: all four variables read; partial declarations
  handled; `Agent` composed as `runtime/agent-id` and as bare `runtime`.
- Append rule: identical identity twice appends **one** entry; a changed PID,
  model, agent, or WB version appends a second.
- Undeclared identity still appends an entry carrying `WBVersion` and
  `Command`.
- Read-only commands append nothing — assert the event count is unchanged
  across `wb worktree info`.
- Warning: emitted on **stderr** when undeclared, absent once declared, and
  never present on stdout when `--format json` is requested.
- `wb worktree own` records the declaration and silences the warning.
