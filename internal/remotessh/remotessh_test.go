package remotessh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/runner/runnertest"
)

// testExecutable is a real, absolute, executable regular file, which is what
// Resolve demands of anything it returns.
func testExecutable(t *testing.T) string {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	return executable
}

func TestBuildProducesAFixedArgvShape(t *testing.T) {
	t.Parallel()
	got := Build("hetzner-vm1", "", []string{"wb", "--internal"})
	want := []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "--", "hetzner-vm1", "wb", "--internal"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Build = %#v, want %#v", got, want)
	}
	got = Build("178.104.41.143", "ai", []string{"wb", "--internal"})
	want = []string{"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=10", "-l", "ai", "--", "178.104.41.143", "wb", "--internal"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("Build with user = %#v, want %#v", got, want)
	}
}

func TestResolveRefusesAnUnusableSSHExecutable(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	missing := filepath.Join(directory, "absent")

	cases := map[string]struct {
		lookPath func(string) (string, error)
		want     string
	}{
		"no lookup":   {nil, "lookup is unavailable"},
		"not found":   {func(string) (string, error) { return "", errors.New("not found") }, "resolve ssh executable"},
		"relative":    {func(string) (string, error) { return "ssh", nil }, "clean absolute path"},
		"unclean":     {func(string) (string, error) { return "/usr/../usr/bin/ssh", nil }, "clean absolute path"},
		"absent":      {func(string) (string, error) { return missing, nil }, "resolve ssh executable"},
		"not regular": {func(string) (string, error) { return directory, nil }, "not a regular executable file"},
		"notexecutable": {func(string) (string, error) {
			path := filepath.Join(directory, "not-executable")
			if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
			return path, nil
		}, "not a regular executable file"},
	}
	for name, testCase := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := Resolve(testCase.lookPath); err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("Resolve error = %v, want it to mention %q", err, testCase.want)
			}
		})
	}

	resolved, err := Resolve(func(string) (string, error) { return testExecutable(t), nil })
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved != testExecutable(t) {
		t.Fatalf("Resolve = %q", resolved)
	}
}

func TestExecRunnerCapturesOutputAndErrors(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.Expect(func(c runnertest.Call) bool {
		return c.Name == "/bin/sh" && string(c.Input) == "payload"
	}, runner.Result{Stdout: "payload", Stderr: "boom\n", ExitCode: 3}, errors.New("exit status 3"))
	execRunner := ExecRunner{Runner: fake}
	stdout := NewLimitedBuffer(1024)
	stderr := NewLimitedBuffer(1024)
	if err := execRunner.Run(context.Background(), "/bin/sh", []string{"-c", "cat; echo boom >&2; exit 3"}, []byte("payload"), stdout, stderr); err == nil {
		t.Fatal("a non-zero exit must be reported")
	}
	if string(stdout.Bytes()) != "payload" {
		t.Fatalf("stdin was not delivered: %q", stdout.Bytes())
	}
	if !strings.Contains(string(stderr.Bytes()), "boom") {
		t.Fatalf("stderr was not captured: %q", stderr.Bytes())
	}
}

// TestExecRunnerDefaultsToProductionRunner proves ExecRunner's zero value
// (as internal/agents wires it: remotessh.ExecRunner{}) resolves a nil
// Runner to the production runner.Runner rather than doing nothing. Under
// `go test`, runner.Real refuses to start a real process (task-24's guard),
// so this observes the guard error instead of shelling out to ssh.
func TestExecRunnerDefaultsToProductionRunner(t *testing.T) {
	t.Parallel()
	execRunner := ExecRunner{}
	stdout := NewLimitedBuffer(1024)
	stderr := NewLimitedBuffer(1024)
	err := execRunner.Run(context.Background(), "ssh", []string{"host"}, nil, stdout, stderr)
	if !errors.Is(err, runner.ErrRealProcessBlocked) {
		t.Fatalf("ExecRunner{} with no injected runner = %v, want it to reach the production runner.Runner", err)
	}
}

// TestExecRunnerSurfacesAWriteFailureWhenTheCommandItselfSucceeded proves
// the rare case where copying captured output into the caller's writer
// fails: the writer's own error must not be swallowed just because the
// child exited zero.
func TestExecRunnerSurfacesAWriteFailureWhenTheCommandItselfSucceeded(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ssh", "host"}, runner.Result{Stdout: "ok"}, nil)
	execRunner := ExecRunner{Runner: fake}
	failing := failingWriter{err: errors.New("disk full")}
	err := execRunner.Run(context.Background(), "ssh", []string{"host"}, nil, failing, NewLimitedBuffer(1024))
	if err == nil || !strings.Contains(err.Error(), "disk full") {
		t.Fatalf("Run() err = %v, want the writer's own failure", err)
	}
}

// TestExecRunnerSurfacesAStderrWriteFailure covers the second, independent
// write-error branch: a stdout writer that works fine must not hide a
// stderr writer's own failure.
func TestExecRunnerSurfacesAStderrWriteFailure(t *testing.T) {
	t.Parallel()
	fake := runnertest.New(t)
	fake.ExpectArgv([]string{"ssh", "host"}, runner.Result{Stderr: "warn"}, nil)
	execRunner := ExecRunner{Runner: fake}
	failing := failingWriter{err: errors.New("pipe closed")}
	err := execRunner.Run(context.Background(), "ssh", []string{"host"}, nil, NewLimitedBuffer(1024), failing)
	if err == nil || !strings.Contains(err.Error(), "pipe closed") {
		t.Fatalf("Run() err = %v, want the stderr writer's own failure", err)
	}
}

// failingWriter always reports err, so a test can exercise the write-error
// branch no real caller's LimitedBuffer ever reaches.
type failingWriter struct{ err error }

func (w failingWriter) Write([]byte) (int, error) { return 0, w.err }

func TestLimitedBufferBoundsAndReportsOverflow(t *testing.T) {
	t.Parallel()
	buffer := NewLimitedBuffer(4)
	written, err := buffer.Write([]byte("abcdef"))
	if err != nil || written != 6 {
		t.Fatalf("Write = %d, %v; a bounded buffer must never fail the writer", written, err)
	}
	if string(buffer.Bytes()) != "abcd" {
		t.Fatalf("retained = %q", buffer.Bytes())
	}
	if !buffer.Exceeded() {
		t.Fatal("overflow must be reported")
	}
	// Writes past the limit keep reporting success so the child is not blocked.
	if n, err := buffer.Write([]byte("more")); err != nil || n != 4 {
		t.Fatalf("write past the limit = %d, %v", n, err)
	}
	if buffer.Exceeded() != true || string(buffer.Bytes()) != "abcd" {
		t.Fatalf("buffer state after overflow = %q", buffer.Bytes())
	}

	small := NewLimitedBuffer(64)
	if _, err := small.Write([]byte("fits")); err != nil {
		t.Fatal(err)
	}
	if small.Exceeded() {
		t.Fatal("a write inside the limit must not report overflow")
	}
}

func TestSanitizeDiagnosticIsOneBoundedLine(t *testing.T) {
	t.Parallel()
	got := SanitizeDiagnostic([]byte("line one\nline two\ttabbed"), false)
	if got != "line one line two tabbed" {
		t.Fatalf("SanitizeDiagnostic = %q", got)
	}
	if got := SanitizeDiagnostic([]byte("  \n\t "), false); got != "" {
		t.Fatalf("whitespace-only diagnostic = %q", got)
	}
	long := strings.Repeat("x", MaxDiagnosticBytes*3)
	got = SanitizeDiagnostic([]byte(long), false)
	if len(got) > MaxDiagnosticBytes || !strings.HasSuffix(got, "...") {
		t.Fatalf("long diagnostic was not bounded: %d bytes", len(got))
	}
	// An explicitly truncated diagnostic is marked even when it is short.
	if got := SanitizeDiagnostic([]byte("short"), true); !strings.HasSuffix(got, "...") {
		t.Fatalf("truncated diagnostic = %q", got)
	}
	// The truncation must not split a UTF-8 sequence.
	multibyte := strings.Repeat("é", MaxDiagnosticBytes)
	got = SanitizeDiagnostic([]byte(multibyte), false)
	if !strings.HasSuffix(got, "...") {
		t.Fatal("expected a truncation marker")
	}
	if strings.ContainsRune(got, '\uFFFD') {
		t.Fatal("truncation split a UTF-8 sequence")
	}
	// Control bytes never reach the caller as control bytes.
	if got := SanitizeDiagnostic([]byte("a\x00b\x1bc"), false); strings.ContainsAny(got, "\x00\x1b") {
		t.Fatalf("control characters survived: %q", got)
	}
}
