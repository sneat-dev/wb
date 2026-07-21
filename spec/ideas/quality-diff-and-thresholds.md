---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Quality diff and thresholds

**Status:** Draft
**Date:** 2026-07-21
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB make coverage results actionable by comparing them with a Git ref
or durable baseline, identifying affected modules, and enforcing explicit
quality budgets without turning a whole-fleet percentage into a misleading
gate?

## Context

`wb coverage` now provides a correct statement-weighted aggregate across every
Go module in a selected local fleet. That answers “what is coverage now?” but
not the questions that drive an engineering decision:

- Did this branch reduce coverage in the packages it changed?
- Which repository caused the fleet percentage to move?
- Is a low-coverage legacy repository stable while new code is improving?
- Can a release branch enforce no regression without requiring an arbitrary
  global 100% target?

CI platforms often answer this through a hosted coverage service. WB can offer
a local, reviewable counterpart using a Git worktree/ref and a versioned YAML
baseline, then let CI invoke the exact same command.

## Recommended Direction

Add `wb quality diff` as a read-only companion to `wb coverage`. It first
builds the ordinary statement totals for the selected checkout(s), then
compares them with exactly one explicit baseline source:

1. a Git ref such as `origin/main`, evaluated in a temporary detached
   worktree; or
2. a previously committed/generated WB coverage YAML index.

The report has two levels. The repository/module table shows base, current,
absolute delta, and percentage-point delta. The fleet summary is
statement-weighted and explicitly labels missing or incomparable baselines
rather than silently treating them as zero. A `--changed` mode restricts the
gate to modules whose source changed from the Git base, while the report still
shows the wider fleet context.

Thresholds should be budgets, not a replacement for review. `--min-coverage`
sets an absolute minimum; `--max-drop` limits a percentage-point regression;
and `--fail-on-missing-base` makes incomparability explicit for CI. The command
does not mutate the repository or rewrite a baseline. A separate, deliberate
`wb quality baseline write` command can generate a reviewed baseline artifact
when the team chooses to adopt one.

## Possible Uses

### Pull-request preflight

A developer changes a Go package in `sneat-core-modules` and wants to know
whether the changed module regressed before opening a PR:

```sh
wb quality diff ~/projects/sneat-co/sneat-core-modules \
  --base origin/main --changed --max-drop 0.25
```

The output distinguishes a 0.4-point regression in the changed package from a
large but unchanged legacy package elsewhere in the repository.

### Fleet maintenance release

Before a coordinated dependency upgrade, maintainers compare the selected
consumer fleet with its committed release baseline:

```sh
wb quality diff --fleet --match 'sneat-co/*' \
  --baseline .wb/baselines/2026-q3.yaml \
  --min-coverage 55 --max-drop 0.5 \
  --report-dir /tmp/q3-quality-diff
```

The Markdown report is review material; its YAML counterpart can be attached
to an automated release record or read by an AI agent.

### CI policy without a hosted coverage service

A repository's CI runs the same local contract after fetching its base ref:

```sh
wb quality diff . --base origin/main --changed \
  --max-drop 0 --fail-on-missing-base --format yaml
```

The job fails only for a changed-module regression or a missing required base,
not because unrelated repositories were unavailable in CI.

### Gradual quality improvement

A team starts below its desired coverage. It freezes regressions first, then
raises the minimum intentionally as modules improve:

```sh
wb quality baseline write --fleet --filter sneat-co/ \
  --output .wb/baselines/initial.yaml
wb quality diff --fleet --filter sneat-co/ \
  --baseline .wb/baselines/initial.yaml --max-drop 0
```

This avoids pretending a legacy aggregate is a useful immediate release gate.

## Alternatives Considered

- **Use only a global coverage percentage.** Rejected because it hides which
  module regressed and allows unrelated additions to distort the result.
- **Require a hosted coverage platform.** Rejected as the foundational path:
  it adds credentials, network dependence, and a different local/CI contract.
- **Parse diffs and estimate line coverage only.** Deferred. It is useful but
  toolchain-specific and should follow the correct module-level baseline.
- **Overwrite a baseline automatically after a successful run.** Rejected:
  a baseline is policy evidence and must be a deliberate reviewed change.

## MVP Scope

- Go-module-only comparison using current `wb coverage` statement totals.
- Exactly one baseline: `--base <git-ref>` or `--baseline <yaml-path>`.
- Repository/module/fleet deltas with explicit comparable/missing state.
- `--changed`, `--min-coverage`, `--max-drop`, and
  `--fail-on-missing-base`.
- Markdown, YAML, and JSON reports plus an optional report directory.
- Read-only temporary worktrees for Git-ref baselines and the same timeout,
  retry, parallelism, and resume controls as Fleet Quality.

Per-line diff coverage, Node/Python coverage adapters, baseline writing,
historical trend storage, and CI annotations are follow-up slices.

## Not Doing (and Why)

- Treating a fleet aggregate as a universal release threshold — repository and
  module context remain essential.
- Creating an automatic remote clone/fetch as a side effect of local baseline
  mode — the required base ref must already be available or fail explicitly.
- Mutating any source, Go module, lockfile, or baseline while comparing.
- Claiming cross-language percentage comparability before language adapters
  define compatible statement/branch semantics.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|---|---|---|
| Must-be-true | Statement-weighted module deltas give reviewers a more truthful signal than a fleet average. | Compare reports for deliberately changed high- and low-statement modules; ensure the reported delta matches the underlying profiles. |
| Must-be-true | A detached Git worktree can reproduce the relevant baseline without disturbing a developer checkout. | Run diff against dirty clones and assert canonical working state remains unchanged. |
| Should-be-true | `--changed` catches regressions with less noise than a whole-repository gate. | Trial it on dependency upgrades and compare reviewer decisions with full-repository coverage reports. |
| Might-be-true | A YAML baseline is sufficient for teams that do not use a hosted coverage service. | Use it in one release cycle and evaluate review burden, drift, and CI portability. |

## SpecScore Integration

- **New Features this would create:** `fleet-quality/coverage-diff` and a
  versioned baseline artifact contract.
- **Existing Features affected:** [Fleet Quality](../features/fleet-quality/README.md)
  supplies current coverage and reliability controls; `wb ci audit` may later
  inspect whether a repository declares an explicit no-regression policy.
- **Dependencies:** local Go toolchain, existing Git refs or a reviewed YAML
  baseline, temporary worktree support, and the current coverage profile
  parser.

## Open Questions

1. Should `--changed` use changed package directories, changed Go modules, or
   both as separately labelled scopes?
2. How should generated code and test-only package changes contribute to a
   changed-module coverage gate?
3. Should baseline files be repository-local, fleet-level, or supported in
   both locations with explicit precedence?
4. What normalized coverage measure, if any, can safely combine Go statements
   with future TypeScript branches and Python lines?
