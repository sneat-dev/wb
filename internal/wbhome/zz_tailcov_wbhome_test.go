package wbhome

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// tailCovRegularFile writes a regular file and returns its path. Several WB
// home failures are "a path component is a file, not a directory" errors,
// which are reported distinctly from a missing directory.
func tailCovRegularFile(t *testing.T, dir, name string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte("not a directory"), 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestTailCovResolutionSurfacesUnresolvableHomePaths pins that an unusable
// WB_HOME or user home is an error rather than a silently wrong directory.
func TestTailCovResolutionSurfacesUnresolvableHomePaths(t *testing.T) {
	t.Run("no user home", func(t *testing.T) {
		t.Setenv(EnvOverride, "")
		t.Setenv("HOME", "")
		if _, err := Resolve(t.TempDir()); err == nil || !strings.Contains(err.Error(), "user home directory") {
			t.Fatalf("Resolve err = %v, want an unresolvable user home reported", err)
		}
		if _, err := Root(t.TempDir()); err == nil {
			t.Fatal("Root must propagate an unresolvable home")
		}
		if _, err := EnsureRoot(t.TempDir()); err == nil {
			t.Fatal("EnsureRoot must propagate an unresolvable home")
		}
	})
	t.Run("unresolvable override", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		file := tailCovRegularFile(t, dir, "regular")
		t.Setenv(EnvOverride, filepath.Join(file, "home"))
		if _, err := Resolve(t.TempDir()); err == nil {
			t.Fatal("an override that cannot be resolved must fail instead of selecting a home")
		}
	})
	t.Run("uninspectable legacy directory", func(t *testing.T) {
		t.Setenv(EnvOverride, "")
		t.Setenv("HOME", resolvedTempDir(t))
		notADir := tailCovRegularFile(t, t.TempDir(), "projects-root")
		if _, err := Resolve(notADir); err == nil {
			t.Fatal("a projects root that cannot be inspected must fail")
		}
	})
}

// TestTailCovEnsureRootRefusesAnUnusableWriteHome pins that EnsureRoot reports
// a home it cannot prepare rather than proceeding to write into it.
func TestTailCovEnsureRootRefusesAnUnusableWriteHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	file := tailCovRegularFile(t, dir, "regular")
	t.Setenv(EnvOverride, file)
	if _, err := EnsureRoot(t.TempDir()); err == nil || !strings.Contains(err.Error(), "not a directory") {
		t.Fatalf("EnsureRoot err = %v, want the unusable home reported", err)
	}
}

// TestTailCovEnsureHomeReportsFilesystemFailures drives each failure branch of
// EnsureHome directly: an uncreatable home, an uninspectable one, and a path
// that exists but is not a directory.
func TestTailCovEnsureHomeReportsFilesystemFailures(t *testing.T) {
	t.Run("uncreatable home", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Symlink(filepath.Join(dir, "missing-target"), filepath.Join(dir, "dangling")); err != nil {
			t.Fatal(err)
		}
		err := EnsureHome(filepath.Join(dir, "dangling", "home"))
		if err == nil || !strings.Contains(err.Error(), "create WB home") {
			t.Fatalf("EnsureHome err = %v, want the creation failure reported", err)
		}
	})
	t.Run("uninspectable home", func(t *testing.T) {
		file := tailCovRegularFile(t, t.TempDir(), "regular")
		err := EnsureHome(filepath.Join(file, "home"))
		if err == nil || !strings.Contains(err.Error(), "inspect WB home") {
			t.Fatalf("EnsureHome err = %v, want the inspection failure reported", err)
		}
	})
	t.Run("home is not a directory", func(t *testing.T) {
		file := tailCovRegularFile(t, t.TempDir(), "regular")
		err := EnsureHome(file)
		if err == nil || !strings.Contains(err.Error(), "not a directory") {
			t.Fatalf("EnsureHome err = %v, want a non-directory home rejected", err)
		}
	})
}

// TestTailCovSeedReadmeReportsFilesystemFailures pins that a README that
// cannot be inspected or written is reported rather than swallowed.
func TestTailCovSeedReadmeReportsFilesystemFailures(t *testing.T) {
	t.Run("uninspectable readme", func(t *testing.T) {
		file := tailCovRegularFile(t, t.TempDir(), "regular")
		err := SeedReadme(file)
		if err == nil || !strings.Contains(err.Error(), "inspect WB home README") {
			t.Fatalf("SeedReadme err = %v, want the inspection failure reported", err)
		}
	})
	t.Run("unwritable readme", func(t *testing.T) {
		missing := filepath.Join(t.TempDir(), "absent-home")
		err := SeedReadme(missing)
		if err == nil || !strings.Contains(err.Error(), "write WB home README") {
			t.Fatalf("SeedReadme err = %v, want the write failure reported", err)
		}
	})
}

// TestTailCovPinnedHomeMarkerRequiresAResolvableUserHome pins that the
// migration marker alone cannot make an explicit home migration-compatible
// when the default user home cannot be resolved for comparison.
func TestTailCovPinnedHomeMarkerRequiresAResolvableUserHome(t *testing.T) {
	pinned := resolvedTempDir(t)
	t.Setenv("HOME", "")
	t.Setenv(EnvOverride, pinned)
	t.Setenv(EnvMigrationCompat, pinned)
	resolution, err := Resolve(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Explicit || len(resolution.Read) != 1 || resolution.Read[0].Home != pinned {
		t.Fatalf("resolution = %#v, want the explicit home alone with no migration compatibility", resolution)
	}
}

// TestTailCovResolveAbsReportsUninspectablePaths pins that resolveAbs returns
// the filesystem's error instead of a half-resolved path when a component
// cannot be traversed (here: a regular file used as a directory).
func TestTailCovResolveAbsReportsUninspectablePaths(t *testing.T) {
	file := tailCovRegularFile(t, t.TempDir(), "regular")
	if resolved, err := resolveAbs(filepath.Join(file, "child")); err == nil {
		t.Fatalf("resolveAbs(%q) = %q, want the traversal error", filepath.Join(file, "child"), resolved)
	}
}
