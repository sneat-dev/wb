package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// TestStateCreatingCommandIgnoresWBHomeAndUsesTheDefaultRoot encodes
// projects-root-layout#ac:one-root-no-second-knob at the command level: with
// WB_HOME set and no WB_PROJECTS_ROOT, a state-creating command writes under
// <root>/.wb and derives no path from WB_HOME.
func TestStateCreatingCommandIgnoresWBHomeAndUsesTheDefaultRoot(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv(wbhome.EnvOverride, "")
	ignored := filepath.Join(t.TempDir(), "legacy-wb-home")
	if err := os.MkdirAll(ignored, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", ignored)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"session", "prune"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("session prune exit = %d; stderr: %s", code, stderr.String())
	}

	state := filepath.Join(userHome, "projects", ".wb")
	if _, err := os.Stat(filepath.Join(state, "README.md")); err != nil {
		t.Fatalf("state was not created under the default root %s: %v", state, err)
	}
	entries, err := os.ReadDir(ignored)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("WB_HOME %s was used as state: %v", ignored, entries)
	}
}

// TestProjectsRootFlagWinsOverEnvForStateCreation encodes the precedence
// clause of projects-root-layout#ac:one-root-no-second-knob: --projects-root
// wins over WB_PROJECTS_ROOT.
func TestProjectsRootFlagWinsOverEnvForStateCreation(t *testing.T) {
	envRoot := t.TempDir()
	flagRoot := t.TempDir()
	t.Setenv(wbhome.EnvOverride, envRoot)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", flagRoot, "session", "prune"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("session prune exit = %d; stderr: %s", code, stderr.String())
	}

	if _, err := os.Stat(filepath.Join(flagRoot, ".wb", "README.md")); err != nil {
		t.Fatalf("--projects-root %s did not receive state: %v", flagRoot, err)
	}
	if _, err := os.Stat(filepath.Join(envRoot, ".wb")); !os.IsNotExist(err) {
		t.Fatalf("WB_PROJECTS_ROOT %s received state despite --projects-root: %v", envRoot, err)
	}
}

// TestDaemonStateFileResolvesUnderProjectsRoot encodes the daemon clause of
// projects-root-layout#ac:one-root-no-second-knob: <root>/.wb/runtime/daemon-state.json.
func TestDaemonStateFileResolvesUnderProjectsRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, ".wb", "runtime", "daemon-state.json")
	got, err := daemonStatePath(root)
	if err != nil {
		t.Fatalf("daemonStatePath(%q): %v", root, err)
	}
	if got != want {
		t.Fatalf("daemonStatePath(%q) = %q, want %q", root, got, want)
	}
	if got != filepath.Join(home, "runtime", "daemon-state.json") {
		t.Fatalf("daemon state file %q is not inside the resolved state directory %q", got, home)
	}
}
