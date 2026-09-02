---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: NPM Peer Compatibility

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-peer-compatibility?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-peer-compatibility?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-peer-compatibility?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/npm-peer-compatibility?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

`wb deps peers <package> --against <checkout>` answers whether a published npm package can be reused in one checkout by judging its declared peerDependencies against the versions that checkout actually resolves, with a per-peer verdict and its evidence, without installing anything.

## Synopsis

```
wb deps peers @sneat/core --against ../renewon              # verdict table
wb deps peers @sneat/core@0.31.0 --against ../renewon       # judge an exact release
wb deps peers @sneat/core --against ../renewon --format json
```

## Problem

"Can I reuse this package here?" is asked constantly across a fleet of related
products, and it is answered badly: run the install, read whatever the package
manager prints about peer conflicts, and hope the warning names the real
culprit.

That is wrong in three ways. It mutates the checkout to find out. Its output
does not distinguish "the target is two majors behind this peer" from "the
publisher marked this peer optional and the target simply does not use it" —
the second is not a problem at all. And it compares nothing to nothing: a
manifest's `^18.0.0` is a range, and a range cannot be judged against another
range. Only the version the lockfile actually installs answers the question.

The result is that reuse decisions are made on impressions. A package that
would have worked is passed over; one that would not is adopted and discovered
at build time.

## Behavior

### REQ: published-peer-requirements

WB MUST read the peer requirements from the published package itself — its
`peerDependencies`, together with `peerDependenciesMeta` for the optional
marking — not from any local copy. An exact published version MAY be named; the
registry's current release MUST be used otherwise, and the resolved version
MUST appear in the report.

A package that declares no peer dependencies MUST be reported as requiring
nothing of its host. That is a legitimate and maximally reusable answer, not an
error and not an empty screen.

### REQ: installed-version-evidence

For each peer, WB MUST report what the target checkout actually resolves, and
where that came from. The version a governing `pnpm-lock.yaml` or
`package-lock.json` installs MUST be preferred over the specifier a manifest
declares. When no lockfile governs the manifest, WB MUST say so in the row's
source rather than presenting a declared range as an installed version.

A package the target only resolves transitively MUST still be reported, and
MUST be distinguishable from one the target declares.

### REQ: five-verdicts

Every peer requirement MUST receive exactly one verdict:

- **satisfied** — the target's resolved version is admitted by the peer range.
- **unsatisfied** — the target has the package, at a version the range rejects.
- **missing** — the target does not have the package at all.
- **optional_missing** — the publisher marked the peer optional and the target
  does not provide it.
- **unevaluated** — the peer range or the target's version is a specifier shape
  outside WB's evaluated subset.

`unevaluated` MUST carry its reason and MUST NEVER be presented as a pass. WB
MUST NOT guess a verdict for a shape it does not evaluate: a report that says
"this was not evaluated" is useful, and one that quietly guesses is not.

### REQ: read-only-and-gated

The command MUST NOT install, write, or otherwise modify either the published
package or the target checkout. It MUST exit non-zero when any required peer is
`unsatisfied` or `missing`, and zero otherwise; `optional_missing` and
`unevaluated` MUST NOT by themselves make the run fail.

## Acceptance Criteria

### AC: every-peer-gets-a-verdict-and-its-evidence

**Requirements:** npm-peer-compatibility#req:published-peer-requirements, npm-peer-compatibility#req:installed-version-evidence, npm-peer-compatibility#req:five-verdicts, npm-peer-compatibility#req:read-only-and-gated

**Given** a fictional published package declaring five peers — one the target
resolves compatibly, one the target resolves at a rejected version, one the
target lacks, one optional peer the target lacks, and one whose range is
outside the evaluated subset — and a target checkout with a lockfile
**When** the peers are inspected against that checkout
**Then** each peer receives its own verdict with the resolved version and the
lockfile or manifest field that produced it, the compatible row is judged
against the locked version rather than the declared range, the optional
absence is not a finding, the unevaluated row carries its reason and is not
reported as a pass, the run exits non-zero for the rejected and absent peers,
and neither the package nor the checkout is modified.

### AC: a-package-that-requires-nothing-says-so

**Requirements:** npm-peer-compatibility#req:published-peer-requirements, npm-peer-compatibility#req:read-only-and-gated

**Given** a published package that declares no peer dependencies
**When** it is inspected against any checkout
**Then** the report states that the package requires nothing of its host and
the run succeeds.

## Open Questions

- Should a future `--fleet` mode answer the same question across every local
  checkout at once, ranking candidates by how few peers block them?

---
*This document follows the https://specscore.md/feature-specification*
