package wbhome

import (
	"os"
	"os/exec"
	"path/filepath"
	"sort"
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

// TestStateDirectoryDerivesFromProjectsRootNotWBHome encodes
// projects-root-layout#ac:one-root-no-second-knob: on a machine with WB_HOME
// set and no WB_PROJECTS_ROOT, a state-creating command writes under
// <root>/.wb and derives nothing from WB_HOME.
func TestStateDirectoryDerivesFromProjectsRootNotWBHome(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	ignored := filepath.Join(resolvedTempDir(t), "legacy-wb-home")
	t.Setenv("WB_HOME", ignored)

	root := resolvedTempDir(t)
	home, err := Root(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); home != want {
		t.Fatalf("Root(%q) = %q, want %q", root, home, want)
	}
	if resolved, err := filepath.EvalSymlinks(ignored); err == nil && strings.HasPrefix(home, resolved) {
		t.Fatalf("Root(%q) = %q derives from WB_HOME %q", root, home, ignored)
	}
}

// TestEnvProjectsRootSelectsTheRootWhenNoArgumentIsGiven covers the middle
// clause of the AC's precedence chain: --projects-root, else WB_PROJECTS_ROOT,
// else the default root.
func TestEnvProjectsRootSelectsTheRootWhenNoArgumentIsGiven(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	envRoot := resolvedTempDir(t)
	t.Setenv(EnvOverride, envRoot)

	home, err := Root("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(envRoot, ".wb"); home != want {
		t.Fatalf("Root(\"\") = %q, want %q", home, want)
	}
}

// TestDefaultRootIsProjectsUnderTheUserHome covers the final clause: with no
// flag and no environment override, the root is ~/projects.
func TestDefaultRootIsProjectsUnderTheUserHome(t *testing.T) {
	userHome := resolvedTempDir(t)
	t.Setenv("HOME", userHome)
	t.Setenv(EnvOverride, "")

	home, err := Root("")
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(userHome, "projects", ".wb"); home != want {
		t.Fatalf("Root(\"\") = %q, want %q", home, want)
	}
}

// TestStoreRootIsADotChildOfTheProjectsRoot encodes the store half of
// projects-root-layout#req:single-root-derivation: the checkout store is
// derived from the same root as the state directory.
func TestStoreRootIsADotChildOfTheProjectsRoot(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	root := resolvedTempDir(t)
	store, err := StoreRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".worktrees"); store != want {
		t.Fatalf("StoreRoot(%q) = %q, want %q", root, store, want)
	}
}

// TestResolveDerivesTheStoreFromTheProjectsRoot pins the store half of
// projects-root-layout#req:single-root-derivation: the write layout's physical
// checkout root is <root>/.worktrees, not a directory nested inside the state
// directory.
func TestResolveDerivesTheStoreFromTheProjectsRoot(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	root := resolvedTempDir(t)
	resolution, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); resolution.Write.Home != want {
		t.Fatalf("write home = %q, want %q", resolution.Write.Home, want)
	}
	if want := filepath.Join(root, ".worktrees"); resolution.Write.WorktreesRoot != want {
		t.Fatalf("write store = %q, want %q", resolution.Write.WorktreesRoot, want)
	}
	if got := resolution.Write.StateWorktreesRoot(); got != filepath.Join(root, ".wb", "worktrees") {
		t.Fatalf("logical task namespace = %q, want %q", got, filepath.Join(root, ".wb", "worktrees"))
	}
}

// TestResolveKeepsTheRetiredDefaultHomeReadable pins
// projects-root-layout#req:existing-checkouts-operable: checkouts left at the
// retired default state directory $HOME/.wb/worktrees stay discoverable in
// place, without being moved or rewritten.
func TestResolveKeepsTheRetiredDefaultHomeReadable(t *testing.T) {
	userHome := resolvedTempDir(t)
	t.Setenv("HOME", userHome)
	t.Setenv(EnvOverride, "")
	retired := filepath.Join(userHome, ".wb", "worktrees", "in-flight", "acme", "app")
	if err := os.MkdirAll(retired, 0o755); err != nil {
		t.Fatal(err)
	}
	root := resolvedTempDir(t)
	resolution, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); resolution.Write.Home != want {
		t.Fatalf("write home = %q, want %q", resolution.Write.Home, want)
	}
	if len(resolution.Read) != 3 {
		t.Fatalf("read layouts = %#v, want the checkout store, the state task namespace, and the retired default home", resolution.Read)
	}
	legacy := resolution.Read[2]
	if !legacy.Legacy {
		t.Fatalf("retired default home is not marked legacy: %#v", legacy)
	}
	if want := filepath.Join(userHome, ".wb"); legacy.Home != want {
		t.Fatalf("legacy home = %q, want %q", legacy.Home, want)
	}
	if want := filepath.Join(userHome, ".wb", "worktrees"); legacy.WorktreesRoot != want {
		t.Fatalf("legacy store = %q, want %q", legacy.WorktreesRoot, want)
	}
}

// TestResolveIgnoresAnEmptyRetiredHome keeps a stray empty ~/.wb from becoming
// an implicit configuration bit that changes every command's read set.
func TestResolveIgnoresAnEmptyRetiredHome(t *testing.T) {
	userHome := resolvedTempDir(t)
	t.Setenv("HOME", userHome)
	t.Setenv(EnvOverride, "")
	if err := os.MkdirAll(filepath.Join(userHome, ".wb"), 0o755); err != nil {
		t.Fatal(err)
	}
	root := resolvedTempDir(t)
	resolution, err := Resolve(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolution.Read) != 2 || resolution.Read[0] != resolution.Write {
		t.Fatalf("read layouts = %#v, want the checkout store plus the state task namespace", resolution.Read)
	}
	if got := resolution.Read[1].WorktreesRoot; got != resolution.Write.StateWorktreesRoot() {
		t.Fatalf("state task namespace = %q, want %q", got, resolution.Write.StateWorktreesRoot())
	}
}

// TestRootEnumerationYieldsHostDirectoriesOnly encodes
// projects-root-layout#ac:root-namespace-stays-clean: a `*` glob and a bare
// `ls` of a root holding host directories plus .wb and .worktrees return host
// directories only.
func TestRootEnumerationYieldsHostDirectoriesOnly(t *testing.T) {
	t.Setenv("HOME", resolvedTempDir(t))
	t.Setenv(EnvOverride, "")
	root := resolvedTempDir(t)
	hosts := []string{"github.com", "gitlab.com"}
	for _, host := range hosts {
		if err := os.MkdirAll(filepath.Join(root, host, "acme", "app", ".git"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	// Both WB-private children must really exist, or the enumeration would be
	// clean for the wrong reason.
	home, err := EnsureRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(root, ".wb"); home != want {
		t.Fatalf("EnsureRoot(%q) = %q, want %q", root, home, want)
	}
	store, err := StoreRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(store, "some-task", "github.com", "acme", "app"), 0o755); err != nil {
		t.Fatal(err)
	}

	want := append([]string(nil), hosts...)
	sort.Strings(want)

	// A shell `*` glob, exactly as a search-scoped command expands it.
	glob := exec.Command("sh", "-c", `for entry in *; do printf '%s\n' "$entry"; done`)
	glob.Dir = root
	globOut, err := glob.Output()
	if err != nil {
		t.Fatalf("expand * in %s: %v", root, err)
	}
	if got := nonEmptyLines(string(globOut)); !equalStrings(got, want) {
		t.Fatalf("* glob in %s = %v, want host directories only %v", root, got, want)
	}

	// A bare `ls`, without -a.
	ls := exec.Command("ls")
	ls.Dir = root
	lsOut, err := ls.Output()
	if err != nil {
		t.Fatalf("ls %s: %v", root, err)
	}
	if got := nonEmptyLines(string(lsOut)); !equalStrings(got, want) {
		t.Fatalf("ls %s = %v, want host directories only %v", root, got, want)
	}

	for _, hidden := range []string{".wb", ".worktrees"} {
		if _, err := os.Stat(filepath.Join(root, hidden)); err != nil {
			t.Fatalf("fixture is missing %s: %v", hidden, err)
		}
	}
}

func nonEmptyLines(output string) []string {
	lines := make([]string, 0)
	for _, line := range strings.Split(output, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func equalStrings(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}
	return true
}

func TestEnsureRootSeedsReadmeInHomeDirectory(t *testing.T) {
	t.Setenv(EnvOverride, "")
	projectsRoot := resolvedTempDir(t)
	root, err := EnsureRoot(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(projectsRoot, ".wb"); root != want {
		t.Fatalf("EnsureRoot(%q) = %q, want %q", projectsRoot, root, want)
	}
	contents, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatalf("expected README.md seeded in WB home: %v", err)
	}
	if !strings.Contains(string(contents), "https://sneat.work/bench") {
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
	t.Parallel()
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
	projectsRoot := filepath.Join(home, "projects")
	if err := os.MkdirAll(projectsRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	root, err := Root(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(resolvedHome, "projects", ".wb")
	if root != want {
		t.Fatalf("Root() = %q, want the ancestor-resolved %q", root, want)
	}
}
