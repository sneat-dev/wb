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
	RecordPullRequestLandedSHA(projectsRoot, "sneat-co/backstage", 846, "cross-repo", strings.Repeat("1", 40))
	if prRef, taskName, ok := UmbrellaForPullRequest(projectsRoot, "sneat-co/backstage", 846, "cross-repo", nil); !ok || prRef != ref || taskName != "cross-repo" {
		t.Fatalf("UmbrellaForPullRequest by PR = (%+v, %q, %v)", prRef, taskName, ok)
	}
	if prRef, _, ok := UmbrellaForPullRequest(projectsRoot, "sneat-co/backstage", 999, "cross-repo", []string{"cross-repo"}); !ok || prRef != ref {
		t.Fatalf("UmbrellaForPullRequest by cleaned task = (%+v, %v)", prRef, ok)
	}
	if prRef, _, ok := UmbrellaForPullRequest(projectsRoot, "trakhimenok/workbench", 999, "cross-repo", nil); !ok || prRef != ref {
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
