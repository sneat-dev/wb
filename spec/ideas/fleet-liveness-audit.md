---
format: https://specscore.md/idea-specification
status: Draft
---
# Idea: Fleet liveness audit

**Status:** Draft
**Date:** 2026-07-25
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:quality-diff-and-thresholds

## Problem Statement

How might WB scan the local fleet and report, per repository, every place where
a declared mechanism — a validation hook, a CI trigger, a test suite, a
threshold, a vendored copy — exists but is not demonstrably connected to
anything that executes it, without producing a report so noisy that people stop
reading it?

## Context

### The defect class, from one day of evidence

A single session across this fleet found eleven defects that were all one
shape: *the mechanism existed, but nothing checked it was connected.* None was
sloppiness. Each was individually reasonable, and every one was invisible to
the people closest to it.

| # | Defect | What was declared | What was connected |
|---|---|---|---|
| 1 | `dal.BeforeSave` in `dal-go/dalgo` | An exported validation entry point for every write | Nothing in the org called it. Its internal helper was misspelled `beforeSafe` — a typo that survived because the path never executed. It would also have panicked if called, so it could never have worked. |
| 2 | `dalgo2memory` vs `dalgo2firestore` | Both implement the same `dalgo` write surface | Only `dalgo2firestore` validated on insert, so downstream validation was dead in test and alive only in production. Five record-key bugs accumulated behind that asymmetry, blocking every new user's first record for up to eighteen months. |
| 3 | A test case named *"Should fail for non trimmed"* | An assertion that trimming is enforced | `wantErr: false`. The test asserted the bug. |
| 4 | `dalgo_collection_nocompile` | A compile guard | Nothing ever executed it |
| 5 | `chatwright/studio` | Branch protection and "MERGEABLE / CLEAN" | No `pull_request` workflow existed at all, so the status meant only "no git conflict" |
| 6 | `chatwright/runtime-ts` vendored JSON schema | A validation gate against a published schema | The vendored copy drifted in step with the code it guarded, so its test passed while the runtime emitted a field the real schema forbade |
| 7 | `LoopEvent.ProposeError` | A harness-failure signal carried in the run bundle | Discarded by the CLI, so a harness failure was reported as a bot failure |
| 8 | `run.OnProgress` | A progress callback fired every actor iteration | Consumed by nobody, so a multi-minute run printed nothing |
| 9 | `chatwright run` exit code | Non-zero on failed verification | The exit-code decision never consulted the verdict; it exited 0 |
| 10 | `dalgo2mysql` / `dalgo2postgres` suites | Conformance tests | Env-gated with no DSN in CI, so they "passed" by skipping |
| 11 | WB's own layering algorithm | A dependency-layering invariant | Flattening every layer to zero left the entire test suite green: zero coverage |

The last one is the scoping argument. This defect class appeared *in the tool
being built to find it*. Vigilance does not work. A scan does.

### What the current fleet looks like

A read-only scan of the 365 local clones under `~/projects` on 2026-07-25:

| Observation | Count |
|---|---|
| Local clones | 365 |
| Repositories with **no** `.github/workflows` at all | 147 |
| Repositories with workflows but **no** `pull_request` trigger | 49 |
| Repositories with a `pull_request` trigger | 169 |
| Repositories with a Go test file combining `t.Skip*` with `os.Getenv`/`os.LookupEnv` | 27 |

Three of those 27 are third-party forks (`trakhimenok/go`,
`trakhimenok/google-cloud-go`, `trakhimenok/gotreesitter`) contributing 74 of
the matched files, and several counts are inflated by `.worktrees/` and
`.claude/worktrees/` checkouts nested inside a canonical clone. Both are
noise sources the audit must eliminate before it reports anything.

### What WB already does today

This matters more than the new checks. A material part of the brief for this
idea is already implemented, and the audit must extend it rather than duplicate
it.

| Capability | Where | Status against this idea |
|---|---|---|
| Read-only per-repository CI policy audit, fleet mode, `--strict` exit code, `--json` | `internal/ciaudit/audit.go`, `cmd/wb/ci.go` (`wb ci audit`) | **Already exists.** It walks every `.github/workflows/*.y[a]ml` and reads `package.json`, `vitest.config`, `jest.config`. Adding a trigger check is a small extension of an existing parser, not a new scanner. |
| Explicit positive Go coverage threshold required in CI | `ciaudit` finding `go-coverage-threshold` | **Already exists.** Detects `min_test_coverage_percent` and a `go tool cover` comparison against a minimum. |
| Explicit positive frontend coverage threshold required in CI | `ciaudit` finding `frontend-coverage-threshold` | **Already exists.** Detects `minimum-coverage`, `coverage-threshold`, and `coverageThreshold`/`thresholds` in Vitest/Jest config. |
| Build-artifact promotion, deploy-rebuilds-source, missing provenance check, unscoped monorepo CI, duplicate E2E setup | `ciaudit` findings `artifact-*`, `deploy-*`, `monorepo-unscoped-ci`, `duplicate-e2e-setup` | **Already exists.** Same defect family (declared pipeline vs. actual pipeline), already shipped. |
| Statement-weighted Go coverage per repository and per module | `internal/quality/coverage.go` (`wb coverage`) | **Partly.** `profileTotals` aggregates to module totals only; there is no per-package view, so a zero-coverage package is invisible. |
| A `skipped` status vocabulary in verification reports | `internal/quality/verify.go` (`StatusSkipped`) | **Partly.** Only used for "script is not defined" and "no Go modules". Nothing parses `go test` output for `--- SKIP` or `[no test files]`, so a suite that skips every test still reports `passed`. |
| Fleet selection, filters, bounded parallelism, timeouts, retries, resume | `cmd/wb/quality.go` (`qualityTargets`, `runTargets`), `internal/discover` | **Already exists and should be reused verbatim.** `discover.ScanLocal` already excludes linked worktrees at repository level. |
| Dual-audience reports: Markdown default, YAML/JSON, `--report-dir`, `schema_version` | `cmd/wb/quality.go`, `cmd/wb/status.go` | **Already exists.** The audit should adopt this contract unchanged. |
| Fleet-wide Go dependency graph with per-module consumer sets | `internal/deps/graph.go` (`wb deps graph`) | **Already exists.** This is what makes the peer-set check below possible; no other tool in the fleet knows which repositories are peers. |
| Coverage baselines, `--min-coverage`, `--max-drop`, no-regression gates | [quality-diff-and-thresholds](quality-diff-and-thresholds.md) | **Already specified as a separate Draft Idea.** Threshold policy belongs there, not here. |

Two consequences follow directly. First, "missing or absent coverage
thresholds" is **not** a new check — it shipped in `wb ci audit`. Only the
zero-coverage-package half of that candidate survives. Second, the natural home
for a `pull_request`-trigger check is `internal/ciaudit`, whose parser already
holds every workflow file in memory.

One incidental defect of the same class was found in WB itself while gathering
this evidence: `internal/quality/coverage.go`'s `goModules` walk skips only
`.git`, `vendor`, and `node_modules`, so `wb coverage --fleet` descends into
`.worktrees/` and `.claude/worktrees/` and measures agent checkouts as if they
were fleet members. `internal/ciaudit`'s `ignoredDirectory` skips `.worktrees`
but not `.claude`. Both need the same exclusion set as this audit.

## Recommended Direction

Add **`wb audit liveness`**: a read-only fleet scan that reports, per
repository, each declared mechanism WB can see that is not demonstrably
connected to anything that executes it. "Liveness" here means one thing
precisely — *there is evidence that the declared mechanism sits on a path that
actually runs* — and the command's help text must say so in those words,
because "liveness" also means a progress guarantee in distributed systems and a
health probe in Kubernetes. `wb audit wiring` is the standing alternative if
that ambiguity proves costly.

Place it under a new top-level `wb audit` group rather than beside `wb
coverage`/`wb verify`/`wb status`, because the audit answers a policy question
about declarations, not a measurement question about code. `wb ci audit` should
become `wb audit ci`, keeping `wb ci audit` as a permanent cobra alias, so the
two audits sit together and `internal/ciaudit` can be extended in place instead
of being copied. The rename is a one-line `Aliases:` change and is reversible;
if it is judged premature, the fallback is to ship `wb audit liveness`
alongside the existing `wb ci audit` and consolidate later.

The design principle that decides every open question below: **the report must
be drivable to zero.** A finding nobody can act on is noise, and a report that
can never reach zero trains people to ignore it — which is the same failure one
level up. Therefore every check is graded by confidence, every finding names
the repository, the file, and the concrete next action, and every accepted
exception is recorded in a `.wb/liveness-allow.yaml` entry that **requires a
written reason**. A suppression with a reason is a decision; a suppression
without one is the same silence this idea exists to break.

### Checks the MVP keeps

Each check below is stated with the reason it survives a signal-to-noise test
against the fleet numbers above.

#### 1. `ci-absent` / `pr-ci-absent` — a PR status that means nothing

A repository containing source WB recognises (a `go.mod`, or a `package.json`
with a test-capable toolchain) that has no workflows at all is `ci-absent`. A
repository whose workflows exist but where none triggers on `pull_request` or
`pull_request_target` is `pr-ci-absent`.

*Signal-to-noise:* the trigger is a literal YAML key, so precision is close to
exact — this is the cheapest high-confidence check available. The noise lives
entirely in the denominator: 147 of 365 clones have no workflows, and most are
landing pages, docs sites, third-party forks, or archives. The check is only
usable with three exclusions applied first: forks, archived repositories, and
nested `.worktrees/`/`.claude/worktrees/` checkouts. Fork and archive status
come from `discover.Repo.IsFork`/`Archived`, which `wb sync` already populates
from `gh repo list`; a fully offline fallback compares the `origin` remote's
owner with the containing directory's org name.

*Actionability:* "`sneat-co/ext-splitus` has one workflow, `publish.yml`, and it
is not PR-triggered; a PR against this repository runs no checks at all."

#### 2. `test-suite-never-runs-in-ci` — a green run where nothing ran

A test file that calls `t.Skip`/`t.Skipf`/`t.SkipNow` under a condition derived
from `os.Getenv`/`os.LookupEnv`, where the named environment variable is set by
no workflow in the repository and referenced by no `secrets.` expression.

*Signal-to-noise:* "there are env-gated skips" is worthless — 27 repositories
have them and most are deliberate, such as `actor/anthropic/live_test.go`
gating on an API key that should never run in CI. The join that creates the
signal is **naming the variable and checking whether CI sets it**. That turns a
vague smell into a sentence a maintainer can act on. Findings split into
`test-suite-never-runs-in-ci` (variable set nowhere — the `dalgo2mysql` /
`dalgo2postgres` case) and an informational `test-suite-env-gated` (variable
set by at least one workflow, listed for context, not counted as a failure).

*Companion change, outside this command:* `wb verify` must stop reporting
`passed` for a suite in which everything skipped. `internal/quality` should run
`go test -json`, count `Action: "skip"` and `[no test files]`, and surface
"43 passed, 12 skipped" using the `StatusSkipped` vocabulary it already has.
Distinguishing skipped from passed is the whole point of this check, and it
belongs where the tests are actually run.

#### 3. `guard-unreferenced` — a declared guard nothing reaches

An exported **package-level function** whose name matches a guard vocabulary
(`BeforeSave`, `AfterLoad`, `Validate*`, `Verify*`, `Enforce*`, `Check*`,
`Assert*`, `Guard*`, `Must*`, `Ensure*`) with no **identifier reference** in
non-test, non-generated Go source anywhere in the fleet.

*Signal-to-noise:* this is the check that most needs discipline, and the fleet
numbers show why. Unrestricted across `dal-go` alone there are 262 exported
package-level functions, of which 103 are referenced only from tests — a 39%
"finding" rate that is pure noise, dominated by generated mocks and by a
published library's public API whose consumers live outside the fleet. Applying
the guard vocabulary and excluding files carrying the
`// Code generated … DO NOT EDIT.` line reduces `dal-go`, `chatwright`,
`specscore`, and `ingitdb` combined to 52 candidates. Those grade into:

- **A — `guard-unreferenced`** (0 today, because `dal.BeforeSave` was fixed
  this week): no reference anywhere. High confidence, always report.
- **B — `guard-test-only`** (15): referenced only from tests. Report for review,
  never as a failure — for a published library this is often correct.
  `specscore/specscore-cli`'s `CheckRegistryParity`, a rule-registry parity
  check that nothing but a test invokes, is a genuine hit inside this class.
- **C — `guard-external-only`** (8): no non-test caller in its own repository,
  callers only in sibling repositories. Informational, and the most interesting
  class — `ingitdb/dalgo2ingitdb`'s `ValidateWrite`/`ValidateDelete` are
  referenced only from `ingitdb/dalgo2ingitdb4local`, which is exactly the
  adapter-asymmetry shape of defect #2.

Two precision requirements are non-negotiable. **Count identifier references,
not call sites:** requiring `(` after the name would have reported
`specscore-cli`'s registry-dispatched checks as dead, and function-value
registration is the dominant Go idiom for exactly this kind of pluggable guard.
**Exclude methods entirely:** a method can be invoked through an interface
without its receiver ever being named, and any single `.Validate(` anywhere in
the fleet marks every `Validate` method live, so a textual method scan is a
false-negative machine. Deciding method liveness correctly needs `go/types` and
a build of every repository, which the scan constraint forbids.

#### 4. `declared-copy-without-drift-gate` — a vendored copy nobody compares

Only files the repository itself **declares** as copies: a
`wb:vendored-from <url-or-fleet-path>` marker in a comment, or an entry in a
`.wb/vendored.yaml` manifest. The finding fires when no workflow step references
that path or marker, i.e. nothing compares the copy with its source.

*Signal-to-noise:* the naive form of this check — "the same file content in
several repositories" — was measured and is unusable. Hashing a curated
extension set across the fleet produced **800 cross-repository duplicate
groups**, essentially all of them from two forks of `microsoft/winget-pkgs`.
Even after excluding forks, content identity cannot distinguish a vendored copy
from a coincidence, and the check would need a hand-maintained ignore list that
grows faster than the findings. Declaration-driven detection has zero noise by
construction: nothing declared, nothing reported. The cost is adoption, and
that is an honest trade recorded below.

One zero-configuration subset is precise enough to include: when a file's
content also exists in a **fleet-local Go module that this repository already
declares as a dependency**, `wb deps graph` has proved the relationship, so the
pair is not a coincidence. That subset covers the `runtime-ts` schema case
within the ecosystem WB can see.

#### 5. `package-zero-coverage` — an untested package with real logic

A Go package whose coverage profile shows N or more statements, all uncovered,
and which contains no `_test.go` file.

*Signal-to-noise:* the missing-threshold half of the original candidate is
dropped outright — `wb ci audit` already implements it for both Go and
frontend, and coverage budgets are already specified in
[quality-diff-and-thresholds](quality-diff-and-thresholds.md). What survives is
the case that produced defect #11: WB's own layering algorithm had zero
coverage while the whole suite was green, and no repository-level percentage
could have shown it. The statement-count floor is what keeps it actionable —
without it, every `doc.go` and generated stub is a finding.

*Dependency:* this check reads a coverage profile; it does not run tests. The
audit consumes an existing `coverage.yaml` and reports the check as
`unavailable` when there is none, rather than quietly shelling out to
`go test`. Producing per-package totals is a small extension of
`profileTotals`, which already parses the file paths it currently discards.

#### 6. `peer-adapter-asymmetry` — the odd one out in a declared peer set

For a module M with two or more fleet-local consumers, report a guard or entry
point exported by M that some consumers reference and others do not, and report
peers whose CI differs materially — one runs its conformance suite, another
env-gates it away.

*Signal-to-noise:* this is the highest-signal check in the list and the one
that most justifies putting the audit in WB at all, because **only WB sees the
peer set**. `wb deps graph` already computes the consumers of every module, so
the denominator is a set the fleet declares rather than one the audit guesses.
"The odd one out among eleven `dalgo2*` adapters" is a fundamentally stronger
statement than "no callers", and the finding names both the peer that does it
and the peer that does not, which is as actionable as a finding gets. It is
also the direct generalisation of two of the eleven defects: #2
(`dalgo2memory` did not validate while `dalgo2firestore` did) and #10
(`dalgo2mysql`/`dalgo2postgres` conformance suites skipped while their siblings
ran). The narrowed guard scan already surfaced the shape live in class C above.

## Possible Uses

### Weekly scheduled fleet report

The natural end state is a schedule, not a hand-run command:

```sh
wb audit liveness --fleet --format yaml --report-dir ~/reports/liveness --strict
```

Stable finding codes, a `schema_version`, and a non-zero exit make the run
diffable week over week. The interesting artefact is not the report but its
delta: a finding that appeared this week names the commit that disconnected
something.

### Preflight before trusting a merge

Before relying on "MERGEABLE / CLEAN" for a repository, confirm the status
means something:

```sh
wb audit liveness ~/projects/chatwright/studio --checks ci
```

This is defect #5 caught before, rather than after, a merge.

### Reviewing one adapter family

After adding a new `dalgo2*` backend, compare it with its declared peers:

```sh
wb audit liveness --fleet --match 'dal-go/*' --checks peers,guards
```

The output answers "what do the other ten adapters call that this one does
not?" — the question nobody thought to ask for eighteen months.

### Agent-driven triage

An AI agent reads the YAML, opens one PR per `pr-ci-absent` repository, and
leaves classes B and C for a human, because those need a judgement about
whether an external consumer exists.

## Alternatives Considered

- **Extend `wb ci audit` with all six checks.** Rejected as the primary shape.
  Three of the six (guards, peers, zero-coverage packages) are not CI policy at
  all, and `ciaudit.Audit` takes a single root path with no fleet-wide index,
  so the cross-repository checks have nowhere to live. The CI-shaped checks
  should still be implemented *inside* `internal/ciaudit`, whose workflow parser
  already exists.
- **A per-repository linter, distributed to each repo's CI.** Rejected because
  the defining property of this defect class is that it is invisible from
  inside one repository. `dal.BeforeSave` looked fine in `dalgo`; only the
  absence of callers across 365 clones made it a defect. A per-repo linter
  cannot see the peer set or the fleet-wide reference universe.
- **Type-aware analysis with `go/packages` and `NeedTypes`.** Rejected: it
  requires building every repository, which the scan constraint forbids, is
  minutes per module at fleet scale, and fails outright on private modules
  without `GOPRIVATE` and a PAT — a machine that can build the whole fleet is
  not a machine this audit can assume.
- **Query GitHub for branch protection and required checks.** Deferred, not
  rejected. This is where "CLEAN means nothing" really lives, and WB already
  shells `gh`. But it is one API call per repository over 365 repositories,
  which is a different cost class from a local scan. It belongs behind an
  explicit `--online` flag in a later slice.

## MVP Scope

One job: **produce a fleet report that a maintainer can drive to zero.**

- `wb audit liveness [repository-path]` with `--fleet`, reusing
  `qualityTargets` for `--filter`/`--match`/`--regex`/`--parallel` unchanged.
- Checks 1, 2, and 3 only — the three that need nothing but a file walk. Check
  6 needs the dependency graph, check 5 needs a coverage profile, and check 4
  needs a declaration convention that does not exist yet; all three are the
  second slice.
- Findings carry `code`, `repository`, `file`, `line` where known, a one-line
  `fix`, and a `confidence` of `high`/`review`/`context`.
- Markdown by default; `--format yaml|json`; `--report-dir` writing
  `audit-liveness.md` and `audit-liveness.yaml` with a `schema_version`.
- `--strict` exits non-zero on `high`-confidence findings only.
- `.wb/liveness-allow.yaml` suppressions, each requiring a non-empty reason;
  the audit fails on a suppression without one.
- A shared exclusion set — `.git`, `vendor`, `node_modules`, `.worktrees`,
  `.claude`, `dist`, `coverage` — applied here and backported to
  `internal/quality`'s `goModules` and `internal/ciaudit`'s `ignoredDirectory`,
  both of which currently miss part of it.

Success is measured on the fleet, not in unit tests: the first run must produce
a list a maintainer reads to the end, and the second run after the obvious
fixes must be materially shorter.

## Not Doing (and Why)

- **Full mutation testing** — the honest answer to defect #3, a test that
  asserts the bug, is mutation testing, and nothing in this scan can catch it.
  But a mutant needs a compile-and-test cycle each, which is the wrong cost
  class for 365 repositories. It belongs in per-repository CI as an opt-in job.
- **Anything that builds a repository** — no `go build`, no `go/packages` with
  type information, no `pnpm install`. This is what forces check 3 to be
  textual and package-level-only, and it is a deliberate accuracy sacrifice
  taken to keep the scan usable.
- **Cloning, fetching, or network calls beyond what WB already does** — the
  audit reads local clones. Fork and archive metadata may come from the
  `gh repo list` call `wb sync` already makes; nothing else is permitted.
- **Naive duplicate-content detection** — measured at 800 cross-repository
  duplicate groups, essentially all fork noise. Replaced by declaration-driven
  detection.
- **Method-level dead-code detection** — interface satisfaction means a method
  can be called without ever being named, and shared method names make every
  such symbol look live. A textual scan produces confident wrong answers.
- **Coverage thresholds and no-regression gates** — already shipped in
  `wb ci audit` and already specified in
  [quality-diff-and-thresholds](quality-diff-and-thresholds.md). Duplicating
  policy configuration in a second command is how two sources of truth start.
- **Reporting every repository that has no workflow** — 147 of 365, mostly
  landings, forks, and archives. Without the source/fork/archive preconditions
  this single check would bury the other five.
- **Automatic fixes** — the audit is read-only. Opening the PR that adds a
  `pull_request` trigger is `wb migrate`'s job, and should stay a separate,
  deliberate command.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | The first fleet run produces a list short enough to be read to the end. | Run checks 1–3 across all 365 clones and count findings by confidence. If `high` exceeds roughly one per repository, tighten preconditions before shipping. |
| Must-be-true | The guard vocabulary plus generated-file exclusion keeps class A precise enough to act on without review. | Replay the scan against the state of `dal-go/dalgo` before this week's fixes and confirm `dal.BeforeSave` is reported as class A with no class-A false positives beside it. |
| Must-be-true | Fork, archive, and nested-worktree exclusion can be decided offline with acceptable accuracy. | Compare the offline `origin`-owner heuristic against `gh repo list` metadata for all 365 clones and measure disagreements. |
| Should-be-true | Naming the gating environment variable makes check 2 actionable rather than merely suggestive. | Take the 27 repositories with env-gated skips, produce the variable-to-workflow join, and check that each finding states a fix a maintainer would accept. |
| Should-be-true | The peer set from `wb deps graph` is a better denominator than any name-based grouping. | Compare graph-derived peers of `dal-go/dalgo` with the `dalgo2*` naming convention and record where they diverge. |
| Might-be-true | A `wb:vendored-from` marker will actually be adopted, making check 4 worth building. | Add the marker to `runtime-ts`'s vendored schema and one other known copy; see whether it survives three months and one refactor. |
| Might-be-true | A weekly scheduled run stays at zero once driven there, rather than drifting back. | Schedule it for a quarter and measure both the finding count and the number of suppressions added. |

## SpecScore Integration

- **New Features this would create:** `fleet-liveness-audit`, covering the
  `wb audit` command group, the finding contract, and the suppression file. A
  second, smaller Feature or a change request against
  [Fleet Quality](../features/fleet-quality/README.md) covers the
  skipped-versus-passed reporting change in `internal/quality`.
- **Existing Features affected:**
  [Fleet Quality](../features/fleet-quality/README.md) supplies target
  selection, report shape, and the coverage profile the zero-coverage check
  reads, and gains the skipped/passed distinction;
  [Dependency Graph](../features/dependency-graph/README.md) supplies the peer
  sets and the fleet-local dependency relationships;
  [Fleet Status](../features/fleet-status/README.md) is the precedent for a
  fleet-first read-only command. `wb ci audit` is extended in place and
  re-homed under `wb audit ci` with an alias.
- **Dependencies:** local clones under `--projects-root`; `internal/ciaudit`'s
  workflow parser; `internal/deps` graph output for checks 4 and 6; a
  `wb coverage` report for check 5; optional `gh repo list` metadata for fork
  and archive exclusion.

## Open Questions

1. Should the guard vocabulary be a fleet-wide default, per-repository
   configuration in `.wb/liveness.yaml`, or replaced entirely by a `// wb:guard`
   marker so that precision comes from the code rather than from a name list?
   A marker is exact but needs adoption; a name list works on day one but will
   always be approximate.
2. Is "referenced only from tests" (class B) a finding at all for a published
   library whose consumers are outside the fleet, or only for a repository the
   graph shows has no external consumers?
3. Does a `pull_request`-triggered workflow whose `paths:` filter excludes the
   change under review count as present? WB's own `go-ci.yml` is PR-triggered
   but path-filtered to Go files, so a spec-only PR runs no Go job at all. This
   is the same "the status means nothing" failure one level down, and it is not
   obvious whether reporting it would be signal or noise.
4. Should fork and archive exclusion require the cached `gh repo list`
   metadata, or must the scan work fully offline on the `origin`-owner
   heuristic alone, accepting the misclassifications that follow?
5. Where does check 5 get its coverage profile — the newest `coverage.yaml` in
   `--report-dir`, an explicit `--coverage-report` path, or a documented
   two-command sequence? Each choice trades freshness against the no-build
   constraint.
6. What is the right peer-set definition for check 6: every fleet-local
   consumer of a module, or only those whose module path matches a sibling
   naming pattern such as `dalgo2*`? The naming pattern is a proxy for a
   declared interface set that the dependency graph does not currently model.
7. Is there any precise formulation of "the test asserts the bug" — defect #3's
   `wantErr: false` under a case named "Should fail for non trimmed"? Table
   expectation fields have no standard name (`wantErr`, `expectError`,
   `shouldFail`, `err`), and mapping a case name to intent is semantic. Every
   heuristic considered was either repo-specific or noisy, so it is left
   unanswered here rather than guessed.
8. Should `wb ci audit` move to `wb audit ci` now, keeping the old path as a
   permanent alias, or should the two audits stay separate until the liveness
   command has proved itself on the fleet?

---
*This document follows the https://specscore.md/idea-specification*
