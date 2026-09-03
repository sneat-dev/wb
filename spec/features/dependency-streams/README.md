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

#### REQ: every-refusal-names-the-sanctioned-command

Every wb guard that refuses MUST name, in its own failure output, the exact
command that satisfies it; and there MUST be no flag that both bypasses a guard
and pushes.

#### REQ: waits-are-verbs-not-instructions

Any wait longer than a blocking tool's own ceiling MUST be owned by a wb verb
that states its ceiling; wb MUST NOT expect a caller to "poll until it settles".

#### REQ: verbs-assert-effects-not-exit-codes

Every verb MUST verify the observable effect — a ref on the remote, a resolvable
tag, an HTTP 200, a registry dist-tag — never an exit status, and MUST NOT pipe a
command whose success it will then trust.

#### REQ: stream-verbs-re-read-state-before-mutating

Every stream verb MUST re-fetch and re-read origin and stream state immediately
before acting. A value read at session start is a snapshot, not a live view.

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

#### REQ: stream-start-proves-the-fleet-is-ready

`wb stream start` MUST, per member, run `wb hooks check`, the npm
provider-identity scan, and a red-`main` check, and MUST refuse or explicitly
report each member that fails.

#### REQ: stream-membership-is-proposed-from-the-transitive-graph

Membership MUST be proposed from `wb deps graph`'s full transitive walk, and any
transitive consumer left out MUST be named in the report — because
`remote-propagation-bumps-only-stream-members` will otherwise leave it silently
behind.

#### REQ: worktrees-derive-their-ambient-resources

Each stream worktree MUST receive a derived port base, private scratchpad path,
and temp/emulator namespace, exported into every verification run; two worktrees
of one repository MUST be able to verify concurrently.

#### REQ: stream-end-proves-absorption-and-removes-its-own-scaffolding

`wb stream end` MUST refuse to remove a member whose branch has commits not
absorbed by the landing — a named, content-level check, never a path listing —
MUST close or report its draft pull requests, and MUST delete its own
task-directory scaffolding.

#### REQ: every-stream-verb-has-a-terminal-recovery

Every reachable `(phase, status)` of a stream verb MUST have a recovery
transition, tested against reachable states rather than imagined ones. An
environment failure MUST NOT strand a member whose work is provably complete.

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

#### REQ: dependency-bumps-are-commits-on-the-stream-branch

An own-library version bump MUST NOT get its own worktree, its own pull request,
or an agent. `wb stream sync` MUST apply bumps **inside the stream's existing
worktree** — it needs a checkout, because `go get` / `go mod tidy` and
`pnpm install --lockfile-only` must run to update `go.sum` and `pnpm-lock.yaml`
— and MUST produce **one commit per library**, subject-formatted to the
repository's own convention (`fix(deps): <module> vX → vY`, or `chore(deps): …`
where that is the repository's style), pushed to `stream/<name>`.

The batch is verified once under the batch-verification requirements, and on
failure bisected by reverting bump commits one at a time.

The founder: *"I wonder if deps propagation can be made directly on stream branch
so we do not create worktree for small deps changes."*

**Exception, and it is the important half.** A bump that needs code adaptation is
not a mechanical bump. When applying a bump breaks compilation, wb MUST stop,
report the failing library and the break, and MUST NOT attempt to adapt the code.
That becomes an **agent task on the stream** with its own worktree and a pull
request into the stream branch, exactly like any other reviewed change.

Repositories outside any stream keep the Renovate safety net unchanged.

#### REQ: stream-backlog-is-counted-by-patch-identity

`wb stream status` and `wb worktree list` MUST collapse patch-identical branches
(`git cherry` / patch-id) and MUST name any cluster of N branches carrying one
body of work.

#### REQ: claims-carry-a-session-identity

A claim on a stream or agent branch MUST record the live registered agent
session, and a push from a different live session on the same machine MUST
refuse. Machine scope remains the fleet-wide unit; session scope is the
intra-machine unit.

#### REQ: push-verifies-the-ref-it-pushed

After any push wb MUST compare the pushed local SHA with `origin/<branch>` and
fail on divergence. A push exit code is not evidence the intended commit landed.

#### REQ: review-checkout-is-disposable

Reviewing a stream or agent branch MUST use a throwaway worktree; wb MUST refuse
a detached checkout inside a claimed worktree.

#### REQ: stream-cleanup-proof-covers-a-rebase-landing

The receipted proof chain for cleanup MUST cover rebase-and-merge landings —
source SHA → stream head by ancestry → landed range on `origin/main` by ancestry
and tree equality — not only the squash chain.

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
worker: Node toolchains with `--maxWorkers=1 --parallel=1`, `NX_DAEMON=false`
and `NX_SKIP_NX_CACHE=true` in the environment, Go with `go test -p 1` and
without `-race`. Verification
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

#### REQ: verification-prints-its-active-links

Every verification run against a linked consumer MUST print the links in effect
and the published version each replaced.

#### REQ: link-discovery-uses-the-canonical-dependency-sections

Local-link discovery MUST use the same canonical dependency-field set as graph
discovery and release evidence.

#### REQ: npm-link-preserves-a-frozen-lockfile-baseline

Before linking, the consumer MUST prove a clean frozen install of its unlinked
tree, so a link never masks a lockfile or manifest mismatch.

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

#### REQ: batch-verification-runs-what-ci-runs

The single batched run MUST execute CI's own mechanisms — `go vet`, `-race`
where CI uses it, `CI=1`, `-count=1`, build cache disabled — **or** name, in its
own output, each mechanism it is not running and state that CI on the stream
pull request owns it. Silently running less than CI while implying otherwise is
the failure this forbids.

#### REQ: single-worker-does-not-replace-per-file-isolation

Single-worker Node verification MUST keep per-file isolation on and MUST set
`NX_SKIP_NX_CACHE=true`. Serialization is **not** a substitute for isolation: it
can make one file's leaked state deterministically poison the next, which is
worse than a flake because it is reproducible and misattributed.

#### REQ: batch-verification-is-keyed-to-a-tree-identity

A batch result MUST be recorded against the exact stream-branch SHA and each
link's library SHA, and MUST be invalidated when either moves.

#### REQ: every-verification-run-is-bounded

The batch run and each bisect step MUST carry a timeout and MUST report a hang as
a failure, bounding descendants that hold the captured output pipe.

#### REQ: a-lockstep-family-is-one-batch-element

A lockstep-versioned family — Angular, Nx, Ionic, Capacitor, `@sneat/*` — MUST be
applied as one batch element and MUST NOT be bisected internally.

#### REQ: every-profile-declares-a-measured-budget

Every verification profile MUST carry a measured cold wall-time budget before
becoming a default, evidenced by `wb hooks metrics`.

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

#### REQ: pre-push-refuses-index-worktree-divergence

Before any expensive work, the push hook MUST fail when a file touched by the
commits being pushed differs between `HEAD` and the working tree; unrelated dirt
is tolerated. This check is sub-second and is *more* necessary under streams,
where rebase and conflict resolution make index and tree drift by construction.

#### REQ: check-profile-fleet-hygiene

A `wb check` profile MUST report, on demand, canonical clones off their default
branch, dirty, or holding unpushed commits, and MUST refuse when
completed-but-unmerged worktrees exceed a small threshold.

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

#### REQ: propagate-remote-preflights-the-tag

Before any tag: refuse to hand-tag a repository whose workflows do not disable
version bumping; require the intended version to be strictly greater than the
highest tag for that module prefix **and** its commit to descend from the
previous tag's commit; and verify the tag on the remote independently of the
push's exit code.

#### REQ: all-fences-run-before-the-first-side-effect

Every release fence MUST run before the first tag, package, or deployment is
created, binding repository, protected branch, reachable commit, event, workflow
path, run status and every required job. Post-tag CI is an alarm, not prevention.

#### REQ: publication-is-a-terminal-outcome

`wb release verify` MUST require an explicit publish, or an explicit
machine-readable `not_releasable` the caller expected, and MUST confirm the tag,
publish run and registry dist-tags agree. It MUST download and run the published
artifact under a timeout, never a rebuild.

#### REQ: propagate-remote-audits-protection-per-consumer

Before marking any stream pull request ready, wb MUST verify strict
server-enforced branch protection whose required contexts match the repository's
configured workflow contexts.

#### REQ: ci-wait-tracks-the-run-that-carried-the-commit

`wb ci wait` MUST confirm the run carrying the pushed SHA reached completion,
MUST query workflow runs by SHA when no check appears, and MUST re-dispatch
rather than wait on faith.

#### REQ: deploy-watch-asserts-the-exact-commit-in-production

`wb deploy watch` MUST fetch the deployed artifact's own version marker and bind
it to the landed commit. A green deploy run alone is not deployment truth.

#### REQ: propagate-remote-checks-the-exported-api-diff

The accepted version bump MUST be validated against the actual exported-symbol
diff, not the conventional-commit prefix; below 1.0.0 a removed export is a
minor. A migration note naming each moved or sealed symbol MUST be emitted with
the release.

#### REQ: reports-are-owner-qualified

wb MUST never print an ownerless version token; every version, tag or moving
reference names its repository in the same sentence.

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

### Worktree hygiene, disk, and caches

The founder: *"We have a real problem with worktrees hygiene — always fight
about it."*

#### REQ: worktree-gc-classifies-by-pull-request-state

`wb worktree gc` MUST classify every worktree by **pull request state, not by
Git ancestry alone** — a squash merge leaves no ancestry evidence, so a merged
branch looks unmerged to `git`. A worktree with a clean tree whose pull request
is merged or closed, or which is a detached review checkout, MUST be removable.
A worktree that is dirty, claimed, or has an open pull request MUST be kept and
listed with the reason.

`gc` MUST purge `.wb-retired-stage-*` leftovers, and MUST report every worktree
with an **owner, an age, and a TTL**. Every refusal MUST name the sanctioned
command.

#### REQ: disk-occupancy-is-reportable

`wb disk` MUST report occupancy — total, and per stream, per repository, per
worktree, plus caches and logs separately — in a machine-readable form as well as
for a human.

The founder: *"I think we need wb command that will report disk space occupancy —
total and per stream and per repo (all worktrees and logs)."*

#### REQ: caches-are-pruned-against-budgets-and-a-floor

`wb cache prune` MUST prune build and package caches against declared budgets,
and `wb stream start` MUST check a **disk floor** before creating anything.
Observed sizes on this workstation, as the initial budgets: go-build 17 GB, pnpm
store 15 GB, npm 6.7 GB, Playwright 2.5 GB.

The founder: *"Maybe cache hygiene can also be part of wb? … I don't like we got
to 99% of disk usage."*

#### REQ: work-and-event-logs-are-never-pruned

`wb worktree gc` and `wb cache prune` MUST NEVER delete work logs or stream event
logs. They are small — 17 MB across the fleet — and they are the source for every
report in this Feature. They may be compressed, rotated, or uploaded; they may
not be discarded.

The founder: *"maybe we do not clean wb work logs?"* — the answer this Feature
gives is no, never, because deleting them destroys the analytics the stream
exists to produce.

### Borrowed from the Orca benchmark

Four shapes worth taking from Stably AI's Orca desktop ADE, per
`orca-vs-wb-benchmark.md`. Each is adapted to WB's own boundaries rather than
copied.

#### REQ: worktree-create-can-share-heavy-artifacts

`wb worktree create --share auto` MUST be able to link `node_modules` and module
caches and copy `.env`, so a new worktree is not a full reinstall. `wb worktree
merge` MUST refuse a tree with a live share link, by the same rule and the same
guard that already refuses a live dependency link.

#### REQ: worktree-selectors-resolve-through-the-claim

Worktree selectors (`task:`, `branch:`, `path:`, `active`) MUST resolve **through
the claim**, so addressing a worktree and being authorized to act on it are the
same operation. A selector that resolves to a worktree the caller does not hold
MUST refuse rather than act.

#### REQ: lane-brief-caches-facts-by-content-hash

`wb lane brief` MUST assemble a lane's briefing facts with a cache keyed by
**content hash**, so repeating a brief costs nothing when nothing changed, and is
invalidated exactly when an input does.

#### REQ: machine-readable-reads-are-cursor-paged

Machine-readable read commands MUST support cursor paging, so an agent consuming
a large inventory does not have to take it in one response.

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

### AC: bumps-land-as-commits-not-worktrees

**Requirements:** dependency-streams#req:dependency-bumps-are-commits-on-the-stream-branch, dependency-streams#req:batch-verifies-once

**Given** ten own-library bumps due in one downstream repository with an open
stream
**When** the operator runs `wb stream sync --verify`
**Then** the stream branch gains exactly ten commits, one per library, with
`go.sum` and `pnpm-lock.yaml` updated in the same commit as their manifest;
**zero** new worktrees and **zero** new pull requests are created; and the full
suite runs exactly **once** over the resulting tree.

### AC: a-bump-needing-code-adaptation-becomes-an-agent-task

**Requirements:** dependency-streams#req:dependency-bumps-are-commits-on-the-stream-branch

**Given** a batch of bumps in which one library's new version breaks compilation
**When** `wb stream sync` applies it
**Then** wb stops, names the failing library and the break, does not attempt to
adapt the code, and reports it as an agent task for the stream — to be done in
its own worktree with a pull request into the stream branch.

### AC: batched-run-either-matches-ci-or-says-what-it-skipped

**Requirements:** dependency-streams#req:batch-verification-runs-what-ci-runs, dependency-streams#req:single-worker-does-not-replace-per-file-isolation

**Given** a repository whose CI runs `go vet` and `-race`, and a stream batch
verified locally without `-race`
**When** the batch run completes
**Then** its output names `-race` as a mechanism it did not run and states that
CI on the stream pull request owns it; and the Node half runs with per-file
isolation retained and `NX_SKIP_NX_CACHE=true` set, so serialization is never
presented as isolation.

### AC: gc-removes-squash-merged-worktrees-and-keeps-claimed-ones

**Requirements:** dependency-streams#req:worktree-gc-classifies-by-pull-request-state, dependency-streams#req:work-and-event-logs-are-never-pruned

**Given** one clean worktree whose pull request was **squash**-merged (so `git`
shows its branch as unmerged), one dirty worktree, one claimed worktree with an
open pull request, and a stale `.wb-retired-stage-*` directory
**When** the operator runs `wb worktree gc`
**Then** the squash-merged one is classified removable on pull-request evidence,
the dirty and claimed ones are kept and listed with their reason and their owner,
age and TTL, the retired-stage leftover is purged, every refusal names the
sanctioned command, and **no work log or event log is deleted**.

### AC: disk-and-cache-report-and-prune-to-budget

**Requirements:** dependency-streams#req:disk-occupancy-is-reportable, dependency-streams#req:caches-are-pruned-against-budgets-and-a-floor

**Given** a workstation near its disk limit
**When** the operator runs `wb disk` and then `wb cache prune`
**Then** occupancy is reported total, per stream, per repository, per worktree,
and for caches and logs separately, in machine-readable form; caches are pruned
against their declared budgets; logs are untouched; and a subsequent
`wb stream start` refuses while free space is below the floor, naming the
command that reclaims it.

## Rehearse Integration

Every acceptance criterion has a deterministic CLI, Git, filesystem, or process
surface. Pending scenario stubs live under `_tests/` and are intended to use
temporary Git remotes plus fake registry, GitHub, build, and hook adapters, so
no scenario publishes a package or opens a real pull request. Coverage MUST
include **positive terminal-receipt** scenarios for `propagate remote` and
`stream end`, not only refusal scenarios, and every mutating stream leaf MUST
ship a `--dry-run` with preview/apply parity and an exact-command execution
test. The guards are
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
- Growing a `wb serve` daemon or a GUI-coupled runtime. A short-lived command is
  a feature on a 4-core shared VM, and leaked daemon generations are Orca's own
  top-voted bug.
- An orchestration layer (Run / Task / Dispatch / inbox / decision gates).
  Deciding who runs next is the harness's job.
- Free-text `worktree --comment` as a status of record: an unverifiable second
  source of truth beside the Work Log. `wb worktree log checkpoint`, which
  carries observed Git evidence, already covers the useful habit.
- Delete-worktree-deletes-branch with a force-delete escape. `wb branch cleanup`
  proving containment in the exact origin target is strictly safer.
- UI-only parent/child nesting: a stream relationship must be real state that
  governs landing order, not a display tree.
- Full-autonomy launch flags as a default, and global state wipes of the
  `orca reset --all` shape.
- Shipping `wb report stream --publish` (see Future work); this Feature specifies
  only the local report.

## Future work — shareable stream replays

Out of scope for this Feature; recorded because it changes what the event log is
worth. The founder: *"That could become a killer feature … wb can upload those
work logs and/or animated graph data to wb cloud (doesn't exist yet) or to
private storage and open and show the graph snapshot or animation on
wb.sneat.dev — this can be shareable … Think twitch for ai agent sessions."*

`wb report stream --publish` would upload a snapshot — or a live replay — to
private storage first and a hosted `wb.sneat.dev` viewer later. Two constraints
are already clear and belong on the record now, because they shape the event
schema this Feature does specify:

- **Redaction is a precondition, not a later feature.** Events carry repository
  paths, branch names, worktree paths and verb output; a publish path MUST
  redact secrets and local filesystem paths before anything leaves the machine.
- **Private storage first, cloud second.** A shareable link is a disclosure
  surface; the default MUST be private, and sharing MUST be an explicit act on a
  named snapshot rather than a mode the stream runs in.

## Open Questions

**1. Five lessons whose prescriptions collide with this spec, and how each is
resolved.** Recorded here because the corpus's own
`an-enforcement-ladder-needs-an-audited-correction-path` requires the original
text be retained rather than deleted:

1. `a-local-landing-gate-must-execute-the-same-mechanisms-as-ci` (17 occurrences)
   — it requires the pre-push hook to run what CI runs; this spec runs **no**
   verification on stream branches. The lesson is really a disjunction: *either
   the hook runs what CI runs, or it stops implying it did.* The stream takes the
   second horn, honestly, via `push-hook-defers-to-ci-on-stream-branches` and
   `batch-verification-runs-what-ci-runs`. **Revise the lesson to sanction that
   horn for stream branches, with those two REQs as its Control.**
2. `fresh-ci-validation-must-bypass-cache-and-preserve-test-file-isolation` —
   not a lesson revision but a **spec defect this review found and this revision
   fixes**: `--maxWorkers=1 --parallel=1` is serialization, not isolation, and
   the draft omitted `NX_SKIP_NX_CACHE=true`. Both are now required by
   `single-worker-does-not-replace-per-file-isolation`.
3. `l127` ("push early and often") — no real conflict: batching governs local
   verification and publication, never pushing to a stream branch, which is cheap
   under the new push profile and runs CI on the draft PR. **Revise the lesson to
   say so**, or "propagate at the end" will be misread as "push less".
4. `l154` ("stop defaulting to Do NOT push") — its own prescribed remedy *is* the
   stream. **Revise to cite this Feature**, distinguishing "push to the stream
   branch" (encouraged) from "propagate" (batched, end-of-stream).
5. `l51` (fail any PR touching `.github/workflows/` without a justification line)
   — would add a per-PR gate to every stream that legitimately touches CI.
   **Keep its push-early/draft-PR half** (already satisfied by
   `stream-branch-with-draft-pr`) **and scope the justification-line proposal to
   non-stream branches.**

**Standing tension, not a contradiction:** `l10` (coverage floors are raised with
real tests, never lowered to fit) and `l2` (a new test must be proven to fail
against the unfixed code) sit against CPU-budget pressure. The corpus already
ratified the reconciliation in
`a-validation-gate-needs-a-wall-time-budget-before-it-becomes-the-default`:
**reduce cost by scheduling and sharding, never by removing checks or lowering
floors.** "Speed is a correctness constraint" is never licence to weaken a gate.

**2. Should agent pull requests be squashed into the stream branch, or should
their raw commits be kept?** This spec assumes **squash — one commit per
reviewed change** (`agent-branches-squash-into-the-stream`), on the grounds that
it is the granularity a reviewer of `main` wants and it keeps "fix typo" out of
the permanent history. The alternative is a rebase that preserves each agent's
commits, giving finer bisect resolution at the cost of a noisier `main`. This is
recorded as an assumption because it is reversible: it changes only how agent
pull requests are landed, not the stream's shape.

**3. Should own-library consumer bumps keep flowing through Renovate once
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

## Appendix — lessons this Feature moves up the enforcement ladder

Mapped in `/home/ai/claude-parking/2026-09-02/lessons-for-wb.md` (118 relevant
backstage lessons reviewed). Shipping this Feature lets **47** change status:
23 Recorded → Enforced, 15 Recorded → Enforced in the dependency/release group,
4 Stated → Enforced, and 5 Recorded → Stated; a further 7 already-Enforced
lessons widen their Control and Evidence only.

The status change is a consequence of shipping, not part of it: each lesson's
Control becomes the REQ above that mechanizes it, and its Evidence becomes that
REQ's acceptance criterion. Representative ids, by theme:

- **Worktree isolation and ambient resources** —
  `worktree-isolation-includes-ambient-resources`, `l2026-08-11-0634`,
  `l2026-08-10-1610`, `l5-git-stash-is-repo-global-across-worktrees`,
  `l45-worktree-policy-must-fail-before-implementation-not-at-commit`,
  `clean-clone-guard-resolves-write-targets`,
  `a-clean-clone-guard-keyed-on-command-text-refuses-commands-that-write-nothing`,
  `l84`.
- **Branch topology and linear history** — `l159`, `l139`,
  `a-squash-cleanup-proof-must-follow-the-receipted-integration-chain`,
  `a-tree-listing-comparison…`, `a-queried-inventory…`,
  `wb-worktree-cleanup-leaves-its-own-task-directory-residue`,
  `cross-session-shared-branch-ownership-must-be-visible-before-push`,
  `merger-lane-branch-race`, `a-reviewer-must-never-check-out…`, `l6`.
- **Push correctness, hooks, gate cost** — `l155`, `l12`, `l120`, `l154`, `l127`,
  `l51`, `l65`, `l160`, `l136`,
  `a-local-landing-gate-must-execute-the-same-mechanisms-as-ci`,
  `fresh-ci-validation-must-bypass-cache-and-preserve-test-file-isolation`,
  `a-validation-gate-needs-a-wall-time-budget-before-it-becomes-the-default`,
  `gate-escape-hatch-must-surface-in-its-own-failure-message`,
  `work-preservation-is-never-grounds-to-bypass-a-hook`.
- **Dependency propagation, publishing, tags** — `l3`, `l11`, `l19`, `l25`,
  `l26`, `l34`, `l77`, `l115`, `l118b`, `l122`, `l147`,
  `a-tag-cutting-ci-must-derive-its-tag-from-the-commit-it-actually-names`,
  `a-successful-release-run-must-not-hide-a-noop-publication`,
  `a-commit-type-prefix-drove-a-semver-bump-that-hid-a-removed-export`,
  `fleet-dependency-campaign-requires-unique-npm-package-providers`,
  `go-consumer-inventory-must-walk-the-full-dependency-graph`,
  `release-evidence-must-inspect-the-same-dependency-sections-as-graph-discovery`,
  `l2026-08-26-1832`, `l2026-08-10-1829`.
- **CI truth and deployment** —
  `deployment-truth-is-terminal-evidence-for-an-exact-commit` (9 occurrences),
  `l144`, `l71`, `l128`, `l78`, `l2026-08-12-0810`,
  `ci-reach-must-model-every-real-job-input`.
- **Agent coordination and recovery** — `l13`, `l86`, `l76`, `l7`, `l51`,
  `a-plan-board-not-re-read…`, `handoff-tuple-is-machine-emitted`,
  `every-failure-state-must-have-a-reachable-terminal-recovery-path`,
  `review-handoffs-must-identify-exact-worktree`, `l2026-08-10-0856`,
  `l2026-08-10-1917`.

The stream verbs MUST ship a `wb-streams` skill through `wb skills sync`, in the
same release artifact, so the agent-facing surface updates with the code.

---
*This document follows the https://specscore.md/feature-specification*
