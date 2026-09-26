package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/quality"
)

// writeCoverageWorklistFixture writes go.mod plus every relativePath ->
// contents entry into a fresh temp module root and returns its path, the
// same shape `wb coverage worklist` expects for --module.
func writeCoverageWorklistFixture(t *testing.T, modulePath string, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module "+modulePath+"\n\ngo 1.27\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for relativePath, contents := range files {
		full := filepath.Join(dir, filepath.FromSlash(relativePath))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const coverageWorklistFixtureA = `package pkg

func FuncA1() int {
	return 1
}

func FuncA2() int {
	return 2
}
`

const coverageWorklistFixtureB = `package pkg

func FuncB1() int {
	return 1
}
`

func writeCoverageWorklistProfile(t *testing.T, dir, modulePath string) string {
	t.Helper()
	profilePath := filepath.Join(dir, "profile.out")
	contents := "mode: set\n" +
		modulePath + "/pkg/a.go:4.2,4.10 10 0\n" +
		modulePath + "/pkg/a.go:8.2,8.10 10 0\n" +
		modulePath + "/pkg/b.go:4.2,4.10 5 0\n"
	if err := os.WriteFile(profilePath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	return profilePath
}

func TestCoverageWorklistWritesTextOutputAndNamesSharedFiles(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, map[string]string{
		"pkg/a.go": coverageWorklistFixtureA,
		"pkg/b.go": coverageWorklistFixtureB,
	})
	profilePath := writeCoverageWorklistProfile(t, dir, modulePath)

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--unit-size", "12", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	output := stdout.String()
	for _, want := range []string{
		"worklist: 3 unit(s), 25 uncovered statement(s) (target unit size 12)",
		"unit 0: 10 statement(s)",
		"pkg/a.go",
		"FuncA1",
		"FuncA2",
		"FuncB1",
		"shares a file with unit",
	} {
		if !strings.Contains(output, want) {
			t.Fatalf("output missing %q; got:\n%s", want, output)
		}
	}
}

func TestCoverageWorklistWritesJSONOutput(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, map[string]string{
		"pkg/a.go": coverageWorklistFixtureA,
		"pkg/b.go": coverageWorklistFixtureB,
	})
	profilePath := writeCoverageWorklistProfile(t, dir, modulePath)

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--unit-size", "12", "--format", "json", "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	var worklist quality.Worklist
	if err := json.Unmarshal(stdout.Bytes(), &worklist); err != nil {
		t.Fatalf("decode JSON output: %v\noutput:\n%s", err, stdout.String())
	}
	if worklist.TotalUncoveredStatements != 25 || worklist.UnitSize != 12 || len(worklist.Units) != 3 {
		t.Fatalf("worklist = %#v", worklist)
	}
}

func TestCoverageWorklistDefaultUnitSizeKeepsSmallFixtureInOneUnit(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, map[string]string{
		"pkg/a.go": coverageWorklistFixtureA,
	})
	profilePath := filepath.Join(dir, "profile.out")
	contents := "mode: set\n" + modulePath + "/pkg/a.go:4.2,4.10 10 0\n"
	if err := os.WriteFile(profilePath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "target unit size 300") {
		t.Fatalf("output missing default unit size 300:\n%s", stdout.String())
	}
	if strings.Contains(stdout.String(), "shares a file with unit") {
		t.Fatalf("a single unit must never claim to share a file:\n%s", stdout.String())
	}
}

func TestCoverageWorklistRejectsInvalidFormat(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, nil)
	profilePath := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--format", "yaml", "--non-interactive"}, &stdout, &stderr)
	if code != exitUsage {
		t.Fatalf("code = %d, want exitUsage (%d)\nstderr:\n%s", code, exitUsage, stderr.String())
	}
}

func TestCoverageWorklistRejectsMissingProfile(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, nil)
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", filepath.Join(dir, "missing.out"), "--module", dir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a missing coverage profile")
	}
}

func TestCoverageWorklistRejectsMissingGoMod(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	profilePath := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero when --module has no go.mod")
	}
}

func TestCoverageWorklistRejectsUnitSizeBelowOne(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, nil)
	profilePath := filepath.Join(dir, "profile.out")
	if err := os.WriteFile(profilePath, []byte("mode: set\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--unit-size", "0", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for --unit-size 0")
	}
}

func TestCoverageWorklistNamesFileLevelBlocksWithoutAnEnclosingFunction(t *testing.T) {
	t.Parallel()
	modulePath := "fixture.test/cliworklist"
	dir := writeCoverageWorklistFixture(t, modulePath, map[string]string{
		"pkg/init.go": "package pkg\n\nvar Global = 1\n\nfunc Covered() int { return 1 }\n",
	})
	profilePath := filepath.Join(dir, "profile.out")
	contents := "mode: set\n" + modulePath + "/pkg/init.go:3.1,3.15 1 0\n"
	if err := os.WriteFile(profilePath, []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", "worklist", profilePath, "--module", dir, "--non-interactive"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("code = %d, want 0\nstderr:\n%s", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "(file-level)") {
		t.Fatalf("output missing file-level marker:\n%s", stdout.String())
	}
}
