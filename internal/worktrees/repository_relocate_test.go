package worktrees

import (
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
	gitTest(t, fixture.canonical, "remote", "set-url", "--push", "origin", "git@github.com:acme/app.git")
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
