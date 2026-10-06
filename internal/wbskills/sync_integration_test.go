package wbskills_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/ai"
	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/cli/cmdskills"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/wbskills"
	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/skillsync"
)

const exitFindings = 1

type exitError struct {
	code    int
	message string
}

func (e *exitError) Error() string { return e.message }
func testRuntime() shared.Runtime {
	return shared.Runtime{ExitError: func(code int, message string) error { return &exitError{code: code, message: message} }}
}
func testSyncDependencies(cfg skillsync.Config, home string) cmdskills.SyncDependencies {
	deps := cmdskills.SyncDependencies{Config: func() (skillsync.Config, error) { return cfg, nil }, Home: os.UserHomeDir, Getenv: os.Getenv}
	if home != "" {
		deps.Home = func() (string, error) { return home, nil }
		deps.Getenv = func(string) string { return "" }
	}
	return deps
}
func newSkillsSyncCmd(runtime shared.Runtime, deps cmdskills.SyncDependencies) *cobra.Command {
	root := cmdskills.New(runtime, deps, cmdskills.HookDependencies{})
	for _, c := range root.Commands() {
		if c.Name() == "sync" {
			root.RemoveCommand(c)
			c.SetOut(io.Discard)
			c.SetErr(io.Discard)
			return c
		}
	}
	panic("sync missing")
}
func testNewSkillsSyncCmdInstallsIntoAnExplicitDirAndIsIdempotent(t *testing.T, cfg skillsync.Config) {
	dir := filepath.Join(t.TempDir(), "skills")
	first := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, filepath.Dir(dir)))
	first.SetArgs([]string{"--dir", dir})
	var firstOut bytes.Buffer
	first.SetOut(&firstOut)
	if err := first.Execute(); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if !strings.Contains(firstOut.String(), "added:") {
		t.Errorf("first sync output = %q, want an added: line", firstOut.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "wb-worktrees", "SKILL.md")); err != nil {
		t.Fatalf("wb-worktrees/SKILL.md was not installed: %v", err)
	}
	if status, err := skillsync.ReadStatus(dir); err != nil || !status.Installed {
		t.Fatalf("no marker written: %v", err)
	}

	second := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, filepath.Dir(dir)))
	second.SetArgs([]string{"--dir", dir})
	var secondOut bytes.Buffer
	second.SetOut(&secondOut)
	if err := second.Execute(); err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if strings.Contains(secondOut.String(), "added:") || strings.Contains(secondOut.String(), "updated:") {
		t.Errorf("second identical sync reported a change: %q", secondOut.String())
	}
	if !strings.Contains(secondOut.String(), "nothing to do") {
		t.Errorf("second sync output = %q, want it to say there was nothing to do", secondOut.String())
	}
}

func testNewSkillsSyncCmdDryRunWritesNothing(t *testing.T, cfg skillsync.Config) {
	dir := filepath.Join(t.TempDir(), "skills")

	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, filepath.Dir(dir)))
	command.SetArgs([]string{"--dir", dir, "--dry-run"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "would sync") {
		t.Errorf("dry-run output = %q, want it to say what it would do", out.String())
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("--dry-run created %s: err=%v", dir, err)
	}
}

func testNewSkillsSyncCmdReportsConflictsAsFindings(t *testing.T, cfg skillsync.Config) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(dir, "wb-worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wb-worktrees", "SKILL.md"), []byte("not wb's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, filepath.Dir(dir)))
	command.SetArgs([]string{"--dir", dir})
	var out bytes.Buffer
	command.SetOut(&out)
	err := command.Execute()
	if err == nil {
		t.Fatal("a conflict must be reported as a findings-level error")
	}
	var coded *exitError
	if !errors.As(err, &coded) {
		t.Fatalf("error %v is not an *exitError", err)
	}
	if coded.code != exitFindings {
		t.Errorf("code = %d, want exitFindings (%d)", coded.code, exitFindings)
	}
	if !strings.Contains(out.String(), "conflicts") {
		t.Errorf("stdout = %q, want the conflict itemized in the report", out.String())
	}
}

func testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor(t *testing.T, cfg skillsync.Config) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, ""))
	command.SetArgs([]string{"--harness", "cursor"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	cursorSkill := filepath.Join(home, ".cursor", "skills", "wb-worktrees", "SKILL.md")
	if _, err := os.Stat(cursorSkill); err != nil {
		t.Fatalf("cursor skill was not installed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".claude", "skills", "wb-worktrees", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("--harness cursor must not also install into Claude")
	}
	if !strings.Contains(out.String(), filepath.Join(home, ".cursor", "skills")) {
		t.Errorf("output = %q, want the cursor skills dir", out.String())
	}
}

func testNewSkillsSyncCmdJSONReportsMultipleHarnessTargets(t *testing.T, cfg skillsync.Config) {
	home := t.TempDir()

	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, home))
	command.SetArgs([]string{"--harness", "cursor,codex", "--format", "json"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload skillsSyncMultiJSON
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("multi-target output is not a targets wrapper: %v\n%s", err, out.String())
	}
	if len(payload.Targets) != 2 {
		t.Fatalf("targets = %+v, want cursor and codex", payload.Targets)
	}
	if payload.Targets[0].Harness != "cursor" || payload.Targets[1].Harness != "codex" {
		t.Errorf("harness ids = %+v", payload.Targets)
	}
	if _, err := os.Stat(filepath.Join(home, ".cursor", "skills", "wb-worktrees", "SKILL.md")); err != nil {
		t.Fatalf("cursor skill missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "wb-worktrees", "SKILL.md")); err != nil {
		t.Fatalf("codex skill missing: %v", err)
	}
}

func testNewSkillsSyncCmdJSONReportsEveryCurrentHarnessAsUnchanged(t *testing.T, cfg skillsync.Config) {
	home := t.TempDir()

	first := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, home))
	first.SetArgs([]string{"--harness", "cursor,codex"})
	if err := first.Execute(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	second := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, home))
	second.SetArgs([]string{"--harness", "cursor,codex", "--format", "json"})
	var out bytes.Buffer
	second.SetOut(&out)
	if err := second.Execute(); err != nil {
		t.Fatalf("already-current multi-harness sync: %v", err)
	}
	var payload skillsSyncMultiJSON
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if len(payload.Targets) != 2 {
		t.Fatalf("targets = %+v, want cursor and codex", payload.Targets)
	}
	for _, target := range payload.Targets {
		if target.Status != "unchanged" {
			t.Errorf("%s status = %q, want unchanged; payload=%+v", target.Harness, target.Status, target)
		}
		if target.Error != "" {
			t.Errorf("%s error = %q, want empty", target.Harness, target.Error)
		}
	}
}

func testNewSkillsSyncCmdDefaultSyncsEveryPresentHarness(t *testing.T, cfg skillsync.Config) {
	home := t.TempDir()
	for _, name := range []string{".claude", ".cursor"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, home))
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{
		filepath.Join(".claude", "skills", "wb-worktrees", "SKILL.md"),
		filepath.Join(".cursor", "skills", "wb-worktrees", "SKILL.md"),
	} {
		if _, err := os.Stat(filepath.Join(home, rel)); err != nil {
			t.Errorf("present-harness default did not install %s: %v", rel, err)
		}
	}
	if _, err := os.Stat(filepath.Join(home, ".codex", "skills", "wb-worktrees", "SKILL.md")); !os.IsNotExist(err) {
		t.Fatal("absent Codex must not be created by the present-harness default")
	}
}

type skillsSyncJSON struct {
	Status         string   `json:"status"`
	Harness        string   `json:"harness,omitempty"`
	Dir            string   `json:"dir"`
	DryRun         bool     `json:"dry_run"`
	PriorWBVersion string   `json:"prior_wb_version,omitempty"`
	WBVersion      string   `json:"wb_version"`
	Added          []string `json:"added,omitempty"`
	Updated        []string `json:"updated,omitempty"`
	Unchanged      []string `json:"unchanged,omitempty"`
	Removed        []string `json:"removed,omitempty"`
	Conflicts      []string `json:"conflicts,omitempty"`
	Error          string   `json:"error,omitempty"`
}

type skillsSyncMultiJSON struct {
	DryRun    bool             `json:"dry_run"`
	WBVersion string           `json:"wb_version"`
	Targets   []skillsSyncJSON `json:"targets"`
}

func testNewSkillsSyncCmdJSONFormatReportsEveryField(t *testing.T, cfg skillsync.Config) {
	dir := filepath.Join(t.TempDir(), "skills")

	command := newSkillsSyncCmd(testRuntime(), testSyncDependencies(cfg, filepath.Dir(dir)))
	command.SetArgs([]string{"--dir", dir, "--format", "json"})
	var out bytes.Buffer
	command.SetOut(&out)
	if err := command.Execute(); err != nil {
		t.Fatal(err)
	}
	var payload skillsSyncJSON
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	gotInfo, gotErr := os.Stat(payload.Dir)
	wantInfo, wantErr := os.Stat(dir)
	if gotErr != nil || wantErr != nil || !os.SameFile(gotInfo, wantInfo) {
		t.Errorf("Dir = %q, want the same target as %q (gotErr=%v wantErr=%v)", payload.Dir, dir, gotErr, wantErr)
	}
	if len(payload.Added) == 0 {
		t.Errorf("Added = %v, want at least one skill on a first sync", payload.Added)
	}
	if payload.WBVersion == "" {
		t.Error("WBVersion is empty")
	}
}

// The embedded source is immutable; only the source/descriptor is shared.
// Each child owns its Home/Getenv callbacks, command, reports and mutation target.
// The cursor journey retains the real environment binding and runs serially;
// the other children run in parallel after its environment cleanup completes.
//
//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
func TestEmbeddedSkillsHarnessJourneys(t *testing.T) {
	start := time.Now()
	cfg, err := wbskills.Config(ai.SkillsFS, buildinfo.Snapshot())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("immutable source setup: %s", time.Since(start))
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdInstallsIntoAnExplicitDirAndIsIdempotent", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdInstallsIntoAnExplicitDirAndIsIdempotent(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdDryRunWritesNothing", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdDryRunWritesNothing(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdReportsConflictsAsFindings", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdReportsConflictsAsFindings(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdHarnessFlagInstallsIntoCursor", func(t *testing.T) {
		start := time.Now()
		testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdJSONReportsMultipleHarnessTargets", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdJSONReportsMultipleHarnessTargets(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdJSONReportsEveryCurrentHarnessAsUnchanged", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdJSONReportsEveryCurrentHarnessAsUnchanged(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdDefaultSyncsEveryPresentHarness", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdDefaultSyncsEveryPresentHarness(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
	//nolint:paralleltest // Process-wide environment changes in testNewSkillsSyncCmdHarnessFlagInstallsIntoCursor; these rows share their parent environment and remain sequential.
	t.Run("TestNewSkillsSyncCmdJSONFormatReportsEveryField", func(t *testing.T) {
		t.Parallel()
		start := time.Now()
		testNewSkillsSyncCmdJSONFormatReportsEveryField(t, cfg)
		t.Logf("operation including isolated target setup: %s", time.Since(start))
	})
}
