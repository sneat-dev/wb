package gitrepo

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/remotestate"
)

// mkClaim builds a claim ready to hand to Provider.Claim in tests.
func mkClaim(login, mach, task string, at time.Time) remotestate.Claim {
	return remotestate.Claim{SchemaVersion: remotestate.ClaimSchemaVersion, Task: task, Login: login, Machine: mach, ClaimedAt: at}
}

// pushClaimToRef commits a claim file directly in a throwaway clone of
// origin and pushes it to ref instead of main, synthesizing "another
// machine already has this claim commit ready" without going through a
// Provider and without touching main. Mirrors pushSnapshotToRef.
func pushClaimToRef(t *testing.T, origin string, claim remotestate.Claim, ref string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "prep-claim")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)

	data, err := remotestate.EncodeClaim(claim)
	if err != nil {
		t.Fatal(err)
	}
	abs := filepath.Join(work, filepath.FromSlash(ClaimPath(claim.Task)))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, data, 0o644); err != nil {
		t.Fatal(err)
	}
	message := fmt.Sprintf("wb: claim %s by %s", claim.Task, claim.Holder())
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", message)
	gitIn(t, work, "push", "-q", "origin", "HEAD:"+ref)
}

func TestClaimAcquiresWhenAbsent(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("Kind = %v, want acquired", outcome.Kind)
	}
	if len(outcome.Location) != 40 {
		t.Fatalf("Location = %q, want commit sha", outcome.Location)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q lacks claims/task-7.yaml", files)
	}
	if msg := gitIn(t, origin, "log", "-1", "--format=%s", "main"); msg != "wb: claim task-7 by alice/laptop" {
		t.Fatalf("commit message = %q", msg)
	}
}

func TestClaimRefreshesOwnClaim(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	later := at.Add(time.Hour)
	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", later), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ClaimRefreshed {
		t.Fatalf("Kind = %v, want refreshed", outcome.Kind)
	}

	data, ok, err := gitops.ShowFile(origin, "main", "claims/task-7.yaml")
	if err != nil || !ok {
		t.Fatalf("show claims/task-7.yaml: ok=%v err=%v", ok, err)
	}
	claim, err := remotestate.DecodeClaim([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if !claim.ClaimedAt.Equal(later) {
		t.Fatalf("claimed_at = %v, want %v", claim.ClaimedAt, later)
	}

	var claimCommits int
	for _, l := range strings.Split(gitIn(t, origin, "log", "--format=%s", "main"), "\n") {
		if strings.HasPrefix(l, "wb: claim task-7") {
			claimCommits++
		}
	}
	if claimCommits != 2 {
		t.Fatalf("claim commits on main = %d, want 2 (acquire + refresh)", claimCommits)
	}
}

func TestClaimHeldByOtherRefusesWithoutForce(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")

	outcome, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatalf("claim held by another is a refusal, not an error: %v", err)
	}
	if outcome.Kind != remotestate.ClaimHeld {
		t.Fatalf("Kind = %v, want held", outcome.Kind)
	}
	if outcome.Current.Holder() != "alice/laptop" {
		t.Fatalf("Current.Holder() = %q, want alice/laptop", outcome.Current.Holder())
	}

	after := gitIn(t, origin, "rev-parse", "main")
	if before != after {
		t.Fatalf("origin main moved from %s to %s; a refusal must not commit", before, after)
	}
}

func TestClaimTakeOverRecordsPrevious(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	outcome, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at.Add(time.Hour)), remotestate.ClaimTakeOverStale, "alice/laptop")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ClaimTookOver {
		t.Fatalf("Kind = %v, want took_over", outcome.Kind)
	}
	if outcome.Previous == nil || outcome.Previous.Holder() != "alice/laptop" {
		t.Fatalf("Previous = %+v, want alice/laptop", outcome.Previous)
	}
	if msg := gitIn(t, origin, "log", "-1", "--format=%s", "main"); msg != "wb: take over task-7 from alice/laptop by bob/vm" {
		t.Fatalf("commit message = %q", msg)
	}

	data, ok, err := gitops.ShowFile(origin, "main", "claims/task-7.yaml")
	if err != nil || !ok {
		t.Fatal(err)
	}
	claim, err := remotestate.DecodeClaim([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Holder() != "bob/vm" {
		t.Fatalf("file holder = %q, want bob/vm", claim.Holder())
	}
}

// TestClaimLosesRaceAfterRebase proves the push-rejected -> rebase (which
// conflicts, since both commits touch the exact same claims/task-7.yaml
// path) -> re-read -> rollback path: bob's competing claim for the SAME
// task is rigged to land on origin between alice's commit and her push, via
// installRejectFirstPushHook. Alice's Claim call must fail with a
// lost-the-race error, her local commit must be fully discarded (the clone
// reset to @{u}), and origin must be left holding bob's claim untouched.
func TestClaimLosesRaceAfterRebase(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	bobClaim := mkClaim("bob", "vm", "task-7", at)
	pushClaimToRef(t, origin, bobClaim, "refs/staging/bob")
	bobSHA := gitIn(t, origin, "rev-parse", "refs/staging/bob")
	installRejectFirstPushHook(t, origin, bobSHA)

	p := machine(t, origin)
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal, "")
	if err == nil || !strings.Contains(err.Error(), "lost the race for task-7 to") {
		t.Fatalf("Claim = %v, want error mentioning 'lost the race for task-7 to'", err)
	}
	if !strings.Contains(err.Error(), "bob/vm") {
		t.Fatalf("error %q should name the winner bob/vm", err.Error())
	}

	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("clone status = %q, want clean after rollback", status)
	}
	head := gitIn(t, p.opts.ClonePath, "rev-parse", "HEAD")
	originMain := gitIn(t, origin, "rev-parse", "main")
	if head != originMain {
		t.Fatalf("clone HEAD = %s, want reset to origin main %s", head, originMain)
	}

	data, ok, err := gitops.ShowFile(origin, "main", "claims/task-7.yaml")
	if err != nil || !ok {
		t.Fatal(err)
	}
	claim, err := remotestate.DecodeClaim([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Holder() != "bob/vm" {
		t.Fatalf("origin claim holder = %q, want bob/vm untouched", claim.Holder())
	}
}

// TestClaimRaceWithDifferentTaskStillLands proves a rebase that brings a
// DIFFERENT task's claim (no shared path, so the rebase completes cleanly)
// must not abort ours: the push is retried and both claims land.
func TestClaimRaceWithDifferentTaskStillLands(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	bobClaim := mkClaim("bob", "vm", "task-9", at)
	pushClaimToRef(t, origin, bobClaim, "refs/staging/bob")
	bobSHA := gitIn(t, origin, "rev-parse", "refs/staging/bob")
	installRejectFirstPushHook(t, origin, bobSHA)

	p := machine(t, origin)
	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatalf("Claim with induced push rejection for a different task: %v", err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("Kind = %v, want acquired", outcome.Kind)
	}

	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"claims/task-7.yaml", "claims/task-9.yaml"} {
		if !strings.Contains(files, want) {
			t.Fatalf("origin tree %q lacks %s", files, want)
		}
	}
}

// pushReleaseToRef commits the deletion of task's claim file in a throwaway
// clone of origin and pushes it to ref, synthesizing "another machine's
// release commit is ready to land" the same way pushClaimToRef synthesizes
// a competing claim.
func pushReleaseToRef(t *testing.T, origin, task, message, ref string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "prep-release")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)
	abs := filepath.Join(work, filepath.FromSlash(ClaimPath(task)))
	if err := os.Remove(abs); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", message)
	gitIn(t, work, "push", "-q", "origin", "HEAD:"+ref)
}

// TestClaimTakeOverRacingReleaseAcquires proves the fix for the reviewer's
// bug: bob holds task-7, alice calls Claim with ClaimTakeOverStale, and
// bob's Release is rigged (via installRejectFirstPushHook) to land on main
// between alice's fetch and her push. Her takeover commit (which MODIFIES
// claims/task-7.yaml) then rebases onto bob's release commit (which
// DELETES the same file) — a genuine modify/delete conflict, so
// mutateStore aborts, resets to upstream, and re-reads the path as gone.
// Before the fix, onLostRace called .Holder() on a zero Claim and returned
// the nonsense "lost the race for task-7 to /". After the fix, it retries
// the whole Claim once against the now-current (empty) store state, which
// simply acquires the now-free task for alice.
func TestClaimTakeOverRacingReleaseAcquires(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	bob := machine(t, origin)
	if _, err := bob.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	pushReleaseToRef(t, origin, "task-7", "wb: release task-7 by bob/vm", "refs/staging/bob-release")
	releaseSHA := gitIn(t, origin, "rev-parse", "refs/staging/bob-release")
	installRejectFirstPushHook(t, origin, releaseSHA)

	alice := machine(t, origin)
	outcome, err := alice.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(time.Hour)), remotestate.ClaimTakeOverStale, "bob/vm")
	if err != nil {
		t.Fatalf("Claim racing a release: %v", err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("Kind = %v, want acquired (the task was free by the time alice landed)", outcome.Kind)
	}
	if outcome.Previous != nil {
		t.Fatalf("Previous = %+v, want nil", outcome.Previous)
	}
	if outcome.Current.Holder() != "alice/laptop" {
		t.Fatalf("Current.Holder() = %q, want alice/laptop", outcome.Current.Holder())
	}

	data, ok, err := gitops.ShowFile(origin, "main", "claims/task-7.yaml")
	if err != nil || !ok {
		t.Fatal(err)
	}
	claim, err := remotestate.DecodeClaim([]byte(data))
	if err != nil {
		t.Fatal(err)
	}
	if claim.Holder() != "alice/laptop" {
		t.Fatalf("origin claim holder = %q, want alice/laptop", claim.Holder())
	}

	if status := gitIn(t, alice.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("alice's clone status = %q, want clean", status)
	}
	if inProgress, err := gitops.RebaseInProgress(alice.opts.ClonePath); err != nil || inProgress {
		t.Fatalf("rebase in progress = %v (err %v), want false", inProgress, err)
	}
}

// TestReleaseRacingReleaseIsNoop documents the reviewer-verified delete/
// delete behaviour for Release: two commits deleting the exact same path
// are, to git's three-way merge, identical outcomes regardless of the
// content each side started from, so PullRebase auto-skips alice's
// now-empty commit and never even reaches onLostRace (verified directly:
// `git pull --rebase` on a delete rebased onto an unrelated concurrent
// delete of the same path exits 0 with no rebase in progress). This test
// pins that behaviour end to end through Provider.Release: racing a
// concurrent release of the same claim must not error, and must leave the
// store and the clone in a clean, released state.
func TestReleaseRacingReleaseIsNoop(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	alice := machine(t, origin)
	if _, err := alice.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	pushReleaseToRef(t, origin, "task-7", "wb: release task-7 by alice/laptop (concurrent)", "refs/staging/concurrent-release")
	releaseSHA := gitIn(t, origin, "rev-parse", "refs/staging/concurrent-release")
	installRejectFirstPushHook(t, origin, releaseSHA)

	outcome, err := alice.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err != nil {
		t.Fatalf("Release racing a release: %v", err)
	}
	if outcome.Kind != remotestate.Released {
		t.Fatalf("Kind = %v, want released", outcome.Kind)
	}

	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if strings.Contains(files, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q still has claims/task-7.yaml", files)
	}
	if status := gitIn(t, alice.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("alice's clone status = %q, want clean", status)
	}

	// Idempotent: releasing again is now a genuine no-op.
	again, err := alice.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	if again.Kind != remotestate.ReleaseNoop {
		t.Fatalf("second Release Kind = %v, want noop", again.Kind)
	}
}

// TestClaimRefreshIdenticalBytesMakesNoCommit proves the mutate-detects-
// byte-identical-refresh short circuit in mutateStore: refreshing with the
// exact same holder and ClaimedAt encodes to the exact same bytes already
// on disk, so mutate reports changed=false and mutateStore never commits
// or pushes.
func TestClaimRefreshIdenticalBytesMakesNoCommit(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")
	beforeCount := gitIn(t, origin, "rev-list", "--count", "main")

	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ClaimRefreshed {
		t.Fatalf("Kind = %v, want refreshed", outcome.Kind)
	}

	after := gitIn(t, origin, "rev-parse", "main")
	afterCount := gitIn(t, origin, "rev-list", "--count", "main")
	if before != after {
		t.Fatalf("origin main moved from %s to %s; identical refresh must not commit", before, after)
	}
	if beforeCount != afterCount {
		t.Fatalf("commit count changed from %s to %s; identical refresh must not commit", beforeCount, afterCount)
	}
	if outcome.Location != after {
		t.Fatalf("Location = %q, want current HEAD %q", outcome.Location, after)
	}
}

func TestReleaseDeletesOwnClaim(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	outcome, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.Released {
		t.Fatalf("Kind = %v, want released", outcome.Kind)
	}

	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if strings.Contains(files, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q still has claims/task-7.yaml", files)
	}
	if msg := gitIn(t, origin, "log", "-1", "--format=%s", "main"); msg != "wb: release task-7 by alice/laptop" {
		t.Fatalf("commit message = %q", msg)
	}

	log := gitIn(t, origin, "log", "--format=%s", "main")
	if !strings.Contains(log, "wb: claim task-7 by alice/laptop") {
		t.Fatalf("history lost the original claim commit: %q", log)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Release(context.Background(), "task-7", "alice", "laptop", false); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")

	outcome, err := p.Release(context.Background(), "task-7", "alice", "laptop", false)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ReleaseNoop {
		t.Fatalf("Kind = %v, want noop", outcome.Kind)
	}
	after := gitIn(t, origin, "rev-parse", "main")
	if before != after {
		t.Fatalf("origin main moved from %s to %s; idempotent release must not commit", before, after)
	}
}

func TestReleaseRefusesOtherHolderWithoutForce(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	outcome, err := p.Release(context.Background(), "task-7", "bob", "vm", false)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ReleaseHeldByOther {
		t.Fatalf("Kind = %v, want held_by_other", outcome.Kind)
	}

	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q lost claims/task-7.yaml, want it untouched", files)
	}
}

func TestReleaseForceRemovesOtherHolder(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	outcome, err := p.Release(context.Background(), "task-7", "bob", "vm", true)
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.Released {
		t.Fatalf("Kind = %v, want released", outcome.Kind)
	}
	files := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if strings.Contains(files, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q still has claims/task-7.yaml", files)
	}
}

func TestClaimsListsAndSortsByTask(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-9", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-2", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	// Drop a malformed claim file directly in a second clone.
	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	bad := filepath.Join(other, "claims", "task-5.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "corrupt claim")
	gitIn(t, other, "push", "-q", "origin", "main")

	entries, err := p.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 {
		t.Fatalf("entries = %d, want 3", len(entries))
	}
	tasks := make([]string, 0, len(entries))
	for _, e := range entries {
		tasks = append(tasks, e.Claim.Task)
	}
	if strings.Join(tasks, " ") != "task-2 task-5 task-9" {
		t.Fatalf("tasks = %v, want sorted by task", tasks)
	}

	var bad5 remotestate.ClaimEntry
	for _, e := range entries {
		if e.Claim.Task == "task-5" {
			bad5 = e
		}
	}
	if bad5.Claim.Task != "task-5" || !strings.Contains(bad5.Error, "schema_version 99") {
		t.Fatalf("malformed entry = %+v, want Task=task-5 and Error mentioning 'schema_version 99'", bad5)
	}
}

func TestClaimOnMalformedFileRefusesWithoutForce(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)

	other := filepath.Join(t.TempDir(), "other")
	gitIn(t, t.TempDir(), "clone", "-q", origin, other)
	bad := filepath.Join(other, "claims", "task-5.yaml")
	if err := os.MkdirAll(filepath.Dir(bad), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bad, []byte("schema_version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, other, "add", "-A")
	gitIn(t, other, "commit", "-q", "-m", "corrupt claim")
	gitIn(t, other, "push", "-q", "origin", "main")

	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	for _, mode := range []remotestate.ClaimMode{remotestate.ClaimNormal, remotestate.ClaimTakeOverStale} {
		_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-5", at), mode, "")
		if err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("mode %v: Claim = %v, want error mentioning 'unreadable'", mode, err)
		}
	}

	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-5", at), remotestate.ClaimForce, "")
	if err != nil {
		t.Fatal(err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("Kind = %v, want acquired", outcome.Kind)
	}
	if outcome.Previous != nil {
		t.Fatalf("Previous = %+v, want nil", outcome.Previous)
	}

	data, ok, err := gitops.ShowFile(origin, "main", "claims/task-5.yaml")
	if err != nil || !ok {
		t.Fatal(err)
	}
	claim, err := remotestate.DecodeClaim([]byte(data))
	if err != nil {
		t.Fatalf("file not valid after force claim: %v", err)
	}
	if claim.Holder() != "alice/laptop" {
		t.Fatalf("holder = %q, want alice/laptop", claim.Holder())
	}
}

// TestClaimDoubleRejectionResetsCloneAndCanRetry proves the fix for the
// reviewer's bug: unlike Publish, a claims mutation must not keep its local
// commit after a second push rejection in a row, because a kept claims
// commit can go on to conflict with a competing same-task commit on origin,
// and Provider.Fetch has no recovery for a wedged rebase — every later
// remote command on the machine would fail until someone manually reset the
// clone. With every push rejected, Claim must fail mentioning "retry", but
// leave the clone clean and reset to upstream so it stays healthy; once
// pushes are allowed again, a plain Fetch and a fresh Claim on the SAME
// Provider must succeed.
func TestClaimDoubleRejectionResetsCloneAndCanRetry(t *testing.T) {
	origin := bareOrigin(t)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	installRejectAlwaysPushHook(t, origin)

	p := machine(t, origin)
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, "")
	if err == nil || !strings.Contains(err.Error(), "retry") {
		t.Fatalf("Claim = %v, want an error mentioning 'retry'", err)
	}

	if status := gitIn(t, p.opts.ClonePath, "status", "--porcelain"); status != "" {
		t.Fatalf("clone status = %q, want clean after the reset", status)
	}
	head := gitIn(t, p.opts.ClonePath, "rev-parse", "HEAD")
	originMain := gitIn(t, origin, "rev-parse", "main")
	if head != originMain {
		t.Fatalf("clone HEAD = %s, want reset to origin main %s", head, originMain)
	}

	clearRejectPushHook(t, origin)

	if err := p.Fetch(context.Background()); err != nil {
		t.Fatalf("Fetch after the reset: %v", err)
	}
	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal, "")
	if err != nil {
		t.Fatalf("Claim once pushes are allowed again: %v", err)
	}
	if outcome.Kind != remotestate.ClaimAcquired {
		t.Fatalf("Kind = %v, want acquired", outcome.Kind)
	}
}

// TestClaimTakeOverExpectedHolderMismatchReturnsHeld proves the fix for the
// reviewer's holder-swap accuracy bug: a caller judges bob's claim stale,
// but by the time its ClaimTakeOverStale call lands, bob has released and
// carol has claimed the same task. The provider must not blindly replace
// carol just because the caller's earlier judgment named bob — it must
// report ClaimHeld naming carol, the actual current holder, and it must not
// write anything.
func TestClaimTakeOverExpectedHolderMismatchReturnsHeld(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	if _, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Release(context.Background(), "task-7", "bob", "vm", false); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(context.Background(), mkClaim("carol", "desktop", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")

	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(2*time.Minute)), remotestate.ClaimTakeOverStale, "bob/vm")
	if err != nil {
		t.Fatalf("Claim with a stale expectedHolder mismatch: %v", err)
	}
	if outcome.Kind != remotestate.ClaimHeld {
		t.Fatalf("Kind = %v, want held", outcome.Kind)
	}
	if outcome.Current.Holder() != "carol/desktop" {
		t.Fatalf("Current.Holder() = %q, want carol/desktop", outcome.Current.Holder())
	}

	after := gitIn(t, origin, "rev-parse", "main")
	if before != after {
		t.Fatalf("origin main moved from %s to %s; a held mismatch must not commit", before, after)
	}
}

// corruptSnapshotOnMain writes unparseable bytes directly to a machine's
// snapshot path on main, in a throwaway clone of origin, synthesizing a
// snapshot file too malformed for remotestate.Decode. Mirrors pushClaimToRef.
func corruptSnapshotOnMain(t *testing.T, origin, login, mach, garbage string) {
	t.Helper()
	work := filepath.Join(t.TempDir(), "corrupt-snapshot")
	gitIn(t, t.TempDir(), "clone", "-q", origin, work)
	abs := filepath.Join(work, filepath.FromSlash(SnapshotPath(login, mach)))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, []byte(garbage), 0o644); err != nil {
		t.Fatal(err)
	}
	gitIn(t, work, "add", "-A")
	gitIn(t, work, "commit", "-q", "-m", "corrupt snapshot")
	gitIn(t, work, "push", "-q", "origin", "main")
}

// TestClaimStampsOwnSnapshotLastSeen: a claim by a machine that has
// published stamps last_seen_at in that machine's own snapshot, in the SAME
// commit as the claim file, leaving published_at untouched.
func TestClaimStampsOwnSnapshotLastSeen(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	published := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", published)); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")

	claimedAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", claimedAt), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	if n := gitIn(t, origin, "rev-list", "--count", before+"..main"); n != "1" {
		t.Fatalf("commits after claim = %s, want 1 (claim file + snapshot stamp in one commit)", n)
	}
	tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	for _, want := range []string{"claims/task-7.yaml", "machines/alice/laptop/snapshot.yaml"} {
		if !strings.Contains(tree, want) {
			t.Fatalf("origin tree %q lacks %s", tree, want)
		}
	}
	data := gitIn(t, origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	got, err := remotestate.Decode([]byte(data))
	if err != nil {
		t.Fatalf("decode stamped snapshot: %v", err)
	}
	if !got.LastSeenAt.Equal(claimedAt) {
		t.Fatalf("LastSeenAt = %v, want %v", got.LastSeenAt, claimedAt)
	}
	if !got.PublishedAt.Equal(published) {
		t.Fatalf("PublishedAt = %v, want unchanged %v", got.PublishedAt, published)
	}
}

// TestReleaseStampsOwnSnapshotLastSeen mirrors TestClaimStampsOwnSnapshotLastSeen
// for Release, which has no caller-supplied operation time.
func TestReleaseStampsOwnSnapshotLastSeen(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	published := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", published)); err != nil {
		t.Fatal(err)
	}
	claimedAt := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", claimedAt), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")
	notBefore := time.Now().UTC()

	if _, err := p.Release(context.Background(), "task-7", "alice", "laptop", false); err != nil {
		t.Fatal(err)
	}
	notAfter := time.Now().UTC()

	if n := gitIn(t, origin, "rev-list", "--count", before+"..main"); n != "1" {
		t.Fatalf("commits after release = %s, want 1 (release + snapshot stamp in one commit)", n)
	}
	data := gitIn(t, origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	got, err := remotestate.Decode([]byte(data))
	if err != nil {
		t.Fatalf("decode stamped snapshot: %v", err)
	}
	if got.LastSeenAt.Before(notBefore) || got.LastSeenAt.After(notAfter) {
		t.Fatalf("LastSeenAt = %v, want between %v and %v", got.LastSeenAt, notBefore, notAfter)
	}
	if !got.PublishedAt.Equal(published) {
		t.Fatalf("PublishedAt = %v, want unchanged %v", got.PublishedAt, published)
	}
}

// TestClaimWithoutSnapshotDoesNotCreateOne: a claim by a machine that never
// published still lands, and no snapshot file is fabricated for it.
func TestClaimWithoutSnapshotDoesNotCreateOne(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)

	if _, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q lacks claims/task-7.yaml", tree)
	}
	if strings.Contains(tree, "machines/bob") {
		t.Fatalf("origin tree %q unexpectedly has a machines/bob snapshot; bob never published", tree)
	}
	if n := gitIn(t, origin, "rev-list", "--count", "main"); n != "2" {
		t.Fatalf("commit count on main = %s, want 2 (seed + claim, no extra commit)", n)
	}
}

// TestClaimWithCorruptOwnSnapshotStillLands: a claim by a machine whose own
// snapshot file exists but fails to decode still lands the claim, and the
// corrupt bytes are left exactly as they were (skip silently, never touch).
func TestClaimWithCorruptOwnSnapshotStillLands(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	corruptSnapshotOnMain(t, origin, "alice", "laptop", "{not yaml")
	before := gitIn(t, origin, "show", "main:machines/alice/laptop/snapshot.yaml")

	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(tree, "claims/task-7.yaml") {
		t.Fatalf("origin tree %q lacks claims/task-7.yaml", tree)
	}
	after := gitIn(t, origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if after != before {
		t.Fatalf("corrupt snapshot bytes changed:\nbefore %q\nafter  %q", before, after)
	}
}

// TestClaimNeverTouchesOtherMachinesSnapshot: a claim by bob must never
// modify alice's snapshot, and must not fabricate one for bob either.
func TestClaimNeverTouchesOtherMachinesSnapshot(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	published := time.Date(2026, 8, 20, 9, 0, 0, 0, time.UTC)
	if _, err := p.Publish(context.Background(), snap("alice", "laptop", published)); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "show", "main:machines/alice/laptop/snapshot.yaml")

	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at), remotestate.ClaimNormal, ""); err != nil {
		t.Fatal(err)
	}

	after := gitIn(t, origin, "show", "main:machines/alice/laptop/snapshot.yaml")
	if after != before {
		t.Fatalf("alice's snapshot changed after bob's claim:\nbefore %q\nafter  %q", before, after)
	}
	tree := gitIn(t, origin, "ls-tree", "-r", "--name-only", "main")
	if strings.Contains(tree, "machines/bob") {
		t.Fatalf("origin tree %q unexpectedly has a machines/bob snapshot; bob never published", tree)
	}
}
