---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Hierarchical Migration Campaigns

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/hierarchical-migration-campaigns?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/hierarchical-migration-campaigns?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/hierarchical-migration-campaigns?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/hierarchical-migration-campaigns?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

WB migrates a dependency hierarchy through dedicated local worktrees, then can
commit, publish, review, and merge the resulting repository branches. A
versioned HCL specification describes language-neutral source operations while
language adapters own structural edits and package-manifest transitions.

## Problem

A repository-level source rewrite is insufficient when its changed dependency
is consumed by multiple repositories. Editing canonical clones risks a dirty
developer checkout; committing local `replace` directives makes a pull request
unverifiable in remote CI; and an ambiguous dry-run report can mislead a human
or AI reviewer about the actual diff.

## Behavior

### Campaign isolation and lifecycle

#### REQ: canonical-clones-untouched

An apply campaign MUST clone missing repositories to
`<github-dir>/<org>/<repo>` and MUST create or resume a dedicated worktree at
`<github-dir>/.wb/worktrees/<migration>/<org>/<repo>`. It MUST NOT check out,
reset, or edit the canonical clone, including when that clone is dirty.

#### REQ: recoverable-resume

`--resume` MUST reuse only an existing worktree on the expected campaign
branch. It MUST preserve and idempotently continue uncommitted changes left by
a partial campaign or manual migration fix, and MUST include those changes when
deciding which modules require verification. A missing or differently branched
worktree MUST fail without replacing it.

#### REQ: evolving-resume-graph

When the root campaign worktree already exists, `--resume` MUST rediscover the
Go module graph from that validated worktree rather than the original clone.
Dependencies introduced by manual fixes or prerequisite branch integration
MUST join the campaign on the next run and receive their own isolated
worktrees, migrations, manifest replacements, and verification. Apply/resume
MUST let official Go tooling repair incomplete `go.mod`/`go.sum` metadata
before inspecting that evolving graph.

#### REQ: campaign-lock

An apply campaign MUST hold one exclusive migration lock below its dedicated
worktree root for both planning and application. A concurrent or interrupted
lock MUST cause a safe failure and MUST NOT be silently overwritten.

#### REQ: narrow-cleanup

`--cleanup` MUST remove only clean, dedicated worktrees for the named
migration. It MUST NOT remove canonical clones, local branches, reports, or a
worktree with uncommitted changes.

### Planning and reports

#### REQ: pruned-go-graph-discovery

WB MUST augment the root command's pruned `go mod graph` with direct
requirements read from the selected modules' own `go.mod` files. A transitive
adapter that directly requires a migration target MUST be included even when
Go omits that adapter's outgoing edges from the root graph view.

#### REQ: deterministic-operation-order

The migration format MUST execute HCL operations in this stable phase order:
`text_replace`, `import_replace`, `selector_rewrite`, then
`selector_rename`, then `composite_field_rename`; manifest edits occur only in
a hierarchical campaign after source edits. Repeated blocks with the same
language label remain valid and preserve their source order within a phase.

#### REQ: syntax-safe-composite-field-rename

The Go adapter MUST limit `composite_field_rename` to identifier keys in
explicitly typed named composite literals. It MUST NOT rewrite maps, arrays,
slices, elided literals, strings, comments, field declarations, or ordinary
identifier references.

#### REQ: deferred-dry-run-results

A hierarchy dry run MUST NOT claim a numeric changed-file count when no
worktree was created and no source plan was evaluated. Its Markdown report
MUST say the count is unknown and its YAML report MUST expose a deferred plan
state with no numeric count.

#### REQ: linked-review-index

Every campaign report MUST provide Markdown links to its isolated worktrees
and per-module migration reports, together with a deterministic YAML index.
Detailed code diffs remain available through Git rather than duplicated in
the report.

#### REQ: cumulative-resume-change-index

After an apply, every repository report MUST deterministically list the
repository-relative paths that differ from its configured remote base ref,
including committed, staged, unstaged, and untracked files. `--resume` MUST
retain this cumulative review surface even when the latest mechanical pass is
idempotent; per-module plan counts MAY describe only the latest pass.

#### REQ: auditable-go-dependency-decisions

For every Go module requirement inspected during manifest normalization, the
campaign YAML and Markdown reports MUST record the dependency path, its
required version at the time of the check, any configured target version, the
resulting version, and separate version and replacement actions. When WB does
not update a dependency, the report MUST state why, including whether it was
already at the configured target or WB preserved it because the migration did
not configure a target version. Publication reporting MUST likewise explain
the removal of temporary campaign worktree replacements.

#### REQ: precise-review-rules

A migration review rule MAY define an optional line-scoped exclusion pattern.
WB MUST suppress only matches whose source line satisfies that exclusion; an
already-correct form elsewhere in the file MUST NOT hide a remaining semantic
review item.

### Publishable Go branches

#### REQ: local-replace-for-verification

While a local campaign is being verified, direct Go dependencies in the same
campaign MAY use relative `go.mod replace` directives pointing at their
dedicated provider worktrees. WB MUST NOT create or rely on a shared
`go.work` file.

#### REQ: released-before-pr

Before `--pr` or `--merge` commits a changed consumer branch, WB MUST remove
each temporary campaign replacement and require an explicit
`go_module_release` version for that module. It MUST update the requirement to
that published version, run `go mod tidy`, and rerun the selected local
verification. A missing release declaration or an unrelated replacement MUST
fail before that repository is committed, pushed, or submitted for review.

The publishability preflight MUST reject every local filesystem replacement
that does not point to the expected worktree of a module in the current
campaign, even when that replacement was preserved from an earlier resume.

#### REQ: dependency-layers-and-parallelism

WB MUST process provider dependency layers before consumer layers. Independent
repositories MAY run up to `--parallel` concurrently. When `--pr` is set, WB
MUST continue ready local work while already-opened pull requests run remote
CI; `--merge` is a final phase that requires every campaign PR's required
checks to pass before any merge is attempted.

Release preflight MUST run immediately before each dependency layer. A missing
release in a later layer MUST NOT prevent a ready earlier layer from being
published, and WB MUST stop before modifying the blocked layer so `--resume`
can continue after the release handoff.

Within one dependency layer, a missing release for one independent repository
MUST NOT prevent release-ready peers from being published. WB MUST leave the
blocked repository unchanged, publish the ready peers, and then report the
remaining blockers so `--resume` can continue. A strongly connected repository
component MUST remain atomic: if any member fails release preflight, no member
of that cyclic component may be modified or published.

A repository containing only provider modules MUST NOT be committed, pushed,
or submitted for review by the campaign, even when its worktree is dirty. WB
MUST preserve such provider-only changes.

#### REQ: human-readable-change-titles

When a migration declares a human-readable title, WB MUST use that title for
generated commit and pull-request subjects. The stable migration ID remains in
branch names, reports, and PR metadata; it MUST NOT be presented as a product
or API version. If the title is absent, WB MAY fall back to the migration ID.

#### REQ: open-pr-retry

When `--pr` retries or resumes a published campaign branch, WB MUST reuse an
existing open pull request with the same head and base branches. It MUST NOT
reuse a closed or merged pull request. If the branch contains later campaign
commits after an earlier pull request was merged, WB MUST open a new pull
request for those commits.

For a local apply campaign without commit or publishing flags, verification
failures MUST be collected deterministically and MUST NOT prevent later
dependency layers from being verified. WB MUST return the collected failures
after all layers have run. A campaign that can commit, push, open PRs, or merge
MUST remain fail-fast before publishing dependent repositories.

Go module graphs MAY contain repository dependency cycles. WB MUST collapse
each strongly connected repository group into one deterministic processing
layer instead of rejecting the campaign. All worktrees MUST be prepared before
repositories in that layer are migrated, so their local replacements are
available during verification.

When changed Go modules in one strongly connected component do not yet have
declared peer releases, `--pr` MUST bootstrap the cycle without publishing a
local replacement. WB MUST commit and push an intermediate seed for every
repository in the component, derive valid Go pseudo-versions for the exact
seed commits, finalize each review branch against those peer pseudo-versions,
rerun verification, and open the component PRs. WB MUST then stop before
modifying downstream layers until the component PRs are merged and explicit
`go_module_release` versions are declared. Seed commits MUST NOT be tagged or
merged independently.

Within each dependency layer, WB MUST complete source edits for every
repository before normalizing any manifests, and MUST complete all manifest
normalization before verification begins. Cyclic peers MUST NOT observe a
half-rewritten source tree while running dependency tooling.

After adding requirements and local campaign replacements, WB MUST run the
language adapter's dependency normalization before verification. For Go this
is `go mod tidy`; WB MUST also remove campaign replacements whose requirements
were removed as unused, so modules with no applicable source change remain
clean.

### Language adapter evolution

#### REQ: type-aware-local-renames

The migration format MUST reserve a type-aware local declaration rename
operation for adapters that can resolve symbols across an entire package or
module. The Go implementation MUST use `go/types`; Python and TypeScript
implementations MUST use LibCST and the TypeScript compiler API respectively.
Until an adapter provides that guarantee, a local type declaration rename MUST
be rejected rather than approximated with text replacement.

#### REQ: adapter-owned-manifests

Language adapters MUST own their package-manifest behavior. Go owns `go.mod`;
the planned Python and TypeScript adapters own `pyproject.toml` and
`package.json`. The campaign core owns dependency ordering, worktree lifecycle,
reports, and publishability gates, not language-specific manifest parsing.

#### REQ: hermetic-campaign-tests

The campaign implementation MUST have hermetic integration coverage using
local Git remotes and a substitute clone resolver. The test suite MUST prove
source roots and canonical clones stay untouched, local replacements point to
campaign worktrees, a clean campaign resumes and cleans safely, and a missing
published version blocks a PR before a consumer commit or push.

## Interaction with Other Features

This feature uses the common migration planning and report protocol. Future
Python and TypeScript adapters extend its language-neutral HCL specification
without changing the campaign lifecycle contract.

## Acceptance Criteria

### AC: safe-isolated-campaign

**Requirements:** hierarchical-migration-campaigns#req:canonical-clones-untouched, hierarchical-migration-campaigns#req:recoverable-resume, hierarchical-migration-campaigns#req:campaign-lock, hierarchical-migration-campaigns#req:narrow-cleanup

A campaign creates or explicitly resumes dedicated worktrees, preserves
partial migration changes, leaves canonical clones untouched, prevents
concurrent execution, and cleans no broader target than the named migration
worktrees.

### AC: publishable-review-branch

**Requirements:** hierarchical-migration-campaigns#req:local-replace-for-verification, hierarchical-migration-campaigns#req:released-before-pr, hierarchical-migration-campaigns#req:dependency-layers-and-parallelism, hierarchical-migration-campaigns#req:open-pr-retry

Local verification may use a dependency worktree, but a review branch uses
published module versions and passes its selected verification again before
publication. Dependency order is preserved while independent work and remote
CI overlap safely.

### AC: truthful-extensible-specification

**Requirements:** hierarchical-migration-campaigns#req:deterministic-operation-order, hierarchical-migration-campaigns#req:deferred-dry-run-results, hierarchical-migration-campaigns#req:linked-review-index, hierarchical-migration-campaigns#req:cumulative-resume-change-index, hierarchical-migration-campaigns#req:auditable-go-dependency-decisions, hierarchical-migration-campaigns#req:type-aware-local-renames, hierarchical-migration-campaigns#req:adapter-owned-manifests, hierarchical-migration-campaigns#req:hermetic-campaign-tests

The format has deterministic operation phases, reports distinguish deferred
planning from measured changes, and the adapter roadmap preserves type safety
and manifest ownership across Go, Python, and TypeScript.

## Open Questions

- Should a future adapter declaration carry a minimum supported runtime or
  compiler version in the migration HCL itself?
- Should a release declaration support a source repository release tag in
  addition to the Go module version used by `go.mod`?

---
*This document follows the https://specscore.md/feature-specification*
