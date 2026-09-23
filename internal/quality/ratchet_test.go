package quality

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fixtureRepo builds a tiny real git repository containing a one-package Go
// module, so ratchet tests exercise real `git diff --color-moved` and real
// `go test -coverprofile` output rather than a hand-rolled mock of either.
type fixtureRepo struct {
	t          *testing.T
	dir        string
	modulePath string
}

func newFixtureRepo(t *testing.T) *fixtureRepo {
	t.Helper()
	dir := t.TempDir()
	repo := &fixtureRepo{t: t, dir: dir, modulePath: "fixture.test/app"}
	repo.runGit("init", "--initial-branch=main")
	repo.runGit("config", "user.email", "fixture@example.com")
	repo.runGit("config", "user.name", "Fixture")
	repo.writeFile("go.mod", "module "+repo.modulePath+"\n\ngo 1.27\n")
	return repo
}

func (r *fixtureRepo) writeFile(relativePath, contents string) {
	r.t.Helper()
	full := filepath.Join(r.dir, relativePath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *fixtureRepo) runGit(args ...string) string {
	r.t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = r.dir
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.com",
		"GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.com",
	)
	output, err := cmd.CombinedOutput()
	if err != nil {
		r.t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
	return string(output)
}

func (r *fixtureRepo) commitAll(message string) string {
	r.t.Helper()
	r.runGit("add", "-A")
	r.runGit("commit", "-m", message)
	return strings.TrimSpace(r.runGit("rev-parse", "HEAD"))
}

// coverProfile runs `go test -coverprofile` for the fixture module as it
// stands on disk right now (whatever ref is checked out) and returns the
// parsed blocks.
func (r *fixtureRepo) coverProfile() []CoverageBlock {
	r.t.Helper()
	profilePath := filepath.Join(r.t.TempDir(), "profile.out")
	cmd := exec.Command("go", "test", "-coverprofile="+profilePath, "./...")
	cmd.Dir = r.dir
	if output, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("go test -coverprofile: %v\n%s", err, output)
	}
	blocks, err := ParseCoverageProfile(profilePath)
	if err != nil {
		r.t.Fatal(err)
	}
	return blocks
}

const fixtureBaseSource = `package app

func Add(a, b int) int {
	return a + b
}

func Uncovered(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}
`

const fixtureTestSource = `package app

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add(1, 2) != 3")
	}
}
`

// TestGitChangedLinesFixturePRMovingUncoveredFunctionUnchangedPasses is the
// task-3 AC: a fixture PR that moves an uncovered function unchanged passes
// the ratchet (no findings), because git identifies the move and the
// moved-code rule only holds the package's uncovered count, which does not
// rise from a pure move.
func TestGitChangedLinesFixturePRMovingUncoveredFunctionUnchangedPasses(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	baseSHA := repo.commitAll("base")

	// Move Uncovered above Add, unchanged, on a feature branch.
	moved := `package app

func Uncovered(a, b int) int {
	if a > b {
		return a - b
	}
	return b - a
}

func Add(a, b int) int {
	return a + b
}
`
	repo.runGit("checkout", "-b", "feature")
	repo.writeFile("app.go", moved)
	repo.commitAll("move Uncovered above Add")

	changed, err := GitChangedLines(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	blocks := repo.coverProfile()
	baseline := PackageBaseline{Packages: map[string]int{".": 3}} // Uncovered's 3 statements, from before the move
	results := EvaluateRatchet(blocks, changed, baseline, repo.modulePath)

	if len(results) != 1 {
		t.Fatalf("results = %#v, want exactly one package", results)
	}
	got := results[0]
	if !got.Pass {
		t.Fatalf("package ratchet = %#v, want Pass (pure move of an uncovered function must not fail)", got)
	}
	if got.Rose {
		t.Fatalf("package ratchet Rose = true, want false: a pure move must not raise the uncovered count")
	}
	if len(got.NewlyUncoveredChanged) != 0 {
		t.Fatalf("NewlyUncoveredChanged = %#v, want none: moved-but-unmodified lines are exempt", got.NewlyUncoveredChanged)
	}
}

// TestGitChangedLinesFixturePRAddingUncoveredStatementFailsAndNamesLine is the
// task-3 AC: a fixture PR that adds one uncovered statement fails and names
// its file:line.
func TestGitChangedLinesFixturePRAddingUncoveredStatementFailsAndNamesLine(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	baseSHA := repo.commitAll("base")

	repo.runGit("checkout", "-b", "feature")
	withNewUncoveredStatement := fixtureBaseSource + `
func NewlyAdded(a, b int) int {
	return a * b
}
`
	repo.writeFile("app.go", withNewUncoveredStatement)
	repo.commitAll("add NewlyAdded, untested")

	changed, err := GitChangedLines(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	blocks := repo.coverProfile()
	baseline := PackageBaseline{Packages: map[string]int{".": 3}}
	results := EvaluateRatchet(blocks, changed, baseline, repo.modulePath)

	if len(results) != 1 {
		t.Fatalf("results = %#v, want exactly one package", results)
	}
	got := results[0]
	if got.Pass {
		t.Fatalf("package ratchet = %#v, want failure: an added uncovered statement must fail", got)
	}
	if len(got.NewlyUncoveredChanged) != 1 {
		t.Fatalf("NewlyUncoveredChanged = %#v, want exactly one finding", got.NewlyUncoveredChanged)
	}
	finding := got.NewlyUncoveredChanged[0]
	// The finding names the repo-relative path git diff uses, not the
	// module-qualified import path.
	if finding.File != "app.go" {
		t.Fatalf("finding.File = %q, want %q", finding.File, "app.go")
	}
	// NewlyAdded's body ("return a * b") is on the line after the func line
	// appended at the end of fixtureBaseSource (11 lines) plus the blank
	// separator and func line: verify it lands inside the appended block,
	// not somewhere in the untouched prefix.
	if finding.Line <= strings.Count(fixtureBaseSource, "\n") {
		t.Fatalf("finding.Line = %d, want a line inside the newly appended function", finding.Line)
	}
}

func TestEvaluateRatchetFailsWhenPackageUncoveredCountRisesWithoutChangedLineOverlap(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/app.go", StartLine: 10, EndLine: 12, Statements: 2, Count: 0},
	}
	changed := ChangedLines{} // no changed lines at all: this models a rebase that only shifted line numbers
	baseline := PackageBaseline{Packages: map[string]int{".": 1}}
	results := EvaluateRatchet(blocks, changed, baseline, "fixture.test/app")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	if !results[0].Rose {
		t.Fatalf("Rose = false, want true: uncovered count grew from 1 to 2 against baseline")
	}
	if results[0].Pass {
		t.Fatal("Pass = true, want false when the package's uncovered count rose")
	}
}

func TestEvaluateRatchetPassesForNewPackageWithNoBaseline(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/new/pkg.go", StartLine: 1, EndLine: 1, Statements: 1, Count: 1},
	}
	results := EvaluateRatchet(blocks, ChangedLines{}, PackageBaseline{Packages: map[string]int{}}, "fixture.test/app")
	if len(results) != 1 || !results[0].Pass || results[0].HasBaseline {
		t.Fatalf("results = %#v, want one passing package with no baseline entry", results)
	}
}

func TestParseCoverageProfileParsesRangesAndCounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	contents := "mode: set\n" +
		"fixture.test/app/app.go:3.30,5.2 1 1\n" +
		"fixture.test/app/app.go:7.34,10.2 3 0\n"
	if err := os.WriteFile(profilePath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	blocks, err := ParseCoverageProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 2 {
		t.Fatalf("blocks = %#v, want 2", blocks)
	}
	if blocks[0] != (CoverageBlock{File: "fixture.test/app/app.go", StartLine: 3, EndLine: 5, Statements: 1, Count: 1}) {
		t.Fatalf("blocks[0] = %#v", blocks[0])
	}
	if blocks[1] != (CoverageBlock{File: "fixture.test/app/app.go", StartLine: 7, EndLine: 10, Statements: 3, Count: 0}) {
		t.Fatalf("blocks[1] = %#v", blocks[1])
	}
}

func TestParseCoverageProfileRejectsMalformedLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\nnot a valid line\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCoverageProfile(profilePath); err == nil {
		t.Fatal("want error for malformed coverage profile line")
	}
}

func TestParseCoverageProfileMissingFile(t *testing.T) {
	t.Parallel()
	if _, err := ParseCoverageProfile(filepath.Join(t.TempDir(), "missing.out")); err == nil {
		t.Fatal("want error for missing coverage profile")
	}
}

func TestPackageOfMapsModuleQualifiedPathToDirectory(t *testing.T) {
	t.Parallel()
	cases := []struct{ file, module, want string }{
		{"github.com/sneat-dev/wb/internal/quality/ratchet.go", "github.com/sneat-dev/wb", "internal/quality"},
		{"github.com/sneat-dev/wb/main.go", "github.com/sneat-dev/wb", "."},
	}
	for _, c := range cases {
		if got := PackageOf(c.file, c.module); got != c.want {
			t.Errorf("PackageOf(%q, %q) = %q, want %q", c.file, c.module, got, c.want)
		}
	}
}

func TestPackageUncoveredCountsSumsOnlyZeroCountBlocks(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "m/a.go", StartLine: 1, EndLine: 1, Statements: 2, Count: 1},
		{File: "m/a.go", StartLine: 3, EndLine: 3, Statements: 5, Count: 0},
		{File: "m/b/c.go", StartLine: 1, EndLine: 1, Statements: 1, Count: 1},
	}
	counts := PackageUncoveredCounts(blocks, "m")
	if counts["."] != 5 {
		t.Errorf("counts[.] = %d, want 5", counts["."])
	}
	if counts["b"] != 0 {
		t.Errorf("counts[b] = %d, want 0 (fully covered package still reports zero, not absent)", counts["b"])
	}
}

func TestBaselineRoundTripsThroughWriteAndLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	want := PackageBaseline{SchemaVersion: 1, SHA: "deadbeef", Packages: map[string]int{"internal/quality": 4, ".": 0}}
	if err := WriteBaseline(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadBaseline(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.SchemaVersion != want.SchemaVersion || got.SHA != want.SHA || len(got.Packages) != len(want.Packages) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	for pkg, count := range want.Packages {
		if got.Packages[pkg] != count {
			t.Errorf("Packages[%q] = %d, want %d", pkg, got.Packages[pkg], count)
		}
	}
}

func TestLoadBaselineMissingFileReturnsError(t *testing.T) {
	t.Parallel()
	if _, err := LoadBaseline(filepath.Join(t.TempDir(), "missing.json")); err == nil {
		t.Fatal("want error for missing baseline file")
	}
}

func TestLoadBaselineRejectsMalformedJSON(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	if err := os.WriteFile(path, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadBaseline(path); err == nil {
		t.Fatal("want error for malformed baseline JSON")
	}
}

func TestComputeBaselineAtRefMeasuresMergeBaseUnderTimeout(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	baseline, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.Packages["."] != 3 {
		t.Fatalf("baseline.Packages[.] = %d, want 3 (Uncovered's three statements)", baseline.Packages["."])
	}
	if baseline.SHA == "" {
		t.Fatal("baseline.SHA = \"\", want the measured commit's SHA")
	}
}

func TestComputeBaselineAtRefFailsLoudOnTimeout(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	_, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Nanosecond)
	if err == nil {
		t.Fatal("want error when the wall-time budget is exceeded")
	}
	if !strings.Contains(err.Error(), "wall-time budget") {
		t.Fatalf("err = %v, want it to name the wall-time budget so the failure is loud, not a hang", err)
	}
}

func TestComputeBaselineAtRefRejectsUnknownRef(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "does-not-exist", time.Minute); err == nil {
		t.Fatal("want error for an unresolvable ref")
	}
}

func TestGitChangedLinesRejectsUnknownMergeBase(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.commitAll("base")
	if _, err := GitChangedLines(context.Background(), repo.dir, "does-not-exist"); err == nil {
		t.Fatal("want error for an unresolvable merge base")
	}
}

// TestGitChangedLinesFailsWithoutExitErrorWhenGitCannotEvenStart exercises the
// non-*exec.ExitError branch (e.g. git could not run in the given directory
// at all), distinct from git running and reporting a nonzero exit.
func TestGitChangedLinesFailsWithoutExitErrorWhenGitCannotEvenStart(t *testing.T) {
	t.Parallel()
	_, err := GitChangedLines(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "main")
	if err == nil {
		t.Fatal("want error when repoRoot does not exist")
	}
}

// TestGitChangedLinesExcludesDeletedFile exercises the "+++ /dev/null"
// (file-deleted) branch of the diff parser using a real deletion.
func TestGitChangedLinesExcludesDeletedFile(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("other.go", "package app\n\nfunc Other() {}\n")
	baseSHA := repo.commitAll("base")

	repo.runGit("checkout", "-b", "feature")
	if err := os.Remove(filepath.Join(repo.dir, "other.go")); err != nil {
		t.Fatal(err)
	}
	repo.commitAll("delete other.go")

	changed, err := GitChangedLines(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if len(changed) != 0 {
		t.Fatalf("changed = %#v, want none: a pure deletion adds no new lines", changed)
	}
}

func TestParseCoverageProfileSkipsBlankLines(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	contents := "mode: set\n\nfixture.test/app/app.go:3.30,5.2 1 1\n\n"
	if err := os.WriteFile(profilePath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	blocks, err := ParseCoverageProfile(profilePath)
	if err != nil {
		t.Fatal(err)
	}
	if len(blocks) != 1 {
		t.Fatalf("blocks = %#v, want 1 (blank lines skipped)", blocks)
	}
}

func TestParseCoverageProfileRejectsOverlongLine(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	huge := strings.Repeat("x", 5*1024*1024)
	contents := "mode: set\n" + huge + "\n"
	if err := os.WriteFile(profilePath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseCoverageProfile(profilePath); err == nil {
		t.Fatal("want error for a line exceeding the scanner's buffer")
	}
}

func TestParseCoverageProfileLineRejectsEveryMalformedShape(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		line string
	}{
		{"missing colon", "nofile 1 1"},
		{"missing comma range", "file.go:1.2 1 1"},
		{"bad start position", "file.go:bad,3.4 1 1"},
		{"bad end position", "file.go:1.2,bad 1 1"},
		{"non-numeric statement count", "file.go:1.2,3.4 x 1"},
		{"non-numeric hit count", "file.go:1.2,3.4 1 y"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseCoverageProfileLine(c.line); err == nil {
				t.Fatalf("parseCoverageProfileLine(%q) want error", c.line)
			}
		})
	}
}

func TestWriteBaselineFailsClosedOnUnwritableDestination(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "no-such-directory", "baseline.json")
	if err := WriteBaseline(path, PackageBaseline{Packages: map[string]int{}}); err == nil {
		t.Fatal("want error when the destination directory does not exist")
	}
}

func TestEvaluateRatchetSortsFindingsByFileThenLine(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "m/pkg/b.go", StartLine: 5, EndLine: 5, Statements: 1, Count: 0},
		{File: "m/pkg/a.go", StartLine: 9, EndLine: 9, Statements: 1, Count: 0},
		{File: "m/pkg/a.go", StartLine: 2, EndLine: 2, Statements: 1, Count: 0},
	}
	changed := ChangedLines{
		"pkg/b.go": {5: true},
		"pkg/a.go": {9: true, 2: true},
	}
	results := EvaluateRatchet(blocks, changed, PackageBaseline{Packages: map[string]int{}}, "m")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want 1 package", results)
	}
	findings := results[0].NewlyUncoveredChanged
	if len(findings) != 3 {
		t.Fatalf("findings = %#v, want 3", findings)
	}
	want := []RatchetFinding{
		{File: "pkg/a.go", Line: 2},
		{File: "pkg/a.go", Line: 9},
		{File: "pkg/b.go", Line: 5},
	}
	for i, w := range want {
		if findings[i] != w {
			t.Fatalf("findings[%d] = %#v, want %#v (findings must sort by file then line)", i, findings[i], w)
		}
	}
}

func TestResolveRefSHARejectsUnknownRef(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.commitAll("base")
	if _, err := resolveRefSHA(context.Background(), repo.dir, "does-not-exist"); err == nil {
		t.Fatal("want error for an unresolvable ref")
	}
}

func TestReadModulePathRejectsMissingGoMod(t *testing.T) {
	t.Parallel()
	if _, err := ReadModulePath(t.TempDir()); err == nil {
		t.Fatal("want error when go.mod is missing")
	}
}

func TestReadModulePathRejectsGoModWithoutModuleDeclaration(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("go 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := ReadModulePath(dir); err == nil {
		t.Fatal("want error when go.mod has no module declaration")
	}
}

func TestComputeBaselineAtRefRejectsMissingGoMod(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	if err := os.Remove(filepath.Join(repo.dir, "go.mod")); err != nil {
		t.Fatal(err)
	}
	repo.writeFile("app.go", "package app\n")
	repo.commitAll("base without go.mod")
	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute); err == nil {
		t.Fatal("want error when the repository has no go.mod")
	}
}

// TestComputeBaselineAtRefFailsWhenGoTestFails exercises the generic
// (non-timeout) `go test` failure branch: a package that fails to build.
func TestComputeBaselineAtRefFailsWhenGoTestFails(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", "package app\n\nfunc Broken() int {\n") // syntax error
	repo.commitAll("broken")
	_, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute)
	if err == nil {
		t.Fatal("want error when go test fails to build")
	}
	if strings.Contains(err.Error(), "wall-time budget") {
		t.Fatalf("err = %v, want the generic go-test-failed message, not a timeout message", err)
	}
}

// A module with no _test.go files still produces a valid (all-zero-count)
// coverage profile: `go test -coverprofile` instruments and reports on every
// matched package regardless of whether it has its own tests. That leaves
// ComputeBaselineAtRef's ParseCoverageProfile-error branch unreachable
// through the real `go test` contract it depends on (a matched build either
// fails, taking the "go test failed" branch above, or succeeds and always
// writes a well-formed profile) — see the PR description for why no fixture
// exercises it.

// TestComputeBaselineAtRefFailsWhenGitWorktreeAddFails exercises the plain
// (non-timeout) branch of "git worktree add" failing, by making .git
// unwritable so git cannot create its worktree administrative files. Not
// parallel-safe (mutates a shared filesystem permission during the test),
// so it runs serially and always restores the permission.
func TestComputeBaselineAtRefFailsWhenGitWorktreeAddFails(t *testing.T) {
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	gitDir := filepath.Join(repo.dir, ".git")
	if err := os.Chmod(gitDir, 0o555); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Chmod(gitDir, 0o755) }()

	_, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute)
	if err == nil {
		t.Fatal("want error when git cannot create the worktree's administrative files")
	}
	if strings.Contains(err.Error(), "wall-time budget") {
		t.Fatalf("err = %v, want the plain git-worktree-add failure, not a timeout message", err)
	}
}

// TestComputeBaselineAtRefFailsClosedWhenTempDirIsUnwritable exercises the
// os.MkdirTemp error branch by pointing TMPDIR at a regular file. Not
// parallel-safe (mutates process-wide TMPDIR), so it runs serially.
func TestComputeBaselineAtRefFailsClosedWhenTempDirIsUnwritable(t *testing.T) {
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// t.Setenv (not os.Setenv) restores TMPDIR automatically and asserts
	// this test never runs in parallel, so the process-wide mutation cannot
	// leak into another test's temp-file creation.
	t.Setenv("TMPDIR", notADir)

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute); err == nil {
		t.Fatal("want error when the temp directory cannot be created")
	}
}

func TestValidateBaselineRejectsWrongSchemaVersion(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 2, SHA: "abc", Packages: map[string]int{".": 0}}, "abc")
	if err == nil {
		t.Fatal("want error for an unsupported schema_version")
	}
}

func TestValidateBaselineRejectsEmptyPackages(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 1, SHA: "abc", Packages: map[string]int{}}, "abc")
	if err == nil {
		t.Fatal("want error for an empty package map")
	}
}

func TestValidateBaselineRejectsSHAMismatch(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 1, SHA: "deadbeef", Packages: map[string]int{".": 0}}, "abc")
	if err == nil {
		t.Fatal("want error when the baseline's sha does not match the merge base")
	}
}

func TestValidateBaselineAcceptsMatchingSHA(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 1, SHA: "abc", Packages: map[string]int{".": 0}}, "abc")
	if err != nil {
		t.Fatalf("want no error for a matching baseline, got %v", err)
	}
}

func TestValidateBaselineToleratesUnknownExpectedSHA(t *testing.T) {
	t.Parallel()
	// An empty expectedSHA means "no specific commit to check against"
	// (used when a caller has not yet resolved one); it must not itself
	// make an otherwise-usable baseline fail.
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 1, SHA: "whatever", Packages: map[string]int{".": 0}}, "")
	if err != nil {
		t.Fatalf("want no error when expectedSHA is empty, got %v", err)
	}
}

// TestGitChangedLinesHandlesPathsWithSpaces guards against the file-name
// key mismatch a quoted or mnemonic-prefixed diff path would otherwise
// cause: --no-... flags pin core.quotePath and diff.mnemonicPrefix off
// explicitly, and this fixture is the one most likely to expose a
// regression, since git quotes a path containing a space by default.
func TestGitChangedLinesHandlesPathsWithSpaces(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("my file.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	baseSHA := repo.commitAll("base")

	repo.writeFile("my file.go", fixtureBaseSource+"\nfunc NewlyAdded(a, b int) int {\n\treturn a * b\n}\n")
	repo.commitAll("add NewlyAdded to a spaced file name")

	changed, err := GitChangedLines(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !changed.Contains("my file.go", strings.Count(fixtureBaseSource, "\n")+2) {
		t.Fatalf("changed = %#v, want it to key the appended lines under the exact spaced file name", changed)
	}
}

// TestComputeBaselineAtRefFailsClosedWhenCoverageProfileIsMalformed
// exercises ComputeBaselineAtRef's ParseCoverageProfile error branch with a
// `go` shim that fakes `go test -coverprofile` to exit 0 while writing a
// profile ParseCoverageProfile rejects (a line with no "file:range"
// separator) — real `go test` cannot produce this, but a hermetic shim
// proves the branch fails closed rather than leaving it untested. Not
// parallel-safe (t.Setenv mutates the process-wide PATH).
func TestComputeBaselineAtRefFailsClosedWhenCoverageProfileIsMalformed(t *testing.T) {
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	repo.commitAll("base")

	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = test ]; then\n" +
		"  for a in \"$@\"; do\n" +
		"    case \"$a\" in -coverprofile=*) p=\"${a#-coverprofile=}\";; esac\n" +
		"  done\n" +
		"  printf 'mode: set\\nbadformat 1 1\\n' > \"$p\"\n" +
		"  exit 0\n" +
		"fi\n" +
		"exec " + realGo + " \"$@\"\n"
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "go"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute); err == nil {
		t.Fatal("want error when the measured profile fails ParseCoverageProfile's stricter parse")
	}
}
