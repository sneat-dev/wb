---
format: https://specscore.md/feature-specification
status: Approved
---

# Feature: Worktree Checkpoint

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-checkpoint?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-checkpoint?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-checkpoint?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/worktree-checkpoint?op=request-change) |
**Status:** Approved
**Source Ideas:** —

## Summary

A cheap, repeatable, hook-proof snapshot that preserves in-flight worktree state against crashes, independent of whether the work compiles or is ready to commit.

## Problem

Three agents lost or nearly lost work in one session, all the same way: holding
every change in the working tree until the end. A machine sleep, a dropped
connection, and a dead session each left a worktree with real, uncommitted
work and no durable copy — in one case the remote branch had already been
deleted, so the crashed machine held the only copy of 144 lines of code in
existence. Telling agents to "commit early and often" did not fix this,
because a real commit is blocked exactly when it matters most: half-written
code that does not build cannot pass `wb`'s own `go/pre-push` verification
hook, so the safest moment to preserve it is also the moment a landable
commit refuses to happen.

`wb worktree abort` already exists to seal an *interrupted task* so it can be
resumed or explicitly discarded, but it is a deliberate, occasional act — it
requires a disposition, a successor, and (for handoff/not_landed) an
explicit claim transfer. It answers "what happens to this task." It does not
answer "how do I stop losing the last five minutes of edits," which needs to
be so cheap and automatic that neither a human nor an agent has to remember
to invoke it.

## Behavior

### A commit that can never be a landable commit

#### REQ: broken-work-always-succeeds

`wb worktree checkpoint` MUST succeed on a worktree whose tracked or
untracked content does not compile, does not pass lint, or would otherwise be
rejected by any configured `pre-commit` or `pre-push` verification hook. It
MUST NOT run build, test, lint, or any other content-verification step
itself, and MUST NOT invoke the real `git commit` porcelain (which would run
`pre-commit`/`commit-msg` hooks). It captures the working tree exactly as
found — tracked modifications, deletions, and untracked files not excluded by
`.gitignore` — via plumbing (`git write-tree`/`git commit-tree`) against a
scratch index, never the caller's real index, so a checkpoint has zero effect
on the working tree, the real index, or any staged-vs-unstaged distinction
the caller was relying on.

#### REQ: dedicated-non-branch-namespace

Every checkpoint commit MUST be written under `refs/wb/checkpoints/<scope>/<timestamp>`
(`<scope>` derived from the current branch, or `detached-<short-sha>` when
HEAD is detached) and MUST NOT move, create, or amend any `refs/heads/*`
branch. This namespace MUST be unmistakably distinguishable from a landable
branch: nothing under `refs/wb/checkpoints/*` may appear as a Git branch,
carry an open pull request, or be reachable from any `refs/heads/*` ref
except as an ancestor already-merged commit would be. A checkpoint commit's
subject line MUST begin with a fixed, greppable marker
(`wip(checkpoint): …`) and its trailers MUST record the branch or scope
checkpointed, the exact HEAD it was taken against, and whether the worktree
was dirty, so a checkpoint accidentally reachable from history is
unmistakable on sight. Checkpoint commits MUST use a fixed non-personal
Git identity, never the caller's configured `user.name`/`user.email`, both
because a checkpoint is not an authored contribution and because it must not
depend on Git identity being configured at all.

#### REQ: idempotent-no-op

`wb worktree checkpoint` MUST compare the snapshot tree and current HEAD
against the most recent existing checkpoint ref for the same scope before
writing anything. When both are unchanged since that checkpoint, it MUST
skip creating a new commit or ref and report the existing checkpoint as
current rather than growing the ref namespace on every call. This MUST hold
even when the worktree has never been dirty, so the command is cheap enough
to call between every step without deliberation.

### Getting it off the machine

#### REQ: push-default-on-best-effort

Unless `--no-push` is given, `wb worktree checkpoint` MUST attempt to push
the active checkpoint ref (freshly written or reused) to `origin` — by
default the same short name Git would resolve for the branch — using
`--no-verify` and a plumbing push of the exact object, never a `refs/heads/*`
update. A push failure (offline, no configured remote, auth failure) MUST
NOT fail the command: local preservation MUST already have succeeded before
the push is attempted, and the result MUST report the push outcome
separately so a caller can tell "preserved locally only" from "preserved and
off this machine." Because the push targets the same ref/commit already
computed for the no-op case, a retried checkpoint after a transient push
failure MUST re-attempt that same push rather than skip it.

#### REQ: checkpoint-push-bypasses-verification-hooks

A `pre-push` hook that runs build/test/lint (for example WB's own
`builtin:go-pre-push`) MUST NOT block a push whose only updated remote refs
are under `refs/wb/checkpoints/*`, mirroring the existing exemption for a
deletion-only push. This MUST be narrowly scoped to that exact ref prefix so
it cannot be used to bypass verification on any `refs/heads/*` push.

### Finding a checkpoint again

#### REQ: discoverable-checkpoints

`wb worktree checkpoint list [path]` MUST enumerate checkpoint refs for the
worktree's current scope (or `--all` for every scope in the repository),
newest first, each with its timestamp, message, recorded HEAD, and whether
the worktree was dirty when taken. `--remote` MUST additionally query
`origin` (`git ls-remote`) so a checkpoint can be found from a fresh clone
after the original worktree is gone, matching the exact failure this feature
exists to prevent. A checkpoint namespace with nothing in it MUST be
reported plainly, never as an error.

#### REQ: explicit-non-destructive-recovery

`wb worktree checkpoint restore <ref> [path]` MUST default to a dry run that
reports what the checkpoint contains (its diffstat against current HEAD)
without changing any Git state. `--apply --branch <name>` MUST create exactly
one new local branch at the checkpoint commit and MUST NOT touch the current
branch, working tree, or index — recovery is always an explicit, separate,
inspectable branch, never an implicit merge or checkout into the caller's
live state. It MUST refuse to overwrite an existing branch name unless
`--force` is given.

### Composing with the rest of WB

#### REQ: auto-checkpoint-before-destructive-worktree-operations

`wb worktree abort` (every disposition) and `wb worktree cleanup --apply`
MUST call the checkpoint engine for each affected repository immediately
before their own destructive or claim-transferring steps, best-effort and
non-fatal to the outer command. For `discarded` abort and for cleanup, the
repository is already required to be clean, so this call is normally a
no-op; it exists as a defense-in-depth backstop, not as the primary
mechanism, so its failure MUST NOT block an otherwise-eligible abort or
cleanup. For `handoff`/`not_landed` abort, where a dirty worktree is
explicitly allowed, this call is the primary mechanism that preserves the
dirty state the outgoing claim is about to hand off, and its result MUST be
surfaced in the abort result so a successor can find it.

#### REQ: fleet-wide-sweep-primitive

`wb worktree checkpoint sweep` MUST checkpoint every locally known WB
worktree in one invocation, continuing past one repository's failure so a
fleet-wide run is never all-or-nothing. It is the single primitive an
external, non-WB-managed scheduler drives on an interval — see REQ:
no-daemon — rather than a mechanism WB itself schedules or supervises.

#### REQ: no-daemon

WB MUST NOT run a background process to checkpoint a worktree on a timer.
`wb worktree create` MUST NOT arrange periodic checkpointing via a daemon or
any other long-lived process it supervises. A per-worktree daemon needs
process supervision, a crash-safe lifecycle of its own, and cleanup
coordinated with worktree teardown — a heavyweight mechanism disabled at the
first sign of trouble protects nothing.

This is not merely "the calling agent should remember to call checkpoint
between steps": later evidence in the same session that motivated this
feature showed a machine sleeping four times in one hour and a *separate*
connection-loss failure while sleep was actively being prevented — proving
prevention alone does not remove the need for checkpointing, and that a
dying agent by definition never gets to run its own final command. Relying solely on the in-process caller is therefore not sufficient. REQ:
fleet-wide-sweep-primitive exists exactly to be driven by something outside
the dying agent's own process: an OS-level scheduler — launchd on macOS, cron
or a systemd timer on Linux — calling `wb worktree checkpoint sweep` on an
interval. That scheduler entry is supervised by the OS, not by WB, so it
carries none of a custom daemon's process-lifecycle cost, and a WB upgrade or
crash cannot silently disable it. Every existing Git hook invocation is a
second, complementary zero-process opportunity to call checkpoint
opportunistically.

### Detecting the gone-upstream failure directly

#### REQ: gone-upstream-warning

Because `wb worktree checkpoint` already computes the current branch, HEAD,
and dirty state on every invocation, it MUST additionally check — cheaply,
from already-fetched local refs, with no new network round trip — whether
the branch has a configured upstream whose remote-tracking ref is absent
locally, and whether HEAD carries commits not reachable from that
remote-tracking ref. When both hold, it MUST report a distinct, prominent
warning (`upstream_gone` plus the unpushed commit count) on every checkpoint,
not only the first: this is the exact shape of the incident where a
worktree's remote branch had already been deleted while local commits were
the only surviving copy.

## Dependencies

- worktree-lifecycle
- work-log

## Acceptance Criteria

### AC: checkpoint-survives-broken-and-crashed-work

**Requirements:** worktree-checkpoint#req:broken-work-always-succeeds, worktree-checkpoint#req:dedicated-non-branch-namespace, worktree-checkpoint#req:idempotent-no-op, worktree-checkpoint#req:push-default-on-best-effort, worktree-checkpoint#req:checkpoint-push-bypasses-verification-hooks, worktree-checkpoint#req:discoverable-checkpoints, worktree-checkpoint#req:explicit-non-destructive-recovery, worktree-checkpoint#req:auto-checkpoint-before-destructive-worktree-operations, worktree-checkpoint#req:fleet-wide-sweep-primitive, worktree-checkpoint#req:no-daemon, worktree-checkpoint#req:gone-upstream-warning

Real Git fixtures prove: a checkpoint of a worktree containing Go source that
fails to compile succeeds and is pushable without running verification; a
checkpoint of only untracked files is captured; a checkpoint with nothing
new since the prior one is a true no-op that writes no new object or ref; a
checkpoint commit never appears as a branch and is never an ancestor
reachable from `refs/heads/*` by anything other than plumbing; `list`
enumerates local and (with `--remote`) remote checkpoints after the
originating worktree is deleted; `restore --apply --branch` creates an
inspectable branch without mutating the caller's current branch, index, or
working tree; `wb worktree abort --disposition handoff` preserves dirty state
via an automatic checkpoint before transferring the claim; `sweep` covers
every known worktree in one run and one repository's failure does not stop
the rest; and a worktree whose upstream remote-tracking ref is missing while
HEAD carries unpushed commits is flagged on every checkpoint call.

## Open Questions

- `sweep` ships as the primitive; an actual `wb worktree checkpoint schedule
  install` (launchd on macOS, cron/systemd timer on Linux) that registers it
  on an interval does not, in this pass. Founder priority was landing the
  simple thing that protects every other lane first; the installer is
  natural follow-up work, not a redesign, once real usage shows the wanted
  interval.
- Should `wb worktree list`/`wb status` surface the same `upstream_gone`
  signal fleet-wide, independent of whether checkpoint has ever been called
  in that worktree? Checkpoint gives the signal a home immediately; a
  fleet-wide sweep is a natural follow-up but touches `wb status`'s own
  contract and is left to that feature to decide.
- Should old checkpoint refs be pruned automatically (age- or count-based),
  or is manual/operator cleanup sufficient until real usage shows the
  namespace growing unmanageably?
- Should the Node `pre-push` profile gain the same `refs/wb/checkpoints/*`
  exemption as the Go profile? Left out of this pass since WB itself is a Go
  repository; the exemption is written narrowly enough to extend later
  without redesign.

---
*This document follows the https://specscore.md/feature-specification*
