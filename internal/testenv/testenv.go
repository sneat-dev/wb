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

// WriteExecutableFile re-exports execfile.WriteExecutableFile for the
// callers across this repository that already import testenv for other
// fixtures. See that package's doc comment for why the implementation
// lives there instead of here: internal/envguard (which this package
// itself imports, for Isolate) needs the same primitive in its own tests,
// and internal/envguard importing internal/testenv back would be a cycle.
func WriteExecutableFile(path string, content []byte, perm os.FileMode) error {
	return execfile.WriteExecutableFile(path, content, perm)
}
