---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: Canonical Claim Admission

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

A Work Log claim may carry `mode: canonical`. While a live canonical claim
exists for a repository, that repository's canonical clone is admitted for
writes on the claim's feature branch, so a quick or isolated task needs no
`wb worktree create` / cleanup round trip. The mode is a claim-corroborated
admission, the same shape as the adopted-worktree admission
(`locateGuardedAdoptedWorktree` in `internal/worktrees/worktrees.go`), applied
consistently by every layer that today refuses canonical writes:

- `worktrees.Guard` and therefore the `WorktreeGuard` git hooks
  (`post-checkout`, `pre-commit`, `pre-push`);
- `internal/agentguard`, the agent tool-call guard;
- the generated `.worktree.md` marker (`internal/checkoutmarker`).

The claim carries a lease. When it lapses, the strict guard resumes by itself.
Landing and cleanup behavior is specified in
[canonical-claim-landing](../canonical-claim-landing/README.md).

This feature does not allow commits to the base branch, more than one canonical
claim per repository, a new inter-agent lock, or any hook bypass.

## Problem

Today a canonical clone is refused for every write (`Guard` requires
`branch == base` and a clean tree; `agentguard.inspectFileTool` refuses any
`KindCanonical` path; the marker says `writable: false`). That protects the
clone every worktree is cut from, but for a quick or isolated change the
worktree create/verify/cleanup ceremony costs more tokens and wall-clock time
than the change itself. Agents then either burn that budget or work around the
guard. A sanctioned, audited, time-boxed canonical mode removes the
incentive without weakening the default.

## Behavior

### Claim mode and entry

#### REQ: claim-mode-field

A Work Log claim MUST support a `mode` field with the values `worktree` (the
default) and `canonical`. A claim with `mode: canonical` MUST also record
`lease_expires_at`, and its `worktree` field MUST be the canonical clone path.
The claim identity derivation (`expectedWorkLogClaimID` in
`internal/worktrees/worklog.go`) MUST cover `mode` so a canonical claim cannot
be rewritten as a worktree claim.

#### REQ: canonical-claim-entry

`wb worktree create --canonical <task> <owner/repository>` MUST create a
canonical claim for exactly one repository. Before any change it MUST verify the
canonical clone is on its base branch, clean (no staged, unstaged or untracked
paths other than WB-ignored state), not detached, and not mid-operation; it MUST
fetch and verify `origin/<base>` using the same private-ref fetch
(`fetchOriginBranchToPrivateRef`) as normal creation, then check out a new
feature branch cut from that verified commit and record the claim with
`base_sha`. It MUST refuse naming the blocker (and `wb worktree rescue` for a
dirty clone) and MUST leave the clone byte-for-byte unchanged on every refusal.
Without `--canonical` the command MUST behave exactly as before.

#### REQ: branch-required

Admission MUST apply only while HEAD is a named branch equal to the claim's
`branch` and different from the claim's `base`. Detached HEAD MUST remain
refused. A commit or push on the base branch, or on any branch other than the
claim's, MUST be refused, so every canonical change reaches the base branch
through a pull request and its CI gate. Auto-tagging repositories therefore
never see a direct commit to main from this mode.

#### REQ: one-canonical-claim-per-repository

At most one claim with `mode: canonical` MAY be active for a repository,
because the clone has a single HEAD. A second `wb worktree create --canonical`
for the same repository, while that claim is active (live or lapsed but not yet
sealed), MUST refuse, name the holding task, and name
`wb worktree create <task> <owner/repository>` as the way to start the second
task in an isolated worktree. A canonical claim MUST NOT block ordinary
worktree claims for the same repository.

#### REQ: worktree-create-unaffected

`wb worktree create` without `--canonical` MUST keep fetching and pinning the
remote base without checking out, resetting, or otherwise touching the canonical
clone, including while a live canonical claim holds that clone on a feature
branch or dirty. A new worktree MUST be cut from the verified remote base, never
from the canonical clone's current HEAD.

### Lease and expiry

#### REQ: lease-bounds

A canonical claim MUST be created with a lease: default 2 hours, settable with
`--lease <duration>`, with a hard maximum of 8 hours. A longer request MUST be
refused naming the maximum. `lease_expires_at` MUST be computed once from the
creation instant and written with the claim.

#### REQ: lease-renewal

Re-running `wb worktree create --canonical <task> <owner/repository>` for the
same task while the claim is active MUST be idempotent: it MUST NOT create a new
branch or claim, MUST re-verify the clone is on the claim's branch, and MUST
extend `lease_expires_at` to now plus the lease, bounded by 8 hours from the
original creation. An extension MUST be appended as claim evidence, never by
mutating the immutable claim.

#### REQ: lease-expiry-restores-strict-guard

Once `lease_expires_at` has passed, every layer MUST treat the claim as not
admitting writes without any command being run: `Guard` MUST apply the original
`branch == base` and clean-tree refusals, `agentguard` MUST refuse canonical
writes, and the marker MUST report `writable: false`. The claim MUST remain
active (it still owns the clone's state for the one-claim rule) until landed,
released, or recovered. The refusal MUST name `wb worktree rescue <path>` for
dirty content and `wb worktree end` or `wb worktree land` for the claim.

### Guard layers

#### REQ: guard-admits-live-canonical-claim

`worktrees.Guard` on a canonical clone (`gitDir == commonDir`) MUST, before the
`branch != base` and clean-tree refusals, look up the clone's active Work Log
claim with `activeWorkLogClaim` (corroborated against live Git, as the adopted
worktree path does) and admit the clone only when all hold: the claim has
`mode: canonical`, the lease has not lapsed, the claim's repository matches the
clone, and HEAD is the claim's feature branch. The result MUST report
`Kind: "canonical"` with the claim's task and lease so callers can tell an
admitted clone from a clean base. Without such a claim the existing refusals
MUST apply unchanged.

#### REQ: hooks-honour-mode

The managed `WorktreeGuard` hook script (`builtin:worktree-guard`) MUST need no
relaxed hook configuration: it already defers to `wb worktree guard`, so
`post-checkout`, `pre-commit` and `pre-push` MUST admit exactly what `Guard`
admits. A `pre-commit` or `pre-push` that would write or publish the base
branch from a canonical clone MUST remain refused while a claim is live. No
layer MUST accept `--no-verify`, a `core.hooksPath` override, or a
hook-disabling construct as a way into canonical mode; the agent guard's
hook-bypass refusal (`managedGitLocation`) MUST keep applying to the canonical
clone.

#### REQ: agentguard-honours-mode

`internal/agentguard` MUST allow a file-write tool call, a Bash file mutator,
and a Bash working directory in a canonical clone only when the injected
admission check reports a live canonical claim for that clone on its claim
branch. The check MUST be supplied by `cmd/wb/hooks_agent.go` through an
`Options` hook so `internal/agentguard` stays free of Work Log imports, and the
`Inspect` contract (never an error, never a panic) MUST hold. Any failure to
read, corroborate, or time-check the claim MUST be treated as no admission:
the guard fails closed to today's refusal.

#### REQ: marker-reflects-claim

While a live canonical claim admits the clone, `.worktree.md` MUST say
`kind: canonical`, `writable: true`, and record the claim's `branch`, `task`, and
`lease_expires_at`, with a body that tells the agent the clone is claimed, which
branch to stay on, and how to land. The marker MUST be rewritten on claim
creation, renewal, landing, release, and `wb sync`, and MUST return to
`writable: false` and its normal body when the claim is sealed or lapses. The
marker is advisory: a marker that outlives its lease MUST NOT be able to admit a
write, because `Guard` and `agentguard` read the claim, never the marker.

### Multi-agent and recovery

#### REQ: subagents-share-the-claim

Subagents dispatched by the claim holder MUST share the orchestrator's single
canonical claim in the same way they share a worktree claim (one claim per
`WBSessionID`/`ClaimID`, see the claim fields in `internal/worktrees/worklog.go`).
Canonical mode MUST add no new lock, queue, or concurrency mechanism: parallel
agents MUST commit with explicit paths by convention, and the marker body MUST
say so.

#### REQ: abandoned-clone-recovered-by-rescue

A canonical clone left dirty or on a feature branch by a crashed or lapsed
canonical claim MUST be recoverable with the existing `wb worktree rescue`
without discarding content: the rescue captures staged, unstaged, and untracked
work onto a branch (`internal/canonicalrescue`) and MUST accept a clone that is
on the claim's feature branch rather than assuming it is on base. It MUST NOT
alter the claim's branch commits. Restoring the clone to its base branch after
rescue is the claim-release path specified in
[canonical-claim-landing](../canonical-claim-landing/README.md).

## Interaction with Other Features

[Worktree Lifecycle](../worktree-lifecycle/README.md) owns `Guard`, the hooks,
rescue, and marker behavior this feature extends. [Work Log](../work-log/README.md)
owns the claim record. [Remote Claims](../remote-claims/README.md) is unchanged:
a canonical claim is still announced fleet-wide by the existing best-effort
auto-claim, keyed by task.

## Acceptance Criteria

### AC: live-claim-admits-feature-branch-writes

**Requirements:** canonical-claim-admission#req:claim-mode-field, canonical-claim-admission#req:canonical-claim-entry, canonical-claim-admission#req:guard-admits-live-canonical-claim, canonical-claim-admission#req:hooks-honour-mode, canonical-claim-admission#req:agentguard-honours-mode, canonical-claim-admission#req:marker-reflects-claim

**Given** a real bare remote, a clean canonical clone on `main`, and a recorded canonical claim created by `wb worktree create --canonical`
**When** an agent edits a file in the canonical clone, commits with explicit paths, and pushes the feature branch, and `wb worktree guard`, the three managed hooks, and the agent guard each inspect the clone
**Then** every layer admits the clone, HEAD is the claim's feature branch cut from the verified `origin/main` commit, `.worktree.md` says `kind: canonical` and `writable: true` with the branch, task, and expiry, and the base branch is unchanged.

### AC: base-branch-and-detached-head-stay-refused

**Requirements:** canonical-claim-admission#req:branch-required, canonical-claim-admission#req:guard-admits-live-canonical-claim, canonical-claim-admission#req:hooks-honour-mode

**Given** a live canonical claim
**When** the clone is checked out to `main`, a commit is attempted on `main`, the clone is put on a different feature branch, or HEAD is detached
**Then** `wb worktree guard`, `pre-commit`, and `pre-push` refuse each case naming the claim's branch, and no commit reaches the base branch through this mode.

### AC: second-task-is-pointed-at-worktree-create

**Requirements:** canonical-claim-admission#req:one-canonical-claim-per-repository, canonical-claim-admission#req:worktree-create-unaffected

**Given** an active canonical claim for a repository
**When** a second `wb worktree create --canonical` is run for another task on that repository, and then an ordinary `wb worktree create` is run for it
**Then** the first is refused naming the holding task and `wb worktree create <task> <owner/repository>`; the second succeeds, cuts its branch from the verified remote base rather than the claimed feature branch, and leaves the canonical clone's branch, index, and working tree unchanged.

### AC: entry-refuses-unsafe-clones-without-touching-them

**Requirements:** canonical-claim-admission#req:canonical-claim-entry

**Given** canonical clones that are dirty, detached, off base, or mid-rebase
**When** `wb worktree create --canonical` is run on each
**Then** each run refuses naming the blocker and, for dirty content, `wb worktree rescue`, no claim is recorded, and the clone is byte-for-byte unchanged.

### AC: lapsed-lease-resumes-strict-guard

**Requirements:** canonical-claim-admission#req:lease-bounds, canonical-claim-admission#req:lease-renewal, canonical-claim-admission#req:lease-expiry-restores-strict-guard, canonical-claim-admission#req:marker-reflects-claim

**Given** a canonical claim with a lease that is then moved past expiry by an injected clock, and a second claim renewed before expiry
**When** the guard layers and marker inspect each clone and `--lease 9h` is requested
**Then** the lapsed clone is refused by `Guard` and `agentguard` with the rescue and end/land remedies and its marker says `writable: false`, while the renewed clone stays admitted with an extended expiry that never exceeds 8 hours from creation; the 9-hour request is refused naming the maximum.

### AC: unreadable-claim-fails-closed

**Requirements:** canonical-claim-admission#req:agentguard-honours-mode, canonical-claim-admission#req:guard-admits-live-canonical-claim

**Given** a canonical clone on a feature branch whose claim is missing, malformed, sealed, for another repository, or unreadable
**When** `Guard` and the agent guard inspect it
**Then** each refuses exactly as today and the agent guard never panics or errors.

### AC: crash-leftover-is-recoverable

**Requirements:** canonical-claim-admission#req:abandoned-clone-recovered-by-rescue, canonical-claim-admission#req:lease-expiry-restores-strict-guard, canonical-claim-admission#req:subagents-share-the-claim

**Given** a canonical clone left dirty on the claim's feature branch after the agent crashed and the lease lapsed
**When** `wb worktree rescue` is run on it
**Then** staged, unstaged, and untracked content is preserved on a rescue branch, the claim's commits are untouched, and nothing is discarded without the explicit discard flag; two subagents of the one orchestrator claim could both have written there without any new lock.

## Open Questions

None at this time. Confirmed by the founder: the `wb worktree create --canonical` / `--lease` verb shape; lease 2h default, 8h cap, renewal by re-running create.

---
*This document follows the https://specscore.md/feature-specification*
