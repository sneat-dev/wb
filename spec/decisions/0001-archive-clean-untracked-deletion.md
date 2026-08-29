---
format: https://specscore.md/decision-specification
status: Approved
---

# Decision: Archive clean deletes explicitly authorised untracked paths

**Status:** Approved
**Date:** 2026-08-29
**Owner:** alex
**Tags:** archived-clone-cleanup,issue-210
**Source Idea:** —
**Supersedes:** —
**Superseded By:** —

## Context

`wb archive clean` correctly treated every untracked path as a hard blocker,
but archived clones can also retain disposable generated artifacts. A cache
name allowlist would be a second, weaker safety policy and would inevitably
miss the next tool. A broad `--force` flag would make an irreversible local
deletion too easy to invoke and would not say what the operator actually
reviewed.

## Decision

`wb archive clean` itemizes every untracked file and directory (with clone
identity and size) in its dry-run plan. It may delete untracked paths only
when the operator supplies both `--apply` and `--delete-untracked`, and only
when untracked paths are the clone's sole remaining blocker.

Before deletion WB records a durable, itemized receipt, rereads the exact
untracked manifest, and refuses any changed or additional path. It rejects
symlinks and path traversal and deletes only the approved descriptors. After a
completed receipt it reevaluates the clone and runs the normal archive prune.

## Rationale

The operator reviews concrete paths rather than guessing from a cache name.
The second flag makes the exceptional authority visible in shell history,
automation, help, and the resulting receipt. Revalidation closes the gap
between the plan and destructive action, while descriptor-anchored operations
keep deletion contained in the reviewed clone even if names are replaced.

## Declined Alternatives

### Cache-name allowlist

Treat known build-cache directory names as automatically disposable. It lost
because names are neither a proof of ownership nor a complete list of tools.

### General force flag

Let `--force` bypass the untracked-file check. It lost because it would not
express the actual reviewed set and could bypass unrelated safety checks.

## Consequences at Decision Time

The plan/report and machine-readable capability contract gain explicit
untracked-path evidence. Applying deletion performs more filesystem and Git
status work, intentionally trading a little time for a reproducible safety
receipt and a refusal on drift.

## Observed Consequences

None observed yet.

## Affected Features

- [Archived Clone Cleanup](../features/archived-clone-cleanup/README.md)

---
*This document follows the https://specscore.md/decision-specification*
