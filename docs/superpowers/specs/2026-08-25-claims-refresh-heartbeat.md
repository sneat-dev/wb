# Design: claim activity refreshes the machine heartbeat

**Date:** 2026-08-25
**Status:** Approved (direction chosen by Alex after the gap surfaced in
production; mechanism discussed in conversation)
**Builds on:** remote state snapshots (2026-08-23) and claims (2026-08-24)

## Problem (observed in production)

Staleness is derived solely from the snapshot's `published_at`. The MacBook
ran a day-long claims campaign — dozens of claim/release commits — without
re-publishing, so it showed STALE and every claim it held became
take-over-able while it was demonstrably active. Activity through the store
must count as liveness.

## Decision

Every successful claim mutation a machine performs (claim, refresh, release,
take-over — explicit or auto) also stamps `last_seen_at` in that machine's
own `machines/<login>/<machine>/snapshot.yaml`, **in the same store commit**.
The effective heartbeat becomes `max(published_at, last_seen_at)`.

Why a separate field rather than bumping `published_at`: `published_at`
dates the *scan data* (repos, worktrees). A claim commit rescans nothing;
re-dating the data would make a 3-day-old worklist look fresh. `last_seen_at`
says only "this machine acted through the store at T" — the two facts stay
independently honest.

## Rules

- `Snapshot.LastSeenAt time.Time` — additive optional field
  (`last_seen_at,omitempty`), schema_version stays 1.
- Effective heartbeat: `max(PublishedAt, LastSeenAt)`; a method
  `Snapshot.Heartbeat() time.Time` is the single definition. All staleness
  judgments (`machineRows` STALE, `holderStale` for claims and take-over
  eligibility, auto-claim's stale check) use it.
- The git provider stamps it inside the existing claim/release mutation
  (`mutateStore`): after the claim file write/delete, load the mutating
  machine's own snapshot file — if present and decodable, set
  `last_seen_at` to the operation time and include the rewrite in the same
  commit. If the snapshot is absent or undecodable, skip silently: the claim
  still lands, and a machine that never published stays "never published"
  (= stale), exactly as the claims spec already defines. Only the caller's
  OWN snapshot file is ever touched — the per-machine file-ownership rule
  holds.
- Publish continues to set `published_at` (and leaves `last_seen_at` as-is;
  it is redundant there since publish time dominates the max).
- Provider contract note (for a future hub): any successful authenticated
  mutation by a machine refreshes its `last_seen_at`; a hub does this
  server-side.
- Display: `wb remote machines` gains a `SEEN` column (effective-heartbeat
  age; equals PUBLISHED when no claim activity since). STALE keys off SEEN.
  `wb remote claims`' existing HEARTBEAT column switches to the effective
  value. `wb remote status` staleness likewise.
- Failure handling: a failed snapshot-stamp read/write inside the mutation
  must not fail the claim — degrade to not stamping (the commit proceeds
  with just the claim change). Never invent a snapshot file.

## Non-goals

- No git-history-derived liveness (hub providers have no git log; a
  first-class field maps to both).
- No change to what publish scans or when machines publish.
- No retroactive stamping of other machines' files, ever.

## Testing

- Model: Heartbeat() max semantics incl. zero LastSeenAt.
- Provider: claim → own snapshot's `last_seen_at` updated in the same
  commit (origin shows one commit; file has new value; `published_at`
  unchanged); release and take-over same; claim by a machine with NO
  snapshot → claim lands, no snapshot file created; corrupt own snapshot →
  claim lands, file untouched; another machine's snapshot never modified.
- Command layer: machines table SEEN column + STALE from effective
  heartbeat; holderStale honours LastSeenAt (fresh claim activity blocks
  take-over of an old-published machine); take-over of a claim whose holder
  has recent last_seen_at refuses without --force.
- Hermetic bare-origin fixtures as in the existing suites.

## Open questions

None — mechanism (own-file stamp in-commit), field split (published vs
last-seen), and skip-when-absent were settled in design discussion.
