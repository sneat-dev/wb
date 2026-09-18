package canonicalrescue

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/testenv"
)

// lgCovRequireEffectivePermissions skips a fixture whose whole point is that a
// chmod denies a write on platforms where it does not.
func lgCovRequireEffectivePermissions(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("chmod does not deny writes on Windows")
	}
	if os.Geteuid() == 0 {
		t.Skip("running as root, so a chmod denial does not take effect")
	}
}

// lgCovChmodTree rewrites every mode under root, so a fixture that denied
// writes can always be restored before t.TempDir cleans up.
func lgCovChmodTree(t *testing.T, root string, dirMode, fileMode os.FileMode) {
	t.Helper()
	_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if entry.IsDir() {
			_ = os.Chmod(path, dirMode)
			return nil
		}
		_ = os.Chmod(path, fileMode)
		return nil
	})
}

// lgCovDenyObjectWrites removes write permission from a clone's object
// database, which is what turns a stage or a write-tree into a real failure,
// and restores it afterwards.
func lgCovDenyObjectWrites(t *testing.T, canonical string) {
	t.Helper()
	objects := filepath.Join(canonical, ".git", "objects")
	lgCovChmodTree(t, objects, 0o555, 0o444)
	t.Cleanup(func() { lgCovChmodTree(t, objects, 0o755, 0o644) })
}

// lgCovBranchOptions names a rescue branch without touching the clock.
func lgCovBranchOptions(repositories fixture, branch string) Options {
	return Options{ProjectsRoot: repositories.ProjectsRoot, Branch: branch}
}

// lgCovPushInput is a well-formed pre-push ref update for branch at commit:
// "<local ref> <local sha> <remote ref> <remote sha>".
func lgCovPushInput(branch, commit string) string {
	ref := "refs/heads/" + branch
	return ref + " " + commit + " " + ref + " 0000000000000000000000000000000000000000\n"
}

// lgCovFailingReader stands in for a pre-push stream that cannot be read.
type lgCovFailingReader struct{}

func (lgCovFailingReader) Read([]byte) (int, error) {
	return 0, errors.New("lgCov: the pre-push stream could not be read")
}

// lgCovBareRepo creates a bare repository that can act as a remote.
func lgCovBareRepo(t *testing.T, path string) {
	t.Helper()
	run(t, filepath.Dir(path), "git", "init", "-q", "--bare", path)
}

// TestLgCovInspectSeesACleanCloneAndDerivesItsBranchName pins both halves of
// the branch-name rule: an empty Branch is derived from the clock, and a
// caller-supplied clock is what the derived name is built from.
func TestLgCovInspectSeesACleanCloneAndDerivesItsBranchName(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	ctx := context.Background()

	fixed := time.Date(2026, 8, 27, 15, 4, 5, 0, time.UTC)
	report, err := Inspect(ctx, repositories.Canonical, Options{
		ProjectsRoot: repositories.ProjectsRoot,
		Now:          func() time.Time { return fixed },
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if report.Dirty() {
		t.Fatalf("a clean clone reported work to rescue: %v", report.Changes)
	}
	if report.UntrackedCount != 0 {
		t.Fatalf("untracked count = %d on a clean clone, want 0", report.UntrackedCount)
	}
	if report.Repository != "sneat-co/backstage" {
		t.Fatalf("repository = %q, want sneat-co/backstage", report.Repository)
	}
	if report.Branch != "main" {
		t.Fatalf("branch = %q, want main", report.Branch)
	}
	if report.Head == "" {
		t.Fatal("a clone with a commit reported no HEAD")
	}
	if want := "rescue/canonical-20260827-150405"; report.RescueBranch != want {
		t.Fatalf("derived branch = %q, want %q", report.RescueBranch, want)
	}

	// A whitespace-only Branch is as empty as a missing one, and with no
	// supplied clock the name comes from the real one.
	derived, err := Inspect(ctx, repositories.Canonical, Options{
		ProjectsRoot: repositories.ProjectsRoot,
		Branch:       "   ",
	})
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if !strings.HasPrefix(derived.RescueBranch, "rescue/canonical-") {
		t.Fatalf("derived branch = %q, want a rescue/canonical-<timestamp> name", derived.RescueBranch)
	}
	if len(derived.RescueBranch) != len("rescue/canonical-20060102-150405") {
		t.Fatalf("derived branch = %q, want a 15-character timestamp", derived.RescueBranch)
	}
}

// TestLgCovInspectRefusesACanonicalPathThatIsNotAGitRepository covers the
// managed path that only looks like a clone: agentguard classifies it as
// canonical from the .git directory alone, and Git then refuses it.
func TestLgCovInspectRefusesACanonicalPathThatIsNotAGitRepository(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	broken := filepath.Join(projectsRoot, "sneat-co", "broken")
	if err := os.MkdirAll(filepath.Join(broken, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(context.Background(), broken, Options{ProjectsRoot: projectsRoot}); err == nil {
		t.Fatal("a directory that is not a Git repository was inspected as a clone")
	}
}

// TestLgCovInspectRefusesACloneWhoseIndexIsUnreadable is the failure the
// rescue must report rather than mistake for "nothing to rescue": Git can
// still name the branch and HEAD, but cannot say what the clone holds.
func TestLgCovInspectRefusesACloneWhoseIndexIsUnreadable(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	index := filepath.Join(repositories.Canonical, ".git", "index")
	if err := os.WriteFile(index, []byte("not a git index"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err == nil {
		t.Fatal("a clone with an unreadable index reported a clean status")
	}
	if !strings.Contains(err.Error(), "status") {
		t.Fatalf("the failure does not name the status read: %v", err)
	}
}

// TestLgCovCaptureRefusesNothingAndAnUnrelatedDirectory checks the two ways a
// capture is refused before a temporary index is even attempted.
func TestLgCovCaptureRefusesNothingAndAnUnrelatedDirectory(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	ctx := context.Background()

	clean, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(ctx, clean); err == nil {
		t.Fatal("a clean clone was captured")
	} else if !strings.Contains(err.Error(), "nothing to rescue") {
		t.Fatalf("the refusal does not explain there is nothing to rescue: %v", err)
	}

	notARepository := t.TempDir()
	_, err = Capture(ctx, Report{
		Path:         notARepository,
		RescueBranch: "rescue/nowhere",
		Head:         "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Changes:      []Change{{Status: "??", Path: "lesson.md"}},
	})
	if err == nil {
		t.Fatal("a capture ran in a directory that is not a Git repository")
	}
}

// TestLgCovCaptureRefusesACloneWithNoCommitToBuildOn covers the unborn clone:
// there is no index to copy and no HEAD to read a tree from, so the capture
// must fail rather than record an incomplete rescue.
func TestLgCovCaptureRefusesACloneWithNoCommitToBuildOn(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	canonical := filepath.Join(projectsRoot, "sneat-co", "backstage")
	if err := os.MkdirAll(canonical, 0o755); err != nil {
		t.Fatal(err)
	}
	run(t, canonical, "git", "init", "-q")
	_, err := Capture(context.Background(), Report{
		Path:           canonical,
		RescueBranch:   "rescue/unborn",
		Head:           "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef",
		Changes:        []Change{{Status: "??", Path: "lesson.md"}},
		UntrackedCount: 1,
	})
	if err == nil {
		t.Fatal("a clone with no commit was captured")
	}
	if !strings.Contains(err.Error(), "temporary index") {
		t.Fatalf("the failure does not name the temporary index: %v", err)
	}
}

// TestLgCovCaptureFailsWhenNoTemporaryDirectoryExists covers the very first
// step of a capture: with nowhere to stage a scratch index, the rescue must
// fail loudly rather than fall back to the clone's real index.
func TestLgCovCaptureFailsWhenNoTemporaryDirectoryExists(t *testing.T) {
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	// Every name the standard library consults for a temporary directory, so
	// the fixture denies one on each platform.
	t.Setenv("TMPDIR", missing)
	t.Setenv("TMP", missing)
	t.Setenv("TEMP", missing)

	_, err = Capture(ctx, report)
	if err == nil {
		t.Fatal("a capture succeeded with no temporary directory to stage an index in")
	}
	if !strings.Contains(err.Error(), "stage a temporary index") {
		t.Fatalf("the failure does not name the temporary index: %v", err)
	}
}

// TestLgCovCaptureFailsWhenTheContentCannotBeStaged is the first half of the
// capture's own failure contract: when staging the clone's content into the
// scratch index fails, the rescue reports it instead of committing a partial tree.
func TestLgCovCaptureFailsWhenTheContentCannotBeStaged(t *testing.T) {
	t.Parallel()
	lgCovRequireEffectivePermissions(t)
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	lgCovDenyObjectWrites(t, repositories.Canonical)
	_, err = Capture(ctx, report)
	if err == nil {
		t.Fatal("a capture succeeded while its content could not be staged")
	}
	if !strings.Contains(err.Error(), "temporary index") {
		t.Fatalf("the failure does not name the temporary index: %v", err)
	}
}

// TestLgCovCaptureFailsWhenTheTreeCannotBeWritten is the second half: staging
// a deletion needs no new object, but writing the resulting tree does, and a
// tree that cannot be written must not become a rescue commit.
func TestLgCovCaptureFailsWhenTheTreeCannotBeWritten(t *testing.T) {
	t.Parallel()
	lgCovRequireEffectivePermissions(t)
	repositories := newFixture(t)
	// A single tracked deletion: staging it writes no object, so the failure
	// lands exactly on write-tree.
	if err := os.Remove(filepath.Join(repositories.Canonical, "README.md")); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	if !report.Dirty() {
		t.Fatal("the deletion was not reported as work to rescue")
	}
	lgCovDenyObjectWrites(t, repositories.Canonical)
	_, err = Capture(ctx, report)
	if err == nil {
		t.Fatal("a capture succeeded while its tree could not be written")
	}
	if !strings.Contains(err.Error(), "write a tree") {
		t.Fatalf("the failure does not name the tree write: %v", err)
	}
}

// TestLgCovCaptureRefusesAnExistingBranchWhoseTreeCannotBeRead keeps the
// reuse path honest: when the named branch cannot be read back, the capture
// refuses rather than assuming it already holds this content.
func TestLgCovCaptureRefusesAnExistingBranchWhoseTreeCannotBeRead(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	gitIn(t, repositories.Canonical, "branch", "rescue/one", "HEAD")
	gitIn(t, repositories.Canonical, "branch", "rescue/two", "HEAD")
	ctx := context.Background()
	// The glob matches two branches, so the reported "existing" commit is not
	// one object and its tree cannot be read.
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/*"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Capture(ctx, report)
	if err == nil {
		t.Fatal("a capture proceeded against an unreadable existing branch")
	}
	if !strings.Contains(err.Error(), "read the tree of branch") {
		t.Fatalf("the failure does not name the branch tree read: %v", err)
	}
}

// TestLgCovCaptureFailsWhenTheRescueCommitCannotBeRecorded covers the parent
// the clone no longer has: commit-tree refuses, and no branch is created.
func TestLgCovCaptureFailsWhenTheRescueCommitCannotBeRecorded(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/headless"))
	if err != nil {
		t.Fatal(err)
	}
	report.Head = "0000000000000000000000000000000000000000"
	if _, err := Capture(ctx, report); err == nil {
		t.Fatal("a capture recorded a commit parented on a commit that does not exist")
	} else if !strings.Contains(err.Error(), "record the rescue commit") {
		t.Fatalf("the failure does not name the rescue commit: %v", err)
	}
}

// TestLgCovCaptureRefusesABranchNameGitRejects covers the last write of the
// capture: a name Git will not accept must not leave a rescue commit behind
// pretending to be recoverable.
func TestLgCovCaptureRefusesABranchNameGitRejects(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/bad name"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(ctx, report); err == nil {
		t.Fatal("a capture created a branch with an invalid name")
	} else if !strings.Contains(err.Error(), "create branch") {
		t.Fatalf("the failure does not name the branch creation: %v", err)
	}
}

// TestLgCovCaptureAndPushAreIdempotentAndSeeTheRemoteReceipt is the whole
// two-step flow run twice: the second capture reuses its own branch and learns
// from the remote-tracking ref that the content is already off this machine,
// and the second push is a no-op rather than a second publish.
func TestLgCovCaptureAndPushAreIdempotentAndSeeTheRemoteReceipt(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/twice"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatalf("Capture: %v", err)
	}
	pushed, err := Push(ctx, captured, "origin")
	if err != nil {
		t.Fatalf("Push: %v", err)
	}
	if !pushed.Pushed {
		t.Fatal("the first push was not recorded")
	}

	again, err := Capture(ctx, report)
	if err != nil {
		t.Fatalf("second Capture: %v", err)
	}
	if again.RescueCommit != captured.RescueCommit {
		t.Fatalf("the second capture made a new commit: %s vs %s", again.RescueCommit, captured.RescueCommit)
	}
	if !again.Pushed {
		t.Fatal("the second capture did not observe that origin already holds the rescue commit")
	}

	repushed, err := Push(ctx, again, "origin")
	if err != nil {
		t.Fatalf("second Push: %v", err)
	}
	if !repushed.Pushed {
		t.Fatal("the second push did not report the remote receipt")
	}
}

// TestLgCovPushRequiresACaptureAndAReachableRemote covers the two refusals
// that happen before anything is published.
func TestLgCovPushRequiresACaptureAndAReachableRemote(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	ctx := context.Background()
	if _, err := Push(ctx, Report{Path: repositories.Canonical, RescueBranch: "rescue/x"}, "origin"); err == nil {
		t.Fatal("a push ran with nothing captured")
	} else if !strings.Contains(err.Error(), "nothing has been captured yet") {
		t.Fatalf("the refusal does not explain that nothing was captured: %v", err)
	}

	report := Report{
		Path:         repositories.Canonical,
		RescueBranch: "rescue/x",
		RescueCommit: gitIn(t, repositories.Canonical, "rev-parse", "HEAD"),
	}
	if _, err := Push(ctx, report, "no-such-remote"); err == nil {
		t.Fatal("a push ran against a remote that does not exist")
	} else if !strings.Contains(err.Error(), "refs/heads/rescue/x") {
		t.Fatalf("the failure does not name the ref it could not read: %v", err)
	}
}

// TestLgCovPushRefusesToReplaceADifferentRemoteCommit is the guard against a
// rescue silently overwriting somebody else's branch on the remote.
func TestLgCovPushRefusesToReplaceADifferentRemoteCommit(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/clash"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	// Put a different commit — the clone's own HEAD — at that ref on origin.
	gitIn(t, repositories.Canonical, "push", "-q", "origin", "HEAD:refs/heads/rescue/clash")

	_, err = Push(ctx, captured, "origin")
	if err == nil {
		t.Fatal("a push replaced a remote branch holding different content")
	}
	if !strings.Contains(err.Error(), "refusing to replace") {
		t.Fatalf("the refusal does not explain the replacement: %v", err)
	}
}

// TestLgCovPushReportsAPushFailure makes the push's own failure observable:
// the remote is reachable for the receipt read but refuses the write.
func TestLgCovPushReportsAPushFailure(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/unwritable"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	gitIn(t, repositories.Canonical, "remote", "set-url", "--push", "origin", filepath.Join(t.TempDir(), "missing.git"))

	_, err = Push(ctx, captured, "origin")
	if err == nil {
		t.Fatal("a push succeeded against a remote whose push URL is gone")
	}
	if !strings.Contains(err.Error(), "push rescue/unwritable to origin") {
		t.Fatalf("the failure does not name the push: %v", err)
	}
}

// TestLgCovPushRefusesAPushWithoutExactRemoteReceipt splits the fetch and push
// URLs: the write lands, but the ref read back afterwards is not the rescue
// commit, so the push is not allowed to claim success.
func TestLgCovPushRefusesAPushWithoutExactRemoteReceipt(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/receipt"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	fetch := filepath.Join(t.TempDir(), "fetch.git")
	receive := filepath.Join(t.TempDir(), "receive.git")
	lgCovBareRepo(t, fetch)
	lgCovBareRepo(t, receive)
	gitIn(t, repositories.Canonical, "remote", "add", "split", fetch)
	gitIn(t, repositories.Canonical, "remote", "set-url", "--push", "split", receive)

	_, err = Push(ctx, captured, "split")
	if err == nil {
		t.Fatal("a push reported success without the exact remote receipt")
	}
	if !strings.Contains(err.Error(), "exact remote receipt") {
		t.Fatalf("the failure does not name the receipt: %v", err)
	}
	// The write really did land on the push URL, which is why the missing
	// receipt matters.
	if listing := run(t, receive, "git", "for-each-ref", "--format=%(refname)", "refs/heads/rescue/receipt"); !strings.Contains(listing, "refs/heads/rescue/receipt") {
		t.Fatalf("the push never reached the push URL:\n%s", listing)
	}
}

// TestLgCovPushReportsAnUnreadableRemoteAfterPushing covers the receipt read
// that fails: the branch was accepted, and then the remote could not be asked
// what it holds. A post-receive hook removes the fetch remote, which is the
// only way the same Push call can see a writable remote and an unreadable one.
func TestLgCovPushReportsAnUnreadableRemoteAfterPushing(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the fixture deletes the fetch remote with a shell post-receive hook")
	}
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/hooked"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	fetch := filepath.Join(t.TempDir(), "fetch.git")
	receive := filepath.Join(t.TempDir(), "receive.git")
	lgCovBareRepo(t, fetch)
	lgCovBareRepo(t, receive)
	hook := filepath.Join(receive, "hooks", "post-receive")
	script := "#!/bin/sh\nrm -rf '" + fetch + "'\n"
	if err := os.WriteFile(hook, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn(t, repositories.Canonical, "remote", "add", "hooked", fetch)
	gitIn(t, repositories.Canonical, "remote", "set-url", "--push", "hooked", receive)

	_, err = Push(ctx, captured, "hooked")
	if err == nil {
		t.Fatal("a push reported success after the remote became unreadable")
	}
	if !strings.Contains(err.Error(), "from hooked") {
		t.Fatalf("the failure does not name the unreadable remote: %v", err)
	}
	if _, statErr := os.Stat(fetch); statErr == nil {
		t.Fatal("the fixture did not remove the fetch remote, so the failure was not exercised")
	}
}

// TestLgCovPushRejectsAnAmbiguousRemoteRef covers a ref pattern that names
// more than one remote branch: without exactly one receipt the push must not
// guess which commit it published.
func TestLgCovPushRejectsAnAmbiguousRemoteRef(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	gitIn(t, repositories.Canonical, "push", "-q", "origin", "HEAD:refs/heads/rescue/one")
	gitIn(t, repositories.Canonical, "push", "-q", "origin", "HEAD:refs/heads/rescue/two")
	report := Report{
		Path:         repositories.Canonical,
		RescueBranch: "rescue/*",
		RescueCommit: gitIn(t, repositories.Canonical, "rev-parse", "HEAD"),
	}
	if _, err := Push(context.Background(), report, "origin"); err == nil {
		t.Fatal("a push accepted an ambiguous remote ref")
	} else if !strings.Contains(err.Error(), "unexpected ls-remote receipt") {
		t.Fatalf("the failure does not name the ambiguous receipt: %v", err)
	}
}

// TestLgCovRestoreRefusesWhenTheCloneCannotReturnToItsHead keeps a rescue from
// cleaning a clone whose recorded HEAD is no longer there.
func TestLgCovRestoreRefusesWhenTheCloneCannotReturnToItsHead(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	captured.Pushed = true
	captured.Head = "0000000000000000000000000000000000000000"
	if _, err := Restore(ctx, captured, false); err == nil {
		t.Fatal("a restore succeeded against a HEAD that does not exist")
	} else if !strings.Contains(err.Error(), "reset") {
		t.Fatalf("the failure does not name the reset: %v", err)
	}
	// The capture is the only copy, so the clone must still be dirty.
	if status := gitIn(t, repositories.Canonical, "status", "--porcelain=v1"); status == "" {
		t.Fatal("a refused restore already cleaned the clone")
	}
}

// TestLgCovRestoreRefusesWhenTheCloneCannotBeCleaned is the destructive step's
// own failure: the content is safely captured, but the working tree cannot be
// returned to the captured base, so the restore reports it rather than
// claiming a clean clone.
func TestLgCovRestoreRefusesWhenTheCloneCannotBeCleaned(t *testing.T) {
	t.Parallel()
	lgCovRequireEffectivePermissions(t)
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	locked := filepath.Join(repositories.Canonical, "locked")
	if err := os.MkdirAll(locked, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(locked, "inside.txt"), "cannot be cleaned\n")
	if err := os.Chmod(locked, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(locked, 0o755) })

	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	captured.Pushed = true
	if _, err := Restore(ctx, captured, false); err == nil {
		t.Fatal("a restore succeeded while the clone could not be cleaned")
	} else if !strings.Contains(err.Error(), "clean") {
		t.Fatalf("the failure does not name the clean: %v", err)
	}
}

// TestLgCovRestoreRefusesACommitItCannotReadBack proves the restore re-reads
// the capture rather than trusting the report: a rescue commit that is not in
// this repository is a refusal, not a clean.
func TestLgCovRestoreRefusesACommitItCannotReadBack(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	report, err := Inspect(context.Background(), repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	report.Pushed = true
	report.RescueCommit = "deadbeefdeadbeefdeadbeefdeadbeefdeadbeef"
	if _, err := Restore(context.Background(), report, false); err == nil {
		t.Fatal("a restore ran against a rescue commit it could not read")
	} else if !strings.Contains(err.Error(), "read back the rescue commit") {
		t.Fatalf("the failure does not name the read-back: %v", err)
	}
	if status := gitIn(t, repositories.Canonical, "status", "--porcelain=v1"); status == "" {
		t.Fatal("a refused restore still cleaned the clone")
	}
}

// TestLgCovRestoreTreatsDeletionsAndDirectoriesAsCaptured covers the two paths
// the completeness check must not flag: a deletion is captured by being
// absent, and a directory entry stands for the files inside it.
func TestLgCovRestoreTreatsDeletionsAndDirectoriesAsCaptured(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, options(repositories))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	// Neither of these paths exists in the rescue commit, and neither should
	// make the completeness check refuse.
	extra := append([]Change(nil), captured.Changes...)
	extra = append(extra,
		Change{Status: "D ", Path: "gone-staged.md"},
		Change{Status: " D", Path: "gone-unstaged.md"},
		Change{Status: "??", Path: "spec/"},
	)
	captured.Changes = extra
	captured.Pushed = true
	restored, err := Restore(ctx, captured, false)
	if err != nil {
		t.Fatalf("Restore refused a deletion or a directory entry: %v", err)
	}
	if !restored.Restored {
		t.Fatal("the restore was not recorded")
	}
	if status := gitIn(t, repositories.Canonical, "status", "--porcelain=v1"); status != "" {
		t.Fatalf("the clone is still dirty:\n%s", status)
	}
}

// TestLgCovVerifyAttestedPushAcceptsTheExactCapture is the happy path of the
// managed-hook route: the attestation names the branch and commit the capture
// actually produced, and the pre-push stream publishes only that ref.
func TestLgCovVerifyAttestedPushAcceptsTheExactCapture(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/attested"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	// Blank lines in the pre-push stream are noise, not extra refs.
	input := "\n" + lgCovPushInput("rescue/attested", captured.RescueCommit) + "\n"
	if err := VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, "rescue/attested", captured.RescueCommit, strings.NewReader(input)); err != nil {
		t.Fatalf("the exact capture was refused: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesMalformedAttestations is the attestation's
// own input contract, checked against a real capture so every refusal is the
// rule under test rather than a missing fixture.
func TestLgCovVerifyAttestedPushRefusesMalformedAttestations(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/attested"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	commit := captured.RescueCommit
	valid := lgCovPushInput("rescue/attested", commit)

	testCases := []struct {
		name    string
		branch  string
		commit  string
		input   string
		wantErr string
	}{
		{"branch without the rescue prefix", "main", commit, lgCovPushInput("main", commit), "invalid canonical rescue push attestation"},
		{"empty commit", "rescue/attested", "", valid, "invalid canonical rescue push attestation"},
		{"branch Git rejects", "rescue/bad name", commit, valid, "invalid canonical rescue branch"},
		{"no refs at all", "rescue/attested", commit, "", "exactly one ref"},
		{"two refs", "rescue/attested", commit, valid + valid, "exactly one ref"},
		{"the wrong ref", "rescue/attested", commit, "refs/heads/other " + commit + " refs/heads/other 0000000000000000000000000000000000000000\n", "must publish only"},
		{"too few fields", "rescue/attested", commit, "refs/heads/rescue/attested " + commit + "\n", "must publish only"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			err := VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, testCase.branch, testCase.commit, strings.NewReader(testCase.input))
			if err == nil {
				t.Fatalf("%s was accepted", testCase.name)
			}
			if !strings.Contains(err.Error(), testCase.wantErr) {
				t.Fatalf("error = %v, want it to mention %q", err, testCase.wantErr)
			}
		})
	}

	if err := VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, "rescue/attested", commit, lgCovFailingReader{}); err == nil {
		t.Fatal("an unreadable pre-push stream was accepted")
	} else if !strings.Contains(err.Error(), "read canonical rescue pre-push refs") {
		t.Fatalf("the failure does not name the pre-push read: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesANonCanonicalRoot keeps the managed-hook
// route pointed at the clone rescue exists for.
func TestLgCovVerifyAttestedPushRefusesANonCanonicalRoot(t *testing.T) {
	t.Parallel()
	elsewhere := t.TempDir()
	run(t, elsewhere, "git", "init", "-q")
	run(t, elsewhere, "git", "config", "user.email", "rescue@example.test")
	run(t, elsewhere, "git", "config", "user.name", "rescue")
	write(t, filepath.Join(elsewhere, "README.md"), "elsewhere\n")
	run(t, elsewhere, "git", "add", "-A")
	run(t, elsewhere, "git", "commit", "-qm", "init")
	commit := run(t, elsewhere, "git", "rev-parse", "HEAD")

	err := VerifyAttestedPush(context.Background(), elsewhere, t.TempDir(), "rescue/foreign", commit, strings.NewReader(lgCovPushInput("rescue/foreign", commit)))
	if err == nil {
		t.Fatal("an attestation was accepted for a path that is not a canonical clone")
	}
	if !strings.Contains(err.Error(), "not a canonical clone") {
		t.Fatalf("the failure does not explain the path: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesACleanCanonicalClone is the attestation's
// reason for existing: it proves the rescue preserved a dirty clone, so a
// clean clone has nothing this route may publish.
func TestLgCovVerifyAttestedPushRefusesACleanCanonicalClone(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	commit := gitIn(t, repositories.Canonical, "rev-parse", "HEAD")
	err := VerifyAttestedPush(context.Background(), repositories.Canonical, repositories.ProjectsRoot, "rescue/clean", commit, strings.NewReader(lgCovPushInput("rescue/clean", commit)))
	if err == nil {
		t.Fatal("an attestation was accepted for a clean canonical clone")
	}
	if !strings.Contains(err.Error(), "requires the dirty clone") {
		t.Fatalf("the failure does not explain the missing dirty clone: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesAMismatchedCommit binds the attestation to
// the branch: naming a different commit than the branch holds is a refusal.
func TestLgCovVerifyAttestedPushRefusesAMismatchedCommit(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/one"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	other := "rescue/two"
	gitIn(t, repositories.Canonical, "branch", other, "HEAD")

	err = VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, other, captured.RescueCommit, strings.NewReader(lgCovPushInput(other, captured.RescueCommit)))
	if err == nil {
		t.Fatal("an attestation naming a commit the branch does not hold was accepted")
	}
	if !strings.Contains(err.Error(), "attestation names") {
		t.Fatalf("the failure does not name the mismatch: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesABranchPointingAtAMissingObject covers a
// ref that was written without its object: the branch read succeeds, so the
// refusal has to come from reading the commit itself.
func TestLgCovVerifyAttestedPushRefusesABranchPointingAtAMissingObject(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	const bogus = "1111111111111111111111111111111111111111"
	refFile := filepath.Join(repositories.Canonical, ".git", "refs", "heads", "rescue", "bogus")
	if err := os.MkdirAll(filepath.Dir(refFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(refFile, []byte(bogus+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := VerifyAttestedPush(context.Background(), repositories.Canonical, repositories.ProjectsRoot, "rescue/bogus", bogus, strings.NewReader(lgCovPushInput("rescue/bogus", bogus)))
	if err == nil {
		t.Fatal("an attestation for a branch whose commit object does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "bad object") {
		t.Fatalf("the failure does not name the missing object: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesAWrongParent proves the commit must be a
// capture of the clone's own HEAD and not merely a commit that exists.
func TestLgCovVerifyAttestedPushRefusesAWrongParent(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/attested"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	tree := gitIn(t, repositories.Canonical, "rev-parse", captured.RescueCommit+"^{tree}")
	ancestor := gitIn(t, repositories.Canonical, "rev-parse", "HEAD~1")
	wrong := gitIn(t, repositories.Canonical, "commit-tree", tree, "-p", ancestor, "-m", "captured from the wrong parent")
	gitIn(t, repositories.Canonical, "branch", "rescue/wrongparent", wrong)

	err = VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, "rescue/wrongparent", wrong, strings.NewReader(lgCovPushInput("rescue/wrongparent", wrong)))
	if err == nil {
		t.Fatal("an attestation for a commit with the wrong parent was accepted")
	}
	if !strings.Contains(err.Error(), "single-parent capture") {
		t.Fatalf("the failure does not name the parent: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesADifferentCapturedTree proves the tree is
// checked, not just the shape of the commit: a branch holding the clean tree
// is not a capture of the dirty clone.
func TestLgCovVerifyAttestedPushRefusesADifferentCapturedTree(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/attested"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := Capture(ctx, report); err != nil {
		t.Fatal(err)
	}
	cleanTree := gitIn(t, repositories.Canonical, "rev-parse", "HEAD^{tree}")
	clean := gitIn(t, repositories.Canonical, "commit-tree", cleanTree, "-p", report.Head, "-m", "a single-parent commit that captured nothing")
	gitIn(t, repositories.Canonical, "branch", "rescue/empty", clean)

	err = VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, "rescue/empty", clean, strings.NewReader(lgCovPushInput("rescue/empty", clean)))
	if err == nil {
		t.Fatal("an attestation for a commit that captured the clean tree was accepted")
	}
	if !strings.Contains(err.Error(), "does not equal the clone's complete captured tree") {
		t.Fatalf("the failure does not name the tree comparison: %v", err)
	}
}

// TestLgCovVerifyAttestedPushRefusesWhenTheCaptureCannotBeRebuilt is the last
// wall before the push is allowed: if the pre-push hook cannot re-capture the
// clone's complete dirty state, nothing may be published under this route.
func TestLgCovVerifyAttestedPushRefusesWhenTheCaptureCannotBeRebuilt(t *testing.T) {
	t.Parallel()
	lgCovRequireEffectivePermissions(t)
	repositories := newFixture(t)
	dirtyTheClone(t, repositories)
	ctx := context.Background()
	report, err := Inspect(ctx, repositories.Canonical, lgCovBranchOptions(repositories, "rescue/attested"))
	if err != nil {
		t.Fatal(err)
	}
	captured, err := Capture(ctx, report)
	if err != nil {
		t.Fatal(err)
	}
	// A fresh edit means the re-capture must write a new blob, which a
	// read-only object database cannot do.
	write(t, filepath.Join(repositories.Canonical, "README.md"), "an edit the object database cannot record\n")
	lgCovDenyObjectWrites(t, repositories.Canonical)

	err = VerifyAttestedPush(ctx, repositories.Canonical, repositories.ProjectsRoot, "rescue/attested", captured.RescueCommit, strings.NewReader(lgCovPushInput("rescue/attested", captured.RescueCommit)))
	if err == nil {
		t.Fatal("an attestation was accepted while the clone could not be re-captured")
	}
	if !strings.Contains(err.Error(), "temporary index") {
		t.Fatalf("the failure does not name the temporary index: %v", err)
	}
}

// TestLgCovPushAttestationFromEnvironment covers the transport's handoff: the
// hook reads which branch and commit WB attests to, and a half-set pair is
// reported rather than silently half-accepted.
func TestLgCovPushAttestationFromEnvironment(t *testing.T) {
	testenv.Isolate(t)
	t.Setenv(PushBranchEnv, "")
	t.Setenv(PushCommitEnv, "")

	branch, commit, present, err := PushAttestationFromEnvironment()
	if err != nil || present || branch != "" || commit != "" {
		t.Fatalf("with no attestation: branch=%q commit=%q present=%v err=%v, want all empty and no error", branch, commit, present, err)
	}

	t.Setenv(PushBranchEnv, "rescue/env")
	if _, _, present, err = PushAttestationFromEnvironment(); !present || err == nil {
		t.Fatalf("with only the branch set: present=%v err=%v, want present and an incomplete-attestation error", present, err)
	} else if !strings.Contains(err.Error(), "incomplete") {
		t.Fatalf("the failure does not explain the incomplete attestation: %v", err)
	}

	t.Setenv(PushBranchEnv, "")
	t.Setenv(PushCommitEnv, "0123456789abcdef0123456789abcdef01234567")
	if _, _, present, err = PushAttestationFromEnvironment(); !present || err == nil {
		t.Fatalf("with only the commit set: present=%v err=%v, want present and an incomplete-attestation error", present, err)
	}

	t.Setenv(PushBranchEnv, "rescue/env")
	branch, commit, present, err = PushAttestationFromEnvironment()
	if err != nil || !present {
		t.Fatalf("with both set: present=%v err=%v, want a complete attestation", present, err)
	}
	if branch != "rescue/env" || commit != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("attestation = %q/%q, want the environment's values", branch, commit)
	}
}

// TestLgCovShortSHAAndRescueMessageBoundTheirOutput pins the truncation rules
// the rescue commit message depends on: a long SHA is shortened, a short one
// is left alone, and a long change list is summarised rather than dumped.
func TestLgCovShortSHAAndRescueMessageBoundTheirOutput(t *testing.T) {
	t.Parallel()
	if got := shortSHA("0123456789abcdef0123"); got != "0123456789ab" {
		t.Fatalf("shortSHA of a long SHA = %q, want its first 12 characters", got)
	}
	if got := shortSHA("abc123"); got != "abc123" {
		t.Fatalf("shortSHA of a short SHA = %q, want it unchanged", got)
	}

	changes := make([]Change, 0, 42)
	for index := range 42 {
		changes = append(changes, Change{Status: "??", Path: "path-" + string(rune('a'+index%26)) + ".md"})
	}
	message := rescueMessage(Report{
		Path:           "/projects/sneat-co/backstage",
		Branch:         "main",
		Head:           "0123456789abcdef0123456789abcdef01234567",
		Changes:        changes,
		UntrackedCount: 42,
	})
	if !strings.Contains(message, "42 path(s), 42 of them untracked:") {
		t.Fatalf("the message does not state what was captured:\n%s", message)
	}
	if !strings.Contains(message, "… and 2 more") {
		t.Fatalf("the message does not summarise the tail of a long change list:\n%s", message)
	}
	if strings.Contains(message, "path-") && strings.Count(message, "?? ") != 40 {
		t.Fatalf("the message did not stop at 40 paths:\n%s", message)
	}
	if !strings.Contains(message, "0123456789ab") {
		t.Fatalf("the message does not carry the short HEAD:\n%s", message)
	}
}

// TestLgCovCopyFileReportsAnUnreadableSource covers the copy that must report
// a missing source rather than write an empty index.
func TestLgCovCopyFileReportsAnUnreadableSource(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	err := copyFile(filepath.Join(directory, "missing"), filepath.Join(directory, "destination"))
	if err == nil {
		t.Fatal("copying a missing file succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(directory, "destination")); statErr == nil {
		t.Fatal("a failed copy still created a destination file")
	}
}

// TestLgCovGitRawReportsAFailureOutsideARepository covers the raw read that
// must return Git's failure rather than an empty status.
func TestLgCovGitRawReportsAFailureOutsideARepository(t *testing.T) {
	t.Parallel()
	if _, err := gitRaw(context.Background(), t.TempDir(), "status", "--porcelain=v1"); err == nil {
		t.Fatal("reading the status of a directory that is not a repository succeeded")
	}
}
