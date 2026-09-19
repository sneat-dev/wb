package herdr

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestResolveBinaryPrefersHERDRBinPath(t *testing.T) {
	resolved, err := ResolveBinary(lookupFromMap(map[string]string{
		envBinPath: "/fake/path/to/herdr",
	}))
	if err != nil {
		t.Fatalf("ResolveBinary() error = %v", err)
	}
	if resolved != "/fake/path/to/herdr" {
		t.Fatalf("ResolveBinary() = %q, want the configured HERDR_BIN_PATH", resolved)
	}
}

func TestResolveBinaryFallsBackToPATH(t *testing.T) {
	directory := t.TempDir()
	fakeHerdr := filepath.Join(directory, "herdr")
	if err := os.WriteFile(fakeHerdr, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
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
	if err := os.WriteFile(fakeHerdr, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
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

func TestExecRunnerRunsArgvOnly(t *testing.T) {
	directory := t.TempDir()
	argvFile := filepath.Join(directory, "argv")
	script := "#!/bin/sh\n: > \"" + argvFile + "\"\nfor a in \"$@\"; do printf '%s\\0' \"$a\" >> \"" + argvFile + "\"; done\nprintf 'ok'\n"
	scriptPath := filepath.Join(directory, "recorder")
	if err := os.WriteFile(scriptPath, []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}

	var runner execRunner
	stdout, stderr, err := runner.Run(context.Background(), scriptPath, []string{"one", "two three", "$(danger)"}, nil)
	if err != nil {
		t.Fatalf("Run() error = %v, stderr = %s", err, stderr)
	}
	if string(stdout) != "ok" {
		t.Fatalf("Run() stdout = %q, want %q", stdout, "ok")
	}

	recorded, err := os.ReadFile(argvFile)
	if err != nil {
		t.Fatal(err)
	}
	got := splitNulTerminated(recorded)
	want := []string{"one", "two three", "$(danger)"}
	if !equalStrings(got, want) {
		t.Fatalf("recorded argv = %#v, want %#v", got, want)
	}
}

func TestBuildEnvEmptySocketPathReturnsNil(t *testing.T) {
	if got := buildEnv([]string{"PATH=/bin", envSocketPath + "=/ambient.sock"}, ""); got != nil {
		t.Fatalf("buildEnv(empty socketPath) = %#v, want nil", got)
	}
}

func TestBuildEnvOverridesExistingEntry(t *testing.T) {
	got := buildEnv([]string{"PATH=/bin", envSocketPath + "=/ambient.sock", "OTHER=1"}, "/configured.sock")
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
	got := buildEnv([]string{"PATH=/bin"}, "/configured.sock")
	want := []string{"PATH=/bin", envSocketPath + "=/configured.sock"}
	if !equalStrings(got, want) {
		t.Fatalf("buildEnv() = %#v, want %#v", got, want)
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
