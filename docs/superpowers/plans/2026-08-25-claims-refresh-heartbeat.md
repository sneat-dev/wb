# Claims Refresh the Heartbeat: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Claim/release/take-over mutations stamp `last_seen_at` in the mutating machine's own snapshot (same store commit); all staleness judgments use `max(published_at, last_seen_at)`.

**Architecture:** `Snapshot.LastSeenAt` + `Heartbeat()` in `internal/remotestate`; the stamp lives inside `gitrepo`'s existing `mutateStore` mutation callback path; the command layer swaps every staleness read to `Heartbeat()` and adds a `SEEN` column.

**Spec:** `docs/superpowers/specs/2026-08-25-claims-refresh-heartbeat.md` (binding; read it first — its Rules and Failure-handling ARE the requirements).

## Global Constraints

- Additive schema only: `last_seen_at,omitempty` yaml+json; schema_version stays 1.
- `Heartbeat()` is the single max() definition; no call site computes the max itself.
- The stamp: only the caller's OWN `machines/<login>/<machine>/snapshot.yaml`; skip silently when absent/undecodable; a stamp failure never fails the claim; never create a snapshot file; `published_at` never modified by the stamp.
- Same commit: claim-file change + snapshot stamp must land atomically (one commit) so the CAS/race machinery in `mutateStore` covers both.
- Gates per task: `go fmt ./...`; `go build ./...`; targeted tests + `HOME=$(mktemp -d)` full-package run; `golangci-lint run ./...` → `0 issues` (paste); manifest validators + `specscore spec lint` in Task 2.
- Docs claims must be true of shipped code (spec features remote-state/remote-claims mention staleness = snapshot heartbeat — Task 2 amends them to "effective heartbeat (publish or claim activity)"; same for `ai/skills/wb-fleet/references/remote.md` and any capability `modes` lines that mention staleness).
- Work in `/home/ai/projects/sneat-dev/wb-heartbeat` on `feat/claims-refresh-heartbeat` (from origin/main @ 1354baf); never touch other checkouts; pin every command with `cd`; rebase before PR (main moves fast).

---

### Task 1: Model + git provider stamp

**Files:** modify `internal/remotestate/snapshot.go` (+`LastSeenAt`, `Heartbeat()`), `internal/remotestate/gitrepo/claims.go` (stamp in the mutation), tests in `internal/remotestate/snapshot_test.go` + `internal/remotestate/gitrepo/claims_test.go`.

**Interfaces produced:**
```go
// snapshot.go
LastSeenAt time.Time `yaml:"last_seen_at,omitempty" json:"last_seen_at,omitempty"`
func (s Snapshot) Heartbeat() time.Time // later of PublishedAt and LastSeenAt
// gitrepo/claims.go (unexported)
func (p *Provider) stampOwnLastSeen(login, machine string, at time.Time) // best-effort, called inside the mutate callbacks of Claim and Release before commit
```

- [ ] Step 1 (RED): tests per the spec's Testing list — `TestHeartbeatIsLaterOfPublishedAndLastSeen` (zero LastSeenAt → PublishedAt; later LastSeenAt wins; later PublishedAt wins); `TestClaimStampsOwnSnapshotLastSeen` (publish as alice → claim task → origin HEAD is ONE commit whose tree has both the claim file and alice's snapshot with `last_seen_at` == claim's ClaimedAt and unchanged `published_at`); `TestReleaseStampsOwnSnapshotLastSeen`; `TestClaimWithoutSnapshotDoesNotCreateOne` (bob never published → claim lands, `machines/…/bob/…` absent); `TestClaimWithCorruptOwnSnapshotStillLands` (write garbage snapshot → claim commit lands, snapshot bytes untouched); `TestClaimNeverTouchesOtherMachinesSnapshot` (alice publishes, bob claims → alice's file byte-identical). Reuse the existing bare-origin fixtures (`bareOrigin`, `setGitIdentity`, `gitIn`) — read `claims_test.go` first.
- [ ] Step 2: run → FAIL. Step 3: implement — read `mutateStore` and the `mutate` callbacks in `Claim`/`Release`; add the stamp INSIDE the mutate callback (after writing/deleting the claim file, before returning the commit message) so rebase-retry paths re-stamp naturally; `stampOwnLastSeen` reads the snapshot with `remotestate.Decode`, on any error returns without touching anything, else sets `LastSeenAt`, re-encodes, writes. `ClaimedAt`/operation time comes from the claim/caller — Release needs a timestamp: use the provider's existing clock source; if none exists, pass `time.Now().UTC()` at the call site in `Release` (check how `Claim` gets `ClaimedAt` — it is caller-supplied; keep Release self-contained).
- [ ] Step 4: green + `HOME=$(mktemp -d) go test ./internal/remotestate/...`; lint 0.
- [ ] Step 5: `git add internal && git commit -m "feat(remotestate): claim activity stamps last_seen_at in the machine's own snapshot"`.

### Task 2: Staleness reads + display + docs

**Files:** modify `cmd/wb/remote_render.go` (`machineRows` SEEN column + STALE from `Heartbeat()`; `holderStale` uses `Heartbeat()`; claims table HEARTBEAT column effective), `cmd/wb/remote_machines.go`/`remote_status.go` if headers live there, `cmd/wb/remote_claim.go` messaging ("heartbeat <age>" already generic — verify), tests in `cmd/wb/remote_claim_test.go`/`remote_test.go`; docs: `docs/superpowers/specs/2026-08-24-remote-claims-design.md` (staleness section: effective heartbeat), `spec/features/remote-state/README.md` + `spec/features/remote-claims/README.md` REQ/AC wording, `ai/skills/wb-fleet/references/remote.md`, `ai/capabilities.json` modes lines mentioning staleness/heartbeat.

- [ ] Step 1 (RED): `TestMachineRowsSeenReflectsLastSeen` (entry with old published_at + fresh last_seen_at → not STALE, SEEN shows the fresh age, PUBLISHED shows the old one); `TestHolderStaleHonoursLastSeen` (holder machine published 48h ago but last_seen_at 1h ago → holderStale false → `--take-over` refused with the fresh message); `TestTakeOverAllowedWhenBothOld` (both old → stale → take-over succeeds); adapt any existing tests that construct entries with only PublishedAt.
- [ ] Step 2: FAIL. Step 3: implement — every staleness site calls `entry.Snapshot.Heartbeat()`; `remoteMachineRow` gains `SeenAt time.Time`+`Seen string` (JSON additive); text header `MACHINE PUBLISHED_AT PUBLISHED SEEN STALE WB ATTENTION WORKTREES` (keep existing columns, insert SEEN after PUBLISHED); claims-table HEARTBEAT now effective age. Grep for every `PublishedAt` read in cmd/wb remote files to catch stragglers (auto-claim's stale check included).
- [ ] Step 4: full `go test ./cmd/wb/` green (+ empty-HOME variant); lint 0; `TestAgentSkills|TestCapabilityManifest` green after doc edits; `specscore spec lint` 0.
- [ ] Step 5: `git add cmd internal docs spec ai && git commit -m "feat(remote): staleness keys off the effective heartbeat; SEEN column"`.

### Task 3: Final verification and PR (controller)
- [ ] Full gates; e2e smoke against a local bare store (publish with old now → claim with fresh now → machines shows not-STALE via SEEN); rebase onto latest origin/main; PR; merge-loop when green with explicit squash subject; release watch; `wb self-update --yes`; republish VM; notify the Mac sessions to upgrade.

## Self-review against the spec
- Field + Heartbeat single definition → T1. Stamp in-commit, own-file-only, skip-silently, never-create, publish-untouched → T1 tests. Effective staleness everywhere incl. take-over + auto paths, SEEN column, claims HEARTBEAT → T2. Doc truthfulness + validators → T2. Hub note lives in the spec (no code). ✔
