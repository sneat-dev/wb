---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Migration release handoff

**Status:** Draft
**Date:** 2026-07-21
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB safely bridge a verified local dependency-migration campaign to
published module releases, so consumer pull requests become CI-resolvable only
after their provider versions are genuinely available?

## Context

Hierarchical migrations correctly use relative `go.mod replace` directives
inside dedicated worktrees while a provider and its consumers are changed
together. Before `--pr`, WB now requires a `go_module_release` declaration,
removes the local path, runs `go mod tidy`, and verifies again. This is safe,
but an incomplete campaign naturally pauses when a provider's branch has not
yet been merged and tagged.

The DALgo record extraction is a representative case. `github.com/dal-go/record
v0.1.0` is already publishable, but consumers that still temporarily replace
the DALgo migration branch need the corresponding DALgo version only after the
extraction branch is merged and tagged. Today a human must find the paused
report, edit the HCL release declaration, and determine which consumer
worktrees are safe to resume.

## Recommended Direction

Add an explicit, opt-in release-handoff phase rather than teaching `wb migrate`
to create releases implicitly. The phase reads a campaign report and migration
HCL, validates that every required module/version is resolvable through the Go
module proxy or an explicitly supplied Git tag/ref, then produces a release
readiness report. It does not merge, tag, publish, or alter dependency source
by default.

When all declarations are available, `wb migrate handoff resume` updates only
the previously blocked consumer worktrees, replaces temporary local paths with
the published versions, tidies and verifies them, and can continue the
existing commit/push/PR lifecycle. The initial implementation requires the
release versions in HCL; a later opt-in integration may propose tags/releases,
but must require separate authorization for every remote state change.

The handoff report forms the audit trail: campaign ID, provider module,
expected version, availability evidence, consumers waiting on it, the prior
worktree/branch, and the exact verification outcome after the release path is
substituted.

## Possible Uses

### Staged DALgo extraction

After the record module is published but before DALgo itself is tagged, check
the campaign's readiness without changing a consumer worktree:

```sh
wb migrate handoff check examples/migrations/dalgo-record-v1.hcl \
  --campaign-report /tmp/dalgo-record/campaign.yaml
```

The report says that `github.com/dal-go/record@v0.1.0` is available and lists
the consumers still waiting for an explicit DALgo release. Once DALgo is
released, a reviewed HCL update adds its `go_module_release` block.

```hcl
go_module_release "github.com/dal-go/dalgo" {
  version = "v0.42.0"
}
```

Then only the blocked consumers resume:

```sh
wb migrate handoff resume examples/migrations/dalgo-record-v1.hcl \
  --campaign-report /tmp/dalgo-record/campaign.yaml \
  --verify full --pr --parallel=2
```

### Multi-module provider repository

One repository may publish several Go modules at different versions. A
handoff report maps the exact module path, not merely the Git repository, to
each expected version and consumer. This prevents a tag for one module from
accidentally authorizing a different module's consumer PR:

```sh
wb migrate handoff check migration.hcl \
  --module-release github.com/acme/sdk/auth=v1.8.0 \
  --module-release github.com/acme/sdk/events=v1.3.0
```

### Offline or private-module preflight

Teams using a private proxy can make availability evidence explicit instead of
allowing a consumer PR to fail later in CI:

```sh
wb migrate handoff check migration.hcl \
  --module-proxy https://proxy.corp.example \
  --offline-evidence releases/2026-07-21.yaml
```

The first MVP may support only the normal Go environment; the example shows
why the evidence interface should be designed rather than hard-coded.

### Release-manager review

A release manager uses a Markdown index to decide whether a campaign is ready
to open consumers, without authorizing any publish operation:

```sh
wb migrate handoff check migration.hcl \
  --campaign-report .wb/reports/migration/campaign.yaml \
  --report-dir /tmp/migration-handoff
```

The linked output identifies the provider PR/tag release evidence and each
consumer branch that would be resumed.

## Alternatives Considered

- **Open consumer PRs with local `replace` paths.** Rejected: remote CI cannot
  resolve a developer's worktree and the PR is not independently reviewable.
- **Make `wb migrate --merge` tag and publish automatically.** Rejected:
  versioning and publishing are material external actions needing separate
  authority and repository-specific release policy.
- **Wait indefinitely inside the original migration process.** Rejected: a
  long-running process is fragile and prevents the user from reviewing or
  handling unrelated work while CI and releases progress.
- **Assume any repository tag equals every module version.** Rejected:
  multi-module repositories and non-tagged Go versions make that unsafe.

## MVP Scope

- Read campaign report and HCL `go_module_release` declarations.
- Determine campaign dependencies that still use local worktree replacements.
- Validate availability through the current Go module environment.
- Write Markdown and YAML readiness reports with provider/consumer mapping.
- Resume only clean named campaign worktrees after all needed releases are
  available, rerun existing publishability checks, and reuse `--retry`,
  `--timeout`, and `--parallel` behavior.
- Require explicit existing versions; no tag, release, merge, publish, or PR
  action is implied by `handoff check`.

Private proxy adapters, GitHub release/tag lookup, automated release PRs,
registry publishing, semantic-version selection, and automatic merging are
follow-up slices.

## Not Doing (and Why)

- Publishing a Go module or creating a Git tag without a separate explicit
  release command and authorization.
- Guessing a version from a branch name, commit SHA, or latest tag.
- Resuming dirty or mismatched worktrees.
- Silently changing a consumer's declared release version to make the command
  pass.
- Treating a reachable version as proof of API compatibility; existing local
  verification remains mandatory.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|---|---|---|
| Must-be-true | Availability can be checked without modifying consumer worktrees. | Run handoff check against published and unpublished test modules and verify all listed paths remain clean. |
| Must-be-true | Module-path/version mapping prevents incorrect release substitution in multi-module repositories. | Build a fixture with two modules and intentionally supply one wrong version; assert the affected consumer remains blocked. |
| Should-be-true | A durable readiness report lets a different user or agent safely resume a paused campaign. | Pause a campaign after provider verification, then resume from the report in a fresh process. |
| Might-be-true | Explicit proxy/offline evidence can support private modules without weakening release verification. | Trial with a private proxy and compare evidence to what CI can resolve. |

## SpecScore Integration

- **New Features this would create:** `hierarchical-migration-campaigns/release-handoff` and a campaign release-readiness report contract.
- **Existing Features affected:** [Hierarchical Migration Campaigns](../features/hierarchical-migration-campaigns/README.md) owns temporary replacements, publishability gates, worktree safety, and PR lifecycle.
- **Dependencies:** campaign YAML reports, HCL release declarations, Go module resolution, clean dedicated worktrees, and optionally a future GitHub or private-proxy evidence adapter.

## Open Questions

1. Should a handoff report record only module-proxy availability, or also the
   provider repository tag/commit that published the version?
2. How should private module availability be proven without recording proxy
   credentials or exposing internal URLs in a shared report?
3. Should one handoff be allowed to resume only a selected consumer layer, or
   must it always resume every now-unblocked dependent?
4. What separate command and confirmation model would be appropriate for
   optional tag/release publication in a later phase?
