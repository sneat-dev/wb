---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: CI Validation Receipt Reuse

**Status:** Draft
**Date:** 2026-09-05
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB avoid repeating unchanged trusted pull-request validation after a merge without allowing a direct push, base drift, workflow change, fork, expired artifact, or incomplete check to authorize release?

## Context

Reuse an already-completed same-repository pull-request validation only when its exact source tree and validation policy match the landed main commit.

## Recommended Direction

Publish a small GitHub-authenticated receipt after the required aggregate succeeds; main validates the associated merged pull request, action-run metadata, policy digest, final tree digest, and artifact before it skips expensive validation jobs.

## Alternatives Considered

- Run the whole Go CI workflow again on every `main` push — rejected for a
  GitHub squash merge whose final tree has already passed the same required
  validation, because it delays release without producing new test evidence.
- Trust a green pull-request check by name — rejected because a check name does
  not bind the final tree, source repository, workflow revision, or artifact.
- Reuse a release artifact — rejected because signed release construction is a
  separate privileged operation and must continue to build from the exact
  landed ref.

## MVP Scope

Go CI only: same-repository pull requests merged into main, one receipt artifact, fail-closed full-validation fallback, and shell fixture tests.

## Not Doing (and Why)

- Cross-repository or fleet-wide receipt sharing — each repository owns its own GitHub Actions trust boundary.
- Release artifact reuse — release continues to build and publish its own signed artifact.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | GitHub exposes an associated merged pull request, its completed workflow run, and its artifact to the `main` workflow token. | Exercise a same-repository pull request and make missing or expired API evidence select the full-validation fallback. |
| Must-be-true | The final landed tree can be compared exactly with the tree tested by the pull-request workflow. | Emit `git rev-parse HEAD^{tree}` in the receipt and reject a changed final tree in a fixture test. |
| Must-be-true | A pull request cannot alter the receipt producer or validation policy and then authorize reuse. | Reject every pull request that changes `.github/`; compare the current policy digest with the receipt. |
| Should-be-true | Receipt upload completes before GitHub produces the merge push. | Treat a missing artifact as ordinary fallback rather than an authorization failure. |
| Might-be-true | The measured saving remains material as the Go suite evolves. | Record elapsed `main` runs before and after adoption; retain the full path for all refused cases. |


## SpecScore Integration

- **New Features this would create:** none for the MVP; this idea stays
  repository-owned workflow policy.
- **Existing Features affected:** [Fleet Quality](../features/fleet-quality/README.md)
  records that a pull-request-only result is not normally final-target evidence;
  this narrowly defined exact-tree receipt is the exception for the duplicate
  validation mechanisms only, never for deployment or cleanup receipts.
- **Dependencies:** GitHub Actions workflow-run and artifact APIs, `jq`, and
  GitHub's authenticated `GITHUB_TOKEN`.

## Open Questions

1. Should GitHub artifact attestations replace the authenticated receipt
   artifact if reuse expands to repositories that accept untrusted same-org
   contributors? The MVP refuses forks and workflow changes, and falls back to
   full validation whenever the normal GitHub API evidence is incomplete.
