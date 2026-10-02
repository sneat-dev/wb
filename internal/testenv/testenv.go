// Package testenv isolates a test process from the ambient machine state
// documented in internal/envguard: WB_AGENT_* variables inherited from
// whichever agent is operating the shell that launched `go test`, and an
// ambient GOWORK the test process would otherwise carry into every `go`
// invocation it makes.
//
// WB's own test suite runs as a subprocess of that operating agent. A test
// that asserts "no active owner" or "no live registered session" observes
// the outer agent's identity instead of a clean slate unless it calls
// Isolate first: WB_AGENT_PID/WB_AGENT_RUNTIME/WB_AGENT_MODEL/WB_AGENT_ID are
// inherited by the `go test` binary itself, not only by subprocesses wb
// spawns, so internal/envguard's subprocess-environment sanitizing cannot
// reach them.
package testenv

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/envguard"
	"github.com/sneat-dev/wb/internal/execfile"
)

// Isolate truly unsets every WB_AGENT_* variable -- the key itself is
// removed from the environment, not merely emptied -- and sets GOWORK=off
// for the current test's environment. It restores every value it changed,
// via t.Cleanup, when t (or the subtest it was called from) completes.
//
// t.Setenv("WB_AGENT_X", "") is not enough: it leaves the key present with
// an empty value, and envguard.Inspect (like most agent-var detection)
// keys off presence in os.Environ(), not value, so an emptied variable
// still reads as set. Isolate calls os.Unsetenv directly instead.
//
// A test that intentionally exercises agent-mode behavior calls Isolate
// first and then sets its own WB_AGENT_* value with t.Setenv, so the
// intentional value applies last and is the one the code under test
// observes.
//
// Like t.Setenv, Isolate makes the calling test (and its subtests) unsafe
// to run in parallel with sibling tests that also touch the process
// environment: it mutates shared process state and only restores it on
// this test's own Cleanup.
func Isolate(t testing.TB) {
	t.Helper()
	// Route through t.Setenv first so the standard library marks this test
	// (and any test that already called t.Parallel) as ineligible for
	// parallel execution, exactly as if Isolate had used t.Setenv
	// throughout -- before this function makes any direct os.Unsetenv call
	// of its own.
	t.Setenv("GOWORK", "off")
	for _, entry := range os.Environ() {
		name, value, found := strings.Cut(entry, "=")
		if !found || !envguard.IsAgentVar(name) {
			continue
		}
		original := value
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("testenv.Isolate: unsetenv %s: %v", name, err)
		}
		t.Cleanup(func() {
			if err := os.Setenv(name, original); err != nil {
				t.Errorf("testenv.Isolate: restore %s: %v", name, err)
			}
		})
	}
}

// IsolateProcess performs the same isolation as Isolate for a caller with no
// *testing.T -- typically a package's TestMain, which isolates its whole
// test binary process before any test runs. Unlike Isolate this is not
// restored: TestMain's process exits once m.Run() returns, so there is
// nothing to restore it for.
func IsolateProcess() {
	for _, entry := range os.Environ() {
		name, _, found := strings.Cut(entry, "=")
		if found && envguard.IsAgentVar(name) {
			_ = os.Unsetenv(name)
		}
	}
	_ = os.Setenv("GOWORK", "off")
	// WB's descriptor-secure paths intentionally refuse symlinked ancestors.
	// macOS commonly exposes its temporary directory through /var while the
	// physical path is /private/var; leaving that alias in TMPDIR makes fixtures
	// test the host alias instead of the WB behavior they were written for.
	// Resolve it once for the whole package test process so every t.TempDir uses
	// the same physical identity that WB's path resolvers return.
	if resolved, err := filepath.EvalSymlinks(os.TempDir()); err == nil {
		_ = os.Setenv("TMPDIR", resolved)
	}
}

// gitAutoMaintenanceKeys are the three settings that stop a git subprocess
// from ever dispatching background maintenance work of its own. Nothing in
// production relies on or sets any of them; they exist purely to keep a
// test's own throwaway git fixtures from racing t.TempDir()'s cleanup with
// a detached gc/maintenance writer (task-21, decision 13; see #711).
//
//   - gc.auto=0 stops `git gc --auto` from ever repacking, pruning, or
//     rewriting pack-refs/reflogs on its own.
//   - maintenance.auto=false stops the newer, separate `git maintenance
//     run --auto` dispatch that a sufficiently new git runs opportunistically
//     after ordinary commands, even with gc.auto=0 (#711's review traced
//     this directly: on CI's git, `git maintenance run --auto` started 129
//     times per test run with only gc.auto=0 set).
//   - receive.autogc=false stops receive-pack's own post-push auto-gc call.
var gitAutoMaintenanceKeys = [3]string{"gc.auto", "maintenance.auto", "receive.autogc"}
var gitAutoMaintenanceValues = [3]string{"0", "false", "false"}

// GitAutoMaintenanceOffEnv returns base with GIT_CONFIG_COUNT/KEY_N/VALUE_N
// entries appended that disable gc.auto, maintenance.auto and
// receive.autogc for any git subprocess started with the returned
// environment. If base already carries a GIT_CONFIG_COUNT sequence (for
// example a caller's own repo-identity overrides), the three keys are
// appended after the existing ones and GIT_CONFIG_COUNT is raised to match,
// rather than overwriting or colliding with them.
//
// This only reaches a git subprocess started directly with this
// environment. It does not reach a server-side `receive-pack` a same-host
// `git push` spawns as its own child process: git strips every
// GIT_CONFIG_* variable from the environment it hands to that child (#711's
// review traced this directly). A bare remote a test pushes to must also be
// configured with ConfigureGitAutoMaintenanceOff on the repository itself.
func GitAutoMaintenanceOffEnv(base []string) []string {
	count := 0
	for _, entry := range base {
		name, value, found := strings.Cut(entry, "=")
		if found && name == "GIT_CONFIG_COUNT" {
			if parsed, err := strconv.Atoi(value); err == nil && parsed > count {
				count = parsed
			}
		}
	}
	env := make([]string, 0, len(base)+len(gitAutoMaintenanceKeys)+1)
	for _, entry := range base {
		name, _, found := strings.Cut(entry, "=")
		if found && name == "GIT_CONFIG_COUNT" {
			continue
		}
		env = append(env, entry)
	}
	for index, key := range gitAutoMaintenanceKeys {
		env = append(env,
			"GIT_CONFIG_KEY_"+strconv.Itoa(count+index)+"="+key,
			"GIT_CONFIG_VALUE_"+strconv.Itoa(count+index)+"="+gitAutoMaintenanceValues[index],
		)
	}
	env = append(env, "GIT_CONFIG_COUNT="+strconv.Itoa(count+len(gitAutoMaintenanceKeys)))
	return env
}

// SetGitAutoMaintenanceOff sets GIT_CONFIG_COUNT/KEY_N/VALUE_N for the
// current test's process environment (via t.Setenv, restored on Cleanup)
// so that every git subprocess this test starts directly -- and every
// subprocess of those, such as a locally spawned `git-upload-pack` -- has
// gc.auto, maintenance.auto and receive.autogc disabled. It extends any
// GIT_CONFIG_COUNT sequence already present in the process environment
// rather than overwriting it.
//
// Like GitAutoMaintenanceOffEnv, this does not reach a server-side
// `receive-pack` a same-host `git push` spawns for a bare remote (see its
// doc comment); call ConfigureGitAutoMaintenanceOff on that repository
// directly as well.
func SetGitAutoMaintenanceOff(t testing.TB) {
	t.Helper()
	count := 0
	if parsed, err := strconv.Atoi(os.Getenv("GIT_CONFIG_COUNT")); err == nil && parsed > count {
		count = parsed
	}
	for index, key := range gitAutoMaintenanceKeys {
		t.Setenv("GIT_CONFIG_KEY_"+strconv.Itoa(count+index), key)
		t.Setenv("GIT_CONFIG_VALUE_"+strconv.Itoa(count+index), gitAutoMaintenanceValues[index])
	}
	t.Setenv("GIT_CONFIG_COUNT", strconv.Itoa(count+len(gitAutoMaintenanceKeys)))
}

// GitAutoMaintenanceOffProcess performs SetGitAutoMaintenanceOff's work for a
// caller with no *testing.T -- a package's TestMain -- so that every git
// subprocess the test binary starts inherits gc.auto, maintenance.auto and
// receive.autogc disabled. That includes git started by the production code
// under test (a pull, fetch or commit against a fixture clone, or a clone or
// temporary worktree production creates itself), which no per-command
// GitAutoMaintenanceOffEnv reaches and no ConfigureGitAutoMaintenanceOff call
// can name in advance. Like IsolateProcess this is not restored: TestMain's
// process exits once m.Run() returns. A bare remote pushed to over a local
// transport still needs ConfigureGitAutoMaintenanceOff (see its doc comment).
func GitAutoMaintenanceOffProcess() {
	for _, entry := range GitAutoMaintenanceOffEnv(os.Environ()) {
		name, value, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(name, "GIT_CONFIG_") {
			_ = os.Setenv(name, value)
		}
	}
}

// ConfigureGitAutoMaintenanceOff runs `git config` inside repoPath to
// disable gc.auto, maintenance.auto and receive.autogc directly in that
// repository's own config file.
//
// This is REQUIRED, not merely redundant, for any bare repository a test
// fixture pushes to over a local transport: git strips every GIT_CONFIG_*
// variable from the environment it hands to the server-side `receive-pack`
// it spawns for that push, so SetGitAutoMaintenanceOff/GitAutoMaintenanceOffEnv
// alone never reaches it. #711's review traced this directly: a bare
// origin.git configured only through the env still started `gc --auto` 34
// times from inside receive-pack, while every directly-started git
// subprocess (the clones) dropped to 0.
func ConfigureGitAutoMaintenanceOff(t testing.TB, repoPath string) {
	t.Helper()
	for index, key := range gitAutoMaintenanceKeys {
		cmd := exec.Command("git", "config", key, gitAutoMaintenanceValues[index])
		cmd.Dir = repoPath
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git -C %s config %s %s: %v: %s", repoPath, key, gitAutoMaintenanceValues[index], err, out)
		}
	}
}

// InitBareRemoteForTest creates a bare repository at path (with an initial
// branch of "main", creating path's parent directory as needed) and
// immediately disables gc.auto, maintenance.auto and receive.autogc directly
// in it via ConfigureGitAutoMaintenanceOff.
//
// #754's round-2 review found the same latent flake class -- a same-host
// `git push` can leave receive-pack's own detached `git gc --auto`/`git
// maintenance run --auto` still writing inside a bare fixture repo, racing
// t.TempDir()'s recursive removal of it at test end -- present in roughly
// fifteen other cmd/wb fixtures that created a bare remote directly and
// never called ConfigureGitAutoMaintenanceOff on it. This helper exists so
// every bare remote a wb test fixture creates gets that protection by
// construction, rather than depending on each new fixture remembering the
// call.
func InitBareRemoteForTest(t testing.TB, path string) string {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %s: %v", filepath.Dir(path), err)
	}
	cmd := exec.Command("git", "init", "--bare", "--initial-branch=main", path)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare --initial-branch=main %s: %v: %s", path, err, out)
	}
	ConfigureGitAutoMaintenanceOff(t, path)
	return path
}

// WriteExecutableFile re-exports execfile.WriteExecutableFile for the
// callers across this repository that already import testenv for other
// fixtures. See that package's doc comment for why the implementation
// lives there instead of here: internal/envguard (which this package
// itself imports, for Isolate) needs the same primitive in its own tests,
// and internal/envguard importing internal/testenv back would be a cycle.
func WriteExecutableFile(path string, content []byte, perm os.FileMode) error {
	return execfile.WriteExecutableFile(path, content, perm)
}

// userStateVariables are, with the home directory itself, the variables that
// select where WB and the tools it drives read a user's own configuration and
// state. Left ambient, a test binary run on a developer's machine reads that
// developer's real wb.yaml, which names the fleet's shared claim store, and
// resolves ~/projects as its projects root.
var userStateVariables = []string{"XDG_CONFIG_HOME", "XDG_STATE_HOME"}

// realStateSelectors are ambient WB variables that point a process at real
// state. They are removed outright: a test that wants one sets it itself.
var realStateSelectors = []string{"WB_HOME", "WB_PROJECTS_ROOT"}

// goToolVariables keep the Go toolchain's caches and settings where they were.
// They default to locations under the user's home, and a test that builds a
// binary must not rebuild the world because its home moved.
var goToolVariables = []string{"GOCACHE", "GOMODCACHE", "GOPATH", "GOENV"}

// UserStateRootEnv names the private root in the environment of every process
// an isolated test binary starts, so a re-executed test binary adopts its
// parent's isolation instead of building its own.
const UserStateRootEnv = "WB_TEST_USER_ROOT"

// userStateRootPrefix is the name every private root starts with.
const userStateRootPrefix = "wb-test-user-"

// defaultProjectsDirectory is the directory under the user's home that wb
// resolves as its projects root when nothing names one (wbhome's default).
const defaultProjectsDirectory = "projects"

// inheritedUserStateRoot is the private root a parent test process passed
// down, or empty when there is none to adopt: the variable is unset, or does
// not name a private root that still exists.
func inheritedUserStateRoot() string {
	root := os.Getenv(UserStateRootEnv)
	if !strings.HasPrefix(filepath.Base(root), userStateRootPrefix) {
		return ""
	}
	if info, err := os.Stat(filepath.Join(root, "home")); err != nil || !info.IsDir() {
		return ""
	}
	return root
}

var (
	userStateRoot string
	makeTempDir   = os.MkdirTemp
)

// IsolateUserState gives the whole test process a private, empty user: HOME,
// XDG_CONFIG_HOME and XDG_STATE_HOME point into one fresh temporary root, and
// the ambient WB variables that select real state are unset. Call it from
// TestMain before any test runs; the returned function removes the root.
//
// It exists because a stubbed command is not an isolated one: on 2026-10-02 a
// cmd/wb test that stubbed the cleanup engine still ran the command's
// post-apply claim release, which read the developer's real wb.yaml and
// released a real claim in the fleet's state repository.
//
// Only the packages whose TestMain calls this are isolated: cmd/wb,
// internal/hooks, internal/lifecyclehooks and internal/hostload. Not yet
// isolated, and still reading the developer's own home: internal/worktrees,
// internal/orchestrate and every other package with tests.
//
// Two things a test legitimately inherits survive the move. The Go toolchain's
// caches and settings are pinned to where they already were (derived from the
// environment, the GOENV file and Go's documented defaults, never by running
// the go command), and the private home's
// .gitconfig includes the developer's own global Git configuration, so Git
// keeps the identity and settings it had. That inclusion is deliberate and it
// is not neutral: the developer's credential helpers and url.insteadOf
// rewrites stay live, so a test that reaches a real remote does so with real
// credentials.
//
// The private home holds an empty projects directory, because a wb process
// given no --projects-root and no WB_PROJECTS_ROOT resolves, and opens,
// ~/projects.
//
// A process the test binary starts inherits all of this through its
// environment, and that includes the test binary re-executing itself as a
// helper: its TestMain calls IsolateUserState again, finds UserStateRootEnv
// naming a live private root, and adopts the environment it was given as it
// is. Isolating afresh there would discard what the parent test deliberately
// set for its children (a WB_PROJECTS_ROOT pointing at its fixture, say) and
// leave them resolving an empty home of their own. The parent owns the root,
// so an adopting process removes nothing.
func IsolateUserState() (remove func(), err error) {
	if inherited := inheritedUserStateRoot(); inherited != "" {
		userStateRoot = inherited
		return func() {}, nil
	}
	pinned := resolvedGoToolVariables()
	gitConfigs := globalGitConfigFiles()
	root, err := makeTempDir("", userStateRootPrefix)
	if err != nil {
		return nil, err
	}
	if resolved, resolveErr := filepath.EvalSymlinks(root); resolveErr == nil {
		root = resolved
	}
	home, config, state := filepath.Join(root, "home"), filepath.Join(root, "config"), filepath.Join(root, "state")
	include := ""
	for _, path := range gitConfigs {
		include += "[include]\n\tpath = " + strconv.Quote(path) + "\n"
	}
	if err := errors.Join(os.Mkdir(home, 0o700), os.Mkdir(config, 0o700), os.Mkdir(state, 0o700),
		os.Mkdir(filepath.Join(home, defaultProjectsDirectory), 0o700),
		os.WriteFile(filepath.Join(home, ".gitconfig"), []byte(include), 0o600)); err != nil {
		_ = os.RemoveAll(root)
		return nil, err
	}
	for name, value := range pinned {
		_ = os.Setenv(name, value)
	}
	for _, name := range realStateSelectors {
		_ = os.Unsetenv(name)
	}
	_ = os.Setenv("HOME", home)
	_ = os.Setenv("USERPROFILE", home)
	_ = os.Setenv("XDG_CONFIG_HOME", config)
	_ = os.Setenv("XDG_STATE_HOME", state)
	_ = os.Setenv(UserStateRootEnv, root)
	userStateRoot = root
	return func() { _ = os.RemoveAll(root) }, nil
}

// resolvedGoToolVariables works out where the Go toolchain keeps its caches
// and settings now, for each variable the environment does not already pin,
// the way the go command itself does and without running it: the process
// environment first, then the user's GOENV file, then the documented default
// (GOENV in the user configuration directory, GOPATH at ~/go, GOMODCACHE at
// pkg/mod under the first GOPATH entry, GOCACHE at go-build in the user cache
// directory). It must run before the home moves. A location that cannot be
// derived is left unpinned rather than guessed.
func resolvedGoToolVariables() map[string]string {
	home, _ := os.UserHomeDir()
	configDir, _ := os.UserConfigDir()
	cacheDir, _ := os.UserCacheDir()
	goEnv := os.Getenv("GOENV")
	if goEnv == "" {
		goEnv = under(configDir, "go", "env")
	}
	saved := goEnvFileValues(goEnv)
	setting := func(name, fallback string) string {
		for _, value := range []string{os.Getenv(name), saved[name]} {
			if value != "" {
				return value
			}
		}
		return fallback
	}
	goPath := setting("GOPATH", under(home, "go"))
	firstGoPath, _, _ := strings.Cut(goPath, string(os.PathListSeparator))
	resolved := map[string]string{
		"GOENV":      goEnv,
		"GOPATH":     goPath,
		"GOMODCACHE": setting("GOMODCACHE", under(firstGoPath, "pkg", "mod")),
		"GOCACHE":    setting("GOCACHE", under(cacheDir, "go-build")),
	}
	pinned := map[string]string{}
	for _, name := range goToolVariables {
		if value := resolved[name]; value != "" && os.Getenv(name) == "" {
			pinned[name] = value
		}
	}
	return pinned
}

// under joins elements below base, or is empty when there is no base to be
// under: a location that could not be resolved has no children.
func under(base string, elements ...string) string {
	if base == "" {
		return ""
	}
	return filepath.Join(append([]string{base}, elements...)...)
}

// goEnvFileValues reads the settings `go env -w` saved in a GOENV file, one
// NAME=value per line. A file that is missing or unreadable saves nothing.
func goEnvFileValues(path string) map[string]string {
	values := map[string]string{}
	content, _ := os.ReadFile(path)
	for _, line := range strings.Split(string(content), "\n") {
		if name, value, found := strings.Cut(strings.TrimSpace(line), "="); found {
			values[name] = value
		}
	}
	return values
}

// globalGitConfigFiles are the user-level Git configuration files that exist
// now, in the order Git reads them.
func globalGitConfigFiles() []string {
	home, _ := os.UserHomeDir()
	xdg := os.Getenv("XDG_CONFIG_HOME")
	if xdg == "" {
		xdg = filepath.Join(home, ".config")
	}
	var files []string
	for _, path := range []string{filepath.Join(xdg, "git", "config"), filepath.Join(home, ".gitconfig")} {
		if info, err := os.Stat(path); err == nil && info.Mode().IsRegular() {
			files = append(files, path)
		}
	}
	return files
}

// UserStateViolations lists every way the current process could still reach a
// real user's configuration or state. It is empty only after IsolateUserState
// and for as long as nothing has pointed the process back outside its root;
// extra are further resolved paths a package wants held to the same root.
func UserStateViolations(extra ...string) []string {
	if userStateRoot == "" {
		return []string{"IsolateUserState was not called for this test binary"}
	}
	var violations []string
	home, _ := os.UserHomeDir()
	paths := append([]string{home}, extra...)
	for _, name := range userStateVariables {
		paths = append(paths, os.Getenv(name))
	}
	for _, path := range paths {
		if relative, err := filepath.Rel(userStateRoot, path); err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			violations = append(violations, path+" is outside the test user root "+userStateRoot)
		}
	}
	for _, name := range realStateSelectors {
		if _, set := os.LookupEnv(name); set {
			violations = append(violations, name+" is set")
		}
	}
	return violations
}
