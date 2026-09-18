package wbhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIgnoredHomeEnvDiagnosticNamesVariableValueAndStateDirectory encodes
// projects-root-layout#ac:wb-home-ignored-with-diagnostic at the resolver
// level: WB_HOME set to a directory that exists and contains state is ignored,
// and the diagnostic names the variable, the value it ignored, and the state
// directory actually in use.
func TestIgnoredHomeEnvDiagnosticNamesVariableValueAndStateDirectory(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	ignored := filepath.Join(resolvedTempDir(t), "pinned-home")
	if err := os.MkdirAll(filepath.Join(ignored, "worktrees", "task"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(ignored, "README.md"), []byte("operator state\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", ignored)

	root := resolvedTempDir(t)
	diagnostic, err := IgnoredHomeEnvDiagnostic(root)
	if err != nil {
		t.Fatalf("IgnoredHomeEnvDiagnostic(%q): %v", root, err)
	}
	for _, want := range []string{EnvHomeRetired, ignored, filepath.Join(root, ".wb")} {
		if !strings.Contains(diagnostic, want) {
			t.Fatalf("diagnostic %q does not name %q", diagnostic, want)
		}
	}
}

// TestIgnoredHomeEnvDiagnosticIsSilentWithoutNonEmptyWBHome keeps the warning
// off every ordinary invocation: only a non-empty WB_HOME is worth reporting.
func TestIgnoredHomeEnvDiagnosticIsSilentWithoutNonEmptyWBHome(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	root := resolvedTempDir(t)

	t.Setenv("WB_HOME", "")
	if diagnostic, err := IgnoredHomeEnvDiagnostic(root); err != nil || diagnostic != "" {
		t.Fatalf("empty WB_HOME diagnostic = (%q, %v), want (\"\", nil)", diagnostic, err)
	}

	t.Setenv("WB_HOME", "   ")
	if diagnostic, err := IgnoredHomeEnvDiagnostic(root); err != nil || diagnostic != "" {
		t.Fatalf("blank WB_HOME diagnostic = (%q, %v), want (\"\", nil)", diagnostic, err)
	}
}

// TestIgnoredHomeEnvDiagnosticSurvivesAnUnusableValue proves a garbage or
// unresolvable WB_HOME neither changes the resolved state directory nor turns
// into an error the command would have to fail on.
func TestIgnoredHomeEnvDiagnosticSurvivesAnUnusableValue(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	blocker := filepath.Join(resolvedTempDir(t), "regular-file")
	if err := os.WriteFile(blocker, []byte("not a directory\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	garbage := filepath.Join(blocker, "nested", "pinned-home")
	t.Setenv("WB_HOME", garbage)

	root := resolvedTempDir(t)
	diagnostic, err := IgnoredHomeEnvDiagnostic(root)
	if err != nil {
		t.Fatalf("unusable WB_HOME must not fail the diagnostic: %v", err)
	}
	if !strings.Contains(diagnostic, garbage) {
		t.Fatalf("diagnostic %q does not name the ignored value %q", diagnostic, garbage)
	}
	home, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); home != want {
		t.Fatalf("unusable WB_HOME changed the state directory: Root(%q) = %q, want %q", root, home, want)
	}
}

// TestResolveNeverAdoptsWBHomeAsAHome encodes the fallback clause of the AC:
// the retired variable cannot become a write or read home, even when the
// directory it names exists and holds state.
func TestResolveNeverAdoptsWBHomeAsAHome(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	ignored := filepath.Join(resolvedTempDir(t), "pinned-home")
	if err := os.MkdirAll(filepath.Join(ignored, "worktrees", "task"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_HOME", ignored)

	root := resolvedTempDir(t)
	resolution, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); resolution.Write.Home != want {
		t.Fatalf("Write.Home = %q, want %q", resolution.Write.Home, want)
	}
	for _, layout := range resolution.Read {
		if layout.Home == ignored {
			t.Fatalf("WB_HOME %q was adopted as a readable home: %#v", ignored, resolution.Read)
		}
	}
}
