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

	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal)
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
		t.Fatal(err)
	}

	later := at.Add(time.Hour)
	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", later), remotestate.ClaimNormal)
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
		t.Fatal(err)
	}
	before := gitIn(t, origin, "rev-parse", "main")

	outcome, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal)
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
		t.Fatal(err)
	}

	outcome, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-7", at.Add(time.Hour)), remotestate.ClaimTakeOverStale)
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
	_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal)
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
	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at.Add(time.Minute)), remotestate.ClaimNormal)
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

func TestReleaseDeletesOwnClaim(t *testing.T) {
	origin := bareOrigin(t)
	p := machine(t, origin)
	at := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-7", at), remotestate.ClaimNormal); err != nil {
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
	if _, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-9", at), remotestate.ClaimNormal); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Claim(context.Background(), mkClaim("bob", "vm", "task-2", at), remotestate.ClaimNormal); err != nil {
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
		_, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-5", at), mode)
		if err == nil || !strings.Contains(err.Error(), "unreadable") {
			t.Fatalf("mode %v: Claim = %v, want error mentioning 'unreadable'", mode, err)
		}
	}

	outcome, err := p.Claim(context.Background(), mkClaim("alice", "laptop", "task-5", at), remotestate.ClaimForce)
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
