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
	previousRoot, previousGoEnv := userStateRoot, goEnvironmentValue
	t.Cleanup(func() { userStateRoot, goEnvironmentValue = previousRoot, previousGoEnv })
	userStateRoot = ""
	goEnvironmentValue = func(name string) (string, error) { return "/go-settings/" + name, nil }
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
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_CONFIG_HOME", config)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, ".local", "state"))
	t.Setenv("WB_HOME", filepath.Join(home, "projects", ".wb"))
	t.Setenv("WB_PROJECTS_ROOT", filepath.Join(home, "projects"))
	for _, name := range goToolVariables {
		t.Setenv(name, os.Getenv(name))
	}
	return home
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolateUserStateMovesEveryUserLocationUnderOnePrivateRoot(t *testing.T) {
	home := ambientUser(t)
	t.Setenv("GOCACHE", "")
	pinnedModules := filepath.Join(home, "already-pinned-modules")
	t.Setenv("GOMODCACHE", pinnedModules)

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
	if got := os.Getenv("GOCACHE"); got != "/go-settings/GOCACHE" {
		t.Fatalf("GOCACHE = %q, want it pinned to where the Go command keeps it, or a build under the private home starts from an empty cache", got)
	}
	remove()
	if _, err := os.Stat(userStateRoot); !os.IsNotExist(err) {
		t.Fatalf("the private root survived its removal: %v", err)
	}
}

//nolint:paralleltest // rewrites the process environment that selects the user.
func TestIsolateUserStateWorksWithoutAGoCommandOrGitConfiguration(t *testing.T) {
	home := ambientUser(t)
	for _, path := range []string{filepath.Join(home, ".gitconfig"), filepath.Join(home, ".config", "git", "config")} {
		if err := os.Remove(path); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("GOCACHE", "")
	goEnvironmentValue = func(string) (string, error) { return "", errors.New("go: command not found") }

	remove, err := IsolateUserState()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(remove)
	if violations := UserStateViolations(); len(violations) != 0 {
		t.Fatalf("isolated process can still reach real user state: %v", violations)
	}
	if value := os.Getenv("GOCACHE"); value != "" {
		t.Fatalf("GOCACHE = %q with no Go command to ask", value)
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
