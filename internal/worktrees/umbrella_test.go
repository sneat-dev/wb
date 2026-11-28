package worktrees

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreebranches"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

func writeUmbrellaTestJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(raw, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
}

func makeUmbrellaTestClaim(t *testing.T, projectsRoot, task, repo, branch, umbrellaRef string, recordedAt time.Time) workLogClaim {
	t.Helper()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	wtPath := filepath.Join(projectsRoot, ".worktrees", task, repo)
	baseSHA := strings.Repeat("a", 40)
	claimID := worktreeclaims.WorkLogClaimID(task, worktreeclaims.CreationResult{
		Repository: repo, WorktreeDir: wtPath, Branch: branch, Base: "main", BaseSHA: baseSHA,
	})
	claim := workLogClaim{
		Version: 2, EffortID: task, RunID: "run-1", ClaimID: claimID, Task: task,
		Repository: repo, Worktree: wtPath, Branch: branch, Base: "main", BaseSHA: baseSHA,
		Lifecycle: "active", RecordedAt: recordedAt, Umbrella: umbrellaRef,
	}
	claimPath := filepath.Join(home, "worklogs", task, "runs", "run-1", "claims", claimID+".json")
	writeUmbrellaTestJSON(t, claimPath, claim)
	return claim
}

func TestSetTaskUmbrellaAttachesToAllClaimsAndSurvivesTerminalization(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	now := time.Now().UTC()
	c1 := makeUmbrellaTestClaim(t, projectsRoot, "cross-repo", "sneat-co/backstage", "cross-repo", "", now)
	c2 := makeUmbrellaTestClaim(t, projectsRoot, "cross-repo", "trakhimenok/workbench", "cross-repo", "", now.Add(time.Second))

	ref := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#845"}
	res, err := SetTaskUmbrella(context.Background(), projectsRoot, "cross-repo", ref)
	if err != nil {
		t.Fatalf("SetTaskUmbrella: %v", err)
	}
	if res.Task != "cross-repo" || res.Umbrella != ref.String() || res.Claims != 2 || len(res.Repositories) != 2 {
		t.Fatalf("unexpected SetTaskUmbrella result: %+v", res)
	}
	if got := UmbrellaForTask(projectsRoot, "cross-repo"); got != ref.String() {
		t.Fatalf("UmbrellaForTask = %q, want %q", got, ref.String())
	}

	// Initially both members are active -> not ready to close.
	ready, members, err := UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil {
		t.Fatalf("UmbrellaReadyToClose active: %v", err)
	}
	if ready || len(members) != 2 {
		t.Fatalf("active members ready=%v len=%d, want false/2", ready, len(members))
	}

	// Land c1 via terminal record + PR binding; archive c2 via retirement receipt.
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	RecordPullRequestLandedSHA(projectsRoot, "sneat-co/backstage", 846, "sneat-co/backstage", "cross-repo", strings.Repeat("1", 40))
	if prRef, taskName, ok := UmbrellaForPullRequest(projectsRoot, "sneat-co/backstage", 846, "sneat-co/backstage", "cross-repo", nil); !ok || prRef != ref || taskName != "cross-repo" {
		t.Fatalf("UmbrellaForPullRequest by PR = (%+v, %q, %v)", prRef, taskName, ok)
	}
	if prRef, _, ok := UmbrellaForPullRequest(projectsRoot, "trakhimenok/workbench", 999, "trakhimenok/workbench", "cross-repo", []string{"cross-repo"}); !ok || prRef != ref {
		t.Fatalf("UmbrellaForPullRequest by cleaned task = (%+v, %v)", prRef, ok)
	}
	if prRef, _, ok := UmbrellaForPullRequest(projectsRoot, "trakhimenok/workbench", 999, "trakhimenok/workbench", "cross-repo", nil); !ok || prRef != ref {
		t.Fatalf("UmbrellaForPullRequest by repo+branch = (%+v, %v)", prRef, ok)
	}

	// Seal c1 as landed and c2 as archived with restore pointer.
	term1 := workLogTerminalRecord{
		Claim: c1, FinalCommit: strings.Repeat("1", 40), Disposition: "landed", SealedAt: now,
		Landed: &worktreeclaims.LandedEvidence{
			Target: "main", LandedSHA: strings.Repeat("1", 40), Proof: worktreeclaims.LandedProofMergedPullRequest, PullRequest: 846,
		},
	}
	term1.Lifecycle = "terminal"
	writeUmbrellaTestJSON(t, filepath.Join(home, "worklogs", "cross-repo", "runs", "run-1", "terminals", c1.ClaimID+".json"), term1)

	srcSHA := strings.Repeat("2", 40)
	retiredRef := worktreebranches.RetiredBranchDestination(now, "cross-repo", srcSHA)
	retireTx := worktreeretire.Transaction{
		Version: 1, Task: "cross-repo", Repository: "trakhimenok/workbench", Branch: "cross-repo",
		ArchiveRepository: "trakhimenok/.retired", RetiredRef: retiredRef, SourceSHA: srcSHA,
		ClaimID: c2.ClaimID, Phase: "complete",
	}
	retireTx.ArchiveRef = worktreeretire.ArchiveRef(retireTx)
	writeUmbrellaTestJSON(t, worktreeretire.ReportPath(home, retireTx), retireTx)

	ready, members, err = UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil {
		t.Fatalf("UmbrellaReadyToClose finished: %v", err)
	}
	if !ready || len(members) != 2 {
		t.Fatalf("finished members ready=%v members=%+v, want true/2", ready, members)
	}
	if members[0].State != "landed" || members[0].LandedSHA != strings.Repeat("1", 40) || members[0].PullRequest != 846 {
		t.Fatalf("member 0 = %+v", members[0])
	}
	if members[1].State != "archived" || members[1].RestorePointer != "trakhimenok/.retired:"+retireTx.ArchiveRef {
		t.Fatalf("member 1 = %+v", members[1])
	}

	// TaskUmbrellaMembers returns umbrella + all recorded members even without live worktrees.
	gotRef, taskMembers, err := TaskUmbrellaMembers(projectsRoot, "cross-repo")
	if err != nil || gotRef != ref.String() || len(taskMembers) != 2 {
		t.Fatalf("TaskUmbrellaMembers = (%q, %+v, %v)", gotRef, taskMembers, err)
	}
}

func TestClassifyClaimMemberAndHandoffDeduplication(t *testing.T) {
	t.Parallel()
	now := time.Now().UTC()
	sha := strings.Repeat("d", 40)
	baseClaim := workLogClaim{Task: "t", Repository: "o/r", Branch: "b", ClaimID: strings.Repeat("a", 64), RecordedAt: now}
	succClaim := workLogClaim{Task: "t", Repository: "o/r", Branch: "b", ClaimID: strings.Repeat("b", 64), RecordedAt: now.Add(time.Second)}

	// Handoff predecessor is superseded by its successor.
	handoffTerm := &workLogTerminalRecord{Claim: baseClaim, Disposition: "handoff", SuccessorClaimID: succClaim.ClaimID}
	landedTerm := &workLogTerminalRecord{
		Claim: succClaim, Disposition: "landed",
		Landed: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: sha, Proof: worktreeclaims.LandedProofContained, PullRequest: 12},
	}
	members := buildMembersFromClaims([]claimRecord{
		{claim: baseClaim, terminal: handoffTerm},
		{claim: succClaim, terminal: landedTerm},
	})
	if len(members) != 1 || !members[0].ReadyToClose || members[0].PullRequest != 12 {
		t.Fatalf("handoff dedupe members = %+v", members)
	}
	if got := buildMembersFromClaims(nil); got != nil {
		t.Fatalf("empty buildMembersFromClaims = %+v", got)
	}

	// Cleanup record after finalize satisfies landed.
	finalizedCleaned := classifyClaimMember(claimRecord{
		claim:    baseClaim,
		terminal: &workLogTerminalRecord{Claim: baseClaim, Disposition: "landed"},
		cleanup:  &workLogCleanupRecord{FinalCommit: sha},
	})
	if !finalizedCleaned.ReadyToClose || finalizedCleaned.LandedSHA != sha {
		t.Fatalf("finalizedCleaned = %+v", finalizedCleaned)
	}

	// Non-ready dispositions keep ReadyToClose=false with exact BlockingReason.
	for _, tc := range []struct {
		disp         string
		wantContains string
	}{
		{"landed", "finalized locally"},
		{"retired", "archive restore pointer"},
		{"discarded", "discarded"},
		{"handoff", "neither landed nor archived"},
		{"not_landed", "neither landed nor archived"},
		{"orphaned", "neither landed nor archived"},
		{"removed", "neither landed nor archived"},
		{"custom_state", "neither landed nor archived"},
	} {
		m := classifyClaimMember(claimRecord{
			claim:    baseClaim,
			terminal: &workLogTerminalRecord{Claim: baseClaim, Disposition: tc.disp},
		})
		if m.ReadyToClose || !strings.Contains(m.BlockingReason, tc.wantContains) {
			t.Fatalf("disposition %q -> %+v, want blocked containing %q", tc.disp, m, tc.wantContains)
		}
	}

	// Restore pointer formatting variants.
	if got := formatRetireRestorePointer(worktreeretire.Transaction{RetiredRef: "retired/x", Preserve: "tag"}); got != "refs/tags/retired/x" {
		t.Fatalf("RetiredRef tag pointer = %q", got)
	}
	if got := formatRetireRestorePointer(worktreeretire.Transaction{ArchiveRef: "retired/r/x"}); got != "retired/r/x" {
		t.Fatalf("ArchiveRef pointer = %q", got)
	}
	if got := formatRetireRestorePointer(worktreeretire.Transaction{}); got != "" {
		t.Fatalf("empty pointer = %q", got)
	}
}

func TestSetTaskUmbrellaErrorsAndResolveDefaultRegistry(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	ref := umbrella.Ref{Registry: "github", ID: "o/r#1"}

	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, "missing-task", umbrella.Ref{}); err == nil {
		t.Fatal("zero ref accepted")
	}
	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, "missing-task", umbrella.Ref{Registry: "github", ID: "bad id"}); err == nil {
		t.Fatal("invalid ref accepted")
	}
	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, "missing-task", ref); err == nil || !strings.Contains(err.Error(), "no work-log claims found") {
		t.Fatalf("missing task error = %v", err)
	}
	nonWorktree := t.TempDir()
	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, nonWorktree, ref); err == nil || !strings.Contains(err.Error(), "not a WB-managed worktree") {
		t.Fatalf("non-worktree dir error = %v", err)
	}
	if _, err := UmbrellaMembers(projectsRoot, umbrella.Ref{}); err == nil {
		t.Fatal("UmbrellaMembers accepted zero ref")
	}
	if got := UmbrellaForTask(projectsRoot, ""); got != "" {
		t.Fatalf("empty task umbrella = %q", got)
	}
	if got := UmbrellaForWorktree(projectsRoot, nonWorktree); got != "" {
		t.Fatalf("non-worktree umbrella = %q", got)
	}

	// Manifest fallback for UmbrellaForWorktree.
	if err := os.MkdirAll(filepath.Join(nonWorktree, ".wb", "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	manifestYAML := "version: 1\neffort_id: task-x\neffort_kind: feature\nrepository: o/r\nworktree: " + nonWorktree + "\nbranch: b\nbase: main\nbase_sha: " + strings.Repeat("a", 40) + "\ncreated_at: 2026-11-28T00:00:00Z\numbrella: github:o/r#42\nprovenance: created\n"
	if err := os.WriteFile(filepath.Join(nonWorktree, ".wb", "local", "manifest.yaml"), []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := UmbrellaForWorktree(projectsRoot, nonWorktree); got != "github:o/r#42" {
		t.Fatalf("manifest fallback UmbrellaForWorktree = %q", got)
	}

	// Repository-level default_registry override and conflict refusal.
	repo1 := t.TempDir()
	repo2 := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repo1, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repo2, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo1, ".wb", "worktrees.yaml"), []byte("version: 1\numbrella:\n  default_registry: synchestra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveUmbrellaDefaultRegistry("", repo1); err != nil || got != "synchestra" {
		t.Fatalf("ResolveUmbrellaDefaultRegistry(repo1) = (%q, %v), want synchestra", got, err)
	}
	if err := os.WriteFile(filepath.Join(repo2, ".wb", "worktrees.yaml"), []byte("version: 1\numbrella:\n  default_registry: github\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveUmbrellaDefaultRegistry(repo1, repo2); err == nil || !strings.Contains(err.Error(), "disagree on umbrella.default_registry") {
		t.Fatalf("conflicting repo default_registry error = %v", err)
	}
	// Invalid YAML in repo config returns error.
	repoBad := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoBad, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoBad, ".wb", "worktrees.yaml"), []byte("version: 99\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveUmbrellaDefaultRegistry(repoBad); err == nil {
		t.Fatal("invalid repo worktrees.yaml accepted")
	}

	// Empty ready-to-close check returns false.
	if ready, _, err := UmbrellaReadyToClose(projectsRoot, umbrella.Ref{Registry: "github", ID: "o/none#99"}); err != nil || ready {
		t.Fatalf("UmbrellaReadyToClose empty = (%v, %v), want (false, nil)", ready, err)
	}
}

func TestUmbrellaCloseRefusesIncompleteOrForgedEvidence(t *testing.T) {
	t.Parallel()
	ref := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#845"}
	now := time.Now().UTC()
	sha1 := strings.Repeat("1", 40)
	sha2 := strings.Repeat("2", 40)

	seedLandedAndActive := func(t *testing.T) (projectsRoot, home string, landed, active workLogClaim) {
		t.Helper()
		projectsRoot = t.TempDir()
		home, err := wbhome.Root(projectsRoot)
		if err != nil {
			t.Fatal(err)
		}
		landed = makeUmbrellaTestClaim(t, projectsRoot, "task-1", "acme/one", "task-1", ref.String(), now)
		active = makeUmbrellaTestClaim(t, projectsRoot, "task-2", "acme/two", "task-2", ref.String(), now)
		term := workLogTerminalRecord{
			Claim: landed, FinalCommit: sha1, Disposition: "landed", SealedAt: now,
			Landed: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: sha1, Proof: worktreeclaims.LandedProofContained},
		}
		term.Lifecycle = "terminal"
		writeUmbrellaTestJSON(t, filepath.Join(home, "worklogs", "task-1", "runs", "run-1", "terminals", landed.ClaimID+".json"), term)
		return projectsRoot, home, landed, active
	}

	t.Run("unreadable active member claims directory refuses close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, _, _ := seedLandedAndActive(t)
		activeClaimsDir := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "claims")
		if err := os.Chmod(activeClaimsDir, 0o000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(activeClaimsDir, 0o700) })
		_, _, err := UmbrellaReadyToClose(projectsRoot, ref)
		if err == nil || !strings.Contains(err.Error(), activeClaimsDir) {
			t.Fatalf("UmbrellaReadyToClose with unreadable claims dir = %v, want error naming %s", err, activeClaimsDir)
		}
	})

	t.Run("malformed active claim refuses close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, _, active := seedLandedAndActive(t)
		claimPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "claims", active.ClaimID+".json")
		if err := os.WriteFile(claimPath, []byte("{corrupt"), 0o600); err != nil {
			t.Fatal(err)
		}
		_, _, err := UmbrellaReadyToClose(projectsRoot, ref)
		if err == nil || !strings.Contains(err.Error(), claimPath) {
			t.Fatalf("UmbrellaReadyToClose with corrupt claim = %v, want error naming %s", err, claimPath)
		}
	})

	t.Run("forged terminal copied from another landed claim refuses close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, landed, active := seedLandedAndActive(t)
		forgedTerm := workLogTerminalRecord{
			Claim: landed, FinalCommit: sha1, Disposition: "landed", SealedAt: now,
			Landed: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: sha1, Proof: worktreeclaims.LandedProofContained},
		}
		forgedTerm.Lifecycle = "terminal"
		forgedPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "terminals", active.ClaimID+".json")
		writeUmbrellaTestJSON(t, forgedPath, forgedTerm)
		_, _, err := UmbrellaReadyToClose(projectsRoot, ref)
		if err == nil || !strings.Contains(err.Error(), forgedPath) {
			t.Fatalf("UmbrellaReadyToClose with forged terminal = %v, want error naming %s", err, forgedPath)
		}
	})

	t.Run("terminal with invalid landed evidence refuses close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, _, active := seedLandedAndActive(t)
		badTerm := workLogTerminalRecord{
			Claim: active, FinalCommit: sha2, Disposition: "landed", SealedAt: now,
			Landed: &worktreeclaims.LandedEvidence{Target: "main", LandedSHA: "not-a-sha", Proof: worktreeclaims.LandedProofContained},
		}
		badTerm.Lifecycle = "terminal"
		termPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "terminals", active.ClaimID+".json")
		writeUmbrellaTestJSON(t, termPath, badTerm)
		_, _, err := UmbrellaReadyToClose(projectsRoot, ref)
		if err == nil || !strings.Contains(err.Error(), termPath) {
			t.Fatalf("UmbrellaReadyToClose with invalid terminal landed = %v, want error naming %s", err, termPath)
		}
	})

	t.Run("forged PR binding with mismatched repository refuses close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, _, active := seedLandedAndActive(t)
		prPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "claims", active.ClaimID+worktreeclaims.PullRequestBindingSuffix)
		writeUmbrellaTestJSON(t, prPath, worktreeclaims.ClaimPullRequestBinding{
			Repository: "acme/one", PullRequest: 99, URL: "https://github.com/acme/one/pull/99",
			LandedSHA: sha1, RecordedAt: now,
		})
		_, _, err := UmbrellaReadyToClose(projectsRoot, ref)
		if err == nil || !strings.Contains(err.Error(), prPath) {
			t.Fatalf("UmbrellaReadyToClose with forged PR binding = %v, want error naming %s", err, prPath)
		}
	})

	t.Run("corrupt umbrella sidecar and symlink sidecar refuse close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, _, active := seedLandedAndActive(t)
		sidecarPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "claims", active.ClaimID+worktreeclaims.UmbrellaBindingSuffix)
		writeUmbrellaTestJSON(t, sidecarPath, worktreeclaims.ClaimUmbrellaBinding{Umbrella: "invalid-no-colon"})
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), sidecarPath) {
			t.Fatalf("UmbrellaReadyToClose with corrupt sidecar = %v, want error naming %s", err, sidecarPath)
		}
		if got := effectiveClaimUmbrella(home, active); got != "" {
			t.Fatalf("effectiveClaimUmbrella trusted corrupt sidecar: %q", got)
		}
		// Symlink sidecar is refused by O_NOFOLLOW.
		outside := filepath.Join(t.TempDir(), "outside.json")
		writeUmbrellaTestJSON(t, outside, worktreeclaims.ClaimUmbrellaBinding{Umbrella: ref.String(), RecordedAt: now})
		_ = os.Remove(sidecarPath)
		if err := os.Symlink(outside, sidecarPath); err != nil {
			t.Fatal(err)
		}
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), sidecarPath) {
			t.Fatalf("UmbrellaReadyToClose with symlink sidecar = %v, want error naming %s", err, sidecarPath)
		}
	})

	t.Run("forged landing sidecar copied from another claim refuses close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, landed, active := seedLandedAndActive(t)
		landingPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "claims", active.ClaimID+worktreeclaims.LandingBindingSuffix)
		writeUmbrellaTestJSON(t, landingPath, worktreeclaims.ClaimLandingBinding{
			ClaimID: landed.ClaimID, Task: landed.Task, Repository: landed.Repository,
			Target: "main", LandedSHA: sha1, RecordedAt: now,
		})
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), landingPath) {
			t.Fatalf("UmbrellaReadyToClose with forged landing sidecar = %v, want error naming %s", err, landingPath)
		}
	})

	t.Run("forged cleanup record and corrupt retirement receipt refuse close", func(t *testing.T) {
		t.Parallel()
		projectsRoot, home, landed, active := seedLandedAndActive(t)
		cleanPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "cleanups", active.ClaimID+".json")
		writeUmbrellaTestJSON(t, cleanPath, workLogCleanupRecord{
			Version: 1, ClaimID: landed.ClaimID, FinalCommit: sha1, CleanedAt: now,
		})
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), cleanPath) {
			t.Fatalf("UmbrellaReadyToClose with forged cleanup = %v, want error naming %s", err, cleanPath)
		}
		_ = os.Remove(cleanPath)

		// Corrupt cleanup JSON and corrupt terminal JSON.
		if err := os.WriteFile(cleanPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), cleanPath) {
			t.Fatalf("UmbrellaReadyToClose with corrupt cleanup JSON = %v", err)
		}
		_ = os.Remove(cleanPath)

		termPath := filepath.Join(home, "worklogs", "task-2", "runs", "run-1", "terminals", active.ClaimID+".json")
		if err := os.MkdirAll(filepath.Dir(termPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(termPath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), termPath) {
			t.Fatalf("UmbrellaReadyToClose with corrupt terminal JSON = %v", err)
		}
		_ = os.Remove(termPath)

		// Corrupt retirement receipt refuses close.
		retirePath := worktreeretire.ReportPath(home, worktreeretire.Transaction{Task: active.Task, Repository: active.Repository})
		if err := os.MkdirAll(filepath.Dir(retirePath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(retirePath, []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), retirePath) {
			t.Fatalf("UmbrellaReadyToClose with corrupt retire report = %v, want error naming %s", err, retirePath)
		}
		// Mismatched task/repo in valid retirement receipt refuses close.
		retiredRef := worktreebranches.RetiredBranchDestination(now, active.Branch, sha2)
		wrongTx := worktreeretire.Transaction{
			Version: 1, Task: "other-task", Repository: active.Repository, Branch: active.Branch,
			ArchiveRepository: "acme/.retired", RetiredRef: retiredRef, SourceSHA: sha2,
			ClaimID: active.ClaimID, Phase: "complete",
		}
		wrongTx.ArchiveRef = worktreeretire.ArchiveRef(wrongTx)
		writeUmbrellaTestJSON(t, retirePath, wrongTx)
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), retirePath) {
			t.Fatalf("UmbrellaReadyToClose with mismatched retire report = %v, want error naming %s", err, retirePath)
		}
	})

	t.Run("split umbrella attachments within one task refuse close", func(t *testing.T) {
		t.Parallel()
		projectsRoot := t.TempDir()
		makeUmbrellaTestClaim(t, projectsRoot, "split-task", "acme/one", "split-task", ref.String(), now)
		makeUmbrellaTestClaim(t, projectsRoot, "split-task", "acme/two", "split-task", "github:sneat-co/backstage#999", now)
		if _, _, err := UmbrellaReadyToClose(projectsRoot, ref); err == nil || !strings.Contains(err.Error(), "inconsistent umbrella attachments") {
			t.Fatalf("UmbrellaReadyToClose with split task = %v, want inconsistent umbrella attachments error", err)
		}
	})

	t.Run("unsafe segments and unreadable subdirectories refuse close", func(t *testing.T) {
		t.Parallel()
		for _, tc := range []struct {
			name  string
			setup func(t *testing.T, home string) string
		}{
			{
				name: "unsafe effort name",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", ".bad-effort")
					if err := os.MkdirAll(p, 0o700); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "non-dir effort entry",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "not-a-dir")
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "non-dir runs entry",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs")
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "unsafe run name",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", ".bad-run")
					if err := os.MkdirAll(p, 0o700); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "non-dir run entry",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", "not-a-dir")
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "non-dir terminals entry",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "terminals")
					if err := os.MkdirAll(filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims"), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "non-dir cleanups entry",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "cleanups")
					if err := os.MkdirAll(filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims"), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("x"), 0o600); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "unsafe claim filename",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims", "badname.json")
					if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(p, []byte("{}"), 0o600); err != nil {
						t.Fatal(err)
					}
					return p
				},
			},
			{
				name: "mismatched claim identity",
				setup: func(t *testing.T, home string) string {
					cid := strings.Repeat("a", 64)
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims", cid+".json")
					writeUmbrellaTestJSON(t, p, workLogClaim{
						Version: 2, EffortID: "other", RunID: "run-1", ClaimID: cid, Task: "t1", Repository: "o/r",
					})
					return p
				},
			},
			{
				name: "orphaned PR binding sidecar",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims", strings.Repeat("a", 64)+worktreeclaims.PullRequestBindingSuffix)
					writeUmbrellaTestJSON(t, p, worktreeclaims.ClaimPullRequestBinding{Repository: "o/r", PullRequest: 1, URL: "https://github.com/o/r/pull/1", RecordedAt: now})
					return p
				},
			},
			{
				name: "orphaned umbrella binding sidecar",
				setup: func(t *testing.T, home string) string {
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims", strings.Repeat("a", 64)+worktreeclaims.UmbrellaBindingSuffix)
					writeUmbrellaTestJSON(t, p, worktreeclaims.ClaimUmbrellaBinding{Umbrella: ref.String(), RecordedAt: now})
					return p
				},
			},
			{
				name: "orphaned landing binding sidecar",
				setup: func(t *testing.T, home string) string {
					cid := strings.Repeat("a", 64)
					p := filepath.Join(home, "worklogs", "t1", "runs", "run-1", "claims", cid+worktreeclaims.LandingBindingSuffix)
					writeUmbrellaTestJSON(t, p, worktreeclaims.ClaimLandingBinding{ClaimID: cid, Task: "t1", Repository: "o/r", Target: "main", LandedSHA: sha1, RecordedAt: now})
					return p
				},
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				t.Parallel()
				root := t.TempDir()
				home, err := wbhome.Root(root)
				if err != nil {
					t.Fatal(err)
				}
				badPath := tc.setup(t, home)
				if _, err := collectClaimsInHome(home); err == nil || !strings.Contains(err.Error(), badPath) {
					t.Fatalf("collectClaimsInHome(%s) = %v, want error naming %s", tc.name, err, badPath)
				}
			})
		}

		// Directory listing errors propagate cleanly at worklogs, runs, and claims levels.
		dirRoot := t.TempDir()
		dirHome, err := wbhome.Root(dirRoot)
		if err != nil {
			t.Fatal(err)
		}
		makeUmbrellaTestClaim(t, dirRoot, "list-task", "o/r", "list-task", ref.String(), now)
		for failAt := 1; failAt <= 3; failAt++ {
			calls := 0
			_, listErr := collectClaimsInHomeWithReadNames(dirHome, func(dir *os.File) ([]string, error) {
				calls++
				if calls == failAt {
					return nil, os.ErrPermission
				}
				return dir.Readdirnames(-1)
			})
			if listErr == nil {
				t.Fatalf("collectClaimsInHomeWithReadNames failAt=%d succeeded", failAt)
			}
		}

		// effectiveUmbrella handles missing/non-directory run and claims paths cleanly.
		missingRunClaim := workLogClaim{
			EffortID: "no-such-effort", RunID: "run-1", ClaimID: strings.Repeat("a", 64), Umbrella: ref.String(),
		}
		if got, err := effectiveUmbrella(dirHome, missingRunClaim); err != nil || got != ref.String() {
			t.Fatalf("effectiveUmbrella missing run = (%q, %v)", got, err)
		}
		fileRunHome := t.TempDir()
		fileRunPath := filepath.Join(fileRunHome, "worklogs", "eff", "runs", "run-1")
		if err := os.MkdirAll(filepath.Dir(fileRunPath), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fileRunPath, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := effectiveUmbrella(fileRunHome, workLogClaim{EffortID: "eff", RunID: "run-1", ClaimID: strings.Repeat("a", 64)}); err == nil {
			t.Fatal("effectiveUmbrella accepted file as run dir")
		}
		noClaimsHome := t.TempDir()
		noClaimsRun := filepath.Join(noClaimsHome, "worklogs", "eff", "runs", "run-1")
		if err := os.MkdirAll(noClaimsRun, 0o700); err != nil {
			t.Fatal(err)
		}
		if got, err := effectiveUmbrella(noClaimsHome, workLogClaim{EffortID: "eff", RunID: "run-1", ClaimID: strings.Repeat("a", 64), Umbrella: ref.String()}); err != nil || got != ref.String() {
			t.Fatalf("effectiveUmbrella missing claims dir = (%q, %v)", got, err)
		}
		if err := os.WriteFile(filepath.Join(noClaimsRun, "claims"), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := effectiveUmbrella(noClaimsHome, workLogClaim{EffortID: "eff", RunID: "run-1", ClaimID: strings.Repeat("a", 64)}); err == nil {
			t.Fatal("effectiveUmbrella accepted file as claims dir")
		}
	})
}

func TestRecordPullRequestLandedSHANeverOverridesConflictingBindingOrForkPR(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#845"}
	cBound := makeUmbrellaTestClaim(t, projectsRoot, "task-bound", "acme/app", "feature", ref.String(), now)
	claimsDir := filepath.Join(home, "worklogs", "task-bound", "runs", "run-1", "claims")
	writeUmbrellaTestJSON(t, filepath.Join(claimsDir, cBound.ClaimID+worktreeclaims.PullRequestBindingSuffix), worktreeclaims.ClaimPullRequestBinding{
		Repository: "acme/app", PullRequest: 20, URL: "https://github.com/acme/app/pull/20", RecordedAt: now,
	})

	// Landing fork PR #21 on the same branch name "feature" MUST NOT overwrite PR #20's binding or mark it landed.
	forkLandedSHA := strings.Repeat("9", 40)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 21, "fork-user/app", "feature", forkLandedSHA)
	if _, _, ok := UmbrellaForPullRequest(projectsRoot, "acme/app", 21, "fork-user/app", "feature", nil); ok {
		t.Fatal("fork PR #21 matched local claim bound to PR #20")
	}
	ready, members, err := UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil || ready || len(members) != 1 || members[0].PullRequest != 20 || members[0].LandedSHA != "" {
		t.Fatalf("fork PR #21 corrupted PR #20 claim: ready=%v members=%+v err=%v", ready, members, err)
	}

	// Even a same-repo PR #22 on branch "feature" MUST NOT overwrite PR #20's explicit binding.
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 22, "acme/app", "feature", strings.Repeat("8", 40))
	ready, members, err = UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil || ready || members[0].PullRequest != 20 || members[0].LandedSHA != "" {
		t.Fatalf("same-repo PR #22 overwrote explicit PR #20 binding: ready=%v members=%+v err=%v", ready, members, err)
	}

	// Landing the bound PR #20 itself marks #20 landed.
	boundLandedSHA := strings.Repeat("7", 40)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 20, "acme/app", "feature", boundLandedSHA)
	ready, members, err = UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil || !ready || members[0].LandedSHA != boundLandedSHA {
		t.Fatalf("bound PR #20 landing = (ready=%v, members=%+v, err=%v)", ready, members, err)
	}

	// Unbound claim on "other-feature": fork PR #23 must NOT match; same-repo PR #24 DOES match.
	ref2 := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#846"}
	makeUmbrellaTestClaim(t, projectsRoot, "task-unbound", "acme/app", "other-feature", ref2.String(), now)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 23, "fork-user/app", "other-feature", strings.Repeat("6", 40))
	if ready, _, err := UmbrellaReadyToClose(projectsRoot, ref2); err != nil || ready {
		t.Fatalf("fork PR #23 marked unbound claim landed: ready=%v err=%v", ready, err)
	}
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 24, "acme/app", "other-feature", strings.Repeat("5", 40))
	if ready, members, err := UmbrellaReadyToClose(projectsRoot, ref2); err != nil || !ready || members[0].PullRequest != 24 {
		t.Fatalf("same-repo PR #24 on unbound claim = (ready=%v, members=%+v, err=%v)", ready, members, err)
	}

	// Superseded handoff predecessor on unbound branch is skipped by RecordPullRequestLandedSHA.
	supClaim := makeUmbrellaTestClaim(t, projectsRoot, "task-sup", "acme/app", "sup-branch", "", now)
	supTerm := workLogTerminalRecord{Claim: supClaim, FinalCommit: strings.Repeat("4", 40), Disposition: "handoff", SuccessorClaimID: strings.Repeat("c", 64), SealedAt: now}
	supTerm.Lifecycle = "terminal"
	writeUmbrellaTestJSON(t, filepath.Join(home, "worklogs", "task-sup", "runs", "run-1", "terminals", supClaim.ClaimID+".json"), supTerm)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 25, "acme/app", "sup-branch", strings.Repeat("4", 40))
	if _, err := os.Stat(filepath.Join(home, "worklogs", "task-sup", "runs", "run-1", "claims", supClaim.ClaimID+worktreeclaims.PullRequestBindingSuffix)); !os.IsNotExist(err) {
		t.Fatalf("superseded predecessor unexpectedly received PR binding: err=%v", err)
	}
}

func TestUmbrellaSidecarInheritedOnClaimExtensionAndHandoff(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	ref := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#845"}

	// 1. Extension sequence: create task without umbrella, attach via SetTaskUmbrella (sidecar), extend into repo2.
	c1 := makeUmbrellaTestClaim(t, projectsRoot, "ext-task", "acme/one", "ext-task", "", now)
	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, "ext-task", ref); err != nil {
		t.Fatalf("SetTaskUmbrella: %v", err)
	}
	extendedOpts, err := workLogOptionsForClaimExtension(home, WorkLogOptions{}, c1)
	if err != nil {
		t.Fatalf("workLogOptionsForClaimExtension: %v", err)
	}
	if extendedOpts.Umbrella != ref.String() {
		t.Fatalf("extended claim Umbrella = %q, want %q", extendedOpts.Umbrella, ref.String())
	}
	// Record repo2 claim with inherited Umbrella; umbrella must now see BOTH members and block close while repo2 is active.
	makeUmbrellaTestClaim(t, projectsRoot, "ext-task", "acme/two", "ext-task", extendedOpts.Umbrella, now.Add(time.Second))
	members, err := UmbrellaMembers(projectsRoot, ref)
	if err != nil || len(members) != 2 {
		t.Fatalf("extended task UmbrellaMembers = (%+v, %v), want 2 members", members, err)
	}

	// Corrupt sidecar causes extension to fail cleanly.
	badSidecar := filepath.Join(home, "worklogs", "ext-task", "runs", "run-1", "claims", c1.ClaimID+worktreeclaims.UmbrellaBindingSuffix)
	writeUmbrellaTestJSON(t, badSidecar, worktreeclaims.ClaimUmbrellaBinding{Umbrella: "corrupt-no-colon"})
	if _, err := workLogOptionsForClaimExtension(home, WorkLogOptions{}, c1); err == nil {
		t.Fatal("workLogOptionsForClaimExtension accepted corrupt umbrella sidecar")
	}

	// 2. Handoff sequence: create task without umbrella, attach via SetTaskUmbrella, hand off via transferWorkLogClaim.
	handoffRoot := t.TempDir()
	handoffHome, err := wbhome.Root(handoffRoot)
	if err != nil {
		t.Fatal(err)
	}
	hClaim := makeUmbrellaTestClaim(t, handoffRoot, "ho-task", "acme/app", "ho-task", "", now)
	if _, err := SetTaskUmbrella(context.Background(), handoffRoot, "ho-task", ref); err != nil {
		t.Fatalf("SetTaskUmbrella handoff: %v", err)
	}
	proj := workLogProjection{Version: 1, EffortID: hClaim.EffortID, RunID: hClaim.RunID, ClaimID: hClaim.ClaimID, Lifecycle: "active"}
	writeUmbrellaTestJSON(t, filepath.Join(hClaim.Worktree, workLogProjectionDirectory, workLogProjectionName), proj)
	ports := defaultTransferClaimPorts()
	ports.corroborate = func(string, string, workLogProjection, workLogClaim) error { return nil }
	ports.writeProjection = func(wt string, p workLogProjection) error {
		writeUmbrellaTestJSON(t, filepath.Join(wt, workLogProjectionDirectory, workLogProjectionName), p)
		return nil
	}
	if err := ports.transferWorkLogClaim(handoffHome, hClaim.Worktree, strings.Repeat("c", 40), "handoff", "next-agent", ClaimExecutionIdentity{Model: "unknown"}); err != nil {
		t.Fatalf("transferWorkLogClaim: %v", err)
	}
	// Successor claim inherits the effective umbrella so trailers and close checks see it and predecessor no longer blocks!
	successorID := declaredSuccessorWorkLogClaimID(hClaim.ClaimID, "next-agent", "handoff", ClaimExecutionIdentity{Model: "unknown"})
	var succClaim workLogClaim
	rawSucc, err := os.ReadFile(filepath.Join(handoffHome, "worklogs", "ho-task", "runs", "run-1", "claims", successorID+".json"))
	if err != nil {
		t.Fatalf("read successor claim: %v", err)
	}
	if err := json.Unmarshal(rawSucc, &succClaim); err != nil {
		t.Fatalf("decode successor claim: %v", err)
	}
	if succClaim.Umbrella != ref.String() {
		t.Fatalf("successor claim Umbrella = %q, want %q", succClaim.Umbrella, ref.String())
	}
	if err := RecordClaimLanding(handoffRoot, "acme/app", "ho-task", hClaim.Worktree, "ho-task", "main", strings.Repeat("d", 40), 0, ""); err != nil {
		t.Fatalf("RecordClaimLanding on successor: %v", err)
	}
	ready, hoMembers, err := UmbrellaReadyToClose(handoffRoot, ref)
	if err != nil || !ready || len(hoMembers) != 1 || hoMembers[0].ClaimID != successorID {
		t.Fatalf("handoff close eligibility = (ready=%v, members=%+v, err=%v)", ready, hoMembers, err)
	}

	// 3. Corrupt sidecar is refused during handoff.
	recRoot := t.TempDir()
	recHome, err := wbhome.Root(recRoot)
	if err != nil {
		t.Fatal(err)
	}
	rClaim := makeUmbrellaTestClaim(t, recRoot, "rec-task", "acme/app", "rec-task", "", now)
	if _, err := SetTaskUmbrella(context.Background(), recRoot, "rec-task", ref); err != nil {
		t.Fatalf("SetTaskUmbrella recycle: %v", err)
	}
	recSHA := strings.Repeat("e", 40)
	recSidecar := filepath.Join(recHome, "worklogs", "rec-task", "runs", "run-1", "claims", rClaim.ClaimID+worktreeclaims.UmbrellaBindingSuffix)
	writeUmbrellaTestJSON(t, recSidecar, worktreeclaims.ClaimUmbrellaBinding{Umbrella: "invalid"})
	recProj := workLogProjection{Version: 1, EffortID: rClaim.EffortID, RunID: rClaim.RunID, ClaimID: rClaim.ClaimID, Lifecycle: "active"}
	writeUmbrellaTestJSON(t, filepath.Join(rClaim.Worktree, workLogProjectionDirectory, workLogProjectionName), recProj)
	if err := ports.transferWorkLogClaim(recHome, rClaim.Worktree, recSHA, "handoff", "next-agent", ClaimExecutionIdentity{Model: "unknown"}); err == nil {
		t.Fatal("transferWorkLogClaim accepted corrupt umbrella sidecar")
	}
}

func TestRecordClaimLandingMakesFreshWorktreeClaimCloseEligible(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	ref := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#845"}
	now := time.Now().UTC()
	c := makeUmbrellaTestClaim(t, projectsRoot, "land-task", "acme/app", "land-task", ref.String(), now)

	if err := RecordClaimLanding(projectsRoot, "acme/app", "land-task", c.Worktree, "land-task", "main", "not-a-sha", 0, ""); err == nil {
		t.Fatal("RecordClaimLanding accepted invalid SHA")
	}
	if err := RecordClaimLanding("bad\x00root", "acme/app", "land-task", c.Worktree, "land-task", "main", strings.Repeat("1", 40), 0, ""); err == nil {
		t.Fatal("RecordClaimLanding accepted invalid projectsRoot")
	}

	landedSHA := strings.Repeat("1", 40)
	// Add sibling claim differing by task, and test non-matching worktree & branch filters.
	makeUmbrellaTestClaim(t, projectsRoot, "other-task", "acme/app", "land-task", "", now)
	if err := RecordClaimLanding(projectsRoot, "acme/app", "land-task", "/other/worktree", "land-task", "main", landedSHA, 0, ""); err != nil {
		t.Fatalf("RecordClaimLanding non-matching worktree: %v", err)
	}
	if err := RecordClaimLanding(projectsRoot, "acme/app", "land-task", c.Worktree, "other-branch", "main", landedSHA, 0, ""); err != nil {
		t.Fatalf("RecordClaimLanding non-matching branch: %v", err)
	}
	if err := RecordClaimLanding(projectsRoot, "acme/app", "land-task", c.Worktree, "land-task", "main", landedSHA, 77, "https://github.com/acme/app/pull/999"); err == nil {
		t.Fatal("RecordClaimLanding accepted mismatched PullRequestURL")
	}
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := RecordClaimLanding(projectsRoot, "acme/app", "land-task", c.Worktree, "land-task", "", landedSHA, 77, "https://github.com/acme/app/pull/77"); err != nil {
		t.Fatalf("RecordClaimLanding: %v", err)
	}
	ready, members, err := UmbrellaReadyToClose(projectsRoot, ref)
	if err != nil || !ready || len(members) != 1 || members[0].State != "landed" || members[0].LandedSHA != landedSHA || members[0].PullRequest != 77 {
		t.Fatalf("UmbrellaReadyToClose after RecordClaimLanding = (ready=%v, members=%+v, err=%v)", ready, members, err)
	}
	// Unwritable claims dir returns write error.
	claimsDir := filepath.Join(home, "worklogs", "land-task", "runs", "run-1", "claims")
	if err := os.Chmod(claimsDir, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(claimsDir, 0o700) })
	if err := RecordClaimLanding(projectsRoot, "acme/app", "land-task", c.Worktree, "land-task", "main", landedSHA, 0, ""); err == nil {
		t.Fatal("RecordClaimLanding succeeded on read-only claims dir")
	}
}

func TestSetTaskUmbrellaLocksTaskAndHandlesMidUpdateFailure(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	now := time.Now().UTC()
	makeUmbrellaTestClaim(t, projectsRoot, "lock-task", "acme/one", "lock-task", "", now)
	makeUmbrellaTestClaim(t, projectsRoot, "lock-task", "acme/two", "lock-task", "", now.Add(time.Second))

	refA := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#100"}
	refB := umbrella.Ref{Registry: "github", ID: "sneat-co/backstage#200"}

	// 1. Concurrent setter is serialized/refused by the per-task lock, never splitting the task.
	locked := make(chan struct{})
	release := make(chan struct{})
	done := make(chan error, 1)
	go func() {
		_, err := setTaskUmbrellaWithHooks(context.Background(), projectsRoot, "lock-task", refA, func() {
			close(locked)
			<-release
		}, nil)
		done <- err
	}()
	<-locked
	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, "lock-task", refB); err == nil {
		t.Fatal("concurrent SetTaskUmbrella succeeded while task lock was held")
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("first SetTaskUmbrella failed: %v", err)
	}
	membersA, err := UmbrellaMembers(projectsRoot, refA)
	if err != nil || len(membersA) != 2 {
		t.Fatalf("after locked update, refA members = (%+v, %v), want 2", membersA, err)
	}

	// 2. Mid-update I/O failure on claim 2 rolls back claim 1 to refA cleanly.
	writes := 0
	_, err = setTaskUmbrellaWithHooks(context.Background(), projectsRoot, "lock-task", refB, nil, func(dir, name string, data []byte) error {
		writes++
		if writes == 2 {
			return os.ErrPermission
		}
		return writeBytesAtomic(dir, name, data, 0o600)
	})
	if err == nil || strings.Contains(err.Error(), "partially updated") {
		t.Fatalf("mid-update failure with clean rollback error = %v", err)
	}
	membersA, err = UmbrellaMembers(projectsRoot, refA)
	if err != nil || len(membersA) != 2 {
		t.Fatalf("after rollback, refA members = (%+v, %v), want all 2 restored to refA", membersA, err)
	}

	// 3. Mid-update failure when rollback ALSO fails refuses and names the partially updated claim.
	writes = 0
	_, err = setTaskUmbrellaWithHooks(context.Background(), projectsRoot, "lock-task", refB, nil, func(dir, name string, data []byte) error {
		writes++
		if writes >= 2 {
			return os.ErrPermission
		}
		return writeBytesAtomic(dir, name, data, 0o600)
	})
	if err == nil || !strings.Contains(err.Error(), "partially updated claims") {
		t.Fatalf("failed rollback error = %v, want error naming partially updated claims", err)
	}

	// 4. Mid-update failure when no prior sidecar existed removes claim 1's new sidecar cleanly.
	freshRoot := t.TempDir()
	makeUmbrellaTestClaim(t, freshRoot, "fresh-task", "acme/one", "fresh-task", "", now)
	makeUmbrellaTestClaim(t, freshRoot, "fresh-task", "acme/two", "fresh-task", "", now.Add(time.Second))
	writes = 0
	_, err = setTaskUmbrellaWithHooks(context.Background(), freshRoot, "fresh-task", refA, nil, func(dir, name string, data []byte) error {
		writes++
		if writes == 2 {
			return os.ErrPermission
		}
		return writeBytesAtomic(dir, name, data, 0o600)
	})
	if err == nil {
		t.Fatal("expected mid-update failure on fresh task")
	}
	if members, mErr := UmbrellaMembers(freshRoot, refA); mErr != nil || len(members) != 0 {
		t.Fatalf("fresh rollback left sidecars behind: members=%+v err=%v", members, mErr)
	}

	// 5. Invalid task name, multi-run sorting, and unwritable lock root are handled cleanly.
	if _, err := SetTaskUmbrella(context.Background(), projectsRoot, "../bad-task", refA); err == nil {
		t.Fatal("unsafe task name accepted by SetTaskUmbrella")
	}
	multiRunRoot := t.TempDir()
	multiHome, err := wbhome.Root(multiRunRoot)
	if err != nil {
		t.Fatal(err)
	}
	cRun1 := makeUmbrellaTestClaim(t, multiRunRoot, "multi-run", "acme/one", "multi-run", "", now)
	cRun2 := cRun1
	cRun2.RunID = "run-2"
	cRun2.Repository = "acme/two"
	cRun2.Worktree = filepath.Join(multiRunRoot, ".worktrees", "multi-run", "acme/two")
	cRun2.ClaimID = worktreeclaims.WorkLogClaimID("multi-run", worktreeclaims.CreationResult{
		Repository: cRun2.Repository, WorktreeDir: cRun2.Worktree, Branch: cRun2.Branch, Base: cRun2.Base, BaseSHA: cRun2.BaseSHA,
	})
	writeUmbrellaTestJSON(t, filepath.Join(multiHome, "worklogs", "multi-run", "runs", "run-2", "claims", cRun2.ClaimID+".json"), cRun2)
	if res, err := SetTaskUmbrella(context.Background(), multiRunRoot, "multi-run", refA); err != nil || res.Claims != 2 {
		t.Fatalf("SetTaskUmbrella multi-run = (%+v, %v)", res, err)
	}
	blockedLockRoot := t.TempDir()
	blockedHome, err := wbhome.Root(blockedLockRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(blockedHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blockedHome, "worktrees"), []byte("not-a-dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := SetTaskUmbrella(context.Background(), blockedLockRoot, "t", refA); err == nil {
		t.Fatal("SetTaskUmbrella succeeded when lock root could not be prepared")
	}
}
