---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Dependency and layering policy

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-policy?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-policy?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-policy?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/dependency-policy?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

One declarative policy states which kinds of repository may depend on which
kinds of dependency, and which direction imports may travel between packages
inside a repository. `wb deps policy` applies it to a repository in CI and
across a fleet from the terminal.

## Problem

A fleet that enforces an architecture boundary usually does it with a `git
grep` in each repository's workflow file, with the allowlist written into the
pattern. Three things follow, and they compound.

A regex cannot express "any *sibling* implementation repository" — only a
literal list, which drifts the moment the organization gains a repository. It
knows nothing about the importing package, so direction inside a repository is
unexpressible. And an exception is typographically identical to a rule:
widening one is a one-word diff that reviews as a typo fix rather than as a
decision to take on architectural debt. Nobody can ask how many exceptions the
fleet is carrying, because the answer is spread across several shell scripts in
several encodings.

## Behavior

### Policy document

#### REQ: ordered-first-match-classification

Groups and types MUST be declared as ordered sequences and resolved
first-match-wins. Overlap is expected and useful — `ext-*/backend` above
`*/backend` reads as "contracts, then everything else" — so precedence MUST be
positional and visible in the document rather than inferred from specificity.

#### REQ: allowlists-without-a-deny-list

A scope MUST declare only which groups it permits. There MUST be no deny list
and no per-repository exception, baseline, or severity mechanism. An import
matching no declared group MUST be classified `unclassified` and refused, so a
policy that meets a new kind of dependency fails closed.

#### REQ: policy-carries-its-own-assertions

A policy MAY declare `expect:` entries asserting that a given import resolves
to a given group, or a given module to a given type. `wb deps policy test` MUST
exercise them, and MUST fail a policy that declares none, because reordering
two patterns otherwise changes every verdict downstream with nothing to show
for it.

#### REQ: unreachable-patterns-are-reported

`wb deps policy validate` MUST report a group or detect pattern that an earlier
declaration already claims in full. It MUST NOT report a group that no type
allows: a group nobody permits is how a policy forbids something.

### Repository declaration

#### REQ: repositories-tighten-but-never-loosen

A repository's config file MUST accept only `policy`, `type`, and
`strict: true`. Declaring groups or types, extending an allow list, setting a
rule mode, or `strict: false` MUST be refused as a usage error naming the rule.

#### REQ: source-named-release-resolved

A repository MUST name which policy governs it and MUST NOT pin a release of
it. A pinned reference MUST be refused, so that a tightened rule reaches every
repository at once rather than waiting for each to opt in.

#### REQ: mode-is-central

Whether a rule family enforces or merely reports MUST be declared in the
central policy. A repository MUST NOT be able to demote a rule; `strict` MAY
only promote report-mode findings to failures locally.

### Evidence

#### REQ: lexical-scan-only

The scan MUST read Go import blocks and `go.mod` and MUST NOT type-check,
resolve, or download modules. A verdict MUST be produced for a repository whose
build cannot start. Files that fail to parse MUST be reported rather than
skipped silently.

#### REQ: requirements-judged-where-used

An indirect `go.mod` requirement MUST NOT be reported, because a repository
cannot act on it. A direct requirement MUST be judged in the scope it is
actually imported in, so a module required directly but used only from
`_test.go` is a test dependency.

#### REQ: composition-roots-are-a-scope

Files in `package main` MUST form a `main` scope distinct from `source` and
`tests`, so a policy can permit a composition root to wire concrete drivers
without permitting the rest of the repository to do so.

### Layers

#### REQ: imports-travel-down-the-order

Package roles MUST be ordered outermost-first, and an import from a shallower
role to a deeper one MUST be permitted while the reverse MUST be reported.
Roles MUST be resolved from the first path segment of a package directory.

#### REQ: forbidden-edges-are-declared

Individual role-to-role edges that the depth order permits MAY be refused by
naming them explicitly with a reason. The depth rule alone cannot express
"delivery must go through the facade", and stating such an edge in the policy
keeps it visible rather than hidden in the tool.

### Reporting

#### REQ: documented-exit-codes

`check` MUST exit 0 when clean, 1 when an enforcing rule is violated, and 2
when the invocation or policy is unusable. Report-mode findings MUST be printed
and counted and MUST NOT affect the exit code.

#### REQ: verdicts-are-interrogable

`explain` MUST name the winning group, the matching pattern and its position,
every later pattern that would also have matched, the repository type and how
it was chosen, and the verdict in each scope. `show` MUST print the effective
rules a repository is held to.

#### REQ: fleet-coverage-is-visible

`report` MUST aggregate findings by rule across selected repositories and MUST
count modules that no policy governs. `drift` MUST list which module runs which
policy and where a declared type disagrees with detection. `impact` MUST diff a
candidate policy's verdicts against the policy each repository runs today.

#### REQ: annotations-cannot-be-forged

GitHub-format output MUST escape workflow-command delimiters, because a finding
quotes an import path taken from the scanned repository.

## Acceptance Criteria

### AC: sibling-implementation-import-is-refused

**Requirements:** dependency-policy#req:ordered-first-match-classification, dependency-policy#req:allowlists-without-a-deny-list, dependency-policy#req:documented-exit-codes

**Given** an extension-implementation repository importing another
implementation repository's packages
**When** `wb deps policy check` runs
**Then** each import is reported with its file, line and group, the command
exits 1, and the same import from a declared contract repository is permitted.

### AC: test-and-composition-scopes-differ-from-source

**Requirements:** dependency-policy#req:requirements-judged-where-used, dependency-policy#req:composition-roots-are-a-scope

**Given** a database adapter imported only from `_test.go`, and another
imported from a `package main` composition root
**When** the policy permits adapters in `tests` and `main` but not in `source`
**Then** neither is reported, the same adapter imported from an ordinary
package is reported, and a direct `go.mod` requirement used only by tests is
attributed to the tests scope.

### AC: a-repository-cannot-grant-itself-an-exception

**Requirements:** dependency-policy#req:repositories-tighten-but-never-loosen, dependency-policy#req:source-named-release-resolved, dependency-policy#req:mode-is-central

**Given** a repository config that extends an allow list, declares groups or
types, sets a rule mode, says `strict: false`, or pins a policy release
**When** any policy command loads it
**Then** the command exits 2 with a message naming the tighten-never-loosen
rule, and `strict: true` is accepted and only ever promotes findings.

### AC: an-ordering-mistake-is-findable

**Requirements:** dependency-policy#req:unreachable-patterns-are-reported, dependency-policy#req:policy-carries-its-own-assertions, dependency-policy#req:verdicts-are-interrogable

**Given** a policy whose broad group pattern is declared above a narrow one
**When** `validate` runs, and `explain` is asked about a path both match
**Then** validate reports the narrow pattern as unreachable and exits 1,
explain names the winning pattern's position and lists the shadowed match, and
the policy's own assertions fail.

### AC: a-broken-build-still-produces-a-verdict

**Requirements:** dependency-policy#req:lexical-scan-only

**Given** a module containing a file that does not compile, and another whose
import block cannot be parsed
**When** `check` runs with no module cache and no credentials
**Then** imports are still read from the file that does not compile, the
unparseable file is named in the output as not checked, and the verdict covers
the rest of the module.

### AC: layer-direction-is-reported-without-gating

**Requirements:** dependency-policy#req:imports-travel-down-the-order, dependency-policy#req:forbidden-edges-are-declared, dependency-policy#req:mode-is-central

**Given** a policy declaring layers in report mode, and a repository importing
upward as well as across a declared forbidden edge
**When** `check` runs, and again with `--strict`
**Then** both findings are printed and counted without affecting the exit code,
the forbidden edge carries its declared reason, and `--strict` makes both
blocking.

### AC: the-fleet-picture-is-honest

**Requirements:** dependency-policy#req:fleet-coverage-is-visible, dependency-policy#req:annotations-cannot-be-forged

**Given** a selection of repositories, some governed by a policy and some not
**When** `report`, `drift` and `impact` run
**Then** ungoverned modules are counted rather than omitted, findings are
grouped by rule with the repositories holding them, a candidate policy's newly
failing repositories are listed, and GitHub-format output escapes workflow
delimiters so a finding cannot forge an annotation.

## Open Questions

1. Does the fleet policy forbid every measured layer edge, or only inversions
   and facade-skipping? Deferred while layers ship in report mode; the answer
   is written as `layers.forbid` entries, not as a code change.
2. Which repository types cover the modules the first Sneat policy does not
   detect — CLIs, deployable services, and `e2e` modules? Measured 2026-08-21:
   18 of 54 Go modules under `sneat-co/*` match no type.
3. Should `report` and `drift` remain separate verbs, or is `report` better as
   `check --fleet` with the exit code suppressed?
