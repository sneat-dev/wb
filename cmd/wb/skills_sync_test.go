package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/skills"
)

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
	if _, err := skills.ReadMarker(dir); err != nil {
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
	if payload.Dir != dir {
		t.Errorf("Dir = %q, want %q", payload.Dir, dir)
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
	never := skillsDriftMessage("/home/user/.claude/skills", skills.Status{}, "1.2.3")
	for _, want := range []string{"/home/user/.claude/skills", "wb skills sync"} {
		if !strings.Contains(never, want) {
			t.Errorf("never-installed message = %q, missing %q", never, want)
		}
	}

	drifted := skillsDriftMessage("/home/user/.claude/skills",
		skills.Status{Installed: true, SyncedWBVersion: "1.0.0"}, "1.2.3")
	for _, want := range []string{"1.0.0", "1.2.3", "wb skills sync"} {
		if !strings.Contains(drifted, want) {
			t.Errorf("drift message = %q, missing %q", drifted, want)
		}
	}
}

func TestMaybeWarnSkillsDriftIsSilentWithoutAClaudeDirectory(t *testing.T) {
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

func TestMaybeWarnSkillsDriftPrintsOnceWhenNeverSynced(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	if err := os.MkdirAll(filepath.Join(home, ".claude"), 0o755); err != nil {
		t.Fatal(err)
	}
	buildinfo.Set("1.2.3")
	t.Cleanup(func() { buildinfo.Set("") })

	root := newRootCmd()
	root.SetArgs([]string{"version"})
	var stdout, stderr bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(stderr.String(), "wb skills sync") {
		t.Errorf("`wb version` is on the suppression list and must never print the drift banner; stderr=%q", stderr.String())
	}

	root = newRootCmd()
	root.SetArgs([]string{"commands", "--format", "json"})
	stdout.Reset()
	stderr.Reset()
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stderr.String(), "wb skills sync") {
		t.Errorf("an ordinary command with skills never synced under an existing ~/.claude must print the drift banner on stderr; stderr=%q", stderr.String())
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
