package quality

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
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
// path). "from" keeps a second, untouched .go file so it stays a buildable
// package after the move — the "git mv leaves nothing buildable behind"
// case is its own test below (M1).
func TestChangedPackagesCountsBothRenamePaths(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("from/thing.go", "package from\n")
	repo.writeFile("from/other.go", "package from\n")
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

// TestChangedPackagesExcludesDirectoryWithNoBuildableGoFileLeft proves a
// `git mv` that empties a directory of every *.go file (leaving only a
// non-Go file such as a README behind, so the directory itself still
// exists) is excluded — `go test`/`go vet` fail outright ("no Go files")
// against a directory like that (review finding M1).
func TestChangedPackagesExcludesDirectoryWithNoBuildableGoFileLeft(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("from/thing.go", "package from\n")
	repo.writeFile("from/README.md", "hello\n")
	baseSHA := repo.commitAll("base")

	if err := os.MkdirAll(filepath.Join(repo.dir, "to"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.runGit("mv", "from/thing.go", "to/thing.go")
	repo.commitAll("rename package, leave a README behind")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./to"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (./from has no buildable .go file left)", result.Packages, want)
	}
}

// TestChangedPackagesExcludesTestdataDirectory proves a change confined to
// a "testdata" directory is excluded — the go tool itself always ignores
// such directories, so `go vet`/`go test` would either skip or mis-handle
// it (review finding M1).
func TestChangedPackagesExcludesTestdataDirectory(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("pkg/pkg.go", "package pkg\n")
	repo.writeFile("pkg/testdata/fixture.go", "package ignored\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("pkg/testdata/fixture.go", "package ignored\n\n// touched\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) != 0 {
		t.Fatalf("Packages = %#v, want none for a testdata-only change", result.Packages)
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

// TestChangedPackagesPropagatesGitTopLevelError proves a directory outside
// any Git work tree at all (a bare repository is one; `git rev-parse
// --show-toplevel` fails there exactly like `git diff` does, since neither
// operation has a work tree to run against) surfaces as an error, not an
// empty result.
func TestChangedPackagesPropagatesGitTopLevelError(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	baseSHA := repo.commitAll("base")

	bareDir := filepath.Join(t.TempDir(), "bare.git")
	repo.runGit("clone", "-q", "--bare", repo.dir, bareDir)

	if _, err := ChangedPackages(context.Background(), bareDir, baseSHA); err == nil {
		t.Fatal("want error when the working directory has no Git work tree at all")
	}
}

// TestChangedPackagesPropagatesFindModuleRootError proves a Git repository
// with no go.mod anywhere in it fails closed with an error rather than
// silently reporting no changes.
func TestChangedPackagesPropagatesFindModuleRootError(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	// newFixtureRepo already wrote go.mod; remove it so no module exists.
	if err := os.Remove(filepath.Join(repo.dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	repo.writeFile("README.md", "hello\n")
	baseSHA := repo.commitAll("base")

	if _, err := ChangedPackages(context.Background(), repo.dir, baseSHA); err == nil {
		t.Fatal("want error when no go.mod exists anywhere in the repository")
	}
}

// TestChangedPackagesPropagatesGitTouchedFilesError proves an error from
// the last git call (GitTouchedFiles), after GitTopLevel, findModuleRoot,
// and GitMergeBase all already resolved successfully, surfaces as an error
// rather than an empty result. A fake `git` in front of PATH delegates
// every subcommand except "diff" to the real binary, isolating this one
// branch deterministically (no reliance on timing).
//
//nolint:paralleltest // installFakeGitFailingDiff calls t.Setenv on the shared process PATH (sneat-dev/wb#646 baseline fix)
func TestChangedPackagesPropagatesGitTouchedFilesError(t *testing.T) {
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	baseSHA := repo.commitAll("base")

	installFakeGitFailingDiff(t)

	if _, err := ChangedPackages(context.Background(), repo.dir, baseSHA); err == nil {
		t.Fatal("want error when GitTouchedFiles' own git diff call fails")
	}
}

// installFakeGitFailingDiff puts a fake `git` at the front of PATH that
// delegates every subcommand to the real git binary except "diff", which it
// fails outright. Not parallel-safe (t.Setenv mutates process-wide PATH).
func installFakeGitFailingDiff(t *testing.T) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git shim requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	script := "#!/bin/sh\n" +
		"for arg in \"$@\"; do\n" +
		"  if [ \"$arg\" = \"diff\" ]; then\n" +
		"    echo 'fake git: diff refused' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	if resolved, err := exec.LookPath("git"); err != nil || resolved != path {
		t.Fatalf("PATH override did not take effect: resolved=%q err=%v", resolved, err)
	}
}

// TestChangedPackagesFromNestedModuleRoot proves a repository whose Go
// module lives in a subdirectory (the fleet's common
// "<product>/backend/go.mod" layout) resolves correctly when invoked from
// that module's own root: patterns stay relative to it, and a change in a
// second, sibling Go module elsewhere in the same repository (its own
// go.mod, so it is a module boundary, not merely a non-Go directory) is
// excluded, per review finding B2 ("exclude packages outside that
// module").
func TestChangedPackagesFromNestedModuleRoot(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	// newFixtureRepo already wrote a go.mod at repo.dir; this test wants the
	// module nested under "backend" instead, so replace it.
	if err := os.Remove(filepath.Join(repo.dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	repo.writeFile("backend/go.mod", "module fixture.test/backend\n\ngo 1.27\n")
	repo.writeFile("backend/internal/a/a.go", "package a\n")
	repo.writeFile("other/go.mod", "module fixture.test/other\n\ngo 1.27\n")
	repo.writeFile("other/x.go", "package other\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("backend/internal/a/a.go", "package a\n\nfunc Changed() {}\n")
	repo.writeFile("other/x.go", "package other\n\nfunc Changed() {}\n")

	moduleRoot := filepath.Join(repo.dir, "backend")
	result, err := ChangedPackages(context.Background(), moduleRoot, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./internal/a"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (other/ is a sibling module and must be excluded)", result.Packages, want)
	}
}

// TestChangedPackagesFromSubdirectoryOfNestedModule proves invocation from
// a subdirectory further inside that same nested module still resolves the
// correct module root and rebases patterns onto the invocation directory
// itself, per review finding B2's exact probe.
func TestChangedPackagesFromSubdirectoryOfNestedModule(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	if err := os.Remove(filepath.Join(repo.dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	repo.writeFile("backend/go.mod", "module fixture.test/backend\n\ngo 1.27\n")
	repo.writeFile("backend/internal/a/a.go", "package a\n")
	repo.writeFile("backend/b.go", "package backend\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("backend/internal/a/a.go", "package a\n\nfunc Changed() {}\n")
	repo.writeFile("backend/b.go", "package backend\n\nfunc Changed() {}\n")

	workingDir := filepath.Join(repo.dir, "backend", "internal", "a")
	result, err := ChangedPackages(context.Background(), workingDir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "../.."}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (own package \".\", module root \"../..\", both relative to cwd)", result.Packages, want)
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

// TestChangedPackagesFailsClosedWhenWorkingDirCannotBeResolved proves a
// relative workingDir that can't be made absolute (the current directory no
// longer exists) surfaces as an error instead of resolving against some
// other, unrelated directory.
//
//nolint:paralleltest // calls t.Chdir on the shared process working directory (sneat-dev/wb#646 baseline fix)
func TestChangedPackagesFailsClosedWhenWorkingDirCannotBeResolved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("directory-removal-under-cwd probe requires POSIX semantics")
	}
	gone := t.TempDir()
	t.Chdir(gone)
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}
	if _, err := ChangedPackages(context.Background(), ".", "main"); err == nil {
		t.Fatal("want error when the current directory no longer exists")
	}
}

// TestGitTopLevelFailsWithoutExitErrorWhenGitCannotEvenStart exercises the
// non-*exec.ExitError branch, mirroring
// TestGitMergeBaseFailsWithoutExitErrorWhenGitCannotEvenStart.
func TestGitTopLevelFailsWithoutExitErrorWhenGitCannotEvenStart(t *testing.T) {
	t.Parallel()
	_, err := GitTopLevel(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"))
	if err == nil {
		t.Fatal("want error when dir does not exist")
	}
}

// TestFindModuleRootStopsAtTheFilesystemRootWhenCeilingIsNotAnAncestor
// proves the upward search terminates even when ceilingDir is never
// encountered (an unrelated path), rather than looping or panicking.
func TestFindModuleRootStopsAtTheFilesystemRootWhenCeilingIsNotAnAncestor(t *testing.T) {
	t.Parallel()
	startDir := t.TempDir()
	if _, err := findModuleRoot(startDir, string(filepath.Separator)+"unrelated-ceiling-path"); err == nil {
		t.Fatal("want error when no go.mod exists anywhere up to the filesystem root")
	}
}

// TestModuleRelativePathReportsNotOKWhenPathsAreNotComparable exercises
// moduleRelativePath's own defensive branch directly: filepath.Rel fails
// only between paths of different absoluteness, which ChangedPackages'
// real call site never produces (both are always absolute there).
func TestModuleRelativePathReportsNotOKWhenPathsAreNotComparable(t *testing.T) {
	t.Parallel()
	if _, ok := moduleRelativePath("relative/base", "/absolute/target"); ok {
		t.Fatal("want ok=false when the two paths cannot be related")
	}
}

// TestHasBuildableGoFileReportsFalseWhenDirectoryCannotBeRead exercises
// hasBuildableGoFile's own os.ReadDir error branch directly.
func TestHasBuildableGoFileReportsFalseWhenDirectoryCannotBeRead(t *testing.T) {
	t.Parallel()
	if hasBuildableGoFile(filepath.Join(t.TempDir(), "does-not-exist")) {
		t.Fatal("want false when the directory cannot be read")
	}
}

// TestRelativePatternPropagatesRelErrorForNonComparablePaths exercises
// relativePattern's own defensive branch directly, for the same reason as
// TestModuleRelativePathReportsNotOKWhenPathsAreNotComparable above.
func TestRelativePatternPropagatesRelErrorForNonComparablePaths(t *testing.T) {
	t.Parallel()
	if _, err := relativePattern("relative/working-dir", "/absolute/target"); err == nil {
		t.Fatal("want error when the two paths cannot be related")
	}
}

// TestChangedPackagesResolvesSymlinkedWorkingDirectory proves a workingDir
// reached only through a symlink (the macOS /tmp -> /private/tmp alias, or
// any other symlinked checkout) still resolves to the real module, instead
// of finding every changed directory "outside" the module and silently
// returning zero packages (review-726 finding B2a). newFixtureRepo's own
// directory already lives under a testing.T.TempDir(), which the Go test
// harness pre-resolves; the symlink under test is built separately, by
// hand, so this test cannot accidentally pass only because of that
// pre-resolution.
func TestChangedPackagesResolvesSymlinkedWorkingDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink probe requires POSIX symlink semantics")
	}
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("pkg/pkg.go", "package pkg\n")
	baseSHA := repo.commitAll("base")
	repo.writeFile("pkg/pkg.go", "package pkg\n\nfunc Changed() {}\n")

	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(repo.dir, alias); err != nil {
		t.Fatal(err)
	}

	result, err := ChangedPackages(context.Background(), filepath.Join(alias, "pkg"), baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"."}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (a symlinked working directory must still resolve to the real module)", result.Packages, want)
	}
}

// TestChangedPackagesExcludesNestedModuleAndItsSubdirectoriesFromTheRoot
// proves a Go module nested inside the current module (its own go.mod
// below moduleRoot, the fleet's tools/, examples/, or <product>/backend
// pattern turned inside-out) is excluded, along with every directory
// beneath it — `go vet`/`go test` invoked from moduleRoot cannot build a
// package belonging to a different module at all ("main module does not
// contain package"), so naming one would just fail the command outright
// (review-726 finding B3, using the reviewer's own probe layout: a root
// module, a nested "svc" module, an ordinary "other" directory, and a
// deeper "svc/internal/a" package).
func TestChangedPackagesExcludesNestedModuleAndItsSubdirectoriesFromTheRoot(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("top.go", "package app\n")
	repo.writeFile("other/o.go", "package other\n")
	repo.writeFile("svc/go.mod", "module fixture.test/app/svc\n\ngo 1.27\n")
	repo.writeFile("svc/b.go", "package svc\n")
	repo.writeFile("svc/internal/a/a.go", "package a\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("top.go", "package app\n\nfunc Changed() {}\n")
	repo.writeFile("other/o.go", "package other\n\nfunc Changed() {}\n")
	repo.writeFile("svc/b.go", "package svc\n\nfunc Changed() {}\n")
	repo.writeFile("svc/internal/a/a.go", "package a\n\nfunc Changed() {}\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{".", "./other"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (svc/ and svc/internal/a belong to a nested module and must be excluded)", result.Packages, want)
	}
}

// TestChangedPackagesExcludesVendorDirectory proves a change confined to a
// "vendor" directory is excluded, the same way a "testdata" directory
// already is: the go tool always ignores a vendor tree for build purposes.
func TestChangedPackagesExcludesVendorDirectory(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("pkg/pkg.go", "package pkg\n")
	repo.writeFile("vendor/example.com/lib/lib.go", "package lib\n")
	baseSHA := repo.commitAll("base")

	repo.writeFile("vendor/example.com/lib/lib.go", "package lib\n\n// touched\n")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Packages) != 0 {
		t.Fatalf("Packages = %#v, want none for a vendor-only change", result.Packages)
	}
}

// TestChangedPackagesExcludesDirectoryWhoseOnlyGoFileIsUnderscorePrefixed
// proves a directory left with only an "_"-prefixed *.go file (which the go
// tool itself always ignores, the same as a "_"-prefixed directory) is
// still treated as having no buildable Go file.
func TestChangedPackagesExcludesDirectoryWhoseOnlyGoFileIsUnderscorePrefixed(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("from/thing.go", "package from\n")
	repo.writeFile("from/_ignored.go", "package from\n")
	baseSHA := repo.commitAll("base")

	if err := os.MkdirAll(filepath.Join(repo.dir, "to"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.runGit("mv", "from/thing.go", "to/thing.go")
	repo.commitAll("rename package, leave an underscore-prefixed file behind")

	result, err := ChangedPackages(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"./to"}
	if !reflect.DeepEqual(result.Packages, want) {
		t.Fatalf("Packages = %#v, want %#v (./from's only remaining .go file is _-prefixed, which the go tool ignores)", result.Packages, want)
	}
}

// TestSamePathComparesCleanedPaths exercises samePath directly: it must
// treat two spellings of the same directory as equal (a trailing slash, a
// "." segment) and two different directories as unequal, independent of
// whichever caller (findModuleRoot's ceiling check, isUnderNestedModule's
// walk) is comparing them (review-726 finding I2, a unit test on the
// comparison helper).
func TestSamePathComparesCleanedPaths(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want bool
	}{
		{filepath.Join("repo", "svc"), filepath.Join("repo", "svc"), true},
		{filepath.Join("repo", "svc") + string(filepath.Separator), filepath.Join("repo", "svc"), true},
		{filepath.Join("repo", ".", "svc"), filepath.Join("repo", "svc"), true},
		{filepath.Join("repo", "svc"), filepath.Join("repo", "other"), false},
	}
	for _, c := range cases {
		if got := samePath(c.a, c.b); got != c.want {
			t.Errorf("samePath(%q, %q) = %v, want %v", c.a, c.b, got, c.want)
		}
	}
}

// TestIsUnderNestedModuleReportsFalseAtModuleRootItself exercises
// isUnderNestedModule's own base case directly: moduleRoot compared against
// itself is never "nested", regardless of what moduleRoot's own go.mod
// means for its parent.
func TestIsUnderNestedModuleReportsFalseAtModuleRootItself(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if isUnderNestedModule(root, root) {
		t.Fatal("want false: a directory is never nested relative to itself")
	}
}

// TestIsUnderNestedModuleStopsAtFilesystemRootWhenModuleRootIsNotAnAncestor
// exercises isUnderNestedModule's own filesystem-root fallback directly:
// ChangedPackages' real call site always passes an absoluteDir moduleRoot
// actually is an ancestor of (moduleRelativePath already checked that), so
// this defensive termination is otherwise unreachable.
func TestIsUnderNestedModuleStopsAtFilesystemRootWhenModuleRootIsNotAnAncestor(t *testing.T) {
	t.Parallel()
	if isUnderNestedModule(string(filepath.Separator)+"unrelated-module-root", t.TempDir()) {
		t.Fatal("want false when moduleRoot is never reached while walking up")
	}
}

// TestChangedPackagesFailsClosedWhenWorkingDirCannotBeSymlinkResolved
// exercises ChangedPackages' own filepath.EvalSymlinks(workingDir) error
// branch directly: an absolute-but-nonexistent workingDir passes
// filepath.Abs trivially (it is already absolute) but fails symlink
// resolution, distinguishing this from
// TestChangedPackagesFailsClosedWhenWorkingDirCannotBeResolved above, which
// exercises the earlier filepath.Abs error instead.
func TestChangedPackagesFailsClosedWhenWorkingDirCannotBeSymlinkResolved(t *testing.T) {
	t.Parallel()
	if _, err := ChangedPackages(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "main"); err == nil {
		t.Fatal("want error when workingDir does not exist for symlink resolution")
	}
}

// installFakeGitReportingMissingTopLevel puts a fake `git` at the front of
// PATH whose "rev-parse --show-toplevel" reports a directory that does not
// exist, delegating every other subcommand to the real git binary. Not
// parallel-safe (t.Setenv mutates process-wide PATH).
func installFakeGitReportingMissingTopLevel(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("fake git shim requires a POSIX shell")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	missing := filepath.Join(dir, "reported-top-level-does-not-exist")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = \"rev-parse\" ] && [ \"$2\" = \"--show-toplevel\" ]; then\n" +
		"  echo '" + missing + "'\n" +
		"  exit 0\n" +
		"fi\n" +
		"exec " + realGit + " \"$@\"\n"
	path := filepath.Join(dir, "git")
	if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return missing
}

// TestChangedPackagesFailsClosedWhenGitTopLevelCannotBeSymlinkResolved
// exercises ChangedPackages' own filepath.EvalSymlinks(gitRoot) error branch
// directly: `git rev-parse --show-toplevel` always names a real, existing
// directory in practice, so this is otherwise unreachable through a real
// git binary.
//
//nolint:paralleltest // installFakeGitReportingMissingTopLevel calls t.Setenv on the shared process PATH (sneat-dev/wb#646 baseline fix)
func TestChangedPackagesFailsClosedWhenGitTopLevelCannotBeSymlinkResolved(t *testing.T) {
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	baseSHA := repo.commitAll("base")

	installFakeGitReportingMissingTopLevel(t)

	if _, err := ChangedPackages(context.Background(), repo.dir, baseSHA); err == nil {
		t.Fatal("want error when the reported git top level cannot be symlink-resolved")
	}
}
