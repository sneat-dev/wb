---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Integration Branch Registry

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-registry?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-registry?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-registry?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/integration-branch-registry?op=request-change) |
**Status:** Draft
**Source Ideas:** integration-branch-batch-landing
**Depends On:** [Integration Branch Batches](../integration-branch-batches/README.md), [Worktree Lifecycle](../worktree-lifecycle/README.md)

## Summary

Register an integration branch whose gate GitHub enforces, let a worktree declare it as its landing target at creation, and promote the integration branch under its own gate.

## Problem

`wb worktree create --base public-demo` records only where a worktree
started. Nothing knows that `public-demo` is an integration branch, what its
gate is, where it promotes to, or which lanes are reviewed, batched or
promoted, so every agent re-derives it and `wb pr create` targets `main` by
default.

## Behavior

The gate is always server-enforced: WB records policy for routing and
reporting, and never acts as the only enforcer of a gate. The strict
up-to-date rule this requires means a lane that lands directly, outside a
batch, pays the fence refresh again; that is intended — batching is the
cheap path.

### REQ: integration-create-enforces-gate

`wb integration create <owner/repo> <branch> --gate <check>[,...]
--promote-to <branch> --promote-gate <check>[,...]|all` MUST create the
branch from `--promote-to` when absent, and then require that a GitHub
ruleset or branch protection on `<branch>` requires every gate check with a
strict up-to-date policy. When none does, it refuses
`integration-gate-not-enforced` and prints the ruleset JSON to apply; with
`--apply-ruleset` it creates that ruleset itself (admin permission needed).

### REQ: integration-policy-ref

The policy (gate, promote-to, promote-gate, creator, time) MUST be stored as
a commit whose tree holds one `policy.yaml`, at
`refs/wb/integrations/<branch>` on origin, so no branch's tree contains it.
`wb integration show|list` read it with one fetch. An identical rerun is a
no-op; a change needs `--update` and parents the new policy commit on the
old. Every batch, landing and promotion receipt records the policy commit
SHA. Because the ref is not ruleset-protected, safety never depends on it:
REQ integration-create-enforces-gate is re-verified on every promotion.

### REQ: integration-worktree-target

`wb create <task> <repo> --integration <branch>` (and `wb worktree create`)
MUST refuse an unregistered branch before creating anything, cut the
worktree from `origin/<branch>`, and record `landing_target: <branch>` in the
Work Log claim. For that worktree `wb pr create` targets the landing target
by default; `wb pr land --batch` (explicit, never implicit) enrolls the PR
into the open batch for that target via `wb pr batch add`, or creates one;
cleanup proves absorption against `origin/<landing-target>`; and `wb worktree
summary` shows one of `unreviewed`, `reviewed`, `in batch <n>`, `landed in
<target>`, `promoted to <promote-to>`.

### REQ: integration-promote

`wb integration promote <owner/repo> <branch>` MUST re-verify REQ
integration-create-enforces-gate, then open or update the one PR from the
integration branch to promote-to with merge method `merge`, and land it via
`wb pr land`. With promote gate `all`, every observed check on the PR head
must pass, auto-merge is not armed, and any `--waive-*` flag is a usage error
(exit `2`). With a list, every listed check must be server-required on
promote-to, else it refuses `promote-gate-not-enforced` printing the
ruleset JSON (or creates it with `--apply-ruleset`); each listed check must
pass and other checks obey [PR Land Waivers](../pr-land-waivers/README.md). Promotion appends the
promoted SHA to the policy ref history.

## Acceptance Criteria

### AC: unenforced-gate-refused

**Requirements:** integration-branch-registry#req:integration-create-enforces-gate

**Given** a branch with no ruleset requiring `build`
**When** `wb integration create o/r public-demo --gate build --promote-to
main --promote-gate all` runs
**Then** it refuses `integration-gate-not-enforced` and prints the ruleset
JSON; with `--apply-ruleset` it creates the ruleset and registers.

### AC: create-makes-the-branch

**Requirements:** integration-branch-registry#req:integration-create-enforces-gate

**Given** no `public-demo` branch on origin
**When** `wb integration create ... --apply-ruleset` runs
**Then** `origin/public-demo` equals `origin/main` and its ruleset requires
the gate with strict up-to-date.

### AC: policy-shared-through-ref

**Requirements:** integration-branch-registry#req:integration-policy-ref

**Given** a registered `public-demo`
**When** a second clone runs `wb integration show o/r public-demo`
**Then** it prints the policy, no branch tree contains `policy.yaml`, an
identical rerun makes no commit, a changed rerun without `--update`
refuses, and with `--update` the new policy commit's parent is the old one.

### AC: integration-lane-targets-and-enrolls-on-request

**Requirements:** integration-branch-registry#req:integration-worktree-target

**Given** a registered `public-demo`
**When** `wb create t o/r --integration public-demo` runs, `wb pr create`
opens its PR, it is reviewed, and `wb pr land --batch` runs
**Then** the worktree started at `origin/public-demo`, the PR's base is
`public-demo`, the PR is in a batch rather than merged, and `wb worktree
summary` shows `in batch 1`; plain `wb pr land` still lands it directly;
`--integration` naming an unregistered branch creates nothing.

### AC: promote-list-gate-must-be-enforced

**Requirements:** integration-branch-registry#req:integration-promote

**Given** promote gate `e2e` that `main` does not require
**When** `wb integration promote` runs
**Then** it refuses `promote-gate-not-enforced` before any PR is armed.

### AC: promote-all-gate

**Requirements:** integration-branch-registry#req:integration-promote

**Given** `public-demo` with promote gate `all`
**When** `wb integration promote o/r public-demo --waive-check e2e` runs
**Then** it exits `2`; without the waiver it opens one PR to `main`, lands it
only after every check passed, and the summary shows promoted lanes.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
