package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
		t.Fatal("preexisting exact disposable destination was not preserved before replacement")
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
