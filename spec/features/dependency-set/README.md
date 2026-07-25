---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Exact Dependency Set

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-set?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-set?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-set?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-set?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb deps set <ecosystem> <dependency>@<version>` changes existing references
to one operator-selected version in a repository or selected fleet. It is the
deterministic counterpart to `wb deps bump`: it does not discover a preferred
version or propagate newly published provider releases. It creates isolated
worktrees, delegates manifest changes to ecosystem adapters, verifies every
changed repository, and can optionally commit, push, open pull requests, wait
for CI, and merge.

For Go, `--propagate` is command sugar for seeding
`wb deps bump --changed <dependency>@<version>` with the same scope and
publication options. It delegates to the bump wave planner instead of
maintaining a second propagation implementation inside exact set.

## Problem

Shared build infrastructure and application dependencies sometimes need one
coordinated, exact rollout. Repeating hand-written search-and-replace commands
across repositories loses provenance, can overwrite dirty checkouts, leaves
mutable GitHub Actions tags in workflows, and makes it hard to distinguish an
already-current repository from one where the dependency was absent. Operators
need one auditable command whose target is fixed before any repository changes.

## Behavior

### Target and scope

#### REQ: canonical-command-and-identity

The command MUST accept a registered ecosystem and a fully qualified dependency
identity followed by `@<version>`. Initial forms are
`wb deps set github-actions strongo/cicd@v1.10.5` and
`wb deps set go github.com/dal-go/dalgo@v0.63.1`. A shorthand such as `cicd`
MUST NOT guess an owner. The target identity and version MUST be resolved once
before repository work starts.

#### REQ: existing-references-only

By default WB MUST change only repositories whose configured base ref already
references the dependency. An absent dependency MUST be reported as skipped
with its checked ref and reason; it MUST NOT be added. Adding a new dependency
requires a future or explicit `--add` mode and is outside the initial command.

#### REQ: repository-and-fleet-scope

Without `--fleet`, the command MUST operate on the supplied repository path or
the current repository. With `--fleet`, WB MUST reconcile non-archived
repositories in the authenticated user's organizations with repositories
already cloned below `--projects-root`. `--filter`, `--match`, `--regex`, and
repeatable `--org` filters MUST be applied before mutation. Missing selected
repositories MAY be cloned into `<projects-root>/<org>/<repo>`.

#### REQ: no-implicit-downgrade

When both the observed and target versions are comparable semantic versions,
WB MUST reject a lower target by default and report both values.
`--allow-downgrade` MUST be required to apply it. An opaque current reference
whose version cannot be proven MUST be reported as unknown rather than guessed.

### Ecosystem adapters

#### REQ: immutable-github-actions-reference

The `github-actions` adapter MUST find matching `uses:` values below
`.github/workflows` while preserving the referenced action or reusable-workflow
subpath. It MUST resolve a supplied version tag to its immutable Git commit,
write the commit SHA after `@`, and retain `# <version>` as the human-readable
version. It MUST distinguish an already-current SHA/tag, an updated reference,
an absent reference, and an unresolvable tag.

#### REQ: go-tool-owned-update

The `go` adapter MUST locate existing requirements in every non-vendored
`go.mod`, invoke official Go tooling with the exact requested module version,
run `go mod tidy`, and inspect the selected result. It MUST NOT implement a Go
dependency solver. A selected version different from the exact target MUST fail
with the before, target, and selected versions in the audit.

#### REQ: private-go-module-environment

For private Go module patterns supplied with repeatable `--go-private`, WB MUST
extend Go subprocess `GOPRIVATE`, `GONOPROXY`, and `GONOSUMDB` settings for the
operation only. It MUST preserve inherited patterns, avoid the public proxy and
checksum database for the supplied patterns, and never store, print, or invent
credentials. Direct Git fetches MUST use the operator's existing Git credential
helper; WB errors MUST retain the attempted command and sanitized Go/Git output.

#### REQ: adapter-boundary

Ecosystem discovery, target resolution, mutation, and result inspection MUST be
implemented behind an adapter boundary so Python and TypeScript package-manager
adapters can be added without changing fleet orchestration or publication.
Requesting an unregistered ecosystem MUST fail before any repository changes.

#### REQ: propagation-delegates-to-bump

`wb deps set go <module>@<version> --propagate` MUST translate the exact target
into one bump release event and delegate planning, wave recalculation, release
observation, reports, resume, and publication to `wb deps bump`. Propagation
MUST require fleet scope. It MUST be rejected for ecosystems without a module
dependency graph, including `github-actions`. Exact set and bump MUST share one
repository lifecycle engine.

#### REQ: opt-in-dependency-order

`--dependency-order` MUST process the selected repositories in the
provider-first layers of [Dependency Graph](../dependency-graph/README.md)
instead of one batch. The layering MUST come from what the selected checkouts
declare — their own module paths and requirements — never from repository
names, organizations, or directory layout. It MUST be rejected for ecosystems
without a module graph, including `github-actions`, and MUST be rejected
together with `--propagate`, which owns release-event waves.

Ordering MUST NOT wait for a release. A layer boundary is a *sequencing*
boundary, not a publication one: WB orders the work and reports the boundary,
and the operator decides when the next layer may start. Waiting for published
provider versions and requeuing dependants remains exclusively
[Dependency Bump Waves](../dependency-bump-waves/README.md); exact set MUST NOT
grow a second release-observation loop.

Without `--dependency-order`, WB MUST behave exactly as it does today: one
batch, no graph scan, and no layer plan in the reports.

#### REQ: layers-fail-closed-downstream

Layers MUST execute in ascending order, and a later layer MUST NOT start after
an earlier layer failed. This does not weaken
`dependency-set#req:complete-independent-results`: repositories inside one
layer are independent of each other and MUST keep `--parallel` concurrency and
complete independently, whereas a repository in a later layer is by
construction *not* independent of a failed provider layer. Repositories in
unstarted layers MUST be reported as blocked, naming the layer that failed,
rather than receiving a pull request no CI run can turn green.

#### REQ: operator-driven-layer-selection

`--layer` MUST restrict an ordered run to one layer (`N`), a closed range
(`N-M`), or every layer from one index onwards (`N-`), so an operator can land
layer 0, wait for its releases, and continue with layer 1 using the same
command. It MUST require `--dependency-order`. Layers outside the selection
MUST be reported as not selected and MUST NOT be inspected or cloned. Markdown
and YAML MUST record the requested selection, every layer, its repositories,
and whether it completed, failed, was blocked, or was not selected.

### Worktrees, verification, and publication

#### REQ: isolated-canonical-checkouts

Fleet work MUST fetch the configured remote ref and create a dedicated branch
and worktree below `<projects-root>/.wb/worktrees/<operation>/<org>/<repo>`.
Canonical clones MUST remain unchanged even when dirty or on another branch.
`--ref` MUST select the base ref. `--resume` MUST accept only the expected
operation branch and preserve completed external actions.

#### REQ: verification-by-default

Every changed repository MUST run applicable lint, test, and build checks by
default. `--checks` MAY select a subset; `--no-verify` MUST explicitly disable
them. `--timeout` MUST bound external checks and CI waiting, and `--retry` MUST
retry failed external checks without hiding prior failure details. No failed
local verification may be committed or published.

#### REQ: cumulative-publication-flags

Without publication flags, WB MUST leave verified changes in local operation
worktrees. `--commit` MUST commit them; `--push` MUST imply commit; `--pr` MUST
imply push and commit; and `--merge` MUST imply PR, push, and commit. Pull
requests MUST be ordinary reviewable PRs and MUST be reused on resume rather
than duplicated.

#### REQ: concurrent-ci-aware-merge

`--parallel=N` MUST bound concurrently processed repositories. When `--merge`
is supplied, a worker MUST continue processing other independent repositories
while already-open PRs run CI. WB MUST wait until each PR has observed checks
and all checks pass or are explicitly skipped before merging it through normal
GitHub branch protection. Failed, cancelled, pending past `--timeout`,
conflicted, or checkless PRs MUST remain unmerged and auditable.

### Reports and recovery

#### REQ: deterministic-dual-report

Every run MUST write a linked Markdown index and deterministic YAML manifest.
For each selected repository the reports MUST record its base ref, canonical
clone, operation worktree and branch when created, observed reference/version,
target version, resolved immutable reference when applicable, changed files,
verification results, commit, push, PR, checks, merge state, final status, and
an explicit reason. Detailed patches MUST remain available through the recorded
Git diff command instead of being duplicated in the report.

#### REQ: complete-independent-results

A failure in one repository MUST NOT prevent independent repository work that
can safely continue. WB MUST return non-zero after writing the complete report
when any selected repository fails. `--resume` MUST reuse successful work and
retry only incomplete stages without duplicating commits or pull requests.

## Synthetic Use Cases

### UC: pin a reusable workflow across organizations

Fictional repositories in `acme-api`, `acme-data`, and `acme-tools` use
`acme/cicd/.github/workflows/go.yml@<old-sha> # v1.8.0`. The operator runs:

```text
wb deps set github-actions acme/cicd@v1.9.2 --fleet \
  --parallel=2 --commit --push --pr --merge
```

WB resolves `v1.9.2` once, rewrites each subpath to the same immutable SHA,
verifies two repositories concurrently, and opens PRs. While the first PR runs
CI, the second worker continues local work. Only green PRs merge; Markdown and
YAML link every PR and show `v1.8.0`, `v1.9.2`, and the resolved SHA.

### UC: dirty canonical checkout stays untouched

The fictional `acme/payments` clone has uncommitted feature work on a topic
branch. Its `origin/main` uses `acme/cicd@v1.8.0`. WB fetches without checking
out the canonical clone, branches an operation worktree from `origin/main`, and
updates that worktree. The topic branch, index, working files, and untracked
files are byte-for-byte unchanged.

### UC: absent action and opaque version are explained

The fictional `acme/docs` repository has workflows but no `acme/cicd` use, and
`acme/legacy` uses a commit SHA with no version comment. Docs is skipped with
the checked ref and “dependency absent.” Legacy is eligible for an exact set,
but its prior semantic version is reported as unknown; WB does not invent a
version or describe the operation as an upgrade or downgrade.

### UC: downgrade requires explicit authority

The fictional `acme/service` uses `acme/cicd@<sha> # v1.9.2`. Setting
`acme/cicd@v1.8.0` fails before the file is written and records both versions.
Repeating with `--allow-downgrade` performs the exact pin and includes the
explicit downgrade decision in both reports.

### UC: exact Go version is selected by Go tooling

Two modules in the fictional `acme/facade` repository require
`example.org/model v0.4.0`. Running
`wb deps set go example.org/model@v0.5.1` invokes `go get` and `go mod tidy` in
both module roots, records resulting `go.mod` and `go.sum` changes, and verifies
the whole repository. If another requirement forces `v0.6.0`, WB reports the
selected mismatch and does not commit.

### UC: update a private GitHub module without exposing it to public services

The fictional `acme/app` module already requires private
`github.com/acme/private-sdk`. Its developer has configured Git credentials
(for GitHub CLI this can be `gh auth setup-git`) but does not want private
module paths sent to a public proxy or checksum database. They run:

```text
wb deps set go github.com/acme/private-sdk@v1.4.0 \
  --go-private github.com/acme --commit
```

WB passes the pattern only to the Go subprocesses it creates, merging it with
any inherited Go privacy settings. `go get`, `go mod tidy`, and final module
inspection fetch directly through the configured Git credential helper. No
token appears in WB flags, worktrees, reports, or logs.

### UC: a breaking release is rolled out one layer at a time

A fictional `example.org/dal` release breaks its API, and fifty-six selected
repositories form nine provider-first levels. Opening every pull request at
once would pin a version the deepest providers have not released yet, so those
pull requests could never go green. The operator first reads the plan:

```text
wb deps set go example.org/dal@v0.64.2 --fleet --dependency-order --dry-run
```

The report lists layers `00` to `08` with their repositories. The operator then
lands one layer, waits for its releases, and continues:

```text
wb deps set go example.org/dal@v0.64.2 --fleet --dependency-order --layer 0 --pr
wb deps set go example.org/dal@v0.64.2 --fleet --dependency-order --layer 1 --pr
```

Layers `01` to `08` are reported as not selected during the first run. WB never
polls for a release; the operator owns the pause between layers.

### UC: a failed provider layer blocks only its dependants

The fictional layer `02` contains three repositories and one fails local
verification. Its two independent siblings still complete, and the run exits
non-zero. Layers `03` and above are reported as blocked, naming layer `02`, and
none of their repositories is cloned, inspected, or given a pull request.
`--resume` continues after the verification failure is fixed.

### UC: CI failure is resumed without duplicate PRs

Three fictional repositories are changed. Two PRs pass and merge while one
lint job fails. The first run exits non-zero with all three outcomes. After the
lint issue is fixed in the operation worktree, the operator runs the same
command with `--resume --merge`; WB reuses the existing branch and PR, does not
recommit or reopen the merged work, and completes the remaining green merge.

## Interaction with Other Features

[Dependency Drift](../dependency-drift/README.md) identifies version divergence
before or after an exact set. [Dependency Bump Waves](../dependency-bump-waves/README.md)
discovers and propagates release events when the desired version is not fixed
in advance. [Fleet Quality](../fleet-quality/README.md) supplies the conventional
verification checks reused before publication.

## Acceptance Criteria

### AC: github-action-is-immutably-and-safely-set

**Requirements:** dependency-set#req:canonical-command-and-identity, dependency-set#req:existing-references-only, dependency-set#req:no-implicit-downgrade, dependency-set#req:immutable-github-actions-reference

**Given** matching, absent, already-current, opaque, and newer semantic GitHub
Actions references exist in five fictional repositories
**When** an exact tag is set without `--allow-downgrade`
**Then** matching older and opaque references use the resolved SHA and target
comment, absent and already-current references are skipped with reasons, and
the newer semantic reference fails unchanged as a blocked downgrade.

### AC: dirty-fleet-publication-is-isolated-and-gated

**Requirements:** dependency-set#req:repository-and-fleet-scope, dependency-set#req:isolated-canonical-checkouts, dependency-set#req:verification-by-default, dependency-set#req:cumulative-publication-flags, dependency-set#req:concurrent-ci-aware-merge

**Given** a selected fleet includes a dirty canonical checkout and independent
repositories with passing and failing CI
**When** the operator runs with `--parallel=2 --merge`
**Then** canonical checkouts remain untouched, verification precedes commits,
independent work continues while CI runs, green PRs merge normally, and failed
or timed-out PRs remain open.

### AC: go-set-and-reports-are-deterministic

**Requirements:** dependency-set#req:go-tool-owned-update, dependency-set#req:adapter-boundary, dependency-set#req:deterministic-dual-report, dependency-set#req:complete-independent-results

**Given** a fleet has two Go repositories selecting the target and one whose
module graph forces a different version
**When** the exact Go dependency version is set
**Then** official Go tooling updates and tidies every existing requirement,
successful repositories continue independently, the forced mismatch fails
before publication, and sorted Markdown and YAML record every decision and
Git diff command.

### AC: exact-propagation-is-one-seed-for-bump

**Requirements:** dependency-set#req:canonical-command-and-identity, dependency-set#req:propagation-delegates-to-bump, dependency-set#req:cumulative-publication-flags

**Given** an exact Go provider version is published and selected fleet
dependants span several release layers
**When** the operator uses `deps set go <module>@<version> --fleet --propagate`
**Then** WB creates one initial bump event, uses the shared typed lifecycle and
wave report, and allows observed consumer releases to become later dependency
events without running a second exact-set propagation implementation.

### AC: dependency-order-sequences-without-waiting

**Requirements:** dependency-set#req:opt-in-dependency-order, dependency-set#req:layers-fail-closed-downstream, dependency-set#req:operator-driven-layer-selection, dependency-set#req:deterministic-dual-report

**Given** a selected fleet whose alphabetical order disagrees with its declared
module dependencies, and a run that requests one layer
**When** the operator uses `--dependency-order --layer N`
**Then** WB derives the layering from the declared modules, processes only that
layer, reports every other layer as not selected without inspecting it, records
the layer plan and selection in Markdown and YAML, and returns without waiting
for any release; a failing layer leaves later layers blocked and unstarted.

## Open Questions

- Should `--dependency-order` eventually offer a supervised wait between
  layers, or does that belong exclusively to `deps bump` release waves?
- Should `--add` be one cross-ecosystem flag or require adapter-specific data
  such as a reusable-workflow path and a Go direct-versus-indirect choice?
- Should a future npm adapter interpret `--allow-downgrade` from the declared
  range, lock-file selection, or both?

---
*This document follows the https://specscore.md/feature-specification*
