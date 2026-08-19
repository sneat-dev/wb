---
format: https://specscore.md/feature-specification
status: In Review
---

# Feature: Preservation and Pre-Flight

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/cleanup-preconditions?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/cleanup-preconditions?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/cleanup-preconditions?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/cleanup-orchestration/cleanup-preconditions?op=request-change) |
**Status:** In Review
**Source Ideas:** —

## Summary

Two gates that every destructive WB operation MUST pass before it deletes
anything, and that no flag may disable:

1. **Preservation.** Capture and verify a recoverable copy of everything at
   risk — patches, untracked-file archives, stash patches, and Git bundles of
   the commits about to lose their ref — write it outside the repository, and
   print where it went.
2. **Stacked-pull-request pre-flight.** Before a branch is merged or its ref
   deleted, find every open pull request based on it, retarget each to that
   branch's own base, verify each retarget actually landed, and only then
   proceed. If any retarget fails, refuse and print the exact remedy per pull
   request.

Neither is a command. Both are preconditions, because safety you have to
remember to invoke is not safety.

## Problem

### Preservation exists only as a habit

The fleet triage of 2026-08-19 was safe only because a human captured patches
and untracked-file archives first — 21 MB across 15 canonical-clone patches and
34 dirty worktrees — before anything was deleted. Nothing in WB does that. The
same day, a hand-rolled sweep was one confirmation away from deleting 742
branches and 308 worktrees with no such capture.

A patch alone is also not enough. A patch preserves a dirty working tree; it
does not preserve the **commits** of a branch whose ref is about to be deleted.
The fleet holds 279 branches whose commits are reachable from no remote ref;
for those, only a Git bundle is a recovery.

### Deleting a base ref closes its dependents

`gh pr merge --delete-branch` on a branch that other open pull requests are
based on closes those dependents rather than retargeting them. This is
documented in the founder's own notes with a verified instance —
`sneat-co/chessraiders` pull request #61, stacked on #60, on 2026-08-10 — and
is believed to have happened repeatedly, though **establishing how many times
by hand requires reading every closed pull request's merge commit, which is
precisely why nobody has**. [Pull Request Recovery
Forensics](../pr-recovery/README.md) exists to answer that question after the
fact; this feature exists to stop it happening again.

Retargeting cannot be fire-and-forget. A retarget that appears to be issued but
did not land leaves the same outcome as never trying, so the pre-flight must
re-read the pull request and confirm.

## Interaction with Other Features

[Cleanup Orchestration](../README.md) invokes both gates per cleanup unit
(`cleanup-orchestration#req:preconditions-gate-apply`) and defines the output
contract used to report them.

These gates are **shared primitives, not orchestrator-private steps**. Every WB
surface that deletes a ref, removes a worktree, or merges a pull request MUST
invoke them: `wb worktree cleanup --apply`, `wb worktree abort --disposition
discarded --apply`, `wb worktree rename --apply`, `wb branch cleanup --apply`,
and any future merge verb. Adding a new destructive surface without these gates
is a defect in that surface.

They compose with, and never replace, the existing safety rules —
[worktree-lifecycle#req:recheck-and-compare-delete](../../worktree-lifecycle/README.md),
[branch-hygiene#req:recheck-before-mutation](../../branch-hygiene/README.md),
and the durable audit reports both features already require. Preservation is
about recoverability if a correct decision was still the wrong one; those rules
are about not making an incorrect decision.

## Behavior

### Preservation

#### REQ: preservation-has-no-opt-out

There MUST be no flag, environment variable, or configuration key that disables
preservation, and none may be added. `--preserve-dir` selects **where**;
nothing selects **whether**.

This is stated as strongly as
[branch-hygiene#req:absorbed-is-report-only](../../branch-hygiene/README.md)
and for the same reason: the moment an escape hatch exists, the run that most
needs preservation is the run that skips it, because it is the run someone is
in a hurry to finish.

When a unit holds nothing at risk — every working tree clean, no stash entries,
every branch to be deleted provably contained in the fetched target — the
capture is a manifest-only record. It is still written, still verified, and
still reported, so "preservation happened" is always true and always auditable.

#### REQ: preservation-content

For each cleanup unit, preservation MUST capture, before that unit's first
destructive operation:

1. **Tracked changes** of every working tree in the unit, as two patches:
   `git diff --binary HEAD` and `git diff --binary --cached`, so the split
   between index and working tree is recoverable rather than flattened.
2. **Untracked, non-ignored files**, as one archive per working tree, built
   from `git ls-files --others --exclude-standard -z`. Ignored files MUST NOT
   be captured by default — they are build output, and capturing them would
   turn a 21 MB capture into a many-gigabyte one. `--preserve-ignored` MAY opt
   in.
3. **Every stash entry** in each repository of the unit, as
   `git stash show -p --binary` output plus the entry's message and SHA, since
   `refs/stash` is repository-global and survives no other capture.
4. **A Git bundle for every branch whose ref this run will delete**, created as
   `git bundle create <file> <sha> --not --remotes`, so the commits survive the
   deletion of their only ref. This is the artifact a patch cannot substitute
   for, and it is what makes deleting any of the fleet's 279 remote-less
   branches reversible.
5. **A manifest** (see `#req:manifest-carries-its-own-restore`).

An artifact that cannot be produced MUST refuse its unit's destructive stages
rather than be silently skipped.

#### REQ: preservation-location-and-retention

Preservation artifacts MUST be written under `<wb-home>/preserved/<run-id>/`,
where `<run-id>` is a UTC timestamp plus a collision-resistant suffix, laid out
as `<run-id>/<owner>/<repository>/`. `--preserve-dir` overrides the root.

They MUST NOT be written inside any repository, worktree, or directory the run
may delete. A capture stored inside the thing being deleted is not a capture.

No WB command MUST ever delete a preservation artifact — not `wb cleanup`, not
`wb worktree cleanup`, not `wb layout clean`, not a future retention verb
enabled by default. Removal is a human action. WB MUST warn on stderr when the
preservation root exceeds a configurable size (default 5 GiB), naming the root
and the manual removal command, and MUST NOT act on that warning itself.

`wb audit --scope preserved` MUST report each run's root, age, artifact count,
and total size, so a human can decide what to prune with evidence.

#### REQ: preservation-is-verified-before-it-counts

After writing a unit's artifacts and before that unit's first destructive
operation, WB MUST:

1. flush and fsync every artifact and the manifest;
2. re-read each artifact and confirm it matches the SHA-256 recorded in the
   manifest;
3. run `git bundle verify` on every bundle and confirm it lists the expected
   tip SHA.

A capture that fails any of these MUST refuse that unit's destructive stages,
report the failure, and MUST NOT be overridable. An unverified capture is
indistinguishable from no capture at the moment it matters.

#### REQ: manifest-carries-its-own-restore

The manifest MUST be machine-readable, MUST be versioned, and MUST remain
meaningful after everything it describes is gone. Each entry MUST carry:
repository path, canonical remote URL, task (when the artifact belongs to a WB
task), branch, exact SHA, configured upstream, the evidence class that made the
artifact necessary, capture time, artifact path, byte size, SHA-256, and **the
exact command sequence that restores it** — `git bundle unbundle` for a bundle,
`git apply` for a patch, the extraction command for an archive.

Recording the restore commands in the artifact itself, rather than in
documentation, is what makes recovery possible for a person who finds the
directory a year later and has never read this specification.

#### REQ: preservation-reports-its-location-first

`wb cleanup --apply` and every other destructive surface MUST print the
preservation root to stderr **before** performing anything destructive
anywhere, and MUST include it in the `--format json` envelope and in the
durable audit report. An interrupted run must still have told its operator
where the recoverable copy is.

### Stacked-pull-request pre-flight

#### REQ: stacked-pr-preflight

Before merging a branch, deleting its remote ref, or deleting its local ref,
WB MUST, in this order:

1. **Enumerate.** Find every open pull request in that repository whose base
   ref is the branch in question.
2. **Resolve the new base.** Determine the retiring branch's own base, in this
   evidence order: the base ref of the retiring branch's own merged or open
   pull request; the base recorded in the WB Work Log claim for its task; the
   explicit `--base` value. If none resolves, WB MUST refuse — it MUST NOT
   assume `main`, because guessing the base is how a stack is silently
   flattened onto the wrong target.
3. **Retarget.** Change each dependent pull request's base to that resolved
   base.
4. **Verify.** Re-read each dependent pull request from the host and confirm
   its base ref now equals the resolved base and its state is still open. An
   issued retarget is not a landed retarget.
5. **Proceed** only when every dependent pull request verified.

The order is normative: enumeration and verified retargeting MUST complete
before the deletion, never alongside or after it.

#### REQ: preflight-failure-refuses-and-instructs

If enumeration, resolution, retargeting, or verification fails for any
dependent pull request, WB MUST refuse to delete or merge **that branch only**,
MUST NOT abort other branches or other units, and MUST print one line per
affected pull request carrying its URL, its current base, the intended base,
and the exact remedy command:

```
gh pr edit <number> --repo <owner>/<repo> --base <resolved-base>
```

A refusal that does not hand over the command reproduces the discoverability
failure this feature family exists to remove.

#### REQ: preflight-runs-in-dry-run

The pre-flight's enumeration, base resolution, and reporting MUST run in a dry
run, so a plan states exactly which pull requests `--apply` would retarget and
to what. A dry run MUST NOT issue the retarget mutation, and MUST NOT be able
to.

#### REQ: preflight-fails-closed-without-host-evidence

When the host is unreachable, unauthenticated, or rate-limited, WB MUST refuse
every remote-ref deletion and every merge for the affected repository, and MUST
report the missing evidence rather than proceeding without it — the same
fail-closed rule as
[branch-hygiene#req:remote-apply-fails-closed-without-pull-request-evidence](../../branch-hygiene/README.md).

Local-ref deletion is unaffected, because a local ref is invisible to the host
and its deletion cannot close a pull request. This asymmetry is also why
[cleanup-orchestration#req:serial-within-a-unit](../README.md) orders the local
stage before the remote stage.

## Acceptance Criteria

### AC: nothing-is-deleted-before-a-verified-capture-exists

**Requirements:** cleanup-preconditions#req:preservation-has-no-opt-out, cleanup-preconditions#req:preservation-content, cleanup-preconditions#req:preservation-location-and-retention, cleanup-preconditions#req:preservation-is-verified-before-it-counts, cleanup-preconditions#req:preservation-reports-its-location-first

Given a cleanup unit holding a working tree with staged changes, unstaged
changes, an untracked non-ignored file and an ignored build artifact, two stash
entries, and a branch whose commits are reachable from no remote ref and whose
local ref this run will delete, when `wb cleanup --apply` runs against a
recording filesystem and Git wrapper, then the preservation root is printed to
stderr before the first destructive operation; under that root exist the
`HEAD` patch, the cached patch, an archive containing the untracked file and
not the ignored one, a patch per stash entry, a bundle for the branch, and a
manifest; every artifact's recorded SHA-256 matches a re-read of the file;
`git bundle verify` on the bundle lists the branch tip; and the first
destructive operation appears in the recording strictly after all of that. When
the artifacts are made to fail verification, then that unit performs no
destructive operation at all. A test MUST assert that no flag, environment
variable, or configuration key disables preservation, and that a unit with
nothing at risk still produces a verified manifest.

### AC: a-preserved-deletion-is-actually-reversible

**Requirements:** cleanup-preconditions#req:preservation-content, cleanup-preconditions#req:manifest-carries-its-own-restore

Given the fixture above, when `wb cleanup --apply` completes and the branch,
its worktree, and its stash entries are gone, then executing only the command
sequences recorded in the manifest, in a clean clone, restores the branch tip
at the identical SHA with identical trees for every commit, restores the staged
and unstaged changes as two distinguishable states, restores the untracked
file, and restores each stash entry's content. The test MUST run the recorded
commands verbatim rather than reimplement them, so the manifest is proved to be
the recovery procedure and not a description of one.

### AC: no-preservation-artifact-is-ever-removed-by-wb

**Requirements:** cleanup-preconditions#req:preservation-location-and-retention

Given a preservation root containing artifacts from three earlier runs, one of
them older than any retention period WB knows about, when `wb cleanup --apply`,
`wb worktree cleanup --apply --remote`, `wb branch cleanup --scope all
--apply`, and `wb layout clean` each run to completion, then every artifact
still exists byte-for-byte. When the root exceeds the configured size
threshold, then a stderr warning names the root, its size, and a manual removal
command, and no artifact is removed. `wb audit --scope preserved` reports each
run's root, age, artifact count, and total size.

### AC: dependent-pull-requests-are-retargeted-and-verified-before-deletion

**Requirements:** cleanup-preconditions#req:stacked-pr-preflight, cleanup-preconditions#req:preflight-failure-refuses-and-instructs, cleanup-preconditions#req:preflight-runs-in-dry-run, cleanup-preconditions#req:preflight-fails-closed-without-host-evidence

Given a repository whose branch `feature-a` has a merged pull request into
`main`, and two open pull requests `#2` and `#3` based on `feature-a`, modelled
by a deterministic host double that records every call, when `wb cleanup` runs
without `--apply`, then the plan names `#2` and `#3` and the base `main` they
would be retargeted to, and the double records no mutation. When `wb cleanup
--apply` runs, then the recorded call order is: enumerate, retarget `#2`,
re-read `#2`, retarget `#3`, re-read `#3`, then delete the remote ref — and the
deletion never precedes a verification. When the double reports the retarget of
`#3` as issued but re-reads it with the old base, then `feature-a` is not
deleted, one line per affected pull request is printed carrying its URL and a
`gh pr edit <number> --repo <owner>/<repo> --base main` remedy, and other
branches in the run still complete. When `feature-a` has no pull request, no
Work Log claim, and no `--base` was given, then WB refuses rather than assuming
`main`. When the double reports the host as unreachable, then no remote ref is
deleted and no merge is performed for that repository, the missing evidence is
reported, and local-ref deletion for a contained branch still proceeds.

## Open Questions

- Should a future `wb restore <run-id>` command drive the manifest's recorded
  commands, or does adding a restore verb create a new destructive surface that
  itself needs these gates? The manifest is deliberately sufficient without
  one.
- Should preservation deduplicate identical bundles across runs by content
  hash? The 279 remote-less branches would otherwise be re-bundled on every
  sweep that touches them.

---
*This document follows the https://specscore.md/feature-specification*
