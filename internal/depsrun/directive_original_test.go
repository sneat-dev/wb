package depsrun

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
)

func TestCodeQLRiskFlagsOnlyWhatDefaultSetupCannotRun(t *testing.T) {
	t.Parallel()
	atRisk, note := codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "1.27.0", Ceiling: "1.24"}, "1.26.7")
	if !atRisk || !strings.Contains(note, "requires go 1.27.0") || !strings.Contains(note, "pinned to go1.26.7") {
		t.Fatalf("atRisk=%v note=%q", atRisk, note)
	}
	atRisk, note = codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "1.26.0", Ceiling: "1.27.0"}, "1.26.7")
	if !atRisk || !strings.Contains(note, "requires go 1.27.0") {
		t.Fatalf("a dependency ceiling above the pinned toolchain must also be flagged: atRisk=%v note=%q", atRisk, note)
	}
	if atRisk, _ := codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "1.26.0", Ceiling: "1.24"}, "1.26.7"); atRisk {
		t.Fatal("a module within the pinned ceiling must not be flagged")
	}
	if atRisk, _ := codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "", Ceiling: ""}, "1.26.7"); atRisk {
		t.Fatal("an assessment with no known version must not be flagged")
	}
}

func TestSweepDirectivesSortsRowsByRepositoryThenModule(t *testing.T) {
	t.Parallel()
	repositories := []deps.Repository{
		{Slug: "zeta/repo"},
		{Slug: "alpha/repo"},
		{Slug: "alpha/repo"},
	}
	rows := New(DefaultDependencies(io.Discard)).ReportDirectives(context.Background(), repositories, deps.DirectivePolicy{}, deps.Options{}, "")
	if len(rows) != 3 {
		t.Fatalf("sweepDirectives returned %d rows, want 3", len(rows))
	}
	for i, row := range rows {
		if row.Verdict != verdictNoModule {
			t.Fatalf("rows[%d].Verdict = %q, want %q for a remote-only repository", i, row.Verdict, verdictNoModule)
		}
	}
	if rows[0].Repository != "alpha/repo" || rows[1].Repository != "alpha/repo" || rows[2].Repository != "zeta/repo" {
		t.Fatalf("rows not sorted by repository: %+v", rows)
	}
}

func TestCwCovGoSyntaxLocalAndCodeQLRisk(t *testing.T) {
	t.Parallel()
	if got := goSyntaxLocal("1.26.7"); got != "go1.26.7" {
		t.Errorf("goSyntaxLocal(1.26.7) = %q", got)
	}
	if got := goSyntaxLocal("go1.27.0"); got != "go1.27.0" {
		t.Errorf("goSyntaxLocal(go1.27.0) = %q", got)
	}

	// No effective version or no ceiling is no evidence of risk.
	if atRisk, note := codeQLRisk(deps.DirectiveAssessment{}, "1.26.7"); atRisk || note != "" {
		t.Errorf("empty assessment = (%t, %q)", atRisk, note)
	}
	if atRisk, note := codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "1.27.0"}, ""); atRisk || note != "" {
		t.Errorf("empty ceiling = (%t, %q)", atRisk, note)
	}
	// A module at or below the ceiling is safe.
	if atRisk, _ := codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "1.26.7"}, "1.26.7"); atRisk {
		t.Error("a module exactly at the ceiling must not be flagged")
	}
	// The effective requirement is the higher of the module's own directive
	// and the dependency ceiling, and it is the one compared.
	atRisk, note := codeQLRisk(deps.DirectiveAssessment{CurrentGoVersion: "1.25.0", Ceiling: "1.27.1"}, "1.26.7")
	if !atRisk {
		t.Fatalf("a dependency ceiling above the pinned toolchain must be flagged")
	}
	for _, want := range []string{"CodeQL default setup would fail here", "requires go 1.27.1", "pinned to go1.26.7", "GOTOOLCHAIN=local"} {
		if !strings.Contains(note, want) {
			t.Errorf("risk note missing %q: %q", want, note)
		}
	}
}

func TestCwCovNeedsAttentionAndDirectiveMarker(t *testing.T) {
	t.Parallel()
	for verdict, want := range map[deps.DirectiveVerdict]bool{
		deps.DirectiveCannotComply: true,
		deps.DirectiveError:        true,
		deps.DirectiveWouldChange:  true,
		deps.DirectiveCompliant:    false,
		deps.DirectiveBelowFloor:   false,
	} {
		if got := needsAttention(verdict, false); got != want {
			t.Errorf("needsAttention(%q, dry-run) = %t, want %t", verdict, got, want)
		}
	}
	// Under --apply, "would change" is the plan --apply consumes rather than a
	// failure.
	if needsAttention(deps.DirectiveWouldChange, true) {
		t.Error("would-change must not fail an --apply run")
	}
	if !needsAttention(deps.DirectiveCannotComply, true) {
		t.Error("cannot-comply must always fail")
	}

}

func TestCwCovModuleLabel(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "repo")
	if err := os.MkdirAll(filepath.Join(root, "backend"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got := moduleLabel(root, root); got != "repo" {
		t.Errorf("moduleLabel(root) = %q, want the base name", got)
	}
	if got := moduleLabel(root, filepath.Join(root, "backend")); got != "backend" {
		t.Errorf("moduleLabel(subdir) = %q", got)
	}
}

func TestCwCovSweepDirectivesClassifiesEveryRepository(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	good := filepath.Join(root, "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "go.mod"),
		[]byte("module github.com/acme/good\n\ngo 1.26.0\n\ntoolchain go1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	broken := filepath.Join(root, "broken")
	if err := os.MkdirAll(broken, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(broken, "go.mod"), []byte("module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	empty := filepath.Join(root, "empty")
	if err := os.MkdirAll(empty, 0o755); err != nil {
		t.Fatal(err)
	}

	rows := New(DefaultDependencies(io.Discard)).ReportDirectives(context.Background(), []deps.Repository{
		{Slug: "acme/remote", Path: ""},
		{Slug: "acme/empty", Path: empty},
		{Slug: "acme/broken", Path: broken},
		{Slug: "acme/good", Path: good},
	}, deps.DirectivePolicy{GoVersion: "1.26.0", Toolchain: "go1.27.0"},
		deps.Options{Timeout: 60 * time.Second, Retry: 1}, "1.26.7")

	if len(rows) != 4 {
		t.Fatalf("rows = %+v, want one per repository", rows)
	}
	// Sorted by repository, so acme/broken, acme/empty, acme/good, acme/remote.
	if rows[0].Repository != "acme/broken" || rows[0].Verdict != string(deps.DirectiveError) || rows[0].Detail == "" {
		t.Errorf("broken row = %+v, want an error verdict with its reason", rows[0])
	}
	if rows[1].Repository != "acme/empty" || rows[1].Verdict != verdictNoModule || rows[1].Detail != "no Go module" {
		t.Errorf("empty row = %+v", rows[1])
	}
	if rows[2].Repository != "acme/good" || rows[2].Verdict != string(deps.DirectiveCompliant) {
		t.Errorf("good row = %+v, want a compliant module", rows[2])
	}
	if rows[2].Module != "github.com/acme/good" || rows[2].CodeQLAtRisk {
		t.Errorf("good row identity = %+v", rows[2])
	}
	if rows[3].Repository != "acme/remote" || rows[3].Verdict != verdictNoModule ||
		!strings.Contains(rows[3].Detail, "remote-only") {
		t.Errorf("remote row = %+v", rows[3])
	}
}
