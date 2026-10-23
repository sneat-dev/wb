package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/testenv"
)

// cwCovExec runs a command constructor in-process. `projects` is the fixture
// root the test set up (t.TempDir() or similar); the build closure is
// responsible for actually threading it into the *invocation it constructs
// (via testInvocation(t, projects), or a literal &invocation{projectsRoot:
// projects} when a non-empty invocation is needed — see zz_cov_wt_marker_test.go,
// zz_cov_wt_extra_test.go for the --filter case). cwCovExec itself no longer
// points any global at `projects` (sneat-dev/wb#733 removed the package-level
// projectsRoot this used to pin) — passing it here and building an unrelated
// or empty invocation inside `build` silently runs the command against the
// operator's real WB home instead of the fixture (sneat-dev/wb#760 review B2).
func cwCovExec(t *testing.T, projects string, build func() *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	testenv.Isolate(t)

	command := build()
	command.SilenceUsage = true
	command.SilenceErrors = true
	command.SetContext(context.Background())
	var out, errOut bytes.Buffer
	command.SetOut(&out)
	command.SetErr(&errOut)
	command.SetArgs(args)
	err = command.Execute()
	return out.String(), errOut.String(), err
}

// cwCovCloneWithOrigin makes a bare origin and a clone of it at clonePath.
func cwCovCloneWithOrigin(t *testing.T, seedRoot, name, clonePath string) string {
	t.Helper()
	return testenv.CloneWithOrigin(t, seedRoot, name, clonePath)
}

// cwCovPointOriginAtForge makes a clone's configured origin name a literal
// forge URL while keeping every fetch local through a url.<local>.insteadOf
// alias. It lets a hermetic test exercise a canonical clone that is still at the
// legacy on-disk <root>/{org}/{repo} path but whose origin names a forge.
func cwCovPointOriginAtForge(t *testing.T, clonePath, remote, host, slug string) {
	t.Helper()
	forgeURL := "https://" + host + "/" + slug + ".git"
	runGit(t, clonePath, "config", "url."+remote+".insteadOf", forgeURL)
	runGit(t, clonePath, "remote", "set-url", "origin", forgeURL)
}

func TestCwCovLayoutAuditAndCleanInProcess(t *testing.T) {
	root := t.TempDir()
	seeds := t.TempDir()
	cwCovCloneWithOrigin(t, seeds, "app", filepath.Join(root, "acme", "app"))
	cwCovCloneWithOrigin(t, seeds, "stray", filepath.Join(root, "stray"))

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newLayoutCmd(&invocation{projectsRoot: root}) }, "audit", "--format", "json")
	if code := exitCodeOf(t, err); code != exitFindings {
		t.Fatalf("layout audit exit = %d, want findings for the top-level clone\n%s", code, stdout)
	}
	var report struct {
		Summary struct {
			Inspected int `json:"inspected"`
			OK        int `json:"ok"`
			TopLevel  int `json:"top_level"`
		} `json:"summary"`
	}
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("layout audit JSON: %v\n%s", err, stdout)
	}
	if report.Summary.Inspected != 2 || report.Summary.TopLevel != 1 {
		t.Fatalf("audit summary = %+v, want 2 inspected with exactly 1 top-level", report.Summary)
	}

	// --report-dir writes all three renderings.
	reportDir := filepath.Join(t.TempDir(), "reports")
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newLayoutCmd(&invocation{projectsRoot: root}) }, "audit", "--format", "markdown", "--report-dir", reportDir); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("layout audit --report-dir exit = %v", err)
	}
	for _, name := range []string{"layout-audit.md", "layout-audit.yaml", "layout-audit.json"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("layout audit did not write %s: %v", name, err)
		}
	}

	// Clean plans the stray clone, then --apply removes it.
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newLayoutCmd(&invocation{projectsRoot: root}) }, "clean", "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("layout clean dry-run exit = %d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "stray")); err != nil {
		t.Fatal("dry-run removed the stray clone")
	}
	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newLayoutCmd(&invocation{projectsRoot: root}) }, "clean", "--apply", "--allow-missing-canonical", "--format", "markdown", "--report-dir", filepath.Join(t.TempDir(), "clean-reports"))
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("layout clean --apply exit = %d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "stray")); !os.IsNotExist(err) {
		t.Fatalf("layout clean --apply left the stray clone behind: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "acme", "app")); err != nil {
		t.Fatalf("layout clean removed the canonical clone: %v", err)
	}

	// Unknown format is an error, not a silent default.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newLayoutCmd(&invocation{projectsRoot: root}) }, "audit", "--format", "toml"); err == nil {
		t.Fatal("layout audit accepted an unknown --format")
	}
}

// wb layout migrate's own recovery/inclusion behaviour is proven end-to-end
// by real-git subprocess tests (layout_migrate_test.go), which run against a
// built binary and so never reach in-process Go coverage instrumentation.
// This test drives the cobra wiring itself in-process against an empty
// projects root, where there is nothing to migrate, to prove the plumbing
// (inv.projectsRoot into layout.Migrate, then into the JSON report) without
// duplicating the real-git fixtures.
func TestLayoutMigrateCLIWiresProjectsRootAndReportsNothingToMigrate(t *testing.T) {
	root := t.TempDir()
	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newLayoutCmd(&invocation{projectsRoot: root}) }, "migrate", "--format", "json")
	if err != nil {
		t.Fatalf("layout migrate on an empty projects root: %v", err)
	}
	var report map[string]any
	if err := json.Unmarshal([]byte(stdout), &report); err != nil {
		t.Fatalf("layout migrate json = %q: %v", stdout, err)
	}
	if report["schema_version"] == nil {
		t.Fatalf("layout migrate report is missing schema_version: %q", stdout)
	}
}

func TestCwCovWorktreeGCCommandInProcess(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	root := t.TempDir()

	stdout, _, err := cwCovExec(t, root, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: root}) }, "--skip-sizes")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("empty gc exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "no WB worktrees") {
		t.Errorf("empty gc output = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, root, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: root}) }, "--skip-sizes", "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("empty gc --format json exit = %d", code)
	}
	var outcome struct {
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal([]byte(stdout), &outcome); err != nil {
		t.Fatalf("gc JSON: %v\n%s", err, stdout)
	}
	if outcome.SchemaVersion == 0 {
		t.Errorf("gc JSON lost its schema version: %s", stdout)
	}

	// The two refusals the command makes before doing any work.
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: root}) }, "--session-freshness", "-1s"); exitCodeOf(t, err) != exitUsage {
		t.Fatalf("negative --session-freshness exit = %v, want usage", err)
	}
	if _, _, err := cwCovExec(t, root, func() *cobra.Command { return newWorktreeGCCmd(&invocation{projectsRoot: root}) }, "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), `unsupported format "toml"`) {
		t.Fatalf("unknown --format error = %v, want a named format refusal", err)
	}
}
