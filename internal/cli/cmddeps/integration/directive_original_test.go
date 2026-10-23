package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/cli/cmddeps"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deps"
	"github.com/sneat-dev/wb/internal/depsrun"
	"github.com/spf13/cobra"
)

func testDirectiveRuntime(flags shared.Flags) shared.Runtime {
	return shared.Runtime{Flags: func() shared.Flags { return flags }, ExitError: func(code int, message string) error { return &codedError{code, message} }}
}
func directiveChild(t *testing.T, name string, flags shared.Flags) *cobra.Command {
	t.Helper()
	family := cmddeps.NewGoDirective(testDirectiveRuntime(flags), cmddeps.DirectiveOperations(depsrun.New(depsrun.DefaultDependencies(io.Discard))))
	child, _, err := family.Find([]string{name})
	if err != nil {
		t.Fatal(err)
	}
	family.RemoveCommand(child)
	return child
}
func directiveExitCode(err error) int {
	if err == nil {
		return 0
	}
	var coded *codedError
	if errors.As(err, &coded) {
		return coded.code
	}
	return 1
}
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
	command := cmddeps.NewGoDirective(testDirectiveRuntime(shared.Flags{}), cmddeps.DirectiveOperations(depsrun.New(depsrun.DefaultDependencies(io.Discard))))
	command.SilenceUsage = true
	command.SilenceErrors = true
	var out bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&out)
	command.SetArgs(args)
	err := command.Execute()
	return out.String(), err
}

func mustReadRepoFile(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(contents)
}

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
	if directiveExitCode(err) != shared.ExitFindings {
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
	if directiveExitCode(err) != shared.ExitFindings {
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
	rows := depsrun.New(depsrun.DefaultDependencies(io.Discard)).ReportDirectives(context.Background(), repositories, deps.DirectivePolicy{GoVersion: "1.26.0", Toolchain: "go1.27.0"}, deps.Options{Timeout: time.Minute, Retry: 1}, "1.26.7")
	if len(rows) != 2 {
		t.Fatalf("rows = %+v", rows)
	}
	if rows[1].Repository != "acme/empty" || rows[1].Verdict != "no-module" {
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

func TestCwCovDepsGoDirectiveCheckCommand(t *testing.T) {
	t.Parallel()
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

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "check", shared.Flags{}) }, root)
	if code := directiveExitCode(err); code != shared.ExitFindings {
		t.Fatalf("check exit = %d, want findings for the malformed module\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "backend") || !strings.Contains(stdout, "x  ") {
		t.Errorf("check output = %s", stdout)
	}

	// A directory with no module at all is not a finding.
	empty := t.TempDir()
	stdout, _, err = cwCovExec(t, empty, func() *cobra.Command { return directiveChild(t, "check", shared.Flags{}) }, empty)
	if code := directiveExitCode(err); code != 0 {
		t.Fatalf("empty check exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "no Go module found") {
		t.Errorf("empty check output = %q", stdout)
	}

	// --json is not a format this command takes; an unknown flag is rejected.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "check", shared.Flags{}) }, root, "--format", "json"); err == nil {
		t.Error("deps go-directive check accepted an unknown flag")
	}
	// --apply on a module that cannot be assessed reports the error and still
	// exits findings rather than pretending it landed.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "check", shared.Flags{}) }, root, "--apply")
	if code := directiveExitCode(err); code != shared.ExitFindings {
		t.Fatalf("apply exit = %d, want findings\n%s", code, stdout)
	}
}

//nolint:paralleltest // Actual private GH discovery fixture mutates process PATH.
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

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "report", shared.Flags{ProjectsRoot: root}) })
	if code := directiveExitCode(err); code != 0 {
		t.Fatalf("report exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "acme/good") || !strings.Contains(stdout, "module(s):") {
		t.Errorf("report text = %s", stdout)
	}

	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "report", shared.Flags{ProjectsRoot: root}) }, "--format", "json")
	if code := directiveExitCode(err); code != 0 {
		t.Fatalf("json report exit = %d", code)
	}
	var rows []depsrun.DirectiveRow
	if err := json.Unmarshal([]byte(stdout), &rows); err != nil {
		t.Fatalf("report JSON: %v\n%s", err, stdout)
	}
	if len(rows) != 1 || rows[0].Repository != "acme/good" || rows[0].Verdict != string(deps.DirectiveCompliant) {
		t.Fatalf("rows = %+v", rows)
	}

	// An unmatched filter is a usage error, never an empty clean report.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "report", shared.Flags{ProjectsRoot: root}) }, "--match", "nothing/*"); directiveExitCode(err) != shared.ExitUsage {
		t.Fatalf("empty match exit = %v, want usage", err)
	}
	// An invalid regex is refused.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return directiveChild(t, "report", shared.Flags{ProjectsRoot: root}) }, "--regex", "("); directiveExitCode(err) != shared.ExitUsage {
		t.Fatalf("invalid regex exit = %v, want usage", err)
	}
}
