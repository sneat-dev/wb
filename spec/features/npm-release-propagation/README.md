---
format: https://specscore.md/feature-specification
status: Stable
---

# Feature: NPM release propagation

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-release-propagation?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-release-propagation?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-release-propagation?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-release-propagation?op=request-change) |
**Status:** Stable
**Source Ideas:** —

## Summary

Publish approved npm packages through repository-owned GitHub Actions workflows, verify exact registry evidence, and hand confirmed releases to recalculated dependency waves.

## Problem

Publishing several npm packages currently requires an operator to dispatch
repository-specific release workflows, identify the exact workflow runs, wait
for npm visibility, and then manually seed `wb deps bump npm`. That sequence is
easy to duplicate, easy to resume incorrectly, and can start a consumer wave
before the provider's registry evidence exists.

## Behavior

### Command and explicit release tuples

#### REQ: explicit-npm-release-tuples

WB MUST expose `wb deps publish npm` (with `wb deps release npm` as an alias).
Every invocation MUST supply aligned repeatable `--repo`, `--workflow`,
`--package`, and `--version` values. A workflow MUST be a repository-owned
`.yml` or `.yaml` file, a package MUST be a valid npm package name, and a
version MUST be an exact npm semver without a range or a `v` prefix. Duplicate
tuples MUST be rejected before any external command runs. Optional
`--workflow-input key=value` values are passed only as GitHub Actions dispatch
inputs. Workflow-input names resembling credentials (including token, secret,
password, auth, or credential variants) MUST be rejected before persistence or
process arguments; WB MUST NOT accept, construct, persist, or print npm
credentials.

#### REQ: approval-safe-publication

The command MUST plan by default. Plan mode MUST pass its release tuples to the
shared bump engine in dry-run mode so fleet findings and its durable report are
not hidden. A plan's wave report MUST be isolated from apply/resume state so it
cannot overwrite an in-progress or resumable handoff. Only explicit `--apply` may dispatch a workflow, and `--apply`
MUST NOT be combined with `--dry-run`. The workflow remains the repository's
publication and approval policy; WB MUST NOT call `npm publish`, bypass
environment approvals, or modify workflow policy. The downstream `--merge`
stage is a separate explicit approval. Without `--merge`, confirmed release
events MUST reach the shared bump engine in dry-run mode.

### Publication receipt and registry evidence

#### REQ: exact-workflow-receipt

Before dispatching, WB MUST record the exact `origin` branch head and the set
of exact workflow-dispatch run IDs already visible for that repository,
workflow, and head. It MUST dispatch through `gh workflow run`, then locate
one and only one `workflow_dispatch` run at that head that is absent from the
persisted baseline; a bounded clock-skew check is secondary only. The report
MUST retain repository, workflow, ref, head SHA, baseline IDs and timestamp,
run ID, run URL, run head SHA, run status, conclusion, and timestamps. A run
whose head differs from the recorded head or an ambiguous candidate set MUST
fail closed.

#### REQ: exact-registry-evidence

After a successful workflow run, WB MUST query the configured npm registry for
the exact `<package>@<version>` using read-only `npm view` evidence. The
registry version MUST equal the requested version before the event can advance
to dependency propagation. A missing, mismatched, or failed registry query
MUST leave the receipt resumable and MUST leave downstream repositories
untouched.

### Resume and dependency handoff

#### REQ: durable-resume

WB MUST persist the publication report before each external action and after
each state transition. `--resume` MUST require the same release tuples and a
persisted report. A receipt with a dispatch timestamp MUST never dispatch the
same workflow again; resume MUST locate or reobserve its exact run and retry
only the missing registry evidence. Workflow and registry failures MUST retain
the exact receipt and a machine-readable reason.

#### REQ: shared-recalculated-wave-handoff

Once every requested package has exact registry evidence, WB MUST create one
release event per package and invoke the existing `wb deps bump npm` wave
engine with the events as its seeds. It MUST reuse the engine's graph
recalculation, provider-first coalescing, verification, report, and resume
machinery; a parallel publication-specific propagation loop is forbidden.

### Machine-readable output

#### REQ: deterministic-report

`--format json` and `--format yaml` MUST expose publication receipts and the
embedded dependency-wave report with stable operation IDs. Markdown MUST link
workflow runs and identify head, registry version, status, and reason for every
tuple. Standard output MUST remain parseable; diagnostics belong on stderr.
Workflow input values MUST be omitted from durable reports and stdout; a
non-secret fingerprint may be retained only to prove resume identity.

## Acceptance Criteria

### AC: safe-multi-package-publication

**Requirements:** npm-release-propagation#req:explicit-npm-release-tuples, npm-release-propagation#req:approval-safe-publication, npm-release-propagation#req:exact-workflow-receipt, npm-release-propagation#req:exact-registry-evidence

**Given** one provider repository with one package or one provider repository
with two packages and their repository-owned release workflow(s)
**When** an operator runs the command without `--apply`
**Then** WB emits a deterministic plan without dispatching a GitHub workflow,
querying npm, or changing dependency files; the plan runs the shared dry-run
fleet wave and retains its findings. With `--apply`, each package gets a unique
exact-head run and registry receipt before it is considered released.

### AC: resumable-registry-failure

**Requirements:** npm-release-propagation#req:durable-resume, npm-release-propagation#req:exact-registry-evidence

**Given** a workflow run passed but npm has not exposed the requested version
**When** the command exits and is rerun with the same tuples and `--resume`
**Then** WB reuses the recorded run, performs no duplicate workflow dispatch,
and continues only after exact registry evidence is available.

### AC: confirmed-events-reach-shared-waves

**Requirements:** npm-release-propagation#req:shared-recalculated-wave-handoff, npm-release-propagation#req:deterministic-report

**Given** one or more package receipts have passed workflow and registry
verification
**When** the command hands them to downstream propagation
**Then** the same recalculated npm dependency-wave engine used by
`wb deps bump npm --changed ... --fleet --merge` receives all events together,
and the output retains both the publication receipt and wave report.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
