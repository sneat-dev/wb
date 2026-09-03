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

### `wb stream sync <name>`

Brings the stream current and applies its batch — **without pushing**.

```sh
wb stream sync checkout-rewrite --verify
```

The order is the mechanism:

1. fetch, so the base is live rather than a session-start snapshot
2. rebase `stream/<name>` onto `origin/<base>` — **never a merge**
3. rebase every open agent branch onto the new stream head, **per branch**
4. **then** compare each library's required version against the target

Step 4 after step 2 is what makes sync **idempotent against Renovate**: a bump
Renovate already landed is in the tree after the rebase, so the required version
is already at target and no commit is written. Two syncs with nothing else
changed produce no new commits the second time.

Bumps are **local commits, one per library**, on the stream branch. A bump never
gets its own worktree, its own pull request or an agent. A conflict is reported
per agent branch — naming the branch, its claiming agent and the conflicting
paths — and never aborts the other branches.

Flags: `--verify`, `--library <name>@<version>` (repeatable), `--base`,
`--allow-mid-review`, `--push`, `--reason`, `--timeout`, `--format`.

#### `--verify`: one run, then a linear prefix scan

The whole batch is applied and the suite runs **once** over the result. On
failure WB reverts, then re-applies **cumulative** prefixes (1, 1+2, 1+2+3 …) on
a **local scratch branch that is never pushed**, stopping at the first failing
prefix and naming its last element the culprit, with the elements before it
listed as proven good.

The cost is honest: **one run when the batch passes, `1 + k` when the culprit is
element k**, worst case `1 + N`. It is a linear prefix scan, **not a bisection**.
If every prefix passes, the failure came from the base or a rebased change and
is reported as an **interaction failure** — no element is blamed.

A lockstep family (Angular, Nx, Ionic, Capacitor, `@sneat/*`) is **one** element
and is never split; a prefix carrying half of Angular cannot build by
construction and would blame the wrong element.

A mechanism the local run skips (`-race`, by design) is only ever printed as
"CI owns it" **after WB reads the member's stream-PR workflows and proves CI
runs it**. Anything neither side carries is reported as **UNGUARDED**.

#### `wb stream sync` refusals

| refusal | what fired | do this |
|---|---|---|
| `review-in-progress` | a branch under review would be rebased, invalidating the review that pinned its patch set | wait for the verdict, or `wb stream sync <name> --allow-mid-review` |
| `dirty-worktree` | sync rebases and commits, so it will not run over uncommitted work | commit or stash, then re-run |
| `unjustified-push` | `--push` without `--reason`, or a trigger WB does not recognise | `--push --reason "<text>"`, or use the verb that owns the trigger |

A **failed bump** fails the run (exit 1) and the worktree is **restored** to the
state sync found it in — a half-applied manifest would make the next sync refuse
as dirty and tell you to commit a bump whose lockfile never refreshed.

A version WB cannot compare is reported as `version-unreadable`, not as
"already at target": no commit is written either way, but only one of those is
a claim WB can support.

### Pushes are justified and counted

**`wb stream sync` does not push unless you give it a trigger.** A push costs
agent time, tokens, CI minutes and money, and ten bumps pushed one at a time
cost ten of each for one landing.

A push happens only on one of exactly **four triggers**:

| trigger | what does it |
|---|---|
| `landing` | `wb deps propagate remote` or the stream landing, after a green batch |
| `review` | the draft stream pull request being made ready |
| `park` | `wb worktree end`, a session park, any hand-off that would lose work |
| `explicit` | `--push --reason "<text>"` — the only escape hatch, reason **mandatory** |

A push with no recognised trigger is **refused**, listing all four. When a
trigger IS given, `wb stream sync --push --reason "<text>"` performs a **real**
push — `--force-with-lease` against the head WB recorded, then re-reading the
ref to prove the intended commit landed — and the event follows the real
outcome. A push that fails is reported as a failure, never as a success.

A sync with no trigger leaves the remote untouched **and says so**;
`wb stream status` shows the local commits accumulating.

### `wb stream absorb <agent-worktree>`

**There are no pull requests below the stream.** An agent branches from
`stream/<name>`, works, and its change reaches the stream branch through this
verb — a rebase and a squash, entirely local.

```sh
wb stream absorb /path/to/agent-worktree --verify
wb stream absorb /path/to/agent-worktree --title "feat(checkout): accept saved cards" --verify
```

Absorb **never pushes**, and never opens, updates or merges a pull request. The
only pull request per repository per stream is the draft stream pull request.
`wb worktree merge --route stream` is an alias of this verb.

The work is still reviewed and still lands as one reviewed commit; what
disappears is the pull request that carried it. So the review hangs on the
**content**: absorb refuses without a recorded `APPROVE` for exactly the
patch-identity set it is about to absorb — `git patch-id --stable` over the
commits the stream branch does not already carry.

- A **content-identical rebase carries the approval forward**: the SHAs move,
  the patch set does not. A reorder is not a content change either.
- **Any content change invalidates it** and needs a new round.
- **`APPROVE-WITH-FIXES` does not clear absorption.** The fixes it asks for
  change the content, so absorbing the unfixed set would land exactly the code
  the reviewer asked to change.
- The **newest** verdict for a patch set wins, so a later `REJECT` supersedes an
  earlier `APPROVE`.
- A **mechanical bump** — a diff touching only dependency manifests and
  lockfiles — skips the ledger, as it does at landing.

The squash message is **aggregated, never defaulted**: the title as subject, the
summary as body, one line per source commit (`<short-sha> <subject>`), and the
reviewed patch set. A squash that kept only a title would discard every message
the branch carried.

`--keep-commits <sha,...>` preserves commits instead of squashing. It **requires
`--reason`**, and **every kept commit must build on its own** — keeping commits
is only better than squashing if the history stays bisectable.

| refusal | do this |
|---|---|
| `unapproved-patch-set` | `wb review request <worktree>`, then `wb review record --worktree <path> --verdict APPROVE --round N` |
| `keep-without-reason` | add `--reason "<text>"` |
| `kept-commit-does-not-build` | fix the commit, or drop `--keep-commits` and squash |
| `nothing-to-absorb` | the stream already carries this work — `wb stream status` |
| `live-link` | `wb deps propagate local … --undo` first |

### `wb review record`

Records a verdict against the patch-identity set a worktree currently carries.

```sh
wb review record --worktree /path/to/agent-worktree --verdict APPROVE --round 1 --by reviewer
wb review record --worktree /path/to/agent-worktree --verdict REJECT --round 2 --note "races on the shared cache"
```

Flags: `--worktree` (required), `--verdict` (`APPROVE` | `APPROVE-WITH-FIXES` |
`REJECT`), `--round` (1 or greater), `--by`, `--note`, `--stream`, `--format`.

Verdicts are **appended** to the stream's event log — or to the fleet log for a
worktree outside every stream, because a review still has to be recorded
somewhere. Nothing is ever rewritten, so a later round never erases the one
before it.

`wb review request` (the reviewer's checkout side) is a separate verb and is not
in this skill yet.

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
