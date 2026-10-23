package testenv

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/envguard"
)

func TestIsolateClearsAgentVarsAndPinsGoworkOff(t *testing.T) {
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("WB_AGENT_PID", "999")
	t.Setenv("WB_AGENT_RUNTIME", "claude-code")
	t.Setenv("WB_AGENT_MODEL", "claude-sonnet-5")
	t.Setenv("GOWORK", "/ambient/go.work")

	Isolate(t)

	for _, name := range []string{"WB_AGENT_ID", "WB_AGENT_PID", "WB_AGENT_RUNTIME", "WB_AGENT_MODEL"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s = %q after Isolate, want empty", name, value)
		}
	}
	if value := os.Getenv("GOWORK"); value != "off" {
		t.Fatalf("GOWORK = %q after Isolate, want %q", value, "off")
	}
}

func TestIsolateTrulyUnsetsAgentVarsAndRestoresAfterTest(t *testing.T) {
	// envguard.Inspect detects an agent var by key presence in the
	// environment slice, not by value -- a t.Setenv(name, "") that leaves
	// the key present with an empty value would still be detected. This
	// test proves the key is gone while the outer test runs, and that the
	// original value comes back once it completes.
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("WB_AGENT_PID", "999")

	t.Run("during", func(t *testing.T) {
		Isolate(t)
		inputs := envguard.Inspect(os.Environ())
		if len(inputs.AgentVars) != 0 {
			t.Fatalf("AgentVars = %v after Isolate, want none", inputs.AgentVars)
		}
		if _, present := os.LookupEnv("WB_AGENT_ID"); present {
			t.Fatal("WB_AGENT_ID key still present in os.Environ() after Isolate")
		}
	})

	if value := os.Getenv("WB_AGENT_ID"); value != "outer-session" {
		t.Fatalf("WB_AGENT_ID = %q after subtest completed, want restored %q", value, "outer-session")
	}
	if value := os.Getenv("WB_AGENT_PID"); value != "999" {
		t.Fatalf("WB_AGENT_PID = %q after subtest completed, want restored %q", value, "999")
	}
}

func TestIsolateLetsATestSetItsOwnAgentVarAfterward(t *testing.T) {
	t.Setenv("WB_AGENT_ID", "outer-session")
	Isolate(t)
	// A test that intentionally exercises agent-mode behavior sets its own
	// value after Isolate, and that is the one the code under test observes.
	t.Setenv("WB_AGENT_ID", "intentional-session")
	if value := os.Getenv("WB_AGENT_ID"); value != "intentional-session" {
		t.Fatalf("WB_AGENT_ID = %q, want the value set after Isolate", value)
	}
}

func TestIsolateProcessClearsAgentVarsAndPinsGoworkOff(t *testing.T) {
	// IsolateProcess uses os.Setenv/Unsetenv directly (it is meant for
	// TestMain, which has no *testing.T to restore through), so this test
	// restores the environment itself via t.Setenv/t.Cleanup rather than
	// relying on automatic restoration.
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("GOWORK", "/ambient/go.work")

	IsolateProcess()

	if value := os.Getenv("WB_AGENT_ID"); value != "" {
		t.Fatalf("WB_AGENT_ID = %q after IsolateProcess, want empty", value)
	}
	if value := os.Getenv("GOWORK"); value != "off" {
		t.Fatalf("GOWORK = %q after IsolateProcess, want %q", value, "off")
	}
}

func TestIsolateProcessResolvesSymlinkedTemporaryRoot(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, "physical-temp")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(root, "temp-alias")
	if err := os.Symlink(target, alias); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	t.Setenv("TMPDIR", alias)

	IsolateProcess()

	if got := os.TempDir(); got != target {
		t.Fatalf("temporary root = %q, want resolved physical directory %q", got, target)
	}
}

func TestGitAutoMaintenanceOffEnvAppendsToAnExistingSequence(t *testing.T) {
	t.Parallel()
	base := []string{
		"UNRELATED=kept",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=user.name",
		"GIT_CONFIG_VALUE_0=t",
	}
	env := GitAutoMaintenanceOffEnv(base)

	want := map[string]string{
		"UNRELATED":          "kept",
		"GIT_CONFIG_COUNT":   "4",
		"GIT_CONFIG_KEY_0":   "user.name",
		"GIT_CONFIG_VALUE_0": "t",
		"GIT_CONFIG_KEY_1":   "gc.auto",
		"GIT_CONFIG_VALUE_1": "0",
		"GIT_CONFIG_KEY_2":   "maintenance.auto",
		"GIT_CONFIG_VALUE_2": "false",
		"GIT_CONFIG_KEY_3":   "receive.autogc",
		"GIT_CONFIG_VALUE_3": "false",
	}
	got := map[string]string{}
	for _, entry := range env {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("entry %q has no '='", entry)
		}
		if _, exists := got[name]; exists {
			t.Fatalf("duplicate key %q in %v", name, env)
		}
		got[name] = value
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("%s = %q, want %q (env=%v)", name, got[name], value, env)
		}
	}
	if len(got) != len(want) {
		t.Fatalf("env = %v, want exactly %v", got, want)
	}
}

func TestGitAutoMaintenanceOffEnvStartsFreshWithNoExistingSequence(t *testing.T) {
	t.Parallel()
	env := GitAutoMaintenanceOffEnv([]string{"UNRELATED=kept"})
	want := map[string]string{
		"UNRELATED":          "kept",
		"GIT_CONFIG_COUNT":   "3",
		"GIT_CONFIG_KEY_0":   "gc.auto",
		"GIT_CONFIG_VALUE_0": "0",
		"GIT_CONFIG_KEY_1":   "maintenance.auto",
		"GIT_CONFIG_VALUE_1": "false",
		"GIT_CONFIG_KEY_2":   "receive.autogc",
		"GIT_CONFIG_VALUE_2": "false",
	}
	got := map[string]string{}
	for _, entry := range env {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			t.Fatalf("entry %q has no '='", entry)
		}
		got[name] = value
	}
	for name, value := range want {
		if got[name] != value {
			t.Fatalf("%s = %q, want %q (env=%v)", name, got[name], value, env)
		}
	}
}

// TestSetGitAutoMaintenanceOffExposesDisabledSettingsToGitConfig proves the
// env this sets actually reaches a real git subprocess: it asserts
// `git config --get` resolves each key from the process environment
// override to the exact disabled value, which is the documented mechanism
// GIT_CONFIG_COUNT/KEY_N/VALUE_N uses to make git behave as if the
// repository's own config held these entries. It does not itself observe a
// real gc/maintenance dispatch being skipped -- that is exercised in the
// packages this helper is used to fix, via GIT_TRACE2_EVENT child-start
// counts.
func TestSetGitAutoMaintenanceOffExposesDisabledSettingsToGitConfig(t *testing.T) {
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "-b", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, out)
	}

	SetGitAutoMaintenanceOff(t)

	for key, want := range map[string]string{
		"gc.auto":          "0",
		"maintenance.auto": "false",
		"receive.autogc":   "false",
	} {
		cmd := exec.Command("git", "config", "--get", key)
		cmd.Dir = repo
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git config --get %s: %v: %s", key, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("git config --get %s = %q, want %q", key, got, want)
		}
	}
}

func TestSetGitAutoMaintenanceOffExtendsAnAlreadySetSequence(t *testing.T) {
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "t")

	SetGitAutoMaintenanceOff(t)

	if got := os.Getenv("GIT_CONFIG_COUNT"); got != "4" {
		t.Fatalf("GIT_CONFIG_COUNT = %q, want %q", got, "4")
	}
	if got := os.Getenv("GIT_CONFIG_KEY_0"); got != "user.name" {
		t.Fatalf("GIT_CONFIG_KEY_0 = %q, want the pre-existing entry preserved", got)
	}
	if got := os.Getenv("GIT_CONFIG_KEY_1"); got != "gc.auto" {
		t.Fatalf("GIT_CONFIG_KEY_1 = %q, want %q", got, "gc.auto")
	}
}

// TestGitAutoMaintenanceOffProcessExtendsTheProcessSequence proves the
// TestMain variant appends the three disabled settings to whatever
// GIT_CONFIG_COUNT sequence the process already carries, and that a real git
// subprocess inheriting the process environment resolves them.
func TestGitAutoMaintenanceOffProcessExtendsTheProcessSequence(t *testing.T) {
	// Route every key this test (or the function under test) touches through
	// t.Setenv first so it is restored -- or unset again -- afterwards.
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "user.name")
	t.Setenv("GIT_CONFIG_VALUE_0", "t")
	for _, index := range []string{"1", "2", "3"} {
		t.Setenv("GIT_CONFIG_KEY_"+index, "")
		t.Setenv("GIT_CONFIG_VALUE_"+index, "")
	}

	GitAutoMaintenanceOffProcess()

	want := map[string]string{
		"GIT_CONFIG_COUNT":   "4",
		"GIT_CONFIG_KEY_0":   "user.name",
		"GIT_CONFIG_VALUE_0": "t",
		"GIT_CONFIG_KEY_1":   "gc.auto",
		"GIT_CONFIG_VALUE_1": "0",
		"GIT_CONFIG_KEY_2":   "maintenance.auto",
		"GIT_CONFIG_VALUE_2": "false",
		"GIT_CONFIG_KEY_3":   "receive.autogc",
		"GIT_CONFIG_VALUE_3": "false",
	}
	for name, value := range want {
		if got := os.Getenv(name); got != value {
			t.Fatalf("%s = %q, want %q", name, got, value)
		}
	}
	cmd := exec.Command("git", "config", "--get", "maintenance.auto")
	cmd.Dir = t.TempDir()
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git config --get maintenance.auto: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "false" {
		t.Fatalf("inherited maintenance.auto = %q, want %q", got, "false")
	}
}

func TestConfigureGitAutoMaintenanceOffWritesIntoTheReposOwnConfig(t *testing.T) {
	t.Parallel()
	repo := t.TempDir()
	cmd := exec.Command("git", "init", "-q", "--bare", "-b", "main")
	cmd.Dir = repo
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init --bare: %v: %s", err, out)
	}

	ConfigureGitAutoMaintenanceOff(t, repo)

	for key, want := range map[string]string{
		"gc.auto":          "0",
		"maintenance.auto": "false",
		"receive.autogc":   "false",
	} {
		// Read back from the repo's own config file directly, with no
		// GIT_CONFIG_* environment override in effect, proving the value
		// was actually written to disk rather than only visible through
		// this test process's own environment.
		cmd := exec.Command("git", "config", "--local", "--get", key)
		cmd.Dir = repo
		cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git config --local --get %s: %v: %s", key, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("git config --local --get %s = %q, want %q", key, got, want)
		}
	}
}

// recordingTB embeds a nil testing.TB so it satisfies the interface
// (testing.TB has an unexported method that only the testing package's own
// types can implement directly) while overriding only the two methods
// ConfigureGitAutoMaintenanceOff actually calls. Its Fatalf records the
// message and calls runtime.Goexit, exactly like the real *testing.T, so
// the function under test still stops running at the same point without
// tearing down the outer test that is asserting on it.
type recordingTB struct {
	testing.TB
	mu       sync.Mutex
	fatalMsg string
	fataled  bool
}

func (r *recordingTB) Helper() {}

func (r *recordingTB) Fatalf(format string, args ...any) {
	r.mu.Lock()
	r.fatalMsg = fmt.Sprintf(format, args...)
	r.fataled = true
	r.mu.Unlock()
	runtime.Goexit()
}

// TestConfigureGitAutoMaintenanceOffFailsLoudlyWhenGitConfigFails drives the
// error branch: repoPath does not exist, so every `git config` invocation
// inside it fails, and ConfigureGitAutoMaintenanceOff must report that
// failure through t.Fatalf rather than silently continuing.
func TestConfigureGitAutoMaintenanceOffFailsLoudlyWhenGitConfigFails(t *testing.T) {
	t.Parallel()
	missing := filepath.Join(t.TempDir(), "does-not-exist")
	recorder := &recordingTB{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		ConfigureGitAutoMaintenanceOff(recorder, missing)
	}()
	<-done

	recorder.mu.Lock()
	t.Cleanup(func() { recorder.mu.Unlock() })
	if !recorder.fataled {
		t.Fatal("ConfigureGitAutoMaintenanceOff against a missing directory did not fail")
	}
	if !strings.Contains(recorder.fatalMsg, "config gc.auto") {
		t.Fatalf("Fatalf message = %q, want it to name the failing git config call", recorder.fatalMsg)
	}
}

// TestWriteExecutableFileWritesAnExecutableFileAtTheGivenPath exercises this
// package's own WriteExecutableFile wrapper (not just the execfile package it
// re-exports): package-level coverage is measured per package, so a caller in
// a different package does not count toward this file's own statement
// coverage.
// TestInitBareRemoteForTestCreatesAConfiguredBareRepo drives
// InitBareRemoteForTest's success path: it must create path's parent
// directory, initialize a bare repository there with an initial branch of
// "main", and disable gc.auto/maintenance.auto/receive.autogc directly in
// that repository's own config -- the same protection
// ConfigureGitAutoMaintenanceOff gives on its own, but by construction, so a
// caller cannot forget it.
func TestInitBareRemoteForTestCreatesAConfiguredBareRepo(t *testing.T) {
	t.Parallel()
	remote := filepath.Join(t.TempDir(), "nested", "remote.git")

	got := InitBareRemoteForTest(t, remote)

	if got != remote {
		t.Fatalf("InitBareRemoteForTest returned %q, want %q", got, remote)
	}
	cmd := exec.Command("git", "rev-parse", "--is-bare-repository")
	cmd.Dir = remote
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git rev-parse --is-bare-repository: %v: %s", err, out)
	}
	if got := strings.TrimSpace(string(out)); got != "true" {
		t.Fatalf("is-bare-repository = %q, want true", got)
	}
	for key, want := range map[string]string{
		"gc.auto":          "0",
		"maintenance.auto": "false",
		"receive.autogc":   "false",
	} {
		cmd := exec.Command("git", "config", "--local", "--get", key)
		cmd.Dir = remote
		cmd.Env = append(os.Environ(), "GIT_CONFIG_COUNT=0")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git config --local --get %s: %v: %s", key, err, out)
		}
		if got := strings.TrimSpace(string(out)); got != want {
			t.Fatalf("git config --local --get %s = %q, want %q", key, got, want)
		}
	}
}

// TestInitBareRemoteForTestFailsLoudlyWhenParentCannotBeCreated drives
// InitBareRemoteForTest's MkdirAll error branch: path's parent collides with
// an existing regular file, so MkdirAll can never create it as a directory.
func TestInitBareRemoteForTestFailsLoudlyWhenParentCannotBeCreated(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	blocker := filepath.Join(root, "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(blocker, "remote.git")
	recorder := &recordingTB{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		InitBareRemoteForTest(recorder, path)
	}()
	<-done

	recorder.mu.Lock()
	t.Cleanup(func() { recorder.mu.Unlock() })
	if !recorder.fataled {
		t.Fatal("InitBareRemoteForTest with an unmakeable parent did not fail")
	}
	if !strings.Contains(recorder.fatalMsg, "mkdir") {
		t.Fatalf("Fatalf message = %q, want it to name the failing mkdir", recorder.fatalMsg)
	}
}

// TestInitBareRemoteForTestFailsLoudlyWhenGitInitFails drives
// InitBareRemoteForTest's `git init --bare` error branch: path already
// exists as a regular file, so git can never initialize a repository there.
func TestInitBareRemoteForTestFailsLoudlyWhenGitInitFails(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "remote.git")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	recorder := &recordingTB{}

	done := make(chan struct{})
	go func() {
		defer close(done)
		InitBareRemoteForTest(recorder, path)
	}()
	<-done

	recorder.mu.Lock()
	t.Cleanup(func() { recorder.mu.Unlock() })
	if !recorder.fataled {
		t.Fatal("InitBareRemoteForTest against a path that is already a file did not fail")
	}
	if !strings.Contains(recorder.fatalMsg, "git init --bare") {
		t.Fatalf("Fatalf message = %q, want it to name the failing git init", recorder.fatalMsg)
	}
}

func TestWriteExecutableFileWritesAnExecutableFileAtTheGivenPath(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "script")
	if err := WriteExecutableFile(path, []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatalf("WriteExecutableFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat: %v", err)
	}
	if info.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, want 0o755", info.Mode().Perm())
	}
	if err := exec.Command(path).Run(); err != nil {
		t.Fatalf("exec written file: %v", err)
	}
}
