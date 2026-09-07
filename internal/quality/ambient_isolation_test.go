package quality

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

// These tests exercise the ambient-state isolation described in
// internal/envguard: real evidence from 2026-09-07 is that a stray go.work
// above TMPDIR silently flipped a temp Go module into workspace mode, and
// WB_AGENT_* variables exported by the operating agent were inherited by a
// validation subprocess and changed its verdict. Every subprocess this
// package launches for a Go check now goes through commandEnv (verify.go),
// which is what these tests pin down.
//
// TestMain in testmain_test.go pins GOWORK=off and clears every WB_AGENT_*
// variable for the whole test binary, so any test that needs to observe the
// *absence* of that ambient state first clears it back with t.Setenv.

// fakeGoDumpingEnv installs a fake `go` on PATH that writes its complete
// environment to envLog and exits 0. It stands in for the real `go` binary so
// these tests assert exactly what commandEnv put in the subprocess
// environment, without depending on real Go toolchain/workspace semantics.
func fakeGoDumpingEnv(t *testing.T, root, envLog string, exitCode int, stderr string) {
	t.Helper()
	bin := filepath.Join(root, "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\nenv > \"" + envLog + "\"\n"
	if stderr != "" {
		script += "echo '" + stderr + "' >&2\n"
	}
	script += "exit " + strconv.Itoa(exitCode) + "\n"
	writeQualityFile(t, filepath.Join(bin, "go"), script)
	if err := os.Chmod(filepath.Join(bin, "go"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// TestGoCheckSubprocessGetsGoworkOffDespiteAncestorGoWork covers required
// behaviour (a): a Go check run against a temp module while a go.work exists
// in a parent of the effective TMPDIR still passes, because commandEnv forces
// GOWORK=off into the go subprocess regardless of where the ambient go.work
// physically sits.
func TestGoCheckSubprocessGetsGoworkOffDespiteAncestorGoWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	root := t.TempDir()
	t.Setenv("GOWORK", "") // undo TestMain's process-wide pin for this test

	ancestor := filepath.Join(root, "ancestor")
	tmp := filepath.Join(ancestor, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	writeQualityFile(t, filepath.Join(ancestor, "go.work"), "go 1.26\n")
	t.Setenv("TMPDIR", tmp)

	repository := filepath.Join(root, "repo")
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/app\n\ngo 1.26\n")

	envLog := filepath.Join(root, "go-env.log")
	fakeGoDumpingEnv(t, root, envLog, 0, "")

	report := Verify(context.Background(), "example/app", repository, []Check{CheckBuild})
	if report.Status != StatusPassed {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(contents), "\nGOWORK=off\n") && !strings.HasPrefix(string(contents), "GOWORK=off\n") {
		t.Fatalf("go subprocess env did not carry GOWORK=off despite an ancestor go.work above TMPDIR:\n%s", contents)
	}
}

// TestGoCheckKeepsWorkspaceModeWhenRepositoryTracksOwnGoWork covers required
// behaviour (b): a repository that tracks its own go.work in HEAD keeps
// workspace mode -- commandEnv must not force GOWORK=off for it.
func TestGoCheckKeepsWorkspaceModeWhenRepositoryTracksOwnGoWork(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not available")
	}
	root := t.TempDir()
	t.Setenv("GOWORK", "")
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	repository := filepath.Join(root, "repo")
	writeQualityFile(t, filepath.Join(repository, "go.work"), "go 1.26\n\nuse ./backend\n")
	writeQualityFile(t, filepath.Join(repository, "backend", "go.mod"), "module example.test/backend\n\ngo 1.26\n")
	runQualityGit(t, repository, "init")
	runQualityGit(t, repository, "config", "user.email", "test@example.com")
	runQualityGit(t, repository, "config", "user.name", "Test")
	runQualityGit(t, repository, "add", ".")
	runQualityGit(t, repository, "commit", "-m", "initial")

	envLog := filepath.Join(root, "go-env.log")
	fakeGoDumpingEnv(t, root, envLog, 0, "")

	report := Verify(context.Background(), "example/workspace", repository, []Check{CheckBuild})
	if report.Status != StatusPassed {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(contents), "GOWORK=off") {
		t.Fatalf("go subprocess env forced GOWORK=off for a repository that tracks its own go.work:\n%s", contents)
	}
}

// TestGoCheckSubprocessDoesNotInheritAgentIdentityVars covers required
// behaviour (c): WB_AGENT_* variables set in the parent (operating-agent)
// environment must not reach the check subprocess.
func TestGoCheckSubprocessDoesNotInheritAgentIdentityVars(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	root := t.TempDir()
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WB_AGENT_ID", "session-abc123")
	t.Setenv("WB_AGENT_PID", "4242")
	t.Setenv("WB_AGENT_RUNTIME", "claude-code")
	t.Setenv("WB_AGENT_MODEL", "claude-sonnet-5")

	repository := filepath.Join(root, "repo")
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/agentvars\n\ngo 1.26\n")

	envLog := filepath.Join(root, "go-env.log")
	fakeGoDumpingEnv(t, root, envLog, 0, "")

	report := Verify(context.Background(), "example/agentvars", repository, []Check{CheckBuild})
	if report.Status != StatusPassed {
		t.Fatalf("report = %+v", report)
	}
	contents, err := os.ReadFile(envLog)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"WB_AGENT_ID", "WB_AGENT_PID", "WB_AGENT_RUNTIME", "WB_AGENT_MODEL"} {
		if strings.Contains(string(contents), name+"=") {
			t.Fatalf("go subprocess env inherited %s from the operating agent:\n%s", name, contents)
		}
	}
}

// TestFailedCheckDetailCarriesAmbientInputsBlock and
// TestFailedCheckDetailOmitsAmbientInputsBlockWhenNoneObserved cover required
// behaviour (d): the ambient-inputs block appears in a failed check's detail
// exactly when ambient state was actually observed.
func TestFailedCheckDetailCarriesAmbientInputsBlock(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	root := t.TempDir()
	ancestor := filepath.Join(root, "ancestor")
	tmp := filepath.Join(ancestor, "tmp")
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		t.Fatal(err)
	}
	writeQualityFile(t, filepath.Join(ancestor, "go.work"), "go 1.26\n")
	t.Setenv("TMPDIR", tmp)
	t.Setenv("WB_AGENT_ID", "session-xyz")

	repository := filepath.Join(root, "repo")
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/failing\n\ngo 1.26\n")

	envLog := filepath.Join(root, "go-env.log")
	fakeGoDumpingEnv(t, root, envLog, 1, "boom")

	report := Verify(context.Background(), "example/failing", repository, []Check{CheckBuild})
	if report.Status != StatusFailed || len(report.Results) != 1 {
		t.Fatalf("report = %+v", report)
	}
	detail := report.Results[0].Detail
	if !strings.Contains(detail, "ambient inputs:") {
		t.Fatalf("detail = %q, want an ambient inputs block", detail)
	}
	if !strings.Contains(detail, "go.work") {
		t.Fatalf("detail = %q, want the ancestor go.work named", detail)
	}
	if !strings.Contains(detail, "WB_AGENT_ID") {
		t.Fatalf("detail = %q, want WB_AGENT_ID named", detail)
	}
}

func TestFailedCheckDetailOmitsAmbientInputsBlockWhenNoneObserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test shell helper is POSIX-only")
	}
	root := t.TempDir()
	t.Setenv("GOWORK", "")
	t.Setenv("TMPDIR", filepath.Join(root, "tmp"))
	if err := os.MkdirAll(filepath.Join(root, "tmp"), 0o755); err != nil {
		t.Fatal(err)
	}

	repository := filepath.Join(root, "repo")
	writeQualityFile(t, filepath.Join(repository, "go.mod"), "module example.test/clean\n\ngo 1.26\n")

	envLog := filepath.Join(root, "go-env.log")
	fakeGoDumpingEnv(t, root, envLog, 1, "boom")

	report := Verify(context.Background(), "example/clean", repository, []Check{CheckBuild})
	if report.Status != StatusFailed || len(report.Results) != 1 {
		t.Fatalf("report = %+v", report)
	}
	detail := report.Results[0].Detail
	if strings.Contains(detail, "ambient inputs:") {
		t.Fatalf("detail = %q, want no ambient inputs block when none was observed", detail)
	}
}

func runQualityGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", dir}, args...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
