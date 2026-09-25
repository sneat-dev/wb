package herdr

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	procrunner "github.com/sneat-dev/wb/internal/runner"
	procrunnertest "github.com/sneat-dev/wb/internal/runner/runnertest"
	"github.com/sneat-dev/wb/internal/testenv"
)

func TestResolveBinaryPrefersHERDRBinPath(t *testing.T) {
	binary := fakeHerdrBinaryPath(t)
	resolved, err := ResolveBinary(lookupFromMap(map[string]string{
		envBinPath: binary,
	}))
	if err != nil {
		t.Fatalf("ResolveBinary() error = %v", err)
	}
	if resolved != binary {
		t.Fatalf("ResolveBinary() = %q, want the configured HERDR_BIN_PATH %q", resolved, binary)
	}
}

func TestResolveBinaryStaleHERDRBinPathFallsBackToPATH(t *testing.T) {
	directory := t.TempDir()
	fakeHerdr := filepath.Join(directory, "herdr")
	if err := testenv.WriteExecutableFile(fakeHerdr, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	staleBinPath := filepath.Join(t.TempDir(), "no-longer-here")
	resolved, err := ResolveBinary(lookupFromMap(map[string]string{envBinPath: staleBinPath}))
	if err != nil {
		t.Fatalf("ResolveBinary() error = %v", err)
	}
	if resolved != fakeHerdr {
		t.Fatalf("ResolveBinary() = %q, want PATH fallback %q for a stale HERDR_BIN_PATH", resolved, fakeHerdr)
	}
}

func TestResolveBinaryStaleHERDRBinPathAndNoPATHFallback(t *testing.T) {
	directory := t.TempDir() // deliberately empty: no herdr binary on PATH
	t.Setenv("PATH", directory)

	staleBinPath := filepath.Join(t.TempDir(), "no-longer-here")
	_, err := ResolveBinary(lookupFromMap(map[string]string{envBinPath: staleBinPath}))
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("ResolveBinary() error = %v, want ErrBinaryNotFound", err)
	}
}

func TestResolveBinaryOtherStatErrorIsNotTreatedAsStale(t *testing.T) {
	original := statPath
	defer func() { statPath = original }()
	sentinel := errors.New("permission denied (simulated)")
	statPath = func(string) (os.FileInfo, error) { return nil, sentinel }

	_, err := ResolveBinary(lookupFromMap(map[string]string{envBinPath: "/configured/herdr"}))
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("ResolveBinary() error = %v, want ErrBinaryNotFound", err)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("ResolveBinary() error = %v, should not claim the path was merely absent", err)
	}
}

func TestIsStaleBinaryPath(t *testing.T) {
	if !isStaleBinaryPath(fs.ErrNotExist) {
		t.Fatal("isStaleBinaryPath(fs.ErrNotExist) = false, want true")
	}
	if isStaleBinaryPath(errors.New("boom")) {
		t.Fatal("isStaleBinaryPath(other) = true, want false")
	}
}

func TestResolveBinaryFallsBackToPATH(t *testing.T) {
	directory := t.TempDir()
	fakeHerdr := filepath.Join(directory, "herdr")
	if err := testenv.WriteExecutableFile(fakeHerdr, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	resolved, err := ResolveBinary(lookupFromMap(nil))
	if err != nil {
		t.Fatalf("ResolveBinary() error = %v", err)
	}
	if resolved != fakeHerdr {
		t.Fatalf("ResolveBinary() = %q, want %q", resolved, fakeHerdr)
	}
}

func TestResolveBinaryNotFound(t *testing.T) {
	directory := t.TempDir() // deliberately empty: no herdr binary on PATH
	t.Setenv("PATH", directory)

	_, err := ResolveBinary(lookupFromMap(nil))
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("ResolveBinary() error = %v, want ErrBinaryNotFound", err)
	}
}

func TestResolveBinaryNilLookup(t *testing.T) {
	_, err := ResolveBinary(nil)
	if !errors.Is(err, ErrBinaryNotFound) {
		t.Fatalf("ResolveBinary(nil) error = %v, want ErrBinaryNotFound", err)
	}
}

func TestResolveBinaryIgnoresEmptyHERDRBinPath(t *testing.T) {
	directory := t.TempDir()
	fakeHerdr := filepath.Join(directory, "herdr")
	if err := testenv.WriteExecutableFile(fakeHerdr, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)

	resolved, err := ResolveBinary(lookupFromMap(map[string]string{envBinPath: ""}))
	if err != nil {
		t.Fatalf("ResolveBinary() error = %v", err)
	}
	if resolved != fakeHerdr {
		t.Fatalf("ResolveBinary() = %q, want PATH fallback %q", resolved, fakeHerdr)
	}
}

// TestExecRunnerRunUsesAnInjectedProcessRunner covers execRunner.Run's argv
// pass-through directly against a scripted runner.Runner. It replaces a
// former real-process test of the same property: no-shell argv fidelity is
// now internal/runner.Real's own contract (proven by that package's
// helper-process tests), and TestFakeHERDRBinaryArgvQuotingEndToEnd in
// fake_binary_test.go already proves the same "no shell" property end to
// end through a real process at the Client level, so a third real-process
// test at this narrower layer would be pure duplication -- exactly what
// the founder's "tests do not work around real processes" mandate argues
// against. This also covers the resolveRunner
// "r != nil" branch directly: a scripted runner.Runner (task-8's seam,
// distinct from this package's own fakeRunner Runner) answers instead of
// the production runner.Runner.
func TestExecRunnerRunUsesAnInjectedProcessRunner(t *testing.T) {
	t.Parallel()
	// Shell-metacharacter-laden args, exactly as the real-process test this
	// replaces used: execRunner.Run must forward each one to the Runner
	// unchanged, as a single argv element -- never joined, split, or
	// otherwise reinterpreted along the way.
	args := []string{"one", "two three", "$(danger)"}
	env := []string{"X=1"}

	fake := procrunnertest.New(t)
	fake.Expect(func(c procrunnertest.Call) bool {
		return c.Name == "herdr" && equalStrings(c.Args, args) && equalStrings(c.Opts.Env, env)
	}, procrunner.Result{Stdout: "herdr 1.0"}, nil)

	runner := execRunner{Runner: fake}
	stdout, _, err := runner.Run(context.Background(), "herdr", args, env)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(stdout) != "herdr 1.0" {
		t.Fatalf("stdout = %q, want %q", stdout, "herdr 1.0")
	}
}

func TestBuildEnvNotTargetedReturnsNilEvenWithSocketPath(t *testing.T) {
	// targeted=false always means "inherit ambient unchanged", regardless
	// of what socketPath holds — callers only ever pass a non-empty
	// socketPath alongside targeted=true (Client.targeted() guarantees
	// this), but buildEnv itself must not depend on that.
	if got := buildEnv([]string{"PATH=/bin", envSocketPath + "=/ambient.sock"}, false, "/configured.sock"); got != nil {
		t.Fatalf("buildEnv(targeted=false) = %#v, want nil", got)
	}
}

func TestBuildEnvOverridesExistingEntry(t *testing.T) {
	got := buildEnv([]string{"PATH=/bin", envSocketPath + "=/ambient.sock", "OTHER=1"}, true, "/configured.sock")
	wantContains := envSocketPath + "=/configured.sock"
	found, stale := false, false
	for _, entry := range got {
		if entry == wantContains {
			found = true
		}
		if entry == envSocketPath+"=/ambient.sock" {
			stale = true
		}
	}
	if !found {
		t.Fatalf("buildEnv() = %#v, missing %q", got, wantContains)
	}
	if stale {
		t.Fatalf("buildEnv() = %#v, still contains the ambient socket path", got)
	}
	if len(got) != 3 { // PATH, OTHER, and the configured socket path
		t.Fatalf("buildEnv() = %#v, want 3 entries", got)
	}
}

func TestBuildEnvAppendsWhenAbsent(t *testing.T) {
	got := buildEnv([]string{"PATH=/bin"}, true, "/configured.sock")
	want := []string{"PATH=/bin", envSocketPath + "=/configured.sock"}
	if !equalStrings(got, want) {
		t.Fatalf("buildEnv() = %#v, want %#v", got, want)
	}
}

func TestBuildEnvTargetedWithoutSocketPathOmitsSocketVarEntirely(t *testing.T) {
	// A Client targeted only via WithSessionName (no WithSocketPath): the
	// ambient HERDR_SOCKET_PATH is stripped, and nothing replaces it, so
	// herdr's own built-in default socket applies rather than an
	// unrelated ambient one.
	got := buildEnv([]string{"PATH=/bin", envSocketPath + "=/ambient.sock"}, true, "")
	want := []string{"PATH=/bin"}
	if !equalStrings(got, want) {
		t.Fatalf("buildEnv(targeted, no socketPath) = %#v, want %#v", got, want)
	}
}

func TestBuildEnvTargetedStripsAmbientIdentityVars(t *testing.T) {
	ambient := []string{
		"PATH=/bin",
		envPaneID + "=w1:p2",
		envTabID + "=w1:t2",
		envWorkspaceID + "=w1",
		envSession + "=some-session",
		envClientSocketPath + "=/ambient-client.sock",
		envSocketPath + "=/ambient.sock",
	}
	got := buildEnv(ambient, true, "/configured.sock")
	want := []string{"PATH=/bin", envSocketPath + "=/configured.sock"}
	if !equalStrings(got, want) {
		t.Fatalf("buildEnv() = %#v, want every ambient identity var stripped, got %#v", got, want)
	}
}

func TestWithSessionFlagEmptyReturnsArgsUnchanged(t *testing.T) {
	args := []string{"pane", "current"}
	got := withSessionFlag("", args)
	if !equalStrings(got, args) {
		t.Fatalf("withSessionFlag(\"\") = %#v, want unchanged %#v", got, args)
	}
}

func TestWithSessionFlagPrepends(t *testing.T) {
	got := withSessionFlag("reviewer-session", []string{"pane", "current"})
	want := []string{"--session", "reviewer-session", "pane", "current"}
	if !equalStrings(got, want) {
		t.Fatalf("withSessionFlag() = %#v, want %#v", got, want)
	}
}

func TestIsExecNotFound(t *testing.T) {
	if !isExecNotFound(exec.ErrNotFound) {
		t.Fatal("isExecNotFound(exec.ErrNotFound) = false, want true")
	}
	if isExecNotFound(errors.New("boom")) {
		t.Fatal("isExecNotFound(other) = true, want false")
	}
}

func splitNulTerminated(data []byte) []string {
	var result []string
	start := 0
	for i, b := range data {
		if b == 0 {
			result = append(result, string(data[start:i]))
			start = i + 1
		}
	}
	return result
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
