---
format: https://specscore.md/feature-specification
status: Draft
---
# Feature: Park and Resume Agent Sessions

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/park-and-resume-agent-sessions?op=request-change) |
**Status:** Draft
**Source Ideas:** —

## Summary

Park and resume lets an agent suspend one active session while preserving every
worktree it owns and bounded continuation context. A later registered session
can resume the whole bundle locally; remote resume is accepted only when every
owned checkout is reconstructable from an exact pushed commit.

## Problem

Session parking is neither worktree abort nor an implicit commit/push. Without
an auditable bundle, dirty work and secondary worktrees are silently lost or
become anonymously claimable while a handoff is pending.

## Behavior

<!-- How the feature works. Group rules under ### topic headings; write each
     enforceable rule as a `#### REQ: <slug>` entry under its topic. -->

### Session lifecycle and bundle custody

#### REQ: append-only-parked-lineage

`wb session park` MUST persist an immutable source declaration, stable parked
checkpoint ID, all worktrees owned by that session, exact Git evidence, and
bounded continuation context. It MUST project the source as parked rather than
live or claimable, and MUST NOT stage, commit, push, or remove user work.

#### REQ: idempotent-successor-resume

`wb session resume <parked-session-id>` MUST append one fresh successor lineage
and return the existing successor on retry. It MUST never create a second
active successor or partially transfer a bundle.

#### REQ: exact-remote-reconstructability

Remote resume via `--to <machine> --via ssh` MUST refuse before transport when
any owned worktree is dirty, has no remote head, or its local HEAD differs from
the recorded remote branch tip, with actionable repository/worktree evidence.

## Acceptance Criteria

The journey starts with a registered agent owning multiple worktrees, including
one dirty checkout. Parking returns a stable ID and stores both worktrees while
the source is shown as parked and no worktree is changed. A later registered
session resumes that ID and sees the whole bundle plus continuation context;
repeating resume returns the same successor. The remote epilogue either starts
from exact pushed checkouts or refuses before delivery with the first bad
worktree named.

### AC: park-preserves-whole-bundle (verifies REQ:append-only-parked-lineage)

**Given** one registered session owns two worktrees and one has uncommitted changes
**When** the agent runs `wb session park --summary ...`
**Then** a stable parked ID is returned, both exact worktree records are stored, the dirty checkout remains dirty, and `wb session list` reports the source as parked.

### AC: resume-is-one-successor (verifies REQ:idempotent-successor-resume)

**Given** a parked checkpoint and a later registered session
**When** it resumes the checkpoint twice
**Then** the same successor lineage is returned and exactly one resumed event exists.

### AC: remote-dirty-refusal (verifies REQ:exact-remote-reconstructability)

**Given** a parked bundle containing a dirty or locally-ahead worktree
**When** remote resume is requested
**Then** WB refuses before SSH delivery and names the exact worktree, HEAD, remote tip, and dirty status.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
