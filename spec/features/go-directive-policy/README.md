---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Go directive fleet policy

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/go-directive-policy?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/go-directive-policy?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/go-directive-policy?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/go-directive-policy?op=request-change) |
**Status:** Approved
**Source Ideas:** —

## Summary

Assess and, per repository, land the go 1.26.x directive / toolchain go1.27.0 pairing across the fleet, refusing where a dependency's own go directive forces a higher floor.

## Problem

Only a module's own `go` directive participates in Go's minimal version
selection (MVS), and MVS takes the maximum across the whole build list. A
widely-consumed module that declares `go 1.27.0` drags every consumer to Go
1.27 whether it wanted that or not — `bots-go-framework/bots-fw-store-dalgo
v0.4.0` was unusable for exactly this reason and had to be re-released as
v0.4.1. The `toolchain` directive is not imposed on consumers, so `go 1.26.x`
paired with `toolchain go1.27.0` lets first-party modules build with 1.27
without forcing 1.27 on anyone downstream.

The fix is not always available: a module's own `go` directive must be at
least the maximum `go` directive declared by every dependency in its resolved
build list. Grepping `go.mod` files cannot see that ceiling — only resolving
the real module graph with `go` tooling can — and a naive rewrite that ignores
it produces a repository that `go mod tidy` silently reverts, which is worse
than not touching it (it looks compliant and is not). The fleet has no command
that determines, per repository, whether the policy is achievable before
touching anything, and that names the exact forcing dependency where it is
not.

## Behavior

### Achievability

#### REQ: resolve-the-real-module-graph

Achievability MUST be determined by resolving each module's actual build list
with `go` tooling (`go list -m -json all` against an isolated copy of its
`go.mod`/`go.sum`), never by lexically scanning `go.mod` files. The resolution
MUST run in a private temporary copy so a read-only assessment never writes to
the repository being assessed.

#### REQ: ceiling-blocks-compliance

A module's own `go` directive MUST be at least the maximum `go` directive
declared by every other module in its resolved build list (the "ceiling").
When the ceiling's language version exceeds the policy's target language
version, the policy is NOT achievable for that module.

#### REQ: below-floor-is-a-separate-category

A module whose current `go` directive language version is already below the
policy's target MUST be reported as its own category and left unchanged.
Raising a floor is a separate policy decision, and this command MUST NOT make
it silently.

### Categorization and reporting

#### REQ: five-category-verdict

Every discovered Go module MUST be assigned exactly one verdict: `compliant`
(already `go 1.26.x` + the target `toolchain`), `would-change` (achievable but
not yet declared that way), `cannot-comply` (blocked by a forcing dependency),
`below-floor` (current directive already below 1.26, left alone), or `error`
(the module graph could not be safely resolved, e.g. an unpublishable local
`replace` directive). A repository with no `go.mod` MUST be reported as having
no Go module rather than silently skipped.

#### REQ: cannot-comply-names-the-forcing-dependency

A `cannot-comply` verdict MUST name the specific dependency (module path and
version) whose own `go` directive sets the ceiling, so the report is a
worklist for which upstream module needs fixing first.

#### REQ: multi-module-repositories-are-walked

A repository with more than one `go.mod` (for example a root module plus
`backend/go.mod`) MUST report one verdict per module, not one verdict per
repository.

### Applying

#### REQ: dry-run-by-default

A bare invocation MUST plan and change nothing. Only `--apply` writes the `go`
and `toolchain` directives, and only for a module whose verdict is
`would-change`.

#### REQ: apply-verifies-with-tidy

After writing the directives, `--apply` MUST run `go mod tidy` and re-resolve
the module's own directive. If tidy reverted the edit (a forcing dependency
was missed), the command MUST fail loudly rather than leave a go.mod that
looks compliant and is not.

#### REQ: fleet-report-is-read-only

The fleet-wide report MUST never write to any repository — it has no `--apply`
flag at all. Applying the policy to any given repository is a separate,
explicit, single-repository operation.

## Acceptance Criteria

### AC: forcing-dependency-blocks-and-is-named

**Requirements:** go-directive-policy#req:resolve-the-real-module-graph, go-directive-policy#req:ceiling-blocks-compliance, go-directive-policy#req:cannot-comply-names-the-forcing-dependency

**Given** a module whose resolved build list contains a dependency declaring
`go 1.27.0`
**When** the go-directive command assesses that module
**Then** the verdict is `cannot-comply`, no file is changed, and the report
names that dependency's module path and version as the forcing dependency.

### AC: achievable-module-changes-and-survives-tidy

**Requirements:** go-directive-policy#req:ceiling-blocks-compliance, go-directive-policy#req:dry-run-by-default, go-directive-policy#req:apply-verifies-with-tidy

**Given** a module whose resolved build list has no dependency above `go
1.26`, currently declaring `go 1.27.0`
**When** the command runs with no flags, and then again with `--apply`
**Then** the dry run reports `would-change` from `go 1.27.0` to `go 1.26.x`
with `toolchain go1.27.0` and writes nothing, and `--apply` writes both
directives and confirms with `go mod tidy` that they were not reverted.

### AC: compliant-and-below-floor-are-left-alone

**Requirements:** go-directive-policy#req:five-category-verdict, go-directive-policy#req:below-floor-is-a-separate-category

**Given** one module already declaring `go 1.26.x` with `toolchain
go1.27.0`, and another declaring `go 1.20`
**When** the command assesses both
**Then** the first reports `compliant`, the second reports `below-floor` and
is never rewritten upward, and `--apply` changes neither.

### AC: multi-module-and-no-module-repositories-are-handled

**Requirements:** go-directive-policy#req:multi-module-repositories-are-walked, go-directive-policy#req:five-category-verdict

**Given** a repository with a root `go.mod` and a `backend/go.mod`, and a
second repository with no `go.mod` at all
**When** the fleet report walks both
**Then** the first repository contributes one verdict row per module and the
second is reported as having no Go module, neither aborting the walk.

### AC: fleet-report-changes-nothing

**Requirements:** go-directive-policy#req:fleet-report-is-read-only, go-directive-policy#req:dry-run-by-default

**Given** the fleet report run across every discovered repository
**When** it completes
**Then** no repository's working tree changed, the report has no `--apply`
flag to request otherwise, and every cannot-comply row is visible together as
the upstream worklist.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
