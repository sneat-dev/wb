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

func TestUmbrellaWorktreeResolutionAndEdgeCases(t *testing.T) {
	t.Parallel()
	// 1. User-level default_registry + error propagation + ResolveUmbrellaDefaultRegistryForSlugs.
	syn := "synchestra"
	if got, err := resolveUmbrellaDefaultRegistry(func() (branchConfigFile, bool, string, error) {
		var cfg branchConfigFile
		cfg.Umbrella.DefaultRegistry = &syn
		return cfg, true, "user.yaml", nil
	}); err != nil || got != "synchestra" {
		t.Fatalf("user config default_registry = (%q, %v)", got, err)
	}
	if _, err := resolveUmbrellaDefaultRegistry(func() (branchConfigFile, bool, string, error) {
		return branchConfigFile{}, false, "", os.ErrPermission
	}); err == nil {
		t.Fatal("user config error ignored")
	}
	repoNoUmbrella := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoNoUmbrella, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoNoUmbrella, ".wb", "worktrees.yaml"), []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveUmbrellaDefaultRegistry(repoNoUmbrella); err != nil || got != "github" {
		t.Fatalf("repo without umbrella key = (%q, %v)", got, err)
	}
	slugsRoot := t.TempDir()
	slugRepo := filepath.Join(slugsRoot, "acme", "app")
	if err := os.MkdirAll(filepath.Join(slugRepo, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(slugRepo, ".wb", "worktrees.yaml"), []byte("version: 1\numbrella:\n  default_registry: synchestra\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got, err := ResolveUmbrellaDefaultRegistryForSlugs(slugsRoot, []string{"acme/app"}); err != nil || got != "synchestra" {
		t.Fatalf("ResolveUmbrellaDefaultRegistryForSlugs = (%q, %v)", got, err)
	}

	// 2. Real git worktree resolution for SetTaskUmbrella and UmbrellaForWorktree (active, terminal, manifest).
	projectsRoot := t.TempDir()
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	wtActive := filepath.Join(projectsRoot, "wt-active")
	if err := os.MkdirAll(wtActive, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, wtActive, "init", "-b", "feat-a")
	gitTest(t, wtActive, "config", "user.email", "t@example.com")
	gitTest(t, wtActive, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(wtActive, "a.txt"), []byte("a\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, wtActive, "add", "a.txt")
	gitTest(t, wtActive, "commit", "-m", "init")
	headSHA, err := git(context.Background(), wtActive, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	headSHA = strings.TrimSpace(headSHA)

	claimID := worktreeclaims.WorkLogClaimID("task-a", worktreeclaims.CreationResult{
		Repository: "acme/app", WorktreeDir: wtActive, Branch: "feat-a", Base: "main", BaseSHA: headSHA,
	})
	claim := workLogClaim{
		Version: 2, EffortID: "task-a", RunID: "run-1", ClaimID: claimID, Task: "task-a",
		Repository: "acme/app", Worktree: wtActive, Branch: "feat-a", Base: "main", BaseSHA: headSHA,
		Lifecycle: "active", RecordedAt: time.Now().UTC(), Umbrella: "github:acme/app#10",
	}
	claimsDir := filepath.Join(home, "worklogs", "task-a", "runs", "run-1", "claims")
	writeUmbrellaTestJSON(t, filepath.Join(claimsDir, claimID+".json"), claim)
	writeUmbrellaTestJSON(t, filepath.Join(wtActive, workLogProjectionDirectory, workLogProjectionName), workLogProjection{
		Version: 1, EffortID: "task-a", RunID: "run-1", ClaimID: claimID, Lifecycle: "active",
	})
	// Active claim path in resolveTaskForUmbrellaSet & UmbrellaForWorktree.
	if got := UmbrellaForWorktree(projectsRoot, wtActive); got != "github:acme/app#10" {
		t.Fatalf("UmbrellaForWorktree active = %q", got)
	}
	if task, err := resolveTaskForUmbrellaSet(context.Background(), home, wtActive); err != nil || task != "task-a" {
		t.Fatalf("resolveTaskForUmbrellaSet active = (%q, %v)", task, err)
	}

	// Terminal claim path in resolveTaskForUmbrellaSet & UmbrellaForWorktree.
	term := workLogTerminalRecord{Claim: claim, FinalCommit: headSHA, Disposition: "landed", SealedAt: time.Now().UTC()}
	term.Lifecycle = "terminal"
	writeUmbrellaTestJSON(t, filepath.Join(home, "worklogs", "task-a", "runs", "run-1", "terminals", claimID+".json"), term)
	writeUmbrellaTestJSON(t, filepath.Join(wtActive, workLogProjectionDirectory, workLogProjectionName), workLogProjection{
		Version: 1, EffortID: "task-a", RunID: "run-1", ClaimID: claimID, Lifecycle: "terminal",
	})
	if got := UmbrellaForWorktree(projectsRoot, wtActive); got != "github:acme/app#10" {
		t.Fatalf("UmbrellaForWorktree terminal = %q", got)
	}
	if task, err := resolveTaskForUmbrellaSet(context.Background(), home, wtActive); err != nil || task != "task-a" {
		t.Fatalf("resolveTaskForUmbrellaSet terminal = (%q, %v)", task, err)
	}

	// Manifest-only git worktree path in resolveTaskForUmbrellaSet.
	_ = os.RemoveAll(filepath.Join(wtActive, workLogProjectionDirectory))
	manifestYAML := "version: 1\neffort_id: task-manifest\neffort_kind: feature\nrepository: acme/app\nworktree: " + wtActive + "\nbranch: feat-a\nbase: main\nbase_sha: " + headSHA + "\ncreated_at: 2026-11-28T00:00:00Z\nprovenance: created\n"
	if err := os.MkdirAll(filepath.Join(wtActive, ".wb", "local"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(wtActive, ".wb", "local", "manifest.yaml"), []byte(manifestYAML), 0o600); err != nil {
		t.Fatal(err)
	}
	if task, err := resolveTaskForUmbrellaSet(context.Background(), home, wtActive); err != nil || task != "task-manifest" {
		t.Fatalf("resolveTaskForUmbrellaSet manifest = (%q, %v)", task, err)
	}
	// Default "" resolves "." (in a WB worktree returns its task; in a non-worktree returns error).
	if task, err := resolveTaskForUmbrellaSet(context.Background(), t.TempDir(), ""); err == nil && task == "" {
		t.Fatal("resolveTaskForUmbrellaSet empty target returned empty task with nil error")
	}

	// 3. RecordPullRequestLandedSHA edge cases: empty/non-positive, existing PR update, unmatched repo, bad projectsRoot.
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 0, "feat-a", headSHA)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 5, "feat-a", "")
	RecordPullRequestLandedSHA("", "acme/app", 5, "feat-a", headSHA)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 5, "feat-a", headSHA)
	// Update existing binding second time to hit `if rec.pr != nil`.
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 5, "feat-a", strings.Repeat("e", 40))

	// 4. UmbrellaForPullRequest & UmbrellaForTask edge cases (unmatched, invalid umbrella string, bad root).
	makeUmbrellaTestClaim(t, projectsRoot, "bad-umb", "acme/bad", "bad-br", "github:invalid", time.Now().UTC())
	makeUmbrellaTestClaim(t, projectsRoot, "no-umb", "acme/none", "none-br", "", time.Now().UTC())
	if _, _, ok := UmbrellaForPullRequest(projectsRoot, "acme/bad", 1, "bad-br", []string{"bad-umb"}); ok {
		t.Fatal("invalid umbrella matched PR")
	}
	if _, _, ok := UmbrellaForPullRequest(projectsRoot, "acme/none", 1, "none-br", []string{"no-umb"}); ok {
		t.Fatal("empty umbrella matched PR")
	}
	if _, _, ok := UmbrellaForPullRequest("", "acme/app", 1, "feat-a", nil); ok {
		t.Fatal("empty projectsRoot matched PR")
	}
	if got := UmbrellaForTask("", "task-a"); got != "" {
		t.Fatalf("empty projectsRoot UmbrellaForTask = %q", got)
	}
	if got := UmbrellaForTask(projectsRoot, "no-umb"); got != "" {
		t.Fatalf("no-umbrella task = %q", got)
	}

	// 5. buildMembersFromClaims sorting across multiple tasks.
	sorted := buildMembersFromClaims([]claimRecord{
		{claim: workLogClaim{Task: "z-task", Repository: "o/b", ClaimID: strings.Repeat("1", 64)}},
		{claim: workLogClaim{Task: "a-task", Repository: "o/a", ClaimID: strings.Repeat("2", 64)}},
	})
	if len(sorted) != 2 || sorted[0].Task != "a-task" || sorted[1].Task != "z-task" {
		t.Fatalf("sorted members = %+v", sorted)
	}

	// 6. collectClaimsInHome skips non-dirs, unsafe segments, missing runs/claims, bad claim files, and fails when worklogs is a file.
	makeUmbrellaTestClaim(t, projectsRoot, "extra", "acme/extra", "ex", "github:acme/app#10", time.Now().UTC())
	if err := os.WriteFile(filepath.Join(home, "worklogs", "not-a-dir"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "no-runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "extra", "runs", ".unsafe"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "extra", "runs", "no-claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	extraClaimsDir := filepath.Join(home, "worklogs", "extra", "runs", "run-1", "claims")
	if err := os.MkdirAll(filepath.Join(extraClaimsDir, "subdir.json"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(extraClaimsDir, "badname.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	badClaimID := strings.Repeat("f", 64)
	if err := os.WriteFile(filepath.Join(extraClaimsDir, badClaimID+".json"), []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	unreadableID := strings.Repeat("e", 64)
	unreadablePath := filepath.Join(extraClaimsDir, unreadableID+".json")
	if err := os.WriteFile(unreadablePath, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadablePath, 0o600) })
	if _, err := collectClaimsInHome(home); err != nil {
		t.Fatalf("collectClaimsInHome should skip unreadable/malformed entries: %v", err)
	}

	// Corrupt worklogs path (regular file instead of dir) propagates errors across all callers.
	badRoot := t.TempDir()
	badHome, err := wbhome.Root(badRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(badHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(badHome, "worklogs"), []byte("not a dir"), 0o600); err != nil {
		t.Fatal(err)
	}
	ref := umbrella.Ref{Registry: "github", ID: "o/r#1"}
	if _, err := SetTaskUmbrella(context.Background(), badRoot, "t", ref); err == nil {
		t.Fatal("SetTaskUmbrella ignored unreadable worklogs")
	}
	if _, err := UmbrellaMembers(badRoot, ref); err == nil {
		t.Fatal("UmbrellaMembers ignored unreadable worklogs")
	}
	if _, _, err := TaskUmbrellaMembers(badRoot, "t"); err == nil {
		t.Fatal("TaskUmbrellaMembers ignored unreadable worklogs")
	}
	if _, _, err := UmbrellaReadyToClose(badRoot, ref); err == nil {
		t.Fatal("UmbrellaReadyToClose ignored unreadable worklogs")
	}
	if _, err := SetTaskUmbrella(context.Background(), "", "t", ref); err == nil {
		t.Fatal("SetTaskUmbrella accepted empty projectsRoot")
	}
	// Passing badRoot (where worklogs is a file) exercises error returns in read helpers,
	// and a NUL path exercises wbhome.Root / wbhome.Resolve error branches.
	if got := UmbrellaForTask(badRoot, "t"); got != "" {
		t.Fatalf("UmbrellaForTask(badRoot) = %q", got)
	}
	if _, _, ok := UmbrellaForPullRequest(badRoot, "o/r", 1, "b", nil); ok {
		t.Fatal("UmbrellaForPullRequest(badRoot) succeeded")
	}
	RecordPullRequestLandedSHA(badRoot, "o/r", 1, "b", headSHA)
	if _, err := SetTaskUmbrella(context.Background(), "bad\x00root", "t", ref); err == nil {
		t.Fatal("SetTaskUmbrella accepted NUL projectsRoot")
	}
	if _, err := collectAllClaims("bad\x00root"); err == nil {
		t.Fatal("collectAllClaims accepted NUL projectsRoot")
	}

	// FormatWorktreeInfoText includes manifest umbrella when present.
	infoText := FormatWorktreeInfoText(WorkLogView{
		Worktree: wtActive,
		Manifest: &Manifest{EffortID: "task-a", EffortKind: "feature", Repository: "acme/app", Branch: "feat-a", Base: "main", BaseSHA: headSHA, Provenance: "created", Umbrella: "github:acme/app#10"},
	})
	if !strings.Contains(infoText, "umbrella: github:acme/app#10") {
		t.Fatalf("FormatWorktreeInfoText missing umbrella:\n%s", infoText)
	}

	// LoadWorkLogView surfaces umbrella from active and terminal claims.
	writeUmbrellaTestJSON(t, filepath.Join(wtActive, workLogProjectionDirectory, workLogProjectionName), workLogProjection{
		Version: 1, EffortID: "task-a", RunID: "run-1", ClaimID: claimID, Lifecycle: "active",
	})
	if view, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: wtActive}); err != nil || view.Claim == nil || view.Claim.Umbrella != "github:acme/app#10" {
		t.Fatalf("LoadWorkLogView active umbrella = (%+v, %v)", view.Claim, err)
	}
	writeUmbrellaTestJSON(t, filepath.Join(wtActive, workLogProjectionDirectory, workLogProjectionName), workLogProjection{
		Version: 1, EffortID: "task-a", RunID: "run-1", ClaimID: claimID, Lifecycle: "terminal",
	})
	if view, err := LoadWorkLogView(context.Background(), LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: wtActive}); err != nil || view.Claim == nil || view.Claim.Umbrella != "github:acme/app#10" {
		t.Fatalf("LoadWorkLogView terminal umbrella = (%+v, %v)", view.Claim, err)
	}

	// activeWorkLogClaimAtPath validates valid and rejects malformed .umbrella.json sidecars.
	sidecarRoot := t.TempDir()
	sidecarHome, _ := wbhome.Root(sidecarRoot)
	scClaim := makeUmbrellaTestClaim(t, sidecarRoot, "sc-task", "acme/app", "sc-br", "", time.Now().UTC())
	scClaimsDir := filepath.Join(sidecarHome, "worklogs", "sc-task", "runs", "run-1", "claims")
	writeUmbrellaTestJSON(t, filepath.Join(scClaimsDir, scClaim.ClaimID+worktreeclaims.UmbrellaBindingSuffix), worktreeclaims.ClaimUmbrellaBinding{
		Umbrella: "github:acme/app#10", RecordedAt: time.Now().UTC(),
	})
	if found, err := activeWorkLogClaimAtPath(sidecarHome, "/other/worktree", map[string]bool{"sc-task": true}); err != nil || found != nil {
		t.Fatalf("activeWorkLogClaimAtPath with valid umbrella sidecar = (%+v, %v)", found, err)
	}
	writeUmbrellaTestJSON(t, filepath.Join(scClaimsDir, scClaim.ClaimID+worktreeclaims.UmbrellaBindingSuffix), worktreeclaims.ClaimUmbrellaBinding{
		Umbrella: "invalid",
	})
	if _, err := activeWorkLogClaimAtPath(sidecarHome, "/other/worktree", map[string]bool{"sc-task": true}); err == nil {
		t.Fatal("activeWorkLogClaimAtPath accepted invalid umbrella sidecar")
	}

	// retireCommitMessage appends Wb-Umbrella trailer with default or explicit message.
	if got := retireCommitMessage(home, claim, ""); !strings.Contains(got, "Retire worktree source changes") || !strings.Contains(got, "Wb-Umbrella: github:acme/app#10") {
		t.Fatalf("retireCommitMessage default = %q", got)
	}
	if got := retireCommitMessage(home, claim, "custom"); got != "custom\n\nWb-Umbrella: github:acme/app#10" {
		t.Fatalf("retireCommitMessage custom = %q", got)
	}
	if got := retireCommitMessage(home, workLogClaim{ClaimID: strings.Repeat("0", 64)}, "plain"); got != "plain" {
		t.Fatalf("retireCommitMessage no umbrella = %q", got)
	}
	if got := retireCommitMessage(home, workLogClaim{ClaimID: strings.Repeat("0", 64), Umbrella: "github:invalid"}, "plain"); got != "plain" {
		t.Fatalf("retireCommitMessage invalid umbrella = %q", got)
	}

	// Unwritable claims dir fails SetTaskUmbrella cleanly.
	roRoot := t.TempDir()
	makeUmbrellaTestClaim(t, roRoot, "ro-task", "acme/ro", "ro", "", time.Now().UTC())
	roHome, _ := wbhome.Root(roRoot)
	roClaims := filepath.Join(roHome, "worklogs", "ro-task", "runs", "run-1", "claims")
	if err := os.Chmod(roClaims, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(roClaims, 0o700) })
	if _, err := SetTaskUmbrella(context.Background(), roRoot, "ro-task", ref); err == nil {
		t.Fatal("SetTaskUmbrella succeeded on read-only claims dir")
	}
}
