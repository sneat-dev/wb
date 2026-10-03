package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/buildinfo"
)

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
