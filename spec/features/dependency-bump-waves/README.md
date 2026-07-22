---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Dependency Bump Waves

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-bump-waves?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-bump-waves?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-bump-waves?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-bump-waves?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb deps bump` recalculates a dependency graph and propagates newly released
versions through local repository worktrees in provider-first waves. It runs
language-owned manifest updates and verification, optionally publishes review
branches, waits for CI, discovers released versions, and requeues dependants
until a fresh graph scan reaches a fixpoint.

`wb deps set go <module>@<version> --propagate` is a convenience spelling for
this command with one exact `--changed` event. Exact target selection belongs
to `set`; release-event propagation belongs only to this wave engine.

## Problem

A one-pass dependency update uses only the releases available when each
repository is visited. During a cross-repository migration, leaf releases
appear later, reveal previously hidden transitive blockers, and change the
versions that consumers should select. Without graph recalculation, temporary
worktree replacements leak into branches, consumers preserve stale versions
without explanation, and an operator or AI agent must manually remember which
repositories need a second sweep.

## Behavior

### Inputs and planning

#### REQ: changed-release-input

`wb deps bump` MUST accept one or more `--changed <dependency>@<version>`
events and MUST be able to consume the deterministic output of
`wb deps drift`. It MUST record whether each starting version came from an
explicit event, a drift report, or online discovery.

The initial CLI form MUST be
`wb deps bump go --changed <module>@<version> [--changed ...] --fleet`.
`deps set --propagate` MUST invoke the same planner and report format rather
than implementing a parallel wave loop.

#### REQ: scoped-repository-discovery

The command MUST operate only on repositories selected below the configured
GitHub directory. Organization and path filters MUST apply before cloning or
creating worktrees. Repository clones MUST use `<github-dir>/<org>/<repo>`;
missing selected repositories MAY be cloned when cloning is enabled.

#### REQ: provider-first-recalculated-waves

WB MUST process providers before their dependants. After a provider release is
available, it MUST rebuild the dependency graph, compare observed versions
with the prior wave, and requeue every affected direct or transitive dependant.
Processing MUST repeat until a complete recalculation yields no manifest
change, no campaign replacement, and no unresolved dependency blocker.

When a direct consumer is already current, WB MUST advance through it only
after registry evidence proves that a published consumer module selects the
event versions. A current source manifest by itself is insufficient. This
allows a second campaign sweep to reach stale transitive dependants without
inventing a release.

#### REQ: bounded-wave-parallelism

Independent repositories in one wave MAY execute concurrently up to
`--parallel`. A dependant MUST NOT publish before all changed providers it
uses have published versions. While remote CI runs, WB SHOULD continue local
work for repositories whose provider releases are already available.

Relevant cross-repository dependency cycles MUST be detected before mutation
and rejected with the cycle path until a coordinated cyclic-release protocol
is available. `--max-waves` MUST independently bound release churn and graph
changes that are not structural cycles.

#### REQ: shared-typed-lifecycle-engine

Exact set and bump waves MUST use one typed repository lifecycle engine for
clone/fetch, worktrees, verification, commit, push, PR creation, CI waiting,
merge, resume validation, locks, and completion of independent results.
Mutation metadata MUST remain adapter-typed so exact-set decisions and wave
events do not depend on unstructured `any` values. The planner chooses
independent execution or dependency waves without duplicating lifecycle code.

### Safe updates and verification

#### REQ: isolated-dirty-checkouts

WB MUST leave canonical clones untouched and create dedicated worktrees from
the configured base ref, including when a canonical checkout is dirty. Resume
MUST validate and reuse only the expected branch and worktree.

#### REQ: adapter-owned-version-update

Language adapters MUST own manifest and lock-file mutation. The Go adapter
MUST use Go tooling to apply requested versions, upgrade compatible package
dependencies, run `go mod tidy`, and inspect the resulting selected build
list; it MUST NOT implement its own `go.mod` parser or dependency solver.

#### REQ: default-verification

Verification MUST be enabled by default. The Go adapter MUST run the
configured lint, test, vet, and build checks with per-command timeouts. A user
MAY explicitly override or disable checks. No commit, push, pull request, or
merge may occur after a failed required local check.

#### REQ: publishable-manifests

Local provider worktree replacements MAY be used while a wave is being
prepared. Before a branch is committed or pushed, WB MUST replace every
campaign worktree link with a published version, tidy again, rerun required
verification, and fail if any unrelated or campaign replacement remains.

### Publication, recovery, and audit

#### REQ: explicit-publication-flags

By default the command MUST make local worktree changes only. `--commit`,
`--push`, `--pr`, and `--merge` MUST explicitly enable their corresponding
publication stages. `--merge` MUST wait for required CI and MUST NOT merge a
failed, pending, conflicted, or externally blocked pull request.

#### REQ: release-event-propagation

After a successful merge, WB MUST determine the provider's published module
version from configured release metadata or an observed tag. It MUST NOT
invent a version. The observed release becomes a dependency event for the
next recalculated wave.

WB MUST capture the latest observed provider version before merging a wave and
MUST require a newer published version afterward before advancing dependants.
If no release appears before `--timeout`, the report MUST use
`awaiting_release`, retain the before version and attempted source, leave
downstream repositories untouched, and allow `--resume` to continue.

#### REQ: resumable-failures

Every wave and repository state MUST be persisted before external actions.
`--resume` MUST continue pending or failed work without repeating successful
commits, pushes, merges, or tags. `--retry` MUST retry only eligible failed
commands or CI observations and MUST preserve the prior attempts in the audit.

#### REQ: dependency-decision-audit

Markdown and YAML reports MUST record, for every dependency check, the phase,
repository, manifest path, version before the check, target or latest version
observed, version after the check, replacement before and after, action,
timestamp, and explicit reason. A preserved manifest MUST say why and at which
version it was checked; a failed upgrade MUST include the attempted version
and sanitized error.

## Synthetic Use Cases

### UC: breaking record error rename propagates to a fleet

The fictional `data/record` module renames `record.ErrRecordNotFound` to
`record.ErrNotFound` without an alias and publishes `v0.2.0`. `data/dal`, two
storage adapters, `acme/facade`, and `acme/api` form successive dependency
layers. The operator runs:

```text
wb deps bump go --fleet \
  --changed example.org/data/record@v0.2.0 \
  --parallel 2 --commit --push --pr --merge
```

WB updates independent adapters concurrently, waits for their releases,
recalculates the graph, requeues the facade and API, and stops only after a
fresh drift scan finds no old record selector or version.

### UC: dirty canonical clone remains untouched

The fictional `acme/renderer` checkout contains an unfinished local feature
on a non-default branch. A transitive SDK release requires a manifest bump.
WB creates a campaign worktree from `origin/main`, updates and verifies that
worktree, and leaves the feature checkout, its branch, and its untracked files
unchanged.

### UC: CI failure pauses only dependent publication

The fictional `acme/sql-adapter` and `acme/file-adapter` are independent. SQL
CI fails while file CI passes and publishes `v0.4.1`; the API depends on both.
WB continues unrelated local work, records SQL's failed check, does not publish
the API, and exits with a resumable campaign. After SQL is fixed, `--resume`
reuses the successful file release and continues from SQL without duplicating
the earlier pull request or tag.

### UC: private dependency cannot be queried

The fictional `acme/payments` module requires a private fraud SDK at `v1.3.0`.
Version discovery lacks registry authentication. WB records the checked
version, attempted source, and sanitized error, preserves the manifest for an
explicit reason, and does not claim that `v1.3.0` is latest. After credentials
are configured, `--retry` adds a new attempt and retains the failed audit row.

### UC: exact set delegates to the wave engine

The fictional `data/record v0.2.0` release is already chosen. The operator runs:

```text
wb deps set go example.org/data/record@v0.2.0 --fleet --propagate --merge
```

WB records the exact release as the sole initial bump event, then uses the same
provider-first graph, worktrees, release observations, reports, and resume state
as `wb deps bump go --changed example.org/data/record@v0.2.0 --fleet --merge`.
No exact-set-specific propagation loop exists.

### UC: second sweep crosses an already released adapter

The fictional adapter's `origin/main` already requires
`data/record v0.2.0`, and registry release `adapter v0.7.1` contains the same
requirement, but a facade still requires `adapter v0.6.0`. Rerunning the record
bump downloads the published adapter `go.mod`, records both pieces of evidence,
turns `adapter v0.7.1` into the next event, and plans the facade update. It does
not skip the facade merely because the direct adapter needs no new source edit.

## Interaction with Other Features

[Dependency Drift](../dependency-drift/README.md) supplies convergence evidence
before and after a bump campaign. [Hierarchical Migration
Campaigns](../hierarchical-migration-campaigns/README.md) uses the same
worktree, verification, publication, and reporting primitives for source
migrations that also trigger dependency waves.

## Acceptance Criteria

### AC: released-provider-requeues-dependants

**Requirements:** dependency-bump-waves#req:changed-release-input, dependency-bump-waves#req:provider-first-recalculated-waves, dependency-bump-waves#req:bounded-wave-parallelism, dependency-bump-waves#req:shared-typed-lifecycle-engine, dependency-bump-waves#req:release-event-propagation

**Given** a fictional provider release has two independent adapters and a
consumer that depends on both
**When** a bump campaign publishes the provider and adapters
**Then** WB recalculates the graph after each release, requeues the consumer
with published versions, and reaches a fixpoint without a temporary replace.

### AC: dirty-clone-and-red-ci-are-safe

**Requirements:** dependency-bump-waves#req:isolated-dirty-checkouts, dependency-bump-waves#req:default-verification, dependency-bump-waves#req:publishable-manifests, dependency-bump-waves#req:explicit-publication-flags

**Given** one selected canonical clone is dirty and one provider's required CI
fails
**When** a publishing bump campaign runs
**Then** canonical work is unchanged, local verification occurs in a dedicated
worktree, no replacement is published, and no failed provider or dependant is
merged.

### AC: resume-preserves-auditable-decisions

**Requirements:** dependency-bump-waves#req:adapter-owned-version-update, dependency-bump-waves#req:resumable-failures, dependency-bump-waves#req:dependency-decision-audit

**Given** a private version query fails after an earlier repository has already
published successfully
**When** credentials are fixed and the operator runs `--resume --retry=1`
**Then** WB does not duplicate successful external actions, retries the failed
query once, and preserves both attempts with checked versions, timestamps,
actions, and reasons in Markdown and YAML.

## Open Questions

- Should release discovery initially require explicit module-to-tag metadata,
  or may adapters infer conventional prefixes when exactly one is unambiguous?
- Should a bump campaign automatically run a final `wb deps drift`, or expose
  that as an explicit but recommended pipeline step?

---
*This document follows the https://specscore.md/feature-specification*
