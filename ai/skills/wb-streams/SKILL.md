---
name: wb-streams
description: Run one named cross-repository unit of work with WB streams — a library and the consumers that must change with it, each on a stream/<name> branch with a draft pull request, coordinated by wb stream start/join/status/end. Use when a change spans a library and its consumers, when a repository refuses a second stream, when you need to know which consumers are linked, behind, or waiting on an untagged library, or when a stream must be retired without publishing anything.
---

# WB streams

A **stream** is one named cross-repository unit of work spanning a library and
the consumers that must change with it. Each member gets one worktree on branch
`stream/<name>` with a **draft** pull request to its base, so CI runs on every
push and the stream's true state is always visible.

The stream name **is** the WB worktree task name. There is no second identity
for the same work, and every checkout is created by the existing
`wb worktree create` path with its branch policy, prompt archival and
fleet-wide claim.

## Lane contract

- Consume the library through `wb deps propagate local`; the orchestrator runs
  `wb deps propagate remote` at the end.
- Branch from `stream/<name>` into your own worktree and open your pull request
  **against the stream branch**, never against `main`.
- End with `wb worktree end`.

## Fast path

```sh
printf '%s\n' 'the exact task request' | wb stream start checkout-rewrite acme/library acme/app --mode manual --initiator me@example.com --model unknown --original-prompt-file -
wb stream status checkout-rewrite
wb stream end checkout-rewrite --apply
```

## Verbs

### `wb stream start <name> <owner/repository>...`

Creates one worktree per repository on `stream/<name>`, each with a draft pull
request to its base, and records the set as one stream in WB-owned state.

The first repository is the **library** — the one whose published artifacts the
others resolve — unless `--library` names another.

Flags: `--library`, `--base`, `--format`, plus the Work Log provenance flags
`--mode`, `--initiator`, `--model`, `--original-prompt-file` (required, `-`
reads stdin), `--agent`, `--agent-runtime`, `--effort`, `--run`, `--cli`,
`--provider`.

Before anything is created it proves the fleet is ready per member:
`wb hooks check`, an npm provider-identity scan, a red-base check, and a check
that each member's pull-request workflow carries a `concurrency` group keyed to
the ref with `cancel-in-progress: true`.

### `wb stream join <name> <owner/repository>`

Adds a repository to an existing stream, creating its stream worktree, branch
and draft pull request exactly as `start` does. It is the sanctioned answer to
the one-stream-per-repository refusal. Joining an existing member is a no-op.

Flags: `--role` (`consumer` default, or `library`), `--base`, `--format`, and
the same Work Log provenance flags as `start`.

```sh
wb stream join checkout-rewrite acme/reports --role consumer --mode manual --initiator me@example.com --model unknown --original-prompt-file ./prompt.txt
```

### `wb stream status [name]`

Reports the three states in which a stream is incomplete, **separately** and
named per repository:

1. consumers holding a live local link, and the library worktree each links to
2. library changes merged into the base but not yet tagged or published
3. consumers still declaring a version older than the library's newest tag

It also lists every open pull request targeting a stream branch, and collapses
patch-identical unabsorbed commits so N branches carrying one body of work read
as one cluster. Everything is reconstructed from stream state, so it answers
after an interrupted session.

Anything WB could not establish is listed under **could not establish** — an
empty gap list is never readable as "nothing is wrong".

With no name, every stream is listed. Flags: `--format`.

### `wb stream end <name>`

Closes or retargets every still-open pull request against the stream branches,
closes each member's own draft pull request, deletes `origin/stream/<name>`,
releases the leases, and retires every stream worktree through
`wb worktree cleanup`.

**Ending publishes, bumps and merges nothing.**

Without `--apply` it reports exactly what it would do and changes nothing.
Flags: `--apply`, `--retarget`, `--keep-remote-branch`, `--force-unabsorbed`,
`--reason`, `--format`.

The absorption guard **fails closed**: if the patch-identity comparison could
not run at all — a stale or missing `origin/<base>`, an unreachable remote — the
end is refused. A check that cannot answer must not pass.
`--force-unabsorbed --reason "<why>"` steps over it; the reason is mandatory and
both the reason and every finding stepped over are written to the event log.

`end` also retires a stream that was interrupted while it was being created:
the record carries every member's intended coordinates from before the first
side effect, so nothing published is unreachable.

### `wb stream delete <name>`

Removes an ended stream's record and event log. Refuses an **open** stream —
deleting one would strand its worktrees, branches and pull requests with no
record any verb could reach.

Deleting is rarely necessary: `wb stream start` on the name of an ended stream
archives the old record as `<name>.ended-<timestamp>` and proceeds, so a name is
never burned by its first use.

```sh
wb stream delete checkout-rewrite.ended-20260903T101500Z
```

## Exit codes

| code | meaning |
|---|---|
| `0` | success |
| `1` | findings — the verb ran and reported something that needs attention |
| `2` | refusal **or usage error** — a guard fired, or the invocation was ambiguous |

Under `--format json` a refusal and a usage error both emit the envelope on
stdout, carrying `refusal_code`, a single runnable `sanctioned_command`, and
every alternative in `sanctioned_commands`. A caller that asked for JSON never
gets an empty stdout.

A findings exit from `start` does **not** mean the stream was not created. The
stream exists; a member was reported rather than refused. Read the report, or
the `outcome` field of `--format json`.

## Refusals and the sanctioned next step

| refusal code | what fired | do this |
|---|---|---|
| `stream-exists` | the stream name is taken | `wb stream status <name>`, or pick another name |
| `repository-in-stream` | the repository already carries an open stream | `wb stream join <holder> <owner/repository>`, or wait and `wb stream end <holder>` |
| `preflight-failed` | `wb hooks check` findings, or two members publishing the same npm package name | `wb hooks repair <path>`; for an ambiguous package name, fix the duplicate declaration before starting |
| `library-exists` | the stream already has a library | `wb stream join <name> <owner/repository> --role consumer` |
| `no-library` | the stream has no library member at all | `wb stream join <name> <owner/repository> --role library` |
| `usage` | the invocation was ambiguous — a bad name, an unknown `--role`, a missing `--reason` | fix the invocation; the envelope names the correct form |
| `stream-ended` | the stream has ended | `wb stream start <new-name> …` |
| `live-link` | `end` found a consumer still resolving an unpublished working tree | run the exact `wb deps propagate local <library> --to <consumer> --undo` the refusal names, then re-run `end` |
| `unabsorbed-work` | a stream branch carries commits the base has not absorbed, **or** the absorption check could not run | `wb stream status <name>`, then land the work (`wb worktree merge <worktree> --route auto`); if the check could not run, retry once origin is reachable, or `wb stream end <name> --apply --force-unabsorbed --reason "<why>"` |
| `stream-exists` on an **ended** stream | does not happen — `start` archives it and proceeds | nothing to do |

Never work around a refusal by hand-chaining `git` or `gh`. Every refusal names
the command that satisfies it; if the named command does not resolve it, that is
a WB defect worth reporting, not a reason to bypass the guard.

## Why a repository carries at most one open stream

Two concurrent streams on one repository are out of scope, and the refusal is
what keeps them so: stream A landing rewrites `main` under stream B, whose
already-approved agent branches would then all need re-rebasing and could each
conflict with A's landed work. `wb stream join` is the supported way to put a
repository into the stream that already holds it.

## State

Stream membership, roles, leases and every live link live in WB-owned state
under `$WB_HOME/streams/<name>/`, beside the Work Log. **No file inside a member
repository records stream membership**, so `git status` in every member stays
clean and the stream is readable after an interrupted session. Each stream also
carries an append-only, versioned, redacted event log at
`$WB_HOME/streams/<name>/events.jsonl`.

## Membership proposal

`start` names any transitive consumer of a member that is **not** in the stream,
because remote propagation bumps only stream members and would otherwise leave
it silently behind. The proposal is read from the dependency graph evidence
`wb deps graph` already wrote; when there is none, `start` says so and names the
command that produces it rather than guessing:

```sh
wb deps graph --fleet --format json
```
