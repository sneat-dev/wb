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
// root, override, or user home is an error rather than a silently wrong
// directory. The root now comes from the explicit argument first, so each case
// passes an empty root to exercise the environment/default path it names.
func TestTailCovResolutionSurfacesUnresolvableHomePaths(t *testing.T) {
	t.Run("no user home", func(t *testing.T) {
		t.Setenv(EnvOverride, "")
		t.Setenv("HOME", "")
		if _, err := Resolve(""); err == nil || !strings.Contains(err.Error(), "user home directory") {
			t.Fatalf("Resolve err = %v, want an unresolvable user home reported", err)
		}
		if _, err := Root(""); err == nil {
			t.Fatal("Root must propagate an unresolvable home")
		}
		if _, err := EnsureRoot(""); err == nil {
			t.Fatal("EnsureRoot must propagate an unresolvable home")
		}
	})
	t.Run("unresolvable override", func(t *testing.T) {
		dir := t.TempDir()
		t.Setenv("HOME", dir)
		file := tailCovRegularFile(t, dir, "regular")
		t.Setenv(EnvOverride, filepath.Join(file, "home"))
		if _, err := Resolve(""); err == nil {
			t.Fatal("an override that cannot be resolved must fail instead of selecting a home")
		}
	})
	t.Run("uninspectable projects root", func(t *testing.T) {
		t.Setenv(EnvOverride, "")
		t.Setenv("HOME", resolvedTempDir(t))
		// A regular file used as a path component cannot be traversed, so the
		// root cannot be inspected; the resolver must report that rather than
		// hand back a half-resolved root.
		notADir := filepath.Join(tailCovRegularFile(t, t.TempDir(), "projects-root"), "child")
		if _, err := Resolve(notADir); err == nil {
			t.Fatal("a projects root that cannot be inspected must fail")
		}
	})
}

// TestTailCovEnsureRootRefusesAnUnusableWriteHome pins that EnsureRoot reports
// a home it cannot prepare rather than proceeding to write into it. The root
// comes from the override here, so the call passes an empty root argument.
func TestTailCovEnsureRootRefusesAnUnusableWriteHome(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	file := tailCovRegularFile(t, dir, "regular")
	t.Setenv(EnvOverride, file)
	if _, err := EnsureRoot(""); err == nil || !strings.Contains(err.Error(), "not a directory") {
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

// TestTailCovPinnedHomeMarkerRequiresAResolvableUserHome keeps the intent of
// the retired migration-compatibility test: the WB_HOME_MIGRATION_COMPAT
// marker alone can neither select the state directory nor add a legacy read
// layout. WB_PROJECTS_ROOT selects the root, the write layout is <root>/.wb,
// and with no resolvable user home there is no retired $HOME/.wb to read.
func TestTailCovPinnedHomeMarkerRequiresAResolvableUserHome(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", "")
	t.Setenv(EnvOverride, root)
	t.Setenv(EnvMigrationCompat, filepath.Join(root, ".wb"))
	resolution, err := Resolve("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); resolution.Write.Home != want {
		t.Fatalf("write home = %q, want %q", resolution.Write.Home, want)
	}
	if len(resolution.Read) != 2 || resolution.Read[0] != resolution.Write {
		t.Fatalf("resolution = %#v, want the write layout first plus the state task namespace", resolution)
	}
	for _, layout := range resolution.Read {
		if layout.Legacy {
			t.Fatalf("the retired migration marker added a legacy read layout: %#v", layout)
		}
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
