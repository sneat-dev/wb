package main

import (
	"bytes"
	"encoding/json"
	"github.com/sneat-dev/wb/internal/quality"
	"github.com/sneat-dev/wb/internal/qualityrun"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	var report qualityrun.ChangedReport
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
