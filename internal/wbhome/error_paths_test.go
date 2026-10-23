package wbhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIgnoredHomeEnvDiagnosticSurfacesARootFailure covers
// IgnoredHomeEnvDiagnostic's Root() error branch (wbhome.go:57-59): when
// WB_HOME is set (so the function proceeds past its early "nothing to
// report" return) but the projects root itself cannot be resolved, the
// diagnostic must surface that failure rather than silently reporting
// nothing. A projects-root value containing a NUL byte is not a valid path
// component on any platform WB runs on, so resolveAbs's EvalSymlinks call
// fails deterministically.
//
//nolint:paralleltest // calls t.Setenv (WB_HOME), which Go's testing package forbids combined with t.Parallel
func TestIgnoredHomeEnvDiagnosticSurfacesARootFailure(t *testing.T) {
	t.Setenv(EnvHomeRetired, "some-value")
	if _, err := IgnoredHomeEnvDiagnostic("bad\x00root"); err == nil {
		t.Fatal("IgnoredHomeEnvDiagnostic with an unresolvable projects root = nil error, want one")
	}
}

// TestResolveSurfacesALegacyHomeResolutionFailure covers both Resolve's
// error-propagation branch for legacyUserLayout (wbhome.go:116-118) and
// legacyUserLayout's own resolveAbs error branch (wbhome.go:136-138).
// $HOME is pointed at a regular file: os.UserHomeDir() still succeeds (it
// just reads the environment variable), but resolving "<HOME>/.wb" fails,
// because a regular file cannot have a child path component (ENOTDIR, not
// ErrNotExist, so resolveAbs cannot treat it as "doesn't exist yet").
//
//nolint:paralleltest // calls t.Setenv (HOME), which Go's testing package forbids combined with t.Parallel
func TestResolveSurfacesALegacyHomeResolutionFailure(t *testing.T) {
	dir := t.TempDir()
	homeFile := filepath.Join(dir, "home-is-a-file")
	if err := os.WriteFile(homeFile, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", homeFile)
	projectsRoot := filepath.Join(dir, "projects")
	_, err := Resolve(projectsRoot)
	if err == nil {
		t.Fatal("Resolve with an unresolvable legacy home = nil error, want one")
	}
	if !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("Resolve error = %v, want it to name the not-a-directory failure resolving the legacy home", err)
	}
}

// TestStoreRootSurfacesAResolveFailure covers StoreRoot's Resolve error
// branch (wbhome.go:152-154).
func TestStoreRootSurfacesAResolveFailure(t *testing.T) {
	t.Parallel()
	if _, err := StoreRoot("bad\x00root"); err == nil {
		t.Fatal("StoreRoot with an unresolvable projects root = nil error, want one")
	}
}
