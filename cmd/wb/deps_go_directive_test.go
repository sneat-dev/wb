package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/deps"
)

// writeGoDirectiveFixture writes a minimal buildable module at dir declaring
// moduleGoVersion, requiring and importing a dependency module at depDir
// declaring depGoVersion (via a local replace, so no network is needed).
func writeGoDirectiveFixture(t *testing.T, dir, moduleGoVersion, depDir, depGoVersion string) {
	t.Helper()
	if err := os.MkdirAll(depDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "go.mod"), []byte("module example.com/dep\n\ngo "+depGoVersion+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(depDir, "dep.go"), []byte("package dep\n\nconst Name = \"dep\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	modBody := "module example.com/app\n\ngo " + moduleGoVersion + "\n\nrequire example.com/dep v0.1.0\n\nreplace example.com/dep => " +
		filepath.ToSlash(depDir) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(modBody), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "app.go"), []byte("package app\n\nimport \"example.com/dep\"\n\nvar Name = dep.Name\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func runDepsGoDirective(t *testing.T, args ...string) (string, error) {
	t.Helper()
	command := newDepsGoDirectiveCmd()
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func TestDepsGoDirectiveCheckReportsNoGoModule(t *testing.T) {
	t.Parallel()
	out, err := runDepsGoDirective(t, "check", t.TempDir())
	if err != nil {
		t.Fatalf("unexpected error: %v\n%s", err, out)
	}
	if !strings.Contains(out, "no Go module found") {
		t.Fatalf("output = %q", out)
	}
}

func TestDepsGoDirectiveCheckMultiModuleReportsEachModule(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeGoDirectiveFixture(t, repo, "1.27.0", filepath.Join(t.TempDir(), "dep1"), "1.24")
	writeGoDirectiveFixture(t, filepath.Join(repo, "backend"), "1.26.0", filepath.Join(t.TempDir(), "dep2"), "1.27.0")
	out, err := runDepsGoDirective(t, "check", repo, "--timeout", "60s")
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("exit = %v\n%s", err, out)
	}
	if !strings.Contains(out, "would change") {
		t.Fatalf("root module row missing:\n%s", out)
	}
	if !strings.Contains(out, "backend") || !strings.Contains(out, "cannot comply: example.com/dep@v0.1.0 declares go 1.27.0") {
		t.Fatalf("backend module row missing or wrong:\n%s", out)
	}
}

func TestDepsGoDirectiveCheckApplyWritesAndLeavesCannotComplyUntouched(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	writeGoDirectiveFixture(t, repo, "1.27.0", filepath.Join(t.TempDir(), "dep1"), "1.24")
	writeGoDirectiveFixture(t, filepath.Join(repo, "backend"), "1.26.0", filepath.Join(t.TempDir(), "dep2"), "1.27.0")
	before, err := os.ReadFile(filepath.Join(repo, "backend", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := runDepsGoDirective(t, "check", repo, "--apply", "--timeout", "60s")
	if exitCodeOf(t, err) != exitFindings {
		t.Fatalf("exit = %v\n%s", err, out)
	}
	if !strings.Contains(out, "applied `go 1.26.0` / `toolchain go1.27.0`") {
		t.Fatalf("root module was not applied:\n%s", out)
	}
	rootMod, err := os.ReadFile(filepath.Join(repo, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(rootMod), "go 1.26.0") || !strings.Contains(string(rootMod), "toolchain go1.27.0") {
		t.Fatalf("root go.mod was not written:\n%s", rootMod)
	}
	after, err := os.ReadFile(filepath.Join(repo, "backend", "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Fatalf("cannot-comply module must not be touched by --apply:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestDepsGoDirectiveReportWalksFleetAndNeverWrites(t *testing.T) {
	t.Parallel()
	compliantDir := t.TempDir()
	writeGoDirectiveFixture(t, compliantDir, "1.26.0", filepath.Join(t.TempDir(), "dep"), "1.24")
	if err := os.WriteFile(filepath.Join(compliantDir, "go.mod"),
		[]byte(strings.Replace(mustReadRepoFile(t, filepath.Join(compliantDir, "go.mod")), "\n\nrequire",
			"\n\ntoolchain go1.27.0\n\nrequire", 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	noModuleDir := t.TempDir()
	before, err := dirSnapshot(compliantDir)
	if err != nil {
		t.Fatal(err)
	}

	repositories := []deps.Repository{
		{Slug: "acme/compliant", Path: compliantDir},
		{Slug: "acme/empty", Path: noModuleDir},
	}
	rows := sweepDirectives(context.Background(), repositories, deps.DirectivePolicy{GoVersion: "1.26.0", Toolchain: "go1.27.0"}, deps.Options{Timeout: time.Minute, Retry: 1}, defaultCodeQLCeiling)
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[1].Repository != "acme/empty" || rows[1].Verdict != verdictNoModule {
		t.Fatalf("empty repository row = %+v", rows[1])
	}
	if rows[0].Repository != "acme/compliant" || rows[0].Verdict != string(deps.DirectiveCompliant) {
		t.Fatalf("compliant repository row = %+v", rows[0])
	}

	after, err := dirSnapshot(compliantDir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Fatal("the fleet report must never write to a repository it walks")
	}
}

func mustReadRepoFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

// dirSnapshot hashes every regular file's contents under dir so a test can
// prove a read-only operation left the directory byte-for-byte unchanged.
func dirSnapshot(dir string) (string, error) {
	var builder strings.Builder
	err := filepath.Walk(dir, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil || info.IsDir() {
			return walkErr
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		builder.WriteString(relative)
		builder.WriteByte(0)
		builder.Write(contents)
		builder.WriteByte(0)
		return nil
	})
	return builder.String(), err
}

// TestCodeQLRiskFlagsOnlyWhatDefaultSetupCannotRun covers the concrete
// failure mode the policy exists to prevent: GitHub CodeQL default-setup
// pins GOTOOLCHAIN=local and cannot switch, so a module whose effective go
// requirement exceeds that pinned ceiling fails Analyze (go) outright,
// independent of whether the fleet policy considers it would-change or
// cannot-comply.
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
