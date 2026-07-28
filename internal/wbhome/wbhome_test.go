package wbhome

import (
	"os"
	"path/filepath"
	"testing"
)

// resolvedTempDir returns a fresh temp directory with symlinks resolved, e.g.
// macOS's /var -> /private/var. Root() resolves its result the same way (see
// resolveAbs), so an expectation built from the raw, unresolved t.TempDir()
// would fail even when Root() is behaving correctly.
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestRootDefaultsUnderUserHome(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t) // no .wb here: a fresh install
	root, err := Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".wb")
	if root != want {
		t.Fatalf("Root() = %q, want %q", root, want)
	}
}

func TestResolveKeepsNewDefaultWriteHomeAndReadsPopulatedLegacyDirectory(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	legacy := filepath.Join(projectsRoot, ".wb", "worktrees", "some-task")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resolution.Write.Home, filepath.Join(home, ".wb"); got != want {
		t.Fatalf("write home = %q, want default %q", got, want)
	}
	if len(resolution.Read) != 2 || !resolution.Read[1].Legacy || resolution.Read[1].Home != filepath.Join(projectsRoot, ".wb") {
		t.Fatalf("compatible layouts = %#v, want default + legacy", resolution.Read)
	}
}

func TestRootIgnoresEmptyLegacyDirectory(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	if err := os.MkdirAll(filepath.Join(projectsRoot, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(home, ".wb")
	if root != want {
		t.Fatalf("Root() = %q, want %q; an empty legacy directory carries no in-flight work to strand", root, want)
	}
}

func TestRootEnvOverrideWinsOverLegacy(t *testing.T) {
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	legacy := filepath.Join(projectsRoot, ".wb", "worktrees", "some-task")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	override := resolvedTempDir(t)
	t.Setenv(EnvOverride, override)
	root, err := Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if root != override {
		t.Fatalf("Root() = %q, want the explicit override %q", root, override)
	}
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Explicit || len(resolution.Read) != 1 || resolution.Read[0].Home != override {
		t.Fatalf("explicit resolution = %#v, want only override", resolution)
	}
}

func TestPinnedDefaultHookHomeKeepsLegacyLayoutReadable(t *testing.T) {
	home := resolvedTempDir(t)
	projectsRoot := resolvedTempDir(t)
	legacy := filepath.Join(projectsRoot, ".wb", "worktrees", "in-flight")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(home, ".wb")
	t.Setenv(EnvOverride, pinned)
	t.Setenv(EnvMigrationCompat, "default")
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Explicit || len(resolution.Read) != 2 || resolution.Read[0].Home != pinned || !resolution.Read[1].Legacy {
		t.Fatalf("pinned default resolution = %#v, want default write + legacy read", resolution)
	}
}

// TestRootResolvesSymlinkedAncestorForNotYetCreatedPath pins the bug this
// fixes: on macOS, os.TempDir() returns paths under /var, a symlink to
// /private/var. Root()'s own target (.wb) does not exist on a first run, so a
// naive EvalSymlinks on the whole path fails outright and must not fall back
// to leaving the ancestor unresolved — that would make Root() disagree with
// what `git rev-parse --show-toplevel` reports for the very same directory.
func TestRootResolvesSymlinkedAncestorForNotYetCreatedPath(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := t.TempDir() // deliberately NOT pre-resolved
	t.Setenv("HOME", home)
	resolvedHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		t.Fatal(err)
	}
	root, err := Root("")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedHome, ".wb")
	if root != want {
		t.Fatalf("Root() = %q, want the ancestor-resolved %q", root, want)
	}
}
