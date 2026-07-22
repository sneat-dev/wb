---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Dependency Drift

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-drift?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-drift?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-drift?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-drift?op=request-change) |

**Status:** Draft
**Source Ideas:** —

## Summary

`wb deps drift` produces a read-only dependency convergence report for one
repository or a selected fleet. It distinguishes declared, selected,
replaced, and latest-known versions, explains the dependency edges that force
each selection, and emits Markdown for people and agents together with YAML or
JSON for deterministic tooling.

## Problem

A successful build does not prove a repository fleet uses a consistent
dependency release. Go's minimum-version selection chooses one version for a
module path inside a single build list, but different repositories may select
different versions; a local `replace` can hide the version CI will use; and
parallel major module paths can keep two incompatible APIs alive. Without a
fleet index, migrations discover these differences one compiler failure at a
time and cannot explain why a manifest was preserved.

## Behavior

### Selection and safety

#### REQ: repository-and-fleet-scope

`wb deps drift [path]` MUST inspect one repository when a path is supplied and
MUST support `--fleet <projects-root>` with the common `--match`, `--regex`,
and organization filters. Fleet results MUST be ordered by repository slug,
independently of discovery or completion order.

#### REQ: read-only-analysis

Dependency drift analysis MUST NOT edit manifests, create worktrees, fetch,
pull, commit, push, open a pull request, or merge. Network version discovery
MUST require an explicit online mode; an offline report MUST identify the
cache or local graph used and MUST NOT describe an unqueried version as latest.

### Version evidence

#### REQ: declared-selected-replaced-latest

For every dependency, the report MUST distinguish the manifest declaration,
the language toolchain's selected version, any replacement source, and the
latest compatible version actually observed. Each value MUST include the time
of observation. A missing value MUST carry an explicit reason rather than be
represented as an empty successful check.

#### REQ: go-selection-semantics

The Go adapter MUST use Go module tooling for build-list selection and version
queries. It MUST report one selected version per module path, parallel major
paths such as `example.org/lib` and `example.org/lib/v2`, local or forked
`replace` directives, and the shortest known requirement edges that force a
selected version.

#### REQ: fleet-version-groups

A fleet report MUST group each canonical dependency path by every declared
and selected version found across repositories. Each group MUST link the
repositories using it and classify the fleet state as converged, divergent,
replaced, major-path split, unavailable, or error.

#### REQ: private-dependency-evidence

When authentication, checksum policy, or registry access prevents a version
check, the report MUST record the dependency, attempted source, version known
before the check, and sanitized failure reason. Reports MUST NOT include
tokens, credential-helper output, or authenticated clone URLs.

### Reports and gates

#### REQ: dual-audience-drift-report

Default output MUST be a linked Markdown index. YAML and JSON MUST expose the
same repositories, dependency identities, version evidence, forcing edges,
classifications, timestamps, and reasons with deterministic ordering.

#### REQ: configurable-drift-gate

`--fail-on-drift` MUST return non-zero after the complete report when any
selected dependency is divergent, replaced, or split across major paths.
Without that flag, detected drift MUST be reportable without turning the
read-only command into a failing quality gate. Inspection errors MUST always
produce a non-zero exit after all selected repositories are reported.

## Synthetic Use Cases

### UC: gradual SDK rollout across a fleet

The fictional `acme/api`, `acme/facade`, and `acme/renderer` repositories use
`example.org/sdk` at `v1.8.0`, `v1.7.2`, and `v1.5.0`. An engineer runs
`wb deps drift --fleet ~/projects --match 'acme/**'` before a cross-repository
API rename. The report groups all three versions, shows which repository uses
each, identifies `v1.8.0` as the latest version actually checked, and provides
the input for a later bump campaign.

### UC: local replacement hides CI behavior

The fictional `acme/payments` manifest requires `example.org/money v0.9.4`
but replaces it with `../money`. The local build passes while CI would consume
the released module. `wb deps drift` classifies the dependency as replaced,
shows both the declared version and local path, and `--fail-on-drift` makes the
condition usable as a pre-publication gate.

### UC: two major APIs coexist accidentally

The fictional `acme/search` graph includes both `example.org/query` and
`example.org/query/v2` through different adapters. The command does not call
this two selected versions of one Go module path; it reports a major-path
split, identifies the importing edges, and gives a reviewer enough evidence
to decide whether coexistence is intended.

## Interaction with Other Features

[Dependency Bump Waves](../dependency-bump-waves/README.md) MAY consume a
deterministic drift report as its starting graph. [Hierarchical Migration
Campaigns](../hierarchical-migration-campaigns/README.md) MAY run drift after
each release layer to prove the campaign has converged.

## Acceptance Criteria

### AC: divergent-fleet-is-explained

**Requirements:** dependency-drift#req:repository-and-fleet-scope, dependency-drift#req:declared-selected-replaced-latest, dependency-drift#req:fleet-version-groups, dependency-drift#req:dual-audience-drift-report

**Given** three fictional repositories select different releases of the same
canonical dependency
**When** a fleet drift report is generated
**Then** Markdown and YAML group every version by repository, identify the
latest version actually checked and its timestamp, and explain each selection.

### AC: replacement-and-major-split-are-distinct

**Requirements:** dependency-drift#req:go-selection-semantics, dependency-drift#req:configurable-drift-gate

**Given** one Go repository has a local replacement and another build graph
contains both v1 and v2 module paths
**When** `wb deps drift --fail-on-drift` inspects them
**Then** it reports distinct replaced and major-path-split classifications and
returns non-zero only after the complete index is written.

### AC: unavailable-private-version-is-auditable

**Requirements:** dependency-drift#req:read-only-analysis, dependency-drift#req:private-dependency-evidence

**Given** a private registry cannot authenticate while the manifest declares
a previously known version
**When** online drift analysis attempts version discovery
**Then** the report preserves the known version, records the sanitized failed
attempt and timestamp, exposes no credential, and makes no repository change.

## Open Questions

- Should the first release support only Go, or expose an adapter registry while
  shipping Go as the sole implemented adapter?
- Should `--fail-on-drift` accept classifications, for example
  `--fail-on-drift=replaced,divergent`, in its first release?

---
*This document follows the https://specscore.md/feature-specification*
