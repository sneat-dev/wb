package worktrees

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRelocateRepositoryMovesCanonicalAndNestedWorktreePreservingClaim(t *testing.T) {
	fixture := newGitFixture(t)
	remoteRoot := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes")
	oldRemote := filepath.Join(remoteRoot, "acme", "app.git")
	newRemote := filepath.Join(remoteRoot, "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.remote, oldRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", oldRemote)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-live", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, _, _, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "feature.txt"), []byte("transferred feature\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, created[0].WorktreeDir, "commit", "-m", "transferred feature")
	featureHead := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)
	gitTest(t, fixture.canonical, "remote", "set-url", "--push", "origin", "git@github.com:acme/app.git")
	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}
	preexistingDestination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	if err := os.MkdirAll(filepath.Dir(preexistingDestination), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(preexistingDestination), "clone", newRemote, preexistingDestination)

	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: newRemote, DefaultBranch: "main"}
	plan, err := RelocateRepository(context.Background(), options)
	if err != nil || !plan.Eligible || plan.Applied {
		t.Fatalf("relocation plan = %#v, err=%v", plan, err)
	}
	options.Apply = true
	applied, err := RelocateRepository(context.Background(), options)
	if err != nil || !applied.Applied || len(applied.ReceiptPaths) != 1 {
		t.Fatalf("relocation apply = %#v, err=%v", applied, err)
	}
	if applied.RetiredDestinationDir == "" {
		t.Fatal("preexisting exact disposable destination was not quarantined before replacement")
	}
	if applied.SourcePushURL != "git@github.com:acme/app.git" || applied.SourcePushURL == applied.SourceFetchURL {
		t.Fatalf("separate source origin URLs were not preserved in evidence: fetch=%q push=%q", applied.SourceFetchURL, applied.SourcePushURL)
	}
	if _, err := os.Lstat(applied.RetiredDestinationDir); !os.IsNotExist(err) {
		t.Fatalf("temporary replacement quarantine remains after verified transfer: %v", err)
	}
	if applied.ReplacementCleanupStatus != repositoryTransferCleanupRetired || !strings.HasSuffix(applied.ReplacementCleanupReceipt, "-cleanup-retired.json") {
		t.Fatalf("replacement cleanup evidence = status %q receipt %q", applied.ReplacementCleanupStatus, applied.ReplacementCleanupReceipt)
	}
	destination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	movedWorktree := filepath.Join(destination, ".worktrees", "transfer-live")
	if _, err := os.Stat(fixture.canonical); !os.IsNotExist(err) {
		t.Fatalf("source canonical remains: %v", err)
	}
	if _, err := Guard(context.Background(), movedWorktree, GuardOptions{ProjectsRoot: fixture.projectsRoot}); err != nil {
		t.Fatalf("guard relocated worktree: %v", err)
	}
	claimAfter, _, _, err := activeWorkLogClaim(fixture.home, movedWorktree)
	if err != nil || claimAfter.ClaimID != claimBefore.ClaimID || claimAfter.Repository != "acme/app" {
		t.Fatalf("relocated claim = %#v, err=%v", claimAfter, err)
	}
	view, _, err := LogShow(context.Background(), fixture.projectsRoot, movedWorktree)
	if err != nil || view.Claim == nil || view.Claim.Repository != "newco/renamed" || view.Claim.Worktree != movedWorktree {
		t.Fatalf("resolved transferred Work Log view = %#v, err=%v", view.Claim, err)
	}
	for _, push := range []bool{false, true} {
		urls, err := exactOriginURLs(context.Background(), destination, push)
		if err != nil || len(urls) != 1 || urls[0] != newRemote {
			t.Fatalf("origin push=%t URLs=%v err=%v", push, urls, err)
		}
	}
	common := gitTestOutput(t, movedWorktree, "rev-parse", "--path-format=absolute", "--git-common-dir")
	if common != filepath.Join(destination, ".git") {
		t.Fatalf("linked worktree common dir = %s", common)
	}
	gitTest(t, destination, "merge", "--no-ff", created[0].Branch, "-m", "merge transferred feature")
	gitTest(t, destination, "push", "origin", "main")
	mergedAt := time.Date(2026, time.September, 6, 18, 48, 12, 0, time.UTC)
	installTransferredPullRequestFixture(t, created[0].Branch, featureHead, mergedAt)
	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "transfer-live", Base: "main", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil || len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].Repository != "newco/renamed" ||
		planned.Results[0].WorktreeDir != movedWorktree || planned.Results[0].MergedPullRequest == nil || planned.Results[0].MergedPullRequest.Number != 8 {
		t.Fatalf("transferred merged cleanup plan = %#v, err=%v", planned, err)
	}
}

// TestCleanupRecoversLegacyRepositoryTransferClaim proves the dogfood failure
// shape: the repository moved before WB started writing transfer receipts. The
// immutable claim keeps its old repository/path, while Git and origin identify
// the new repository. Cleanup must first prove this exact head landed, then
// append the missing evidence without rewriting that claim.
func TestCleanupRecoversLegacyRepositoryTransferClaim(t *testing.T) {
	fixture := newGitFixture(t)
	remoteRoot := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes")
	oldRemote := filepath.Join(remoteRoot, "acme", "app.git")
	newRemote := filepath.Join(remoteRoot, "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.remote, oldRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", oldRemote)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "legacy-transfer-cleanup", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	claim, projection, claimPath, err := activeWorkLogClaim(fixture.home, created[0].WorktreeDir)
	if err != nil {
		t.Fatal(err)
	}
	claimBefore, err := os.ReadFile(claimPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(created[0].WorktreeDir, "feature.txt"), []byte("legacy transfer\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, created[0].WorktreeDir, "add", "feature.txt")
	gitTest(t, created[0].WorktreeDir, "commit", "-m", "legacy transferred feature")
	head := gitTestOutput(t, created[0].WorktreeDir, "rev-parse", "HEAD")
	gitTest(t, created[0].WorktreeDir, "push", "-u", "origin", created[0].Branch)

	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.canonical, destination); err != nil {
		t.Fatal(err)
	}
	movedWorktree := filepath.Join(destination, ".worktrees", "legacy-transfer-cleanup")
	gitTest(t, destination, "remote", "set-url", "origin", newRemote)
	gitTest(t, destination, "remote", "set-url", "--push", "origin", newRemote)
	gitTest(t, destination, "worktree", "repair", movedWorktree)
	gitTest(t, destination, "merge", "--no-ff", created[0].Branch, "-m", "merge legacy transferred feature")
	gitTest(t, destination, "push", "origin", "main")

	mergedAt := time.Date(2026, time.September, 6, 20, 0, 0, 0, time.UTC)
	installOpenAndMergedExactHeadPullRequestFixture(t, head, "main", "main", mergedAt)
	blocked, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "legacy-transfer-cleanup", Base: "main", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil || len(blocked.Results) != 1 || blocked.Results[0].Eligible || blocked.Results[0].OpenPullRequest == nil {
		t.Fatalf("legacy transfer cleanup bypassed open PR fence: %#v, err=%v", blocked, err)
	}
	if resolution, err := latestRelocationResolution(fixture.home, claim, movedWorktree); err != nil || resolution.receipt != nil {
		t.Fatalf("open PR created legacy transfer evidence: %#v err=%v", resolution, err)
	}
	installTransferredPullRequestFixture(t, created[0].Branch, head, mergedAt)
	planned, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "legacy-transfer-cleanup", Base: "main", OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil || len(planned.Results) != 1 || !planned.Results[0].Eligible || planned.Results[0].MergedPullRequest == nil || planned.Results[0].MergedPullRequest.Number != 8 {
		t.Fatalf("legacy transfer cleanup plan = %#v, err=%v", planned, err)
	}
	if resolution, err := latestRelocationResolution(fixture.home, claim, movedWorktree); err != nil || resolution.receipt != nil {
		t.Fatalf("legacy claim had transfer evidence before cleanup: %#v err=%v", resolution, err)
	}

	applied, err := Cleanup(context.Background(), CleanupOptions{
		ProjectsRoot: fixture.projectsRoot, Task: "legacy-transfer-cleanup", Base: "main", Apply: true, DeleteRemote: true, OlderThan: 0,
		Now: func() time.Time { return mergedAt.Add(time.Hour) },
	})
	if err != nil || len(applied.Results) != 1 || !applied.Results[0].Applied {
		t.Fatalf("legacy transfer cleanup apply = %#v, err=%v", applied, err)
	}
	claimAfter, err := os.ReadFile(claimPath)
	if err != nil || !bytes.Equal(claimBefore, claimAfter) {
		t.Fatalf("immutable claim changed during recovery: err=%v", err)
	}
	resolution, err := latestRelocationResolution(fixture.home, claim, movedWorktree)
	if err != nil || resolution.receipt == nil || resolution.repository != "newco/renamed" || resolution.worktree != movedWorktree {
		t.Fatalf("legacy transfer recovery evidence = %#v, err=%v", resolution, err)
	}
	if _, statErr := os.Stat(movedWorktree); !os.IsNotExist(statErr) {
		t.Fatalf("recovered worktree remains after cleanup: %v", statErr)
	}
	if got := remoteBranchForTest(t, destination, created[0].Branch); got != "" {
		t.Fatalf("recovered source branch remains at %s", got)
	}
	terminalPath := filepath.Join(fixture.home, "worklogs", projection.EffortID, "runs", projection.RunID, "terminals", projection.ClaimID+".json")
	if _, err := os.Stat(terminalPath); err != nil {
		t.Fatalf("cleanup did not seal a terminal after recovery: %v", err)
	}
}

func installTransferredPullRequestFixture(t *testing.T, branch, head string, mergedAt time.Time) {
	t.Helper()
	binDir := t.TempDir()
	script := filepath.Join(binDir, "gh")
	content := "#!/bin/sh\nset -eu\nif [ \"$1 $2\" != \"api --paginate\" ]; then echo \"unexpected gh command: $*\" >&2; exit 2; fi\nprintf '%s\\n' \"$WB_TEST_TRANSFERRED_PULL\"\n"
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	payload := `[{"number":8,"html_url":"https://github.com/newco/renamed/pull/8","state":"closed","merged_at":"` + mergedAt.Format(time.RFC3339) + `","head":{"ref":"` + branch + `","sha":"` + head + `"},"base":{"ref":"main","sha":""},"merge_commit_sha":""}]`
	t.Setenv("WB_TEST_TRANSFERRED_PULL", payload)
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

func TestRelocateRepositoryReturnsResumableCleanupPending(t *testing.T) {
	fixture := newGitFixture(t)
	remoteRoot := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes")
	oldRemote := filepath.Join(remoteRoot, "acme", "app.git")
	newRemote := filepath.Join(remoteRoot, "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.remote, oldRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", oldRemote)
	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(destination), "clone", newRemote, destination)

	forced := errors.New("forced retirement interruption")
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: newRemote, DefaultBranch: "main", Apply: true,
		beforeReplacementRetirement: func() error { return forced }}
	result, err := RelocateRepository(context.Background(), options)
	if err != nil || !result.Applied || !result.CleanupPending || result.RecoveryCommand == "" {
		t.Fatalf("cleanup-pending transfer = %#v, err=%v", result, err)
	}
	if !strings.Contains(result.RecoveryCommand, "repo transfer cleanup") || !strings.Contains(result.RecoveryCommand, "--non-interactive") {
		t.Fatalf("recovery command is not canonical and non-interactive: %q", result.RecoveryCommand)
	}
	if _, err := os.Stat(result.RetiredDestinationDir); err != nil {
		t.Fatal(err)
	}
	recovered, err := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, ReceiptPath: result.ReplacementCleanupReceipt, Apply: true,
	})
	if err != nil || !recovered.Applied {
		t.Fatalf("cleanup recovery = %#v, err=%v", recovered, err)
	}
	if _, err := os.Lstat(result.RetiredDestinationDir); !os.IsNotExist(err) {
		t.Fatalf("replacement quarantine remains after recovery: %v", err)
	}
	reconciled, err := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, ReceiptPath: result.ReplacementCleanupReceipt, Apply: true,
	})
	if err != nil || !reconciled.Applied || reconciled.Outcome != repositoryTransferCleanupRetired {
		t.Fatalf("absent-quarantine evidence recovery = %#v, err=%v", reconciled, err)
	}
}

func TestRecoverRepositoryTransferCleanupRecordsRestoredDestination(t *testing.T) {
	temporaryRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	projectsRoot := filepath.Join(temporaryRoot, "projects")
	t.Setenv("WB_HOME", filepath.Join(temporaryRoot, "wb-home"))
	destination := filepath.Join(projectsRoot, "newco", "renamed")
	quarantine := filepath.Join(projectsRoot, "newco", ".wb-replaced-renamed-0123456789ab")
	if err := os.MkdirAll(quarantine, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := openAbsoluteDirectoryNoFollow(quarantine, false)
	if err != nil {
		t.Fatal(err)
	}
	options := RepositoryRelocateOptions{ProjectsRoot: projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: "git@github.com:newco/renamed.git", DefaultBranch: "main", Now: time.Now}
	result := RepositoryRelocateResult{SourceRepository: options.SourceRepository, DestinationRepository: options.DestinationRepository,
		DestinationDir: destination, RetiredDestinationDir: quarantine, RemoteURL: options.RemoteURL, DefaultBranch: options.DefaultBranch}
	_, pending, err := recordRepositoryTransferCleanupIntent(options, result, "0123456789abcdef0123456789abcdef01234567", held)
	if err != nil {
		t.Fatal(err)
	}
	_ = held.Close()
	restored, err := moveRenameDirectory(quarantine, destination, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = restored.Close()

	recovered, err := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{
		ProjectsRoot: projectsRoot, ReceiptPath: pending, Apply: true,
	})
	if err != nil || !recovered.Applied || recovered.Outcome != repositoryTransferCleanupRestored {
		t.Fatalf("restored cleanup evidence = %#v, err=%v", recovered, err)
	}
	if _, err := os.Stat(destination); err != nil {
		t.Fatalf("restored destination was removed: %v", err)
	}
}

func TestRelocateRepositoryRecoversEvidenceAfterReplacementAlreadyRetired(t *testing.T) {
	fixture := newGitFixture(t)
	remoteRoot := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes")
	oldRemote := filepath.Join(remoteRoot, "acme", "app.git")
	newRemote := filepath.Join(remoteRoot, "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.remote, oldRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", oldRemote)
	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(destination), "clone", newRemote, destination)

	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: newRemote, DefaultBranch: "main", Apply: true,
		beforeReplacementCleanupCompleted: func() error { return errors.New("forced evidence interruption") }}
	result, err := RelocateRepository(context.Background(), options)
	if err != nil || !result.Applied || !result.CleanupPending || !strings.Contains(result.Reason, "evidence_pending") {
		t.Fatalf("evidence-pending transfer = %#v, err=%v", result, err)
	}
	if _, err := os.Lstat(result.RetiredDestinationDir); !os.IsNotExist(err) {
		t.Fatalf("replacement quarantine remains after retirement: %v", err)
	}
	recovered, err := RecoverRepositoryTransferCleanup(context.Background(), RepositoryTransferCleanupOptions{
		ProjectsRoot: fixture.projectsRoot, ReceiptPath: result.ReplacementCleanupReceipt, Apply: true,
	})
	if err != nil || !recovered.Applied || recovered.Outcome != repositoryTransferCleanupRetired {
		t.Fatalf("evidence recovery = %#v, err=%v", recovered, err)
	}
}

func TestRelocateRepositoryRefusesDirtyOrOccupiedDestination(t *testing.T) {
	fixture := newGitFixture(t)
	remote := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes", "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(remote), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(remote), "clone", "--bare", fixture.remote, remote)
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", filepath.Join(filepath.Dir(fixture.projectsRoot), "acme", "app.git"))
	if err := os.WriteFile(filepath.Join(fixture.canonical, "dirty.txt"), []byte("dirty\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	options := RepositoryRelocateOptions{ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app",
		DestinationRepository: "newco/renamed", RemoteURL: remote, DefaultBranch: "main"}
	result, err := RelocateRepository(context.Background(), options)
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "local changes") {
		t.Fatalf("dirty result = %#v, err=%v", result, err)
	}
	if err := os.Remove(filepath.Join(fixture.canonical, "dirty.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(fixture.projectsRoot, "newco", "renamed"), 0o755); err != nil {
		t.Fatal(err)
	}
	result, err = RelocateRepository(context.Background(), options)
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "not safely replaceable") {
		t.Fatalf("collision result = %#v, err=%v", result, err)
	}
}

func TestFinalizeRepositoryTransferWorkLogsRecoversInterruptedCompletionAndVerifiesOrigin(t *testing.T) {
	fixture := newGitFixture(t)
	remoteRoot := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes")
	oldRemote := filepath.Join(remoteRoot, "acme", "app.git")
	newRemote := filepath.Join(remoteRoot, "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.remote, oldRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", oldRemote)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: "transfer-receipt-recovery", WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}
	forced := errors.New("forced Work Log completion interruption")
	options := RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed",
		RemoteURL: newRemote, DefaultBranch: "main", Apply: true,
		beforeWorkLogCompletion: func(string) error { return forced },
	}
	if _, err := RelocateRepository(context.Background(), options); !errors.Is(err, forced) {
		t.Fatalf("relocation interruption error = %v", err)
	}
	destination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	movedWorktree := filepath.Join(destination, ".worktrees", "transfer-receipt-recovery")
	if _, err := os.Stat(created[0].WorktreeDir); !os.IsNotExist(err) {
		t.Fatalf("source worktree remains after verified move: %v", err)
	}
	gitTest(t, destination, "remote", "set-url", "origin", "git@github.com:other/repository.git")
	gitTest(t, destination, "remote", "set-url", "--push", "origin", "git@github.com:other/repository.git")
	options.beforeWorkLogCompletion = nil
	if _, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err == nil || !strings.Contains(err.Error(), "does not identify") {
		t.Fatalf("mismatched-origin recovery error = %v", err)
	}
	gitTest(t, destination, "remote", "set-url", "origin", newRemote)
	gitTest(t, destination, "remote", "set-url", "--push", "origin", newRemote)
	receipts, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options)
	if err != nil || len(receipts) != 1 {
		t.Fatalf("receipt recovery = %v, %v", receipts, err)
	}
	if repeated, err := FinalizeRepositoryTransferWorkLogs(context.Background(), options); err != nil || len(repeated) != 0 {
		t.Fatalf("repeated receipt recovery = %v, %v", repeated, err)
	}
	view, _, err := LogShow(context.Background(), fixture.projectsRoot, movedWorktree)
	if err != nil || view.Claim == nil || view.Claim.Repository != "newco/renamed" {
		t.Fatalf("recovered Work Log view = %#v, %v", view.Claim, err)
	}
}

func TestRelocateRepositoryPlanDoesNotFetchDisposableDestination(t *testing.T) {
	options, destination, remote := newRepositoryTransferCollisionFixture(t)
	head := gitTestOutput(t, destination, "rev-parse", "HEAD")
	gitTest(t, remote, "update-ref", "refs/heads/unseen", head)
	before := gitTestOutput(t, destination, "show-ref")
	if strings.Contains(before, "refs/remotes/origin/unseen") {
		t.Fatal("fixture unexpectedly fetched unseen remote branch")
	}
	result, err := RelocateRepository(context.Background(), options)
	if err != nil || !result.Eligible || result.Applied {
		t.Fatalf("relocation plan = %#v, err=%v", result, err)
	}
	after := gitTestOutput(t, destination, "show-ref")
	if after != before {
		t.Fatalf("dry-run mutated destination refs\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestRelocateRepositoryRefusesCustomDestinationRef(t *testing.T) {
	options, destination, _ := newRepositoryTransferCollisionFixture(t)
	gitTest(t, destination, "update-ref", "refs/notes/review", "HEAD")
	result, err := RelocateRepository(context.Background(), options)
	if err != nil || result.Eligible || !strings.Contains(result.Reason, "unsupported local ref refs/notes/review") {
		t.Fatalf("custom-ref plan = %#v, err=%v", result, err)
	}
	if got := gitTestOutput(t, destination, "rev-parse", "refs/notes/review"); got == "" {
		t.Fatal("custom ref disappeared during plan")
	}
}

func newRepositoryTransferCollisionFixture(t *testing.T) (RepositoryRelocateOptions, string, string) {
	t.Helper()
	fixture := newGitFixture(t)
	remoteRoot := filepath.Join(filepath.Dir(fixture.projectsRoot), "remotes")
	oldRemote := filepath.Join(remoteRoot, "acme", "app.git")
	newRemote := filepath.Join(remoteRoot, "newco", "renamed.git")
	if err := os.MkdirAll(filepath.Dir(oldRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(fixture.remote, oldRemote); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "remote", "set-url", "origin", oldRemote)
	if err := os.MkdirAll(filepath.Dir(newRemote), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(oldRemote, newRemote); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(fixture.projectsRoot, "newco", "renamed")
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		t.Fatal(err)
	}
	gitTest(t, filepath.Dir(destination), "clone", newRemote, destination)
	return RepositoryRelocateOptions{
		ProjectsRoot: fixture.projectsRoot, SourceRepository: "acme/app", DestinationRepository: "newco/renamed",
		RemoteURL: newRemote, DefaultBranch: "main",
	}, destination, newRemote
}
