---
format: https://specscore.md/idea-specification
status: Draft
---

# Idea: Declarative dependency and layering policy check

**Status:** Draft
**Date:** 2026-08-21
**Owner:** alex
**Promotes To:** —
**Supersedes:** —
**Related Ideas:** —

## Problem Statement

How might WB let a fleet declare, in one central policy, which kinds of
repository may depend on which kinds of dependency — and enforce it per repo in
CI — so that architecture boundaries stop being hand-rolled regexes with
invisible exceptions?

## Context

Four Sneat repositories enforce an architecture boundary today, and each does it
with its own bash `git grep` inside its own workflow file, with the allowlist
baked into a regex alternation:

| Repository | CI job | Allowlist |
|---|---|---|
| `sneat-co/calendarius` | `architecture` | `calendarius\|ext-calendarius\|ext-contactus` |
| `sneat-co/competios` | `architecture` | `competios\|ext-*` (awk) |
| `sneat-co/togethered` | `architecture` | `togethered\|ext-*` (shell `case`) |
| `sneat-co/gametable` | `backend-guard` | guards `replace` directives only — no import guard |

Three properties make these unfixable in place. The rule is a regex, so it
cannot express "a *sibling* implementation repository" — only a literal list.
An exception is invisible: `ext-contactus` in the calendarius alternation is an
unmarked, unowned, non-expiring grant that reads exactly like the rest of the
pattern. And there is no shared vocabulary of repository kinds, so each repo
restates the whole rule and drifts from the others.

The triggering violation is `sneat-co/gametable`: `backend/facade4gametable` and
`backend/ports4gametable` import `sneat-co/calendarius/backend/{dbo4,dto4}`
directly instead of `ext-calendarius/backend`, breaking the standing
contract-only rule. The 2026-08-19 conformance audit (row 15) records an
`architecture` job that allowed `calendarius/backend` as a known exception — but
that job was in unmerged PR #4. What landed in PR #23 has no import guard at
all, so the violation is currently *unchecked* rather than sanctioned. Either
way, the boundary is not enforced there.

Measured across seven Sneat backends on 2026-08-21, source files only:

- **Cross-repo:** calendarius, contactus, listus, togethered, gameboard and
  competios are all clean under the strict rule. `gametable` is the fleet's only
  violator, with three imports across two packages.
- **DALgo adapters:** competios and gameboard import `dal-go/dalgo2firestore`,
  but exclusively from `_test.go` — emulator-backed repository tests. Test code
  therefore needs its own declared scope, not an exception.
- **Intra-repo layering:** the role order
  `ext/cmd → api/botsvc/delayed → facade → dal → dto/ports → dbo/models → const`
  already holds almost everywhere, with 22 edges breaking it — including
  `dal → api` (competios ×3), `api → dal` (contactus), `api → dbo` (listus),
  `dal → dto` (×4) and `botsvc/delayed → dbo` (×3).

The package roles themselves are documented (`const4`, `api4`, `dbo4`, `dal4`,
`dto4`, `facade4`, `delayed4` in `sneat-co/sneat-go` `AGENTS.md`), but their
permitted *direction* has never been written down.

WB is the natural home. `wb deps graph` already scans Go and npm manifests
across a selected fleet, preserves the manifest evidence, and derives layered
provider→consumer views from one canonical model. Fleet selection, report
artifacts, CI-facing commands (`wb ci audit`, `wb ci wait`) and distribution
already exist here; a separate utility would rebuild all four.

## Recommended Direction

Add `wb deps policy check` — a lexical import-and-manifest scanner driven by a
declarative policy that WB itself knows nothing about. The engine understands
four concepts: **groups** (patterns that classify an import path), **types**
(kinds of repository), **scopes** (`source` and `tests`), and **layers**
(ordered package roles). The Sneat vocabulary lives outside WB, in
`sneat-co/cicd//policy/sneat-backend.yaml`, so `dal-go`, `strongo` and
`bots-go-framework` can each author their own policy against the same engine.

Classification is ordered and first-match-wins, which is what lets a rule say
"any sibling implementation" without enumerating the org:

```yaml
groups:
  - {name: own-repo,                 match: ["<self>/..."]}
  - {name: extension-contract,       match: ["github.com/sneat-co/ext-*/..."]}
  - {name: host,                     match: ["github.com/sneat-co/{sneat-go,sneat-bots}/..."]}
  - {name: shared-kernel,            match: ["github.com/sneat-co/{sneat-go-core,sneat-core-modules}/..."]}
  - {name: extension-implementation, match: ["github.com/sneat-co/*/...", "github.com/sneat-games/*/..."]}
  - {name: bot-framework,            match: ["github.com/bots-go-framework/..."]}
  - {name: dalgo-adapter,            match: ["github.com/dal-go/dalgo{2,4}*/..."]}
  - {name: dalgo-core,               match: ["github.com/dal-go/..."]}
```

Rules are **allowlists with no `deny` list**. Anything absent is forbidden, and
an unmatched import becomes `unclassified` and fails closed. This is the
mechanism that makes an exception impossible to add quietly: there is no list to
widen, only a group to legitimise centrally.

```yaml
types:
  extension-implementation:
    detect: ["github.com/sneat-co/*/backend", "github.com/sneat-games/*/backend"]
    scopes:
      source: {allow: [own-repo, extension-contract, shared-kernel, dalgo-core, strongo, third-party]}
      tests:  {allow: [own-repo, extension-contract, shared-kernel, dalgo-core, dalgo-adapter, strongo, third-party]}
```

A repository declares only which policy applies to it, and optionally its type
when the module path is not enough to deduce it:

```yaml
# gametable/backend/.wb-deps-policy.yaml — the whole file
policy: sneat-co/cicd//policy/sneat-backend.yaml
type: extension-implementation
```

Two invariants keep central control absolute. **A repository file may tighten
but never loosen**: setting a rule mode, extending an `allow` list or declaring
a group is a configuration error, not a weaker rule; the single permitted
addition is `strict: true`, which promotes report-mode rules to errors locally.
And **the repository names the policy source but never its version** — a repo
frozen on an old policy release would be an exception with extra steps. The
version is resolved by the caller, defaulting to the fleet's current policy
release, and the safety net moves to `sneat-co/cicd`'s own CI, which runs a
candidate policy against every fleet repository before its tag is cut.

Package layering uses the same policy file, with its enforcement mode set
centrally so no repository can demote itself:

```yaml
layers:
  mode: report
  roles: {const: [const4*], dbo: [dbo4*], dal: [dal4*], facade: [facade4*], ...}
  order: [[cmd, ext], [api, botsvc, delayed], [facade], [dal], [dto, ports], [dbo, models], [const]]
  unknown-role: ignore
```

Report findings print, are counted in `--format json`, and never affect the exit
code — turning the 22 measured edges into a fleet burn-down that one commit to
the policy flips to `enforce` when it reaches zero.

The scan is lexical: `*.go` import blocks plus `go.mod` require/replace
directives. No type-checking, no module download, no `GOPRIVATE` credentials —
so the check still reports when the build itself cannot start, which is the same
reason `gametable` made its `backend-guard` a separate credential-free job.

## Alternatives Considered

**A standalone utility in `sneat-dev`.** The original framing, and the first
instinct. It loses because WB already owns the fleet dependency model that this
needs: `wb deps graph` classifies `sneat-co/ext-*` against `sneat-co/*/backend`
informally today. A separate binary would rebuild fleet selection, report
formats, CI commands and distribution, and add a fifth artifact to version and
pin into every consumer workflow.

**Generating `.golangci.yml` depguard rules from the policy.** Attractive
because `golangci-lint` already runs fleet-wide through `strongo/cicd`, and it
would give IDE diagnostics for free. It loses on expressiveness: depguard
matches literal path prefixes, so "any sibling implementation repository" would
have to be enumerated and regenerated whenever the org gains a repo, and
depguard has no concept of the *source* package's role, making intra-repo
layering inexpressible.

**A `go/analysis` vettool.** Composes with `go vet` and gives precise
positions, but it type-checks, so it needs the full private module graph
resolvable in CI — putting the guard downstream of the thing most likely to be
broken. Imports are lexical; type information buys nothing this rule needs.

## MVP Scope

One job: replace the four hand-rolled bash guards with `wb deps policy check`
reading a single central policy, and make `gametable`'s violation the only
failing repository in the fleet.

That means the Go scanner (imports plus `go.mod`), ordered group
classification, type detection from module path with optional override, the
`source`/`tests` scope split, allowlist-only rules, the tighten-never-loosen
repo-config validation, and `text`/`json`/`github` output with exit codes
`0`/`1`/`2`. Layer rules are implemented and the layer policy is authored, but
ship centrally set to `report`.

The subcommand surface divides four ways. Only the first group is MVP; the rest
are named here so the noun does not have to be re-litigated later.

| Verb | Job | Stage |
|---|---|---|
| `check` | Gate one repository. Exit `1` on violation. | MVP |
| `explain <path>` | Show the matched group, the matching pattern and its precedence, the detected type, the scope, and the deciding rule. | MVP |
| `show` | Print this repository's effective policy: source, resolved release, type, allow-list per scope, which rules enforce. | MVP |
| `validate <policy>` | Lint the policy file: group patterns shadowed by an earlier entry, unknown groups in an `allow`, types without `detect`, roles missing from the layer order. | MVP |
| `test <policy>` | Run the policy's own `expect:` assertions — import path to group, module path to type. | policy authoring |
| `impact <policy>` | Dry-run a candidate policy across the fleet and diff verdicts against the released one. Gates a policy change before its tag. | policy authoring |
| `report --fleet` | Burn-down of report-mode findings by repository, rule and mode. The signal to flip layering to `enforce`. | fleet |
| `drift --fleet` | Which repositories run which policy release, which declare a type detection disagrees with, which have no policy file. | fleet |
| `graph` | Render permitted type-to-group edges through `deps graph`'s SVG machinery — architecture documentation generated from the enforced rules. | later |
| `init` | Scaffold the per-repo file with the detected type, then run `check`. | later |

`explain` and `validate` are in the MVP for the same reason: a rule engine whose
verdicts cannot be interrogated gets routed around, and an ordering bug in a
first-match-wins group list is silent by construction.

## Not Doing (and Why)

- Exception, baseline or per-repo severity mechanisms — the failure being fixed
  is precisely that an exception became invisible and permanent; adding a
  governed channel for them reintroduces the loosening path.
- A TypeScript or Nx analyzer — Nx already enforces TS boundaries through
  eslint tags, and a second engine over the same code would create two
  disagreeing sources of truth.
- Type-checked analysis — the rules are lexical, and requiring a resolvable
  private module graph would make the guard fail exactly when it is most needed.
- Repository-level policy version pinning — a stale pin is an exception with
  extra steps; the reproducibility it buys belongs in the policy repo's own CI.
- Auto-fixing violations — rewriting an implementation import to a contract
  import usually requires publishing types into the contract library first, so
  there is no mechanical fix to apply.

## Key Assumptions to Validate

| Tier | Assumption | How to validate |
|------|------------|-----------------|
| Must-be-true | The fleet is clean enough that a no-exceptions rule is adoptable — i.e. `gametable` really is the only cross-repo violator | Re-run the measurement across every `sneat-co`, `sneat-games` and `dal-go` backend, not just the seven sampled |
| Must-be-true | Lexical scanning of import blocks and `go.mod` catches every real violation, with no need for type resolution | Parity suite: reproduce each of the four bash guards' verdicts on their own repositories before deleting them |
| Should-be-true | Repository *type* can be deduced from the module path often enough that the per-repo file is usually just a policy reference | Attempt detection-only classification across the fleet; count how many repos need an explicit `type:` |
| Should-be-true | `gametable`'s violation is fixable by publishing `HappeningBase`/`HappeningSlot` into `ext-calendarius/backend` | Inspect what `facade4gametable` and `ports4gametable` actually consume from `dbo4calendarius`/`dto4calendarius` |
| Might-be-true | The measured layer order is the intended one, not merely the emergent one | Founder review of the 22 divergent edges; several (`dal → dto`, `botsvc → dbo`) may be legitimate |
| Might-be-true | Running WB inside a consumer repository's CI is acceptable overhead versus a purpose-built binary | Measure cold-start install and scan time in one repo's workflow |

## SpecScore Integration

- **New Features this would create:** a `wb deps policy` Feature in this
  repository; a companion policy-artifact Feature in `sneat-co/cicd` covering
  `policy/sneat-backend.yaml` and the fleet-validation CI that gates its
  releases.
- **Existing Features affected:** `wb deps` gains a fourth noun alongside
  `set`, `bump`, `drift` and `graph`; `wb ci audit` remains separate, since it
  validates workflow configuration rather than source imports.
- **Dependencies:** `internal/deps` canonical model and report infrastructure.

## Open Questions

1. Does the fleet policy forbid all 22 measured layer edges, or only the
   inversions and facade-skipping (`dal → api`, `api → dal`, `api → dbo`)?
   Deferred until the burn-down is published, since layering ships in `report`
   mode either way.
2. Who owns `gametable`'s remediation — publishing the calendarius types into
   `ext-calendarius/backend` — and does it block that repository from gating on
   the cross-repo rules in the meantime?
3. Does the `report`/`drift` fleet split earn two verbs, or is `report` just
   `check --fleet` with report-mode findings included and the exit code
   suppressed?
4. `wb deps` currently means dependency *versions* (`set`, `bump`, `drift`,
   `graph`). Adding `policy` puts import boundaries under the same noun, and
   `deps policy drift` sits beside an unrelated `deps drift`. Accepted
   deliberately; revisit if the overload confuses users.
