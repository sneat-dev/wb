---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Dependency Streams

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-streams?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-streams?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-streams?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-streams?op=request-change) |
**Status:** Draft
**Source Ideas:** —
**Supersedes:** —

## Summary

A **stream** is one named cross-repository unit of work spanning a library and
the consumers that must change with it: a consumer builds against the library's
*working tree* through an untracked local link, so a change is proven across
every affected repository before anything is published; each downstream
repository accumulates the work on one `stream/<name>` branch kept current by
rebase and landed by rebase-and-merge, so history stays linear and granular; and
publication with consumer bumps happens once, at the end, over exactly the
repositories the stream linked.

There are no merge commits and no single opaque squash at the end. Verification
is batched: a stream applies a whole batch and runs the suite once, bisecting
only when that run fails. Every verb appends a structured event to the stream's
own log, which is the single source for an animated timeline and the metrics
that make the stream's cost and waits measurable rather than remembered.

## Problem

Changing a shared library and its consumers together currently costs one full
release cycle per iteration. Each attempt publishes a version, waits for
registry or tag visibility, opens consumer pull requests, waits for CI, merges,
and only then discovers whether the consumer compiles. A wrong guess costs
another published version, so the version stream fills with releases that exist
only because someone needed to compile something.

`wb deps bump` and `wb deps publish npm` already make that cycle correct and
resumable, but they cannot make it cheap: they are remote propagation, and
remote propagation is the expensive half. Nothing in WB lets a consumer build
against an unpublished library, and nothing records that several worktrees are
one piece of work — so an operator holds the set in their head, and a
half-finished cross-repository change is indistinguishable from an abandoned one.

Two further costs compound it. **History**: several agents working the same
repository during one effort, plus a bump per upstream release, produce either a
thicket of merge commits or one squashed commit that hides every change. Neither
is reviewable. **Time and CPU**: ten dependency bumps land as ten pushes, each
re-running the full lint/vet/test suite through commit and push hooks and again
in CI — on a 4-core workstation that is the difference between a stream that
lands in an hour and one that occupies a day.

The founder's directive states the intended shape directly: *"do less deps
propagations and work more with locally changed deps and propagate at the end"*
and *"stream is a good idea"* — and, on history: *"What I don't want to have is
a lot of merge commits. Want to have clean but granular history."*

## Principles

These are stream-wide rules, not per-command details. Every REQ below is an
application of one of them.

#### REQ: one-verb-per-operation

Any operation that needs more than one `gh` or `git` call to complete **and
verify** MUST be exactly one wb verb. An operator or agent MUST NOT be expected
to chain calls and check the result by eye: the sequence, its waits, and its
verification are the verb's responsibility. A verb MUST report the evidence it
relied on, so a caller never has to re-derive it.

The founder's framing: *"Make deterministic operations when multiple tool calls
required to be performed by wb."*

#### REQ: verbs-state-and-deduplicate-their-work

Every verb MUST state which checks it will run before running them, and MUST run
a given check at most once for the same inputs within one invocation. Re-running
an identical check on unchanged inputs is a defect, not a safety margin.

#### REQ: stream-speed-and-cpu-are-first-class

Stream latency and CPU load MUST be treated as correctness constraints, not
preferences. Verification MUST be batched by default (see Batch verification),
and MUST respect the workstation's concurrency cap — this fleet's is **3
concurrent lanes on a 4-core VM, at most one Angular build and at most two Go
builds**. A design that is correct but re-verifies the same tree ten times MUST
be rejected.

The founder's framing: *"optimize number of tests and vet/lint etc we run … we
don't want to run test 10 times if we merge 10 deps bumps … This is critical and
should be one of the main wb principles — care about speed of stream and cpu
load."*

## Behavior

### Stream lifecycle

#### REQ: stream-is-a-named-set-of-worktrees

WB MUST expose `wb stream start <name> <owner/repository>...`, which creates one
worktree per repository by delegating to the existing
[`wb worktree create <task> [owner/repository...]`](../worktree-lifecycle/README.md)
— including its branch policy, `--original-prompt-file` archival, and the
fleet-wide [remote claim](../remote-claims/README.md) — and records the
resulting set as one stream. The stream name MUST be the worktree task name, so
a stream introduces no second identity for the same work. WB MUST NOT invent a
new worktree creation path.

A stream MUST distinguish exactly one **library** role from one or more
**consumer** roles, because propagation direction is not symmetric. The library
is the repository whose published artifacts the others resolve.

#### REQ: stream-state-is-untracked-and-local

Stream membership, role assignment, and every live link MUST be recorded in
WB-owned state outside the source repositories, alongside the existing
[Work Log](../work-log/README.md). No file inside any member repository may be
created or modified to record stream membership. A stream MUST be readable after
an interrupted session, and `wb stream status` MUST reconstruct it from that
state rather than from repository contents.

#### REQ: stream-status-reports-the-three-gaps

`wb stream status` MUST report, for the whole stream: which consumers currently
hold a live local link and to which library worktree; which library changes are
merged but not yet tagged or published; and which consumers still declare a
version older than the library's newest published version. These are the three
states in which a stream is incomplete, and each MUST be reported separately —
a merged-but-untagged library is not the same problem as a consumer left behind.

#### REQ: stream-end-restores-published-state

`wb stream end <name>` MUST remove every live link the stream created,
restoring each consumer to its published dependency versions, before delegating
worktree removal to the existing
[`wb worktree cleanup`](../worktree-lifecycle/README.md). It MUST refuse to
report success while any link remains. Ending a stream MUST NOT publish, bump,
or merge anything.

### Linear history

#### REQ: stream-branch-with-draft-pr

For each downstream repository, `wb stream start` MUST create exactly one
worktree on branch `stream/<name>` and open a **draft** pull request from it to
`main`, so CI runs on every push to the stream branch and the stream's true
state is always visible. The draft PR MUST NOT be marked ready by `start`; only
landing does that.

#### REQ: agent-branches-squash-into-the-stream

Agents working inside a stream MUST branch from `stream/<name>` into their own
worktrees, with claims exactly as today, and open pull requests **against the
stream branch**, never against `main`. After review each agent PR MUST be landed
into the stream branch with a **squash** merge, producing exactly one commit per
reviewed change.

That one-commit-per-reviewed-change rule is the stream's granularity
**assumption**, recorded as such: it is what makes the final history granular
without carrying every intermediate "fix typo" commit. The alternative — keeping
each agent's raw commits — is an Open Question below.

#### REQ: upstream-bumps-are-one-commit-each

An upstream library release MUST reach the stream branch as exactly **one
commit per bump**. `wb stream sync` MUST write that version bump once the
library is tagged and published. Before the library is tagged, consumers MUST
use `wb deps propagate local` links instead, so an unpublished library never
produces a version-bump commit.

WB MUST NOT open Renovate-style pull requests against `main` for a dependency
that is inside an open stream. Renovate's own-library pull requests continue to
land on `main` as the safety net for repositories and dependencies outside any
stream; `wb stream sync` picks those up automatically, because it rebases the
stream branch onto the updated `origin/main`.

#### REQ: sync-rebases-and-never-merges

`wb stream sync` MUST rebase `stream/<name>` onto the freshly fetched
`origin/main` — never merge it — and MUST then rebase every open agent branch of
the stream onto the new stream head. WB knows those branches from the stream's
claims and recorded membership, so the operator MUST NOT have to name them.

Conflicts MUST be reported **per agent branch**, naming the branch, the agent
that claimed it, and the conflicting paths; a conflict in one agent's branch
MUST NOT abort the rebase of the others. Sync MUST refuse by default while an
agent pull request is mid-review, and MUST offer an explicit flag to proceed
with a warning, because rebasing a branch under review invalidates the review.

#### REQ: landing-is-rebase-and-merge

`wb stream propagate` MUST land a downstream repository by: performing a final
`wb stream sync`, marking the stream pull request ready, waiting for green
checks, and merging it with GitHub's **rebase-and-merge** so `main`
fast-forwards and receives the stream's commits individually. The result MUST be
**one push, one auto-tag, and one deploy per repository**, rather than one per
constituent change.

WB MUST verify the repository permits rebase merges before starting, and MUST
name any repository where it is disabled rather than silently falling back.
Audited 2026-09-03: `allow_rebase_merge` is `true` on all 28 sneat-co product
repositories and on `sneat-dev/wb`, so no repository currently needs enabling.

#### REQ: never-merge-commit-a-stream-branch

WB MUST refuse to land a `stream/<name>` branch with a merge commit, on any
route, and MUST say which route it will use instead. This guard is necessary
rather than theoretical: `allow_merge_commit` is `true` on 26 of the 28 audited
product repositories, so the unwanted route is available in the UI and to any
caller that does not go through wb.

While a stream is open, [`wb worktree merge --route auto`](../mechanical-worktree-merge/README.md)
MUST route an agent worktree of that stream to the **stream branch**, not to
`main`. If an agent pull request is nonetheless found open against `main` while
its repository has an open stream, WB MUST report it as misrouted, name the
stream branch it belongs to, and MUST NOT merge it as part of stream work.

### Local propagation

#### REQ: local-link-discovers-what-the-library-publishes

`wb deps propagate local <library-worktree> --to <consumer-worktree>...` MUST
discover the library's published identities from the library worktree itself:
the Go module path from `backend/go.mod` (or the module root, where the
repository has no `backend/`), and npm package names from `libs/**/package.json`.
Discovery MUST be evidence-based and MUST NOT accept an operator-supplied
package name as a substitute; a consumer that does not depend on any discovered
identity MUST be reported and skipped rather than linked.

#### REQ: go-consumers-link-through-an-untracked-go-work

For a Go consumer, WB MUST write a `go.work` at the consumer worktree root
containing a `use` entry for the library worktree, and MUST ensure that path is
Git-excluded so it can never be committed. `go.work` MUST NOT be added to the
repository's tracked `.gitignore` if that file is tracked; WB MUST use a
worktree-local exclude instead.

CI MUST be unaffected, and the reason MUST be structural rather than
conventional: the file does not exist in the repository, so a CI checkout has no
`go.work` to honour. Where a toolchain might otherwise discover one, WB MUST
document `GOWORK=off` as the explicit guarantee. WB MUST NOT add a `replace`
directive to `go.mod`.

#### REQ: npm-consumers-link-through-a-built-dist

For an npm consumer, WB MUST build the library once using the repository's own
build target, then link the built output into the consumer's `node_modules`
using the package manager's own link mechanism, so no tracked file changes.

WB MUST NOT write a `pnpm` override, alias, or `workspace:` protocol entry into
`pnpm-workspace.yaml` or any `package.json`. This is a hard prohibition, not a
default: the founder rule forbids build-tooling workarounds in tracked config,
and an override is exactly the artefact that survives the stream, reaches CI,
and makes a consumer build against something the registry never published.

#### REQ: verify-runs-single-worker-against-the-linked-copy

`--verify` MUST run each consumer's lint and tests against the linked library
through the existing [`wb verify`](../fleet-quality/README.md) /
[`wb check`](../fleet-quality/README.md) profiles, constrained to a single
worker: Node toolchains with `--maxWorkers=1 --parallel=1` and `NX_DAEMON=false`
in the environment, Go with `go test -p 1` and without `-race`. Verification
MUST report per consumer and MUST NOT stop at the first failure, because the
point of a stream is to learn about every consumer in one pass.

#### REQ: links-are-recorded-and-undoable

Every link MUST be recorded in stream state at the moment it is created, with
enough detail to reverse it exactly: the consumer, the library, the mechanism,
and the dependency version that was in effect before linking. `--undo` MUST
restore those published versions and remove the link. `--undo` MUST succeed even
if the library worktree has since been removed, because the recorded state — not
the library — is the source of truth for reversal.

#### REQ: merge-refuses-a-linked-worktree

`wb worktree merge` MUST refuse to push or land a worktree that has a live link
recorded in stream state, or a `go.work` containing a `use` entry, and MUST name
the offending link and the command that clears it. The refusal MUST be based on
both signals independently: state alone would miss a hand-written `go.work`, and
`go.work` alone would miss an npm link. There MUST be no flag that both bypasses
this guard and pushes.

### Batch verification

#### REQ: batch-verifies-once

`wb stream sync --verify` MUST apply the whole batch it is given — for example
ten dependency bumps plus the rebased agent changes — and then run the full
lint, vet, test and build suite **once** over the resulting tree. It MUST NOT
run the suite per applied change. The same rule applies at landing: the final
verification before merging the stream pull request is one run over the final
tree.

#### REQ: batch-failure-bisects-to-the-first-bad-change

When a batch verification fails, WB MUST revert the batch and re-apply the
changes one at a time, verifying after each, stopping at and reporting the
**first** change whose application fails. The report MUST name that change, the
failing check, and the changes already known good. WB MUST NOT leave the tree in
the failed batch state, and MUST NOT continue past the first failure, because
every later result would be measured against a known-broken tree.

The total cost MUST therefore be one full run in the passing case, and one full
run plus the bisect runs in the failing case — never one full run per change.

### Hook profiles

#### REQ: commit-hook-is-fast-and-scoped

The WB-managed commit hook installed by
[`wb hooks install`](../pre-push-tiering-and-remote-checkpoints/README.md) MUST
run only formatting and static checks, scoped to the files changed in that
commit, and MUST be measured in seconds. It MUST NOT run the test suite. A
commit is not a release gate; it is a save point.

#### REQ: push-hook-defers-to-ci-on-stream-branches

The push hook MUST run **no** verification when the push target is a
`stream/<name>` branch, because that branch has a pull request whose CI verifies
every push, and a local re-run duplicates it on the very machine the stream is
trying to keep free. For every other target the current full profile MUST be
unchanged. WB MUST report which profile it selected and why.

`wb hooks metrics` MUST be able to evidence the difference — local commits, push
attempts, hook failures and durations — so the saving is measured rather than
asserted.

### Remote propagation

#### REQ: remote-propagation-is-the-end-of-stream-wave

`wb deps propagate remote <library-worktree> [--stream <name>]` MUST perform the
end-of-stream wave by composing existing commands rather than reimplementing
them: it MUST ensure the library's pull requests are merged and its tags cut,
publish and verify npm packages through
[`wb deps publish npm`](../npm-release-propagation/README.md) with its exact
registry evidence, and propagate to consumers through
[`wb deps bump <ecosystem>`](../dependency-bump-waves/README.md) using
`--changed <module>@<version>` seeded from the verified release events.

#### REQ: remote-propagation-bumps-only-stream-members

Consumer selection MUST come from the stream's recorded links, not from a fleet
scan. WB MUST NOT bump a repository that was not linked in the stream, even when
`wb deps graph` shows it depends on the library. A repository that depends on
the library but was never linked was never verified against these changes, and
belongs to the Renovate safety net rather than to this wave.

#### REQ: remote-propagation-reports-per-consumer

For each consumer WB MUST land the stream branch as specified in
`landing-is-rebase-and-merge`, wait for checks, and verify the resulting deploy
where the repository has one. The report MUST state, per consumer, the versions
moved, the pull request, the check outcome, and the deploy evidence. A consumer
whose checks fail MUST leave the stream open and MUST NOT block the reporting of
the others.

#### REQ: remote-propagation-refuses-live-links

`wb deps propagate remote` MUST refuse to start while any link in the stream is
live, and MUST name them. Publishing a library whose consumers are still
resolving it from a working tree would verify nothing.

### Event log and analytics

#### REQ: every-verb-appends-a-structured-event

Every wb verb that acts inside a stream MUST append one structured JSONL event
to that stream's event log. The log MUST live with the stream's WB-owned state,
never inside a member repository, and MUST be append-only: a verb MUST NOT
rewrite or delete earlier events, because the log's value is that it records
what actually happened rather than what the current state implies.

Each event MUST carry, where applicable: `ts`, `stream`, `agent` and `session`,
`verb`, `repo`, `worktree`, `branch`, `pr`, `outcome`, and `start`/`end`. Where
the harness supplies them, it MUST also carry `tokens`, `tool_uses` and
`duration_ms`.

This log MUST be the single source for every report below. A report MUST NOT
re-derive history by querying GitHub, because a verb's own outcome is evidence
the API cannot reconstruct — an agent's token spend and a review verdict leave
no trace in a merged commit.

#### REQ: harness-usage-is-ingested-through-the-session-hook

Token, tool-use and duration figures MUST be ingested from the harness through
the installed session hook (`wb hooks agent`), not estimated by wb. Where the
harness reports **cumulative** context tokens for an agent rather than a
per-task delta — as today's does — the log MUST record the value as cumulative
and label it as such, and any per-task figure MUST be derived by differencing
consecutive reports of the same agent. A report MUST NOT present a cumulative
value as if it were the cost of one task.

Events for which the harness supplied no usage MUST be recorded with those
fields absent rather than zero, so a report can distinguish "free" from
"unmeasured".

#### REQ: report-stream-renders-an-animated-timeline

`wb report stream <name> [--html|--json]` MUST render an animated timeline of
the stream from its event log:

- one **swimlane per agent**, with task segments from dispatch to report,
  coloured by kind (author, reviewer, orchestrator, and so on);
- **cumulative tokens and tool calls per lane** on the same time axis, so spend
  and elapsed time are read together rather than in separate reports;
- a **delivery track**: PR opened, review verdict, merge, tag, publish, deploy;
- a **founder-directive track**, so a change of instruction is visible against
  the work it redirected;
- **VM load against the concurrency cap**, so overload and idleness are visible
  as periods rather than as a single average;
- a **playhead animation** over the whole span.

`--json` MUST emit the same model the HTML renders, so the visualization is one
projection of the data and not a second implementation of it.

The prototype dataset at
`/home/ai/claude-parking/2026-09-02/stream-analytics/lane-reports.json` is the
reference shape: `agents[]` (`id`, `kind`, `label`), `reports[]` (`agent`,
`start`, `end`, `tokens`, `tool_uses`, `duration_ms`, `task`, `outcome`),
`deliveries[]` (`t`, `repo`, `event`, `ref`), `load_samples[]` (`t`, `load1`),
and `founder_directives[]` (`t`, `text`). Its `source` field records that
`tokens` is cumulative per agent context — exactly the case the previous
requirement forbids reporting raw.

#### REQ: report-stream-emits-a-metrics-table

The same command MUST emit a metrics table alongside the timeline:

- **lead time per pull request**, split into author, review, CI wait, external
  service wait (for example a bot's queue), and orchestrator wait — an
  undifferentiated lead time hides which of those is the bottleneck, and they
  have different owners;
- **review rounds and reject rate**;
- **tokens and tool calls per merged change**;
- **CI minutes per merged change**, and **redundant runs** — runs whose inputs
  were identical to an earlier run in the same stream, which is the measurement
  that makes `verbs-state-and-deduplicate-their-work` and batch verification
  falsifiable;
- **idle slot minutes against overload minutes**, measured against the
  concurrency cap;
- **rework**: pull requests reverted or superseded.

Every figure MUST be traceable to the events it came from, and a figure that
cannot be computed from the log MUST be reported as unavailable rather than
estimated.

#### REQ: report-fleet-compares-streams

`wb report fleet` MUST aggregate completed streams over a period, defaulting to
a week, and compare them on the same metrics so a trend is visible across
streams rather than only within one.

#### REQ: metric-regression-proposes-a-lesson

When a metric regresses against the trailing comparison, wb MUST **propose** a
backstage lesson naming the metric, the streams compared, and the events behind
the change. It MUST propose only: wb MUST NOT write to the lessons corpus
itself, because a lesson is a human judgement about cause, and a metric change
is only evidence. The proposal MUST link to the lessons corpus and follow its
entry shape; the lessons-mining lane supplies `lessons-for-wb.md` as the
integration point.

## Architecture & Dependencies

```mermaid
sequenceDiagram
    participant O as Operator / lane agents
    participant S as wb stream
    participant L as Library worktree
    participant C as Consumer stream/<name> branch
    participant R as Registry / GitHub
    O->>S: stream start <name> <repos...>
    S->>L: wb worktree create (+ claim)
    S->>C: worktree on stream/<name> + DRAFT PR to main
    O->>S: deps propagate local L --to C... --verify
    S->>C: go.work use / built dist link (untracked)
    Note over O,C: agents branch from stream/<name>, PR into it,<br/>squash-merged: one commit per reviewed change
    O->>S: stream sync --verify
    S->>C: REBASE onto origin/main, rebase agent branches
    S->>C: apply batch, run full suite ONCE (bisect on failure)
    O->>S: deps propagate remote L --stream <name>
    S->>S: refuse if any link is live
    S->>R: merge library PRs, cut tags, deps publish npm
    R-->>S: exact tag + registry evidence
    S->>C: one bump commit per release
    S->>R: ready PR, wait green, REBASE-AND-MERGE
    R-->>S: one push → one auto-tag → one deploy
    O->>S: stream end <name> → undo links, worktree cleanup
```

WB owns the stream identity, the link record, the linear-history guards, and the
batch policy. It owns no new publication, bump, worktree, claim, or hook
mechanics: those remain with `wb deps publish`, `wb deps bump`,
`wb worktree create/merge/cleanup`, `wb remote claim`, and `wb hooks
install/check/repair/metrics`. The link record and stream membership are the only
new durable state, and they live beside the Work Log, never in a member
repository.

### Deterministic verbs — follow-up features

Applications of `one-verb-per-operation`, each derived from a multi-call
sequence performed by hand during the 2026-09-02/03 release night. Each is its
own Feature, not part of this one:

- **`wb pr land <repo#n>`** — verify head, mergeability and green checks, squash
  with the pull request title as the subject, delete the branch, report the
  resulting commit SHA.
- **`wb release verify <repo>`** — confirm the tag, the publish workflow run, and
  the registry `dist-tags` agree, and name the one that disagrees.
- **`wb deploy watch <repo>`** — follow the CI and deploy runs to a green result
  or the exact failing step.
- **`wb renovate run <repo> --wait`** — tick the Dependency Dashboard manual job,
  wait for the run, and report what was offered, created, and armed.
- **`wb deps drift`** — already specified in
  [Dependency Drift](../dependency-drift/README.md); the per-repository
  pin-versus-latest table belongs to its output, not to a hand-built report.

## Acceptance Criteria

### AC: stream-groups-worktrees-under-one-name

**Requirements:** dependency-streams#req:stream-is-a-named-set-of-worktrees, dependency-streams#req:stream-state-is-untracked-and-local, dependency-streams#req:stream-branch-with-draft-pr

**Given** an operator names a stream and three repositories
**When** they run `wb stream start`
**Then** WB creates one worktree per repository through the existing worktree
creation path with its claim and Work Log archival, each downstream repository
is on branch `stream/<name>` with a draft pull request open to `main`, the
stream is recorded in WB-owned state outside every repository, and `git status`
in each worktree reports no new or modified file.

### AC: status-separates-linked-untagged-and-behind

**Requirements:** dependency-streams#req:stream-status-reports-the-three-gaps

**Given** a stream in which one consumer holds a live link, the library has a
merged pull request with no tag, and a second consumer declares an older
published version
**When** the operator runs `wb stream status`
**Then** all three conditions are reported separately and named per repository,
and the report is produced from stream state after a session restart.

### AC: agent-work-lands-as-one-commit-each

**Requirements:** dependency-streams#req:agent-branches-squash-into-the-stream, dependency-streams#req:upstream-bumps-are-one-commit-each

**Given** two agents each with a reviewed pull request against `stream/<name>`,
one containing five intermediate commits, and one tagged upstream library release
**When** both are landed and `wb stream sync` records the bump
**Then** the stream branch gains exactly three commits — one per reviewed agent
change and one for the bump — and contains no merge commit.

### AC: sync-rebases-and-reports-conflicts-per-agent

**Requirements:** dependency-streams#req:sync-rebases-and-never-merges

**Given** an open stream with three agent branches, where `origin/main` has moved
and exactly one agent branch conflicts with the new stream head
**When** the operator runs `wb stream sync`
**Then** the stream branch is rebased onto `origin/main` with no merge commit
created, the two non-conflicting agent branches are rebased onto the new head,
the conflict is reported naming the branch, its claiming agent and the
conflicting paths, and the other two agents' results are still reported.

### AC: sync-refuses-while-a-pr-is-mid-review

**Requirements:** dependency-streams#req:sync-rebases-and-never-merges

**Given** an agent pull request against the stream branch that is under review
**When** the operator runs `wb stream sync`
**Then** WB refuses by default, names the pull request and its reviewer state,
and proceeds only under an explicit flag, emitting a warning that the review was
invalidated.

### AC: misrouted-agent-pr-is-reported-not-merged

**Requirements:** dependency-streams#req:never-merge-commit-a-stream-branch

**Given** an agent worktree of an open stream whose pull request was opened
against `main` by mistake
**When** the operator runs `wb worktree merge --route auto`
**Then** WB routes stream agent worktrees to the stream branch, reports the
misrouted pull request and the stream branch it belongs to, and does not merge
it as part of stream work.

### AC: landing-is-a-fast-forward-with-one-deploy

**Requirements:** dependency-streams#req:landing-is-rebase-and-merge, dependency-streams#req:never-merge-commit-a-stream-branch

**Given** a stream branch holding six commits and a repository that permits
rebase merges
**When** the operator lands it
**Then** `main` fast-forwards and gains the six commits individually with no
merge commit, exactly one push, one auto-tag and one deploy occur, and — in a
repository where `allow_rebase_merge` is disabled — WB names the repository and
refuses instead of falling back to another route.

### AC: go-consumer-builds-against-the-library-worktree

**Requirements:** dependency-streams#req:local-link-discovers-what-the-library-publishes, dependency-streams#req:go-consumers-link-through-an-untracked-go-work

**Given** a Go consumer that requires the library's module path and a library
worktree containing an uncommitted change to that module
**When** the operator runs `wb deps propagate local <library> --to <consumer>`
**Then** the consumer compiles against the working-tree library, a `go.work`
with a `use` entry exists at the consumer worktree root, `go.mod` is unchanged,
`git status` reports a clean tree, and the same build with `GOWORK=off` resolves
the published version instead.

### AC: npm-consumer-links-without-tracked-config

**Requirements:** dependency-streams#req:npm-consumers-link-through-a-built-dist

**Given** an npm consumer depending on a package the library publishes
**When** the operator links it locally
**Then** the library is built once with the repository's own build target and
linked from its dist into the consumer's `node_modules`; `pnpm-workspace.yaml`
and every `package.json` are byte-identical to their committed contents; and no
override, alias, or `workspace:` entry is introduced anywhere in tracked config.

### AC: verify-reports-every-consumer-single-worker

**Requirements:** dependency-streams#req:verify-runs-single-worker-against-the-linked-copy, dependency-streams#req:stream-speed-and-cpu-are-first-class

**Given** two linked consumers, one of which fails its tests against the
library's change
**When** the operator adds `--verify`
**Then** both consumers are verified against the linked copy, Node runs carry
`--maxWorkers=1 --parallel=1` with `NX_DAEMON=false` and Go runs use
`go test -p 1` without `-race`, concurrency stays within the workstation cap,
the failure is attributed to its consumer, and the passing consumer is still
reported.

### AC: undo-restores-published-versions

**Requirements:** dependency-streams#req:links-are-recorded-and-undoable

**Given** consumers linked to a library worktree that has since been deleted
**When** the operator runs `wb deps propagate local --undo`
**Then** every consumer resolves its published version again, no `go.work` or
package link remains, each worktree is clean, and the operation succeeds without
reading the removed library worktree.

### AC: merge-refuses-while-a-link-is-live

**Requirements:** dependency-streams#req:merge-refuses-a-linked-worktree

**Given** a consumer worktree with a live link, and separately a worktree with a
hand-written `go.work` containing a `use` entry and no stream record
**When** `wb worktree merge` is run on either
**Then** it refuses before any push, names the link or the `use` entry and the
command that clears it, and no flag combination both bypasses the guard and
pushes.

### AC: ten-bumps-verify-once-then-bisect

**Requirements:** dependency-streams#req:batch-verifies-once, dependency-streams#req:batch-failure-bisects-to-the-first-bad-change, dependency-streams#req:verbs-state-and-deduplicate-their-work

**Given** a batch of ten dependency bumps in which the seventh breaks the build
**When** the operator runs `wb stream sync --verify`
**Then** WB applies all ten and runs the full suite exactly **once**, and only
after that run fails does it revert and re-apply one at a time; it stops at the
seventh, names it and the failing check, lists bumps one to six as known good,
leaves the tree out of the failed batch state, and never performs ten full runs.
With all ten passing, the total is exactly one full run.

### AC: hooks-are-cheap-on-a-stream-branch

**Requirements:** dependency-streams#req:commit-hook-is-fast-and-scoped, dependency-streams#req:push-hook-defers-to-ci-on-stream-branches

**Given** a repository with WB-managed hooks and an open stream branch
**When** an agent commits to and pushes that branch, and separately pushes a
non-stream branch
**Then** the commit hook runs only formatting and static checks over the changed
files and no test suite; the push to the stream branch runs no local
verification and says CI on the stream pull request is the gate; the push to the
other branch runs the current full profile unchanged; and `wb hooks metrics`
shows the recorded durations for both.

### AC: remote-wave-publishes-then-bumps-only-members

**Requirements:** dependency-streams#req:remote-propagation-is-the-end-of-stream-wave, dependency-streams#req:remote-propagation-bumps-only-stream-members, dependency-streams#req:remote-propagation-reports-per-consumer, dependency-streams#req:one-verb-per-operation

**Given** a stream whose links have been undone, whose library changes are
merged, and a fourth repository that depends on the library but was never in the
stream
**When** the operator runs `wb deps propagate remote --stream <name>`
**Then** one verb completes and verifies the whole sequence, the library is
tagged and published with exact registry evidence before any consumer changes,
exactly the stream's consumers land their stream branches, the fourth repository
receives none, and the report states per consumer the versions moved, the pull
request, the check outcome, and the deploy evidence.

### AC: remote-wave-refuses-live-links

**Requirements:** dependency-streams#req:remote-propagation-refuses-live-links, dependency-streams#req:stream-end-restores-published-state

**Given** a stream with one live link remaining
**When** the operator runs `wb deps propagate remote`
**Then** it refuses before publishing anything and names the live link; and when
the operator instead runs `wb stream end`, every link is undone and the command
refuses to report success while any link remains.

### AC: every-verb-leaves-one-event

**Requirements:** dependency-streams#req:every-verb-appends-a-structured-event

**Given** a stream in which sync, a local link, an agent PR landing and a
release have all run
**When** the operator inspects the stream's event log
**Then** each verb has appended exactly one JSONL event carrying its `ts`,
`stream`, `agent`, `verb`, `repo`, `outcome` and timing; no earlier event has
been rewritten or removed; and no file inside any member repository was created
or modified.

### AC: cumulative-tokens-are-never-reported-as-per-task

**Requirements:** dependency-streams#req:harness-usage-is-ingested-through-the-session-hook

**Given** a harness that reports cumulative context tokens, and one agent with
three consecutive reports of 100k, 250k and 286k tokens
**When** `wb report stream` presents per-task cost
**Then** the three tasks are shown as 100k, 150k and 36k derived by differencing,
the stored values remain labelled cumulative, and a fourth event whose usage the
harness did not supply is shown as unmeasured rather than zero.

### AC: timeline-and-json-are-one-model

**Requirements:** dependency-streams#req:report-stream-renders-an-animated-timeline

**Given** a completed stream whose log matches the reference shape of the
prototype dataset
**When** the operator runs `wb report stream <name> --html` and again with
`--json`
**Then** the HTML shows a swimlane per agent with segments coloured by kind,
cumulative tokens and tool calls on the same axis, the delivery and
founder-directive tracks, VM load against the cap, and a playhead; the `--json`
output contains the same model the HTML renders; and both are produced from the
event log without querying GitHub.

### AC: metrics-separate-the-waits-and-count-redundant-runs

**Requirements:** dependency-streams#req:report-stream-emits-a-metrics-table, dependency-streams#req:verbs-state-and-deduplicate-their-work

**Given** a stream containing a pull request that waited on review, on CI, and
on an external bot's queue, and a batch that verified once plus one identical
re-run
**When** the metrics table is produced
**Then** lead time is split into author, review, CI wait, external wait and
orchestrator wait rather than reported as one number; the identical re-run is
counted as a redundant run; idle and overload minutes are reported against the
concurrency cap; and any metric not computable from the log is marked
unavailable rather than estimated.

### AC: regression-proposes-a-lesson-without-writing-one

**Requirements:** dependency-streams#req:report-fleet-compares-streams, dependency-streams#req:metric-regression-proposes-a-lesson

**Given** two completed streams where the later one has a materially worse
tokens-per-merged-change figure
**When** the operator runs `wb report fleet`
**Then** the comparison names the regressed metric and the events behind it, a
lesson is **proposed** with a link to the lessons corpus, and the corpus itself
is unchanged on disk.

## Rehearse Integration

Every acceptance criterion has a deterministic CLI, Git, filesystem, or process
surface. Pending scenario stubs live under `_tests/` and are intended to use
temporary Git remotes plus fake registry, GitHub, build, and hook adapters, so
no scenario publishes a package or opens a real pull request. The guards are
observable without a network: `git status`, the presence or absence of
`go.work`, byte-comparison of tracked manifests against `HEAD`, and
`git log --merges` on the landed branch. Batch behavior is observable by
counting adapter invocations — the ten-bump scenario asserts the number of full
suite runs, which is the property that matters.

## Not Doing

- Migrating any repository into a monorepo, or introducing a permanent
  multi-repo workspace.
- Committing `replace` directives, `go.work` files, `pnpm` overrides, aliases,
  or `workspace:` protocol entries to any repository.
- Changing Renovate presets, schedules, or automerge policy from wb.
- Replacing `wb deps bump`, `wb deps publish`, `wb worktree create/merge`,
  `wb remote claim`, or `wb hooks` with stream-specific implementations.
- Publishing from a developer machine: publication remains the repository's own
  workflow, as [NPM release propagation](../npm-release-propagation/README.md)
  already requires.
- Cross-machine streams. A stream is local to one workstation; its worktrees are
  claimed fleet-wide, but its links are not shared.
- Landing a stream with a merge commit or a single squash of the whole stream.
- Implementing the deterministic follow-up verbs listed above; this Feature only
  names them.
- Writing to the backstage lessons corpus. wb proposes; a human decides.
- Estimating token or duration figures the harness did not report, or presenting
  a cumulative context total as the cost of one task.
- Reconstructing stream history from the GitHub API. The event log is the record;
  an absent event is a gap to fix in the verb, not to paper over in the report.

## Open Questions

**1. Should agent pull requests be squashed into the stream branch, or should
their raw commits be kept?** This spec assumes **squash — one commit per
reviewed change** (`agent-branches-squash-into-the-stream`), on the grounds that
it is the granularity a reviewer of `main` wants and it keeps "fix typo" out of
the permanent history. The alternative is a rebase that preserves each agent's
commits, giving finer bisect resolution at the cost of a noisier `main`. This is
recorded as an assumption because it is reversible: it changes only how agent
pull requests are landed, not the stream's shape.

**2. Should own-library consumer bumps keep flowing through Renovate once
`wb deps propagate remote` exists?** Renovate's own-library automerge is
currently the mechanism that keeps every consumer on the latest release, and it
is the only thing that covers repositories nobody put in a stream. But once a
stream publishes and bumps its members deliberately, Renovate becomes a second
writer for the same edges, and the two can open competing pull requests for the
same version.

Recommendation: keep Renovate as the **safety net**, not the primary path, and
give own-library groups a daily `automergeSchedule` window. Streams then own the
fast path for work that is in flight, Renovate closes the gap for everything
else within a day, and the two cannot race in the minutes after a publish. The
alternative — making `propagate remote` the only path — would leave any consumer
outside a stream silently behind, which is the failure this fleet has already
had once.

Related policy item, deliberately **not** part of this feature: auto-tagging
currently cuts a release tag for commits that touch only tests or documentation,
so the version stream carries releases with no consumer-visible change. That
inflates every wave this feature triggers, but it is a CI policy question for
the tagging workflow rather than a stream behavior.

---
*This document follows the https://specscore.md/feature-specification*
