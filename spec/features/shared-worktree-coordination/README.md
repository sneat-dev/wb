---
format: https://specscore.md/feature-specification
status: Implementing
---

# Feature: Shared worktree ownership and joined sessions

> [SpecScore.**Studio**](https://specscore.studio): | [Explore](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/shared-worktree-coordination?op=explore) | [Edit](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/shared-worktree-coordination?op=edit) | [Ask question](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/shared-worktree-coordination?op=ask) | [Request change](https://specscore.studio/app/github.com/sneat-dev/wb/spec/features/shared-worktree-coordination?op=request-change) |
**Status:** Implementing
**Source Ideas:** —

## Summary

Explicit ownership, joined registered sessions and durable local coordination without edit leases.

## Problem

Agents knowingly sharing one checkout currently have append-only custody history but no explicit participant membership or single current coordinator owner. Attaching through `worktree own` suggests ownership takeover. Manual coordination also lacks a general local peer inbox: existing session messaging requires a completed handoff successor. The feature makes responsibility and coordination visible without imposing file leases or additional instructions on every turn.

## Behavior

The MVP is opt-in, local and cooperative. A registered session explicitly establishes ownership with `wb worktree take-ownership <id-or-path> --expected-owner <observed-id-or-none>`. This also joins the owner. `none` is valid only when WB has no effective owner evidence, including existing creator/adopter/custody records; absence of the new coordination snapshot alone does not mean unowned. Joining never initializes or replaces ownership. Existing single-session behavior and immutable Work Log claims remain unchanged outside this mode.

| Command | Contract |
| --- | --- |
| `wb session register ... --join <id-or-path>` | Register, then invoke the same join service; report registration success separately if join fails. |
| `wb worktree join <id-or-path>` | Add the authenticated registered session as a participant, idempotently, without ownership or Git changes. |
| `wb worktree leave <id-or-path>` | Remove that participant; the current owner must transfer first. |
| `wb worktree info <id-or-path>` | Show exactly one current owner and joined sessions, with observed liveness, in text and JSON. No dedicated information command. |
| `wb worktree transfer-ownership <id-or-path> --to-session <id>` | Current owner atomically appoints a live joined successor; the previous owner remains joined. |
| `wb worktree take-ownership <id-or-path> --expected-owner <id-or-none>` | Atomically compare the observed owner and take ownership. Live or uncertain ownership additionally requires `--force --reason`; force never bypasses the comparison. |
| `wb worktree message send <id-or-path> --to-session <id> --idempotency-key <key> --message-file <file-or-dash>` | Persist a bounded local peer message. An explicit broadcast option may address current participants. Same key and payload retry is idempotent; changed payload refuses. |
| `wb worktree message inbox <id-or-path>` / `ack ... <message-id>` | Fetch new recipient messages and acknowledge consumption. Empty inbox is quiet. Cursor handling cannot skip unacknowledged messages. |

Caller identity comes from the invoking process's registered ancestor session and lifecycle, never a supplied sender ID or recipient lookup alone. Worktree paths/IDs resolve to the same corroborated checkout identity; rebinding to another checkout refuses. The current owner is always a joined participant. Ownership transitions publish a durable audit and participant notice; user-message capacity cannot veto ownership recovery. Notices may be coalesced by owner epoch outside ordinary inbox capacity.

For initial opt-in, `info` reports the effective owner from corroborated creation/adoption/custody evidence, mapped to a registered WB session when unambiguous. Otherwise expose an exact opaque legacy-owner observation ID and mark liveness/identity unresolved. A matching current owner can opt itself in; a different live or unresolved legacy owner requires its exact observed ID plus audited force/reason. Missing/unresolved evidence is never silently converted to `none`. Recheck the legacy owner observation under the existing journal lock with a consistent coordination-lock order before initial publication. A third-party `none` request refuses without side effects even with force. New owner state is thereafter explicit; participant custody events must not replace it.

Coordination metadata is private, bounded where applicable, outside tracked source, locked across processes and atomically persisted. Registration and join are separate stores and therefore do not promise cross-store atomicity. Recorded messages and consumed acknowledgements do not prove work succeeded; agents reply with outcomes. Arbitrary Git/editor writes remain cooperative. This does not transfer immutable Work Log claims, invent handoff receipts, start daemons, wake idle agents, install harness hooks or add cross-machine transport. Path/function leases and edit-hook enforcement are deferred.

## Acceptance Criteria

### AC: explicit-owner-and-membership

Initial take establishes one owner and joins it after comparing the effective existing owner evidence. `none` is accepted only for a genuinely unowned checkout. A different registered session cannot use absence of coordination state to take over a live or unresolved legacy owner; `none` refuses even with force. Matching legacy observation IDs are rechecked atomically and require the same live/unknown takeover controls. Two other registered sessions can join and leave without changing the owner, immutable claim, branch, HEAD or custody history. First join on uninitialized coordination refuses with the explicit initialization command. `info` exposes owner and participants; owner leave refuses until transfer. Repeating join is idempotent.

### AC: registration-shortcut

`session register --join` accepts a worktree ID or path and shares the join implementation. A failed join leaves a valid registered session and reports registered-but-not-joined. Omission preserves registration behavior.

### AC: ownership-transitions

Transfer requires authenticated current ownership and a live joined successor. Take requires the exact observed owner ID, including literal `none` for unowned state. Under one lock, two contenders with the same expectation yield one winner and one stale-owner refusal. Missing/wrong expectations refuse even with force; live/unknown-owner takeover needs audited force and reason. The new owner is joined, the previous owner remains joined, and saturated user inboxes cannot block ownership changes.

### AC: durable-local-messages

Only authenticated joined peers can send, read their inbox or acknowledge their own messages. Bounded private messages survive restart, carry stable IDs and caller-supplied idempotency keys, and do not duplicate after a lost response retry. Keys bind the complete immutable request: checkout/collaboration identity, authenticated sender, exact recipient set, message kind and bytes/digest; owner epoch is recorded at first acceptance but a later ownership change does not invalidate an otherwise identical retry. Changed recipients, sender, kind or bytes under one key refuse. Consumption acknowledgement is idempotent and out-of-order acknowledgement cannot skip earlier unread messages. Ownership notices remain available independently of ordinary inbox capacity. No sender or message body appears in unrelated diagnostics.

### AC: testability-and-complete-coverage

Pure membership/ownership/message transitions are separate from persistence, caller identity, checkout resolution, clock and ID generation. Reuse existing atomic-file and locking primitives; use narrow invocation-local interfaces for genuine I/O boundaries, without package globals. Every new or modified compiled production body must have a fresh source-bound 100% statement profile from the first implementation batch. Native multi-session/Git fixtures verify unchanged checkout and claim authority, exact compare-and-swap races, private persistence and restart/retry behavior. Focused race, vet, pinned lint, applicable quality guards and default compilation pass; portable new code cross-compiles for Windows without claiming Windows runtime validation.

## Open Questions

None at this time.

---
*This document follows the https://specscore.md/feature-specification*
