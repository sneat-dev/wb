---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Canonical Claim Admission

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/canonical-claim-admission?op=request-change) |
**Status:** Approved
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

`wb worktree create --canonical <task> <owner/repository>` (with `--agent`,
`--agent-runtime`, `--model`, `--original-prompt-file` as ordinary create) MUST
create a canonical claim for exactly one repository, in this order: verify the
clone is on its base branch, clean (no staged, unstaged or untracked paths other
than WB-ignored state), not detached, not mid-operation, and holds no active
canonical claim; fetch and verify `origin/<base>` with the private-ref fetch
(`fetchOriginBranchToPrivateRef`) used by normal creation; record the claim
(with `base_sha`) while the clone is still on base; then check out the new
feature branch cut from that verified commit. Recording first means the
`post-checkout` hook already sees a live claim and raises no spurious warning.
If the checkout fails, the claim MUST be sealed as released by the same call and
the clone left on its base branch. Every refusal before the claim is recorded
MUST name the blocker (and `wb worktree rescue` for a dirty clone) and leave the
clone byte-for-byte unchanged. Without `--canonical` the command MUST behave
exactly as before.

#### REQ: branch-required

Admission MUST apply only while HEAD is a named branch equal to the claim's
`branch` and different from the claim's `base`. Detached HEAD MUST remain
refused. A commit or push on the base branch, or on any branch other than the
claim's, MUST be refused, so every canonical change reaches the base branch
through a pull request and its CI gate. Auto-tagging repositories therefore
never see a direct commit to main from this mode.

#### REQ: one-canonical-claim-per-repository

A canonical claim is **active** from the moment it is recorded until it is
sealed (landed or released); it is **live** while active and its lease has not
lapsed, and **lapsed** while active and past its lease. At most one active
canonical claim MAY exist per repository, because the clone has a single HEAD. A
second `wb worktree create --canonical` for the same repository while one is
active (live or lapsed) MUST refuse, name the holding task, and name
`wb worktree create <task> <owner/repository>` as the way to start the second
task in an isolated worktree. An active canonical claim MUST NOT block ordinary
worktree claims for the same repository, nor canonical claims for other
repositories.

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
same task while the claim is **live** MUST be idempotent: it MUST NOT create a
new branch or claim, MUST re-verify the clone is on the claim's branch, and MUST
extend `lease_expires_at` to now plus the lease, bounded by 8 hours from the
original creation. An extension MUST be appended as claim evidence, never by
mutating the immutable claim. Once the claim is lapsed, renewal MUST be refused:
the only paths are `wb worktree rescue`, `wb worktree end`, and landing.

#### REQ: lease-expiry-restores-strict-guard

Once `lease_expires_at` has passed, without any command being run: the agent
guard MUST refuse every canonical write and the marker MUST report
`writable: false`. `Guard` MUST stop admitting new work in the clone for any
branch, but MUST keep admitting a commit or push on the **claim's own feature
branch** while the claim is active (lapsed but unsealed), because `wb worktree
end`, landing and `wb pr create` commit and push through the managed hooks, which
defer to `Guard`, and `--no-verify` is forbidden; a lapsed claim must not make
exactly those recovery paths fail. Even lapsed, `Guard` MUST refuse the base
branch, any other branch, and detached HEAD. The claim MUST remain active until
landed, released, or recovered. A refusal MUST name `wb worktree rescue <path>`
for dirty content and `wb worktree end` or landing for the claim.

### Guard layers

#### REQ: guard-admits-live-canonical-claim

`worktrees.Guard` on a canonical clone (`gitDir == commonDir`) MUST, before the
`branch != base` and clean-tree refusals, look up the clone's active Work Log
claim with `activeWorkLogClaim` (corroborated against live Git, as the adopted
worktree path does) and admit the clone only when all hold: the claim has
`mode: canonical`, the claim's repository matches the clone, and HEAD is the
claim's feature branch. A live lease admits all guarded operations; a lapsed
lease admits only commit and push on that branch, as specified in
lease-expiry-restores-strict-guard. The result MUST report `Kind: "canonical"`
with the claim's task and lease. Without such a claim the existing refusals MUST
apply unchanged.

#### REQ: hooks-honour-mode

The managed `WorktreeGuard` hook script (`builtin:worktree-guard`, which defers to
`wb worktree guard`) MUST need no relaxed hook configuration:
`post-checkout`, `pre-commit` and `pre-push` MUST admit exactly what `Guard`
admits. Because admission is per clone, not per ref, `pre-push` MUST additionally
inspect the refs being pushed and refuse the base branch and any ref other than
the claim's feature branch (including `HEAD:main`-style refspecs and deletes), so
the base branch can only change through a pull request. No layer MUST accept
`--no-verify`, a `core.hooksPath` override, or a hook-disabling construct as a
way into canonical mode; the agent guard's hook-bypass refusal
(`managedGitLocation`) MUST keep applying to the canonical clone.

#### REQ: agentguard-honours-mode

`internal/agentguard` MUST allow a file-write tool call, a Bash file mutator,
and a Bash working directory in a canonical clone only when the injected
admission check reports a **live** (not lapsed) canonical claim for that clone
on its claim branch. While admitted, the agent guard MUST still refuse a
checkout or switch to any other branch, `git branch -D`/`-d` of the claim branch
or base, `git reset` or `git update-ref` that moves the base branch, and a push
whose refs include the base branch, because admission is per clone. The check
MUST be supplied by `cmd/wb/hooks_agent.go` through an `Options` hook so
`internal/agentguard` stays free of Work Log imports. It MUST be read-only
(no projection migration or other write) and cheap: one lookup of the clone's
projection and claim files per tool call, not cached across calls, so a lapsed
or sealed claim takes effect on the next call. The `Inspect` contract (never an
error, never a panic) MUST hold, and any failure to read, corroborate, or
time-check the claim MUST be treated as no admission.

#### REQ: marker-reflects-claim

While a live canonical claim admits the clone, `.worktree.md` MUST say
`kind: canonical`, `writable: true`, and record the claim's `branch`, `task`, and
`lease_expires_at`, with a body that tells the agent the clone is claimed, which
branch to stay on, and how to land. The marker MUST be rewritten on claim
creation, renewal, landing, release, and `wb sync`, and MUST return to
`writable: false` and its normal body when the claim is sealed or lapses. The
marker is advisory: a marker that outlives its lease MUST NOT be able to admit a
write, because `Guard` and `agentguard` read the claim, never the marker.

#### REQ: sealed-claim-does-not-block-the-next

A sealed canonical claim leaves a terminal projection at the canonical path. That
terminal projection MUST NOT block, be mistaken for, or admit writes for a later
canonical claim in the same clone: creating a new canonical claim after the first
was sealed MUST succeed and the new claim MUST be independent of the sealed one.
(`EnsureWorkLogClaim` today treats a non-active projection as an error other
than not-found, so the entry path must explicitly handle a terminal projection at
the canonical path.)

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
**Then** every layer admits the clone, HEAD is the claim's feature branch cut from the verified `origin/main` commit, `post-checkout` raised no warning during entry, `.worktree.md` says `kind: canonical` and `writable: true` with the branch, task, and expiry, and the base branch is unchanged; a failed checkout during entry leaves the clone on `main` with the claim sealed as released.

### AC: base-branch-and-detached-head-stay-refused

**Requirements:** canonical-claim-admission#req:branch-required, canonical-claim-admission#req:guard-admits-live-canonical-claim, canonical-claim-admission#req:hooks-honour-mode, canonical-claim-admission#req:agentguard-honours-mode

**Given** a live canonical claim
**When** the clone is checked out to `main`, a commit is attempted on `main`, the clone is put on a different feature branch, HEAD is detached, `git push origin HEAD:main` or `git push origin :<claim-branch>` is run, or the agent guard sees `git checkout main`, `git branch -D <claim-branch>`, or `git reset --hard origin/main`
**Then** `wb worktree guard`, `pre-commit`, `pre-push`, and the agent guard refuse each case naming the claim's branch, and no commit or ref update reaches the base branch through this mode.

### AC: second-task-is-pointed-at-worktree-create

**Requirements:** canonical-claim-admission#req:one-canonical-claim-per-repository, canonical-claim-admission#req:worktree-create-unaffected

**Given** an active canonical claim (live, and separately lapsed but unsealed) for a repository
**When** a second `wb worktree create --canonical` is run for another task on that repository, and then an ordinary `wb worktree create` is run for it
**Then** the first is refused naming the holding task and `wb worktree create <task> <owner/repository>`; the second succeeds, cuts its branch from the verified remote base rather than the claimed feature branch, and leaves the canonical clone's branch, index, and working tree unchanged.

### AC: entry-refuses-unsafe-clones-without-touching-them

**Requirements:** canonical-claim-admission#req:canonical-claim-entry

**Given** canonical clones that are dirty, detached, off base, or mid-rebase
**When** `wb worktree create --canonical` is run on each
**Then** each run refuses naming the blocker and, for dirty content, `wb worktree rescue`, no claim is recorded, and the clone is byte-for-byte unchanged.

### AC: lapsed-lease-resumes-strict-guard

**Requirements:** canonical-claim-admission#req:lease-bounds, canonical-claim-admission#req:lease-renewal, canonical-claim-admission#req:lease-expiry-restores-strict-guard, canonical-claim-admission#req:marker-reflects-claim

**Given** a canonical claim in repository A whose lease is moved past expiry by an injected clock, and a second canonical claim in a different repository B renewed before its expiry
**When** the guard layers and marker inspect each clone, a commit and a push on A's claim branch are attempted, a renewal of A is attempted, and `--lease 9h` is requested
**Then** A's agent guard refuses writes and its marker says `writable: false`; `Guard` refuses everything in A except a commit and push on its claim branch (so `wb worktree end` and landing still work); renewal of A is refused naming rescue, end, and landing; B stays admitted with an extended expiry that never exceeds 8 hours from creation; the 9-hour request is refused naming the maximum.

### AC: unreadable-claim-fails-closed

**Requirements:** canonical-claim-admission#req:agentguard-honours-mode, canonical-claim-admission#req:guard-admits-live-canonical-claim

**Given** a canonical clone on a feature branch whose claim is missing, malformed, sealed, for another repository, or unreadable
**When** `Guard` and the agent guard inspect it
**Then** each refuses exactly as today and the agent guard never panics or errors.

### AC: second-canonical-claim-after-sealing

**Requirements:** canonical-claim-admission#req:sealed-claim-does-not-block-the-next, canonical-claim-admission#req:one-canonical-claim-per-repository

**Given** a canonical clone whose first canonical claim was landed and sealed, leaving a terminal projection at the canonical path
**When** `wb worktree create --canonical` is run for a new task on the same repository
**Then** it succeeds with a new claim, branch, and lease independent of the sealed one, the guard layers admit the new claim only, and the sealed claim's terminal record is unchanged.

### AC: crash-leftover-is-recoverable

**Requirements:** canonical-claim-admission#req:abandoned-clone-recovered-by-rescue, canonical-claim-admission#req:lease-expiry-restores-strict-guard

**Given** a canonical clone left dirty on the claim's feature branch after the agent crashed and the lease lapsed
**When** `wb worktree rescue` is run on it
**Then** staged, unstaged, and untracked content is preserved on a rescue branch, the claim's commits are untouched, and nothing is discarded without the explicit discard flag.

## Open Questions

None at this time. Confirmed by the founder: the `wb worktree create --canonical` / `--lease` verb shape; lease 2h default, 8h cap, renewal by re-running create.

---
*This document follows the https://specscore.md/feature-specification*
