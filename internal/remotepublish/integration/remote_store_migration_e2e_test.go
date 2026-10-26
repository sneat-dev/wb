//go:build e2e

package integration

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/gitrepo"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A machine upgraded from a release that cloned the store at
// <root>/<owner>/<name> migrates it on the first `wb remote` operation: the
// clone moves into <root>/.wb/remote-store/<owner>/<name> with its unpushed
// claim commit, which the next write pushes, and afterwards the fleet scan
// (what `wb hooks install --fleet` and sync walk) no longer sees it
// (sneat-dev/wb#901).
//
//nolint:paralleltest // newRemoteFixture sets process-wide environment.
func TestE2EOpenedRemoteStoreMigratesTheLegacyCloneOutOfTheFleet(t *testing.T) {
	f := newRemoteFixture(t, "mac")
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state", Machine: "mac"}
	legacy := filepath.Join(f.projectsRoot, "team", "wb-state")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, filepath.Dir(legacy), "clone", "-q", f.origin, legacy)
	remoteGit(t, legacy, "remote", "set-url", "origin", remotepublish.CloneURL(cfg))
	remoteGit(t, legacy, "config", "url."+f.origin+".insteadOf", remotepublish.CloneURL(cfg))
	unpushed := filepath.Join(legacy, filepath.FromSlash(gitrepo.ClaimPath("unpushed")))
	if err := os.MkdirAll(filepath.Dir(unpushed), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(unpushed, []byte("task: unpushed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, legacy, "add", "claims")
	remoteGit(t, legacy, "commit", "-q", "-m", "wb: claim unpushed")
	if repos, err := discover.ScanLocal(f.projectsRoot); err != nil || !scanned(repos, legacy) {
		t.Fatalf("fixture: the legacy clone is not a fleet repository (%v, %v)", repos, err)
	}

	provider, err := remotepublish.Open(cfg, f.projectsRoot, testExitFactory)
	if err != nil {
		t.Fatal(err)
	}
	claim := remotestate.Claim{SchemaVersion: remotestate.ClaimSchemaVersion, Task: "next-task", Login: "me", Machine: "mac", ClaimedAt: time.Date(2026, 10, 24, 12, 0, 0, 0, time.UTC)}
	if _, err := provider.Claim(context.Background(), claim, remotestate.ClaimNormal, ""); err != nil {
		t.Fatalf("Claim after migration = %v", err)
	}
	store, err := remotepublish.ClonePath(f.projectsRoot, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store, ".git")); err != nil {
		t.Fatalf("no migrated clone at %s: %v", store, err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("legacy clone still at %s: %v", legacy, err)
	}
	files := remoteGit(t, f.origin, "ls-tree", "-r", "--name-only", "main")
	if !strings.Contains(files, gitrepo.ClaimPath("unpushed")) || !strings.Contains(files, gitrepo.ClaimPath("next-task")) {
		t.Fatalf("origin files = %q, want the migrated unpushed claim and the new one", files)
	}
	repos, err := discover.ScanLocal(f.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if scanned(repos, store) {
		t.Fatalf("fleet scan %v sees the remote store inside .wb", repos)
	}
}

// scanned compares physical paths: the store path is symlink-resolved (macOS
// /var -> /private/var) while the scan joins the root as given.
func scanned(repos []discover.Repo, path string) bool {
	physical := func(p string) string {
		if resolved, err := filepath.EvalSymlinks(p); err == nil {
			return resolved
		}
		return filepath.Clean(p)
	}
	for _, repo := range repos {
		if physical(repo.Path) == physical(path) {
			return true
		}
	}
	return false
}

// A store reader built before the migration — the daemon builds its cockpit
// fleet reader once at start-up — still sees the machines after the daemon's
// own publish (or any CLI operation) moved the clone into .wb.
//
//nolint:paralleltest // newRemoteFixture sets process-wide environment.
func TestE2EActiveStoreReaderKeepsReadingAcrossTheMigration(t *testing.T) {
	f := newRemoteFixture(t, "mac")
	cfg := remotestate.Config{Provider: "git", Repo: "team/wb-state", Machine: "mac"}
	legacy := filepath.Join(f.projectsRoot, "team", "wb-state")
	if err := os.MkdirAll(filepath.Dir(legacy), 0o755); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, filepath.Dir(legacy), "clone", "-q", f.origin, legacy)
	remoteGit(t, legacy, "remote", "set-url", "origin", remotepublish.CloneURL(cfg))
	remoteGit(t, legacy, "config", "url."+f.origin+".insteadOf", remotepublish.CloneURL(cfg))
	snapshot := filepath.Join(legacy, filepath.FromSlash(gitrepo.SnapshotPath("bob", "desk")))
	data, err := remotestate.Encode(remotestate.Snapshot{SchemaVersion: remotestate.SchemaVersion, Login: "bob", Machine: "desk", PublishedAt: time.Date(2026, 10, 24, 8, 0, 0, 0, time.UTC), WBVersion: "v0.1.0"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(snapshot), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(snapshot, data, 0o644); err != nil {
		t.Fatal(err)
	}
	remoteGit(t, legacy, "add", "machines")
	remoteGit(t, legacy, "commit", "-q", "-m", "wb: publish bob/desk")

	reader := remotepublish.ActiveStoreReader{ProjectsRoot: f.projectsRoot, Config: cfg}
	if entries, err := reader.ReadLocal(context.Background()); err != nil || len(entries) != 1 {
		t.Fatalf("before migration: ReadLocal = (%d entries, %v), want bob/desk", len(entries), err)
	}
	provider, err := remotepublish.Open(cfg, f.projectsRoot, testExitFactory)
	if err != nil {
		t.Fatal(err)
	}
	mine := remotestate.Snapshot{SchemaVersion: remotestate.SchemaVersion, Login: "me", Machine: "mac", PublishedAt: time.Date(2026, 10, 24, 9, 0, 0, 0, time.UTC), WBVersion: "v0.1.0"}
	if _, err := provider.Publish(context.Background(), mine); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(legacy); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture: the publish did not migrate the legacy clone: %v", err)
	}
	entries, err := reader.ReadLocal(context.Background())
	if err != nil || len(entries) != 2 {
		t.Fatalf("after migration: ReadLocal = (%v, %v), want bob/desk and me/mac", entries, err)
	}
}
