package sessiontransport

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func writeConfig(t *testing.T, contents string) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "wb.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write fixture config: %v", err)
	}
	return path
}

func TestLoadOverrideAbsentFileIsNotAnError(t *testing.T) {
	t.Parallel()
	kind, ok, err := LoadOverride(filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if err != nil {
		t.Fatalf("LoadOverride(missing file) error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("LoadOverride(missing file) ok = true, want false")
	}
	if kind != "" {
		t.Fatalf("LoadOverride(missing file) kind = %q, want empty", kind)
	}
}

func TestLoadOverrideNoSessionSectionIsNotAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session_move:\n  targets: {}\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride(no session section) error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("LoadOverride(no session section) ok = true, want false")
	}
	if kind != "" {
		t.Fatalf("LoadOverride(no session section) kind = %q, want empty", kind)
	}
}

func TestLoadOverrideNoTransportKeyIsNotAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session: {}\n")
	_, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride(empty session section) error = %v, want nil", err)
	}
	if ok {
		t.Fatalf("LoadOverride(empty session section) ok = true, want false")
	}
}

func TestLoadOverrideReadsConfiguredTransport(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session:\n  transport: tmux\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride error = %v, want nil", err)
	}
	if !ok {
		t.Fatalf("LoadOverride ok = false, want true")
	}
	if kind != KindTmux {
		t.Fatalf("LoadOverride kind = %q, want %q", kind, KindTmux)
	}
}

func TestLoadOverrideToleratesOtherTopLevelSections(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session_move:\n  targets:\n    mac:\n      default_courier: ssh\n      ssh:\n        host: mac\nsession:\n  transport: herdr\n")
	kind, ok, err := LoadOverride(path)
	if err != nil {
		t.Fatalf("LoadOverride error = %v, want nil", err)
	}
	if !ok || kind != KindHerdr {
		t.Fatalf("LoadOverride = (%q, %v), want (%q, true)", kind, ok, KindHerdr)
	}
}

func TestLoadOverrideUnreadableFileIsAnError(t *testing.T) {
	t.Parallel()
	// A directory can never be read as a config file: os.ReadFile fails
	// with something other than os.ErrNotExist, exercising the distinct
	// "the file exists but could not be read" branch.
	_, _, err := LoadOverride(t.TempDir())
	if err == nil {
		t.Fatal("LoadOverride(a directory) error = nil, want an error")
	}
}

func TestLoadOverrideMalformedYAMLIsAnError(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session: [this is not a mapping\n")
	_, _, err := LoadOverride(path)
	if err == nil {
		t.Fatal("LoadOverride(malformed YAML) error = nil, want an error")
	}
}

// TestResolveOverrideExplicitTmuxOverrideFailsClosedWithNoTmuxBinary proves
// AC:explicit-override-fails-closed directly: `wb.yaml` names
// session.transport: tmux, no tmux binary is on PATH, and resolving the
// transport refuses rather than silently falling back to herdr or none.
func TestResolveOverrideExplicitTmuxOverrideFailsClosedWithNoTmuxBinary(t *testing.T) {
	t.Parallel()
	path := writeConfig(t, "session:\n  transport: tmux\n")
	requested, ok, err := LoadOverride(path)
	if err != nil || !ok {
		t.Fatalf("LoadOverride = (%q, %v, %v), want (tmux, true, nil)", requested, ok, err)
	}

	noTmuxOnPath := func(string) (string, error) {
		return "", errors.New("exec: \"tmux\": executable file not found in $PATH")
	}

	resolved, err := ResolveOverride(requested, CheckTmuxBinary(noTmuxOnPath))
	if err == nil {
		t.Fatalf("ResolveOverride succeeded with resolved = %q, want a fail-closed error", resolved)
	}
	if !errors.Is(err, ErrOverridePrerequisiteUnavailable) {
		t.Fatalf("ResolveOverride error = %v, want ErrOverridePrerequisiteUnavailable", err)
	}
	if resolved == KindHerdr || resolved == KindNone {
		t.Fatalf("ResolveOverride silently fell back to %q instead of refusing", resolved)
	}
	if resolved != "" {
		t.Fatalf("ResolveOverride returned non-empty Kind %q alongside an error", resolved)
	}
}

func TestResolveOverrideExplicitTmuxOverrideSucceedsWhenTmuxIsAvailable(t *testing.T) {
	t.Parallel()
	tmuxOnPath := func(file string) (string, error) {
		if file != "tmux" {
			t.Fatalf("CheckTmuxBinary looked up %q, want \"tmux\"", file)
		}
		return "/usr/local/bin/tmux", nil
	}
	resolved, err := ResolveOverride(KindTmux, CheckTmuxBinary(tmuxOnPath))
	if err != nil {
		t.Fatalf("ResolveOverride error = %v, want nil", err)
	}
	if resolved != KindTmux {
		t.Fatalf("ResolveOverride = %q, want %q", resolved, KindTmux)
	}
}

func TestResolveOverrideRejectsAnUnshippedTransport(t *testing.T) {
	t.Parallel()
	resolved, err := ResolveOverride("docker", nil)
	if err == nil {
		t.Fatalf("ResolveOverride(\"docker\") succeeded with %q, want an error", resolved)
	}
	if !errors.Is(err, ErrOverrideInvalid) {
		t.Fatalf("ResolveOverride error = %v, want ErrOverrideInvalid", err)
	}
	if resolved != "" {
		t.Fatalf("ResolveOverride(\"docker\") resolved = %q, want empty", resolved)
	}
}

func TestResolveOverrideWithNilCheckAcceptsAnyShippedKind(t *testing.T) {
	t.Parallel()
	resolved, err := ResolveOverride(KindHerdr, nil)
	if err != nil {
		t.Fatalf("ResolveOverride error = %v, want nil", err)
	}
	if resolved != KindHerdr {
		t.Fatalf("ResolveOverride = %q, want %q", resolved, KindHerdr)
	}
}

func TestCheckTmuxBinaryIgnoresOtherKinds(t *testing.T) {
	t.Parallel()
	neverCalled := func(string) (string, error) {
		t.Fatal("CheckTmuxBinary's lookPath must not be called for a non-tmux Kind")
		return "", nil
	}
	if err := CheckTmuxBinary(neverCalled)(KindHerdr); err != nil {
		t.Fatalf("CheckTmuxBinary(...)(KindHerdr) = %v, want nil", err)
	}
	if err := CheckTmuxBinary(neverCalled)(KindNone); err != nil {
		t.Fatalf("CheckTmuxBinary(...)(KindNone) = %v, want nil", err)
	}
}

func TestCheckTmuxBinaryDefaultsToExecLookPath(t *testing.T) {
	t.Parallel()
	// A nil lookPath must not panic; it falls back to exec.LookPath, whose
	// outcome depends on the host and is not asserted here.
	_ = CheckTmuxBinary(nil)(KindTmux)
}

func TestComposeChecksStopsAtFirstFailure(t *testing.T) {
	t.Parallel()
	var secondCalled bool
	first := func(Kind) error { return errors.New("first check failed") }
	second := func(Kind) error { secondCalled = true; return nil }

	err := ComposeChecks(first, second)(KindHerdr)
	if err == nil || err.Error() != "first check failed" {
		t.Fatalf("ComposeChecks error = %v, want %q", err, "first check failed")
	}
	if secondCalled {
		t.Fatal("ComposeChecks called the second check after the first failed")
	}
}

func TestComposeChecksSkipsNilEntries(t *testing.T) {
	t.Parallel()
	err := ComposeChecks(nil, func(Kind) error { return nil }, nil)(KindNone)
	if err != nil {
		t.Fatalf("ComposeChecks with nil entries = %v, want nil", err)
	}
}

func TestComposeChecksAllPass(t *testing.T) {
	t.Parallel()
	calls := 0
	pass := func(Kind) error { calls++; return nil }
	if err := ComposeChecks(pass, pass)(KindTmux); err != nil {
		t.Fatalf("ComposeChecks(pass, pass) = %v, want nil", err)
	}
	if calls != 2 {
		t.Fatalf("ComposeChecks called its checks %d times, want 2", calls)
	}
}
