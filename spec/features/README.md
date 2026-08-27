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
| [Remote State](remote-state/README.md) | Implementing | `wb remote` shares fleet state across machines through a pluggable store: |
| [Remote Claims](remote-claims/README.md) | Implementing | `wb remote claim`/`release`/`claims` reserve a task fleet-wide in the same |
| [Hierarchical Migration Campaigns](hierarchical-migration-campaigns/README.md) | Implementing | WB migrates a dependency hierarchy through dedicated local worktrees, then can |
| [Worktree Lifecycle](worktree-lifecycle/README.md) | Implementing | `wb worktree` creates, guards, inventories, and safely cleans task worktrees |
| [Self-Update](self-update/README.md) | Implementing | `wb self-update` (alias `wb update`) brings a running `wb` binary to the latest |
| [Work Log Recovery](work-log/README.md) | Approved | `wb worktree log` gives every WB-managed effort a private, durable journal that |
| [Branch Hygiene](branch-hygiene/README.md) | Implementing | `wb branch` is a top-level command family that inventories and safely retires |
| [Cleanup Orchestration](cleanup-orchestration/README.md) | In Review | `wb cleanup` is one top-level entry point that retires a repository's |
| [Operations Journal](operations-journal/README.md) | Draft | Append-only journal of every wb operation, bundle-backed preservation for unreachable commits, and a wb restore command that reads reports and journal records back into a branch. |
| [Dependency and layering policy](dependency-policy/README.md) | Implementing | One declarative policy states which kinds of repository may depend on which |
| [NPM release propagation](npm-release-propagation/README.md) | Stable | Publish approved npm packages through repository-owned GitHub Actions workflows, verify exact registry evidence, and hand confirmed releases to recalculated dependency waves. |
| [Agent Session Move](agent-session-move/README.md) | Approved | Move an active AI agent session to another WB machine through an interchangeable courier while preserving Git state, handover context, lineage, and Work Log evidence. |
| [Park and Resume Agent Sessions](park-and-resume-agent-sessions/README.md) | Approved | Park and resume lets a coordinator suspend one active WB session while |
| [Archived Clone Cleanup](archived-clone-cleanup/README.md) | Implementing | wb archive clean safely removes local clones of repositories confirmed archived on GitHub, but only when the clone holds nothing that would be lost. |
| [Go directive fleet policy](go-directive-policy/README.md) | Approved | Assess and, per repository, land the go 1.26.x directive / toolchain go1.27.0 pairing across the fleet, refusing where a dependency's own go directive forces a higher floor. |
| [Pre-Push Tiering and Remote Checkpoints](pre-push-tiering-and-remote-checkpoints/README.md) | Implementing | Tier the managed pre-push hook by push target and give wb worktree checkpoint a fast, tier-0-only remote ref, so agents can persist work often without paying the full test-suite tax on every push. |

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/features-index-specification*
