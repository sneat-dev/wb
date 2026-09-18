package checkoutmarker

// This file adds behavior-asserting coverage for the placeholder text Render
// emits, the failure contracts of Apply/EnsureExclude/writeFileAtomically, and
// the filesystem-only parsing Describe relies on. Every helper here is
// prefixed tailCov to keep it disjoint from the fixtures in
// checkoutmarker_test.go.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tailCovDescriptor is a worktree descriptor whose marker path is only valid
// when the caller points CheckoutPath at a directory it controls.
func tailCovDescriptor(checkoutPath string) Descriptor {
	return Descriptor{
		Kind:         KindWorktree,
		Writable:     true,
		Repository:   "sneat-dev/wb",
		CheckoutPath: checkoutPath,
		Branch:       "tailcov",
		BaseBranch:   "main",
		Task:         "tailcov",
		GeneratedBy:  "wb v0.0.0-tailcov",
		GeneratedAt:  time.Date(2026, 8, 27, 12, 0, 0, 0, time.UTC),
	}
}

// TestTailCovCanonicalRenderNamesThePlaceholderContract pins the text an agent
// reads when a canonical clone has no repository coordinate: the placeholder
// must appear on the `wb worktree create` remedy line too, or the copy-pasted
// command is nonsense.
func TestTailCovCanonicalRenderNamesThePlaceholderContract(t *testing.T) {
	t.Parallel()
	rendered := Render(Descriptor{
		Kind: KindCanonical, CheckoutPath: "/p/owner/name", CanonicalPath: "/p/owner/name",
		Branch: "main", BaseBranch: "main", GeneratedBy: "wb v1", GeneratedAt: time.Unix(0, 0),
	})
	for _, expected := range []string{
		"is the shared canonical clone of **<owner/repository>**",
		"wb worktree create <task> <owner/repository>",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("canonical marker with no repository is missing %q:\n%s", expected, rendered)
		}
	}
}

// TestTailCovWorktreeRenderNamesThePlaceholderContract pins the same contract
// for a worktree: an unrecorded task and an unknown repository each read as an
// explicit placeholder rather than as empty table cells.
func TestTailCovWorktreeRenderNamesThePlaceholderContract(t *testing.T) {
	t.Parallel()
	rendered := Render(Descriptor{
		Kind: KindWorktree, Writable: true, CheckoutPath: "/w/tailcov",
		Branch: "tailcov", BaseBranch: "main", GeneratedBy: "wb v1", GeneratedAt: time.Unix(0, 0),
	})
	for _, expected := range []string{
		"| task | (not recorded) |",
		"is an isolated linked worktree of **<owner/repository>**",
	} {
		if !strings.Contains(rendered, expected) {
			t.Fatalf("worktree marker with no task/repository is missing %q:\n%s", expected, rendered)
		}
	}
}

// TestTailCovApplyReportsAMarkerItCannotStage pins Apply's failure contract:
// when the checkout directory is gone the marker cannot be written, the error
// names the staging step, and the ignore rule — written first, on purpose —
// is still in place.
func TestTailCovApplyReportsAMarkerItCannotStage(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	excludePath := filepath.Join(root, "info", "exclude")
	descriptor := tailCovDescriptor(filepath.Join(root, "vanished"))

	result, err := Apply(descriptor, excludePath)
	if err == nil {
		t.Fatal("Apply staged a marker under a checkout directory that does not exist")
	}
	if !strings.Contains(err.Error(), "stage a replacement") {
		t.Fatalf("error %q does not name the failed staging step", err)
	}
	if result.MarkerWritten {
		t.Fatalf("Apply reported a written marker after failing: %+v", result)
	}
	if !result.ExcludeWritten {
		t.Fatalf("Apply did not write the ignore rule before the marker failed: %+v", result)
	}
	contents, readErr := os.ReadFile(excludePath)
	if readErr != nil {
		t.Fatalf("read the exclude file: %v", readErr)
	}
	if !strings.Contains(string(contents), ExcludePattern) {
		t.Fatalf("the ignore rule is missing after a failed marker write:\n%s", contents)
	}
}

// TestTailCovApplyReportsAMarkerItCannotReplace pins the other write failure:
// a path that already exists as a non-empty directory cannot be replaced by
// the marker file, and Apply must report that rather than claim success.
func TestTailCovApplyReportsAMarkerItCannotReplace(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	checkout := filepath.Join(root, "checkout")
	blocker := filepath.Join(checkout, FileName)
	if err := os.MkdirAll(blocker, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(blocker, "keep"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := Apply(tailCovDescriptor(checkout), filepath.Join(root, "info", "exclude"))
	if err == nil {
		t.Fatal("Apply replaced a non-empty directory with the marker file")
	}
	if !strings.Contains(err.Error(), "replace ") {
		t.Fatalf("error %q does not name the failed replacement", err)
	}
	if result.MarkerWritten {
		t.Fatalf("Apply reported a written marker after failing: %+v", result)
	}
	info, statErr := os.Stat(blocker)
	if statErr != nil || !info.IsDir() {
		t.Fatalf("Apply damaged the existing path: info=%v err=%v", info, statErr)
	}
}

// TestTailCovEnsureExcludeRefusesAnEmptyPath pins that a checkout whose exclude
// file could not be resolved is an error, never a silent success.
func TestTailCovEnsureExcludeRefusesAnEmptyPath(t *testing.T) {
	t.Parallel()
	written, err := EnsureExclude("")
	if err == nil {
		t.Fatal("EnsureExclude(\"\") succeeded with no exclude file resolved")
	}
	if written {
		t.Fatal("EnsureExclude(\"\") reported a write")
	}
	if !strings.Contains(err.Error(), "no exclude file was resolved") {
		t.Fatalf("error %q does not explain that no exclude file was resolved", err)
	}
}

// TestTailCovEnsureExcludeReportsAnUnreadableFile pins that a read failure
// other than "not there yet" is surfaced rather than treated as an empty file,
// which would otherwise blindly append and could mask a permission problem.
func TestTailCovEnsureExcludeReportsAnUnreadableFile(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	written, err := EnsureExclude(directory)
	if err == nil {
		t.Fatal("EnsureExclude succeeded reading a directory as the exclude file")
	}
	if written {
		t.Fatal("EnsureExclude reported a write after an unreadable exclude file")
	}
	if !strings.Contains(err.Error(), "read ") {
		t.Fatalf("error %q does not name the failed read", err)
	}
}

// TestTailCovEnsureExcludeReportsWhenTheDirectoryCannotBeCreated pins the
// MkdirAll failure: the rule file does not exist yet, but its parent cannot be
// created. A dangling symlink stands in for that parent so the failure is
// deterministic and needs no elevated permissions.
func TestTailCovEnsureExcludeReportsWhenTheDirectoryCannotBeCreated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	link := filepath.Join(root, "dangling")
	if err := os.Symlink(filepath.Join(root, "absent-target"), link); err != nil {
		t.Fatalf("create a dangling symlink: %v", err)
	}
	excludePath := filepath.Join(link, "info", "exclude")

	written, err := EnsureExclude(excludePath)
	if err == nil {
		t.Fatalf("EnsureExclude wrote through an unusable parent: written=%v", written)
	}
	if written {
		t.Fatal("EnsureExclude reported a write after MkdirAll failed")
	}
	if !strings.Contains(err.Error(), "create ") {
		t.Fatalf("error %q does not name the failed directory creation", err)
	}
	if _, statErr := os.Lstat(excludePath); statErr == nil {
		t.Fatalf("%s exists after a reported failure", excludePath)
	}
}

// TestTailCovDescribeDefaultsTheBaseBranchToMain pins that an unset
// BaseBranch falls back to "main" rather than leaving the marker's
// base_branch empty.
func TestTailCovDescribeDefaultsTheBaseBranchToMain(t *testing.T) {
	t.Parallel()
	repositories := newFixture(t)
	options := describeOptions(repositories)
	options.BaseBranch = ""

	inspection, err := Describe(repositories.Canonical, options)
	if err != nil {
		t.Fatalf("Describe: %v", err)
	}
	if got := inspection.Descriptor.BaseBranch; got != "main" {
		t.Fatalf("BaseBranch = %q with no option set, want %q", got, "main")
	}
}

// TestTailCovDescribeReportsAWorktreeWithNoGitdirPointer pins the integration
// failure path: a `.git` file that Classify reads as a linked worktree but
// that carries no gitdir pointer is reported, not guessed at.
func TestTailCovDescribeReportsAWorktreeWithNoGitdirPointer(t *testing.T) {
	t.Parallel()
	projectsRoot := t.TempDir()
	linked := filepath.Join(projectsRoot, "loose-worktree")
	if err := os.MkdirAll(linked, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(linked, ".git"), []byte("not a pointer\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Describe(linked, DescribeOptions{ProjectsRoot: projectsRoot, BaseBranch: "main", Version: "wb v1"})
	if err == nil {
		t.Fatal("Describe accepted a linked checkout whose .git holds no gitdir pointer")
	}
	if !strings.Contains(err.Error(), "holds no gitdir pointer") {
		t.Fatalf("error %q does not say the gitdir pointer is missing", err)
	}
}

// TestTailCovDescribeReportsAPathItCannotResolve lives in
// describe_linux_tailcov_test.go: filepath.Abs only fails when the working
// directory cannot be read, and Linux is the platform on which removing the
// process's own working directory actually makes os.Getwd fail. On darwin the
// kernel keeps the removed directory's vnode reachable, so the branch is
// unreachable there.

// TestTailCovLinkedGitDirReportsAnUnreadablePointer pins the read failure of
// the pointer reader, using a `.git` that cannot be read as a file.
func TestTailCovLinkedGitDirReportsAnUnreadablePointer(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := linkedGitDir(root); err == nil {
		t.Fatal("linkedGitDir read a .git directory as a worktree pointer")
	}
}

// TestTailCovLinkedGitDirFollowsTheGitdirLine pins the parser: comment and
// blank lines are skipped, and the first gitdir target is returned cleaned.
func TestTailCovLinkedGitDirFollowsTheGitdirLine(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	contents := "# a worktree pointer\n\ngitdir: /canonical/repo/.git/worktrees/tailcov\n"
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := linkedGitDir(root)
	if err != nil {
		t.Fatalf("linkedGitDir: %v", err)
	}
	want := filepath.Clean("/canonical/repo/.git/worktrees/tailcov")
	if got != want {
		t.Fatalf("linkedGitDir = %q, want %q", got, want)
	}
}

// TestTailCovLinkedGitDirResolvesARelativeTarget pins that a relative gitdir
// target is resolved against the worktree root, which is what Git writes for
// some layouts.
func TestTailCovLinkedGitDirResolvesARelativeTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte("gitdir: ../elsewhere/worktrees/tailcov\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := linkedGitDir(root)
	if err != nil {
		t.Fatalf("linkedGitDir: %v", err)
	}
	want := filepath.Clean(filepath.Join(root, "..", "elsewhere", "worktrees", "tailcov"))
	if got != want {
		t.Fatalf("linkedGitDir(relative) = %q, want %q", got, want)
	}
}

// TestTailCovLinkedGitDirReportsAnEmptyTarget pins that a `gitdir:` line with
// no target stops the parse and is reported, rather than falling through to
// some later line or a guessed path.
func TestTailCovLinkedGitDirReportsAnEmptyTarget(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	contents := "gitdir:   \ngitdir: /canonical/repo/.git/worktrees/tailcov\n"
	if err := os.WriteFile(filepath.Join(root, ".git"), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := linkedGitDir(root)
	if err == nil {
		t.Fatalf("linkedGitDir accepted an empty gitdir target and returned %q", got)
	}
	if !strings.Contains(err.Error(), "holds no gitdir pointer") {
		t.Fatalf("error %q does not say the gitdir pointer is missing", err)
	}
}

// TestTailCovCommonDirectoryForOnlyAcceptsWorktreesLayout pins the common
// directory derivation and that anything else yields "" rather than a guess.
func TestTailCovCommonDirectoryForOnlyAcceptsWorktreesLayout(t *testing.T) {
	t.Parallel()
	gitDir := filepath.Join("/tmp", "repo", ".git", "worktrees", "tailcov")
	if got, want := commonDirectoryFor(gitDir), filepath.Join("/tmp", "repo", ".git"); got != want {
		t.Fatalf("commonDirectoryFor(%q) = %q, want %q", gitDir, got, want)
	}
	notWorktrees := filepath.Join("/tmp", "repo", ".git")
	if got := commonDirectoryFor(notWorktrees); got != "" {
		t.Fatalf("commonDirectoryFor(%q) = %q, want \"\"", notWorktrees, got)
	}
}

// TestTailCovTaskCoordinatesNeedsRepositoryCoordinates pins that without a
// usable owner/repository pair the task and worktrees root are both empty,
// rather than being recovered from an unrelated directory shape.
func TestTailCovTaskCoordinatesNeedsRepositoryCoordinates(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "some", "task")
	if task, worktreesRoot := taskCoordinates(root, "", ""); task != "" || worktreesRoot != "" {
		t.Fatalf("taskCoordinates with no repository = (%q, %q), want empty", task, worktreesRoot)
	}
	if task, worktreesRoot := taskCoordinates(root, "", "no-slash-here"); task != "" || worktreesRoot != "" {
		t.Fatalf("taskCoordinates with a slashless repository = (%q, %q), want empty", task, worktreesRoot)
	}
}

// TestTailCovReadHeadBranchReportsNoBranch pins that an unreadable HEAD and a
// detached HEAD both yield "" — the marker states an empty branch rather than
// inventing one.
func TestTailCovReadHeadBranchReportsNoBranch(t *testing.T) {
	t.Parallel()
	if got := readHeadBranch(filepath.Join(t.TempDir(), "missing-git-dir")); got != "" {
		t.Fatalf("readHeadBranch(missing git dir) = %q, want \"\"", got)
	}

	detached := t.TempDir()
	if err := os.WriteFile(filepath.Join(detached, "HEAD"), []byte("0123456789abcdef0123456789abcdef01234567\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHeadBranch(detached); got != "" {
		t.Fatalf("readHeadBranch(detached HEAD) = %q, want \"\"", got)
	}
}

// TestTailCovReadHeadBranchStripsTheHeadsPrefix pins the normal case's exact
// output, so a branch name that itself contains slashes survives unchanged.
func TestTailCovReadHeadBranchStripsTheHeadsPrefix(t *testing.T) {
	t.Parallel()
	gitDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(gitDir, "HEAD"), []byte("ref: refs/heads/feature/tailcov\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := readHeadBranch(gitDir); got != "feature/tailcov" {
		t.Fatalf("readHeadBranch = %q, want %q", got, "feature/tailcov")
	}
}
