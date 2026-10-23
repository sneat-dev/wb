//go:build e2e

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

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EAdoptionClaimStatePreservesAdmissionAuthority(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adoption-claim-state")
	claimed, err := adoptionClaimState(fixture.home, path)
	if claimed || err != nil {
		t.Fatalf("unclaimed external checkout: claimed=%t err=%v", claimed, err)
	}
	projectionDir := filepath.Join(path, workLogProjectionDirectory)
	if err := os.MkdirAll(projectionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(projectionDir, workLogProjectionName)
	if err := os.WriteFile(projectionPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	claimed, err = adoptionClaimState(fixture.home, path)
	if claimed || err == nil {
		t.Fatalf("malformed projection treated as absence: claimed=%t err=%v", claimed, err)
	}
	candidate := OrphanWorktree{Path: path, Layout: LayoutExternal}
	refused := adoptOne(context.Background(), fixture.projectsRoot, fixture.home, candidate, false, "", time.Now().UTC())
	if refused.Action != AdoptSkipped || !strings.Contains(refused.Reason, "existing Work Log state is inconsistent") {
		t.Fatalf("pre-preview claim refusal: %+v", refused)
	}
	if err := os.Remove(projectionPath); err != nil {
		t.Fatal(err)
	}
	applied, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: path, Apply: true})
	if err != nil || len(applied) != 1 || applied[0].Action != AdoptAdopted {
		t.Fatalf("valid adoption: %+v, %v", applied, err)
	}
	claimed, err = adoptionClaimState(fixture.home, path)
	if !claimed || err != nil {
		t.Fatalf("published claim: claimed=%t err=%v", claimed, err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EAdoptionSweepFiltersAndResolvesPathAlias(t *testing.T) {
	fixture := newGitFixture(t)
	selected := fixture.externalWorktree(t, "feature/adoption-selected")
	other := fixture.externalWorktree(t, "feature/adoption-other")
	managed := filepath.Join(fixture.projectsRoot, ".worktrees", "managed-adoption", "acme", "app")
	if err := os.MkdirAll(filepath.Dir(managed), 0o700); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "worktree", "add", "-b", "feature/adoption-managed", managed, "main")
	clock := time.Date(2026, time.October, 1, 8, 30, 0, 0, time.UTC)
	results, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, AllExternal: true,
		Filter: "adoption-selected", Now: func() time.Time { return clock }})
	if err != nil || len(results) != 1 || results[0].Path != selected || results[0].Action != AdoptWouldAdopt {
		t.Fatalf("filtered dry sweep: %+v, %v", results, err)
	}
	if _, err := ReadManifest(selected); err == nil {
		t.Fatal("filtered dry sweep wrote a manifest")
	}
	alias := filepath.Join(t.TempDir(), "selected")
	if err := os.Symlink(selected, alias); err != nil {
		t.Fatal(err)
	}
	results, err = Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, Path: alias})
	if err != nil || len(results) != 1 || results[0].Path != selected || results[0].Action != AdoptWouldAdopt {
		t.Fatalf("symlinked path selection: %+v, %v", results, err)
	}
	if _, err := ReadManifest(other); err == nil {
		t.Fatal("unselected checkout gained a manifest")
	}
	results, err = Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, AllExternal: true})
	if err != nil || len(results) != 2 || results[0].Path > results[1].Path || results[0].Path == managed || results[1].Path == managed {
		t.Fatalf("all-external sweep did not exclude managed layout or sort: %+v, %v", results, err)
	}
}

//nolint:paralleltest // the native fixture sets process-wide Git environment.
func TestE2EAdoptionRejectsInvalidRootAndUnresolvableLegacyHome(t *testing.T) {
	if _, err := Adopt(context.Background(), AdoptOptions{AllExternal: true}); err == nil || !strings.Contains(err.Error(), "projects root is required") {
		t.Fatalf("missing projects root: %v", err)
	}
	fixture := newGitFixture(t)
	userHome := os.Getenv("HOME")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".wb", filepath.Join(userHome, ".wb")); err != nil {
		t.Fatal(err)
	}
	if _, err := Adopt(context.Background(), AdoptOptions{ProjectsRoot: fixture.projectsRoot, AllExternal: true}); err == nil {
		t.Fatal("unresolvable legacy WB home admitted")
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EAdoptionRefusesBadManifestAndMissingBase(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adoption-manifest-refusal")
	candidate := OrphanWorktree{Path: path, Layout: LayoutExternal}
	manifestDir := filepath.Join(path, journalRootDirectory, journalLocalDirectory)
	if err := os.MkdirAll(manifestDir, 0o700); err != nil {
		t.Fatal(err)
	}
	manifestPath := filepath.Join(manifestDir, manifestName)
	if err := os.WriteFile(manifestPath, []byte("[broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	refused := adoptOne(context.Background(), fixture.projectsRoot, fixture.home, candidate, false, "", time.Now().UTC())
	if refused.Action != AdoptSkipped || refused.Reason == "" {
		t.Fatalf("malformed manifest preview: %+v", refused)
	}
	if err := os.Remove(manifestPath); err != nil {
		t.Fatal(err)
	}
	gitTest(t, fixture.canonical, "update-ref", "-d", "refs/remotes/origin/main")
	refused = adoptOne(context.Background(), fixture.projectsRoot, fixture.home, candidate, false, "", time.Now().UTC())
	if refused.Action != AdoptSkipped || !strings.Contains(refused.Reason, "cannot determine an ancestor base commit") {
		t.Fatalf("unfetched base admitted: %+v", refused)
	}
	if _, err := ReadManifest(path); !errors.Is(err, errManifestNotFound) {
		t.Fatalf("refused preview wrote a manifest: %v", err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EAdoptionPublicationRetriesExactRegistration(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adoption-publication-retry")
	manifest, err := PreviewReconstructedManifest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(fixture.home, 0o700); err != nil {
		t.Fatal(err)
	}
	worklogsRoot := filepath.Join(fixture.home, "worklogs")
	if err := os.WriteFile(worklogsRoot, []byte("obstructed"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptApply(fixture.home, "acme", "app", manifest, path, "operator", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "record adoption Work Log claim") {
		t.Fatalf("blocked immutable claim publication: %v", err)
	}
	registration := filepath.Join(fixture.home, "worktrees", manifest.EffortID, "acme", "app", adoptedWorktreePointerName)
	if _, err := os.Stat(registration); err != nil {
		t.Fatalf("interrupted adoption lost registration evidence: %v", err)
	}
	if claimed, err := adoptionClaimState(fixture.home, path); claimed || err != nil {
		t.Fatalf("failed publication created claim: claimed=%t err=%v", claimed, err)
	}
	if err := os.Remove(worklogsRoot); err != nil {
		t.Fatal(err)
	}
	if err := adoptApply(fixture.home, "acme", "app", manifest, path, "operator", time.Now().UTC()); err != nil {
		t.Fatalf("retry with exact registration: %v", err)
	}
	if claimed, err := adoptionClaimState(fixture.home, path); !claimed || err != nil {
		t.Fatalf("retry omitted immutable claim: claimed=%t err=%v", claimed, err)
	}
	if err := adoptApply(fixture.home, "acme", "app", manifest, path, "operator", time.Now().UTC()); err != nil {
		t.Fatalf("already-claimed retry: %v", err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EAdoptionRefusesChangedCoordinatesAndLockedPublication(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adoption-lock-refusal")
	candidate := OrphanWorktree{Path: path, Layout: LayoutExternal}
	bad := candidate
	bad.Path = t.TempDir()
	refused := adoptOne(context.Background(), fixture.projectsRoot, fixture.home, bad, false, "", time.Now().UTC())
	if refused.Action != AdoptSkipped || refused.Reason == "" {
		t.Fatalf("foreign checkout coordinates: %+v", refused)
	}
	manifest, err := PreviewReconstructedManifest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	operation, err := prepareOperationRoot(fixture.home, manifest.EffortID, nil)
	if err != nil {
		t.Fatal(err)
	}
	lockPath := filepath.Join(operation.Path, ".lock")
	operation.close()
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	refused = adoptOne(context.Background(), fixture.projectsRoot, fixture.home, candidate, true, "", time.Now().UTC())
	if refused.Action != AdoptSkipped || refused.Reason == "" {
		t.Fatalf("blocked task lock: %+v", refused)
	}
	if _, err := os.Stat(filepath.Join(path, workLogProjectionDirectory, workLogProjectionName)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lock refusal published claim projection: %v", err)
	}
}

func TestE2EAdoptionRegistrationRefusesRedirectsAndStorageFaults(t *testing.T) {
	t.Parallel()
	home := t.TempDir()
	operation, err := prepareOperationRoot(home, "adoption-registration-faults", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(operation.close)
	worktree := filepath.Join(t.TempDir(), "external-checkout")
	if err := os.Mkdir(worktree, 0o700); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, time.October, 1, 9, 0, 0, 0, time.UTC)
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(operation.Path, "redirected-owner")); err != nil {
		t.Fatal(err)
	}
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, "redirected-owner", "app", worktree, now); err == nil {
		t.Fatal("symlinked owner admitted")
	}
	if entries, err := os.ReadDir(outside); err != nil || len(entries) != 0 {
		t.Fatalf("owner symlink wrote outside held task: %v, %v", entries, err)
	}
	wrongRoot := filepath.Join(t.TempDir(), "different-task")
	if _, err := createAdoptionRegistration(operation.Directory, wrongRoot, "acme", "app", worktree, now); err == nil || !strings.Contains(err.Error(), "owner path changed") {
		t.Fatalf("unbound lexical owner path: %v", err)
	}
	ownerPath := filepath.Join(operation.Path, "acme")
	if err := os.Symlink(outside, filepath.Join(ownerPath, "redirected-repo")); err != nil {
		t.Fatal(err)
	}
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "redirected-repo", worktree, now); err == nil {
		t.Fatal("symlinked repository registration admitted")
	}
	badPointerDir := filepath.Join(ownerPath, "directory-pointer")
	if err := os.MkdirAll(filepath.Join(badPointerDir, adoptedWorktreePointerName), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "directory-pointer", worktree, now); err == nil || !strings.Contains(err.Error(), "inspect existing adoption registration") {
		t.Fatalf("unreadable pointer record admitted: %v", err)
	}
	readOnly := filepath.Join(ownerPath, "read-only")
	if err := os.Mkdir(readOnly, 0o500); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(readOnly, 0o700) })
	if _, err := createAdoptionRegistration(operation.Directory, operation.Path, "acme", "read-only", worktree, now); err == nil || !strings.Contains(err.Error(), "write adoption registration") {
		t.Fatalf("read-only registration accepted: %v", err)
	}
}

//nolint:paralleltest // newGitFixture sets process-wide Git environment.
func TestE2EAdoptionApplyRechecksClaimUnderTaskLock(t *testing.T) {
	fixture := newGitFixture(t)
	path := fixture.externalWorktree(t, "feature/adoption-locked-recheck")
	manifest, err := PreviewReconstructedManifest(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	badHome := filepath.Join(t.TempDir(), "file-home")
	if err := os.WriteFile(badHome, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptApply(badHome, "acme", "app", manifest, path, "operator", time.Now().UTC()); err == nil {
		t.Fatal("non-directory WB home admitted")
	}
	operation, err := prepareOperationRoot(fixture.home, manifest.EffortID, nil)
	if err != nil {
		t.Fatal(err)
	}
	operationPath := operation.Path
	operation.close()
	lockPath := filepath.Join(operationPath, ".lock")
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := adoptApply(fixture.home, "acme", "app", manifest, path, "operator", time.Now().UTC()); err == nil {
		t.Fatal("blocked task lock admitted")
	}
	if err := os.Remove(lockPath); err != nil {
		t.Fatal(err)
	}
	projectionDir := filepath.Join(path, workLogProjectionDirectory)
	if err := os.MkdirAll(projectionDir, 0o700); err != nil {
		t.Fatal(err)
	}
	projectionPath := filepath.Join(projectionDir, workLogProjectionName)
	if err := os.WriteFile(projectionPath, []byte("{broken"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := adoptApply(fixture.home, "acme", "app", manifest, path, "operator", time.Now().UTC()); err == nil || !strings.Contains(err.Error(), "existing Work Log state is inconsistent") {
		t.Fatalf("malformed claim under task lock: %v", err)
	}
	if err := os.Remove(projectionPath); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(operationPath, "acme")); err != nil {
		t.Fatal(err)
	}
	if err := adoptApply(fixture.home, "acme", "app", manifest, path, "operator", time.Now().UTC()); err == nil {
		t.Fatal("redirected registration owner admitted")
	}
	if claimed, err := adoptionClaimState(fixture.home, path); claimed || err != nil {
		t.Fatalf("refusals published a claim: claimed=%t err=%v", claimed, err)
	}
}
