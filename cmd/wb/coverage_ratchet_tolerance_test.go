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

func toleratedPackageResults() []quality.PackageRatchet {
	return []quality.PackageRatchet{
		{Package: "internal/quiet", Uncovered: 3, BaselineUncovered: 3, HasBaseline: true, Changed: true, Pass: true},
		{
			Package: "internal/orchestrate", Uncovered: 1562, BaselineUncovered: 1560, HasBaseline: true, Changed: true, Pass: true,
			Tolerance: 2,
			Tolerated: []quality.ToleratedStatement{
				{File: "internal/orchestrate/ciwait.go", Line: 458, Function: "waitForCommitChecksWith", Reason: "timing-dependent\nbranches"},
				{File: "internal/orchestrate/worktree_merge.go", Line: 4168, Function: "verifyWorktreeMergeTargetChecks", Reason: "timing-dependent\nbranches"},
			},
		},
	}
}

func TestWarnToleratedCoverageNamesThePackageAllowanceStatementsAndReason(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	warnToleratedCoverage(&stderr, toleratedPackageResults()[:1], true)
	if stderr.Len() != 0 {
		t.Fatalf("a run that needed no tolerance printed %q", stderr.String())
	}
	warnToleratedCoverage(&stderr, toleratedPackageResults(), false)
	if strings.Count(stderr.String(), "WARNING:") != 1 || strings.Contains(stderr.String(), "::warning") {
		t.Fatalf("warning = %q, want exactly one WARNING line and no workflow command", stderr.String())
	}
	for _, want := range []string{
		"WARNING: coverage ratchet tolerance used for internal/orchestrate",
		"uncovered count 1562 is above baseline 1560",
		"tolerance of 2 statement(s)",
		"Reason: timing-dependent\nbranches",
		"tolerated: internal/orchestrate/ciwait.go:458 (in waitForCommitChecksWith): newly uncovered (was covered at base)",
		"tolerated: internal/orchestrate/worktree_merge.go:4168 (in verifyWorktreeMergeTargetChecks): newly uncovered (was covered at base)",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("warning = %q, want it to contain %q", stderr.String(), want)
		}
	}
}

func TestWarnToleratedCoverageAnnotatesTheRunOnlyUnderGitHubActions(t *testing.T) {
	t.Parallel()
	var plain, annotated bytes.Buffer
	warnToleratedCoverage(&plain, toleratedPackageResults(), false)
	warnToleratedCoverage(&annotated, toleratedPackageResults(), true)
	firstLine, rest, _ := strings.Cut(annotated.String(), "\n")
	want := "::warning title=Coverage ratchet tolerance used::internal/orchestrate: uncovered count 1562 is above baseline 1560, within the configured tolerance of 2 statement(s). Tolerated: internal/orchestrate/ciwait.go:458 (waitForCommitChecksWith), internal/orchestrate/worktree_merge.go:4168 (verifyWorktreeMergeTargetChecks). Reason: timing-dependent%0Abranches"
	if firstLine != want {
		t.Fatalf("annotation = %q, want one escaped line %q", firstLine, want)
	}
	if rest != plain.String() {
		t.Fatalf("the annotation replaced the plain warning: %q", rest)
	}
}

func TestGitHubActionsEnabledReadsTheRunnerVariable(t *testing.T) {
	t.Parallel()
	for value, want := range map[string]bool{"true": true, "": false, "false": false} {
		got := githubActionsEnabled(func(name string) string {
			if name != "GITHUB_ACTIONS" {
				t.Fatalf("read %q, want GITHUB_ACTIONS", name)
			}
			return value
		})
		if got != want {
			t.Fatalf("GITHUB_ACTIONS=%q: enabled = %t, want %t", value, got, want)
		}
	}
}

func TestChangedCoverageReportsCarryTheToleranceUsed(t *testing.T) {
	t.Parallel()
	reportDir := t.TempDir()
	var out bytes.Buffer
	report := changedCoverageReport{MergeBase: "abc123", Target: "origin/main", Packages: toleratedPackageResults()}
	if err := writeChangedCoverageOutputTo(&out, report, "json", reportDir); err != nil {
		t.Fatal(err)
	}
	persisted, err := os.ReadFile(filepath.Join(reportDir, "coverage-ratchet.json"))
	if err != nil {
		t.Fatal(err)
	}
	for name, encoded := range map[string][]byte{"stdout": out.Bytes(), "coverage-ratchet.json": persisted} {
		var decoded struct {
			Packages []map[string]json.RawMessage `json:"packages"`
		}
		if err := json.Unmarshal(encoded, &decoded); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(decoded.Packages) != 2 {
			t.Fatalf("%s: packages = %s", name, encoded)
		}
		if _, ok := decoded.Packages[0]["tolerance"]; ok {
			t.Fatalf("%s: a package that needed no tolerance carries one: %s", name, encoded)
		}
		if _, ok := decoded.Packages[0]["tolerated"]; ok {
			t.Fatalf("%s: a package that needed no tolerance lists tolerated statements: %s", name, encoded)
		}
		if string(decoded.Packages[1]["tolerance"]) != "2" {
			t.Fatalf("%s: tolerance = %s, want 2", name, decoded.Packages[1]["tolerance"])
		}
		var tolerated []map[string]any
		if err := json.Unmarshal(decoded.Packages[1]["tolerated"], &tolerated); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(tolerated) != 2 || tolerated[0]["file"] != "internal/orchestrate/ciwait.go" || tolerated[0]["line"] != float64(458) || tolerated[0]["function"] != "waitForCommitChecksWith" || tolerated[0]["reason"] != "timing-dependent\nbranches" {
			t.Fatalf("%s: tolerated = %+v", name, tolerated)
		}
	}

	out.Reset()
	if err := writeChangedCoverageOutputTo(&out, report, "markdown", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "TOLERATED (tolerance 2) internal/orchestrate/worktree_merge.go:4168 (in verifyWorktreeMergeTargetChecks): timing-dependent") {
		t.Fatalf("text report = %q, want the tolerated statement listed", out.String())
	}
	if err := changedCoverageRatchetError(report.Packages); err != nil {
		t.Fatalf("a tolerated rise failed the ratchet: %v", err)
	}
}

func TestCoverageChangedRejectsAMalformedTolerancePolicyBeforeMeasuring(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.test/app\n\ngo 1.22\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	policy := "version: 1\ntiming_tolerance:\n  - package: .\n    statements: 2\n"
	if err := os.WriteFile(filepath.Join(dir, ".wb", "coverage-ratchet.yaml"), []byte(policy), 0o644); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := run([]string{"coverage", dir, "--changed", "--target", "main", "--non-interactive"}, &stdout, &stderr)
	if code == 0 {
		t.Fatal("code = 0, want nonzero for a tolerance entry without a reason")
	}
	if !strings.Contains(stderr.String(), "needs a reason") {
		t.Fatalf("stderr = %q, want the policy error", stderr.String())
	}
}
