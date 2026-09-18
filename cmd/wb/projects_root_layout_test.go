package main

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
)

// warnAboutRetiredWBHomeFixture pins WB_HOME at a directory that exists and
// contains state, so a test can prove WB ignores both the variable and its
// bytes. It returns the directory and a byte-level snapshot of it.
func warnAboutRetiredWBHomeFixture(t *testing.T) (string, map[string][]byte) {
	t.Helper()
	pinned := filepath.Join(t.TempDir(), "pinned-wb-home")
	for path, content := range map[string]string{
		"README.md":                      "operator state that must survive\n",
		"worktrees/task/claim.json":      `{"claim":"keep"}`,
		"sessions/0000000000000001.json": `{"id":"keep"}`,
	} {
		absolute := filepath.Join(pinned, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(wbhome.EnvHomeRetired, pinned)
	return pinned, snapshotTrees(t, pinned)
}

// TestStateCreatingCommandWarnsAboutIgnoredWBHomeAndKeepsItsState encodes
// projects-root-layout#ac:wb-home-ignored-with-diagnostic: with WB_HOME set to
// a directory that exists and contains state, a state-creating command ignores
// the variable, writes under <root>/.wb, emits a diagnostic naming the
// variable, its value and the state directory in use, and leaves the ignored
// directory's bytes untouched.
func TestStateCreatingCommandWarnsAboutIgnoredWBHomeAndKeepsItsState(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv(wbhome.EnvOverride, "")
	pinned, before := warnAboutRetiredWBHomeFixture(t)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"session", "prune"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("session prune exit = %d; stderr: %s", code, stderr.String())
	}

	state, err := wbhome.Root("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(userHome, "projects", ".wb"); filepath.Clean(state) != filepath.Clean(want) {
		// wbhome resolves symlinks (macOS /var -> /private/var); compare
		// through the same resolver rather than the raw fixture path.
		t.Fatalf("resolved state directory = %q, want %q", state, want)
	}
	if _, err := os.Stat(filepath.Join(state, "README.md")); err != nil {
		t.Fatalf("state was not created under the resolved root %s: %v", state, err)
	}
	if after := snapshotTrees(t, pinned); !reflect.DeepEqual(before, after) {
		t.Fatalf("WB_HOME %s was written to; before=%v after=%v", pinned, before, after)
	}
	diagnostic := stderr.String()
	for _, want := range []string{wbhome.EnvHomeRetired, pinned, state} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("stderr %q does not name %q", diagnostic, want)
		}
	}
}

// TestGarbageWBHomeDoesNotChangeStateOrFail covers the AC's failure clause: an
// unusable WB_HOME value cannot change the resolved state directory or make the
// command fail.
func TestGarbageWBHomeDoesNotChangeStateOrFail(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv(wbhome.EnvOverride, "")
	blocker := filepath.Join(t.TempDir(), "regular-file")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(blocker, "nested", "pinned-home")
	t.Setenv(wbhome.EnvHomeRetired, garbage)

	var stdout, stderr bytes.Buffer
	if code := run([]string{"session", "prune"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("session prune with an unusable WB_HOME exit = %d; stderr: %s", code, stderr.String())
	}
	state, err := wbhome.Root("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "README.md")); err != nil {
		t.Fatalf("state was not created under %s: %v", state, err)
	}
	if !strings.Contains(stderr.String(), garbage) {
		t.Fatalf("stderr %q does not name the ignored value %q", stderr.String(), garbage)
	}
}

// TestExplicitProjectsRootNeverFallsBackToALegacyHome proves the AC's last
// clause: neither the ignored WB_HOME nor a pre-existing retired $HOME/.wb
// checkout hierarchy becomes a write home.
func TestExplicitProjectsRootNeverFallsBackToALegacyHome(t *testing.T) {
	userHome := t.TempDir()
	t.Setenv("HOME", userHome)
	t.Setenv(wbhome.EnvOverride, "")
	legacy := filepath.Join(userHome, ".wb")
	if err := os.MkdirAll(filepath.Join(legacy, "worktrees", "keep"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacy, "worktrees", "keep", "marker"), []byte("legacy\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacyBefore := snapshotTrees(t, legacy)
	pinned, pinnedBefore := warnAboutRetiredWBHomeFixture(t)

	root := t.TempDir()
	var stdout, stderr bytes.Buffer
	if code := run([]string{"--projects-root", root, "session", "prune"}, &stdout, &stderr); code != exitOK {
		t.Fatalf("session prune exit = %d; stderr: %s", code, stderr.String())
	}

	state, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(state, "README.md")); err != nil {
		t.Fatalf("state was not created under the selected root %s: %v", state, err)
	}
	if after := snapshotTrees(t, legacy); !reflect.DeepEqual(legacyBefore, after) {
		t.Fatalf("a command wrote to the retired $HOME/.wb home; before=%v after=%v", legacyBefore, after)
	}
	if after := snapshotTrees(t, pinned); !reflect.DeepEqual(pinnedBefore, after) {
		t.Fatalf("a command wrote to WB_HOME %s; before=%v after=%v", pinned, pinnedBefore, after)
	}
	for _, want := range []string{pinned, state} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr %q does not name %q", stderr.String(), want)
		}
	}
}

// TestHookExecutionPathSuppressesTheWBHomeDiagnostic documents the one
// visibility exception: the machine hook-execution leaves — `wb hooks run`,
// which every WB-generated git shim runs, and `wb hooks agent ...`, which a
// WB-generated settings file runs around every agent tool call — repeat too
// often to nag, and the WB_HOME they carry was planted by WB rather than chosen
// by an operator. The hook-management verbs that can actually remove a stale
// pin keep warning, and so does every ordinary command.
func TestHookExecutionPathSuppressesTheWBHomeDiagnostic(t *testing.T) {
	for _, commandID := range []string{"hooks run", "hooks agent", "hooks agent pre-tool-use", "hooks agent install"} {
		if !hookExecutionCommand(commandID) {
			t.Errorf("hookExecutionCommand(%q) = false, want true", commandID)
		}
	}
	for _, commandID := range []string{"hooks install", "hooks repair", "hooks check", "hooks measure", "worktree create", "session prune"} {
		if hookExecutionCommand(commandID) {
			t.Errorf("hookExecutionCommand(%q) = true, want false", commandID)
		}
	}
}

// TestHookExecutionCommandEmitsNoWBHomeWarning proves the suppression is
// wired, not just declared: the agent-hook leaf runs with the retired variable
// set and its stderr never carries the diagnostic. The command's own exit code
// is deliberately ignored — only the warning's absence is asserted — so the
// test does not depend on agent-guard policy.
func TestHookExecutionCommandEmitsNoWBHomeWarning(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(wbhome.EnvOverride, "")
	t.Setenv(wbhome.EnvHomeRetired, filepath.Join(t.TempDir(), "pinned-wb-home"))

	var stdout, stderr bytes.Buffer
	_ = runWithStdin([]string{"hooks", "agent", "pre-tool-use"}, strings.NewReader("{}"), &stdout, &stderr)
	if strings.Contains(stderr.String(), wbhome.EnvHomeRetired) {
		t.Fatalf("agent-hook path emitted the retired-WB_HOME warning: %q", stderr.String())
	}
}

// TestWBHomeDiagnosticDoesNotChangeTheExitCode keeps the warning a warning: a
// rejected invocation still exits 2, exactly as it did before WB_HOME was
// retired.
func TestWBHomeDiagnosticDoesNotChangeTheExitCode(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	t.Setenv(wbhome.EnvHomeRetired, filepath.Join(t.TempDir(), "pinned-wb-home"))

	var stdout, stderr bytes.Buffer
	if code := run([]string{"--definitely-not-a-wb-flag"}, &stdout, &stderr); code != exitUsage {
		t.Fatalf("rejected invocation exit = %d, want %d; stderr: %s", code, exitUsage, stderr.String())
	}
}

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
