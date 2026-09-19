package worktrees

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestWTCoreCovDirtyCaptureClassifiesEveryPath asserts the bounded capture
// distinguishes a modified tracked file, an untracked file, a symlink, and a
// file deleted from disk, and that the receipt accounts for each of them.
func TestWTCoreCovDirtyCaptureClassifiesEveryPath(t *testing.T) {
	ctx := context.Background()
	repository := newJournalWorktree(t)

	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "removed.txt"), []byte("gone\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "tracked.txt", "removed.txt")
	gitTest(t, repository, "commit", "-m", "base")

	if err := os.WriteFile(filepath.Join(repository, "tracked.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repository, "untracked.txt"), []byte("three\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("tracked.txt", filepath.Join(repository, "link")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(repository, "removed.txt")); err != nil {
		t.Fatal(err)
	}

	material, err := collectDirtyCapture(ctx, repository)
	if err != nil {
		t.Fatalf("collectDirtyCapture: %v", err)
	}
	kinds := map[string]string{}
	blobs := map[string]int{}
	for _, entry := range material.Manifest.Entries {
		kinds[entry.Path] = entry.Kind
		blobs[entry.Path] = len(material.Blobs[entry.Blob])
		if entry.Kind != "deleted" && entry.SHA256 == "" {
			t.Fatalf("entry %#v carries no digest", entry)
		}
		if entry.Blob != "" && entry.Blob != dirtyCaptureBlobName(int(entry.Bytes), entry.SHA256) {
			t.Fatalf("entry %#v has an inconsistent blob name", entry)
		}
	}
	want := map[string]string{
		"link":          "symlink",
		"removed.txt":   "deleted",
		"tracked.txt":   "file",
		"untracked.txt": "file",
	}
	if len(kinds) != len(want) {
		t.Fatalf("captured %#v, want %#v", kinds, want)
	}
	for path, kind := range want {
		if kinds[path] != kind {
			t.Fatalf("captured %#v, want %s=%s", kinds, path, kind)
		}
	}
	if got := blobs["link"]; got != len("tracked.txt") {
		t.Fatalf("symlink blob holds %d bytes, want the link target", got)
	}
	if material.Manifest.Receipt.Files != len(want) || material.Manifest.Receipt.Bytes <= 0 || material.Manifest.Receipt.SHA256 == "" {
		t.Fatalf("receipt = %#v", material.Manifest.Receipt)
	}
	// Entries are sorted, so the receipt is deterministic across runs.
	for index := 1; index < len(material.Manifest.Entries); index++ {
		if material.Manifest.Entries[index-1].Path >= material.Manifest.Entries[index].Path {
			t.Fatalf("entries are not sorted: %#v", material.Manifest.Entries)
		}
	}
}

// TestWTCoreCovDirtyCaptureRejectsOversizePath asserts the per-file retention
// bound is enforced before any bytes are read.
func TestWTCoreCovDirtyCaptureRejectsOversizePath(t *testing.T) {
	ctx := context.Background()
	repository := newJournalWorktree(t)
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "seed.txt")
	gitTest(t, repository, "commit", "-m", "base")
	oversize := filepath.Join(repository, "oversize.bin")
	if err := os.WriteFile(oversize, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Truncate(oversize, maxDirtyCaptureFileBytes+1); err != nil {
		t.Fatal(err)
	}
	if _, err := collectDirtyCapture(ctx, repository); err == nil {
		t.Fatal("an oversize dirty file must be refused")
	} else if !strings.Contains(err.Error(), "exceeds bounded") {
		t.Fatalf("error %q does not name the retention bound", err)
	}
	if _, err := dirtyWorktreeEvidence(ctx, repository); err == nil {
		t.Fatal("dirtyWorktreeEvidence must surface the same refusal")
	}
}

// TestWTCoreCovDirtyCaptureRejectsUnsafePaths asserts every rejected path shape
// in dirtyCapturePath is refused and the accepted one is returned unchanged.
func TestWTCoreCovDirtyCaptureRejectsUnsafePaths(t *testing.T) {
	for _, unsafe := range []string{"", ".", "..", "/absolute", "../escape", "a/../b", "./b"} {
		if _, err := dirtyCapturePath(unsafe); err == nil {
			t.Fatalf("unsafe dirty path %q was accepted", unsafe)
		}
	}
	for _, safe := range []string{"file.txt", "dir/file.txt", "dir/sub/file.txt"} {
		got, err := dirtyCapturePath(safe)
		if err != nil {
			t.Fatalf("safe dirty path %q rejected: %v", safe, err)
		}
		if got != safe {
			t.Fatalf("dirtyCapturePath(%q) = %q", safe, got)
		}
	}
}

// TestWTCoreCovDirtyCaptureRejectsInvalidDestination asserts publication never
// begins without a private run directory and a valid claim identity.
func TestWTCoreCovDirtyCaptureRejectsInvalidDestination(t *testing.T) {
	t.Parallel()
	material := dirtyCaptureMaterial{Manifest: dirtyCaptureManifest{Version: 1}}
	if _, err := materializeDirtyCapture(nil, "claim", material); err == nil {
		t.Fatal("nil run directory was accepted")
	}
	runDir, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = runDir.Close() })
	if _, err := materializeDirtyCapture(runDir, "not a claim id", material); err == nil {
		t.Fatal("invalid claim id was accepted")
	}
}

// TestWTCoreCovDirtyCaptureMatchesRequiresADigest asserts a capture with no
// expectation never matches, and that a changed receipt produces the
// diagnostic naming both sides.
func TestWTCoreCovDirtyCaptureMatchesRequiresADigest(t *testing.T) {
	t.Parallel()
	actual := DirtyWorktreeEvidence{SHA256: "aa", Bytes: 1, Files: 1}
	if dirtyCaptureMatches(DirtyWorktreeEvidence{}, actual) {
		t.Fatal("an empty expectation must never match")
	}
	if dirtyCaptureMatches(actual, actual) == false {
		t.Fatal("an identical receipt must match")
	}
	changed := actual
	changed.Bytes = 2
	err := dirtyCaptureChangedError(actual, changed)
	for _, want := range []string{"expected sha256=aa bytes=1 files=1", "observed sha256=aa bytes=2 files=1"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

// TestWTCoreCovCaptureAndPersistRefusesChangedBytesAndMissingClaim asserts the
// two pre-publication refusals: bytes that moved after evidence was captured,
// and a worktree with no private Work Log claim to write into.
func TestWTCoreCovCaptureAndPersistRefusesChangedBytesAndMissingClaim(t *testing.T) {
	ctx := context.Background()
	repository := newJournalWorktree(t)
	if err := os.WriteFile(filepath.Join(repository, "seed.txt"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitTest(t, repository, "add", "seed.txt")
	gitTest(t, repository, "commit", "-m", "base")
	if err := os.WriteFile(filepath.Join(repository, "dirty.txt"), []byte("first\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	home := t.TempDir()
	expected := DirtyWorktreeEvidence{SHA256: "0000000000000000000000000000000000000000000000000000000000000000", Bytes: 1, Files: 1}
	if _, err := captureAndPersistDirtyWorktree(ctx, home, repository, &expected); err == nil {
		t.Fatal("changed dirty bytes were accepted")
	} else if !strings.Contains(err.Error(), "changed after evidence capture") {
		t.Fatalf("error %q does not name the change", err)
	}
	if _, err := captureAndPersistDirtyWorktree(ctx, home, repository, nil); err == nil {
		t.Fatal("a worktree without a private Work Log claim was accepted")
	} else if !strings.Contains(err.Error(), "without a private Work Log claim") {
		t.Fatalf("error %q does not name the missing claim", err)
	}
}

// TestWTCoreCovCreateWorktreeAtPlacementRefusesBadInputs asserts the descriptor
// anchored creator refuses every input it cannot prove against configuration,
// a physical destination, or an exact branch.
func TestWTCoreCovCreateWorktreeAtPlacementRefusesBadInputs(t *testing.T) {
	ctx := context.Background()
	fixture := newGitFixture(t)
	base := gitTestOutput(t, fixture.canonical, "rev-parse", "origin/main")
	placement, err := ResolveWorktreePlacement(ctx, fixture.projectsRoot, fixture.canonical, base)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWorktreeAtPlacement(ctx, fixture.projectsRoot, filepath.Join(t.TempDir(), "absent"), placement, "task", "acme/app", "wb/task", "main", base); err == nil {
		t.Fatal("an absent canonical repository was accepted")
	}
	mismatched := placement
	mismatched.Root = filepath.Join(t.TempDir(), "elsewhere")
	if _, err := CreateWorktreeAtPlacement(ctx, fixture.projectsRoot, fixture.canonical, mismatched, "task", "acme/app", "wb/task", "main", base); err == nil {
		t.Fatal("a placement that does not match configured policy was accepted")
	} else if !strings.Contains(err.Error(), "does not match the configured policy") {
		t.Fatalf("error %q does not name the placement mismatch", err)
	}
	if _, err := CreateWorktreeAtPlacement(ctx, fixture.projectsRoot, fixture.canonical, placement, "task", "no-owner-slug", "wb/task", "main", base); err == nil {
		t.Fatal("an unqualified repository slug was accepted")
	}
	if _, err := CreateWorktreeAtPlacement(ctx, fixture.projectsRoot, fixture.canonical, placement, "task", "acme/app", "main", "main", base); err == nil {
		t.Fatal("a branch already checked out in the canonical clone was accepted")
	} else if !strings.Contains(err.Error(), "already checked out") {
		t.Fatalf("error %q does not name the occupied branch", err)
	}

	// A destination that already exists must never be clobbered.
	existing := placement
	existingPath, pathErr := existing.Path("occupied-task", "acme/app")
	if pathErr != nil {
		t.Fatal(pathErr)
	}
	if err := os.MkdirAll(existingPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateWorktreeAtPlacement(ctx, fixture.projectsRoot, fixture.canonical, existing, "occupied-task", "acme/app", "wb/occupied", "main", base); err == nil {
		t.Fatal("an existing destination was accepted")
	} else if !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("error %q does not name the existing destination", err)
	}

	var absent *PlacementWorktree
	absent.Close()
}

// TestWTCoreCovActiveClaimSummariesStructuralFailures asserts the read-only
// active-claim walk skips unrecognised entries, fails closed on unsafe
// structures, and sorts multiple live claims deterministically.
func TestWTCoreCovActiveClaimSummariesStructuralFailures(t *testing.T) {
	// ListActiveClaimSummaries reads every wbhome-resolved home, including the
	// retired legacy $HOME/.wb (see BLOCKING #1 in the projects-root-layout
	// migrate review): isolate HOME so "empty home" below observes an
	// actually empty home, not this machine's real fleet claims.
	userHome, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	projectsRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(projectsRoot, ".wb")
	t.Setenv(wbhome.EnvOverride, home)
	t.Setenv(wbhome.EnvMigrationCompat, "")

	// No worklogs directory at all is silence, not an error.
	if listed, err := ListActiveClaimSummaries(projectsRoot, ""); err != nil || len(listed) != 0 {
		t.Fatalf("empty home = %#v, err=%v", listed, err)
	}

	worktrees := []string{}
	for index := 0; index < 3; index++ {
		worktree, evalErr := filepath.EvalSymlinks(t.TempDir())
		if evalErr != nil {
			t.Fatal(evalErr)
		}
		gitTest(t, worktree, "init")
		worktrees = append(worktrees, worktree)
	}
	type wtCoreCovClaimSpec struct {
		effort     string
		run        string
		repository string
		branch     string
	}
	specs := []wtCoreCovClaimSpec{
		{effort: "task-b", run: "run", repository: "acme/zeta", branch: "wb/zeta"},
		{effort: "task-a", run: "run", repository: "acme/app", branch: "wb/app-two"},
		{effort: "task-a", run: "run", repository: "acme/app", branch: "wb/app-one"},
	}
	claimIDs := make([]string, 0, len(specs))
	for index, spec := range specs {
		outcome, recordErr := recordWorkLogWithHooks(home, spec.effort, CreateResult{
			Repository: spec.repository, WorktreeDir: worktrees[index], Branch: spec.branch, Base: "main",
			BaseSHA: strings.Repeat("a", 40),
		}, WorkLogOptions{
			EffortID: spec.effort, RunID: spec.run, AgentID: "codex", Model: "unknown",
		}, workLogPublicationHooks{})
		if recordErr != nil {
			t.Fatalf("record claim %d: %v", index, recordErr)
		}
		claimIDs = append(claimIDs, outcome.claim.ClaimID)
	}

	listed, err := ListActiveClaimSummaries(projectsRoot, "")
	if err != nil {
		t.Fatalf("list active claims: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("active claims = %#v", listed)
	}
	// Sorted by repository, then task, then branch: all three comparator arms.
	wantOrder := []string{"acme/app|task-a|wb/app-one", "acme/app|task-a|wb/app-two", "acme/zeta|task-b|wb/zeta"}
	for index, want := range wantOrder {
		got := listed[index]
		key := got.Repository + "|" + got.Task + "|" + got.Branch
		if key != want {
			t.Fatalf("claim %d = %s, want %s (all %#v)", index, key, want, listed)
		}
	}
	if filtered, filterErr := ListActiveClaimSummaries(projectsRoot, "acme/app"); filterErr != nil || len(filtered) != 2 {
		t.Fatalf("filtered claims = %#v, err=%v", filtered, filterErr)
	}

	// A worklogs root that is a regular file fails closed.
	worklogs := filepath.Join(home, "worklogs")
	backup := filepath.Join(projectsRoot, "worklogs-backup")
	if err := os.Rename(worklogs, backup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(worklogs, []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListActiveClaimSummaries(projectsRoot, ""); err == nil {
		t.Fatal("a non-directory worklogs root was accepted")
	}
	if err := os.Remove(worklogs); err != nil {
		t.Fatal(err)
	}
	// A symlinked worklogs root must not be traversed.
	if err := os.Symlink(backup, worklogs); err != nil {
		t.Fatal(err)
	}
	if _, err := ListActiveClaimSummaries(projectsRoot, ""); err == nil {
		t.Fatal("a symlinked worklogs root was traversed")
	}
	if err := os.Remove(worklogs); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backup, worklogs); err != nil {
		t.Fatal(err)
	}

	// Skippable debris in the same home: a stray file, an effort with no runs,
	// a run with no claims, and unreadable claim entries. None of it changes the
	// live set and none of it aborts the walk.
	if err := os.WriteFile(filepath.Join(worklogs, "not-a-task"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(worklogs, "stray-effort"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(worklogs, "run-without-claims", "runs", "run"), 0o700); err != nil {
		t.Fatal(err)
	}
	malformed := filepath.Join(worklogs, "malformed", "runs", "run", "claims")
	if err := os.MkdirAll(malformed, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, "claim.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(malformed, "ignore.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err = ListActiveClaimSummaries(projectsRoot, "")
	if err != nil {
		t.Fatalf("structural walk: %v", err)
	}
	if len(listed) != 3 {
		t.Fatalf("structural walk changed the live set: %#v", listed)
	}

	// A runs entry that is a regular file fails the whole walk closed.
	runsAsFile := filepath.Join(worklogs, "runs-as-file", "runs")
	if err := os.MkdirAll(filepath.Dir(runsAsFile), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(runsAsFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListActiveClaimSummaries(projectsRoot, ""); err == nil {
		t.Fatal("a runs entry that is a regular file was accepted")
	}
	if err := os.RemoveAll(filepath.Join(worklogs, "runs-as-file")); err != nil {
		t.Fatal(err)
	}

	// A claims entry that is a regular file fails the whole walk closed.
	claimsAsFile := filepath.Join(worklogs, "claims-as-file", "runs", "run")
	if err := os.MkdirAll(claimsAsFile, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(claimsAsFile, "claims"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListActiveClaimSummaries(projectsRoot, ""); err == nil {
		t.Fatal("a claims entry that is a regular file was accepted")
	}
	if err := os.RemoveAll(filepath.Join(worklogs, "claims-as-file")); err != nil {
		t.Fatal(err)
	}

	// A terminals entry that is a regular file fails the whole walk closed.
	validRun := filepath.Join(worklogs, "task-a", "runs", "run")
	terminals := filepath.Join(validRun, "terminals")
	if err := os.MkdirAll(terminals, 0o700); err != nil {
		t.Fatal(err)
	}
	terminalsBackup := filepath.Join(projectsRoot, "terminals-backup")
	if err := os.Rename(terminals, terminalsBackup); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(terminals, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ListActiveClaimSummaries(projectsRoot, ""); err == nil {
		t.Fatal("a terminals entry that is a regular file was accepted")
	}
	if err := os.Remove(terminals); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(terminalsBackup, terminals); err != nil {
		t.Fatal(err)
	}

	// A sealed claim carries a terminal record and is no longer active.
	if err := os.WriteFile(filepath.Join(terminals, claimIDs[1]+".json"), []byte("{}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	listed, err = ListActiveClaimSummaries(projectsRoot, "")
	if err != nil {
		t.Fatalf("sealed walk: %v", err)
	}
	if len(listed) != 2 {
		t.Fatalf("a sealed claim was still reported active: %#v", listed)
	}
}

// TestWTCoreCovRepositoryRegistrationLockBoundaries asserts the lock refuses a
// missing descriptor, fails closed when its lock file is not a regular file,
// and honours an exhausted deadline instead of blocking forever.
func TestWTCoreCovRepositoryRegistrationLockBoundaries(t *testing.T) {
	if _, err := acquireRepositoryRegistrationLock(nil); err == nil {
		t.Fatal("a nil canonical repository was accepted")
	}
	if _, err := acquireRepositoryRegistrationLock(&canonicalRepository{}); err == nil {
		t.Fatal("a canonical repository without a Git descriptor was accepted")
	}

	var absent *repositoryRegistrationLock
	if err := absent.release(); err != nil {
		t.Fatalf("releasing a nil lock = %v", err)
	}
	empty := &repositoryRegistrationLock{}
	if err := empty.release(); err != nil {
		t.Fatalf("releasing an empty lock = %v", err)
	}

	fixture := newGitFixture(t)
	canonical, err := openCanonicalRepository(fixture.canonical)
	if err != nil {
		t.Fatal(err)
	}
	defer canonical.close()
	// A directory where the lock file belongs is not ENOENT, so it is a hard
	// failure rather than the Darwin transient that is retried.
	if err := os.Mkdir(filepath.Join(fixture.canonical, ".git", repositoryRegistrationLockName), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := acquireRepositoryRegistrationLock(canonical); err == nil {
		t.Fatal("a lock path that is a directory was accepted")
	}

	// An exhausted deadline is reported after the lock file itself was opened,
	// instead of waiting the full timeout out.
	originalTimeout := repositoryRegistrationLockTimeout
	repositoryRegistrationLockTimeout = 0
	t.Cleanup(func() { repositoryRegistrationLockTimeout = originalTimeout })
	acquired, err := acquireRepositoryRegistrationLock(canonical)
	if err == nil {
		_ = acquired.release()
		t.Fatal("an exhausted lock deadline was not reported")
	}
	if !strings.Contains(err.Error(), "repository registration lock") {
		t.Fatalf("error %q does not name the lock", err)
	}
}
