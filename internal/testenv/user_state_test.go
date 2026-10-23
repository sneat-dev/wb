package testenv

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// ambientUser builds a developer's machine as the test process would inherit
// it: a real-looking home with Git identity, a wb.yaml, and the WB variables
// that select real state. Every variable is registered with t.Setenv first, so
// whatever IsolateUserState changes is restored when the test ends.
func ambientUser(t *testing.T) (home string) {
	t.Helper()
	previousRoot := userStateRoot
	t.Cleanup(func() { userStateRoot = previousRoot })
	userStateRoot = ""
	home = t.TempDir()
	config := filepath.Join(home, ".config")
	if err := os.MkdirAll(filepath.Join(config, "git"), 0o700); err != nil {
		t.Fatal(err)
	}
	for path, content := range map[string]string{
		filepath.Join(home, ".gitconfig"):       "[user]\n\tname = Ambient Developer\n",
		filepath.Join(config, "git", "config"):  "[user]\n\temail = ambient@example.test\n",
		filepath.Join(config, "wb", "wb.yaml"):  "remote:\n  provider: git\n  repo: acme/real-state\n",
		filepath.Join(home, "unrelated-marker"): "",
	} {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv(UserStateRootEnv, "")
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("WB_HOME", filepath.Join(home, "projects", ".wb"))
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(home, "projects"))
	// No Go setting is pinned by the environment: every one is derived.
	for _, name := range goToolVariables {
		t.Setenv(name, "")
	}
	t.Setenv("XDG_CACHE_HOME", "")
	return home
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolateUserStateMovesEveryUserLocationUnderOnePrivateRoot(t *testing.T) {
	home := ambientUser(t)
	pinnedModules := filepath.Join(home, "already-pinned-modules")
	t.Setenv("GOMODCACHE", pinnedModules)
	ambientCache, cacheErr := os.UserCacheDir()
	ambientConfig, configErr := os.UserConfigDir()
	if cacheErr != nil || configErr != nil {
		t.Fatalf("ambient user directories: %v, %v", cacheErr, configErr)
	}

	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	if got := os.Getenv("GOMODCACHE"); got != pinnedModules {
		t.Fatalf("GOMODCACHE = %q, want the value the environment already pinned", got)
	}
	if violations := UserStateViolations(); len(violations) != 0 {
		t.Fatalf("isolated process can still reach real user state: %v", violations)
	}
	isolatedHome, _ := os.UserHomeDir()
	if isolatedHome == home || strings.HasPrefix(os.Getenv("XDG_CONFIG_HOME"), home) {
		t.Fatalf("home %q and config %q still point at the ambient user %q", isolatedHome, os.Getenv("XDG_CONFIG_HOME"), home)
	}
	if _, err := os.Stat(filepath.Join(os.Getenv("XDG_CONFIG_HOME"), "wb", "wb.yaml")); !os.IsNotExist(err) {
		t.Fatalf("the ambient wb.yaml is visible in the isolated config home: %v", err)
	}
	// Git keeps the identity it had: the private home's .gitconfig includes
	// both of the ambient user's global configuration files.
	included, err := os.ReadFile(filepath.Join(isolatedHome, ".gitconfig"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{filepath.Join(home, ".config", "git", "config"), filepath.Join(home, ".gitconfig")} {
		if !strings.Contains(string(included), "[include]\n\tpath = \""+path+"\"\n") {
			t.Fatalf("private .gitconfig = %q, want it to include %s", included, path)
		}
	}
	// The Go toolchain stays where the ambient user had it: the cache at its
	// documented default, GOPATH and GOENV under the ambient home, or a build
	// under the private home would start from an empty cache.
	if got := os.Getenv("GOCACHE"); got != filepath.Join(ambientCache, "go-build") {
		t.Fatalf("GOCACHE = %q, want go-build under the ambient cache directory %q", got, ambientCache)
	}
	if got := os.Getenv("GOPATH"); got != filepath.Join(home, "go") {
		t.Fatalf("GOPATH = %q, want the ambient default %q", got, filepath.Join(home, "go"))
	}
	if got := os.Getenv("GOENV"); got != filepath.Join(ambientConfig, "go", "env") {
		t.Fatalf("GOENV = %q, want the ambient settings file under %q", got, ambientConfig)
	}
	remove()
	if _, err := os.Stat(userStateRoot); !os.IsNotExist(err) {
		t.Fatalf("the private root survived its removal: %v", err)
	}
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolateUserStateWorksWithoutGitConfiguration(t *testing.T) {
	home := ambientUser(t)
	for _, path := range []string{filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git", "config")} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "")

	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	if violations := UserStateViolations(); len(violations) != 0 {
		t.Fatalf("isolated process can still reach real user state: %v", violations)
	}
	isolatedHome, _ := os.UserHomeDir()
	if content, err := os.ReadFile(filepath.Join(isolatedHome, ".gitconfig")); err != nil || len(content) != 0 {
		t.Fatalf("private .gitconfig = %q, %v; want an empty file", content, err)
	}
}

// failedIsolation runs IsolateUserState with a temporary-directory source that
// cannot produce a usable root and asserts nothing about the process moved.
func failedIsolation(t *testing.T, temp func(string, string) (string, error)) {
	t.Helper()
	home := ambientUser(t)
	previous := makeTempDir
	t.Cleanup(func() { makeTempDir = previous })
	makeTempDir = temp
	remove, err := IsolateUserState()
	if err == nil || remove != nil {
		t.Fatalf("remove set = %t, err = %v; want a failure", remove != nil, err)
	}
	if got, _ := os.UserHomeDir(); got != home || userStateRoot != "" {
		t.Fatalf("a failed isolation moved the home to %q (root %q)", got, userStateRoot)
	}
	if violations := UserStateViolations(); len(violations) != 1 || !strings.Contains(violations[0], "was not called") {
		t.Fatalf("violations = %v", violations)
	}
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolateUserStateLeavesTheProcessUntouchedWithoutATemporaryDirectory(t *testing.T) {
	failedIsolation(t, func(string, string) (string, error) { return "", errors.New("no space left on device") })
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolateUserStateLeavesTheProcessUntouchedWhenItsRootVanishes(t *testing.T) {
	gone := filepath.Join(t.TempDir(), "gone")
	failedIsolation(t, func(string, string) (string, error) { return gone, nil })
}

// The guard is what makes the isolation structural: it names every way a test
// process could reach a real user again, whoever undid it.
//
//nolint:paralleltest // rewrites the process environment that selects the user.
func TestUserStateViolationsNameEveryEscapeFromThePrivateRoot(t *testing.T) {
	ambientUser(t)
	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	outside := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", outside)
	t.Setenv("WB_PROJECTS_ROOT", outside)
	inside := filepath.Join(userStateRoot, "config", "wb", "wb.yaml")
	resolved := filepath.Join(outside, "wb.yaml")

	violations := strings.Join(UserStateViolations(inside, resolved), "\n")
	for _, want := range []string{outside + " is outside the test user root", resolved + " is outside the test user root", "WB_PROJECTS_ROOT is set"} {
		if !strings.Contains(violations, want) {
			t.Fatalf("violations = %q, want %q", violations, want)
		}
	}
	if strings.Contains(violations, inside) || strings.Contains(violations, "WB_HOME") {
		t.Fatalf("violations name a path inside the root or an unset variable: %q", violations)
	}
}

// A wb process given no projects root resolves ~/projects and opens it, so the
// private home has one, and every child inherits where the root is.
//
//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolatedUserHasTheDefaultProjectsRootAndNamesItsRootForChildren(t *testing.T) {
	ambientUser(t)
	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	home, _ := os.UserHomeDir()
	if info, err := os.Stat(filepath.Join(home, "projects")); err != nil || !info.IsDir() {
		t.Fatalf("default projects root under the private home: %v", err)
	}
	if got := os.Getenv(UserStateRootEnv); got != userStateRoot || got == "" {
		t.Fatalf("%s = %q, want the private root %q", UserStateRootEnv, got, userStateRoot)
	}
}

// The test binary re-executed as a helper process runs TestMain again. It must
// keep the environment its parent test gave it, not replace it with an empty
// user of its own: reported from CI, where a helper's wb children lost the
// parent's WB_PROJECTS_ROOT and opened a projects root that did not exist.
//
//nolint:paralleltest // rewrites the process environment that selects the user.
func TestReexecutedTestBinaryAdoptsItsParentsIsolationUnchanged(t *testing.T) {
	ambientUser(t)
	removeParent, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(removeParent)
	parentRoot := userStateRoot
	// What a parent test sets for the processes it starts.
	fixture := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", fixture)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(fixture, "config"))
	parentHome, _ := os.UserHomeDir()

	userStateRoot = "" // the child starts with no state of its own
	removeChild, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	childHome, _ := os.UserHomeDir()
	if userStateRoot != parentRoot || childHome != parentHome || os.Getenv("WB_PROJECTS_ROOT") != fixture ||
		os.Getenv("XDG_CONFIG_HOME") != filepath.Join(fixture, "config") {
		t.Fatalf("child root = %q, home = %q, WB_PROJECTS_ROOT = %q; want the parent's environment unchanged",
			userStateRoot, childHome, os.Getenv("WB_PROJECTS_ROOT"))
	}
	removeChild()
	if _, err := os.Stat(parentRoot); err != nil {
		t.Fatalf("the child removed its parent's private root: %v", err)
	}
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestAStaleOrForeignRootMarkerIsNotAdopted(t *testing.T) {
	for name, marker := range map[string]string{
		"root no longer exists": filepath.Join(t.TempDir(), "wb-test-user-gone"),
		"not a private root":    t.TempDir(),
		"private name, no home": mustMakeDirectory(t, filepath.Join(t.TempDir(), "wb-test-user-empty")),
	} {
		home := ambientUser(t)
		t.Setenv(UserStateRootEnv, marker)
		remove, err := IsolateUserState()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		isolated, _ := os.UserHomeDir()
		if userStateRoot == marker || isolated == home || len(UserStateViolations()) != 0 {
			t.Fatalf("%s: root = %q, home = %q, violations = %v; want a fresh private user", name, userStateRoot, isolated, UserStateViolations())
		}
		remove()
	}
}

func mustMakeDirectory(t *testing.T, path string) string {
	t.Helper()
	if err := os.Mkdir(path, 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

// `go env -w` saves settings in the GOENV file, and the go command reads them
// from there. A saved GOPATH moves the module cache with it; the environment
// still wins over the file.
//
//nolint:paralleltest // rewrites the process environment that selects the user.
func TestGoSettingsSavedInTheGoEnvFileAreKeptWhereTheyPoint(t *testing.T) {
	home := ambientUser(t)
	goEnv := filepath.Join(home, "saved-go-env")
	saved := "GOPATH=" + filepath.Join(home, "gopath-one") + string(os.PathListSeparator) + filepath.Join(home, "gopath-two") +
		"\nGOCACHE=" + filepath.Join(home, "saved-cache") + "\nnot a setting\n"
	if err := os.WriteFile(goEnv, []byte(saved), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GOENV", goEnv)

	pinned := resolvedGoToolVariables()
	want := map[string]string{
		"GOPATH":     filepath.Join(home, "gopath-one") + string(os.PathListSeparator) + filepath.Join(home, "gopath-two"),
		"GOMODCACHE": filepath.Join(home, "gopath-one", "pkg", "mod"),
		"GOCACHE":    filepath.Join(home, "saved-cache"),
	}
	if len(pinned) != len(want) {
		t.Fatalf("pinned = %v, want exactly %v (GOENV is already in the environment)", pinned, want)
	}
	for name, value := range want {
		if pinned[name] != value {
			t.Fatalf("%s = %q, want %q", name, pinned[name], value)
		}
	}
}

func TestALocationThatCannotBeResolvedHasNothingUnderIt(t *testing.T) {
	t.Parallel()
	if got := under("", "go", "env"); got != "" {
		t.Fatalf("under an unresolved base = %q, want nothing", got)
	}
	if got, want := under("base", "go", "env"), filepath.Join("base", "go", "env"); got != want {
		t.Fatalf("under = %q, want %q", got, want)
	}
}
