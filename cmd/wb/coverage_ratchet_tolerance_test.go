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
				{File: "internal/orchestrate/ciwait.go", Line: 458, Reason: "timing-dependent branches"},
				{File: "internal/orchestrate/worktree_merge.go", Line: 4168, Reason: "timing-dependent branches"},
			},
		},
	}
}

func TestWarnToleratedCoverageNamesThePackageAllowanceStatementsAndReason(t *testing.T) {
	t.Parallel()
	var stderr bytes.Buffer
	warnToleratedCoverage(&stderr, toleratedPackageResults()[:1])
	if stderr.Len() != 0 {
		t.Fatalf("a run that needed no tolerance printed %q", stderr.String())
	}
	warnToleratedCoverage(&stderr, toleratedPackageResults())
	if strings.Count(stderr.String(), "WARNING:") != 1 {
		t.Fatalf("warning = %q, want exactly one WARNING line", stderr.String())
	}
	for _, want := range []string{
		"WARNING: coverage ratchet tolerance used for internal/orchestrate",
		"uncovered count 1562 is above baseline 1560",
		"tolerance of 2 statement(s)",
		"Reason: timing-dependent branches",
		"tolerated: internal/orchestrate/ciwait.go:458: newly uncovered (was covered at base)",
		"tolerated: internal/orchestrate/worktree_merge.go:4168: newly uncovered (was covered at base)",
	} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("warning = %q, want it to contain %q", stderr.String(), want)
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
		var tolerated []quality.ToleratedStatement
		if err := json.Unmarshal(decoded.Packages[1]["tolerated"], &tolerated); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(tolerated) != 2 || tolerated[0].File != "internal/orchestrate/ciwait.go" || tolerated[0].Line != 458 || tolerated[0].Reason != "timing-dependent branches" {
			t.Fatalf("%s: tolerated = %+v", name, tolerated)
		}
		if !strings.Contains(string(encoded), `"file": "internal/orchestrate/ciwait.go"`) || !strings.Contains(string(encoded), `"line": 458`) {
			t.Fatalf("%s: tolerated entries are not keyed file/line/reason: %s", name, encoded)
		}
	}

	out.Reset()
	if err := writeChangedCoverageOutputTo(&out, report, "markdown", ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "TOLERATED (tolerance 2) internal/orchestrate/worktree_merge.go:4168: timing-dependent branches") {
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
	policy := "version: 1\ntiming_tolerance:\n  - package: internal/orchestrate\n    statements: 2\n"
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
