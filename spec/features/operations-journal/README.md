---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Operations Journal

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/operations-journal?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Append-only journal of every wb operation, bundle-backed preservation for unreachable commits, and a wb restore command that reads reports and journal records back into a branch.

## Problem

### There is no single record of what WB did

The founder's question on 2026-08-19 was direct: *"Do we have a log of wb
destructive (and non destructive) operations? We probably would be able to
restore branches from the log by ref hash."* The honest answer, measured the
same night, is no.

What exists instead is durable but scattered evidence, one directory per run,
with no index tying it together:

| Location | Measured | What it holds |
|---|---|---|
| `reports/worktree-cleanup/<UTC-timestamp>/cleanup.json` | **624 run directories** | Per-candidate decisions from `wb worktree cleanup` — task, repository, branch, `head_sha`, `remote_target_sha`, disposition, and whether it was applied. |
| `reports/worktree-cleanup/backlog/<64-hex>.json` | hundreds of files | Interrupted-operation resume records, named **only by a content hash** — nothing in the filename says repository, branch, or task. |
| `reports/worktree-cleanup/stage-archive/<task>-<hash>/` | — | Internal staging artifacts from a resumed cleanup. |
| `reports/branch-cleanup/<timestamp>/` | — | Written by `wb branch cleanup --apply` before its first destructive operation. |
| `worklogs/<effort-id>/` | **509 entries** | Per-effort agent-session recovery journals (see [Work Log Recovery](../work-log/README.md)) — prompts, claims, provenance. Not about Git mutations across the fleet. |
| `archive/` | **1 entry** | `superseded-patches`, created by hand tonight. |
| `backups/` | **1 entry** | `competios-all-refs-2026-08-12.bundle`, created by hand during a previous scare. |

Reconstructing "what did WB do to `sneat-co/competios` this week" means
walking some subset of those 624 timestamped directories, opening each
`cleanup.json`, and cross-referencing hash-named backlog files by hand. That
this is even possible is a testament to the individual report writers, not to
WB as a system: nothing aggregates them, nothing is greppable by repository or
branch, and nothing distinguishes "WB did this on purpose and it succeeded"
from "WB started this and the process died." `archive/` and `backups/` each
holding exactly **one** hand-made entry, next to 624 machine-written report
directories, is the measurement that the current discipline is manual — a
human remembered to capture something twice — and manual discipline is not a
log.

Tonight's own run — `wb branch cleanup --scope all --apply` deleting **777
branches** (416 local, 361 remote) — is itself evidence of the gap: it is
traceable today only because its own report directory happens to still exist
and because the operator was watching. Non-destructive operations — `wb
worktree create`, `wb sync`, `wb deps set`, a plan run without `--apply` — are
not recorded at all, so "what did WB do" cannot currently distinguish "nothing
happened" from "something happened and left no trace."

### A recorded SHA is not a durable restore path

Every cleanup report already carries `head_sha`, so in principle
`git branch <name> <sha>` restores a deleted branch — **but only while the
object still exists**. A branch that loses its only ref becomes unreachable,
and `git gc` prunes unreachable objects (roughly a two-week default horizon).
After that, the report tells a reader exactly what was lost and gives them no
way back — the log becomes a record of failure rather than a recovery path.

Tonight's 777 deletions are safe precisely because every one of them was
disposition `contained`: every commit is already an ancestor of the fetched
`origin/main`, so the commits stay reachable through that ref even after the
branch ref that used to point at them is gone. The gap is real for the classes
[branch-hygiene#req:evidence-class-taxonomy](../branch-hygiene/README.md)
deliberately does **not** delete: **353 `absorbed`** and **726 `unique`**
branches in the current fleet. Their commits are not proven reachable from any
surviving ref, so a recorded SHA alone is not a promise WB can keep for them.
[Preservation and Pre-Flight](../cleanup-orchestration/cleanup-preconditions/README.md)
already reached the same conclusion independently and requires a Git bundle —
not a patch, not a SHA — for exactly this reason
(`cleanup-preconditions#req:preservation-content`, item 4): a patch cannot
preserve the commits of a branch that is about to lose its only ref.

### Reports carry everything a restore needs; nothing reads them back

`cleanup.json`, the branch-cleanup report, and the preservation manifest
defined by `cleanup-preconditions#req:manifest-carries-its-own-restore`
between them already carry repository, branch, exact SHA, and — for a
preserved branch — the literal restore command. Recovery today means a human
opening the right JSON file, copying a SHA or a bundle path, and typing the
`git` commands by hand. Nothing in WB reads a report or a manifest back into a
live branch.

## Interaction with Other Features

### Why a sibling of Cleanup Orchestration, not a fifth child

[Cleanup Orchestration](../cleanup-orchestration/README.md) is a parent of
four children — `wb cleanup`, `wb unpushed`, `wb audit`, `wb recover` —
because those four share one output contract, one unit-partitioning rule, one
concurrency model, and one preservation gate, and its own "why one parent"
section is explicit that the thing which justifies the grouping is a **shared
evidence model at a bounded scope**: the worktree/branch retirement lifecycle
of one repository at a time.

This feature's scope is wider than that lifecycle in the dimension that
matters for a parent/child decision: the journal MUST record `wb worktree
create`, `wb sync`, `wb deps set`, `wb migrate`, and every other
state-changing WB command, not only the four cleanup-family verbs. Nesting it
under `cleanup-orchestration` would misstate its scope — a reader would
reasonably expect a child of that parent to be about retiring worktrees and
branches, and this is about every operation WB performs, destructive or not.
It is therefore a **sibling** of `cleanup-orchestration`,
`worktree-lifecycle`, and `branch-hygiene`, at the same level in
`spec/features/`.

Within this feature, the journal (an index over every operation) and `wb
restore` (a command that reads that index and reports back) are **one
document, not two children**, for the same reason
[cleanup-preconditions](../cleanup-orchestration/cleanup-preconditions/README.md)
folded preservation and stacked-pull-request pre-flight into one document
rather than two: they share exactly one evidence model — the journal record
**is** `wb restore`'s input — so splitting them would only guarantee the two
halves drift out of sync with each other.

### Composes with cleanup-preconditions; does not re-specify it

Bundle-backed preservation for the worktree/branch retirement lifecycle is
**already fully specified** by
[cleanup-preconditions](../cleanup-orchestration/cleanup-preconditions/README.md):
what gets captured
(`cleanup-preconditions#req:preservation-content`), where it lives and how
long it survives
(`cleanup-preconditions#req:preservation-location-and-retention`), how it is
verified before it counts
(`cleanup-preconditions#req:preservation-is-verified-before-it-counts`), and
that its manifest already carries its own restore command
(`cleanup-preconditions#req:manifest-carries-its-own-restore`). That feature's
own Open Questions section asks the exact question this feature answers:
*"Should a future `wb restore <run-id>` command drive the manifest's recorded
commands, or does adding a restore verb create a new destructive surface that
itself needs these gates?"* This feature's answer is: **yes to both** — `wb
restore` drives the manifest, and because it creates or overwrites a ref it is
itself a destructive-adjacent surface, gated by its own refusal rules in
`#req:restore-refuses-to-clobber-a-diverged-branch` rather than by
cleanup-preconditions' gates, which are about the moment before *deletion*,
not creation.

This feature therefore does not restate bundle creation, verification, or
manifest content. It adds only what cleanup-preconditions does not cover:
a fleet-wide, cross-repository **index** so a bundle or a manifest can be
*found* without knowing its run ID in advance
(`#req:journal-is-an-index-not-a-replacement`), a naming rule that replaces
the `<64-hex>.json` anti-pattern
(`#req:preserved-artifacts-are-self-describing-by-name`), and the read path
that turns a verified manifest back into a live branch (`#req:wb-restore`).

### Composes with Work Log Recovery; answers a different question

[Work Log Recovery](../work-log/README.md) already defines an append-only,
crash-consistent journal — `events.jsonl` plus a derived `projection.json`,
sealed on cleanup into `<WB_HOME>/worklogs/<effort-id>/` and later archived to
`<WB_HOME>/worklogs/archive/<YYYY>/<MM>/<effort-id>/`. This feature's
Operations Journal reuses that crash-safety idiom (append-only JSONL, atomic
projection rewrite, fsync on high-value events, recovery that discards only a
torn final line — `work-log#req:append-only-crash-consistent-events`) rather
than inventing a second one, but it is not the same journal wearing a new
name:

| | Work Log | Operations Journal |
|---|---|---|
| Scope | one effort, one worktree | every repository, every task, the whole fleet |
| Subject | prompts, claims, agent provenance, session recovery | Git-state-changing WB operations: refs, branches, worktrees, stashes, pull requests, preservation |
| Lives | inside the worktree, then `<WB_HOME>/worklogs/<effort-id>/` | `<WB_HOME>/journal/`, never inside a worktree or a canonical clone |
| Answers | "what directed this effort, and can a successor resume it?" | "what did WB do to this repository or branch, and can it be undone?" |

A single operation MAY reference both: a `wb cleanup --apply` run performed
under an active Work Log claim carries that claim's effort ID in its journal
record (`#req:journal-record-fields`), so the two can be cross-referenced, but
neither reads or writes the other's storage.

### Composes with Fleet Audit and the existing report writers

[Fleet Audit](../cleanup-orchestration/fleet-audit/README.md) already defines
`wb audit --scope preserved` to report each preservation run's root, age,
artifact count, and total size
(`cleanup-preconditions#req:preservation-location-and-retention`). This
feature extends that surface with `--scope journal` rather than defining a
competing report command (`#req:journal-audit-scope`).

`internal/worktrees/lifecycle.go` already writes `cleanup.json` durably —
temporary file, `fsync`, atomic rename — via `writeCleanupReport`, at the path
`DefaultCleanupReportDir` computes below `<wb-home>/reports/worktree-cleanup/`.
This feature does not change that writer or that path. The journal is a
second, much smaller record written **alongside** each such report — never a
replacement for it — that exists purely so the report can be *found*
(`#req:journal-is-an-index-not-a-replacement`). The 624 existing report
directories and hundreds of hash-named backlog files that predate this
feature are handled by `#req:journal-backfill`, not by rewriting the report
writer.

## Behavior

### Operations Journal

#### REQ: journal-scope-is-operations-not-invocations

The journal MUST record one entry per **operation** — a WB invocation that
performs, attempts, or refuses at least one mutating action against a
repository, worktree, branch, ref, stash, pull request, or preservation
store. It MUST record destructive and non-destructive operations alike,
exactly as the founder asked for both.

A pure read/report command — `wb audit`, `wb worktree list`, `wb branch
list`, `wb worktree log show`, a plan run without `--apply` that performs no
mutation — produces no journal entry, because it changed nothing there is
evidence for. This is a scope boundary, not an oversight:
`#req:journal-record-fields` requires a before/after SHA pair and an outcome,
neither of which a read-only command has anything to report. Whether a
lightweight "observed" event class should exist for read commands too is
recorded in Open Questions rather than decided here, because it is a disk-cost
trade-off the founder should make with real numbers in front of them.

The closed set of operation kinds, extensible only by amending this
requirement, is: `worktree_create`, `worktree_rename`, `worktree_abort`,
`worktree_recycle`, `worktree_remove`, `branch_delete_local`,
`branch_delete_remote`, `ref_update`, `stash_drop`, `pr_retarget`, `pr_merge`,
`preservation_capture`, `bundle_create`, `restore_apply`, `sync_apply`
(covers `wb sync`, `wb deps set`, `wb migrate`), and `other` for a mutating
action this list has not yet named. `other` MUST still carry every field
`#req:journal-record-fields` requires — an unclassified operation is
recorded, never silently invisible, and a build that emits `other` more than
incidentally is a signal this list needs an amendment.

#### REQ: journal-record-fields

Every journal record MUST carry: `schema_version`; a monotonic per-file
`sequence`; `recorded_at` (UTC, RFC 3339); `operation` (the closed set from
`#req:journal-scope-is-operations-not-invocations`); `destructive` (bool);
`repository` (`owner/name`); `branch` (when applicable); `worktree_dir` (when
applicable); `task` / effort ID (when the operation ran under a WB Work Log
claim, cross-referencing `work-log#req:deterministic-identities-and-evidence`
without duplicating its content); `before_sha` and `after_sha` (either MAY be
empty — a `worktree_create` has no `before_sha`, a refused deletion has no
`after_sha`); `outcome`, drawn from the closed set `succeeded`, `refused`,
`failed`, `partial`; `evidence_path`, a pointer to the full durable report or
preservation manifest this operation produced (never omitted when one
exists); `run_id` and `command` (the exact invoked WB command line, flags
included, secrets excluded); and `actor` (`human` or `agent`, from the same
provenance WB already collects for a Work Log Run).

This is normative for the same reason
`cleanup-orchestration#req:every-row-carries-evidence-and-remedy`'s sibling
requirement is: a record that identifies *that* something happened without
saying *what*, *to what*, *from what state*, *to what state*, and *where the
detail lives* is not an audit trail, it is a heartbeat.

#### REQ: journal-is-an-index-not-a-replacement

A journal record MUST NOT inline the full per-candidate detail a durable
report already carries — no embedded diff, no per-file manifest entry, no
list of every branch a multi-branch run touched inside one record. Each
record describes exactly one operation on exactly one subject and points, via
`evidence_path`, to the report or manifest that carries the rest. A
multi-branch `wb cleanup --apply` run therefore produces one journal record
per branch retired, each pointing at the same `cleanup.json`, not one record
for the whole run.

This keeps every record small and bounds its size
(`#req:concurrent-appends-stay-uncorrupted`), and it is what makes the
journal the answer to "what did WB do to `sneat-co/competios` this week"
without reproducing the 107,690-byte-report problem
`cleanup-orchestration#req:grouped-end-of-run-summary` already exists to
avoid.

#### REQ: one-global-append-only-journal

WB MUST maintain **one global journal**, not one file per repository. It MUST
be stored as newline-delimited JSON (ndjson) — one record per line — rotated
monthly at `<wb-home>/journal/operations-<YYYY-MM>.jsonl`, where `<YYYY-MM>`
is the UTC year and month in which the record was appended.

Both halves of this decision are deliberate:

- **Global over per-repository.** The founder's own question was framed
  per-repository ("what did wb do to `sneat-co/competios` this week"), which
  might suggest a file per repository. A global file is chosen instead
  because the fleet holds 397 repositories and a 500-per-hour agent-lane
  operation rate would otherwise mean 397 growing files to rotate, retain,
  and — critically — to *lock* independently across concurrent processes for
  no benefit: `#req:journal-query-command` answers the per-repository
  question by filtering one small file, not by opening one of 397. A global
  file also answers the fleet-wide question — "what did WB do anywhere this
  week" — that a per-repository layout cannot answer without opening all of
  them, and that question is exactly what this feature was commissioned to
  answer for the 777-branch run.
- **ndjson over a directory of files.** The existing report writers already
  use one directory and one file per run (`cleanup.json`) and per-record
  (`backlog/<64-hex>.json`); that is precisely the pattern that produced 624
  directories with no index. A directory of small per-operation files
  multiplies that problem rather than fixing it. ndjson is streamable,
  greppable with plain `grep`/`jq` without a directory walk, appends in a
  single write, and is the same format Work Log Recovery's `events.jsonl`
  already uses successfully in this repository.

Monthly rotation bounds any one file's size without requiring rotation logic
to run inside every WB invocation that appends — the file name is a pure
function of the current UTC month, so no process needs to coordinate a
rollover.

#### REQ: concurrent-appends-stay-uncorrupted

Multiple `wb` processes append to the same monthly journal file
concurrently — this is the ordinary case, not an edge case, on a workstation
running several agent lanes at once. WB MUST guarantee that concurrent
appends never interleave partial lines and never lose a record other than as
`#req:journal-write-is-best-effort` allows. It MUST do so by:

1. Serializing each record to one JSON line with a hard cap (8 KiB); an
   `evidence_path` pointer keeps every legitimate record far under that cap,
   per `#req:journal-is-an-index-not-a-replacement`. A record that cannot fit
   MUST be truncated to its mandatory fields plus `evidence_path` rather than
   dropped.
2. Opening the file `O_APPEND` and writing each record in one `write(2)`
   call, relying on the POSIX guarantee that a single `write` below the
   platform's atomic-write limit (conventionally `PIPE_BUF`, at least 4 KiB)
   never interleaves with a concurrent writer's `write` to the same file.
3. Additionally taking a short-held advisory lock (`flock` on POSIX,
   `LockFileEx` on Windows) around each append as defense in depth, because
   `O_APPEND` atomicity is not guaranteed on every filesystem WB might run
   against (network filesystems in particular), and because a small,
   bounded, per-record lock costs microseconds against operations that
   already cost hundreds of milliseconds in `git` subprocess time — it is not
   the fleet's bottleneck.

A record MUST be fully written — a complete, valid JSON line terminated by
`\n` — or not written at all from the reader's point of view; a reader MUST
treat a non-JSON or incomplete final line as evidence of an interrupted
append, and discard only that trailing line, exactly as
`work-log#req:append-only-crash-consistent-events` already specifies for
`events.jsonl`.

#### REQ: journal-write-is-best-effort-and-never-blocks-the-operation

Preservation is a no-opt-out gate
(`cleanup-preconditions#req:preservation-has-no-opt-out`) because losing a
preservation artifact loses the only recoverable copy of something
irreplaceable. The journal is different in kind: it is an **index**, and
every fact it records also exists in the durable report or manifest
`evidence_path` points at. Losing one journal write therefore degrades
discoverability, not safety.

Consequently the journal write MUST NOT be a precondition for any operation
to proceed, and a failure to append — journal directory unwritable, disk
full, lock timeout — MUST NOT fail, block, or roll back the underlying
operation. WB MUST emit one stderr warning naming the failure and the
operation it could not index, and the operation MUST proceed exactly as it
would have if the journal did not exist. This is the opposite default from
preservation, and deliberately so, for the reason stated above.

#### REQ: journal-append-ordering-around-the-destructive-act

For a destructive operation, WB MUST append a `succeeded`, `refused`, or
`failed` record only after the operation has reached that terminal state, so
`after_sha` and `outcome` are accurate. This does not weaken the existing
happens-before rule that durable *preservation* evidence is written and
verified before any deletion
(`cleanup-preconditions#req:preservation-is-verified-before-it-counts`,
`worktree-lifecycle#req:recheck-and-compare-delete`) — that rule is about the
preservation artifact, which this feature's journal record only points to via
`evidence_path`, and it is unchanged by this feature.

A process that dies between the destructive act and the journal append
therefore leaves no journal record for that operation. `#req:journal-backfill`
exists precisely to close that gap after the fact, from the durable report
the operation already wrote before this feature existed to index it — the
report, not the journal, remains the fleet's evidence of record.

#### REQ: journal-backfill

WB MUST provide `wb journal reindex`, which walks
`<wb-home>/reports/worktree-cleanup/`, `<wb-home>/reports/branch-cleanup/`,
`<wb-home>/preserved/`, and `<wb-home>/worklogs/`, and appends one journal
record per operation it can reconstruct from those existing durable
artifacts, for any operation not already represented in the journal. It MUST
be idempotent — reindexing twice MUST NOT duplicate a record — keyed on a
deterministic record ID derived from the source report path plus subject
identity.

This is required, not optional, because this feature ships into a fleet that
already holds 624 report directories, hundreds of hash-named backlog files,
and 509 Work Log entries with no index. A journal that only starts recording
from the day it ships leaves that entire history exactly as undiscoverable as
it is today.

#### REQ: journal-query-command

WB MUST expose `wb journal show`, read-only, with:

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--repository` | string | none | exact `owner/name` filter |
| `--branch` | string | none | exact branch-name filter |
| `--task` | string | none | effort-ID filter |
| `--operation` | string list | all | restrict to the named operation kinds |
| `--since` | duration | `168h` (one week) | only records at or after now minus this |
| `--destructive-only` | bool | `false` | restrict to `destructive: true` records |
| `--format` | string | `text` | `text`, `json`, or `ndjson` |

`wb journal show` MUST be read-only, MUST NOT create, rotate, or delete a
journal file, and obeys the same output-contract rules
`cleanup-orchestration#req:stdout-is-the-report-only` and
`cleanup-orchestration#req:documented-exit-codes` already state for the
sibling commands, adopted here by reference rather than restated. It MUST
read every monthly file its `--since` window touches, in chronological order,
and MUST tolerate and skip a torn trailing line in the current month's file
per `#req:concurrent-appends-stay-uncorrupted` without failing the whole
query.

This is the command that answers the founder's question directly: `wb
journal show --repository sneat-co/competios --since 168h` MUST answer "what
did WB do to this repository this week" without opening any report
directory, in one call.

#### REQ: journal-audit-scope

`wb audit --scope journal` MUST report each monthly journal file's path, age,
record count, and byte size, using the same reporting shape
`cleanup-preconditions#req:preservation-location-and-retention` already
defines for `--scope preserved`, so a human deciding what to compact or
archive has evidence rather than a guess.

#### REQ: journal-retention-is-a-human-decision

WB MUST NOT delete a journal file itself, under any flag, in any command —
the same rule `cleanup-preconditions#req:preservation-location-and-retention`
already states for preservation artifacts, and for the same reason: a log
that can prune itself is a log an operator cannot trust when it matters most.
WB MUST warn on stderr when the total journal directory exceeds a configurable
size (default 500 MiB — journal records are small, bounded text, not the
multi-gigabyte preservation payloads the 5 GiB preservation threshold guards
against), naming the directory, its size, and that compaction or archival is a
manual decision. The exact default threshold and any automatic archival
policy are recorded in Open Questions.

### Bundle-Backed Preservation

This section extends, and does not restate,
[cleanup-preconditions](../cleanup-orchestration/cleanup-preconditions/README.md),
which already normatively requires a Git bundle for every branch a cleanup
run is about to delete
(`cleanup-preconditions#req:preservation-content`, item 4), verified before
it counts (`cleanup-preconditions#req:preservation-is-verified-before-it-counts`),
with its own restore command recorded in its manifest
(`cleanup-preconditions#req:manifest-carries-its-own-restore`). Everything
below is additional.

#### REQ: reachability-determines-what-restore-can-promise

The distinction the founder asked for — a bundle required versus a recorded
SHA sufficient — is a property of **reachability**, and this feature states
it explicitly because `wb restore` (`#req:wb-restore`) must act on it even for
evidence written before this feature existed:

- A `contained` branch (`branch-hygiene#req:evidence-class-taxonomy`) has
  every commit already an ancestor of the fetched exact target. Those commits
  remain reachable through the target ref after the branch's own ref is
  deleted, so **a recorded `before_sha` is sufficient**; a bundle is
  redundant but never wrong to have.
- An `absorbed` or `unique` branch's commits are not proven reachable from
  any surviving ref. For those, **only a verified Git bundle — or the branch
  ref itself, still intact — is a restore path**; a recorded SHA alone is a
  promise WB cannot keep once garbage collection runs.

Because `cleanup-preconditions#req:preservation-content` already bundles
every branch a cleanup run deletes regardless of class, every operation this
feature's journal indexes from that path already satisfies the stronger
requirement. This rule matters at the boundary that requirement does not
cover: history that predates it (`#req:journal-backfill`, where only a
`head_sha` was ever recorded) and any future destructive surface that deletes
a ref without routing through cleanup-preconditions' gates. `wb restore`
(`#req:restore-verifies-before-promising`) MUST check reachability itself
rather than trust a record's age or origin.

#### REQ: preserved-artifacts-are-self-describing-by-name

`reports/worktree-cleanup/backlog/<64-hex>.json`, named only by a content
hash, is a documented anti-pattern: nothing about the filename says what
repository, branch, or task it concerns, so identifying it requires opening
it. No artifact this feature governs may repeat that pattern.

Preservation artifacts already live under a self-describing directory path,
`<run-id>/<owner>/<repository>/`
(`cleanup-preconditions#req:preservation-location-and-retention`). This
feature additionally requires that within that directory, a Git bundle file
name itself carry the branch: `<branch-slug>-<short-sha>.bundle`, where
`<branch-slug>` is the branch name with `/` replaced by `--` and any
character outside `[A-Za-z0-9._-]` percent-encoded. A directory listing alone
— without opening the manifest — MUST identify which branch each bundle
holds. `wb journal reindex` (`#req:journal-backfill`) MUST NOT retroactively
rename existing hash-named files; it indexes them by reading their content
once, and the naming rule binds new artifacts going forward.

### wb restore

#### REQ: wb-restore

WB MUST expose `wb restore` as a top-level command that recreates a branch —
local, remote, or both — from a journal record, a durable report, or a
preservation manifest.

| Flag | Type | Default | Meaning |
|---|---|---|---|
| `--repository` | string | required | exact `owner/name` |
| `--branch` | string | required unless `--from-bundle` names one | branch to restore |
| `--from-bundle` | string | none | explicit bundle path, overriding discovery |
| `--from-sha` | string | none | explicit SHA, overriding discovery |
| `--as` | string | `--branch` value | name the restored branch, when it must differ from the original to avoid collision |
| `--local` / `--remote` | bool | `--local` only | which ref(s) to recreate |
| `--apply` | bool | `false` | perform the restore; without it the run is a plan |
| `--format` | string | `text` | `text` or `json` |

Given only `--repository` and `--branch`, WB MUST resolve the most recent
matching journal record itself (`#req:journal-record-fields`), following its
`evidence_path` to the report or manifest, rather than requiring the caller
to already know a run ID — the discovery flow
`#req:restore-discoverability` names is the default path, not a fallback.

#### REQ: restore-dry-run-default

`wb restore` MUST be a plan unless `--apply` is explicit, following the same
convention `cleanup-orchestration#req:dry-run-default` states for its
sibling commands (adopted here as a design choice, not inherited by document
structure — see `#req:wb-restore`'s note on why this feature is not a child).
A plan MUST perform every read and verification step — locating the record,
checking reachability, checking for a collision — and state exactly what
`--apply` would do, without creating or touching any ref.

#### REQ: restore-verifies-before-promising

Before reporting a branch as restorable, in a plan or under `--apply`, WB
MUST verify the object actually exists and is usable:

- For a bundle source: run `git bundle verify` and confirm it lists the
  expected tip SHA, exactly as
  `cleanup-preconditions#req:preservation-is-verified-before-it-counts`
  already requires at capture time — this is the same check, re-run at
  restore time, because a bundle's presence on disk is not proof it is still
  intact.
- For a SHA-only source (no bundle recorded or found): confirm the object is
  present and reachable from some existing ref in the target repository
  (`git cat-file -e <sha>` plus a reachability check against every local and
  remote ref), per `#req:reachability-determines-what-restore-can-promise`.

If verification fails, WB MUST refuse and state plainly that the branch
cannot be restored and why — object missing, bundle corrupt, or unreachable
and unbundled — rather than attempting a partial restore or promising
something it cannot deliver. A false claim of recoverability is worse than an
honest refusal, because it is discovered only when it is too late to matter.

#### REQ: restore-refuses-to-clobber-a-diverged-branch

If the target branch name already exists — locally for `--local`, on
`origin` for `--remote` — at a SHA different from the one being restored, WB
MUST refuse rather than overwrite it, in a plan and under `--apply` alike. No
flag may force an overwrite. The remedy WB MUST name is `--as <new-name>`, to
restore the same content under a name that does not collide, or an explicit
instruction to remove or rename the existing branch first. This mirrors the
compare-and-delete discipline
`worktree-lifecycle#req:recheck-and-compare-delete` and
`branch-hygiene#req:compare-and-delete` already apply to deletion, applied in
reverse to creation: WB MUST NOT mutate a ref out from under state it did not
expect to find there.

A target branch that already exists at the **same** SHA is a no-op, reported
as already-restored, not a refusal.

#### REQ: restore-local-and-remote-are-independent

`--local` and `--remote` MAY be combined; each MUST be verified and applied
independently, and a refusal on one (for example, the remote name is taken by
different content) MUST NOT block the other when it is clean. Restoring
`--remote` MUST use a compare-and-create push (`git push origin
<sha>:refs/heads/<name>`, refusing if the ref already exists with different
content) rather than a bare force-push, so a concurrent legitimate push to
that name cannot be silently clobbered.

#### REQ: restore-discoverability

Running `wb restore --repository <owner>/<name>` with no `--branch` MUST list
every candidate the journal and preservation store know about for that
repository — recently retired branches, their disposition, whether a bundle
or only a SHA backs them, and their age — so a user does not need to already
know what to restore before asking. This is the same discoverability
principle `cleanup-orchestration#req:every-warning-names-a-remedy` states for
refusals, applied to recovery: a restore command that only works once you
already know the exact record to cite is not a recovery path for the common
case of "I think I deleted something, what can I get back."

`wb audit --scope preserved` and `wb journal show --destructive-only` remain
the two entry points into this discovery; `wb restore` without a branch is a
third, scoped to one repository and phrased as an answer to "what can I
restore here" rather than "what did WB do here."

#### REQ: restore-bulk-mode-inherits-liveness-and-incrementality

`wb restore --run-id <id> --apply`, restoring every branch one preservation
run captured in a single invocation, is the long-running case this feature
defines. It MUST inherit
`cleanup-orchestration#req:progress-liveness` and
`cleanup-orchestration#req:incremental-findings` unchanged: a liveness event
per branch as it is verified, flushed as it happens, and each branch's
restore outcome emitted as soon as that branch completes rather than held
until the whole run ends. These are referenced, not restated, because
[Cleanup Orchestration](../cleanup-orchestration/README.md) already states
why a fleet-scale WB command that buffers its output until the end is a
defect, and repeating the argument here would only invite the two documents
to drift.

#### REQ: restore-non-goals

`wb restore` MUST NOT reopen or retarget a pull request — that is
[Pull Request Recovery Forensics](../cleanup-orchestration/pr-recovery/README.md)'
concern, not this feature's, and a restored branch with a closed pull request
based on it is reported as such, not acted on. `wb restore` MUST NOT restore
a worktree or a Work Log claim; it recreates a ref, and re-attaching a
worktree to it is `wb worktree create --resume` or a manual checkout, not a
restore-command responsibility. `wb restore` MUST NOT run against a repository
whose exact target could not be freshly fetched — the same fail-closed
default `cleanup-orchestration#req:fetch-before-every-decision` requires for
deletion applies here to avoid restoring against a stale view of what already
exists.

## Acceptance Criteria

### AC: every-operation-produces-one-small-indexed-record

**Requirements:** operations-journal#req:journal-scope-is-operations-not-invocations, operations-journal#req:journal-record-fields, operations-journal#req:journal-is-an-index-not-a-replacement

Given a fixture repository and worktree, when `wb worktree create`, `wb sync`,
`wb cleanup --apply` retiring three branches in one unit, and `wb audit`
(read-only) each run, then `wb worktree create` and `wb sync` each produce
exactly one journal record with `destructive: false`; the `wb cleanup --apply`
run produces exactly three journal records, one per branch, each with
`destructive: true`, its own `before_sha`/`after_sha`/`outcome`, and an
`evidence_path` pointing at the one `cleanup.json` all three share, with no
record inlining another branch's SHA or diff; `wb audit` produces no journal
record at all; and every record produced carries `schema_version`,
`sequence`, `recorded_at`, `operation`, `repository`, `run_id`, `command`,
and `actor`, with `task` populated when the operation ran under a Work Log
claim and empty otherwise. A record for a refused branch (`--stages
local-branch,remote-branch` on a checked-out branch) MUST carry
`outcome: refused` and an empty `after_sha`.

### AC: concurrent-and-interrupted-writers-never-corrupt-the-journal

**Requirements:** operations-journal#req:one-global-append-only-journal, operations-journal#req:concurrent-appends-stay-uncorrupted, operations-journal#req:journal-write-is-best-effort-and-never-blocks-the-operation, operations-journal#req:journal-append-ordering-around-the-destructive-act

Given twenty concurrent WB processes across different repositories each
appending one operation record at the same moment, when all twenty complete,
then the current month's `operations-<YYYY-MM>.jsonl` contains exactly twenty
well-formed JSON lines, each parses independently, none is interleaved with
another, and a byte-level diff confirms no line was partially overwritten.
Given a process that is `SIGKILL`ed after completing a destructive operation
but before its journal append flushes, when the journal file is read, then
either the record is absent or the trailing line is a torn fragment that a
reader discards without failing the read of every prior line — and `wb
journal reindex` (`#req:journal-backfill`) subsequently recovers that
operation from its durable report. Given the journal directory made
read-only so every append fails, when `wb cleanup --apply` runs against a
fixture with eligible branches, then every eligible branch is still retired
exactly as it would be with a writable journal, one stderr warning per failed
append names the operation it could not index, and the run's exit code and
durable report are unaffected by the journal failure.

### AC: the-founders-question-is-answerable-in-one-command

**Requirements:** operations-journal#req:journal-query-command, operations-journal#req:journal-audit-scope, operations-journal#req:journal-retention-is-a-human-decision

Given a journal spanning three months and covering ten repositories with at
least one operation each, when `wb journal show --repository
sneat-co/competios --since 168h` runs, then stdout contains every operation
recorded against that repository within the last week and no operation
against any other repository, in chronological order, without opening any
report directory. When `--format json` is given, stdout parses as a single
document obeying `cleanup-orchestration#req:stdout-is-the-report-only`.
Given `wb audit --scope journal` runs, then it reports each monthly file's
path, age, record count, and byte size. Given the journal directory made to
exceed the configured size threshold, when any journal-writing command runs,
then a stderr warning names the directory, its size, and that removal is a
manual decision; and a test asserts no WB command ever deletes a journal
file, in any flag combination, across every command in this feature and in
`cleanup-orchestration`.

### AC: pre-existing-history-is-backfilled-without-duplication

**Requirements:** operations-journal#req:journal-backfill

Given a fixture `<wb-home>` containing fifty pre-existing
`reports/worktree-cleanup/<timestamp>/cleanup.json` directories, a dozen
hash-named `reports/worktree-cleanup/backlog/<hash>.json` files, and no
journal, when `wb journal reindex` runs, then the journal gains one record
per operation reconstructable from those reports, each with `evidence_path`
pointing at its source report; `wb journal show --repository <owner>/<repo>`
then answers correctly for that pre-existing history. Running `wb journal
reindex` a second time with no new reports added produces zero additional
records, proving idempotency, and a record's deterministic ID is confirmed
identical across both runs.

### AC: restorability-follows-reachability-and-artifacts-are-self-naming

**Requirements:** operations-journal#req:reachability-determines-what-restore-can-promise, operations-journal#req:preserved-artifacts-are-self-describing-by-name

Given a `contained` branch and an `absorbed` branch, both retired by `wb
cleanup --apply` per `cleanup-preconditions#req:preservation-content`, when
both branches' object availability is checked after the target's history has
been repacked and the branch refs deleted, then the `contained` branch's
commits remain reachable from the target with no bundle required, while the
`absorbed` branch's original commits are confirmed to depend on its captured
bundle for recoverability. A directory listing of the preservation run's
`<owner>/<repository>/` directory, with no manifest opened, identifies each
bundle's branch from its filename alone (`<branch-slug>-<short-sha>.bundle`);
a branch name containing `/` and characters outside `[A-Za-z0-9._-]` produces
a filename with `/` replaced by `--` and the other characters
percent-encoded, and the manifest's recorded path for that entry matches the
file actually on disk byte for byte.

### AC: wb-restore-recreates-a-branch-and-never-clobbers

**Requirements:** operations-journal#req:wb-restore, operations-journal#req:restore-dry-run-default, operations-journal#req:restore-verifies-before-promising, operations-journal#req:restore-refuses-to-clobber-a-diverged-branch, operations-journal#req:restore-local-and-remote-are-independent

Given a repository from which branch `feature-x` was deleted locally and on
`origin` by a `wb cleanup --apply` run that captured a verified bundle, when
`wb restore --repository <owner>/<repo> --branch feature-x --local --remote`
runs without `--apply`, then the plan resolves the branch via its journal
record without any other flag supplied, states it will recreate both refs at
the bundled tip SHA, and creates nothing. When `--apply` then runs, then
`git bundle verify` is executed and passes before either ref is created, both
the local and remote `feature-x` exist at the exact original SHA with an
identical tree, and re-running the same command reports the branch already
restored rather than erroring or duplicating work. Given the bundle file is
corrupted before a second such restore, when `wb restore --apply` runs for a
different branch backed only by a recorded SHA that is no longer reachable
from any ref, then both are refused, each stating plainly that the branch
cannot be restored and why, and no ref is created for either. Given a branch
`feature-y` already exists locally at a different SHA than the one being
restored, when `wb restore --branch feature-y --apply` runs, then it refuses,
names `--as <new-name>` as the remedy, no flag overrides the refusal, and
`wb restore --branch feature-y --as feature-y-restored --apply` then succeeds
without touching the existing `feature-y`. Given the remote name is taken by
different content while the local name is free, when `wb restore --local
--remote --apply` runs, then the local restore succeeds and the remote
restore is refused independently, and the exit code reflects the partial
outcome.

### AC: a-user-can-discover-what-is-restorable

**Requirements:** operations-journal#req:restore-discoverability

Given a repository with three branches retired over the past month at
different dispositions, when `wb restore --repository <owner>/<repo>` runs
with no `--branch`, then stdout lists all three candidates with their
disposition, age, and whether a bundle or only a SHA backs each, and creates
nothing. The same information is confirmed reachable independently via `wb
audit --scope preserved` and `wb journal show --destructive-only --repository
<owner>/<repo>`, so a test can assert all three discovery paths agree on the
same candidate set.

### AC: bulk-restore-streams-progress-and-respects-non-goals

**Requirements:** operations-journal#req:restore-bulk-mode-inherits-liveness-and-incrementality, operations-journal#req:restore-non-goals

Given a preservation run that captured twelve branches, each restore taking a
measurable, unequal interval to verify, when `wb restore --run-id <id>
--apply` runs with stdout and stderr captured and timestamped, then a
liveness event appears on stderr for each branch as its verification begins,
flushed at that moment rather than buffered; each branch's restore outcome
appears no later than shortly after that branch completes rather than held
until the run ends; and killing the run after five branches have completed
leaves stdout already containing all five outcomes. Given one of the twelve
branches has a pull request that was closed when its base was deleted, when
the restore for that branch completes, then the pull request is reported as
closed and unaffected, and no call is made to reopen or retarget it. Given
the repository's exact target cannot be freshly fetched, when `wb restore
--apply` runs for any branch in that repository, then it refuses for every
branch in that repository and states the fetch failure as the reason.

## Open Questions

- **Retention policy for the journal itself.** `#req:journal-retention-is-a-human-decision`
  states WB must never delete a journal file and picks a 500 MiB warn
  threshold as a starting default. Is 500 MiB the right number, should it
  scale with fleet size (397 repositories today), and should WB offer an
  opt-in automatic move-to-archive step analogous to `wb worktree log
  archive`'s seven-day window, rather than warn-only forever? This is a
  founder call, not an engineering one.
- **Disk budget for bundle-backed preservation at fleet scale.**
  `cleanup-preconditions#req:preservation-content` bundles every branch a
  cleanup run deletes, unconditionally, regardless of reachability class.
  `#req:reachability-determines-what-restore-can-promise` observes that a
  `contained` branch's bundle is fully redundant with the target's own
  history. At fleet scale — roughly 2,211 remote branches measured
  2026-08-19 — should `cleanup-preconditions` be amended to bundle only
  `absorbed` and `unique` branches, recording a SHA-only reference for
  `contained` ones, to cut preservation disk cost with no loss of actual
  recoverability? This feature deliberately does not decide it: it would
  amend a merged, in-review sibling specification's normative requirement,
  and the trade-off (disk cost versus the small residual risk of the target
  branch itself being force-pushed or rewritten before the branch is
  restored) is the founder's to weigh, not this feature's to assume.
- **Should a lightweight "observed" event class exist for read-only
  commands?** `#req:journal-scope-is-operations-not-invocations` deliberately
  excludes `wb audit`, `wb worktree list`, and similar read commands from
  producing journal records, on the grounds that they change nothing. An
  agent-fleet operator might still want "was this repository even looked at
  this week" as a signal distinct from "was it changed." Adding it multiplies
  journal volume by the read-command call rate, which is unmeasured; this
  should be sized before it is decided.
- **Should `wb journal show` support a `--since <last-run-marker>` mode**,
  mirroring the open question `cleanup-orchestration` already raised for `wb
  cleanup --since`, so a daily digest costs proportional to the day's
  operations rather than requiring the caller to track their own last-read
  timestamp?
- **Does a Windows-hosted WB installation exist today**, and if so, has
  `LockFileEx`-based advisory locking for `#req:concurrent-appends-stay-uncorrupted`
  actually been exercised anywhere in this codebase, or is it a requirement
  written for a platform WB does not yet run on?

---
*This document follows the https://specscore.md/feature-specification*
