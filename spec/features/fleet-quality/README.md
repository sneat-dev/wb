---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Fleet Quality

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-quality?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-quality?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-quality?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/fleet-quality?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

WB measures Go test coverage and runs conventional lint, test, and build checks for one repository or a selected fleet of local clones. The commands continue through every selected repository and produce a reviewable Markdown index plus deterministic YAML or JSON for tools.

## Problem

Cross-repository quality work otherwise requires manually finding each clone, remembering its language conventions, and collecting failures from multiple terminal runs. A single failure can hide later failures, while an unweighted average of module percentages misstates fleet coverage.

## Behavior

### Repository selection

#### REQ: local-fleet-selection

`wb coverage` and `wb verify` MUST accept one repository path by default and MUST select every local Git repository below `--projects-root` only with `--fleet`. A fleet operation MUST never clone, fetch, modify, commit, or push a repository.

#### REQ: composable-filters

Fleet selection MUST apply the existing substring `--filter`, an optional `--match` glob, and an optional `--regex` regular expression to the `org/repo` slug. All supplied filters MUST match. Invalid regular expressions and a selection with no repositories MUST fail before checks begin.

#### REQ: bounded-parallelism

`--parallel` MUST cap concurrently processed repositories and MUST reject values below one. Results MUST be sorted by repository slug independently of completion order.

### Coverage

#### REQ: all-go-modules

`wb coverage` MUST find every `go.mod` below a selected repository while excluding `.git`, `vendor`, and `node_modules`. It MUST run `go test` with a temporary coverage profile for each module and MUST never write a coverage artifact into the repository.

#### REQ: weighted-coverage

Fleet coverage MUST aggregate Go coverage by covered statements divided by all instrumented statements, not by averaging module percentages. A repository without a Go module is skipped; a failing test or malformed profile is failed.

#### REQ: remote-ci-coverage-reporting

`wb coverage --ci` and `wb fleet coverage` MUST inspect latest test coverage reports without running local tests. `wb coverage --ci` queries the latest recorded CI coverage for the specified repository (or the repository containing the current working directory). `wb fleet coverage` inspects and aggregates coverage across all repositories in the fleet coverage store. When `--minimum` is passed, coverage below the threshold MUST return a non-zero exit code.

#### REQ: coverage-summary-artifact

`wb coverage summary` MUST parse a Go coverage profile and emit a standardized JSON summary (`wb-coverage-summary`) suitable for build artifact publication and remote collection.

#### REQ: hub-coverage-harvester

The Workbench Hub MUST subscribe to `workflow_run.completed` GitHub webhook events on default branches, download the `wb-coverage-summary` artifact using the GitHub App token, and persist the coverage record to the repository coverage store.

#### REQ: fleet-metrics-web-dashboard

The Workbench daemon/hub server MUST serve an interactive web dashboard at `/metrics` (and redirect `/coverage` to `/metrics?type=test_coverage`) displaying test coverage and registered fleet metrics across repositories. For test coverage, the dashboard MUST display per-repository status, statements, covered statements, and an expandable hierarchical breakdown by Go package.

#### REQ: generic-repository-metrics

The Workbench Hub MUST provide a generalized repository metrics model and API (`/v0/workbench/metrics`) capable of storing and querying arbitrary multi-dimensional repository measurements with scalar summaries, units, threshold rules, and dimensional breakdowns (e.g. packages, days, authors) using a DALgo document store.

### Verification

#### REQ: conventional-go-checks

`wb verify` MUST support the ordered check set `lint,test,build`, defaulting to all three. For every discovered Go module, test and build run `go test ./...` and `go build ./...`. Lint defaults to `go vet ./...`; a tracked `.wb/quality.yaml` MAY replace it with ordered structured argv commands, including an exact version-pinned linter. Empty commands or arguments MUST fail closed.

#### REQ: conventional-node-checks

For a root `package.json`, `wb verify` MUST run defined `lint`, `test`, and
`build` scripts using the declared or lockfile-detected npm, pnpm, yarn, or bun
package manager. When that lockfile scope is an Nx workspace and a root script
is absent, WB MUST execute the corresponding Nx target across all applicable
projects instead of reporting the check skipped. Outside an Nx workspace, a
missing optional script is skipped rather than failed.

#### REQ: complete-index

Quality commands MUST continue after repository-level failures and report each attempted, skipped, passed, or failed check. They MUST return non-zero after the complete index is written if any selected repository failed.

### Check profiles and reliability

#### REQ: check-profiles

`wb check` MUST provide named built-in profiles: `fast` runs lint, `full` runs lint, test, and build, and `ci` adds SpecScore lint when a repository has a `spec/` directory, unless the repository is an external SpecScore Plans store as defined below. When `specscore.yaml` explicitly configures SpecScore, `ci` MUST fail if the canonical `spec/` root is missing; repositories with neither the config nor the root remain non-applicable. `full` MUST be the default profile. A profile MUST use the same conventional Go and Node adapters as `wb verify`.

A repository is an external SpecScore Plans store (SpecScore's Plan repository routing; `sneat-co/workbench` is one) only when all of these hold: its root has no `specscore.yaml` entry, a symlink counting as present; its root `.gitignore` is a regular file with the line `/.specscore-lifecycle.lock` (SpecScore's `REQ:external-store-lifecycle-lock`); every non-directory entry under `spec/` is `spec/plans/README.md`, a namespace index `spec/plans/{host}/{owner}/{repo}/README.md`, or lies beneath a plan directory `spec/plans/{host}/{owner}/{repo}/{plan-id}/`; and at least one such entry lies inside a namespace. `{host}` MUST look like a hostname: lowercase letters, digits, `-` and `.`, at least one dot, and dot-separated labels that are non-empty and neither start nor end with `-`. `{owner}` and `{repo}` MUST be non-empty and MUST NOT start with `.`. For such a repository `ci` MUST report SpecScore lint skipped and MUST NOT claim the Plans are validated: SpecScore lint does not apply to that layout, because without a `specscore.yaml` it cannot run, and with one it reports structural violations (`readme-exists` and `plan-hierarchy` in specscore 0.49.0). Every other `spec/` -- an empty one, one holding only directories, one reached through a symlink, a SpecScore project that lost its `specscore.yaml`, or a same-repository `spec/plans/{plan-id}` tree -- MUST still run lint and fail exactly as when the layout does not apply.

#### REQ: bounded-command-execution

Coverage, verification, and check commands MUST apply `--timeout` independently
to every external command. A Go test process MUST receive the same value through
its native `-timeout` flag so Go's hidden ten-minute default cannot terminate a
check whose WB budget is longer; `--timeout=0` MUST pass `-timeout 0`. The
default timeout MUST be finite. `--retry=N` MUST make at most N additional
attempts for a failed command and record the number of attempts in the report.

#### REQ: repository-safe-test-sharding

Repository quality policy MUST shard only explicitly named packages. Packages
with process-global or real-clock journeys MUST be left in the single unsharded
job; shard counts SHOULD be the smallest value that removes the long tail so
validation does not multiply compilation and `TestMain` setup unnecessarily.

#### REQ: report-resume

When `--resume` and `--report-dir` are supplied, a quality command MUST read its previous YAML report and run only selected repositories whose prior status was failed. It MUST fail when the report is unavailable or invalid, and MUST leave it intact when no selected repository needs resuming.

### Reports and extension

#### REQ: dual-audience-reports

The default stdout format MUST be Markdown. Both commands MUST also support YAML and JSON stdout, and `--report-dir` MUST write Markdown and YAML files with stable names. Coverage reports include repository and fleet statement totals; verification reports include each executed command and a bounded failure detail.

#### REQ: graduation-receipt

`wb verify receipt` MUST compose five explicit, versioned machine-readable
producer documents into one graduation receipt: a one-repository
`wb check --profile ci --format json` report, a direct final-target
`wb ci wait --json` receipt, an exact `wb verify receipt remote-target`
observation, an external deployed-revision receipt, and the real
`wb worktree cleanup --apply --remote` report. Every component MUST resolve to
the same repository and full Git revision. The local report MUST bind its
passed lint, test, and build mechanisms to an unchanged clean checkout at that
revision; PR-only CI is not final-target CI; and the deployment provider's
structured payload MUST expose the revision through a declared JSON pointer
whose bytes match its digest and credential-free immutable run URL.

Terminal cleanup MUST attest that every campaign worktree and local source
branch is gone and each remote source branch was deleted or was already absent.
It MUST retain the canonical target checkout and bind the selected repository's
cleanup to the same remote-target revision. The command MUST reject a missing,
failed, malformed, mismatched, future/non-monotonic, prose, or hand-authored
status component rather than infer that a recently green branch was deployed
or cleaned. Its receipt records the exact source digest and producer timestamp
of all five components so an independent reviewer can audit the complete
graduation journey without treating any one check as completion.

#### REQ: custom-stack-recipes

WB MUST keep ecosystem-specific custom verification outside this command's hard-coded behavior. Python, workspace-specific Node, and other custom stacks remain expressible through `wb run` recipes until they have a stable, conventional adapter contract.

## Interaction with Other Features

[Hierarchical Migration Campaigns](../hierarchical-migration-campaigns/README.md) uses module-local verification while a migration is in progress. Fleet Quality gives users a separate, read-only view of the existing local clone fleet.

## Acceptance Criteria

### AC: truthful-fleet-coverage

**Requirements:** fleet-quality#req:local-fleet-selection, fleet-quality#req:composable-filters, fleet-quality#req:bounded-parallelism, fleet-quality#req:all-go-modules, fleet-quality#req:weighted-coverage

A selected local fleet has predictable filtering and bounded execution, and its coverage total is based on actual covered and instrumented statement counts across every Go module.

### AC: complete-conventional-verification

**Requirements:** fleet-quality#req:conventional-go-checks, fleet-quality#req:conventional-node-checks, fleet-quality#req:complete-index, fleet-quality#req:check-profiles, fleet-quality#req:bounded-command-execution, fleet-quality#req:report-resume, fleet-quality#req:dual-audience-reports, fleet-quality#req:graduation-receipt, fleet-quality#req:custom-stack-recipes

Every applicable conventional check appears in a complete, tool-readable and human-readable index. Unsupported custom stacks are not guessed and remain available through explicit recipes.

### AC: exact-graduation-receipt

**Requirements:** fleet-quality#req:graduation-receipt

**Given** exact-clean-revision local evidence, direct final-target CI, a remote
target observation, a structured deployed-revision receipt, and terminal
campaign cleanup evidence for one repository and commit
**When** `wb verify receipt` composes them
**Then** it emits one machine-readable receipt binding all five components to
that exact identity. If any component names another revision, reports a failed
mechanism, or leaves a worktree or source branch behind, the command refuses
without emitting a graduation receipt.

### AC: instant-remote-ci-coverage

**Requirements:** fleet-quality#req:remote-ci-coverage-reporting, fleet-quality#req:coverage-summary-artifact, fleet-quality#req:hub-coverage-harvester

**Given** a repository coverage summary artifact published by GitHub Actions on push/merge to default branch and harvested by Workbench Hub into the coverage store
**When** `wb coverage [repo] --ci` or `wb fleet coverage` is executed
**Then** latest test coverage statements and percentage are reported instantaneously (<100ms) without executing local `go test` runs.

### AC: fleet-metrics-web-dashboard

**Requirements:** fleet-quality#req:fleet-metrics-web-dashboard, fleet-quality#req:generic-repository-metrics

**Given** a running Workbench server (`wb daemon serve` or `wb hub`) with harvested repository coverage records or published metrics
**When** a user navigates to `/metrics` or `/coverage`
**Then** the server serves a responsive HTML page displaying each repository's primary metric with visual health indicators, and allows expanding a repository to inspect its dimensional breakdown (such as Go packages for coverage).

## Open Questions

- Should a future Python adapter standardize on `uv`, `pytest`, and `ruff`, or remain recipe-only until repository metadata provides an explicit command?
- Should a future threshold flag turn a coverage report into a fleet quality gate without duplicating CI policy configuration?

---
*This document follows the https://specscore.md/feature-specification*
