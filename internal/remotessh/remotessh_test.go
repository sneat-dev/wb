package remotessh

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	runner := ExecRunner{}
	stdout := NewLimitedBuffer(1024)
	stderr := NewLimitedBuffer(1024)
	// A portable command that echoes stdin and exits non-zero.
	if err := runner.Run(context.Background(), "/bin/sh", []string{"-c", "cat; echo boom >&2; exit 3"}, []byte("payload"), stdout, stderr); err == nil {
		t.Fatal("a non-zero exit must be reported")
	}
	if string(stdout.Bytes()) != "payload" {
		t.Fatalf("stdin was not delivered: %q", stdout.Bytes())
	}
	if !strings.Contains(string(stderr.Bytes()), "boom") {
		t.Fatalf("stderr was not captured: %q", stderr.Bytes())
	}
}

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
