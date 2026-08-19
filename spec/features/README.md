---
format: https://specscore.md/features-index-specification
---

# WB Features

## Index

| Feature | Status | Description |
|---|---|---|
| [Dependency Bump Waves](dependency-bump-waves/README.md) | Implementing | `wb deps bump` recalculates a dependency graph and propagates newly released |
| [Dependency Graph](dependency-graph/README.md) | Implementing | `wb deps graph` discovers one canonical dependency-evidence graph and projects |
| [Dependency Drift](dependency-drift/README.md) | Draft | `wb deps drift` produces a read-only dependency convergence report for one |
| [Exact Dependency Set](dependency-set/README.md) | Implementing | `wb deps set <ecosystem> <dependency>@<version>` changes existing references |
| [Fleet Quality](fleet-quality/README.md) | Implementing | WB measures Go test coverage and runs conventional lint, test, and build checks for one repository or a selected fleet of local clones. The commands continue through every selected repository and produce a reviewable Markdown index plus deterministic YAML or JSON for tools. |
| [Fleet Status](fleet-status/README.md) | Implementing | Fleet inspection uses explicit nouns: |
| [Hierarchical Migration Campaigns](hierarchical-migration-campaigns/README.md) | Implementing | WB migrates a dependency hierarchy through dedicated local worktrees, then can |
| [Worktree Lifecycle](worktree-lifecycle/README.md) | Implementing | `wb worktree` creates, guards, inventories, and safely cleans task worktrees |
| [Self-Update](self-update/README.md) | Implementing | `wb self-update` (alias `wb update`) brings a running `wb` binary to the latest |
| [Work Log Recovery](work-log/README.md) | Approved | `wb worktree log` gives every WB-managed effort a private, durable journal that |
| [Branch Hygiene](branch-hygiene/README.md) | In Review | `wb branch` is a top-level command family that inventories and safely retires |
| [Worktree Checkpoint](worktree-checkpoint/README.md) | Approved | A cheap, repeatable, hook-proof snapshot that preserves in-flight worktree state against crashes, independent of whether the work compiles or is ready to commit. |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/features-index-specification*
