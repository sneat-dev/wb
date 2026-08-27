package deps

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func fleetPolicy() DirectivePolicy {
	return DirectivePolicy{GoVersion: "1.26.0", Toolchain: "go1.27.0"}
}

// buildFixtureModule writes a minimal buildable module at dir requiring
// dependencyModule (declared at depDir with depGoVersion), importing its one
// exported symbol so `go mod tidy` keeps the requirement instead of dropping
// it as unused. moduleGoVersion is the fixture's own current `go` directive.
func buildFixtureModule(t *testing.T, dir, moduleGoVersion, depDir, depGoVersion string) {
	t.Helper()
	writeTestFile(t, filepath.Join(depDir, "go.mod"), "module example.com/model\n\ngo "+depGoVersion+"\n")
	writeTestFile(t, filepath.Join(depDir, "model.go"), "package model\n\nconst Name = \"model\"\n")
	modLine := ""
	if moduleGoVersion != "" {
		modLine = "\ngo " + moduleGoVersion + "\n"
	}
	writeTestFile(t, filepath.Join(dir, "go.mod"),
		"module example.com/app\n"+modLine+
			"\nrequire example.com/model v0.1.0\n\nreplace example.com/model => "+filepath.ToSlash(depDir)+"\n")
	writeTestFile(t, filepath.Join(dir, "app.go"),
		"package app\n\nimport \"example.com/model\"\n\nvar Name = model.Name\n")
}

func TestAssessDirectiveCannotComplyNamesForcingDependency(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	depDir := t.TempDir()
	buildFixtureModule(t, dir, "1.26.0", depDir, "1.27.0")
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveCannotComply {
		t.Fatalf("verdict = %q, want %q (detail: %s)", assessment.Verdict, DirectiveCannotComply, assessment.Detail)
	}
	if len(assessment.Forcing) != 1 || assessment.Forcing[0].Path != "example.com/model" || assessment.Forcing[0].GoVersion != "1.27.0" {
		t.Fatalf("forcing = %+v", assessment.Forcing)
	}
	if !strings.Contains(assessment.Detail, "example.com/model@v0.1.0 declares go 1.27.0") {
		t.Fatalf("detail = %q", assessment.Detail)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "go 1.26.0") {
		t.Fatalf("cannot-comply must not touch go.mod:\n%s", contents)
	}
}

func TestAssessDirectiveWouldChangeWhenAchievable(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	depDir := t.TempDir()
	buildFixtureModule(t, dir, "1.27.0", depDir, "1.24")
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveWouldChange {
		t.Fatalf("verdict = %q, want %q (detail: %s)", assessment.Verdict, DirectiveWouldChange, assessment.Detail)
	}
	if assessment.TargetGoVersion != "1.26.0" {
		t.Fatalf("target go version = %q", assessment.TargetGoVersion)
	}
	if !strings.Contains(assessment.Detail, "go 1.27.0") || !strings.Contains(assessment.Detail, "go 1.26.0") {
		t.Fatalf("detail = %q", assessment.Detail)
	}
}

func TestAssessDirectiveCompliant(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	depDir := t.TempDir()
	buildFixtureModule(t, dir, "1.26.0", depDir, "1.24")
	writeTestFile(t, filepath.Join(dir, "go.mod"), strings.Replace(
		mustReadFile(t, filepath.Join(dir, "go.mod")), "go 1.26.0\n", "go 1.26.0\n\ntoolchain go1.27.0\n", 1))
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveCompliant {
		t.Fatalf("verdict = %q, want %q (detail: %s)", assessment.Verdict, DirectiveCompliant, assessment.Detail)
	}
}

func TestAssessDirectiveBelowFloorLeavesModuleAlone(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"), "module example.com/legacy\n\ngo 1.20\n")
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveBelowFloor {
		t.Fatalf("verdict = %q, want %q (detail: %s)", assessment.Verdict, DirectiveBelowFloor, assessment.Detail)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "go 1.20") {
		t.Fatalf("below-floor must not raise the directive:\n%s", contents)
	}
}

func TestAssessDirectiveLocalReplaceIsReportedAsError(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	writeTestFile(t, filepath.Join(dir, "go.mod"),
		"module example.com/app\n\ngo 1.27.0\n\nrequire example.com/model v0.1.0\n\nreplace example.com/model => ../model\n")
	assessment, err := AssessDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveError {
		t.Fatalf("verdict = %q, want %q (detail: %s)", assessment.Verdict, DirectiveError, assessment.Detail)
	}
	if !strings.Contains(assessment.Detail, "local replace directive") {
		t.Fatalf("detail = %q", assessment.Detail)
	}
}

func TestApplyDirectiveWritesAndSurvivesTidy(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	depDir := t.TempDir()
	buildFixtureModule(t, dir, "1.27.0", depDir, "1.24")
	assessment, err := ApplyDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveCompliant {
		t.Fatalf("verdict = %q, want %q (detail: %s)", assessment.Verdict, DirectiveCompliant, assessment.Detail)
	}
	contents, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(contents)
	if !strings.Contains(text, "go 1.26.0") {
		t.Fatalf("go.mod was not written with the target go directive:\n%s", text)
	}
	if !strings.Contains(text, "toolchain go1.27.0") {
		t.Fatalf("go.mod was not written with the target toolchain directive:\n%s", text)
	}
	// Prove the edit survives a second, independent `go mod tidy` — an edit
	// tidy reverts is worse than no edit, because it looks like it worked.
	if _, _, err := runGoCommand(context.Background(), Options{Timeout: time.Minute}, dir, "mod", "tidy"); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(after), "go 1.26.0") || !strings.Contains(string(after), "toolchain go1.27.0") {
		t.Fatalf("a second go mod tidy reverted the applied directive:\n%s", after)
	}
}

func TestApplyDirectiveNoOpOnCannotComply(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	depDir := t.TempDir()
	buildFixtureModule(t, dir, "1.26.0", depDir, "1.27.0")
	before, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	assessment, err := ApplyDirective(context.Background(), dir, fleetPolicy(), Options{Timeout: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	if assessment.Verdict != DirectiveCannotComply {
		t.Fatalf("verdict = %q, want %q", assessment.Verdict, DirectiveCannotComply)
	}
	after, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("apply must not touch go.mod when the verdict is not would-change:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// TestDirectiveAssessmentEffectiveGoVersionIsTheHigherOfCurrentAndCeiling
// covers the value a caller comparing against a fixed local toolchain (for
// example GitHub CodeQL default-setup's pinned GOTOOLCHAIN=local) must use:
// what the repository actually requires today, not what the fleet policy
// would set.
func TestDirectiveAssessmentEffectiveGoVersionIsTheHigherOfCurrentAndCeiling(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		current string
		ceiling string
		want    string
	}{
		{name: "current higher", current: "1.27.0", ceiling: "1.24", want: "1.27.0"},
		{name: "ceiling higher", current: "1.26.0", ceiling: "1.27.0", want: "1.27.0"},
		{name: "no ceiling known", current: "1.26.1", ceiling: "", want: "1.26.1"},
		{name: "no current known", current: "", ceiling: "1.24", want: "1.24"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			assessment := DirectiveAssessment{CurrentGoVersion: testCase.current, Ceiling: testCase.ceiling}
			if got := assessment.EffectiveGoVersion(); got != testCase.want {
				t.Fatalf("EffectiveGoVersion() = %q, want %q", got, testCase.want)
			}
		})
	}
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}
