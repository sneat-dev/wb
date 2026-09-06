---
format: https://specscore.md/feature-specification
status: Draft
---

# Feature: GitHub App Repository Events

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/github-app-repository-events?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/github-app-repository-events?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/github-app-repository-events?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/github-app-repository-events?op=request-change) |

**Status:** Draft
**Source Ideas:** —

## Journey

A developer enrolls one WB machine with the Workbench GitHub App provider and
publishes its bounded repository inventory. Nobody needs to watch GitHub or run
`wb sync` manually.

When GitHub reports a default-branch update, the provider verifies the signed
webhook, intersects the machine inventory with the developer's
server-authoritative GitHub installation entitlement, and durably queues a
privacy-safe event. The running WB daemon receives it through an authenticated,
bounded long poll and durably records the local sync job before acknowledging
delivery. The observable good result is that the canonical clean checkout
fast-forwards to the latest remote default branch while unrelated and active
feature worktrees remain unchanged.

If the daemon or provider restarts at any point, delivery resumes from its
opaque cursor and the same event ID is harmlessly deduplicated. If the canonical
checkout is dirty, on another branch, or unsafe to relocate after a repository
rename, WB preserves it and reports the skipped or retrying operation rather
than changing local work.

## Behavior

### Public protocol

#### REQ: versioned-safe-event-contract

The public repository-event contract MUST be versioned and MUST contain only
an opaque event ID, canonical `github.com/owner/repository` identity, full
branch ref, bounded reason, optional target object ID, optional occurrence
time, and the previous canonical identity for a rename. It MUST exclude paths,
credentials, actors, commit messages, webhook bodies, and installation IDs.

#### REQ: authenticated-bounded-long-poll

The daemon MUST call the provider through an authenticated outbound request.
Poll limits, waits, cursors, response sizes, and acknowledgement batches MUST
be strictly bounded and validated. Cursors are opaque.

#### REQ: authoritative-entitlement-intersection

A machine-published repository inventory is diagnostic client input and MUST
NOT grant repository access. The provider MUST route an event only when the
machine inventory and a server-authoritative GitHub user installation binding
both contain the canonical repository.

### Durable delivery

#### REQ: durable-enqueue-before-ack

WB MUST persist every event as a local queue job before acknowledging it. A
failed durable enqueue MUST leave the whole provider batch unacknowledged.

#### REQ: restart-safe-deduplication

Event IDs MUST deduplicate identical deliveries across retries. A reused ID
with different content MUST fail. Queue jobs, pending acknowledgements, and the
last acknowledged cursor MUST survive daemon restart and executable handoff.
The provider MUST treat replay of the same exact machine/cursor/event-ID
acknowledgement as idempotent.

#### REQ: progress-during-long-operations

The receiver and running queue job MUST report progress at least every ten
seconds while waiting or processing.

#### REQ: bounded-parallel-coalesced-refresh

The local queue MUST process different repositories with bounded parallel
workers sized from WB's effective CPU budget and admitted through the global
run queue. It MUST serialize Git writers for the same repository. Queued
default-branch events for the same repository and ref MUST coalesce to the
newest event while retaining a durable auditable `superseded` outcome for each
older event. A rename MUST remain ordered before later events for its new
repository identity.

### Safe repository refresh

#### REQ: existing-safe-sync-path

A default-branch event MUST enqueue the existing throttled WB sync operation
asynchronously. It MAY fast-forward a clean canonical checkout through WB's
safe sync logic only after its configured origin identifies the event's exact
GitHub repository. It MUST NOT force, reset, stash, discard dirty state, switch
a canonical checkout's branch, or update an active feature worktree.

#### REQ: rename-preserves-active-work

A rename event MUST carry both canonical identities and use the previous
identity for machine discovery. Local directory reconciliation MUST preserve
the old path whenever the shared guarded relocation operation cannot repair all
canonical and linked-worktree references safely. Until that shared operation is
available, the daemon MUST keep the rename job queued for retry rather than
implementing a second directory-moving path.

## Acceptance Criteria

### AC: safe-contract-and-routing

**Requirements:** github-app-repository-events#req:versioned-safe-event-contract, github-app-repository-events#req:authenticated-bounded-long-poll, github-app-repository-events#req:authoritative-entitlement-intersection

Contract tests reject local paths, unsafe refs, unsupported reasons, duplicate
IDs, mismatched cursors, and unbounded requests. Provider integration tests
prove that signed webhook translation routes only through the intersection of
machine inventory and exact-user GitHub installation entitlement.

### AC: acknowledged-only-after-durable-queue

**Requirements:** github-app-repository-events#req:durable-enqueue-before-ack, github-app-repository-events#req:restart-safe-deduplication, github-app-repository-events#req:bounded-parallel-coalesced-refresh

Deterministic receiver tests fail an enqueue mid-batch and observe no
acknowledgement. Restart tests recover queued/running jobs, reject an ID reused
for different content, and resume a persisted pending acknowledgement before
polling again. Queue tests prove different repositories run concurrently,
same-repository writers never overlap, and a burst leaves only the newest
default-branch event runnable while preserving superseded records.

### AC: canonical-fast-forward-with-work-preserved

**Requirements:** github-app-repository-events#req:existing-safe-sync-path, github-app-repository-events#req:rename-preserves-active-work, github-app-repository-events#req:progress-during-long-operations

A local Git integration test advances a bare remote, receives the event, and
observes a fast-forward of the clean canonical checkout. The same test then
makes the checkout dirty and observes that a later event leaves both HEAD and
the dirty file unchanged. Rename coverage proves the event remains retryable
until WB's shared guarded relocation operation can process it.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
