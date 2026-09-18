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

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/diskusage"
	"github.com/sneat-dev/wb/internal/layout"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// cwCovExec runs a command constructor in-process with the shared projectsRoot
// global pointed at a fixture. Several cmd/wb commands read that global rather
// than declaring a --projects-root flag of their own, so a test that only sets
// the flag would never reach them.
func cwCovExec(t *testing.T, projects string, build func() *cobra.Command, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	testenv.Isolate(t)
	previousRoot, previousFilter, previousOrgs := projectsRoot, filterFlag, extraOrgs
	projectsRoot, filterFlag, extraOrgs = projects, "", nil
	t.Cleanup(func() { projectsRoot, filterFlag, extraOrgs = previousRoot, previousFilter, previousOrgs })

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

// cwCovCaptureStdoutInt captures os.Stdout while fn returns an exit code: the
// migrate and campaign writers print with fmt.Print / os.Stdout and return a
// code rather than an error.
func cwCovCaptureStdoutInt(t *testing.T, fn func() int) int {
	t.Helper()
	var code int
	cwCovCaptureStdout(t, func() { code = fn() })
	return code
}

// cwCovCloneWithOrigin makes a bare origin and a clone of it at clonePath.
func cwCovCloneWithOrigin(t *testing.T, seedRoot, name, clonePath string) string {
	t.Helper()
	seed := filepath.Join(seedRoot, name+"-seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	runGit(t, seed, "config", "user.email", "wb@example.test")
	runGit(t, seed, "config", "user.name", "WB Test")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte(name+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", ".")
	runGit(t, seed, "commit", "-m", "init")

	remote := filepath.Join(seedRoot, name+".git")
	runGit(t, seedRoot, "clone", "--bare", seed, remote)
	if err := os.MkdirAll(filepath.Dir(clonePath), 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, filepath.Dir(clonePath), "clone", remote, clonePath)
	// `git clone` does not inherit the seed's local config, so the clone needs
	// its own identity before anything here runs `git commit-tree` on it. CI
	// runs with an empty HOME and user.useConfigOnly, where git otherwise fails
	// with "Author identity unknown".
	runGit(t, clonePath, "config", "user.email", "wb@example.test")
	runGit(t, clonePath, "config", "user.name", "WB Test")
	return remote
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

	stdout, _, err := cwCovExec(t, root, newLayoutAuditCmd, "--format", "json")
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
	if _, _, err := cwCovExec(t, root, newLayoutAuditCmd, "--format", "markdown", "--report-dir", reportDir); exitCodeOf(t, err) != exitFindings {
		t.Fatalf("layout audit --report-dir exit = %v", err)
	}
	for _, name := range []string{"layout-audit.md", "layout-audit.yaml", "layout-audit.json"} {
		if _, err := os.Stat(filepath.Join(reportDir, name)); err != nil {
			t.Errorf("layout audit did not write %s: %v", name, err)
		}
	}

	// Clean plans the stray clone, then --apply removes it.
	stdout, _, err = cwCovExec(t, root, newLayoutCleanCmd, "--format", "json")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("layout clean dry-run exit = %d\n%s", code, stdout)
	}
	if _, err := os.Stat(filepath.Join(root, "stray")); err != nil {
		t.Fatal("dry-run removed the stray clone")
	}
	stdout, _, err = cwCovExec(t, root, newLayoutCleanCmd, "--apply", "--allow-missing-canonical", "--format", "markdown", "--report-dir", filepath.Join(t.TempDir(), "clean-reports"))
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
	if _, _, err := cwCovExec(t, root, newLayoutAuditCmd, "--format", "toml"); err == nil {
		t.Fatal("layout audit accepted an unknown --format")
	}
}

func TestCwCovWriteLayoutOutputAndReports(t *testing.T) {
	command := newLayoutAuditCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := writeLayoutOutput(command, "markdown", "# report\n", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "# report") {
		t.Errorf("markdown output = %q", out.String())
	}
	out.Reset()
	if err := writeLayoutOutput(command, "yaml", "", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "x: 1") {
		t.Errorf("yaml output = %q", out.String())
	}
	out.Reset()
	if err := writeLayoutOutput(command, "json", "", map[string]int{"x": 1}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), `"x": 1`) {
		t.Errorf("json output = %q", out.String())
	}
	if err := writeLayoutOutput(command, "toml", "", nil); err == nil ||
		!strings.Contains(err.Error(), `unknown --format "toml"`) {
		t.Fatalf("unknown format error = %v", err)
	}

	auditDir := filepath.Join(t.TempDir(), "audit")
	if err := writeLayoutAuditReports(auditDir, cwCovLayoutReportFixture()); err != nil {
		t.Fatal(err)
	}
	cleanDir := filepath.Join(t.TempDir(), "clean")
	if err := writeLayoutCleanReports(cleanDir, cwCovCleanReportFixture()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"layout-audit.md", "layout-audit.yaml", "layout-audit.json"} {
		data, err := os.ReadFile(filepath.Join(auditDir, name))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if strings.TrimSpace(string(data)) == "" {
			t.Errorf("%s is empty", name)
		}
	}
	for _, name := range []string{"layout-clean.md", "layout-clean.yaml", "layout-clean.json"} {
		if _, err := os.Stat(filepath.Join(cleanDir, name)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}

	// A report directory that cannot be created is an error, not a panic.
	blocked := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(blocked, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := writeLayoutAuditReports(filepath.Join(blocked, "child"), cwCovLayoutReportFixture()); err == nil {
		t.Error("writeLayoutAuditReports accepted a path under a regular file")
	}
	if err := writeLayoutCleanReports(filepath.Join(blocked, "child"), cwCovCleanReportFixture()); err == nil {
		t.Error("writeLayoutCleanReports accepted a path under a regular file")
	}
}

func TestCwCovDisabledWhenZeroMapsTheOffSwitch(t *testing.T) {
	if got := disabledWhenZero(0); got != worktrees.DisableSessionFreshness {
		t.Fatalf("disabledWhenZero(0) = %v, want the explicit disable value", got)
	}
	if got := disabledWhenZero(90 * time.Minute); got != 90*time.Minute {
		t.Fatalf("disabledWhenZero(90m) = %v, want the window unchanged", got)
	}
}

func TestCwCovPrintWorktreeGCRendersEveryRowShape(t *testing.T) {
	outcome := worktrees.GCOutcome{
		SchemaVersion: 1,
		Apply:         true,
		Entries: []worktrees.GCEntry{
			{
				Task: "landed", Repository: "acme/app", Branch: "task/landed", Class: "landed-clean",
				Applied: true, Owner: "lane-a", AgeSeconds: 7200,
				Reason: "landed by squash", Evidence: []string{"pr#42 merged"},
				Warnings: []string{"branch renamed since claim"}, Management: "unmanaged",
			},
			{
				Task: "review", Repository: "acme/app", HeadSHA: "abcdef1234567890", Class: "detached-review",
				Eligible: true, Owner: "lane-b", AgeSeconds: 30,
				Reason: "detached at a landed PR head", SanctionedCommand: "wb worktree abort review --apply",
			},
			{
				Task: "stuck", Repository: "beta/tool", Branch: "task/stuck", Class: "unpushed",
				Owner: "lane-c", AgeSeconds: 0, Reason: "GitHub has never seen this head",
				Error: "could not read origin",
			},
		},
		PartialTasks: []worktrees.GCPartialTask{{Task: "multi", Retired: []string{"acme/app"}, LeftAlone: []string{"beta/tool"}}},
		Artifacts: []worktrees.LifecycleArtifact{
			{Kind: "stage", Path: "/tmp/stage", Reason: "non-empty quarantined stage"},
		},
		Shells: []worktrees.RetiredShell{
			{Task: "empty", Path: "/tmp/empty", Error: "permission denied"},
			{Task: "quiet", Path: "/tmp/quiet"},
		},
		Reclaimed: diskusage.Usage{ApparentBytes: 2 << 20, UnsharedBytes: 1 << 20},
		Totals: map[string]int{
			"retired": 2, "eligible": 3, "refused": 1, "purged_artefacts": 4, "retired_shells": 5,
		},
	}
	command := newWorktreeGCCmd()
	var out bytes.Buffer
	command.SetOut(&out)
	if err := printWorktreeGC(command, outcome); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, want := range []string{
		"landed", "landed-clean", "retired", "evidence: [pr#42 merged]",
		"warning: branch renamed since claim", "WB management: unmanaged",
		"detached-review", "would retire", "resolve with: wb worktree abort review --apply",
		"error: could not read origin",
		"partial: task multi retired [acme/app] and left [beta/tool] behind",
		"artifact stage /tmp/stage: non-empty quarantined stage",
		"shell empty /tmp/empty: permission denied",
		"2 retired, 3 eligible, 1 kept, 4 terminal artefacts purged, 5 empty shells retired",
		"reclaimed",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("gc text missing %q:\n%s", want, text)
		}
	}
	// Only shells carrying an error are itemized.
	if strings.Contains(text, "shell quiet") {
		t.Errorf("a shell without an error must not be itemized:\n%s", text)
	}

	// A dry run reports reclaimable bytes and the shells it *would* retire.
	dry := outcome
	dry.Apply = false
	dry.Totals = map[string]int{"retired": 0, "eligible": 3, "refused": 1, "purged_artefacts": 0, "eligible_shells": 6}
	dry.Reclaimable = diskusage.Usage{ApparentBytes: 3 << 20, UnsharedBytes: 1 << 20}
	out.Reset()
	if err := printWorktreeGC(command, dry); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "reclaimable") || !strings.Contains(out.String(), "empty shells to retire") {
		t.Errorf("dry-run footer is wrong:\n%s", out.String())
	}

	// No checkouts at all still prints a footer rather than nothing.
	out.Reset()
	if err := printWorktreeGC(command, worktrees.GCOutcome{Totals: map[string]int{}}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "no WB worktrees") {
		t.Errorf("empty sweep = %q", out.String())
	}
}

func TestCwCovWorktreeGCCommandInProcess(t *testing.T) {
	t.Setenv("WB_HOME", t.TempDir())
	root := t.TempDir()

	stdout, _, err := cwCovExec(t, root, newWorktreeGCCmd, "--skip-sizes")
	if code := exitCodeOf(t, err); code != exitOK {
		t.Fatalf("empty gc exit = %d\n%s", code, stdout)
	}
	if !strings.Contains(stdout, "no WB worktrees") {
		t.Errorf("empty gc output = %q", stdout)
	}

	stdout, _, err = cwCovExec(t, root, newWorktreeGCCmd, "--skip-sizes", "--format", "json")
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
	if _, _, err := cwCovExec(t, root, newWorktreeGCCmd, "--session-freshness", "-1s"); exitCodeOf(t, err) != exitUsage {
		t.Fatalf("negative --session-freshness exit = %v, want usage", err)
	}
	if _, _, err := cwCovExec(t, root, newWorktreeGCCmd, "--format", "toml"); err == nil ||
		!strings.Contains(err.Error(), `unsupported format "toml"`) {
		t.Fatalf("unknown --format error = %v, want a named format refusal", err)
	}
}

func cwCovLayoutReportFixture() layout.Report {
	return layout.Report{
		SchemaVersion: 1,
		ProjectsRoot:  "/tmp/projects",
		Summary:       layout.Summary{Inspected: 1, TopLevel: 1},
		Findings: []layout.Finding{{
			Path: "/tmp/projects/stray", Kind: layout.KindTopLevel,
			OriginSlug: "acme/stray", Reason: "checkout sits directly under the projects root",
		}},
	}
}

func cwCovCleanReportFixture() layout.CleanReport {
	return layout.CleanReport{
		SchemaVersion: 1,
		ProjectsRoot:  "/tmp/projects",
		DryRun:        true,
		Actions: []layout.CleanAction{{
			Path: "/tmp/projects/stray", OriginSlug: "acme/stray",
			Status: "planned", Reason: "replaceable by the canonical clone",
		}},
	}
}
