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

#### REQ: clean-resume

`--resume` MUST reuse only an existing worktree on the expected campaign
branch that has no uncommitted changes. A missing, dirty, or differently
branched worktree MUST fail without replacing it.

#### REQ: campaign-lock

An apply campaign MUST hold one exclusive migration lock below its dedicated
worktree root. A concurrent or interrupted lock MUST cause a safe failure and
MUST NOT be silently overwritten.

#### REQ: narrow-cleanup

`--cleanup` MUST remove only clean, dedicated worktrees for the named
migration. It MUST NOT remove canonical clones, local branches, reports, or a
worktree with uncommitted changes.

### Planning and reports

#### REQ: deterministic-operation-order

The migration format MUST execute HCL operations in this stable phase order:
`text_replace`, `import_replace`, `selector_rewrite`, then
`selector_rename`; manifest edits occur only in a hierarchical campaign after
source edits. Repeated blocks with the same language label remain valid and
preserve their source order within a phase.

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

#### REQ: dependency-layers-and-parallelism

WB MUST process provider dependency layers before consumer layers. Independent
repositories MAY run up to `--parallel` concurrently. When `--pr` is set, WB
MUST continue ready local work while already-opened pull requests run remote
CI; `--merge` is a final phase that requires every campaign PR's required
checks to pass before any merge is attempted.

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

**Requirements:** hierarchical-migration-campaigns#req:canonical-clones-untouched, hierarchical-migration-campaigns#req:clean-resume, hierarchical-migration-campaigns#req:campaign-lock, hierarchical-migration-campaigns#req:narrow-cleanup

A campaign creates or reuses only clean, dedicated worktrees, leaves canonical
clones untouched, prevents concurrent execution, and cleans no broader target
than the named migration worktrees.

### AC: publishable-review-branch

**Requirements:** hierarchical-migration-campaigns#req:local-replace-for-verification, hierarchical-migration-campaigns#req:released-before-pr, hierarchical-migration-campaigns#req:dependency-layers-and-parallelism

Local verification may use a dependency worktree, but a review branch uses
published module versions and passes its selected verification again before
publication. Dependency order is preserved while independent work and remote
CI overlap safely.

### AC: truthful-extensible-specification

**Requirements:** hierarchical-migration-campaigns#req:deterministic-operation-order, hierarchical-migration-campaigns#req:deferred-dry-run-results, hierarchical-migration-campaigns#req:linked-review-index, hierarchical-migration-campaigns#req:type-aware-local-renames, hierarchical-migration-campaigns#req:adapter-owned-manifests, hierarchical-migration-campaigns#req:hermetic-campaign-tests

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
