package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
	"github.com/spf13/cobra"
)

func TestCwCovGoSyntaxLocalAndCodeQLRisk(t *testing.T) {
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

	for verdict, want := range map[deps.DirectiveVerdict]string{
		deps.DirectiveCompliant:    "-",
		deps.DirectiveWouldChange:  "✓",
		deps.DirectiveCannotComply: "✗",
		deps.DirectiveBelowFloor:   "▪",
		deps.DirectiveError:        "x",
		"unknown":                  "x",
	} {
		if got := directiveMarker(verdict); got != want {
			t.Errorf("directiveMarker(%q) = %q, want %q", verdict, got, want)
		}
	}
}

func TestCwCovModuleLabel(t *testing.T) {
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

func TestCwCovWriteDirectiveReportText(t *testing.T) {
	rows := []directiveRow{
		{Repository: "acme/app", Module: "github.com/acme/app", Verdict: string(deps.DirectiveCompliant), Detail: "compliant"},
		{Repository: "acme/app", Module: "github.com/acme/app/backend", Verdict: string(deps.DirectiveWouldChange), Detail: "would write go 1.26.0"},
		{Repository: "acme/blocked", Module: "github.com/acme/blocked", Verdict: string(deps.DirectiveCannotComply), Detail: "cannot comply", Forcing: []deps.ForcingDependency{{Path: "github.com/x/y", Version: "v1", GoVersion: "1.27"}}},
		{Repository: "acme/low", Module: "github.com/acme/low", Verdict: string(deps.DirectiveBelowFloor), Detail: "below the floor"},
		{Repository: "acme/broken", Module: "github.com/acme/broken", Verdict: string(deps.DirectiveError), Detail: "go list failed"},
		{Repository: "acme/nomodule", Verdict: verdictNoModule, Detail: "no Go module"},
		{Repository: "acme/risky", Module: "github.com/acme/risky", Verdict: string(deps.DirectiveCompliant), Detail: "compliant", CodeQLAtRisk: true},
	}
	var out bytes.Buffer
	writeDirectiveReportText(&out, rows)
	text := out.String()
	for _, want := range []string{
		"acme/app (github.com/acme/app)", "acme/app (github.com/acme/app/backend)",
		"✓", "✗", "▪", "–",
		"7 module(s): ",
		"1 below-floor", "1 cannot-comply", "2 compliant", "1 error", "1 no-module", "1 would-change",
		"1 module(s) at risk under CodeQL default setup's pinned GOTOOLCHAIN=local",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("directive report missing %q:\n%s", want, text)
		}
	}
	// The footer's counts are sorted so a rerun reads the same.
	if strings.Index(text, "below-floor") > strings.Index(text, "cannot-comply") {
		t.Errorf("footer counts are not sorted:\n%s", text)
	}

	// No at-risk row means no at-risk line at all.
	out.Reset()
	writeDirectiveReportText(&out, []directiveRow{{Repository: "acme/only", Verdict: string(deps.DirectiveCompliant), Detail: "compliant"}})
	if strings.Contains(out.String(), "at risk under CodeQL") {
		t.Errorf("at-risk line printed with no at-risk row:\n%s", out.String())
	}
}

func TestCwCovSweepDirectivesClassifiesEveryRepository(t *testing.T) {
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

	rows := sweepDirectives(context.Background(), []deps.Repository{
		{Slug: "acme/remote", Path: ""},
		{Slug: "acme/empty", Path: empty},
		{Slug: "acme/broken", Path: broken},
		{Slug: "acme/good", Path: good},
	}, deps.DirectivePolicy{GoVersion: "1.26.0", Toolchain: "go1.27.0"},
		deps.Options{Timeout: 60 * time.Second, Retry: 1}, defaultCodeQLCeiling)

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

func TestCwCovDepsGoDirectiveCheckCommand(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"),
		[]byte("module github.com/acme/good\n\ngo 1.26.0\n\ntoolchain go1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := filepath.Join(root, "backend")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sub, "go.mod"), []byte("module\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", t.TempDir())

	stdout, _, err := cwCovExec(t, root, newDepsGoDirectiveCheckCmd, root)
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("check exit = %d, want findings for the malformed module\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "backend") || !strings.Contains(stdout, "x  ") {
		t.Errorf("check output = %s", stdout)
	}

	// A directory with no module at all is not a finding.
	empty := t.TempDir()
	stdout, _, err = cwCovExec(t, empty, newDepsGoDirectiveCheckCmd, empty)
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("empty check exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "no Go module found") {
		t.Errorf("empty check output = %q", stdout)
	}

	// --json is not a format this command takes; an unknown flag is rejected.
	if _, _, err := cwCovExec(t, root, newDepsGoDirectiveCheckCmd, root, "--format", "json"); err == nil {
		t.Error("deps go-directive check accepted an unknown flag")
	}
	// --apply on a module that cannot be assessed reports the error and still
	// exits findings rather than pretending it landed.
	stdout, _, err = cwCovExec(t, root, newDepsGoDirectiveCheckCmd, root, "--apply")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("apply exit = %d, want findings\n%s", code, stdout)
	}
}

func TestCwCovDepsGoDirectiveReportCommand(t *testing.T) {
	root := t.TempDir()
	good := filepath.Join(root, "acme", "good")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(good, "go.mod"),
		[]byte("module github.com/acme/good\n\ngo 1.26.0\n\ntoolchain go1.27.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(good, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	cwCovFakeGH(t, "cwcov-user", nil, `[]`)
	t.Setenv("WB_HOME", t.TempDir())

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsGoDirectiveReportCmd(&invocation{projectsRoot: root}) })
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("report exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "acme/good") || !strings.Contains(stdout, "module(s):") {
		t.Errorf("report text = %s", stdout)
	}

	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newDepsGoDirectiveReportCmd(&invocation{projectsRoot: root}) }, "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("json report exit = %d", code)
	}
	var rows []directiveRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("report JSON: %v\n%s", err, stdout)
	}
	if len(rows) != 1 || rows[0].Repository != "acme/good" || rows[0].Verdict != string(deps.DirectiveCompliant) {
		t.Fatalf("rows = %+v", rows)
	}

	// An unmatched filter is a usage error, never an empty clean report.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsGoDirectiveReportCmd(&invocation{projectsRoot: root}) }, "--match", "nothing/*"); exitCodeOf(t, err) != exitUsage {
		t.Fatalf("empty match exit = %v, want usage", err)
	}
	// An invalid regex is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newDepsGoDirectiveReportCmd(&invocation{projectsRoot: root}) }, "--regex", "("); exitCodeOf(t, err) != exitUsage {
		t.Fatalf("invalid regex exit = %v, want usage", err)
	}
}
