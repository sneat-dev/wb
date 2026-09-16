package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// wtLogCovReconciliationClaim returns one valid immutable v1 claim plus the
// matching projection for the given worktree.
func wtLogCovReconciliationClaim(worktree string) (workLogClaim, workLogProjection) {
	claim := workLogClaim{Version: 1, EffortID: "effort", RunID: "run", Task: "task",
		Repository: "acme/app", Worktree: worktree, Branch: "wb/x", Base: "main",
		BaseSHA: strings.Repeat("a", 40), Lifecycle: "active", RecordedAt: time.Now().UTC()}
	claim.ClaimID = workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository,
		WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	projection := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID,
		ClaimID: claim.ClaimID, Lifecycle: "active"}
	return claim, projection
}

func TestWtLogCovValidateBranchReconciliationOptions(t *testing.T) {
	valid := LogRecoverOptions{Worktree: "/tmp/wt", ReconcileBranch: "wb/x", ExpectedHead: strings.Repeat("a", 40),
		Actor: "operator", Reason: "rebind", EventID: "event-1", Remote: true}
	if err := validateBranchReconciliationOptions(valid); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
	for name, mutate := range map[string]func(*LogRecoverOptions){
		"worktree":     func(o *LogRecoverOptions) { o.Worktree = "" },
		"branch":       func(o *LogRecoverOptions) { o.ReconcileBranch = "" },
		"expected":     func(o *LogRecoverOptions) { o.ExpectedHead = "" },
		"actor":        func(o *LogRecoverOptions) { o.Actor = "" },
		"reason":       func(o *LogRecoverOptions) { o.Reason = "" },
		"event id":     func(o *LogRecoverOptions) { o.EventID = "" },
		"remote":       func(o *LogRecoverOptions) { o.Remote = false },
		"takeover":     func(o *LogRecoverOptions) { o.Takeover = true },
		"bad head":     func(o *LogRecoverOptions) { o.ExpectedHead = "zzz" },
		"bad event id": func(o *LogRecoverOptions) { o.EventID = "../escape" },
		"bad branch":   func(o *LogRecoverOptions) { o.ReconcileBranch = "bad branch name" },
	} {
		options := valid
		mutate(&options)
		if err := validateBranchReconciliationOptions(options); err == nil {
			t.Errorf("invalid reconciliation options %q were accepted", name)
		}
	}
}

func TestWtLogCovCorroborateReconciliationClaimShape(t *testing.T) {
	worktree := t.TempDir()
	claim, projection := wtLogCovReconciliationClaim(worktree)
	if err := corroborateReconciliationClaimShape(worktree, projection, claim); err != nil {
		t.Fatalf("valid claim shape rejected: %v", err)
	}

	successor := claim
	successor.ParentClaimID = strings.Repeat("b", 64)
	successor.AgentID = "agent"
	successor.AcquiredVia = "recycle_failed"
	successor.ClaimID = successorWorkLogClaimID(successor.ParentClaimID, successor.AgentID, successor.AcquiredVia)
	projection.ClaimID = successor.ClaimID
	if err := corroborateReconciliationClaimShape(worktree, projection, successor); err != nil {
		t.Fatalf("valid v1 successor rejected: %v", err)
	}
	declared := successor
	declared.Version, declared.Model, declared.ModelProvenance, declared.AcquiredVia = 2, "claude-sonnet", modelProvenanceCallerDeclared, "handoff"
	declared.ClaimID = declaredSuccessorWorkLogClaimID(declared.ParentClaimID, declared.AgentID, declared.AcquiredVia,
		ClaimExecutionIdentity{Model: declared.Model})
	projection.ClaimID = declared.ClaimID
	if err := corroborateReconciliationClaimShape(worktree, projection, declared); err != nil {
		t.Fatalf("valid declared v2 successor rejected: %v", err)
	}

	for name, mutate := range map[string]func(*workLogClaim){
		"version":   func(c *workLogClaim) { c.Version = 3 },
		"effort":    func(c *workLogClaim) { c.EffortID = "other" },
		"run":       func(c *workLogClaim) { c.RunID = "other" },
		"claim":     func(c *workLogClaim) { c.ClaimID = strings.Repeat("c", 64) },
		"lifecycle": func(c *workLogClaim) { c.Lifecycle = "terminal" },
		"worktree":  func(c *workLogClaim) { c.Worktree = filepath.Join(c.Worktree, "other") },
		"task":      func(c *workLogClaim) { c.Task = "../escape" },
		"base sha":  func(c *workLogClaim) { c.BaseSHA = "zzz" },
		"digest":    func(c *workLogClaim) { c.Branch = "wb/rewritten" },
		"parent id": func(c *workLogClaim) { c.ParentClaimID = "short"; c.AgentID = "a"; c.AcquiredVia = "handoff" },
		"parent via": func(c *workLogClaim) {
			c.ParentClaimID = strings.Repeat("b", 64)
			c.AgentID = "a"
			c.AcquiredVia = "teleported"
		},
		"parent agent": func(c *workLogClaim) { c.ParentClaimID = strings.Repeat("b", 64); c.AcquiredVia = "handoff" },
	} {
		mutated := claim
		projectionCopy := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID,
			ClaimID: claim.ClaimID, Lifecycle: "active"}
		mutate(&mutated)
		if err := corroborateReconciliationClaimShape(worktree, projectionCopy, mutated); err == nil {
			t.Errorf("invalid reconciliation claim %q was accepted", name)
		}
	}
}

func TestWtLogCovReconciliationEventAndProjection(t *testing.T) {
	record := branchReconciliationRecord{EventID: "event-1", Reason: "rebind", Actor: "operator",
		LiveBranch: "wb/live", ClaimBranch: "wb/claim", LocalHead: "local", RemoteHead: "remote"}
	event := reconciliationEvent(context.Background(), t.TempDir(), record)
	if event.ID != record.EventID || event.Type != LocalEventBranchReconciled || event.Message != record.Reason {
		t.Fatalf("event = %#v", event)
	}
	if event.Extra["actor"] != "operator" || event.Extra["live_branch"] != "wb/live" || event.Extra["claim_branch"] != "wb/claim" {
		t.Fatalf("event extra = %#v", event.Extra)
	}
	if event.Git == nil {
		t.Fatal("event must carry observed Git evidence")
	}
	_, projection := wtLogCovReconciliationClaim(t.TempDir())
	local := localProjectionForReconciliation(projection)
	if local == nil || local.Version != 1 || local.EffortID != projection.EffortID || local.ClaimID != projection.ClaimID || local.Lifecycle != "active" {
		t.Fatalf("local projection = %#v", local)
	}
}

func TestWtLogCovBranchReconciliationRecordRoundTrip(t *testing.T) {
	home := t.TempDir()
	claim, _ := wtLogCovReconciliationClaim(t.TempDir())
	record := branchReconciliationRecord{Version: 1, EventID: "event-1", ClaimID: claim.ClaimID,
		Worktree: claim.Worktree, Repository: claim.Repository, ClaimBranch: claim.Branch, LiveBranch: "wb/live",
		ExpectedHead: strings.Repeat("a", 40), LocalHead: strings.Repeat("b", 40), RemoteHead: strings.Repeat("c", 40),
		TargetHead: strings.Repeat("d", 40), Actor: "operator", Reason: "rebind", Stage: "recorded", CreatedAt: time.Now().UTC()}
	directory, err := createBranchReconciliationRecord(home, claim, record)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	stored, readDirectory, err := readBranchReconciliationRecord(home, claim, record.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = readDirectory.Close() }()
	if stored.EventID != record.EventID || stored.ClaimID != claim.ClaimID || stored.Stage != "recorded" {
		t.Fatalf("stored record = %#v", stored)
	}
	if _, _, err := readBranchReconciliationRecord(home, claim, "missing-event"); err == nil {
		t.Fatal("missing reconciliation record was read")
	}

	projection := workLogProjection{Version: 1, EffortID: claim.EffortID, RunID: claim.RunID, ClaimID: claim.ClaimID, Lifecycle: "active"}
	event := reconciliationEvent(context.Background(), claim.Worktree, record)
	result, err := finishBranchReconciliation(directory, stored, claim.Worktree, event, *localProjectionForReconciliation(projection))
	if err != nil {
		t.Fatal(err)
	}
	if !result.Applied || !result.ReadyForNormalCleanup || result.Worktree != claim.Worktree || result.Verb != "recover" {
		t.Fatalf("result = %#v", result)
	}
	finished, finishedDirectory, err := readBranchReconciliationRecord(home, claim, record.EventID)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = finishedDirectory.Close() }()
	if finished.Stage != reconciliationStageComplete {
		t.Fatalf("finished stage = %q", finished.Stage)
	}
}

func TestWtLogCovCorroborateReconciliationRecord(t *testing.T) {
	claim, _ := wtLogCovReconciliationClaim("/tmp/wt")
	head := strings.Repeat("a", 40)
	options := LogRecoverOptions{Worktree: "/tmp/wt", ReconcileBranch: "wb/live", ExpectedHead: head,
		Actor: "operator", Reason: "rebind", EventID: "event-1", Remote: true}
	record := branchReconciliationRecord{Version: 1, EventID: "event-1", ClaimID: claim.ClaimID,
		Worktree: "/tmp/wt", ClaimBranch: claim.Branch, LiveBranch: "wb/live", ExpectedHead: head,
		LocalHead: strings.Repeat("b", 40), RemoteHead: strings.Repeat("c", 40), TargetHead: strings.Repeat("d", 40),
		Actor: "operator", Reason: "rebind"}
	if err := corroborateReconciliationRecord(record, claim, "/tmp/wt", options); err != nil {
		t.Fatalf("valid record rejected: %v", err)
	}
	for name, mutate := range map[string]func(*branchReconciliationRecord){
		"version":    func(r *branchReconciliationRecord) { r.Version = 2 },
		"event":      func(r *branchReconciliationRecord) { r.EventID = "other" },
		"claim":      func(r *branchReconciliationRecord) { r.ClaimID = strings.Repeat("e", 64) },
		"worktree":   func(r *branchReconciliationRecord) { r.Worktree = "/tmp/other" },
		"claim br":   func(r *branchReconciliationRecord) { r.ClaimBranch = "wb/other" },
		"live br":    func(r *branchReconciliationRecord) { r.LiveBranch = "wb/other" },
		"expected":   func(r *branchReconciliationRecord) { r.ExpectedHead = strings.Repeat("f", 40) },
		"actor":      func(r *branchReconciliationRecord) { r.Actor = "other" },
		"reason":     func(r *branchReconciliationRecord) { r.Reason = "other" },
		"local head": func(r *branchReconciliationRecord) { r.LocalHead = "zzz" },
		"remote head": func(r *branchReconciliationRecord) {
			r.RemoteHead = "zzz"
		},
		"target head": func(r *branchReconciliationRecord) { r.TargetHead = "zzz" },
	} {
		mutated := record
		mutate(&mutated)
		if err := corroborateReconciliationRecord(mutated, claim, "/tmp/wt", options); err == nil {
			t.Errorf("invalid reconciliation record %q was accepted", name)
		}
	}
}

func TestWtLogCovReconciliationClaimReaders(t *testing.T) {
	fixture := newGitFixture(t)
	claim, projection := wtLogCovReconciliationClaim(fixture.canonical)
	if err := writeWorkLogProjection(fixture.canonical, projection); err != nil {
		t.Fatal(err)
	}
	runDir, _, err := openWorkLogRun(fixture.home, claim.EffortID, claim.RunID, true)
	if err != nil {
		t.Fatal(err)
	}
	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := writeJSONImmutableAt(claims, claim.ClaimID+".json", claim, false); err != nil {
		t.Fatal(err)
	}
	_ = claims.Close()
	_ = runDir.Close()

	loadedProjection, loadedClaim, err := reconciliationClaim(fixture.home, fixture.canonical)
	if err != nil {
		t.Fatalf("reconciliation claim read failed: %v", err)
	}
	if loadedProjection.ClaimID != projection.ClaimID || loadedClaim.ClaimID != claim.ClaimID {
		t.Fatalf("loaded projection/claim = %#v/%#v", loadedProjection, loadedClaim)
	}
	unlock, err := lockBranchReconciliationClaim(fixture.home, claim)
	if err != nil {
		t.Fatal(err)
	}
	unlock()
	if _, _, err := reconciliationClaim(fixture.home, filepath.Join(t.TempDir(), "none")); err == nil {
		t.Fatal("reconciliation claim without a projection was accepted")
	}
	missingRun := claim
	missingRun.RunID = "other-run"
	if _, _, err := reconciliationClaim(fixture.home, fixture.canonical); err != nil {
		t.Fatalf("projection read depended on the claim run: %v", err)
	}
	if _, err := lockBranchReconciliationClaim(fixture.home, missingRun); err == nil {
		t.Fatal("claim lock for a missing run was acquired")
	}
}

func TestWtLogCovRequireClaimHeadsAndAbsence(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "branch", "wb/recover")
	gitTest(t, fixture.canonical, "push", "origin", "wb/recover")
	if err := requireLocalClaimHead(context.Background(), canonical, "wb/recover", head); err != nil {
		t.Fatalf("matching local head rejected: %v", err)
	}
	if err := requireLocalClaimHead(context.Background(), canonical, "wb/recover", strings.Repeat("f", 40)); err == nil {
		t.Fatal("mismatched local head was accepted")
	}
	if err := requireRemoteClaimHead(context.Background(), fixture.canonical, "wb/recover", head); err != nil {
		t.Fatalf("matching remote head rejected: %v", err)
	}
	if err := requireRemoteClaimHead(context.Background(), fixture.canonical, "wb/recover", strings.Repeat("f", 40)); err == nil {
		t.Fatal("mismatched remote head was accepted")
	}
	if err := requireRemoteClaimAbsent(context.Background(), fixture.canonical, "wb/recover"); err == nil {
		t.Fatal("present remote branch was reported absent")
	}
	if err := requireRemoteClaimAbsent(context.Background(), fixture.canonical, "wb/missing"); err != nil {
		t.Fatalf("absent remote branch rejected: %v", err)
	}
	if err := requireLocalClaimAbsent(context.Background(), canonical, "wb/missing"); err != nil {
		t.Fatalf("absent local branch rejected: %v", err)
	}
	if err := requireLocalClaimAbsent(context.Background(), canonical, "wb/recover"); err == nil {
		t.Fatal("present local branch was reported absent")
	}
}

func TestWtLogCovValidateReconciliationLifecycleEvidence(t *testing.T) {
	fixture := newGitFixture(t)
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	claim, _ := wtLogCovReconciliationClaim(fixture.canonical)
	claim.BaseSHA = head
	options := LogRecoverOptions{Worktree: fixture.canonical, ReconcileBranch: "wb/x", ExpectedHead: head,
		Actor: "operator", Reason: "rebind", EventID: "event-1", Remote: true}
	entry := ListResult{WorktreeDir: fixture.canonical, CanonicalDir: fixture.canonical, Branch: "wb/x",
		HeadSHA: head, Clean: true, IntegratedAtOrigin: true, RemoteTargetSHA: head,
		MergedPullRequest: &PullRequest{Number: 1, URL: "https://example.test/pull/1"}}
	if err := validateReconciliationLifecycleEvidence(context.Background(), entry, claim, options); err != nil {
		t.Fatalf("valid lifecycle evidence rejected: %v", err)
	}
	for name, mutate := range map[string]func(*ListResult){
		"head":       func(e *ListResult) { e.HeadSHA = strings.Repeat("f", 40) },
		"dirty":      func(e *ListResult) { e.Clean = false },
		"open PR":    func(e *ListResult) { e.OpenPullRequest = &PullRequest{Number: 2, URL: "https://example.test/pull/2"} },
		"no merged":  func(e *ListResult) { e.MergedPullRequest = nil },
		"not integ":  func(e *ListResult) { e.IntegratedAtOrigin = false },
		"no target":  func(e *ListResult) { e.RemoteTargetSHA = "" },
		"remote adv": func(e *ListResult) { e.RemoteHeadSHA = strings.Repeat("9", 40) },
	} {
		mutated := entry
		mutate(&mutated)
		if err := validateReconciliationLifecycleEvidence(context.Background(), mutated, claim, options); err == nil {
			t.Errorf("invalid lifecycle evidence %q was accepted", name)
		}
	}
	// A claimed base that is not an ancestor of the live head must be refused.
	diverged := entry
	diverged.HeadSHA = head
	diverged.RemoteHeadSHA = ""
	claim.Branch = "wb/x"
	claim.BaseSHA = strings.Repeat("a", 40)
	if err := validateReconciliationLifecycleEvidence(context.Background(), diverged, claim, options); err == nil {
		t.Fatal("unrelated claimed base was accepted")
	}
}

func TestWtLogCovReconciliationBundleHelpers(t *testing.T) {
	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	head := gitTestOutput(t, fixture.canonical, "rev-parse", "HEAD")
	gitTest(t, fixture.canonical, "branch", "wb/bundle")
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = directory.Close() }()
	if err := bundleClaimHead(context.Background(), canonical, directory, "event-1", "local", "refs/heads/wb/bundle", head); err != nil {
		t.Fatalf("bundle preservation failed: %v", err)
	}
	for _, name := range []string{"local.bundle", "local.json"} {
		if _, err := os.Stat(filepath.Join(directory.Name(), name)); err != nil {
			t.Fatalf("preserved %s missing: %v", name, err)
		}
	}
	if err := requireBundleAdvertisesClaimRef(context.Background(), canonical, filepath.Join(directory.Name(), "local.bundle"), "refs/heads/wb/bundle", head); err != nil {
		t.Fatalf("bundle advertisement rejected: %v", err)
	}
	if err := requireBundleAdvertisesClaimRef(context.Background(), canonical, filepath.Join(directory.Name(), "local.bundle"), "refs/heads/wb/bundle", strings.Repeat("f", 40)); err == nil {
		t.Fatal("bundle advertising a different head was accepted")
	}
	if err := requireBundleAdvertisesClaimRef(context.Background(), canonical, filepath.Join(directory.Name(), "local.bundle"), "refs/heads/wb/other", head); err == nil {
		t.Fatal("bundle missing the requested ref was accepted")
	}
	if err := requireBundleAdvertisesClaimRef(context.Background(), canonical, filepath.Join(t.TempDir(), "missing.bundle"), "refs/heads/wb/bundle", head); err == nil {
		t.Fatal("missing bundle was accepted")
	}
}
