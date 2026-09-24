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
	// A real baseline built from the base commit's own profile (not a
	// hand-typed count), so its UncoveredBlocks matches what BaselineFromProfile
	// actually publishes.
	baseline := BaselineFromProfile(repo.coverProfile(), repo.modulePath, baseSHA)

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
	touched, err := GitTouchedFiles(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	blocks := repo.coverProfile()
	results, _ := EvaluateRatchet(blocks, changed, touched, nil, baseline, repo.modulePath)

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
	baseline := BaselineFromProfile(repo.coverProfile(), repo.modulePath, baseSHA)

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
	touched, err := GitTouchedFiles(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	blocks := repo.coverProfile()
	results, _ := EvaluateRatchet(blocks, changed, touched, nil, baseline, repo.modulePath)

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

// TestGitTouchedFilesFixturePRRenamingOnlyTestFileOutOfPackageFailsRatchet is
// review-696 blocking #1: with rename detection on, `git diff --name-only`
// prints only a rename's destination path, so moving a package's only test
// file elsewhere (a pure R100 rename that still compiles as an external
// test package at its new path) never marked the source package as
// changed, letting its whole uncovered-statement count escape the count
// rule entirely. GitTouchedFiles now passes --no-renames, so both the old
// and new paths are always listed and the source package is judged.
func TestGitTouchedFilesFixturePRRenamingOnlyTestFileOutOfPackageFailsRatchet(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("a/a.go", `package a

func Foo(x int) int {
	if x > 0 {
		return x + 1
	}
	return x - 1
}
`)
	repo.writeFile("a/ext_test.go", `package a_test

import (
	"testing"

	"`+repo.modulePath+`/a"
)

func TestFoo(t *testing.T) {
	if a.Foo(1) != 2 {
		t.Fatal("Foo(1) != 2")
	}
	if a.Foo(-1) != -2 {
		t.Fatal("Foo(-1) != -2")
	}
}
`)
	baseSHA := repo.commitAll("base")
	baseline := BaselineFromProfile(repo.coverProfile(), repo.modulePath, baseSHA)

	repo.runGit("checkout", "-b", "feature")
	if err := os.MkdirAll(filepath.Join(repo.dir, "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	repo.runGit("mv", "a/ext_test.go", "c/ext_test.go")
	repo.commitAll("move a's only test out of package a")

	touched, err := GitTouchedFiles(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !touched["a/ext_test.go"] {
		t.Fatalf("touched = %#v, want a/ext_test.go present: --no-renames must list the rename's source path", touched)
	}
	changed, err := GitChangedLines(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	blocks := repo.coverProfile()
	results, _ := EvaluateRatchet(blocks, changed, touched, nil, baseline, repo.modulePath)

	var pkgA *PackageRatchet
	for i := range results {
		if results[i].Package == "a" {
			pkgA = &results[i]
		}
	}
	if pkgA == nil {
		t.Fatalf("results = %#v, want an entry for package a", results)
	}
	if !pkgA.Changed {
		t.Fatal("package a Changed = false, want true: its only test file moved out via a pure rename")
	}
	if pkgA.Pass {
		t.Fatal("package a Pass = true, want false: its uncovered count rose from 0 to 3 when its only test moved away")
	}
}

func TestEvaluateRatchetFailsWhenPackageUncoveredCountRisesWithoutChangedLineOverlap(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/app.go", StartLine: 10, EndLine: 12, Statements: 2, Count: 0},
	}
	changed := ChangedLines{} // no changed lines at all: this models a rebase that only shifted line numbers
	// app_test.go (not app.go, whose uncovered block's position must stay
	// trustworthy for the Rose-fallback match below) is what the PR
	// touched, which is enough to make "." a changed package.
	touched := map[string]bool{"app_test.go": true}
	baseline := PackageBaseline{Packages: map[string]int{".": 1}}
	results, warnings := EvaluateRatchet(blocks, changed, touched, nil, baseline, "fixture.test/app")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	if !results[0].Rose {
		t.Fatalf("Rose = false, want true: uncovered count grew from 1 to 2 against baseline")
	}
	if results[0].Pass {
		t.Fatal("Pass = true, want false when a changed package's uncovered count rose")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none: a changed package's rise is a failure, not a warning", warnings)
	}
}

// TestEvaluateRatchetWarnsInsteadOfFailingWhenAnUnchangedPackagesCountRises
// is the founder's 2026-09-23 B1 decision: the hard "count must never rise"
// rule applies only to packages the PR itself changes; every other
// package's rise is a warning, and the package still passes.
func TestEvaluateRatchetWarnsInsteadOfFailingWhenAnUnchangedPackagesCountRises(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/app.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 2, Count: 0},
	}
	changed := ChangedLines{}
	// The PR touches a wholly different package ("other"), not "." (app.go).
	touched := map[string]bool{"other/thing.go": true}
	baseline := PackageBaseline{Packages: map[string]int{".": 1}}
	results, warnings := EvaluateRatchet(blocks, changed, touched, nil, baseline, "fixture.test/app")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	got := results[0]
	if got.Changed {
		t.Fatal("Changed = true, want false: the PR did not touch this package")
	}
	if !got.Pass {
		t.Fatalf("package ratchet = %#v, want Pass: an unchanged package's rise must never fail", got)
	}
	if len(got.NewlyUncoveredChanged) != 0 {
		t.Fatalf("NewlyUncoveredChanged = %#v, want none: an unchanged package's rise is a warning, not a finding", got.NewlyUncoveredChanged)
	}
	if len(warnings) != 1 {
		t.Fatalf("warnings = %#v, want exactly one", warnings)
	}
	if warnings[0] != (RatchetWarning{Package: ".", File: "app.go", Line: 10, Reason: ReasonNewlyUncoveredAtBase}) {
		t.Fatalf("warnings[0] = %#v, want {.  app.go 10 %q}", warnings[0], ReasonNewlyUncoveredAtBase)
	}
}

// TestEvaluateRatchetFailsWhenOnlyANonGoFixtureChangedInAPackageThatRose is
// review finding B1: a package counts as "changed" for the ratchet when the
// PR touches ANY file in it, not only *.go files. A table-test fixture that
// sits next to the code and that tests read (for example
// os.ReadFile("cases.json")) can change a package's coverage without any
// *.go file appearing in the diff at all — a package.json/go.mod/go.sum
// change, or a .s/.c/.syso asm/cgo file, is the same case. Narrowing
// package ownership to *.go files here (as `wb run --changed` correctly
// does for its own, different purpose of picking `go test`/`go vet`
// arguments) would let a rise like this one only warn instead of fail.
func TestEvaluateRatchetFailsWhenOnlyANonGoFixtureChangedInAPackageThatRose(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/app.go", StartLine: 10, StartCol: 1, EndLine: 12, EndCol: 2, Statements: 2, Count: 0},
	}
	changed := ChangedLines{} // the diff touched no .go line at all
	touched := map[string]bool{"cases.json": true}
	baseline := PackageBaseline{Packages: map[string]int{".": 1}}
	results, warnings := EvaluateRatchet(blocks, changed, touched, nil, baseline, "fixture.test/app")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	got := results[0]
	if !got.Changed {
		t.Fatal("Changed = false, want true: the PR touched cases.json in this package")
	}
	if got.Pass {
		t.Fatal("Pass = true, want false: a changed package's uncovered count rose (1 -> 2)")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none: this is a failure, not a warning", warnings)
	}
}

func TestEvaluateRatchetPassesForNewPackageWithNoBaseline(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/new/pkg.go", StartLine: 1, EndLine: 1, Statements: 1, Count: 1},
	}
	results, warnings := EvaluateRatchet(blocks, ChangedLines{}, nil, nil, PackageBaseline{Packages: map[string]int{}}, "fixture.test/app")
	if len(results) != 1 || !results[0].Pass || results[0].HasBaseline {
		t.Fatalf("results = %#v, want one passing package with no baseline entry", results)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
	}
}

// TestEvaluateRatchetFailsForChangedNewPackageWithUncoveredStatements is
// review-696 blocking #2: a changed package with no baseline entry used to
// skip the count rule entirely, so moving covered code into a brand-new
// package and dropping its test passed silently. The merge-base measurement
// covers every package that existed then, so a missing entry means the
// package did not exist, not that it is exempt: treat it as a baseline of 0.
func TestEvaluateRatchetFailsForChangedNewPackageWithUncoveredStatements(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/n/n.go", StartLine: 1, StartCol: 1, EndLine: 3, EndCol: 2, Statements: 3, Count: 0},
	}
	touched := map[string]bool{"n/n.go": true}
	results, warnings := EvaluateRatchet(blocks, ChangedLines{}, touched, nil, PackageBaseline{Packages: map[string]int{}}, "fixture.test/app")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	got := results[0]
	if !got.HasBaseline || got.BaselineUncovered != 0 {
		t.Fatalf("package ratchet = %#v, want a synthesized baseline of 0 for a changed package with no prior entry", got)
	}
	if got.Pass {
		t.Fatal("Pass = true, want false: 3 uncovered statements moved into a brand-new changed package must fail")
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none: a changed package's rise is a failure, not a warning", warnings)
	}
}

// TestEvaluateRatchetPassesForChangedNewPackageFullyCovered guards against
// over-correcting the blocking #2 fix above: a brand-new package with zero
// uncovered statements must still pass, since 0 is not greater than the
// synthesized baseline of 0.
func TestEvaluateRatchetPassesForChangedNewPackageFullyCovered(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "fixture.test/app/n/n.go", StartLine: 1, StartCol: 1, EndLine: 3, EndCol: 2, Statements: 3, Count: 1},
	}
	touched := map[string]bool{"n/n.go": true}
	results, warnings := EvaluateRatchet(blocks, ChangedLines{}, touched, nil, PackageBaseline{Packages: map[string]int{}}, "fixture.test/app")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	got := results[0]
	if !got.Pass {
		t.Fatalf("package ratchet = %#v, want Pass: a fully covered new package must not fail", got)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none", warnings)
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
	if blocks[0] != (CoverageBlock{File: "fixture.test/app/app.go", StartLine: 3, StartCol: 30, EndLine: 5, EndCol: 2, Statements: 1, Count: 1}) {
		t.Fatalf("blocks[0] = %#v", blocks[0])
	}
	if blocks[1] != (CoverageBlock{File: "fixture.test/app/app.go", StartLine: 7, StartCol: 34, EndLine: 10, EndCol: 2, Statements: 3, Count: 0}) {
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

// TestBaselineFromProfileSortsUncoveredBlocksByFileThenLineThenColumn covers
// BaselineFromProfile's tie-break comparator across all three of its
// dimensions: two uncovered blocks in the same package but different files
// (the File tie-break), and two uncovered blocks sharing a file and start
// line but different start columns (the StartCol tie-break, reached once
// File and StartLine are both equal).
func TestBaselineFromProfileSortsUncoveredBlocksByFileThenLineThenColumn(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "m/z.go", StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 5, Statements: 1, Count: 0},
		{File: "m/a.go", StartLine: 5, StartCol: 9, EndLine: 5, EndCol: 12, Statements: 1, Count: 0},
		{File: "m/a.go", StartLine: 5, StartCol: 2, EndLine: 5, EndCol: 5, Statements: 1, Count: 0},
	}
	baseline := BaselineFromProfile(blocks, "m", "deadbeef")
	got := baseline.UncoveredBlocks["."]
	if len(got) != 3 {
		t.Fatalf("uncovered blocks = %#v, want 3", got)
	}
	want := []UncoveredBlock{
		{File: "m/a.go", StartLine: 5, StartCol: 2, EndLine: 5, EndCol: 5},
		{File: "m/a.go", StartLine: 5, StartCol: 9, EndLine: 5, EndCol: 12},
		{File: "m/z.go", StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 5},
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("uncovered blocks[%d] = %#v, want %#v (want file, then line, then column order)", i, got[i], want[i])
		}
	}
}

func TestBaselineRoundTripsThroughWriteAndLoad(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	path := filepath.Join(dir, "baseline.json")
	want := PackageBaseline{SchemaVersion: 2, SHA: "deadbeef", Packages: map[string]int{"internal/quality": 4, ".": 0}}
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

	baseline, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{})
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

	_, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Nanosecond, RunOptions{})
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

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "does-not-exist", time.Minute, RunOptions{}); err == nil {
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

func TestGitTouchedFilesRejectsUnknownMergeBase(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.commitAll("base")
	if _, err := GitTouchedFiles(context.Background(), repo.dir, "does-not-exist"); err == nil {
		t.Fatal("want error for an unresolvable merge base")
	}
}

// TestGitTouchedFilesFailsWithoutExitErrorWhenGitCannotEvenStart exercises
// the non-*exec.ExitError branch, mirroring
// TestGitChangedLinesFailsWithoutExitErrorWhenGitCannotEvenStart.
func TestGitTouchedFilesFailsWithoutExitErrorWhenGitCannotEvenStart(t *testing.T) {
	t.Parallel()
	_, err := GitTouchedFiles(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "main")
	if err == nil {
		t.Fatal("want error when repoRoot does not exist")
	}
}

// TestGitTouchedFilesIncludesAPureDeletion is the review B1 case
// GitChangedLines cannot cover: deleting a file (or every line in it) adds
// no line, so it never appears in ChangedLines, but it must still count as
// "the PR touched this file" for per-package ratchet scoping.
func TestGitTouchedFilesIncludesAPureDeletion(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	baseSHA := repo.commitAll("base")

	repo.runGit("checkout", "-b", "feature")
	if err := os.Remove(filepath.Join(repo.dir, "app_test.go")); err != nil {
		t.Fatal(err)
	}
	repo.commitAll("delete app_test.go")

	touched, err := GitTouchedFiles(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if !touched["app_test.go"] {
		t.Fatalf("touched = %#v, want app_test.go present even though its deletion added no line", touched)
	}
}

// TestFileLineOffsetsMapCoversEveryBranch exercises Map directly against a
// hand-built two-hunk FileLineOffsets, covering: a line before every hunk
// (unshifted), a line inside a hunk's old range (unmappable), a line
// between hunks (shifted by the first hunk's net delta only), and a line
// after both hunks (shifted by both hunks' net deltas together).
func TestFileLineOffsetsMapCoversEveryBranch(t *testing.T) {
	t.Parallel()
	offsets := FileLineOffsets{hunks: []lineOffsetHunk{
		{oldStart: 10, oldCount: 2, newStart: 10, newCount: 5}, // net +3
		{oldStart: 20, oldCount: 3, newStart: 23, newCount: 0}, // net -3
	}}
	if newLine, ok := offsets.Map(5); !ok || newLine != 5 {
		t.Fatalf("Map(5) = (%d, %v), want (5, true): a line before every hunk is unshifted", newLine, ok)
	}
	if _, ok := offsets.Map(10); ok {
		t.Fatal("Map(10) ok = true, want false: line 10 falls inside the first hunk's old range")
	}
	if _, ok := offsets.Map(11); ok {
		t.Fatal("Map(11) ok = true, want false: line 11 falls inside the first hunk's old range")
	}
	if newLine, ok := offsets.Map(15); !ok || newLine != 18 {
		t.Fatalf("Map(15) = (%d, %v), want (18, true): shifted by the first hunk's net +3", newLine, ok)
	}
	if _, ok := offsets.Map(21); ok {
		t.Fatal("Map(21) ok = true, want false: line 21 falls inside the second hunk's old range")
	}
	if newLine, ok := offsets.Map(30); !ok || newLine != 30 {
		t.Fatalf("Map(30) = (%d, %v), want (30, true): shifted by both hunks' net deltas (+3 - 3 = 0)", newLine, ok)
	}
}

// TestAtoiOrDefaultParsesOrFallsBack covers all three of atoiOrDefault's
// outcomes: an empty string (the unified-diff header omitted the count,
// meaning 1), a valid number, and a malformed one.
func TestAtoiOrDefaultParsesOrFallsBack(t *testing.T) {
	t.Parallel()
	if got := atoiOrDefault("", 1); got != 1 {
		t.Fatalf("atoiOrDefault(\"\", 1) = %d, want 1", got)
	}
	if got := atoiOrDefault("7", 1); got != 7 {
		t.Fatalf("atoiOrDefault(\"7\", 1) = %d, want 7", got)
	}
	if got := atoiOrDefault("not-a-number", 1); got != 1 {
		t.Fatalf("atoiOrDefault(\"not-a-number\", 1) = %d, want the default 1", got)
	}
}

// TestGitLineOffsetsFixtureMapsAcrossAnInsertionAndADeletion is the
// review-696 non-blocking #3 mechanism end to end: a real PR that inserts a
// single line (a hunk header with an omitted old count, exercising
// atoiOrDefault's "empty means 1" branch) and deletes a whole block (a hunk
// with explicit counts on both sides) produces offsets whose Map correctly
// places a line before, between and after both hunks.
func TestGitLineOffsetsFixtureMapsAcrossAnInsertionAndADeletion(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	baseSHA := repo.commitAll("base")

	repo.runGit("checkout", "-b", "feature")
	// Insert one comment line before Add (line 2 -> +1 line), then delete
	// the blank line and Uncovered entirely (lines 6-11, wholly removed).
	edited := "package app\n\n// Add adds two numbers.\nfunc Add(a, b int) int {\n\treturn a + b\n}\n"
	repo.writeFile("app.go", edited)
	repo.commitAll("insert a comment, delete Uncovered")

	offsets, err := GitLineOffsets(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	fileOffsets, ok := offsets["app.go"]
	if !ok {
		t.Fatalf("offsets = %#v, want an entry for app.go", offsets)
	}
	// Line 1 (the package clause) sits before the insertion hunk: unshifted.
	if newLine, ok := fileOffsets.Map(1); !ok || newLine != 1 {
		t.Fatalf("Map(1) = (%d, %v), want (1, true)", newLine, ok)
	}
	// Line 6 (the blank line right before Uncovered) falls inside the
	// deletion hunk's old range: unmappable.
	if _, ok := fileOffsets.Map(6); ok {
		t.Fatal("Map(6) ok = true, want false: line 6 was deleted by the second hunk")
	}
}

// TestGitLineOffsetsFixtureIgnoresDevNullAndOrphanHunks exercises
// parseLineOffsets' two defensive branches with a real deletion diff: the
// "+++ /dev/null" file-deleted case clears currentFile, and every hunk that
// follows a cleared (or not-yet-seen) file header is skipped rather than
// attributed to the wrong file.
func TestGitLineOffsetsFixtureIgnoresDevNullAndOrphanHunks(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	baseSHA := repo.commitAll("base")

	repo.runGit("checkout", "-b", "feature")
	if err := os.Remove(filepath.Join(repo.dir, "app.go")); err != nil {
		t.Fatal(err)
	}
	repo.commitAll("delete app.go")

	offsets, err := GitLineOffsets(context.Background(), repo.dir, baseSHA)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := offsets["app.go"]; ok {
		t.Fatalf("offsets = %#v, want no entry for a wholly deleted file", offsets)
	}
}

func TestGitLineOffsetsRejectsUnknownMergeBase(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.commitAll("base")
	if _, err := GitLineOffsets(context.Background(), repo.dir, "does-not-exist"); err == nil {
		t.Fatal("want error for an unresolvable merge base")
	}
}

// TestGitLineOffsetsFailsWithoutExitErrorWhenGitCannotEvenStart exercises
// the non-*exec.ExitError branch, mirroring
// TestGitChangedLinesFailsWithoutExitErrorWhenGitCannotEvenStart.
func TestGitLineOffsetsFailsWithoutExitErrorWhenGitCannotEvenStart(t *testing.T) {
	t.Parallel()
	_, err := GitLineOffsets(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "main")
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
		// These two have a "." (so parsePosition passes its missing-dot
		// check) but a non-numeric line or column component, exercising
		// parsePosition's two strconv.Atoi error returns separately from
		// the "no dot at all" shape above.
		{"non-numeric start line", "file.go:x.2,3.4 1 1"},
		{"non-numeric start column", "file.go:1.x,3.4 1 1"},
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
	results, _ := EvaluateRatchet(blocks, changed, nil, nil, PackageBaseline{Packages: map[string]int{}}, "m")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want 1 package", results)
	}
	findings := results[0].NewlyUncoveredChanged
	if len(findings) != 3 {
		t.Fatalf("findings = %#v, want 3", findings)
	}
	want := []RatchetFinding{
		{File: "pkg/a.go", Line: 2, Reason: ReasonChangedStatementUncovered},
		{File: "pkg/a.go", Line: 9, Reason: ReasonChangedStatementUncovered},
		{File: "pkg/b.go", Line: 5, Reason: ReasonChangedStatementUncovered},
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
	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{}); err == nil {
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
	_, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{})
	if err == nil {
		t.Fatal("want error when go test fails to build")
	}
	if strings.Contains(err.Error(), "wall-time budget") {
		t.Fatalf("err = %v, want the generic go-test-failed message, not a timeout message", err)
	}
}

// A module with no _test.go files still produces a valid (all-zero-count)
// coverage profile: `go test -coverprofile` instruments and reports on every
// matched package regardless of whether it has its own tests. Real `go test`
// therefore never reaches ComputeBaselineAtRef's ParseCoverageProfile-error
// branch on its own (a matched build either fails, taking the "go test
// failed" branch above, or succeeds and always writes a well-formed
// profile); TestComputeBaselineAtRefFailsClosedWhenCoverageProfileIsMalformed
// exercises it with a `go` shim instead.

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

	_, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{})
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

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{}); err == nil {
		t.Fatal("want error when the temp directory cannot be created")
	}
}

func TestValidateBaselineRejectsWrongSchemaVersion(t *testing.T) {
	t.Parallel()
	// SchemaVersion 1 baselines carry counts only, not the per-package
	// uncovered-block identities EvaluateRatchet needs to attribute a
	// count-only rise to exact statements, so they are rejected the same
	// as any other unsupported version.
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 1, SHA: "abc", Packages: map[string]int{".": 0}}, "abc")
	if err == nil {
		t.Fatal("want error for an unsupported schema_version")
	}
}

func TestValidateBaselineRejectsEmptyPackages(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 2, SHA: "abc", Packages: map[string]int{}}, "abc")
	if err == nil {
		t.Fatal("want error for an empty package map")
	}
}

func TestValidateBaselineRejectsSHAMismatch(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 2, SHA: "deadbeef", Packages: map[string]int{".": 0}}, "abc")
	if err == nil {
		t.Fatal("want error when the baseline's sha does not match the merge base")
	}
}

func TestValidateBaselineAcceptsMatchingSHA(t *testing.T) {
	t.Parallel()
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 2, SHA: "abc", Packages: map[string]int{".": 0}}, "abc")
	if err != nil {
		t.Fatalf("want no error for a matching baseline, got %v", err)
	}
}

func TestValidateBaselineToleratesUnknownExpectedSHA(t *testing.T) {
	t.Parallel()
	// An empty expectedSHA means "no specific commit to check against"
	// (used when a caller has not yet resolved one); it must not itself
	// make an otherwise-usable baseline fail.
	err := ValidateBaseline(PackageBaseline{SchemaVersion: 2, SHA: "whatever", Packages: map[string]int{".": 0}}, "")
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

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{}); err == nil {
		t.Fatal("want error when the measured profile fails ParseCoverageProfile's stricter parse")
	}
}

// TestComputeBaselineAtRefFailsClosedWhenRepositoryQualityPolicyIsMalformed
// covers ComputeBaselineAtRef's RepositoryRunOptions error branch (review
// item 3: the merge-base fallback now reuses the sharded, policy-aware
// runner, so a malformed .wb/quality.yaml checked out at the ref must fail
// the baseline measurement the same way it fails the head measurement).
func TestComputeBaselineAtRefFailsClosedWhenRepositoryQualityPolicyIsMalformed(t *testing.T) {
	t.Parallel()
	repo := newFixtureRepo(t)
	repo.writeFile("app.go", fixtureBaseSource)
	repo.writeFile("app_test.go", fixtureTestSource)
	// go_test.shards below the required minimum of 2 is a malformed policy
	// RepositoryRunOptions rejects (internal/quality/config.go).
	repo.writeFile(".wb/quality.yaml", "version: 1\ngo_test:\n  shards: 1\n  packages: [\"./...\"]\n")
	repo.commitAll("base with a malformed repository quality policy")

	if _, err := ComputeBaselineAtRef(context.Background(), repo.dir, "HEAD", time.Minute, RunOptions{}); err == nil {
		t.Fatal("want error when the ref's own .wb/quality.yaml is malformed")
	}
}

// TestEvaluateRatchetNamesFileLineForACountOnlyRise is the review B2
// regression: a package that fails only on its count (no changed line
// overlaps a diff hunk, as when a PR deletes the package's only test) must
// still report the exact file:line of the newly uncovered statement, not
// just a count.
func TestEvaluateRatchetNamesFileLineForACountOnlyRise(t *testing.T) {
	t.Parallel()
	baseBlocks := []CoverageBlock{
		{File: "m/pkg/a.go", StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 10, Statements: 1, Count: 1},
	}
	baseline := BaselineFromProfile(baseBlocks, "m", "base-sha")

	// No overlapping changed lines at all: this models deleting the
	// package's only test, which raises the uncovered count without
	// touching any line a diff would flag.
	currentBlocks := []CoverageBlock{
		{File: "m/pkg/a.go", StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 10, Statements: 1, Count: 0},
	}
	// pkg/a_test.go (not a.go itself) is what the PR deleted: enough to make
	// "pkg" a changed package, while leaving a.go's block position
	// trustworthy for the exact-match attribution below.
	touched := map[string]bool{"pkg/a_test.go": true}
	results, warnings := EvaluateRatchet(currentBlocks, ChangedLines{}, touched, nil, baseline, "m")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none: a changed package's rise is a failure, not a warning", warnings)
	}
	got := results[0]
	if !got.Rose || got.Pass {
		t.Fatalf("package ratchet = %#v, want Rose and not Pass", got)
	}
	if len(got.NewlyUncoveredChanged) != 1 {
		t.Fatalf("NewlyUncoveredChanged = %#v, want exactly one finding even though no diff line overlaps", got.NewlyUncoveredChanged)
	}
	finding := got.NewlyUncoveredChanged[0]
	if finding.File != "pkg/a.go" || finding.Line != 5 {
		t.Fatalf("finding = %#v, want pkg/a.go:5", finding)
	}
}

// TestEvaluateRatchetDoesNotBlameLineShiftedBlocksInFilesThePRTouched is a
// regression for a false positive review-696 CI hit for real: inserting
// lines anywhere in a file the PR edits shifts every later uncovered
// block's line number in that same file, so it can no longer exact-match
// the baseline's stored position — the Rose-fallback must not mistake that
// shifted, pre-existing statement for a newly uncovered one. A genuinely
// new uncovered statement in a different, untouched file in the same
// package must still be named.
func TestEvaluateRatchetDoesNotBlameLineShiftedBlocksInFilesThePRTouched(t *testing.T) {
	t.Parallel()
	baseline := BaselineFromProfile([]CoverageBlock{
		{File: "m/pkg/a.go", StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 10, Statements: 1, Count: 0},
	}, "m", "base-sha")

	currentBlocks := []CoverageBlock{
		// Same statement as the baseline's, only shifted to line 20 by an
		// edit earlier in a.go (which this PR made).
		{File: "m/pkg/a.go", StartLine: 20, StartCol: 1, EndLine: 20, EndCol: 10, Statements: 1, Count: 0},
		// A genuinely new uncovered statement, in a file the PR did not
		// touch at all.
		{File: "m/pkg/b.go", StartLine: 3, StartCol: 1, EndLine: 3, EndCol: 10, Statements: 1, Count: 0},
	}
	touched := map[string]bool{"pkg/a.go": true}
	// A single 15-line insertion before the old line 5 (an earlier edit in
	// a.go this PR made) maps the baseline's line 5 to the current line 20,
	// exactly where currentBlocks says a.go's uncovered block now sits.
	offsets := map[string]FileLineOffsets{
		"pkg/a.go": {hunks: []lineOffsetHunk{{oldStart: 5, oldCount: 0, newStart: 5, newCount: 15}}},
	}
	results, _ := EvaluateRatchet(currentBlocks, ChangedLines{}, touched, offsets, baseline, "m")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	got := results[0]
	if !got.Rose || got.Pass {
		t.Fatalf("package ratchet = %#v, want Rose and not Pass", got)
	}
	if len(got.NewlyUncoveredChanged) != 1 || got.NewlyUncoveredChanged[0] != (RatchetFinding{File: "pkg/b.go", Line: 3, Reason: ReasonNewlyUncoveredAtBase}) {
		t.Fatalf("NewlyUncoveredChanged = %#v, want only pkg/b.go:3 (a.go's shifted-but-pre-existing block must not be blamed)", got.NewlyUncoveredChanged)
	}
}

// TestEvaluateRatchetDedupesAFindingReportedByBothTheDirectAndRoseRules
// covers report()'s "already reported" early return: a statement in a file
// the PR did not itself touch can be caught both by the direct
// changed-line rule and by the Rose-fallback block-diff (its package is
// "changed" via a different file), and must appear once, not twice.
func TestEvaluateRatchetDedupesAFindingReportedByBothTheDirectAndRoseRules(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "m/pkg/a.go", StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 10, Statements: 1, Count: 0},
	}
	changed := ChangedLines{"pkg/a.go": {5: true}}
	baseline := PackageBaseline{Packages: map[string]int{"pkg": 0}}
	// pkg/other.go (not a.go) is what the PR touched, so "pkg" is a changed
	// package, but a.go's own block position is still trustworthy.
	touched := map[string]bool{"pkg/other.go": true}
	results, warnings := EvaluateRatchet(blocks, changed, touched, nil, baseline, "m")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	if len(warnings) != 0 {
		t.Fatalf("warnings = %#v, want none: pkg is a changed package", warnings)
	}
	if len(results[0].NewlyUncoveredChanged) != 1 {
		t.Fatalf("NewlyUncoveredChanged = %#v, want exactly one finding (deduped, not reported twice)", results[0].NewlyUncoveredChanged)
	}
}

// TestEvaluateRatchetSortsWarningsByPackageThenFileThenLine covers the
// warnings slice's full three-way sort comparator.
func TestEvaluateRatchetSortsWarningsByPackageThenFileThenLine(t *testing.T) {
	t.Parallel()
	blocks := []CoverageBlock{
		{File: "m/y/a.go", StartLine: 9, StartCol: 1, EndLine: 9, EndCol: 2, Statements: 1, Count: 0},
		{File: "m/x/b.go", StartLine: 1, StartCol: 1, EndLine: 1, EndCol: 2, Statements: 1, Count: 0},
		{File: "m/x/a.go", StartLine: 9, StartCol: 1, EndLine: 9, EndCol: 2, Statements: 1, Count: 0},
		{File: "m/x/a.go", StartLine: 2, StartCol: 1, EndLine: 2, EndCol: 2, Statements: 1, Count: 0},
	}
	baseline := PackageBaseline{Packages: map[string]int{"x": 0, "y": 0}}
	// touchedFiles is nil: neither package is one the PR changes, so every
	// rise becomes a warning, exercising all three tie-break levels.
	_, warnings := EvaluateRatchet(blocks, ChangedLines{}, nil, nil, baseline, "m")
	want := []RatchetWarning{
		{Package: "x", File: "x/a.go", Line: 2, Reason: ReasonNewlyUncoveredAtBase},
		{Package: "x", File: "x/a.go", Line: 9, Reason: ReasonNewlyUncoveredAtBase},
		{Package: "x", File: "x/b.go", Line: 1, Reason: ReasonNewlyUncoveredAtBase},
		{Package: "y", File: "y/a.go", Line: 9, Reason: ReasonNewlyUncoveredAtBase},
	}
	if len(warnings) != len(want) {
		t.Fatalf("warnings = %#v, want %#v", warnings, want)
	}
	for i := range want {
		if warnings[i] != want[i] {
			t.Fatalf("warnings[%d] = %#v, want %#v", i, warnings[i], want[i])
		}
	}
}

// TestEvaluateRatchetDoesNotRefindABlockTheBaselineAlreadyHad guards
// against over-reporting: a package whose count rose because of one truly
// new uncovered block must not also re-report every pre-existing uncovered
// block that the baseline already recorded.
func TestEvaluateRatchetDoesNotRefindABlockTheBaselineAlreadyHad(t *testing.T) {
	t.Parallel()
	baseBlocks := []CoverageBlock{
		{File: "m/pkg/a.go", StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 10, Statements: 1, Count: 0},
	}
	baseline := BaselineFromProfile(baseBlocks, "m", "base-sha")

	currentBlocks := []CoverageBlock{
		{File: "m/pkg/a.go", StartLine: 5, StartCol: 1, EndLine: 5, EndCol: 10, Statements: 1, Count: 0},
		{File: "m/pkg/a.go", StartLine: 9, StartCol: 1, EndLine: 9, EndCol: 10, Statements: 1, Count: 0},
	}
	touched := map[string]bool{"pkg/a_test.go": true}
	results, _ := EvaluateRatchet(currentBlocks, ChangedLines{}, touched, nil, baseline, "m")
	if len(results) != 1 {
		t.Fatalf("results = %#v, want one package", results)
	}
	got := results[0]
	if len(got.NewlyUncoveredChanged) != 1 || got.NewlyUncoveredChanged[0].Line != 9 {
		t.Fatalf("NewlyUncoveredChanged = %#v, want only line 9 (the pre-existing line-5 block must not be re-reported)", got.NewlyUncoveredChanged)
	}
}
