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
// only HOME, USER, LOGNAME and SSH_AUTH_SOCK pass (and, on Windows, SystemRoot
// and USERPROFILE, which ssh.exe needs); PATH is the platform's system
// directories and the given directory; LANG is C.
func TestAllowedEnvironmentIsAnAllowListAndAFixedPath(t *testing.T) {
	t.Parallel()
	environ := []string{
		"HOME=/Users/alex", "USER=alex", "LOGNAME=alex", "SSH_AUTH_SOCK=/tmp/agent.sock", "PATH=/Users/alex/bin:/evil", "LANG=ru_RU.UTF-8", "LC_ALL=ru_RU.UTF-8",
		"DISPLAY=:0", "SSH_ASKPASS=/evil/askpass", "SSH_ASKPASS_REQUIRE=force", "GIT_SSH_COMMAND=evil", "WB_HOME=/x", "GITHUB_TOKEN=secret", "HOMEBREW=1", "USERNAME=x",
		`SYSTEMROOT=D:\Win`, `UserProfile=C:\Users\alex`, "home=/lower",
	}
	for name, test := range map[string]struct {
		goos      string
		environ   []string
		directory string
		want      []string
	}{
		"unix": {"darwin", environ, "/opt/local/bin",
			[]string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin:/opt/local/bin", "LANG=C", "HOME=/Users/alex", "USER=alex", "LOGNAME=alex", "SSH_AUTH_SOCK=/tmp/agent.sock"}},
		"the system's own directory is not added twice": {"linux", []string{"HOME=", "USER=alex"}, "/usr/bin", []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C", "USER=alex"}},
		"no directory and no environment":               {"linux", nil, "", []string{"PATH=/usr/bin:/bin:/usr/sbin:/sbin", "LANG=C"}},
		"windows": {"windows", environ, `C:\Tools\ssh`,
			[]string{`PATH=D:\Win\System32\OpenSSH;D:\Win\System32;C:\Tools\ssh`, "LANG=C", `SystemRoot=D:\Win`, `USERPROFILE=C:\Users\alex`, "HOME=/Users/alex", "USER=alex", "LOGNAME=alex", "SSH_AUTH_SOCK=/tmp/agent.sock"}},
		"windows with no SystemRoot": {"windows", []string{"Path=C:\\evil"}, `C:\Windows\System32\OpenSSH`,
			[]string{`PATH=C:\Windows\System32\OpenSSH;C:\Windows\System32`, "LANG=C"}},
	} {
		if got := allowedEnvironment(test.goos, test.environ, test.directory); !slices.Equal(got, test.want) {
			t.Errorf("%s: allowedEnvironment = %q, want %q", name, got, test.want)
		}
	}
}

// TestOnlyADirectoryNobodyButRootCanWriteToIsAddedToTheCommandsPath: the
// directory of the resolved ssh joins the command's PATH only when it is root's
// and neither its group nor everyone may write to it.
func TestOnlyADirectoryNobodyButRootCanWriteToIsAddedToTheCommandsPath(t *testing.T) {
	t.Parallel()
	facts := map[string]pathFacts{
		"/opt/root/bin":     {mode: os.ModeDir | 0o755, owner: 0},
		"/opt/user/bin":     {mode: os.ModeDir | 0o755, owner: 501},
		"/opt/writable/bin": {mode: os.ModeDir | 0o775, owner: 0},
		"/opt/world/bin":    {mode: os.ModeDir | 0o757, owner: 0},
	}
	inspect := func(path string) (pathFacts, error) {
		held, found := facts[path]
		if !found {
			return pathFacts{}, os.ErrNotExist
		}
		return held, nil
	}
	for executable, want := range map[string]string{
		"/opt/root/bin/ssh":     "/opt/root/bin",
		"/opt/user/bin/ssh":     "",
		"/opt/writable/bin/ssh": "",
		"/opt/world/bin/ssh":    "",
		"/opt/absent/bin/ssh":   "",
		"ssh":                   "",
	} {
		if got := pathDirectory(executable, "linux", inspect); got != want {
			t.Errorf("pathDirectory(%q) = %q, want %q", executable, got, want)
		}
	}
	// Windows has no owner or mode to read: the directory is added.
	if got := pathDirectory("/opt/user/bin/ssh", "windows", inspect); got != "/opt/user/bin" {
		t.Errorf("on Windows pathDirectory = %q", got)
	}
}

// TestAGroupRunnersCommandIsPreparedWithTheAllowedEnvironmentAndABoundedWait
// inspects the command without starting it.
func TestAGroupRunnersCommandIsPreparedWithTheAllowedEnvironmentAndABoundedWait(t *testing.T) {
	t.Parallel()
	const executable = "/nonexistent/bin/ssh"
	grouped := prepare(t.Context(), executable, []string{"-T"}, nil, io.Discard, io.Discard, true)
	if !slices.Equal(grouped.Env, commandEnvironment(executable)) || !slices.Contains(grouped.Env, "LANG=C") || len(grouped.Env) > 6 || grouped.WaitDelay != groupWaitDelay || !slices.Equal(grouped.Args, []string{executable, "-T"}) {
		t.Fatalf("the grouped command = %+v", grouped)
	}
	plain := prepare(t.Context(), executable, nil, nil, io.Discard, io.Discard, false)
	if plain.Env != nil || plain.WaitDelay != 0 || plain.SysProcAttr != nil {
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
// the system's ssh wins when it exists; otherwise the lookup's result is
// followed to the file itself and taken only when it and its directory are
// root's or the user's own and not writable by group or others, and it is not
// under the user's home.
func TestResolveTrustedPrefersTheSystemsSSHAndRefusesOneAnotherProcessCouldReplace(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("the modes these cases set do not exist on Windows")
	}
	place := func(directoryMode, fileMode os.FileMode) string {
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(root, "bin")
		if err := os.Mkdir(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(directory, "ssh")
		if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
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
	const user, stranger = 501, 777
	// owners says who owns a path; every other path is the user's own.
	owners := map[string]int{}
	inspect := func(path string) (pathFacts, error) {
		facts, err := inspectPath(path)
		facts.owner = user
		if owner, named := owners[path]; named {
			facts.owner = owner
		}
		return facts, err
	}
	resolve := func(lookedUp, preferred, home, goos string) (string, error) {
		return resolveTrusted(found(lookedUp), preferred, home, goos, user, inspect)
	}
	system, other, rooted := place(0o755, 0o755), place(0o755, 0o755), place(0o755, 0o755)
	owners[rooted], owners[filepath.Dir(rooted)] = 0, 0
	absent := filepath.Join(t.TempDir(), "absent")
	if got, err := resolve(other, system, "", "linux"); err != nil || got != system {
		t.Fatalf("with a system ssh = %q, %v", got, err)
	}
	if got, err := resolve(other, absent, "/Users/alex", "linux"); err != nil || got != other {
		t.Fatalf("with no system ssh = %q, %v", got, err)
	}
	if got, err := resolve(rooted, absent, "", "linux"); err != nil || got != rooted {
		t.Fatalf("an ssh of root's = %q, %v", got, err)
	}
	// A system path that is a directory, or not executable, is not the system's ssh.
	if got, err := resolve(other, filepath.Dir(system), "", "linux"); err != nil || got != other {
		t.Fatalf("with a directory at the system path = %q, %v", got, err)
	}
	if got, err := resolve(other, place(0o755, 0o644), "", "linux"); err != nil || got != other {
		t.Fatalf("with a non-executable at the system path = %q, %v", got, err)
	}
	// A link in a directory nobody else can write to, to a file in one everybody can.
	exposed := place(0o777, 0o755)
	link := filepath.Join(filepath.Dir(place(0o755, 0o755)), "ssh-link")
	if err := os.Symlink(exposed, link); err != nil {
		t.Fatal(err)
	}
	strangers, strangersDirectory := place(0o755, 0o755), place(0o755, 0o755)
	owners[strangers], owners[filepath.Dir(strangersDirectory)] = stranger, stranger
	homeLink := filepath.Join(t.TempDir(), "home")
	if err := os.Symlink(filepath.Dir(filepath.Dir(other)), homeLink); err != nil {
		t.Fatal(err)
	}
	unreadable := place(0o755, 0o755)
	owners[unreadable] = user
	failing := func(path string) (pathFacts, error) {
		if path == unreadable {
			return pathFacts{}, os.ErrPermission
		}
		return inspect(path)
	}
	for name, test := range map[string]struct {
		path, home, want string
	}{
		"a world-writable file":            {place(0o755, 0o757), "", "can be written"},
		"a group-writable file":            {place(0o755, 0o775), "", "can be written"},
		"a world-writable directory":       {exposed, "", "can be written"},
		"a group-writable directory":       {place(0o775, 0o755), "", "can be written"},
		"a link to a replaceable file":     {link, "", "can be written"},
		"a file of another user's":         {strangers, "", "not owned by root or this user"},
		"a directory of another user's":    {strangersDirectory, "", "not owned by root or this user"},
		"under the home directory":         {other, filepath.Dir(filepath.Dir(other)) + "/", "under the home directory"},
		"under a home directory by a link": {other, homeLink, "under the home directory"},
		"not found":                        {absent, "", "resolve ssh executable"},
	} {
		if got, err := resolve(test.path, absent, test.home, "linux"); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("%s: resolveTrusted = %q, %v; want an error mentioning %q", name, got, err, test.want)
		}
	}
	if got, err := resolveTrusted(found(unreadable), absent, "", "linux", user, failing); err == nil {
		t.Errorf("a file whose owner cannot be read = %q", got)
	}
	// Windows says neither owner nor mode: only the home rule is left.
	if got, err := resolve(strangers, absent, "", "windows"); err != nil || got != strangers {
		t.Errorf("on Windows = %q, %v", got, err)
	}
	// A home that does not exist is compared as it is written.
	if got, err := resolve(other, absent, filepath.Join(absent, "home"), "linux"); err != nil || got != other {
		t.Errorf("with a home that does not exist = %q, %v", got, err)
	}
	// The exported form is the same rule for this platform and this user.
	if got, err := ResolveTrusted(found(other), system, ""); err != nil || got != system {
		t.Fatalf("ResolveTrusted = %q, %v", got, err)
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
