# `wb remote claim` — Fleet-Wide Task Claims: Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Claim/release/list fleet-wide task claims in the remote state repo, with git push rejection as the compare-and-swap, staleness from the machine snapshot heartbeat, and best-effort non-blocking auto-claim/auto-release in the worktree lifecycle.

**Architecture:** `internal/remotestate` gains the claim model and three `Provider` methods; `internal/remotestate/gitrepo` implements them over the existing clone/rebase/push machinery; `cmd/wb` adds `remote claim|release|claims`, joins claims with machine heartbeats for staleness, and hooks `worktree create`/`cleanup`/`abort` best-effort.

**Tech Stack:** Go 1.26, cobra, yaml.v3, existing `internal/gitops` primitives, bare-repo + pre-receive-hook test fixtures (already in the repo).

**Spec:** `docs/superpowers/specs/2026-08-24-remote-claims-design.md` (PR #143).

## Global Constraints

- Claim file `claims/<task>.yaml`: `schema_version: 1`, `task`, `login`, `machine`, `claimed_at` (UTC), optional `note`. Release **deletes** the file. Task name must match `^[A-Za-z0-9][A-Za-z0-9._-]*$`.
- Commit messages: `wb: claim <task> by <login>/<machine>`, `wb: release <task> by <login>/<machine>`, `wb: take over <task> from <old-login>/<old-machine> by <login>/<machine>`.
- CAS: fetch → read → write/delete → commit → push; on rejection rebase → **re-read** → if the rebase brought a competing claim, roll back (`git reset --hard @{u}` on the state clone only) and fail "lost the race"; else push once more.
- Same login on a different machine = another holder (softened wording only). Refresh applies only to the exact `login/machine`.
- Staleness: claim stale ⇔ holder machine's snapshot `published_at` older than the `--stale` window (default `24h`), or holder has no snapshot. Evaluated in the command layer, never in the provider.
- `--take-over` only replaces a stale claim; `--force` replaces anything, loudly; auto paths never use `--force` and auto-take-over only stale claims.
- Exit codes: 0 acquired/refreshed/released/idempotent-release; 1 held-by-other, lost race, push rejected twice, store unreachable (explicit commands); 2 unconfigured/invalid config/bad task name. Auto paths never change the host command's exit code.
- Malformed claim file: error row in `claims`; `claim` on that task refuses without `--force` (unreadable ≠ unheld).
- In all worktree-facing output say **"remote claim"** — `wb worktree` already uses bare "claim" for local Work-Log ownership; the two must never be conflated.
- Spec deviation (approved by controller): the auto-claim outcome is printed to stdout and included in `worktree create --format json` output; it is NOT written into the Work Log (the log is strictly a prompt journal). Task 5 amends the spec sentence.
- Gates for every task: `go fmt ./...` before build/test; `go build ./...`; `golangci-lint run ./...` → `0 issues` (paste output — a prior task shipped a staticcheck failure while claiming "pristine"); tests that commit via `gitops.run()` set git identity with `t.Setenv`; run key suites also with `HOME=$(mktemp -d)`.
- New public commands need `ai/skills/commands.json` coverage, skill reference, `spec/features/...`, `ai/capabilities.json` entries (sorted IDs), and `persistentFlagSupport` rows in `cmd/wb/main.go` (`--projects-root` yes for all three claim commands; `--filter`/`--org` not) — `cmd/wb/skills_test.go` and `TestPersistentFlagMatrix` enforce this.
- Coverage floor: total ≥ 58% (`MINIMUM_COVERAGE`); new code aims for full coverage of its own statements.
- Work in the worktree `/home/ai/projects/sneat-dev/wb-claims` on branch `feat/remote-claims` (created from `spec/remote-claims`); never touch the shared checkout `/home/ai/projects/sneat-dev/wb`.

---

## File structure

| Path | Responsibility |
|---|---|
| `internal/remotestate/claim.go` | `Claim`, `ClaimMode`, `ClaimOutcome`, `ReleaseOutcome`, `ClaimEntry`, name validation, YAML codec; `Provider` interface additions |
| `internal/remotestate/claim_test.go` | model + codec tests |
| `internal/remotestate/gitrepo/claims.go` | git CAS implementation of `Claim`/`Release`/`Claims` |
| `internal/remotestate/gitrepo/claims_test.go` | CAS race, refresh, release, takeover, malformed |
| `cmd/wb/remote_claim.go` | `wb remote claim` |
| `cmd/wb/remote_release.go` | `wb remote release` |
| `cmd/wb/remote_claims.go` | `wb remote claims` + staleness join helper |
| `cmd/wb/remote_status.go` (modify) | claims per machine section |
| `cmd/wb/remote_autoclaim.go` | best-effort claim/release helpers used by worktree commands |
| `cmd/wb/worktree.go` (modify) | create: auto-claim + `--no-claim`; cleanup/abort: auto-release |
| `cmd/wb/remote_claim_test.go` | command-layer tests (all claim/release/claims/auto tests live here) |
| `spec/features/remote-claims/README.md`, `spec/features/README.md`, `ai/*`, `README.md`, `docs/cli-flag-matrix.md`, spec amendment | docs/manifests |

---

### Task 1: Claim model and Provider interface

**Files:**
- Create: `internal/remotestate/claim.go`, `internal/remotestate/claim_test.go`
- Modify: `internal/remotestate/provider.go` (interface additions)

**Interfaces:**
- Produces:
  ```go
  const ClaimSchemaVersion = 1
  type Claim struct { SchemaVersion int; Task, Login, Machine string; ClaimedAt time.Time; Note string }  // yaml+json tags like Snapshot
  func (c Claim) Holder() string                    // "<login>/<machine>"
  type ClaimMode int
  const ( ClaimNormal ClaimMode = iota; ClaimTakeOverStale; ClaimForce )
  type ClaimOutcomeKind string
  const ( ClaimAcquired ClaimOutcomeKind = "acquired"; ClaimRefreshed = "refreshed"; ClaimHeld = "held"; ClaimTookOver = "took_over" )
  type ClaimOutcome struct { Kind ClaimOutcomeKind; Current Claim; Previous *Claim; Location string }
  // Kind==ClaimHeld ⇒ Current is the OTHER party's claim and no write happened.
  type ReleaseOutcomeKind string
  const ( Released ReleaseOutcomeKind = "released"; ReleaseNoop = "noop"; ReleaseHeldByOther = "held_by_other" )
  type ReleaseOutcome struct { Kind ReleaseOutcomeKind; Current *Claim; Location string }
  type ClaimEntry struct { Claim Claim; Error string }
  func ValidTaskName(task string) error             // same regex as machine; reject "", ".", ".."
  func EncodeClaim(c Claim) ([]byte, error)
  func DecodeClaim(data []byte) (Claim, error)      // errors on schema_version > ClaimSchemaVersion
  ```
- `Provider` interface gains (self-contained like Publish/List — implementations refresh their own store view):
  ```go
  Claim(ctx context.Context, claim Claim, mode ClaimMode) (ClaimOutcome, error)
  Release(ctx context.Context, task, login, machine string, force bool) (ReleaseOutcome, error)
  Claims(ctx context.Context) ([]ClaimEntry, error)
  ```

- [ ] **Step 1: Write the failing tests**

```go
// internal/remotestate/claim_test.go
package remotestate

import (
	"strings"
	"testing"
	"time"
)

func TestClaimEncodeDecodeRoundTrip(t *testing.T) {
	claim := Claim{SchemaVersion: ClaimSchemaVersion, Task: "task-7", Login: "alice", Machine: "laptop",
		ClaimedAt: time.Date(2026, 8, 24, 9, 15, 0, 0, time.UTC), Note: "rehearsal"}
	data, err := EncodeClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	back, err := DecodeClaim(data)
	if err != nil {
		t.Fatal(err)
	}
	if back != claim {
		t.Fatalf("round trip: %+v != %+v", back, claim)
	}
	if back.Holder() != "alice/laptop" {
		t.Fatalf("Holder() = %q", back.Holder())
	}
}

func TestDecodeClaimRejectsNewerSchema(t *testing.T) {
	if _, err := DecodeClaim([]byte("schema_version: 99\ntask: t\n")); err == nil || !strings.Contains(err.Error(), "schema_version 99") {
		t.Fatalf("err = %v, want newer-schema error", err)
	}
}

func TestDecodeClaimRejectsGarbage(t *testing.T) {
	if _, err := DecodeClaim([]byte("{nope")); err == nil {
		t.Fatal("expected YAML error")
	}
}

func TestValidTaskName(t *testing.T) {
	for _, ok := range []string{"task-7", "T1", "a.b_c-d"} {
		if err := ValidTaskName(ok); err != nil {
			t.Errorf("%q: unexpected error %v", ok, err)
		}
	}
	for _, bad := range []string{"", ".", "..", "-x", "a/b", "a b", ".hidden"} {
		if err := ValidTaskName(bad); err == nil {
			t.Errorf("%q: expected error", bad)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/remotestate/ -run 'TestClaim|TestDecodeClaim|TestValidTask' -v`
Expected: FAIL — undefined symbols.

- [ ] **Step 3: Implement**

```go
// internal/remotestate/claim.go
package remotestate

import (
	"fmt"
	"time"

	"gopkg.in/yaml.v3"
)

// ClaimSchemaVersion is the claim format this binary writes and the newest
// it can read.
const ClaimSchemaVersion = 1

// Claim says one task is being worked on by one login/machine. The claim
// file's existence in the store IS the claim; releasing deletes it, so git
// history is the audit trail and no state field exists.
type Claim struct {
	SchemaVersion int       `yaml:"schema_version" json:"schema_version"`
	Task          string    `yaml:"task" json:"task"`
	Login         string    `yaml:"login" json:"login"`
	Machine       string    `yaml:"machine" json:"machine"`
	ClaimedAt     time.Time `yaml:"claimed_at" json:"claimed_at"`
	Note          string    `yaml:"note,omitempty" json:"note,omitempty"`
}

// Holder identifies the claimant: "<login>/<machine>". Mutual exclusion is
// per machine — the same login on another machine is another holder.
func (c Claim) Holder() string { return c.Login + "/" + c.Machine }

// ClaimMode selects how an existing claim by someone else is treated.
type ClaimMode int

const (
	// ClaimNormal refuses to touch anyone else's claim.
	ClaimNormal ClaimMode = iota
	// ClaimTakeOverStale replaces another holder's claim; callers must have
	// established staleness first — the provider does not judge it.
	ClaimTakeOverStale
	// ClaimForce replaces anything, including an unreadable claim file.
	ClaimForce
)

// ClaimOutcomeKind is what a Claim call did.
type ClaimOutcomeKind string

const (
	ClaimAcquired ClaimOutcomeKind = "acquired"
	ClaimRefreshed ClaimOutcomeKind = "refreshed"
	// ClaimHeld means no write happened; Current is the other holder's claim.
	ClaimHeld ClaimOutcomeKind = "held"
	ClaimTookOver ClaimOutcomeKind = "took_over"
)

// ClaimOutcome reports a Claim call so commands own all messaging.
type ClaimOutcome struct {
	Kind     ClaimOutcomeKind `json:"kind"`
	Current  Claim            `json:"current"`
	Previous *Claim           `json:"previous,omitempty"` // set on took_over
	Location string           `json:"location,omitempty"` // commit SHA / URL
}

// ReleaseOutcomeKind is what a Release call did.
type ReleaseOutcomeKind string

const (
	Released ReleaseOutcomeKind = "released"
	// ReleaseNoop: no claim existed; releasing is idempotent.
	ReleaseNoop ReleaseOutcomeKind = "noop"
	// ReleaseHeldByOther: refused (force was false); Current names the holder.
	ReleaseHeldByOther ReleaseOutcomeKind = "held_by_other"
)

// ReleaseOutcome reports a Release call.
type ReleaseOutcome struct {
	Kind     ReleaseOutcomeKind `json:"kind"`
	Current  *Claim             `json:"current,omitempty"`
	Location string             `json:"location,omitempty"`
}

// ClaimEntry is one claim as read from the store; Error is set when the
// file could not be decoded (Claim then carries only Task from the path).
type ClaimEntry struct {
	Claim Claim  `json:"claim"`
	Error string `json:"error,omitempty"`
}

// ValidTaskName enforces the machine-name rule on task names, which also
// keeps claims/<task>.yaml a safe path.
func ValidTaskName(task string) error {
	if !machineName.MatchString(task) {
		return fmt.Errorf("task %q must start with a letter or digit and contain only letters, digits, dots, underscores, or dashes", task)
	}
	return nil
}

// EncodeClaim renders a claim as YAML.
func EncodeClaim(c Claim) ([]byte, error) { return yaml.Marshal(c) }

// DecodeClaim parses a claim, refusing formats newer than this binary knows.
func DecodeClaim(data []byte) (Claim, error) {
	var c Claim
	if err := yaml.Unmarshal(data, &c); err != nil {
		return Claim{}, fmt.Errorf("parse claim: %w", err)
	}
	if c.SchemaVersion > ClaimSchemaVersion {
		return Claim{}, fmt.Errorf("claim schema_version %d is newer than supported %d; update wb", c.SchemaVersion, ClaimSchemaVersion)
	}
	return c, nil
}
```

Note: `machineName` already exists in `config.go` (same package). `ValidTaskName` reuses it; `.`/`..` are already rejected by the regex (leading dot disallowed) — verify with the test table, don't add a redundant check unless the test fails.

Add the three methods to the `Provider` interface in `provider.go` with doc comments mirroring Publish/List ("self-contained: refresh the store view before acting"), and on `Claim` add: "The provider never judges staleness; ClaimTakeOverStale merely authorizes replacing another holder — commands establish staleness first."

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go build ./... && go test ./internal/remotestate/ -v 2>&1 | grep -E '^(--- |ok|FAIL)'`
Expected: new tests PASS. `go build ./...` will now FAIL in `gitrepo` (interface not satisfied) — that is Task 2's job; to keep this task shippable, add to `internal/remotestate/gitrepo/claims.go` three stub methods returning `fmt.Errorf("claims: not implemented")` typed correctly, with a `// Implemented in the claims task.` comment. Build must be green before commit.

- [ ] **Step 5: Lint + commit**

Run: `golangci-lint run ./...` → `0 issues`.
```bash
git add internal/remotestate
git commit -m "feat(remotestate): claim model, outcomes, and Provider claim methods"
```

---

### Task 2: Git provider CAS implementation

**Files:**
- Create: `internal/remotestate/gitrepo/claims_test.go`
- Modify: `internal/remotestate/gitrepo/claims.go` (replace Task 1 stubs), possibly `internal/gitops/gitops.go` (only if a needed primitive is missing — `ResetHardUpstream` below)

**Interfaces:**
- Consumes: Task 1 types; existing gitrepo internals (`ensureClone`, `Fetch`, `push`, `abortDetailIfRebasing`), `gitops.AddCommit`, `gitops.HeadSHA`, `gitops.PullRebase`.
- Produces: `func ClaimPath(task string) string // "claims/<task>.yaml"`; the three provider methods; `gitops.ResetHardUpstream(repoPath string) error` (`git reset --hard @{u}`).

- [ ] **Step 1: Write the failing tests** (reuse `bareOrigin`, `gitIn`, `setGitIdentity`, and the pre-receive-hook helper from `provider_test.go` — read that file first; extract shared fixtures rather than duplicating them)

```go
// internal/remotestate/gitrepo/claims_test.go — test list with the assertions that matter
// (write full bodies in the style of provider_test.go):

// TestClaimAcquiresWhenAbsent: Claim(task-7, alice/laptop, ClaimNormal) →
//   Kind==ClaimAcquired, Location is a 40-char SHA; origin ls-tree shows
//   claims/task-7.yaml; origin log -1 subject == "wb: claim task-7 by alice/laptop".
// TestClaimRefreshesOwnClaim: same holder claims again with a later ClaimedAt →
//   Kind==ClaimRefreshed; origin file's claimed_at updated; two commits total.
// TestClaimHeldByOtherRefusesWithoutForce: bob/vm claims after alice →
//   Kind==ClaimHeld, Current.Holder()=="alice/laptop", no new commit, error is nil
//   (a refusal is an outcome, not an error).
// TestClaimTakeOverRecordsPrevious: bob/vm with ClaimTakeOverStale →
//   Kind==ClaimTookOver, Previous.Holder()=="alice/laptop"; commit subject
//   "wb: take over task-7 from alice/laptop by bob/vm"; file now bob's.
// TestClaimLosesRaceAfterRebase: use the pre-receive reject-first-push hook to
//   inject a competing claim for the SAME task between our commit and push →
//   returns error containing "lost the race for task-7 to", the state clone is
//   reset to @{u} (git status clean, HEAD == origin/main), and the store holds
//   the competitor's claim untouched.
// TestClaimRaceWithDifferentTaskStillLands: hook injects a claim for ANOTHER
//   task → our push succeeds after rebase (both files present at origin).
// TestReleaseDeletesOwnClaim: Kind==Released; file gone at origin; commit
//   subject "wb: release task-7 by alice/laptop"; git log still shows the
//   old claim commit (history is the audit).
// TestReleaseIsIdempotent: releasing again → Kind==ReleaseNoop, no commit.
// TestReleaseRefusesOtherHolderWithoutForce: bob releasing alice's claim,
//   force=false → Kind==ReleaseHeldByOther, file still present.
// TestReleaseForceRemovesOtherHolder: force=true → Released; commit made.
// TestClaimsListsAndSortsByTask: two claims + one malformed file
//   (schema_version: 99) → three entries sorted by Task; malformed one has
//   Error containing "schema_version 99" and Claim.Task from the filename.
// TestClaimOnMalformedFileRefusesWithoutForce: Claim on the malformed task,
//   ClaimNormal and ClaimTakeOverStale → error mentioning "unreadable";
//   ClaimForce → ClaimAcquired (Previous nil), file rewritten valid.
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/remotestate/gitrepo/ -run 'TestClaim|TestRelease' -v`
Expected: FAIL — stubs return "not implemented".

- [ ] **Step 3: Implement**

Core shape for `claims.go` (adapt to the file's existing helpers; `mutateStore` is the shared write path — implement it once and reuse):

```go
// ClaimPath is the store-relative path of one task's claim.
func ClaimPath(task string) string { return path.Join("claims", task+".yaml") }

// readClaim returns the claim at task, a flag for existence, and a decode
// error kept separate so callers can distinguish unreadable from unheld.
func (p *Provider) readClaim(task string) (claim remotestate.Claim, exists bool, decodeErr error, err error)

// mutateStore runs the shared fetch → mutate → commit → push-with-one-rebase
// sequence. check runs after every fetch/rebase and may abort (e.g. lost
// race); mutate writes or deletes files and returns the commit message.
func (p *Provider) mutateStore(check func() error, mutate func() (message string, changed bool, err error)) (sha string, err error)
```

`Claim` logic: `Fetch`; `readClaim`; decide outcome per mode (held/refresh/acquire/take-over; unreadable + !ClaimForce → error "claims/<task>.yaml is unreadable (<decodeErr>); use --force to replace it"); write file; commit with the exact message; push; on rejection `PullRebase`, **re-read the claim**: if it now belongs to someone else (and mode is not ClaimForce) → `gitops.ResetHardUpstream` and return `fmt.Errorf("lost the race for %s to %s", task, other.Holder())`; else push once more. `Release` mirrors it with delete (`os.Remove` + `AddCommit` of the path — note `git add --all -- <path>` stages deletions, which Task 4 of the previous programme verified). `Claims` walks `claims/*.yaml` (files directly under `claims/` only; a directory or deeper file becomes an error entry like `List` does).

Add to `internal/gitops/gitops.go`:

```go
// ResetHardUpstream discards local commits and tree state, returning the
// branch to its upstream. Used only on the state-repo clone to roll back a
// claim commit that lost its CAS race.
func ResetHardUpstream(repoPath string) error {
	_, err := run(repoPath, "git", "reset", "--hard", "@{u}")
	return err
}
```
with a test in `staterepo_test.go` (commit locally, reset, assert HEAD == @{u} and tree clean).

- [ ] **Step 4: Run tests**

Run: `go fmt ./... && go test ./internal/remotestate/... ./internal/gitops/ -v 2>&1 | grep -cE '^--- PASS'` and `HOME=$(mktemp -d) go test ./internal/remotestate/gitrepo/`
Expected: all pass, both runs.

- [ ] **Step 5: Lint + commit**

`golangci-lint run ./...` → `0 issues`.
```bash
git add internal
git commit -m "feat(gitrepo): claim/release/claims with push-rejection compare-and-swap"
```

---

### Task 3: `wb remote claim | release | claims` + status integration

**Files:**
- Create: `cmd/wb/remote_claim.go`, `cmd/wb/remote_release.go`, `cmd/wb/remote_claims.go`, `cmd/wb/remote_claim_test.go`
- Modify: `cmd/wb/remote.go` (register three commands), `cmd/wb/remote_status.go` + `remote_render.go` (claims per machine), `cmd/wb/main.go` (`persistentFlagSupport`: add `"remote claim"`, `"remote release"`, `"remote claims"` to `projects-root` only)

**Interfaces:**
- Consumes: `loadRemote(deps, projectsRoot)`, `remoteDeps`, Task 1/2 types, `machineRows`/staleness pattern from `remote_render.go`.
- Produces:
  ```go
  func runRemoteClaim(deps remoteDeps, projectsRoot, task, note string, takeOver, force, jsonOut bool, stale time.Duration, out io.Writer) error
  func runRemoteRelease(deps remoteDeps, projectsRoot, task string, force, jsonOut bool, out io.Writer) error
  func runRemoteClaims(deps remoteDeps, projectsRoot string, stale time.Duration, jsonOut bool, out io.Writer) error
  type claimRow struct { Task, Holder string; ClaimedAt time.Time; HeartbeatAge string; Stale bool; Note, Error string }
  func claimRows(claims []remotestate.ClaimEntry, machines []remotestate.Entry, now time.Time, stale time.Duration) []claimRow
  func holderStale(machines []remotestate.Entry, login, machine string, now time.Time, stale time.Duration) bool  // no snapshot ⇒ true
  ```

- [ ] **Step 1: Write the failing tests** (in `cmd/wb/remote_claim_test.go`, reusing `newRemoteFixture`/`remoteGit` from `remote_test.go`)

```go
// Test list — full bodies in the existing remote_test.go style:
// TestRemoteClaimAcquireReleaseRoundTrip: claim → out contains "claimed task-7"
//   and exit nil; store has the file; release → "released task-7"; release again
//   → nil error, out contains "no remote claim"; store file gone.
// TestRemoteClaimHeldByOtherExitsFindings: fixture B claims first; A's claim
//   returns *exitError code exitFindings; message contains B's holder key,
//   "heartbeat", and mentions --take-over eligibility (stale ⇒ "eligible",
//   fresh ⇒ "ask <holder> to release").
// TestRemoteClaimTakeOverRequiresStaleness: B claimed and B HAS a fresh
//   snapshot (publish as B first) → A --take-over fails exitFindings with
//   "claim is fresh"; then rewrite B's snapshot published_at 48h old via a
//   direct store commit (or publish with deps.now 48h in the past) → A
//   --take-over succeeds, out mentions "took over" and B's key.
// TestRemoteClaimNoSnapshotHolderIsStale: B claims but never publishes →
//   A --take-over succeeds immediately.
// TestRemoteClaimForceOverridesFresh: --force succeeds on a fresh claim; out
//   contains "OVERRIDING" and the holder key.
// TestRemoteClaimBadTaskNameIsUsage: task "a/b" → exitUsage before any store access
//   (deps.open must not be called — use a deps whose open panics/errors).
// TestRemoteClaimsListsWithStaleness: two claims (one fresh holder, one
//   heartbeat-less) → text has both rows, STALE on the second; --json decodes
//   to []claimRow with the same facts.
// TestRemoteStatusShowsClaims: after a claim, wb remote status text contains
//   "remote claims:" line under the holder's machine section listing task-7;
//   --json includes the claims.
// TestRemoteClaimUnconfiguredIsUsage: no config → exitUsage with snippet
//   (same as publish).
```

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/wb/ -run TestRemoteClaim -v` → FAIL undefined.

- [ ] **Step 3: Implement**

Command skeletons follow `remote_publish.go` exactly (flags → `RunE` → `runRemoteX(defaultRemoteDeps(), …)`). Messaging rules:
- Acquire: `claimed <task> for <login>/<machine> → <sha>`; refresh: `refreshed your remote claim on <task>`.
- Held: to compose the refusal, call `provider.Claims` is unnecessary — the outcome's `Current` has the holder; then `provider.List` for the heartbeat: `remote claim on <task> is held by <holder> (heartbeat <age|"never published">); ` + (stale → `it is stale — retry with --take-over` ; fresh → `it is fresh — ask them to release, or use --force`). Same-login-different-machine: prefix `by you on <machine>` instead of the bare holder key.
- Take-over: `took over <task> from <previous> (their heartbeat: <age|never>)`.
- Force: `OVERRIDING fresh remote claim by <holder> on <task>`.
- Release refusal: `remote claim on <task> is held by <holder>, not you; --force to release it anyway`.
- `claims` table columns: `TASK  HOLDER  CLAIMED_AT  HEARTBEAT  STALE  NOTE`; error rows as in `machines`.
- `remote status`: after each machine's worktree lines, `  remote claims: task-7, task-9` (only tasks that machine holds); JSON: add `claims []claimRow` to `remoteStatusReport`.
- Validate the task name with `remotestate.ValidTaskName` before `loadRemote` (bad name → `exitUsage`).
- `claim`/`release`/`claims` all accept `--json`; `claim` and `claims` accept `--stale` (default `24*time.Hour`).

- [ ] **Step 4: Run tests** — `go fmt ./... && go test ./cmd/wb/ -run 'TestRemote|TestPersistentFlagMatrix' -v 2>&1 | grep -E '^(--- |ok|FAIL)'` and the same with `HOME=$(mktemp -d)`. Full `go test ./cmd/wb/` will fail ONLY `TestAgentSkillsCoverPublicCommands`/`TestCapabilityManifest…` (Task 5 fixes; do not touch them).

- [ ] **Step 5: Lint + commit**

`golangci-lint run ./...` → `0 issues`.
```bash
git add cmd/wb
git commit -m "feat(remote): wb remote claim, release, and claims with heartbeat staleness"
```

---

### Task 4: Best-effort auto-claim and auto-release in the worktree lifecycle

**Files:**
- Create: `cmd/wb/remote_autoclaim.go`
- Modify: `cmd/wb/worktree.go` (`newWorktreeCreateCmd`, `newWorktreeCleanupCmd`, `newWorktreeAbortCmd`)
- Test: `cmd/wb/remote_claim_test.go` (append)

**Interfaces:**
- Consumes: Tasks 1–3.
- Produces:
  ```go
  // autoClaimResult is included in worktree create's JSON output.
  type autoClaimResult struct { Outcome string `json:"outcome"`; Detail string `json:"detail,omitempty"` }
  // tryAutoClaim never returns an error and never takes long-shot actions:
  // it maps unconfigured → ("disabled",""), unreachable/failed → ("skipped", err),
  // acquired/refreshed → those, held-fresh → ("held", who+softened wording),
  // held-stale → auto take-over → ("took_over", from-who).
  func tryAutoClaim(deps remoteDeps, projectsRoot, task string, stale time.Duration, out io.Writer) autoClaimResult
  func tryAutoRelease(deps remoteDeps, projectsRoot, task string, out io.Writer)  // best-effort; prints "remote claim release skipped: …" on any failure
  ```

- [ ] **Step 1: Write the failing tests**

```go
// Test list (bodies in the fixture style; every test asserts BOTH the printed
// line and, where applicable, store state):
// TestTryAutoClaimDisabledWithoutConfig: deps with missing config →
//   outcome "disabled", nothing printed (silence when the feature is off).
// TestTryAutoClaimAcquires: outcome "acquired"; out line starts
//   "remote claim: acquired task-7"; store has the file.
// TestTryAutoClaimHeldFreshWarnsAndProceeds: other fresh holder → outcome
//   "held"; out contains "remote claim: task-7 is held by bob/vm" and
//   "proceeding"; store claim untouched (still bob's).
// TestTryAutoClaimHeldByYouElsewhere: same login, other machine, fresh →
//   outcome "held"; out contains "held by you on".
// TestTryAutoClaimTakesOverStale: heartbeat-less holder → outcome
//   "took_over"; out names the previous holder; store claim is now ours.
// TestTryAutoClaimSkippedWhenUnreachable: deps.open returns a provider
//   whose store URL points at a nonexistent path (fixture helper) → outcome
//   "skipped"; out contains "remote claim skipped:"; NO error propagates.
// TestTryAutoReleaseOnlyOwnClaim: after our claim, tryAutoRelease deletes it;
//   when bob holds it, tryAutoRelease leaves it and prints
//   "remote claim release skipped: held by bob/vm".
```

For the command wiring, unit-test at the flag level (the full `worktree create` path needs repos + prompts; keep it cheap):
```go
// TestWorktreeCreateHasNoClaimFlag: newWorktreeCreateCmd().Flags().Lookup("no-claim") != nil, default "false".
// TestWorktreeCreateAutoClaimWiring: assert via persistentFlagSupport / by
//   calling the extracted hook — the RunE must call tryAutoClaim exactly when
//   (config present && !noClaim); extract the call into a small named func
//   `worktreeCreateAutoClaim(noClaim bool, task string, out io.Writer) autoClaimResult`
//   so the test can call it with a fixture config and assert the outcome.
```

- [ ] **Step 2: Run to verify failure** — `go test ./cmd/wb/ -run TestTryAuto -v` → FAIL.

- [ ] **Step 3: Implement**

`remote_autoclaim.go`: `tryAutoClaim` loads config via `remotestate.LoadConfig(deps.configPath)`; an `UnconfiguredError` (or any config error) → `{"disabled"}` silently. Then `deps.login()`; failure → `{"skipped", ...}` printed. Build `Claim{...ClaimedAt: deps.now()}`, call `provider.Claim(ctx, c, ClaimNormal)`; on `ClaimHeld`, judge staleness via `provider.List` + `holderStale`; stale → retry with `ClaimTakeOverStale`; fresh → print the held line ("held by you on X" softening) + `proceeding without the remote claim` and return. Any provider/store error at any point → `{"skipped", err}` printed as `remote claim skipped: <err>`. All prints go to the passed `out`.

Wiring in `worktree.go`:
- `create`: add `--no-claim` flag ("skip the best-effort fleet-wide remote claim for this task"). In `RunE`, after `ValidateRepositories` and BEFORE `worktrees.Create`, run the hook; keep its result and, in the `json` output branch, wrap: `{"remote_claim": <result>, "worktrees": <results>}` — **only when a claim was attempted or skipped** (outcome != "disabled"); when disabled, emit the plain `results` array exactly as today so existing consumers see no change. In text mode the hook's own printed line is the record.
- `cleanup`: in the existing `--apply` success path (after cleanup applied without error), call `tryAutoRelease(defaultRemoteDeps(), projectsRoot, task, command.OutOrStdout())`.
- `abort`: same, in the `--apply` path when disposition is `discarded` or `handoff` (not `not_landed` — the task is expected to resume). Read the RunE to place it after `worktrees.Abort` returns applied results.
- Never let any of this change the exit code: the helpers return no error by contract.

- [ ] **Step 4: Run tests** — `go fmt ./... && go test ./cmd/wb/ -run 'TestTryAuto|TestWorktreeCreate|TestRemote' -v 2>&1 | grep -E '^(--- |ok|FAIL)'`; also run the pre-existing worktree suites: `go test ./cmd/wb/ -run TestWorktree 2>&1 | tail -1` — no regressions.

- [ ] **Step 5: Lint + commit**

`golangci-lint run ./...` → `0 issues`.
```bash
git add cmd/wb
git commit -m "feat(worktree): best-effort remote claim on create, release on cleanup/abort"
```

---

### Task 5: Docs, skill, capabilities, feature spec, spec amendment

**Files:**
- Create: `spec/features/remote-claims/README.md`
- Modify: `spec/features/README.md`, `ai/skills/wb-fleet/references/remote.md`, `ai/skills/wb-fleet/SKILL.md`, `ai/skills/commands.json` (no change needed — `remote` is already covered by wb-fleet; verify), `ai/capabilities.json` (add `wb.remote.claim`, `wb.remote.claims`, `wb.remote.release`, sorted; extend `wb.worktree.*` create capability's flags with `--no-claim` if the validator demands it — check the drift error), `README.md`, `docs/cli-flag-matrix.md`, `docs/superpowers/specs/2026-08-24-remote-claims-design.md` (work-log deviation)

- [ ] **Step 1**: run `go test ./cmd/wb/ -run 'TestAgentSkills|TestCapabilityManifest' -v` and fix exactly what it reports, iterating. Model new capability entries on `wb.remote.publish`; `feature_refs: ["spec/features/remote-claims/README.md"]`; help anchors verbatim from live `--help`; `tests.references` name real functions from Tasks 3–4.
- [ ] **Step 2**: `spec/features/remote-claims/README.md` in the siblings' structure (front matter, Studio links, `## Summary`, `## Problem`, `## Behavior` with `#### REQ:` ids, `## Acceptance Criteria` with `### AC:` blocks citing REQs, `## Open Questions`). Derive REQs from the design spec's Claim record / CAS / staleness / commands / auto-claim sections; every claim must be true of the shipped code. Add the row to `spec/features/README.md`.
- [ ] **Step 3**: `references/remote.md` — add a claims table (claim/release/claims/take-over/auto-claim) and the `--no-claim` note; SKILL.md rows for claims.
- [ ] **Step 4**: README `### wb remote` section: add a short claims paragraph (explicit commands + best-effort auto-claim; staleness = heartbeat; release deletes, history audits). Flag matrix: add `remote claim`, `remote release`, `remote claims` (projects-root yes; filter/org rejected).
- [ ] **Step 5**: Amend the design spec's auto-claim bullet: outcome "is printed and included in `worktree create --format json` output; the store's git history is the durable audit (the Work Log stays a prompt journal and is not written)". Run `specscore spec lint` → 0 violations.
- [ ] **Step 6**: Gates — full `go test ./...` green (no expected failures remain), `golangci-lint run ./...` → `0 issues`.
```bash
git add spec ai README.md docs
git commit -m "docs(remote): claims feature spec, skill, capabilities, flag matrix"
```

---

### Task 6: Final verification and PR (controller)

- [ ] `go fmt ./...`; `golangci-lint run ./...`; `go build ./...`; `HOME=$(mktemp -d) go test ./...`; coverage ≥ 58% total (`go tool cover -func` on a fresh profile).
- [ ] Manual e2e against a local bare store (insteadOf redirect, real `gh` login): claim → claims → second-machine claim refusal → take-over after staleness → release; `worktree create --no-claim` and auto-claim lines.
- [ ] Push `feat/remote-claims`, PR to `main` (supersedes/includes #143's spec), watch CI, rebase if BEHIND, squash-merge with explicit subject, delete branches, close #143, pull, `go install ./cmd/wb`.

---

## Self-review against the spec

- Claim record fields, delete-on-release, commit messages, task-name rule → Tasks 1–2. ✔
- CAS with post-rebase re-read + rollback; same-task vs different-task races → Task 2. ✔
- Same-login-different-machine = another holder, softened wording → Tasks 2 (holder identity is exact login/machine) + 3 (wording). ✔
- Staleness from heartbeat, no-snapshot ⇒ stale, judged in command layer → Task 3 (`holderStale`), consumed by Task 4. ✔
- take-over stale-only, force loud, autopaths never force → Tasks 3–4. ✔
- Exit-code table incl. explicit-command-offline = exit 1 (provider error → `exitFindings` in Task 3) vs auto paths never failing → Tasks 3–4. ✔
- Malformed claim: error row + refusal without force → Task 2. ✔
- `remote status` claims; JSON shapes → Task 3. ✔
- Provider seam (three methods, typed outcomes, hub-mappable) → Task 1. ✔
- Auto-claim outcomes incl. unreachable/`--no-claim`/disabled-silence; auto-release own-claim-only; work-log deviation amended in spec → Tasks 4–5. ✔
- Gates: skills/capabilities/flag-matrix/persistentFlagSupport → Tasks 3 (allowlist) + 5 (manifests). ✔
