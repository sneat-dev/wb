package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/strongo/cli-helpers/skillsync"
	skillscmd "github.com/strongo/cli-helpers/skillsync/cobracmd"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

func TestNewSkillsSyncConfigBindsTheEmbeddedWBPluginToThisBuild(t *testing.T) {
	cfg, err := newSkillsSyncConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.CLI != wbSkillsCLI || cfg.CurrentVersion != collectVersion().Version {
		t.Fatalf("CLI config = %+v @ %q", cfg.CLI, cfg.CurrentVersion)
	}
	if len(cfg.Bundles) != 1 {
		t.Fatalf("bundles = %d, want one WB plugin", len(cfg.Bundles))
	}
	bundle := cfg.Bundles[0]
	if bundle.Plugin != wbSkillsPlugin {
		t.Errorf("plugin = %+v, want %+v", bundle.Plugin, wbSkillsPlugin)
	}
	if bundle.Source.Repository != "github.com/sneat-dev/wb" || bundle.Source.Path != "ai/skills" {
		t.Errorf("source = %+v", bundle.Source)
	}
	wantRevision := collectVersion().Revision
	if len(wantRevision) != 40 {
		wantRevision = wbSkillsUnknownSource
	}
	if bundle.Source.Revision != wantRevision {
		t.Errorf("revision = %q, want %q", bundle.Source.Revision, wantRevision)
	}
	if bundle.Source.Digest == "" {
		t.Error("embedded plugin digest is empty")
	}
	if flag := newSkillsSyncCmd().Flags().Lookup("newer-compatible"); flag == nil {
		t.Error("explicit newer-compatible selection flag is missing")
	}
}

func TestNewSkillsSyncCmdInstallsIntoAnExplicitDirAndIsIdempotent(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")

	first := newSkillsSyncCmd()
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

	second := newSkillsSyncCmd()
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

func TestNewSkillsSyncCmdDryRunWritesNothing(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")

	command := newSkillsSyncCmd()
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

func TestNewSkillsSyncCmdJSONFormatReportsEveryField(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")

	command := newSkillsSyncCmd()
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

func TestNewSkillsSyncCmdReportsConflictsAsFindings(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "skills")
	if err := os.MkdirAll(filepath.Join(dir, "wb-worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wb-worktrees", "SKILL.md"), []byte("not wb's\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := newSkillsSyncCmd()
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

func TestSkillsDriftMessageNamesTheDirAndBothVersionsOrTheMissingInstall(t *testing.T) {
	never := skillsDriftMessage("/home/user/.claude/skills", skillsync.Status{}, "1.2.3")
	for _, want := range []string{"/home/user/.claude/skills", "wb skills sync"} {
		if !strings.Contains(never, want) {
			t.Errorf("never-installed message = %q, missing %q", never, want)
		}
	}

	plugin := wbSkillsPlugin.String()
	drifted := skillsDriftMessage("/home/user/.claude/skills", skillsync.Status{
		Installed: true,
		Plugins: map[string]skillsync.Source{
			plugin: {},
		},
		SupplierCLIVersions: map[string]map[string]string{
			plugin: {wbSkillsCLI.String(): "1.0.0"},
		},
	}, "1.2.3")
	for _, want := range []string{"1.0.0", "1.2.3", "wb skills sync"} {
		if !strings.Contains(drifted, want) {
			t.Errorf("drift message = %q, missing %q", drifted, want)
		}
	}
}

func TestOrdinaryCommandsNeverPrintSkillsDrift(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // no ~/.claude at all: no Claude Code on this machine
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })

	root := newRootCmd()
	root.SetArgs([]string{"commands", "--format", "json"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Errorf("a machine with no ~/.claude must never see the drift banner; stderr=%q", stderr.String())
	}
}

func TestOrdinaryCommandsStaySilentWhenSkillsAreStale(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })

	root := newRootCmd()
	root.SetArgs([]string{"commands", "--format", "json"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "wb skills sync") {
		t.Errorf("ordinary commands must leave skills drift to SessionStart; stderr=%q", stderr.String())
	}
}

func TestMaybeWarnSkillsDriftNeverFiresForTheSkillsCommandFamily(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })

	root := newRootCmd()
	root.SetArgs([]string{"skills", "sync", "--dir", filepath.Join(home, ".claude", "skills")})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "wb: Agent Skills") {
		t.Errorf("`wb skills sync` must never warn about the drift it is itself fixing; stderr=%q", stderr.String())
	}
}

func TestNewSkillsSyncCmdHarnessFlagInstallsIntoCursor(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	command := newSkillsSyncCmd()
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

func TestNewSkillsSyncCmdJSONReportsMultipleHarnessTargets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	command := newSkillsSyncCmd()
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

func TestNewSkillsSyncCmdJSONReportsEveryCurrentHarnessAsUnchanged(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")

	first := newSkillsSyncCmd()
	first.SetArgs([]string{"--harness", "cursor,codex"})
	if err := first.Execute(); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	second := newSkillsSyncCmd()
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

func TestWriteSkillsSyncJSONIncludesTargetError(t *testing.T) {
	var out bytes.Buffer
	err := writeSkillsSyncJSON(&out, []skillscmd.TargetResult{{
		Harness: "codex",
		Dir:     "/tmp/codex/skills",
		Report: skillsync.Report{
			Dir:        "/tmp/codex/skills",
			CLIVersion: "0.96.6",
		},
		Err: errors.New("legacy marker content differs"),
	}})
	if err != nil {
		t.Fatal(err)
	}
	var payload skillsSyncJSON
	if err := json.Unmarshal(out.Bytes(), &payload); err != nil {
		t.Fatalf("output is not valid JSON: %v\n%s", err, out.String())
	}
	if payload.Status != "failed" {
		t.Errorf("status = %q, want failed", payload.Status)
	}
	if payload.Error != "legacy marker content differs" {
		t.Errorf("error = %q", payload.Error)
	}
	if payload.Harness != "codex" || payload.Dir != "/tmp/codex/skills" {
		t.Errorf("target = %+v", payload)
	}
}

func TestWriteSkillsSyncTextDoesNotDescribeAFailedTargetAsCurrent(t *testing.T) {
	var out bytes.Buffer
	err := writeSkillsSyncText(&out, skillscmd.TargetResult{
		Dir: "/tmp/codex/skills",
		Err: errors.New("invalid legacy wb_version"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "nothing to do") {
		t.Errorf("failed target output = %q, must not claim it is current", out.String())
	}
	for _, want := range []string{"wb skills sync failed", "invalid legacy wb_version"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("failed target output = %q, missing %q", out.String(), want)
		}
	}
}

func TestNewSkillsSyncCmdDefaultSyncsEveryPresentHarness(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	for _, name := range []string{".claude", ".cursor"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	command := newSkillsSyncCmd()
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

func TestOrdinaryCommandsDoNotWarnAboutSkillsDrift(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CLAUDE_CONFIG_DIR", "")
	t.Setenv("CODEX_HOME", "")
	for _, name := range []string{".claude", ".cursor"} {
		if err := os.Mkdir(filepath.Join(home, name), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })

	root := newRootCmd()
	root.SetArgs([]string{"commands", "--format", "json"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if stderr.Len() != 0 {
		t.Fatalf("ordinary command emitted skills drift noise: %q", stderr.String())
	}
}

func TestNewSkillsSyncCmdRejectsDirAndHarnessTogether(t *testing.T) {
	command := newSkillsSyncCmd()
	command.SetArgs([]string{"--dir", t.TempDir(), "--harness", "cursor"})
	err := command.Execute()
	if err == nil {
		t.Fatal("expected --dir and --harness together to fail")
	}
	var coded *exitError
	if !errors.As(err, &coded) || coded.code != exitUsage {
		t.Fatalf("err = %v, want exitUsage", err)
	}
}
