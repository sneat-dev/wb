package quality

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// TestGitMergeBaseFailsWithoutExitErrorWhenGitCannotEvenStart exercises the
// non-*exec.ExitError branch, mirroring
// TestGitTouchedFilesFailsWithoutExitErrorWhenGitCannotEvenStart. Moved here
// from cmd/wb/coverage_ratchet_test.go alongside GitMergeBase itself
// (task-19 cutover): `wb coverage --changed` and `wb run --changed` now both
// call this one function.
func TestGitMergeBaseFailsWithoutExitErrorWhenGitCannotEvenStart(t *testing.T) {
	t.Parallel()
	_, err := GitMergeBase(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "main")
	if err == nil {
		t.Fatal("want error when repoRoot does not exist")
	}
}

func TestGitMergeBaseRejectsUnknownTarget(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.commitAll("base")
	if _, err := GitMergeBase(context.Background(), repo.dir, "does-not-exist"); err == nil {
		t.Fatal("want error for an unresolvable target")
	}
}

// TestChangedPackagesIncludesCommittedStagedAndUnstagedChangesTogether
// proves ChangedPackages captures all three kinds of local change in one
// pass, per task-19's brief: a package changed only by an already-committed
// commit since the merge base, one changed only by a staged edit, and one
// changed only by an unstaged edit all appear in the result.
func TestChangedPackagesIncludesCommittedStagedAndUnstagedChangesTogether(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("committed/committed.go", "package committed\n")
	repo.writeFile("staged/staged.go", "package staged\n")
	repo.writeFile("unstaged/unstaged.go", "package unstaged\n")
	baseSHA := repo.commitAll("base")

	repo.runGit("checkout", "-b", "feature")
	repo.writeFile("committed/committed.go", "package committed\n\nfunc Changed() {}\n")
	repo.commitAll("change committed package")

	repo.writeFile("staged/staged.go", "package staged\n\nfunc Changed() {}\n")
	repo.runGit("add", "staged/staged.go")

	repo.writeFile("unstaged/unstaged.go", "package unstaged\n\nfunc Changed() {}\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./committed", "./staged", "./unstaged"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v", result.Packages, want)
	}
	if result.Target != baseSHA {
		t.Fatalf("Target = %q, want %q", result.Target, baseSHA)
	}
}

// TestChangedPackagesIgnoresNonGoFiles proves a package touched only by a
// non-Go file (a README, for example) is not reported as changed, matching
// the pre-commit hook template's own `-- '*.go'` scope.
func TestChangedPackagesIgnoresNonGoFiles(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("docs/README.md", "hello\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("docs/README.md", "hello again\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) != 0 {
		t.Fatalf("Packages = %#v, want none for a non-Go-only change", result.Packages)
	}
}

// TestChangedPackagesMapsModuleRootFilesToDot proves a changed file directly
// at the module root reports as ".", the pattern `go test`/`go vet` expect
// for the root package.
func TestChangedPackagesMapsModuleRootFilesToDot(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("main.go", "package main\n\nfunc main() {}\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("main.go", "package main\n\nfunc main() { _ = 1 }\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Packages, []string{"."}) {
		t.Fatalf("Packages = %#v, want [\".\"]", result.Packages)
	}
}

// TestChangedPackagesCountsBothRenamePaths proves a rename reports both its
// old package and its new one, mirroring GitTouchedFiles' own --no-renames
// rationale (rename detection would otherwise print only the destination
// path). `git mv` leaves the old directory behind on disk, empty but still
// present, exactly like the pre-commit hook's own `[ -d "$package" ]` shell
// check would see it — this is not the "fully deleted" case (below), which
// needs the directory itself removed, not merely emptied.
func TestChangedPackagesCountsBothRenamePaths(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("from/thing.go", "package from\n")
	baseSHA := repo.commitAll("base")

	if err := os.MkdirAll(filepath.Join(repo.dir, "to"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.runGit("mv", "from/thing.go", "to/thing.go")
	repo.commitAll("rename package")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./from", "./to"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v", result.Packages, want)
	}
}

// TestChangedPackagesDropsFullyDeletedPackageDirectory proves a package
// directory the diff empties out entirely (deleted, or every file moved
// elsewhere) is dropped from the result: there is nothing left there for
// `go test`/`go vet` to run against, exactly as the pre-commit hook's own
// existing-directory check requires.
func TestChangedPackagesDropsFullyDeletedPackageDirectory(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("gone/gone.go", "package gone\n")
	repo.writeFile("kept/kept.go", "package kept\n")
	baseSHA := repo.commitAll("base")

	// `git rm` removes the now-empty parent directory along with the file
	// (unlike `git mv`, which leaves an empty directory behind — see the
	// rename test above), so no extra os.Remove is needed to simulate a
	// fully deleted package here.
	repo.runGit("rm", "gone/gone.go")
	repo.writeFile("kept/kept.go", "package kept\n\nfunc Changed() {}\n")
	repo.commitAll("delete gone package, change kept package")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./kept"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (the deleted package must be dropped)", result.Packages, want)
	}
}

// TestChangedPackagesPropagatesGitMergeBaseError proves an unresolvable
// target surfaces as an error rather than an empty result.
func TestChangedPackagesPropagatesGitMergeBaseError(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.commitAll("base")

	if _, err := ChangedPackages(context.Background(), repo.dir, "does-not-exist"); err == nil {
		t.Fatal("want error for an unresolvable target")
	}
}

// TestChangedPackagesPropagatesGitTouchedFilesError proves an error from the
// second git call (GitTouchedFiles), after GitMergeBase already resolved
// successfully, surfaces as an error rather than an empty result. A bare
// repository resolves `git merge-base` fine (it needs no worktree) but
// `git diff` fails outright (it does), isolating this branch from
// GitMergeBase's own error path.
func TestChangedPackagesPropagatesGitTouchedFilesError(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	baseSHA := repo.commitAll("base")

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	repo.runGit("clone", "-q", "--bare", repo.dir, bareDir)

	if _, err := ChangedPackages(context.Background(), bareDir, baseSHA); err == nil {
		t.Fatal("want error when GitTouchedFiles cannot run (bare repository has no work tree)")
	}
}

// TestChangedPackagesCountsTestFileChanges proves a "_test.go"-only change
// still reports its package as changed.
func TestChangedPackagesCountsTestFileChanges(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	baseSHA := repo.commitAll("base")

	repo.writeFile("app_test.go", fixtureTestSource+"\n// touched\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(result.Packages, []string{"."}) {
		t.Fatalf("Packages = %#v, want [\".\"] for a _test.go-only change at the module root", result.Packages)
	}
}
