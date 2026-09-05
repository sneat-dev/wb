---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Graph-assisted fleet optimization and test selection

**Status:** Draft
**Date:** 2026-09-05
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** extends:developer-lifecycle-metrics

## Problem Statement

How might WB use a fresh, attested CodeGrapher dependency graph to reduce redundant fleet checks and choose focused tests without skipping a test that would have found a regression?

## Context

WB already has dependency and changed-file evidence, while CodeGrapher can expose symbol, call, import, and test relationships. Combining them could avoid repeatedly running the same downstream validation during a dependency wave, especially on four-core VMs. It also risks a false-negative selection: a partial graph, dynamic loading, generated code, or an outdated index could claim that a test is irrelevant when it is not.

The first measured WB index of the WB repository took 7.9 seconds for 806 files, 14,572 nodes, 54,237 edges, and 53.79 MB; an unchanged sync took 0.13 seconds. Those observations justify investigating a coalesced refresh, but not treating graph selection as a quality gate.

## Recommended Direction

Treat graph assistance as an advisory scheduling signal first. It may rank or batch likely affected tests and downstream repositories, while existing deterministic language ownership, explicit dependency rules, and CI remain the safety baseline. Every recommendation records its exact source SHA, graph version, selected candidates, omitted candidates, and the later observed failures so recall can be measured.

Graduate only after a representative sample demonstrates a predefined recall threshold for regression-catching tests, separately by language and repository class. A miss must immediately disable exclusion for that affected classifier and retain the failure evidence.

## Alternatives Considered

- Run every test after every change. Safest but wastes the limited CPU budget and repeats work that cached Go results or a batched integration branch could avoid.
- Let an agent decide tests ad hoc. This spends tokens and produces inconsistent, unauditable selection.
- Use graph reachability as a hard exclusion immediately. Fast, but unsafe until coverage of dynamic and generated edges is measured.

## MVP Scope

Record only: collect graph-attested candidate recommendations next to actual changed-file and test outcomes. Do not skip local tests, alter hooks, or change CI until recall measurement is available.

## Not Doing (and Why)

- Sending source code or graph content to an external service without an explicit privacy contract.
- Replacing required CI with graph selection.
- Triggering CodeGrapher refresh from ordinary `wb sync` before the provider returns an exact-SHA receipt.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|---|---|---|
| Must-be-true | Graph provenance can be bound to the exact source SHA. | Provider integration test rejects a mismatched or missing SHA acknowledgement. |
| Must-be-true | Selected-test recall meets the agreed threshold for actual regressions. | Shadow-mode sample comparing selected tests with the full required suite. |
| Should-be-true | Coalesced refresh costs less than redundant downstream validation. | Compare queue timings and CPU load across 1, 3, 5, and 7 streams. |
| Might-be-true | Symbol-level evidence improves prioritization beyond changed-file selection. | A/B ranking analysis on recorded recommendations. |

## SpecScore Integration

- **New Features this would create:** graph-assisted fleet optimization
- **Existing Features affected:** tool-plugins, agent-sdlc-throughput, fleet-quality, developer-lifecycle-metrics
- **Dependencies:** exact-SHA CodeGrapher provider acknowledgement, WB operations journal, privacy-safe telemetry

## Open Questions

- What recall threshold and sample size are sufficient before any test is excluded rather than merely ranked?
- How should generated sources and runtime-discovered dependencies be represented in confidence scores?

---
*This document follows the https://specscore.md/idea-specification*
