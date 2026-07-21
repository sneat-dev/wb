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

### Verification

#### REQ: conventional-go-checks

`wb verify` MUST support the ordered check set `lint,test,build`, defaulting to all three. For every discovered Go module, those checks run `go vet ./...`, `go test ./...`, and `go build ./...` respectively.

#### REQ: conventional-node-checks

For a root `package.json`, `wb verify` MUST run only defined `lint`, `test`, and `build` scripts using the declared or lockfile-detected npm, pnpm, yarn, or bun package manager. A missing optional script is skipped rather than failed.

#### REQ: complete-index

Quality commands MUST continue after repository-level failures and report each attempted, skipped, passed, or failed check. They MUST return non-zero after the complete index is written if any selected repository failed.

### Reports and extension

#### REQ: dual-audience-reports

The default stdout format MUST be Markdown. Both commands MUST also support YAML and JSON stdout, and `--report-dir` MUST write Markdown and YAML files with stable names. Coverage reports include repository and fleet statement totals; verification reports include each executed command and a bounded failure detail.

#### REQ: custom-stack-recipes

WB MUST keep ecosystem-specific custom verification outside this command's hard-coded behavior. Python, workspace-specific Node, and other custom stacks remain expressible through `wb run` recipes until they have a stable, conventional adapter contract.

## Interaction with Other Features

[Hierarchical Migration Campaigns](../hierarchical-migration-campaigns/README.md) uses module-local verification while a migration is in progress. Fleet Quality gives users a separate, read-only view of the existing local clone fleet.

## Acceptance Criteria

### AC: truthful-fleet-coverage

**Requirements:** fleet-quality#req:local-fleet-selection, fleet-quality#req:composable-filters, fleet-quality#req:bounded-parallelism, fleet-quality#req:all-go-modules, fleet-quality#req:weighted-coverage

A selected local fleet has predictable filtering and bounded execution, and its coverage total is based on actual covered and instrumented statement counts across every Go module.

### AC: complete-conventional-verification

**Requirements:** fleet-quality#req:conventional-go-checks, fleet-quality#req:conventional-node-checks, fleet-quality#req:complete-index, fleet-quality#req:dual-audience-reports, fleet-quality#req:custom-stack-recipes

Every applicable conventional check appears in a complete, tool-readable and human-readable index. Unsupported custom stacks are not guessed and remain available through explicit recipes.

## Open Questions

- Should a future Python adapter standardize on `uv`, `pytest`, and `ruff`, or remain recipe-only until repository metadata provides an explicit command?
- Should a future threshold flag turn a coverage report into a fleet quality gate without duplicating CI policy configuration?

---
*This document follows the https://specscore.md/feature-specification*
