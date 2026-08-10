package wbhome

import (
	"os"
	"path/filepath"
	"strings"
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
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	legacy := filepath.Join(projectsRoot, ".wb", "worktrees", "in-flight")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	pinned := filepath.Join(home, ".wb")
	t.Setenv(EnvOverride, pinned)
	t.Setenv(EnvMigrationCompat, pinned)
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if resolution.Explicit || len(resolution.Read) != 2 || resolution.Read[0].Home != pinned || !resolution.Read[1].Legacy {
		t.Fatalf("pinned default resolution = %#v, want default write + legacy read", resolution)
	}
}

func TestAmbientMigrationMarkerCannotWeakenExplicitHome(t *testing.T) {
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	legacy := filepath.Join(projectsRoot, ".wb", "worktrees", "in-flight")
	if err := os.MkdirAll(legacy, 0o755); err != nil {
		t.Fatal(err)
	}
	explicit := filepath.Join(resolvedTempDir(t), "isolated-home")
	t.Setenv(EnvOverride, explicit)
	// This is the prior release's generic marker. It may be ambient in a
	// caller's shell but must not turn a non-default explicit home into a
	// migration-compatible one.
	t.Setenv(EnvMigrationCompat, "default")
	resolution, err := Resolve(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !resolution.Explicit || len(resolution.Read) != 1 || resolution.Read[0].Home != explicit {
		t.Fatalf("explicit home resolution = %#v, want only the explicit home", resolution)
	}
}

func TestEnsureRootSeedsReadmeInHomeDirectory(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	root, err := EnsureRoot(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	contents, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("expected README.md seeded in WB home: %v", err)
	}
	if !strings.Contains(string(contents), "https://sneat.dev/workbench") {
		t.Fatalf("README.md missing workbench link, got: %s", contents)
	}
	readme := strings.Join(strings.Fields(string(contents)), " ")
	for _, durableState := range []string{"Work Logs", "prompt archives", "cleanup backlogs", "recovery evidence"} {
		if !strings.Contains(readme, durableState) {
			t.Errorf("README.md does not identify durable %s, got: %s", durableState, contents)
		}
	}
	if strings.Contains(readme, "safe to delete") || !strings.Contains(readme, "Do not delete") {
		t.Fatalf("README.md must not describe the durable WB home as disposable, got: %s", contents)
	}
}

func TestEnsureRootDoesNotOverwriteExistingReadme(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	root, err := EnsureRoot(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	readmePath := filepath.Join(root, "README.md")
	custom := []byte("these are my own notes\n")
	if err := os.WriteFile(readmePath, custom, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := EnsureRoot(projectsRoot); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(custom) {
		t.Fatalf("README.md was overwritten: got %q, want preserved %q", got, custom)
	}
}

func TestEnsureHomeRefusesSymlinkedHome(t *testing.T) {
	dir := resolvedTempDir(t)
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	linked := filepath.Join(dir, "linked")
	if err := os.Symlink(real, linked); err != nil {
		t.Fatal(err)
	}
	if err := EnsureHome(linked); err == nil || !strings.Contains(err.Error(), "symlinked") {
		t.Fatalf("EnsureHome(symlinked) = %v, want a symlinked-home error", err)
	}
}

// TestEnsureRootDoesNotRaceCreateWorktreesOwnHomeOpen pins the ordering bug
// this fixes: Create's beforeHomeDirectoryOpen test seam (and its real
// descriptor-anchored, symlink-rejecting open of the same directory) needs
// WB's home to not exist yet when it runs. Root must stay a pure resolver so
// that seam still gets the first, unhardened look at the path — only
// EnsureRoot may create it.
func TestEnsureRootDoesNotRaceCreateWorktreesOwnHomeOpen(t *testing.T) {
	t.Setenv(EnvOverride, "")
	home := resolvedTempDir(t)
	t.Setenv("HOME", home)
	projectsRoot := resolvedTempDir(t)
	root, err := Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, statErr := os.Stat(root); !os.IsNotExist(statErr) {
		t.Fatalf("Root() must not create the home directory itself: stat error = %v", statErr)
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
