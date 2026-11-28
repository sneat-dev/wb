//go:build e2e

package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/umbrella"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

func TestE2EUmbrellaWorktreeResolutionAndEdgeCases(t *testing.T) {
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
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 0, "acme/app", "feat-a", headSHA)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 5, "acme/app", "feat-a", "")
	RecordPullRequestLandedSHA("", "acme/app", 5, "acme/app", "feat-a", headSHA)
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 5, "acme/app", "feat-a", headSHA)
	// Update existing binding second time to hit `if rec.pr != nil`.
	RecordPullRequestLandedSHA(projectsRoot, "acme/app", 5, "acme/app", "feat-a", strings.Repeat("e", 40))

	// 4. UmbrellaForPullRequest & UmbrellaForTask edge cases (unmatched, invalid umbrella string, bad root).
	makeUmbrellaTestClaim(t, projectsRoot, "bad-umb", "acme/bad", "bad-br", "github:invalid", time.Now().UTC())
	makeUmbrellaTestClaim(t, projectsRoot, "no-umb", "acme/none", "none-br", "", time.Now().UTC())
	if _, _, ok := UmbrellaForPullRequest(projectsRoot, "acme/bad", 1, "acme/bad", "bad-br", []string{"bad-umb"}); ok {
		t.Fatal("invalid umbrella matched PR")
	}
	if _, _, ok := UmbrellaForPullRequest(projectsRoot, "acme/none", 1, "acme/none", "none-br", []string{"no-umb"}); ok {
		t.Fatal("empty umbrella matched PR")
	}
	if _, _, ok := UmbrellaForPullRequest("", "acme/app", 1, "acme/app", "feat-a", nil); ok {
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

	// 6. collectClaimsInHome ignores missing runs/claims and atomic-write scratch files,
	// and fails closed on malformed or unreadable claim entries.
	makeUmbrellaTestClaim(t, projectsRoot, "extra", "acme/extra", "ex", "github:acme/app#10", time.Now().UTC())
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "no-runs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(home, "worklogs", "extra", "runs", "no-claims"), 0o700); err != nil {
		t.Fatal(err)
	}
	extraClaimsDir := filepath.Join(home, "worklogs", "extra", "runs", "run-1", "claims")
	if err := os.WriteFile(filepath.Join(extraClaimsDir, ".tmp-scratch"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := collectClaimsInHome(home); err != nil {
		t.Fatalf("collectClaimsInHome should allow missing runs/claims and scratch files: %v", err)
	}
	badClaimID := strings.Repeat("f", 64)
	badClaimPath := filepath.Join(extraClaimsDir, badClaimID+".json")
	if err := os.WriteFile(badClaimPath, []byte("{bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := collectClaimsInHome(home); err == nil || !strings.Contains(err.Error(), badClaimPath) {
		t.Fatalf("collectClaimsInHome should refuse malformed claim %s, got %v", badClaimPath, err)
	}
	_ = os.Remove(badClaimPath)
	unreadableID := strings.Repeat("e", 64)
	unreadablePath := filepath.Join(extraClaimsDir, unreadableID+".json")
	if err := os.WriteFile(unreadablePath, []byte("{}"), 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(unreadablePath, 0o600) })
	if _, err := collectClaimsInHome(home); err == nil || !strings.Contains(err.Error(), unreadablePath) {
		t.Fatalf("collectClaimsInHome should refuse unreadable claim %s, got %v", unreadablePath, err)
	}
	_ = os.Chmod(unreadablePath, 0o600)
	_ = os.Remove(unreadablePath)

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
	if _, _, ok := UmbrellaForPullRequest(badRoot, "o/r", 1, "o/r", "b", nil); ok {
		t.Fatal("UmbrellaForPullRequest(badRoot) succeeded")
	}
	RecordPullRequestLandedSHA(badRoot, "o/r", 1, "o/r", "b", headSHA)
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

	// activeWorkLogClaimAtPath validates valid and rejects malformed .umbrella.json and .landed.json sidecars.
	sidecarRoot := t.TempDir()
	sidecarHome, _ := wbhome.Root(sidecarRoot)
	scClaim := makeUmbrellaTestClaim(t, sidecarRoot, "sc-task", "acme/app", "sc-br", "", time.Now().UTC())
	scClaimsDir := filepath.Join(sidecarHome, "worklogs", "sc-task", "runs", "run-1", "claims")
	writeUmbrellaTestJSON(t, filepath.Join(scClaimsDir, scClaim.ClaimID+worktreeclaims.UmbrellaBindingSuffix), worktreeclaims.ClaimUmbrellaBinding{
		Umbrella: "github:acme/app#10", RecordedAt: time.Now().UTC(),
	})
	writeUmbrellaTestJSON(t, filepath.Join(scClaimsDir, scClaim.ClaimID+worktreeclaims.LandingBindingSuffix), worktreeclaims.ClaimLandingBinding{
		ClaimID: scClaim.ClaimID, Task: scClaim.Task, Repository: scClaim.Repository,
		Target: "main", LandedSHA: headSHA, RecordedAt: time.Now().UTC(),
	})
	if found, err := activeWorkLogClaimAtPath(sidecarHome, "/other/worktree", map[string]bool{"sc-task": true}); err != nil || found != nil {
		t.Fatalf("activeWorkLogClaimAtPath with valid umbrella+landing sidecars = (%+v, %v)", found, err)
	}
	writeUmbrellaTestJSON(t, filepath.Join(scClaimsDir, scClaim.ClaimID+worktreeclaims.LandingBindingSuffix), worktreeclaims.ClaimLandingBinding{
		ClaimID: scClaim.ClaimID,
	})
	if _, err := activeWorkLogClaimAtPath(sidecarHome, "/other/worktree", map[string]bool{"sc-task": true}); err == nil {
		t.Fatal("activeWorkLogClaimAtPath accepted invalid landing sidecar")
	}
	_ = os.Remove(filepath.Join(scClaimsDir, scClaim.ClaimID+worktreeclaims.LandingBindingSuffix))
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

	// Real-git handoff and failed-recycle recovery inherit effective umbrella from sidecar.
	hoRoot := t.TempDir()
	hoHome, err := wbhome.Root(hoRoot)
	if err != nil {
		t.Fatal(err)
	}
	hoWT := filepath.Join(hoRoot, "wt-ho")
	if err := os.MkdirAll(hoWT, 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, hoWT, "init", "-b", "ho-br")
	gitTest(t, hoWT, "config", "user.email", "t@example.com")
	gitTest(t, hoWT, "config", "user.name", "Test")
	if err := os.WriteFile(filepath.Join(hoWT, "f.txt"), []byte("f\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, hoWT, "add", "f.txt")
	gitTest(t, hoWT, "commit", "-m", "init")
	hoHead, err := git(context.Background(), hoWT, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	hoHead = strings.TrimSpace(hoHead)
	hoClaimID := worktreeclaims.WorkLogClaimID("ho-e2e", worktreeclaims.CreationResult{
		Repository: "acme/app", WorktreeDir: hoWT, Branch: "ho-br", Base: "main", BaseSHA: hoHead,
	})
	hoClaim := workLogClaim{
		Version: 2, EffortID: "ho-e2e", RunID: "run-1", ClaimID: hoClaimID, Task: "ho-e2e",
		Repository: "acme/app", Worktree: hoWT, Branch: "ho-br", Base: "main", BaseSHA: hoHead,
		Lifecycle: "active", RecordedAt: time.Now().UTC(), Model: "unknown", ModelProvenance: modelProvenanceUnknown,
	}
	hoClaimsDir := filepath.Join(hoHome, "worklogs", "ho-e2e", "runs", "run-1", "claims")
	writeUmbrellaTestJSON(t, filepath.Join(hoClaimsDir, hoClaimID+".json"), hoClaim)
	writeUmbrellaTestJSON(t, filepath.Join(hoWT, workLogProjectionDirectory, workLogProjectionName), workLogProjection{
		Version: 1, EffortID: "ho-e2e", RunID: "run-1", ClaimID: hoClaimID, Lifecycle: "active",
	})
	if _, err := SetTaskUmbrella(context.Background(), hoRoot, "ho-e2e", ref); err != nil {
		t.Fatalf("SetTaskUmbrella(ho-e2e): %v", err)
	}
	if err := transferWorkLogClaim(hoHome, hoWT, hoHead, "handoff", "next-e2e", ClaimExecutionIdentity{Model: "unknown"}); err != nil {
		t.Fatalf("transferWorkLogClaim(ho-e2e): %v", err)
	}
	if got := UmbrellaForWorktree(hoRoot, hoWT); got != ref.String() {
		t.Fatalf("UmbrellaForWorktree after real-git handoff = %q, want %q", got, ref.String())
	}

	// Failed-recycle recovery also inherits effective umbrella and refuses corrupt sidecar.
	recClaimID := worktreeclaims.WorkLogClaimID("rec-e2e", worktreeclaims.CreationResult{
		Repository: "acme/app", WorktreeDir: hoWT, Branch: "ho-br", Base: "main", BaseSHA: hoHead,
	})
	recClaim := workLogClaim{
		Version: 2, EffortID: "rec-e2e", RunID: "run-1", ClaimID: recClaimID, Task: "rec-e2e",
		Repository: "acme/app", Worktree: hoWT, Branch: "ho-br", Base: "main", BaseSHA: hoHead,
		Lifecycle: "active", RecordedAt: time.Now().UTC(), Model: "unknown", ModelProvenance: modelProvenanceUnknown,
	}
	recClaimsDir := filepath.Join(hoHome, "worklogs", "rec-e2e", "runs", "run-1", "claims")
	writeUmbrellaTestJSON(t, filepath.Join(recClaimsDir, recClaimID+".json"), recClaim)
	recTerm := workLogTerminalRecord{Claim: recClaim, FinalCommit: hoHead, Disposition: "recycled", SealedAt: time.Now().UTC()}
	recTerm.Lifecycle = "terminal"
	writeUmbrellaTestJSON(t, filepath.Join(hoHome, "worklogs", "rec-e2e", "runs", "run-1", "terminals", recClaimID+".json"), recTerm)
	recPrior := workLogProjection{Version: 1, EffortID: "rec-e2e", RunID: "run-1", ClaimID: recClaimID, Lifecycle: "active"}
	writeUmbrellaTestJSON(t, filepath.Join(recClaimsDir, recClaimID+worktreeclaims.UmbrellaBindingSuffix), worktreeclaims.ClaimUmbrellaBinding{Umbrella: "invalid"})
	if err := recoverFailedRecycleClaim(hoHome, hoWT, hoHead, recPrior); err == nil {
		t.Fatal("recoverFailedRecycleClaim accepted corrupt umbrella sidecar")
	}
	writeUmbrellaTestJSON(t, filepath.Join(recClaimsDir, recClaimID+worktreeclaims.UmbrellaBindingSuffix), worktreeclaims.ClaimUmbrellaBinding{
		Umbrella: ref.String(), RecordedAt: time.Now().UTC(),
	})
	if err := recoverFailedRecycleClaim(hoHome, hoWT, hoHead, recPrior); err != nil {
		t.Fatalf("recoverFailedRecycleClaim: %v", err)
	}
	if got := UmbrellaForWorktree(hoRoot, hoWT); got != ref.String() {
		t.Fatalf("UmbrellaForWorktree after recycle recovery = %q, want %q", got, ref.String())
	}
}
