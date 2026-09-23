package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
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
	wantFile := repo.modulePath + "/app.go"
	if !strings.Contains(stderr.String(), wantFile) {
		t.Fatalf("stderr = %q, want it to name %s", stderr.String(), wantFile)
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
	baseline := quality.PackageBaseline{SchemaVersion: 1, SHA: baseSHA, Packages: map[string]int{".": 100}}
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

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when go test fails to build")
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
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 1, Packages: map[string]int{".": 3}}); err != nil {
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
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 1, Packages: map[string]int{".": 3}}); err != nil {
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
	if err := quality.WriteBaseline(baselinePath, quality.PackageBaseline{SchemaVersion: 1, Packages: map[string]int{".": 3}}); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--target", baseSHA, "--baseline-file", baselinePath, "--minimum=0", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0 under a --minimum of 0\nstderr:\n%s", code, stderr.String())
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
	previous, hadPrevious := os.LookupEnv("TMPDIR")
	if err := os.Setenv("TMPDIR", notADir); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if hadPrevious {
			_ = os.Setenv("TMPDIR", previous)
		} else {
			_ = os.Unsetenv("TMPDIR")
		}
	}()

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
