---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Developer lifecycle metrics and environment diagnostics

**Status:** Draft
**Date:** 2026-07-20
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB turn local developer lifecycle events—commits, push attempts,
formatting, lint, tests, builds, hook outcomes, cache behavior, and CI
results—into privacy-respecting feedback that helps an individual or team find
slow and unreliable feedback loops, diagnose unhealthy development machines,
reduce GitHub Actions minutes, and shorten time to live without turning activity
counts into developer surveillance?

## Context

Git hooks are usually treated as disposable shell scripts. They can block a bad
commit or push, but the useful evidence they produce disappears as soon as the
terminal scrolls away. CI retains better history, but it sees work late, runs in
a standardized environment, spends paid minutes, and cannot explain why the
same build takes 40 seconds on one developer's machine and six minutes on
another.

WB is in a unique position to create a trustworthy local event stream because
it installs the hook shim, detects repository profiles, composes user-owned
language/product blocks, knows the repository and commit, and observes each
block's duration and outcome. That event stream can answer
questions that no individual hook can answer:

- Are local checks getting faster or slower?
- Which repositories or checks create the longest feedback delay?
- Is a developer machine missing a Go, Nx, pnpm, or compiler cache?
- Do OS, architecture, toolchain, disk, or memory differences explain outliers?
- Which checks are flaky locally but stable in CI, or vice versa?
- Are expensive checks running both before push and in CI without benefit?
- How much CI time is avoided because failures are caught locally?
- How long does work take to move from commit, to push, to green CI, to live?

Commit count is useful as an event-volume denominator and personal activity
timeline. It is **not** a measure of productivity, impact, effort, or individual
performance. Pushes are observable only as `pre-push` attempts because Git has
no `post-push` hook that can confirm the remote accepted the update.

## Recommended Direction

Build a local-first, versioned developer-lifecycle event platform inside WB,
with user-owned hook templates as its first producer.

1. **Keep collection local by default.** Append compact JSONL events to the
   user's state directory. Do not record diffs, filenames, command arguments,
   environment secrets, source content, or terminal output. Metrics failures
   must never block a successful commit or push.

2. **Use a stable event envelope.** Every event carries a schema version,
   timestamp, repository slug, hook/action, outcome, duration, commit SHA,
   branch, OS, architecture, and optional user-defined labels. This supports
   future readers and exporters without coupling them to hook implementation.

3. **Separate developer and machine identity.** Cross-developer or
   cross-machine comparison is opt-in. A user can add pseudonymous labels such
   as `developer: dev-17` and `machine: laptop-a`; WB must not silently upload
   usernames, hostnames, email addresses, or hardware serial numbers.

4. **Use detected hook blocks as the first named spans, then generalize.** Go,
   Node, and custom product-profile blocks already give hook work stable names
   and separate timings. A future wrapper such as
   `wb metrics run --name frontend-build -- pnpm nx build app` should record a
   child span with toolchain versions, cache signals, duration, and outcome.
   This makes “the pre-push hook took six minutes” diagnosable as “the frontend
   build missed the Nx cache and took 5m20s.” Templates remain ordinary scripts
   and opt into spans where useful.

5. **Correlate local, CI, and deployment events.** Repository + commit SHA is
   the join key. An optional GitHub importer can add queue, build, test,
   artifact, deploy, and smoke-test timings. WB can then chart the full path
   from local change to production and identify where time-to-live is lost.

6. **Diagnose environments, not rank people.** Compare distributions by
   repository, command profile, toolchain, OS/architecture, and pseudonymous
   machine. Flag actionable outliers: missing caches, version drift, unusually
   slow I/O, repeated dependency downloads, thermal throttling, low disk space,
   or checks that are consistently slower than peers and CI. Never publish a
   developer leaderboard.

7. **Support multiple consumers.** WB should render useful terminal charts and
   emit JSON. Later exporters may feed Backstage, Grafana, an OpenTelemetry
   collector, or a team-owned database. Export requires explicit configuration,
   redaction policy, retention, and visible destination.

## Possible Uses

- **Personal feedback:** daily/weekly commit and push-attempt timeline, local
  failure rate, average hook duration, and the slowest checks.
- **Machine health:** compare one machine with the user's other machines or an
  opt-in team baseline; recommend cache repair, toolchain alignment, disk-space
  cleanup, or environment reprovisioning.
- **Build performance:** compare p50/p95 build, lint, and test times across
  developers, machines, branches, operating systems, and CI runners.
- **Cache effectiveness:** estimate Go build/module, Nx, pnpm, Playwright, and
  container cache hit rates and expose repeated cold-start behavior.
- **Flakiness:** find commands that fail and pass on the same commit or differ
  between local and CI environments.
- **Shift-left/right decisions:** move cheap, reliable checks earlier; move slow
  or low-signal checks out of pre-commit; keep authoritative gates in CI.
- **CI economics:** estimate failures caught before push, avoided CI runs, saved
  Actions minutes, and whether local cost is lower than remote cost.
- **Time to live:** join commit, push, CI, artifact, deployment, and smoke-test
  events into a single trace and optimize the longest stage.
- **Fleet policy:** chart hook installation, template versions, drift, bypass
  gaps, and repositories that lack the expected feedback profile.
- **Capacity planning:** find repositories whose build/test growth is likely to
  exceed the pre-push budget or CI timeout before it becomes an incident.
- **Developer experience:** measure whether an environment or workflow change
  actually reduced waiting and failure recovery time.

## Alternatives Considered

- **Collect only GitHub Actions metrics.** Rejected as the sole source: CI
  cannot see local failures, machine-specific problems, or work stopped before
  push, and it measures standardized runners rather than developer feedback.
- **Upload raw hook logs to a central service.** Rejected: logs can contain
  paths, filenames, code, credentials, customer data, and arbitrary command
  output. The event model must be structured and deliberately sparse.
- **Use commit count as a productivity score.** Explicitly rejected. Commit
  shape varies by task, repository, and developer; optimizing it encourages
  gaming and damages trust. Counts are activity context, not performance
  evaluation.
- **Adopt a third-party analytics SaaS first.** Rejected for the foundation:
  it creates a privacy and availability dependency before the schema and value
  are proven. Exporters can be added later without surrendering local ownership.
- **Keep metrics as ad-hoc text emitted by each template.** Rejected: it cannot
  be compared, versioned, safely aggregated, or joined with CI and deployment
  evidence.

## MVP Scope

- WB-managed shims for user-configured hook templates.
- Local JSONL event schema v1 containing repository, hook/action, outcome,
  duration, commit, branch, OS, architecture, and optional labels.
- Exact successful local commit counts from `post-commit`.
- Push-attempt and pre-push-check counts from `pre-push`, clearly labelled as
  attempts rather than confirmed remote pushes.
- Pre-commit/pre-push outcome plus whole WB-dispatch and detected-profile block
  duration metrics; machine-local shell outside the managed delimiter remains
  unobserved.
- A daily terminal bar chart plus JSON summary, with date and repository
  filters.
- Metrics enabled locally by default once WB hooks are installed; configurable
  path and an explicit off switch.
- Metrics write failures are warnings and never change a hook's successful
  result.

Named command spans outside profile blocks, environment/toolchain snapshots
beyond OS/architecture, CI/deployment import, shared dashboards, and automatic
diagnostics are the next feature slices, not requirements for the first
hook-management release.

## Not Doing (and Why)

- Uploading any event by default—local ownership and trust come first.
- Capturing diffs, filenames, source, command arguments, stdout/stderr,
  credentials, user email, hostname, IP address, or hardware serial numbers.
- Claiming that a successful `pre-push` event proves a remote push succeeded.
- Ranking developers or using activity volume for performance management.
- Replacing CI with local hook evidence; CI remains authoritative and protected.
- Running intrusive hardware benchmarks during hooks; diagnostics should infer
  from real work first and offer an explicit benchmark only when useful.
- Promising exact CI-minute savings before local and CI events can be joined by
  commit and check profile.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | A sparse local event stream can diagnose useful feedback-loop problems without storing code, filenames, logs, or personal identifiers. | Run the event schema against several Go, Nx, and mixed repositories; answer the initial questions using only declared fields and document any field expansion with a privacy review. |
| Must-be-true | Metrics recording is reliable and fast enough that developers do not notice it and a write failure never blocks Git. | Benchmark append latency, simulate unwritable/corrupt state paths, and verify hook exit behavior is unchanged except for a warning. |
| Must-be-true | Commit SHA and repository are sufficient join keys for later local/CI/deployment correlation. | Import a sample Actions run and artifact/deployment provenance for the same SHA; reconstruct one end-to-end timeline without fuzzy matching. |
| Should-be-true | Comparing named build/test spans by pseudonymous machine reveals actionable environment problems such as missing caches or toolchain drift. | Opt in two or more machines, deliberately disable a cache or change a toolchain, and verify the dashboard identifies the outlier and correct remediation. |
| Should-be-true | Developers trust the system when collection is local-first, export is explicit, and commit counts are never treated as productivity. | Conduct a field trial with the exact event payload and dashboard labels visible; collect trust/usability feedback before enabling any shared export. |
| Might-be-true | Local checks measurably reduce GitHub Actions minutes and time to live across the fleet. | After CI correlation exists, compare failure stage, Actions minutes, and commit-to-live duration before and after standardized hooks. |

## SpecScore Integration

- **New Features this would create:** *Hook policy and template management*;
  *Local lifecycle event store*; *Developer metrics CLI charts*; *Named command
  spans*; *Environment diagnostics*; *CI/deployment correlation*; *Opt-in team
  metrics export*; *Backstage developer-feedback dashboard*.
- **Existing Features affected:** `wb hooks` is the initial event producer and
  policy owner; `wb ci audit` can consume adoption/drift and local-vs-CI evidence;
  fleet recipes can distribute versioned policies and templates.
- **Dependencies:** Git hook lifecycle; repository remotes and commit SHAs;
  local XDG-compatible config/state directories; optional future GitHub Actions
  API and Backstage/OpenTelemetry exporters.

## Open Questions

1. Which named span API is easiest to use from arbitrary shell templates while
   preserving stdin, signals, and the wrapped command's exact exit code?
2. Which environment fields are sufficiently diagnostic but non-identifying,
   and which must require explicit labels or consent?
3. Should shared aggregation use an existing OpenTelemetry collector,
   Backstage backend, GitHub artifact, or a WB-specific local/team store?
4. What retention and deletion defaults should apply locally and after an
   explicit team export?
5. How should WB estimate cache hits consistently across Go, Nx, pnpm,
   Playwright, Docker/BuildKit, and other build systems?
6. How should bypasses (`--no-verify`) be represented without pretending WB can
   observe an event that Git deliberately skipped?
7. Which diagnostics may be automated safely, and which should remain
   recommendations requiring a developer to opt in?
