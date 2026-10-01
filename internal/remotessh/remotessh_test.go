package remotessh

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
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

func TestBuildWithSetsTheConnectTimeoutAndNeutralisesTheUsersConfiguration(t *testing.T) {
	t.Parallel()
	got := BuildWith(Options{ConnectTimeoutSeconds: 5, NoForwarding: true, Unattended: true}, "vm.example", "alex", []string{"/usr/local/bin/wb", "cockpit", "export"})
	want := []string{
		"-T", "-o", "BatchMode=yes", "-o", "ConnectTimeout=5",
		"-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes",
		"-o", "ControlMaster=no", "-o", "RemoteCommand=none", "-o", "PermitLocalCommand=no", "-o", "LogLevel=ERROR",
		"-l", "alex", "--", "vm.example", "/usr/local/bin/wb", "cockpit", "export",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("BuildWith = %#v, want %#v", got, want)
	}
	for _, argument := range got {
		// Host key checking is the user's configuration's, and an existing master
		// is still reached through the configured ControlPath.
		if strings.Contains(argument, "StrictHostKeyChecking") || strings.Contains(argument, "UserKnownHostsFile") || strings.Contains(argument, "ControlPath") {
			t.Fatalf("BuildWith sets %q", argument)
		}
	}
	if got := BuildWith(Options{ConnectTimeoutSeconds: -1}, "vm.example", "", []string{"wb"}); !slices.Equal(got, Build("vm.example", "", []string{"wb"})) {
		t.Fatalf("BuildWith with no timeout = %#v, want the default options", got)
	}
}

// TestAllowedEnvironmentIsAnAllowListAndAFixedPath: of the caller's environment
// only HOME, USER, LOGNAME and SSH_AUTH_SOCK pass; PATH is the system
// directories and the executable's own; LANG is C.
func TestAllowedEnvironmentIsAnAllowListAndAFixedPath(t *testing.T) {
	t.Parallel()
	environ := []string{
		"HOME=/Users/alex", "USER=alex", "LOGNAME=alex", "SSH_AUTH_SOCK=/tmp/agent.sock", "PATH=/Users/alex/bin:/evil", "LANG=ru_RU.UTF-8", "LC_ALL=ru_RU.UTF-8",
		"DISPLAY=:0", "SSH_ASKPASS=/evil/askpass", "SSH_ASKPASS_REQUIRE=force", "GIT_SSH_COMMAND=evil", "WB_HOME=/x", "GITHUB_TOKEN=secret", "HOMEBREW=1", "USERNAME=x",
	}
	got := AllowedEnvironment(environ, "/opt/homebrew/bin/ssh")
	want := []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin" + string(os.PathListSeparator) + "/opt/homebrew/bin", "LANG=C", "HOME=/Users/alex", "USER=alex", "LOGNAME=alex", "SSH_AUTH_SOCK=/tmp/agent.sock"}
	if !slices.Equal(got, want) {
		t.Fatalf("AllowedEnvironment = %q, want %q", got, want)
	}
	// The system's own ssh adds no directory, an unset or empty variable is not
	// passed, and a relative executable adds nothing.
	if got, want := AllowedEnvironment([]string{"HOME=", "USER=alex"}, "/usr/bin/ssh"), []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "USER=alex"}; !slices.Equal(got, want) {
		t.Fatalf("AllowedEnvironment = %q, want %q", got, want)
	}
	if got := AllowedEnvironment(nil, "ssh"); !slices.Equal(got, []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}) {
		t.Fatalf("AllowedEnvironment of nothing = %q", got)
	}
}

// TestAGroupRunnersCommandIsPreparedWithTheAllowedEnvironmentAndABoundedWait
// inspects the command without starting it.
func TestAGroupRunnersCommandIsPreparedWithTheAllowedEnvironmentAndABoundedWait(t *testing.T) {
	t.Parallel()
	const executable = "/nonexistent/bin/ssh"
	grouped := prepare(t.Context(), executable, []string{"-T"}, nil, io.Discard, io.Discard, true)
	if !slices.Equal(grouped.Env, AllowedEnvironment(os.Environ(), executable)) || !slices.Contains(grouped.Env, "LANG=C") || grouped.WaitDelay != groupWaitDelay || !slices.Equal(grouped.Args, []string{executable, "-T"}) {
		t.Fatalf("the grouped command = %+v", grouped)
	}
	plain := prepare(t.Context(), executable, nil, nil, io.Discard, io.Discard, false)
	if plain.Env != nil || plain.WaitDelay != 0 || plain.Cancel == nil && plain.SysProcAttr != nil {
		t.Fatalf("the plain command = %+v", plain)
	}
	// Neither runner can start a program that does not exist.
	if err := (GroupRunner{}).Run(t.Context(), executable, nil, nil, io.Discard, io.Discard); err == nil {
		t.Fatal("a missing executable must be reported")
	}
}

func TestTailBufferKeepsTheEndOfWhatWasWritten(t *testing.T) {
	t.Parallel()
	buffer := NewTailBuffer(8)
	if written, err := buffer.Write([]byte("abc")); written != 3 || err != nil || buffer.Discarded() || string(buffer.Bytes()) != "abc" {
		t.Fatalf("a short write = %d, %v, %q", written, err, buffer.Bytes())
	}
	if written, err := buffer.Write([]byte("a long banner, then: denied")); written != 27 || err != nil {
		t.Fatalf("Write = %d, %v; a bounded buffer must never fail the writer", written, err)
	}
	if string(buffer.Bytes()) != ": denied" || !buffer.Discarded() {
		t.Fatalf("kept %q", buffer.Bytes())
	}
}

// TestResolveTrustedPrefersTheSystemsSSHAndRefusesOneAnotherProcessCouldReplace:
// the system's ssh wins when it exists; otherwise the lookup's result is taken
// only when neither it nor its directory is writable by group or others and it
// is not under the user's home.
func TestResolveTrustedPrefersTheSystemsSSHAndRefusesOneAnotherProcessCouldReplace(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("Windows has no mode bits to check")
	}
	place := func(directoryMode, fileMode os.FileMode) string {
		directory := filepath.Join(t.TempDir(), "bin")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "ssh")
		if err := os.WriteFile(path, []byte("x"), 0o755); err != nil {
			t.Fatal(err)
		}
		// The modes are set after creation, so the process's umask does not decide them.
		if err := os.Chmod(path, fileMode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(directory, directoryMode); err != nil {
			t.Fatal(err)
		}
		return path
	}
	found := func(path string) func(string) (string, error) {
		return func(string) (string, error) { return path, nil }
	}
	system, other := place(0o755, 0o755), place(0o755, 0o755)
	absent := filepath.Join(t.TempDir(), "absent")
	if got, err := ResolveTrusted(found(other), system, ""); err != nil || got != system {
		t.Fatalf("with a system ssh = %q, %v", got, err)
	}
	if got, err := ResolveTrusted(found(other), absent, "/Users/alex"); err != nil || got != other {
		t.Fatalf("with no system ssh = %q, %v", got, err)
	}
	// A system path that is a directory, or not executable, is not the system's ssh.
	if got, err := ResolveTrusted(found(other), filepath.Dir(system), ""); err != nil || got != other {
		t.Fatalf("with a directory at the system path = %q, %v", got, err)
	}
	if got, err := ResolveTrusted(found(other), place(0o755, 0o644), ""); err != nil || got != other {
		t.Fatalf("with a non-executable at the system path = %q, %v", got, err)
	}
	for name, test := range map[string]struct {
		path, home, want string
	}{
		"a world-writable file":      {place(0o755, 0o757), "", "can be written"},
		"a group-writable file":      {place(0o755, 0o775), "", "can be written"},
		"a world-writable directory": {place(0o777, 0o755), "", "can be written"},
		"a group-writable directory": {place(0o775, 0o755), "", "can be written"},
		"under the home directory":   {other, filepath.Dir(filepath.Dir(other)) + "/", "under the home directory"},
		"not found":                  {absent, "", "resolve ssh executable"},
	} {
		if got, err := ResolveTrusted(found(test.path), absent, test.home); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: ResolveTrusted = %q, %v; want an error mentioning %q", name, got, err, test.want)
		}
	}
	if SystemExecutable != "/usr/bin/ssh" {
		t.Fatalf("SystemExecutable = %q", SystemExecutable)
	}
}

func TestSanitizeDiagnosticLeavesOnlyPrintableCharacters(t *testing.T) {
	t.Parallel()
	hostile := "ok\x1b[31m red\u202e reversed\u200b zero\u2028 line\u2029 para\ufeff bom\x00 nul\xff bad \u00e9"
	got := SanitizeDiagnostic([]byte(hostile), false)
	if got != "ok [31m red reversed zero line para bom nul bad \u00e9" {
		t.Fatalf("SanitizeDiagnostic = %q", got)
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
