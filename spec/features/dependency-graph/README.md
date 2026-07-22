---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Dependency Graph

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-graph?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-graph?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-graph?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-graph?op=request-change) |

**Status:** Implementing
**Source Ideas:** —

## Summary

`wb deps graph` discovers one canonical dependency-evidence graph and projects
it as repository-to-repository, dependency-to-repository, or selected-version-
to-repository relationships. It writes deterministic machine and human
artifacts, renders accessible SVG, and can explicitly open a self-contained
interactive HTML report in the user's browser.

The first adapter is Go. The canonical model is ecosystem-neutral enough for
future Python and TypeScript adapters; views and renderers do not rescan or
reinterpret manifests.

## Problem

Dependency migrations are difficult to reason about from isolated `go.mod`
files. An operator needs to answer different questions from the same evidence:
which repositories must release first, which repositories consume a module,
and where versions have drifted. Separate scanners for each question would
produce inconsistent counts and hide the manifest evidence needed by humans,
automation, and AI agents.

## Behavior

### Command and discovery

#### REQ: canonical-command

The command MUST accept `wb deps graph [repository-path]`. Go MUST be the
initial and default ecosystem. `--fleet` MUST use the same repository discovery,
`--match`, `--regex`, `--filter`, `--org`, `--ref`, and clone layout as the
other dependency commands. Without `--fleet`, the supplied repository or the
current repository MUST be inspected. Fleet discovery MUST treat linked Git
worktrees as alternate checkouts of their canonical repository, not as
additional fleet repositories or module providers.

#### REQ: canonical-evidence-model

One scan MUST produce a canonical sorted model of repositories, declared
modules, and requirements. Every requirement MUST retain its consumer module,
consumer repository, manifest path, selected version, and direct-versus-
indirect evidence. Internal provider repositories MUST be resolved only from
module declarations in the selected graph; unknown providers MUST remain
external dependencies rather than being guessed. Duplicate module declarations
MUST be retained as ambiguous provider candidates in this read-only report;
unlike a mutation planner, visualization MUST NOT fail or silently choose one.
When a `github.com/<owner>/<repository>` module has exactly one declaration in
that same `<owner>/<repository>` checkout, WB MAY label that declaration as the
canonical visual provider while still reporting every candidate and counting
the module as ambiguous. Mutation planning MUST continue to reject all
duplicate declarations, including this resolvable visualization case.

#### REQ: adapter-boundary

Manifest discovery MUST remain behind an ecosystem adapter boundary. The Go
adapter MUST use `golang.org/x/mod/modfile`. Future Python and TypeScript
adapters MAY add evidence to the same canonical model without changing view
projection, report serialization, SVG layout, or browser opening.

### Projections

#### REQ: repository-view

`--view repos` MUST project internal provider repository to consumer repository
edges. Multiple module requirements between the same repositories MUST be
aggregated visually while retaining their individual evidence in the canonical
report. This view is intended for release order, cycles, and propagation blast
radius.

#### REQ: dependency-view

`--view dependencies` MUST project dependency or module nodes to consuming
repository nodes. It MUST include external dependencies and MUST answer which
selected repositories consume a named dependency.

#### REQ: selection-view

`--view selections` MUST project `dependency@version` nodes to consuming
repository nodes. Versions MUST be reported as observed, not described as
registry-latest unless a registry query supplied that evidence. The renderer
MAY identify the highest comparable version observed in this fleet and MUST
label that state as fleet-relative.

#### REQ: stable-projection-identity

Every projected node and edge MUST have a deterministic identifier derived
from canonical evidence. All views MUST report consistent repository,
requirement, and version counts. Switching views MUST NOT trigger another
repository or manifest scan.

### Reports and browser visualization

#### REQ: deterministic-report-set

Every run MUST be able to write `deps-graph.md`, `deps-graph.yaml`,
`deps-graph.json`, `deps-graph.svg`, and `deps-graph.html`. YAML and JSON MUST
contain the canonical evidence graph in deterministic order. Markdown MUST
summarize counts and list requirement evidence. SVG MUST render the selected
default view. HTML MUST be self-contained and embed all supported projections.

#### REQ: accessible-svg

SVG nodes MUST expose text labels, keyboard focus, stable IDs, and accessible
descriptions. Providers MUST flow toward consumers. Direct and indirect edges
MUST be visually distinguishable. Cycles MUST render deterministically rather
than making graph generation fail. A legend MUST explain node, edge, and
fleet-relative version status.

#### REQ: interactive-html

The HTML report MUST switch between repository, dependency, and selection
projections without network access. It SHOULD support text search, outdated-
selection highlighting, node-path highlighting, zoom, reset, and a readable
mobile fallback. All manifest-derived text MUST be escaped before inclusion in
SVG or HTML.

#### REQ: explicit-code-intelligence-drilldown

Repository-backed nodes MUST expose deterministic GitHub and CodeGrapher
drill-down links derived from the canonical `owner/repository` identity. The
HTML report MUST show those links in selected-node details and Markdown MUST
link requirement consumers to their code graph. Report generation MUST NOT
probe CodeGrapher, publish a snapshot, trigger indexing, or otherwise make a
network request. External dependencies without a resolved provider repository
MUST NOT receive an invented CodeGrapher target. Navigation MUST happen only
after an explicit user click and MUST preserve the local report.

#### REQ: explicit-browser-open

Browser opening MUST be opt-in through `--open`. WB MUST first finish writing
the report set, then open the local HTML path using the operating system's
browser mechanism. Headless and CI runs without `--open` MUST never attempt a
GUI action. An open failure MUST preserve the completed report and return the
path with an actionable error.

#### REQ: scoped-dependency-filter

Repeatable `--dependency` filters MUST limit rendered evidence to exact module
identities while retaining the repositories and internal providers needed to
interpret matching edges. Repository filters MUST run before cloning or graph
discovery; dependency filters MUST run against canonical evidence afterward.

## Synthetic Use Cases

### UC: plan a provider-first release wave

Fictional modules `data/record`, `data/dal`, two storage adapters, a facade,
and an API span five repositories. The operator runs:

```text
wb deps graph --fleet --match 'data-*/*' --view repos --open
```

The repository view flows record to DAL, adapters, facade, and API. Selecting
DAL highlights its upstream provider and all downstream consumers. Two module
requirements between one adapter and facade are one visual repository edge but
remain two evidence rows in YAML and Markdown.

### UC: find the blast radius of one dependency

A security advisory affects `example.org/crypto`. The operator runs:

```text
wb deps graph --fleet --dependency example.org/crypto \
  --view dependencies --format markdown
```

The report lists every selected consuming repository, module, manifest, and
direct or indirect requirement. It does not claim that unselected repositories
are unaffected and does not invent a provider repository for the external
module.

### UC: explain fleet-relative version drift

Four repositories select three versions of `example.org/model`. The selection
view creates one node per observed `module@version`, connects each consuming
repository, marks lower comparable versions as behind the highest version seen
in this scan, and labels the comparison “fleet highest,” not “latest.”

### UC: headless report remains deterministic

CI runs the same graph command twice without `--open`. No browser process is
started. With unchanged `origin/main` manifests, YAML, JSON, Markdown, and SVG
are byte-for-byte stable and provide the same canonical counts for an AI agent.

### UC: a dependency cycle is visible

Fictional repositories `acme/a` and `acme/b` declare modules that require each
other. Repository projection places them in one deterministic cycle layer and
draws both directed edges. Visualization succeeds even though dependency bump
would require a coordinated cyclic-release protocol.

### UC: drill from fleet impact into code impact

An operator selects `data/storage-firestore` in the repository projection.
The inspector reports its repository identity and connected fleet nodes, then
offers **Explore code in CodeGrapher**. Opening it navigates to the deterministic
CodeGrapher repository route in a new tab. The original offline WB report stays
open; WB neither checks for nor publishes a CodeGrapher snapshot. Requirement
evidence also links directly to each consumer repository so an operator can
inspect where the dependency is used.

### UC: campaign worktrees do not duplicate fleet members

An operator has canonical `acme/widgets` plus linked worktrees named
`widgets-refactor` and `widgets-hotfix` under the same projects tree. A fleet
graph contains `acme/widgets` once. The alternate checkout names do not become
invented repository identities and duplicate module declarations do not create
a false ambiguous provider.

## Interaction with Other Features

[Dependency Bump Waves](../dependency-bump-waves/README.md) consumes the same
Go graph evidence for release planning. [Exact Dependency
Set](../dependency-set/README.md) supplies mutation lifecycle primitives but
does not alter this read-only command. A future [Dependency
Drift](../dependency-drift/README.md) command can consume the canonical graph
instead of implementing another manifest scanner.

[CodeGrapher](https://codegrapher.dev/) complements this fleet-level evidence
with repository-level symbols, imports, calls, implementations, and impact.
CodeGrapher can link back to `https://wb.sneat.dev/` with `repository`, `ref`,
and `view` query context plus the `#deps-graph` fragment. This public link is a
navigation contract only: hosted WB graph publication remains a separate,
explicit future capability.

## Acceptance Criteria

### AC: one-scan-three-projections

**Requirements:** dependency-graph#req:canonical-command, dependency-graph#req:canonical-evidence-model, dependency-graph#req:adapter-boundary, dependency-graph#req:repository-view, dependency-graph#req:dependency-view, dependency-graph#req:selection-view, dependency-graph#req:stable-projection-identity

**Given** a selected fleet contains internal providers, external dependencies,
multiple versions, indirect requirements, and a repository cycle
**When** each graph view is generated
**Then** all views derive from one sorted evidence model, preserve consistent
counts, display the expected relationships, and retain every manifest source.

### AC: deterministic-accessible-browser-report

**Requirements:** dependency-graph#req:deterministic-report-set, dependency-graph#req:accessible-svg, dependency-graph#req:interactive-html, dependency-graph#req:explicit-browser-open

**Given** graph evidence contains characters requiring HTML escaping
**When** deterministic reports are written and `--open` is requested
**Then** Markdown, YAML, JSON, SVG, and self-contained HTML are safe and stable,
the SVG is keyboard-readable, and the browser opens only after all files exist.

### AC: filters-preserve-explanatory-context

**Requirements:** dependency-graph#req:scoped-dependency-filter, dependency-graph#req:canonical-evidence-model, dependency-graph#req:dependency-view

**Given** repository filters select one organization and a dependency filter
selects one exact module
**When** the graph is generated
**Then** no unselected repository is cloned, every matching consumer evidence
row remains, and required internal provider context is present without unrelated
dependency edges.

### AC: code-intelligence-links-are-passive

**Requirements:** dependency-graph#req:explicit-code-intelligence-drilldown, dependency-graph#req:interactive-html, dependency-graph#req:deterministic-report-set

**Given** canonical evidence contains internal GitHub repositories and an
external dependency without a provider
**When** Markdown, SVG, and HTML reports are generated offline
**Then** internal repository nodes and consumer evidence expose deterministic
GitHub and CodeGrapher links, the external dependency has no invented code
graph, and report generation performs no network or publication action.

## Open Questions

- Should a future registry-enriched mode be part of `deps graph`, or should
  `deps drift` annotate a saved canonical graph with online latest-version
  evidence?
- Should very large fleets default to organization clusters or dependency
  communities in the interactive view?

---
*This document follows the https://specscore.md/feature-specification*
