package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

// ratchetFixtureRepo builds a tiny real git repository containing a
// one-package Go module, so `wb coverage --changed` CLI tests exercise the
// real command surface (real git, real go test) rather than a mock of it.
type ratchetFixtureRepo struct {
	t          *testing.T
	dir        string
	modulePath string
}

func newRatchetFixtureRepo(t *testing.T) *ratchetFixtureRepo {
	t.Helper()
	dir := t.TempDir()
	repo := &ratchetFixtureRepo{t: t, dir: dir, modulePath: "fixture.test/cliapp"}
	runCommand(t, dir, "git", "init", "-q", "--initial-branch=main")
	runCommand(t, dir, "git", "config", "user.email", "fixture@example.com")
	runCommand(t, dir, "git", "config", "user.name", "Fixture")
	repo.writeFile("go.mod", "module "+repo.modulePath+"\n\ngo 1.27\n")
	return repo
}

func (r *ratchetFixtureRepo) writeFile(relativePath, contents string) {
	r.t.Helper()
	full := filepath.Join(r.dir, relativePath)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		r.t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
		r.t.Fatal(err)
	}
}

func (r *ratchetFixtureRepo) commitAll(message string) string {
	r.t.Helper()
	runCommand(r.t, r.dir, "git", "add", "-A")
	runCommand(r.t, r.dir, "git", "-c", "user.email=fixture@example.com", "-c", "user.name=Fixture", "commit", "-qm", message)
	return runCommand(r.t, r.dir, "git", "rev-parse", "HEAD")
}

const ratchetFixtureBaseSource = `package app

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

const ratchetFixtureTestSource = `package app

import "testing"

func TestAdd(t *testing.T) {
	if Add(1, 2) != 3 {
		t.Fatal("Add(1, 2) != 3")
	}
}
`

func TestCoverageChangedFixturePRMovingUncoveredFunctionUnchangedPasses(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
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
	repo.writeFile("app.go", moved)
	repo.commitAll("move Uncovered above Add")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0 (pure move of an uncovered function must pass)\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "PASS") {
		t.Fatalf("stdout = %q, want it to report PASS", stdout.String())
	}
}

func TestCoverageChangedFixturePRAddingUncoveredStatementFailsAndNamesLine(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("app.go", ratchetFixtureBaseSource+"\nfunc NewlyAdded(a, b int) int {\n\treturn a * b\n}\n")
	repo.commitAll("add NewlyAdded, untested")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatalf("code = 0, want nonzero: an added uncovered statement must fail\nstdout:\n%s", stdout.String())
	}
	// The finding names the repo-relative path git diff uses ("app.go"), not
	// the module-qualified import path ("fixture.test/cliapp/app.go").
	if !strings.Contains(stderr.String(), "app.go:") {
		t.Fatalf("stderr = %q, want it to name app.go:<line>", stderr.String())
	}
	if strings.Contains(stderr.String(), repo.modulePath+"/app.go") {
		t.Fatalf("stderr = %q, want the repo-relative path, not the module-qualified import path", stderr.String())
	}
}

func TestCoverageChangedRejectsUnknownTarget(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.commitAll("base")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", "does-not-exist", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for an unresolvable --target")
	}
}

func TestCoverageChangedUsesPublishedBaselineFileWhenPresent(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("app.go", ratchetFixtureBaseSource+"\nfunc NewlyAdded(a, b int) int {\n\treturn a * b\n}\n")
	repo.commitAll("add NewlyAdded, untested")

	// A baseline claiming a much higher already-uncovered count must not by
	// itself make the run pass: the changed-line rule still fires.
	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	baseline := quality.PackageBaseline{SchemaVersion: 2, SHA: baseSHA, Packages: map[string]int{".": 100}}
	if err := quality.WriteBaseline(baselinePath, baseline); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero: the changed-line rule fires regardless of a generous baseline")
	}
	if !strings.Contains(stdout.String(), "baseline 100") {
		t.Fatalf("stdout = %q, want it to reflect the published baseline (100), not a freshly measured one", stdout.String())
	}
}

func TestCoverageChangedRejectsMalformedBaselineFile(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := os.WriteFile(baselinePath, []byte("not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a malformed --baseline-file")
	}
	if !strings.Contains(stderr.String(), baselinePath) {
		t.Fatalf("stderr = %q, want it to name the malformed baseline file", stderr.String())
	}
}

func TestCoverageChangedFailsWhenGoTestFails(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", "package app\n\nfunc Broken() int {\n") // syntax error
	baseSHA := repo.commitAll("broken")

	// --report-dir exercises the same quality.CoverWithOptions failure path
	// the sharded coverage-diagnostics manifest uses (review item 5);
	// runChangedCoverage never surfaces that manifest, since --changed
	// cannot combine with --test-shards (validateCoverageExecutionOptions),
	// so no manifest is ever written for it to find.
	reportDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--report-dir", reportDir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when go test fails to build")
	}
	if !strings.Contains(stderr.String(), "coverage could not be measured") {
		t.Fatalf("stderr = %q, want it to report the measurement failure", stderr.String())
	}
}

func TestCoverageChangedEnforcesMinimumBackstop(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	// No source changes at all: the ratchet itself passes (nothing changed,
	// nothing rose), but the aggregate is well under a 100% --minimum.
	repo.writeFile("README.md", "unrelated\n")
	repo.commitAll("unrelated doc change")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 2, Packages: map[string]int{".": 3}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--minimum=100", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero: the --minimum backstop still applies under --changed")
	}
	if !strings.Contains(stderr.String(), "is below required") {
		t.Fatalf("stderr = %q, want the --minimum backstop message", stderr.String())
	}
}

func TestCoverageChangedJSONFormatAndReportDir(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "unrelated\n")
	repo.commitAll("unrelated doc change")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 2, Packages: map[string]int{".": 3}}); err != nil {
		t.Fatal(err)
	}
	reportDir := t.TempDir()

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--format", "json", "--report-dir", reportDir, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	var report changedCoverageReport
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not valid JSON: %v\n%s", err, stdout.String())
	}
	if report.MergeBase != baseSHA {
		t.Fatalf("report.MergeBase = %q, want %q", report.MergeBase, baseSHA)
	}
	reportFile := filepath.Join(reportDir, "coverage-ratchet.json")
	if _, err := os.Stat(reportFile); err != nil {
		t.Fatalf("want %s to exist: %v", reportFile, err)
	}
}

func TestCoverageBaselineWritesPerPackageUncoveredCounts(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture.test/base\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	profilePath := filepath.Join(dir, "profile.out")
	profile := "mode: set\n" +
		"fixture.test/base/a.go:3.10,5.2 2 1\n" +
		"fixture.test/base/pkg/b.go:9.10,11.2 4 0\n"
	if err := os.WriteFile(profilePath, []byte(profile), 0o644); err != nil {
		t.Fatal(err)
	}
	outPath := filepath.Join(dir, "baseline.json")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "baseline", profilePath, "--module", dir, "--sha", "abc123", "--out", outPath, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	baseline, err := quality.LoadBaseline(outPath)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.SHA != "abc123" {
		t.Fatalf("baseline.SHA = %q, want abc123", baseline.SHA)
	}
	if baseline.Packages["."] != 0 || baseline.Packages["pkg"] != 4 {
		t.Fatalf("baseline.Packages = %#v, want {.: 0, pkg: 4}", baseline.Packages)
	}
}

func TestCoverageBaselineRejectsMissingProfile(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module fixture.test/base\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "baseline", filepath.Join(dir, "missing.out"), "--module", dir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a missing coverage profile")
	}
}

func TestCoverageBaselineRejectsMissingGoMod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "baseline", profilePath, "--module", dir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when --module has no go.mod")
	}
}

func TestCoverageChangedRejectsMissingGoModAtRepoPath(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	runCommand(t, dir, "git", "init", "-q", "--initial-branch=main")
	runCommand(t, dir, "git", "config", "user.email", "fixture@example.com")
	runCommand(t, dir, "git", "config", "user.name", "Fixture")
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runCommand(t, dir, "git", "add", "-A")
	runCommand(t, dir, "git", "commit", "-qm", "no go.mod here")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", dir, "--changed", "--target", "main", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when the repository has no go.mod")
	}
	if !strings.Contains(stderr.String(), "requires exactly one Go module") {
		t.Fatalf("stderr = %q, want it to explain the missing module", stderr.String())
	}
}

func TestCoverageChangedFallsBackToMeasuringMergeBaseWhenBaselineFileIsMissing(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
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
	repo.writeFile("app.go", moved)
	repo.commitAll("move Uncovered above Add")

	missingBaseline := filepath.Join(t.TempDir(), "no-such-baseline.json")
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", missingBaseline, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "measuring merge base") {
		t.Fatalf("stderr = %q, want it to report falling back to measuring the merge base", stderr.String())
	}
}

func TestCoverageChangedPassesUnderALenientMinimum(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "unrelated\n")
	repo.commitAll("unrelated doc change")

	baselinePath := filepath.Join(t.TempDir(), "baseline.json")
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 2, Packages: map[string]int{".": 3}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--minimum=0", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0 under a --minimum of 0\nstderr:\n%s", code, stderr.String())
	}
}

// TestWriteChangedCoverageOutputToPrintsWarningsInMarkdownFormat covers the
// markdown/text renderer's warnings loop (founder decision 2026-09-23,
// review B1): a count rise in a package the PR did not change prints as a
// WARN line naming its package and file:line, separate from the pass/fail
// package lines above it.
func TestWriteChangedCoverageOutputToPrintsWarningsInMarkdownFormat(t *testing.T) {
	t.Parallel()
	report := changedCoverageReport{
		MergeBase: "deadbeef",
		Target:    "main",
		Packages: []quality.PackageRatchet{
			{Package: ".", Uncovered: 1, HasBaseline: true, BaselineUncovered: 1, Pass: true},
		},
		Warnings: []quality.RatchetWarning{
			{Package: "legacy", File: "legacy/old.go", Line: 42},
		},
	}
	var out bytes.Buffer
	if err := writeChangedCoverageOutputTo(&out, report, "markdown", ""); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	if !strings.Contains(got, "WARN legacy legacy/old.go:42") {
		t.Fatalf("output = %q, want a WARN line naming the package and file:line", got)
	}
}

func TestWriteChangedCoverageOutputToFailsClosedWhenReportDirIsBlocked(t *testing.T) {
	t.Parallel()
	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(blocker, "reports") // blocker is a file, not a directory
	var out bytes.Buffer
	err := writeChangedCoverageOutputTo(&out, changedCoverageReport{}, "markdown", reportDir)
	if err == nil {
		t.Fatal("want error when the report directory's parent is a regular file")
	}
}

func TestWriteChangedCoverageOutputToFailsClosedWhenReportFileIsBlocked(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	// Pre-create the destination filename as a directory so the write fails.
	if err := os.MkdirAll(filepath.Join(reportDir, "coverage-ratchet.json"), 0o755); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	err := writeChangedCoverageOutputTo(&out, changedCoverageReport{}, "markdown", reportDir)
	if err == nil {
		t.Fatal("want error when the report file path is already a directory")
	}
}

func TestGitMergeBaseFailsWithoutExitErrorWhenGitCannotEvenStart(t *testing.T) {
	t.Parallel()
	_, err := gitMergeBase(context.Background(), filepath.Join(t.TempDir(), "does-not-exist"), "main")
	if err == nil {
		t.Fatal("want error when repoPath does not exist")
	}
}

// TestCoverageChangedFailsClosedWhenTempProfileCannotBeCreated points TMPDIR
// at a regular file so os.CreateTemp fails. Not parallel-safe (mutates
// process-wide TMPDIR), so it runs serially and always restores it.
func TestCoverageChangedFailsClosedWhenTempProfileCannotBeCreated(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	notADir := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(notADir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	// t.Setenv (not os.Setenv) restores TMPDIR automatically and asserts
	// this test never runs in parallel.
	t.Setenv("TMPDIR", notADir)

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when the temp coverage profile cannot be created")
	}
}

func TestCoverageChangedFailsClosedWhenReportDirIsBlocked(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	blocker := filepath.Join(t.TempDir(), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	reportDir := filepath.Join(blocker, "reports")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--report-dir", reportDir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when --report-dir cannot be created")
	}
}

// TestCoverageChangedFailsClosedWhenWorkingDirectoryIsGone exercises
// runChangedCoverage's filepath.Abs error branch: Abs only calls os.Getwd
// for a relative path, and only a removed working directory makes Getwd
// fail. Not parallel-safe (mutates the process-wide working directory), so
// it runs serially and always restores it.
func TestCoverageChangedFailsClosedWhenWorkingDirectoryIsGone(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := os.Chdir(original); err != nil {
			t.Fatal(err)
		}
	}()

	gone := filepath.Join(t.TempDir(), "gone")
	if err := os.Mkdir(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(gone); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(gone); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", ".", "--changed", "--target", "main", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when the working directory has been removed")
	}
}

// TestCoverageChangedFailsClosedWhenGitDiffCannotRun exercises
// runChangedCoverage's quality.GitChangedLines error branch with a `git`
// shim that fails only its `diff` invocation and forwards every other
// subcommand (merge-base, rev-parse, ...) to the real binary, so
// gitMergeBase still succeeds and GitChangedLines fails on its own. Not
// parallel-safe (t.Setenv mutates the process-wide PATH).
func TestCoverageChangedFailsClosedWhenGitDiffCannotRun(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")
	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "x\n")
	repo.commitAll("doc change")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = diff ]; then\n" +
		"    echo 'fake git diff failure' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when git diff cannot run")
	}
	if !strings.Contains(stderr.String(), "fake git diff failure") {
		t.Fatalf("stderr = %q, want it to surface the git diff failure", stderr.String())
	}
}

// TestCoverageChangedFailsClosedWhenGitTouchedFilesCannotRun exercises
// runChangedCoverage's quality.GitTouchedFiles error branch specifically: a
// `git` shim that only fails the `--name-only` invocation (GitTouchedFiles),
// forwarding every other diff (GitChangedLines' own -U0 --color-moved run,
// which must succeed first) and every other subcommand to the real binary.
// Not parallel-safe (t.Setenv mutates the process-wide PATH).
func TestCoverageChangedFailsClosedWhenGitTouchedFilesCannotRun(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")
	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "x\n")
	repo.commitAll("doc change")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = --name-only ]; then\n" +
		"    echo 'fake git diff --name-only failure' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	shimDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when git diff --name-only cannot run")
	}
	if !strings.Contains(stderr.String(), "fake git diff --name-only failure") {
		t.Fatalf("stderr = %q, want it to surface the git diff --name-only failure", stderr.String())
	}
}

// TestCoverageChangedFailsClosedOnMalformedRepositoryQualityPolicy exercises
// runChangedCoverage's quality.RepositoryRunOptions error branch: --changed
// now reuses the same .wb/quality.yaml-aware sharded runner the plain
// `wb coverage` path uses, so a malformed policy must fail it closed too.
func TestCoverageChangedFailsClosedOnMalformedRepositoryQualityPolicy(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.writeFile(".wb/quality.yaml", "version: 2\n")
	baseSHA := repo.commitAll("base")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a malformed .wb/quality.yaml policy")
	}
	if !strings.Contains(stderr.String(), "quality.yaml") {
		t.Fatalf("stderr = %q, want it to name the malformed policy file", stderr.String())
	}
}

// TestCoverageChangedFailsClosedWhenCoverageProfileIsMalformed exercises
// runChangedCoverage's quality.ParseCoverageProfile error branch after a
// successful quality.CoverWithOptions run. profileTotals (used inside
// CoverWithOptions to decide pass/fail) accepts any three
// whitespace-separated fields with numeric counts; ParseCoverageProfile
// additionally requires field 1 to be
// "file:startLine.startCol,endLine.endCol". This `go` shim fakes `go test
// -coverprofile` to write a profile that satisfies the first parser but not
// the second, so CoverWithOptions reports success while the ratchet's own
// stricter parse still fails closed. Not parallel-safe (t.Setenv mutates
// the process-wide PATH).
func TestCoverageChangedFailsClosedWhenCoverageProfileIsMalformed(t *testing.T) {
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")
	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	repo.writeFile("README.md", "x\n")
	repo.commitAll("doc change")

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

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when the coverage profile fails the ratchet's stricter parse")
	}
	if !strings.Contains(stderr.String(), "produced by go test") {
		t.Fatalf("stderr = %q, want it to name the parse failure", stderr.String())
	}
}

// TestCoverageChangedRejectsFormatsOtherThanMarkdownOrJSON is the CLI-level
// counterpart of --changed's ignored-flags rule (AGENTS.md): an unsupported
// --format must be a usage error, not silently printed as plain text.
func TestCoverageChangedRejectsFormatsOtherThanMarkdownOrJSON(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	baseSHA := repo.commitAll("base")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--format", "yaml", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("code = %d, want %d (usage error) for an unsupported --format under --changed", code, exitUsage)
	}
}

// TestCoverageRejectsBaselineTimeoutWithoutChanged is the CLI-level
// counterpart of --baseline-timeout's ignored-flags rule (AGENTS.md):
// --baseline-timeout has no effect outside --changed and must be rejected,
// not silently accepted.
func TestCoverageRejectsBaselineTimeoutWithoutChanged(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.commitAll("base")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--baseline-timeout", "5m", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("code = %d, want %d (usage error) for --baseline-timeout without --changed", code, exitUsage)
	}
}

// TestValidateBaselineRejectsUnusableArtifacts is the CLI-level regression
// for review item 3: a baseline that parses as JSON but is unusable (wrong
// schema, empty, or measured for a different commit) must never pass the
// ratchet silently. Each fixture PR raises a package's uncovered count from
// 0 to 1 with no non-test line changed, so only the count rule can catch
// it; a baseline treated as valid-but-generous would let it through.
func TestCoverageChangedFailsClosedOnUnusableBaselineArtifacts(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		contents string
	}{
		{name: "empty object", contents: "{}"},
		{name: "wrong schema version and sha", contents: `{"schema_version":2,"sha":"deadbeef","packages":{".":0}}`},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			repo := newRatchetFixtureRepo(t)
			repo.writeFile("app.go", ratchetFixtureBaseSource)
			repo.writeFile("app_test.go", ratchetFixtureTestSource)
			baseSHA := repo.commitAll("base")

			runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
			// Delete the package's only test: the uncovered count goes from
			// 0 to 1 and no non-test line changes, so only the baseline
			// count rule can catch it.
			if err := os.Remove(filepath.Join(repo.dir, "app_test.go")); err != nil {
				t.Fatal(err)
			}
			repo.commitAll("delete the only test")

			baselinePath := filepath.Join(t.TempDir(), "baseline.json")
			if err := os.WriteFile(baselinePath, []byte(c.contents), 0o644); err != nil {
				t.Fatal(err)
			}

			var stdout, stderr bytes.Buffer
			code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--non-interactive"}, &stdout, &stderr)
			if code == 0 {
				t.Fatalf("code = 0, want nonzero: an unusable baseline (%s) must not silently pass a rising uncovered count\nstdout:\n%s\nstderr:\n%s", c.name, stdout.String(), stderr.String())
			}
			if !strings.Contains(stderr.String(), "rose above baseline") {
				t.Fatalf("stderr = %q, want it to report the uncovered count rising once the unusable baseline is discarded", stderr.String())
			}
		})
	}
}

// TestCoverageChangedSurfacesTheShardDiagnosticManifestOnFailure is the
// review non-blocking #3 regression: quality.RepositoryRunOptions applies
// .wb/quality.yaml's own go_test.shards policy independently of --changed's
// CLI --test-shards restriction, so a repository that shards a package can
// still fail with a coverage-diagnostics manifest on disk, and
// runChangedCoverage must still point at it.
func TestCoverageChangedSurfacesTheShardDiagnosticManifestOnFailure(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.writeFile("pkg/pkg.go", "package pkg\n\nfunc Foo() int { return 1 }\n")
	repo.writeFile("pkg/pkg_test.go", "package pkg\n\nimport \"testing\"\n\nfunc TestFoo(t *testing.T) {\n\tt.Fatal(\"boom\")\n}\n")
	repo.writeFile(".wb/quality.yaml", "version: 1\ngo_test:\n  shards: 2\n  packages: [\"./pkg\"]\n")
	baseSHA := repo.commitAll("base")

	reportDir := t.TempDir()
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--report-dir", reportDir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when a sharded package's test fails")
	}
	if !strings.Contains(stderr.String(), "diagnostic manifest") {
		t.Fatalf("stderr = %q, want it to point at the coverage-diagnostics manifest", stderr.String())
	}
}
