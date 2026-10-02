//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/testenv"
)

// changedPackagesOnlyFixture is a two-package module whose second package
// ("other") has a test that always fails: any run that measures it fails, so a
// passing scoped run proves it was left alone.
func changedPackagesOnlyFixture(t *testing.T) (repo *ratchetFixtureRepo, baseSHA string) {
	t.Helper()
	repo = newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.writeFile("app_test.go", ratchetFixtureTestSource)
	repo.writeFile("other/other.go", "package other\n\nfunc Value() int { return 1 }\n")
	repo.writeFile("other/other_test.go", "package other\n\nimport \"testing\"\n\nfunc TestAlwaysFails(t *testing.T) { t.Fatal(\"unrelated package was measured\") }\n")
	baseSHA = repo.commitAll("base")
	runCommand(t, repo.dir, "git", "checkout", "-q", "-b", "feature")
	return repo, baseSHA
}

const changedPackagesOnlyMovedSource = `package app

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

func TestE2ECoverageChangedPackagesOnlyMeasuresTheTouchedPackageAndSaysWhatItSkipped(t *testing.T) {
	t.Parallel()
	repo, baseSHA := changedPackagesOnlyFixture(t)
	repo.writeFile("app.go", changedPackagesOnlyMovedSource)
	repo.commitAll("move Uncovered above Add")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0: the failing unrelated package must not be measured\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	for _, want := range []string{"PASS", "Only the packages this diff touches were measured", "CI's full run remains the gate", "measured: ."} {
		if !strings.Contains(stdout.String(), want) {
			t.Fatalf("stdout = %q, want it to contain %q", stdout.String(), want)
		}
	}
	if strings.Contains(stdout.String(), "other") {
		t.Fatalf("stdout = %q, the unrelated package must not appear", stdout.String())
	}
}

func TestE2ECoverageChangedPackagesOnlyStillFailsAnUncoveredAddedStatement(t *testing.T) {
	t.Parallel()
	repo, baseSHA := changedPackagesOnlyFixture(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource+"\nfunc NewlyAdded(a, b int) int {\n\treturn a * b\n}\n")
	repo.commitAll("add NewlyAdded, untested")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFindings, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "app.go:") {
		t.Fatalf("stderr = %q, want it to name app.go:<line>", stderr.String())
	}
}

// A package the change adds has no directory at the merge base; its baseline
// is measured over the packages that existed there and the new one counts as
// a baseline of zero.
func TestE2ECoverageChangedPackagesOnlyMeasuresAPackageTheChangeAdds(t *testing.T) {
	t.Parallel()
	repo, baseSHA := changedPackagesOnlyFixture(t)
	repo.writeFile("fresh/fresh.go", "package fresh\n\nfunc Untested() int { return 2 }\n")
	repo.commitAll("add an untested package")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code != exitFindings {
		t.Fatalf("code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitFindings, stdout.String(), stderr.String())
	}
	if !strings.Contains(stderr.String(), "fresh/fresh.go:") {
		t.Fatalf("stderr = %q, want it to name fresh/fresh.go:<line>", stderr.String())
	}
}

func TestE2ECoverageChangedPackagesOnlyWithNoGoPackageChangedMeasuresNothingAndSaysSo(t *testing.T) {
	t.Parallel()
	repo, baseSHA := changedPackagesOnlyFixture(t)
	repo.writeFile("README.md", "docs only\n")
	repo.commitAll("docs")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", baseSHA, "--non-interactive", "--format", "json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	var report struct {
		Scope struct {
			ChangedPackagesOnly bool     `json:"changed_packages_only"`
			MeasuredPackages    []string `json:"measured_packages"`
			Note                string   `json:"note"`
		} `json:"scope"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &report); err != nil {
		t.Fatalf("stdout is not JSON: %v\n%s", err, stdout.String())
	}
	if !report.Scope.ChangedPackagesOnly || len(report.Scope.MeasuredPackages) != 0 || !strings.Contains(report.Scope.Note, "CI's full run remains the gate") {
		t.Fatalf("scope = %+v", report.Scope)
	}
}

func TestE2ECoverageChangedPackagesOnlyWithNoGoPackageChangedSaysSoInText(t *testing.T) {
	t.Parallel()
	repo, baseSHA := changedPackagesOnlyFixture(t)
	repo.writeFile("README.md", "docs only\n")
	repo.commitAll("docs")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code != 0 || !strings.Contains(stdout.String(), "no packages (the diff touches no Go package)") {
		t.Fatalf("code = %d, stdout = %q", code, stdout.String())
	}
}

func TestE2ECoverageRejectsChangedPackagesOnlyWithoutChanged(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.commitAll("base")

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed-packages-only", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage || !strings.Contains(stderr.String(), "--changed-packages-only requires --changed") {
		t.Fatalf("code = %d, want %d with the requirement named; stderr: %s", code, exitUsage, stderr.String())
	}
}

func TestE2ECoverageChangedPackagesOnlyFailsClosedWhenTheTargetIsUnknown(t *testing.T) {
	t.Parallel()
	repo := newRatchetFixtureRepo(t)
	repo.writeFile("app.go", ratchetFixtureBaseSource)
	repo.commitAll("base")

	var stdout, stderr bytes.Buffer
	if code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", "no-such-ref", "--non-interactive"}, &stdout, &stderr); code == 0 {
		t.Fatalf("an unknown target passed: %s", stdout.String())
	}
}

// The changed-package list needs the repository's top level; a git that cannot
// report it must fail the run closed instead of measuring nothing. The shim
// forwards every other invocation, so everything before that step succeeds.
//
//nolint:paralleltest // t.Setenv puts a git shim on the shared process PATH
func TestE2ECoverageChangedPackagesOnlyFailsClosedWhenTheChangedPackagesCannotBeResolved(t *testing.T) {
	repo, baseSHA := changedPackagesOnlyFixture(t)
	repo.writeFile("app.go", changedPackagesOnlyMovedSource)
	repo.commitAll("move Uncovered above Add")

	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n" +
		"for a in \"$@\"; do\n" +
		"  if [ \"$a\" = --show-toplevel ]; then\n" +
		"    echo 'fake git rev-parse --show-toplevel failure' >&2\n" +
		"    exit 1\n" +
		"  fi\n" +
		"done\n" +
		"exec " + realGit + " \"$@\"\n"
	shimDir := t.TempDir()
	if err := testenv.WriteExecutableFile(filepath.Join(shimDir, "git"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", shimDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", repo.dir, "--changed", "--changed-packages-only", "--target", baseSHA, "--non-interactive"}, &stdout, &stderr)
	if code == 0 || !strings.Contains(stderr.String(), "fake git rev-parse --show-toplevel failure") {
		t.Fatalf("code = %d, stderr = %q, want a failure naming the git error", code, stderr.String())
	}
}
