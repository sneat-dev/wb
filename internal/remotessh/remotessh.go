// Package remotessh is WB's single remote-call boundary: it builds the OpenSSH
// invocation, resolves the local ssh executable, and bounds what a remote
// response can make WB hold in memory.
//
// Every element of a built argument list is either fixed or comes from
// validated WB configuration. Caller data never becomes part of the remote
// command, because OpenSSH joins the remote arguments into one string that the
// remote login shell then parses; the only safe channel for a request is
// standard input, which the remote WB entry point reads and validates exactly
// as it validates its own command line.
package remotessh

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

const (
	// ExecutableName is the local SSH client WB invokes.
	ExecutableName = "ssh"
	// ConnectTimeoutSeconds is the default bound of connection establishment, so
	// an unreachable host fails in seconds rather than hanging a supervisor.
	ConnectTimeoutSeconds = 10
	// DefaultWBCommand is the remote command name used when a target configures
	// no exact path.
	DefaultWBCommand = "wb"
	// MaxDiagnosticBytes bounds the remote stderr WB is willing to quote back.
	MaxDiagnosticBytes = 1024
)

// Resolve looks up and validates the local ssh executable. A result that is not
// an absolute, clean, regular, executable file is refused rather than handed to
// exec, so a PATH entry cannot substitute something else.
func Resolve(lookPath func(string) (string, error)) (string, error) {
	if lookPath == nil {
		return "", fmt.Errorf("resolve ssh executable: executable lookup is unavailable")
	}
	executable, err := lookPath(ExecutableName)
	if err != nil {
		return "", fmt.Errorf("resolve ssh executable: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return "", fmt.Errorf("resolve ssh executable: %q is not a clean absolute path", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return "", fmt.Errorf("resolve ssh executable %s: %w", executable, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return "", fmt.Errorf("resolve ssh executable: %q is not a regular executable file", executable)
	}
	return executable, nil
}

// Options are the fixed OpenSSH options of one call. None of them is caller
// data: they are constants of the calling package.
type Options struct {
	// ConnectTimeoutSeconds bounds connection establishment; zero or less means
	// ConnectTimeoutSeconds, the package's default.
	ConnectTimeoutSeconds int
	// NoForwarding refuses agent and X11 forwarding and every port forward the
	// user's ssh configuration names for the host, whatever that configuration
	// says: a background call carries no credential to the remote and opens no
	// listener.
	NoForwarding bool
	// Unattended is for a call no person watches, such as a daemon's. It
	// neutralises the directives of the user's ssh configuration that would
	// change what such a call does: the call never becomes a connection-sharing
	// master (ControlMaster=no; a master that already exists is still used
	// through the configured ControlPath), so killing it at its timeout cannot
	// drop a session of the user's that shares it; a RemoteCommand configured for
	// the host does not replace the remote words (RemoteCommand=none); a
	// LocalCommand is not run on this machine (PermitLocalCommand=no); and ssh
	// writes only errors to stderr, not banners (LogLevel=ERROR).
	Unattended bool
}

// SystemExecutable is where the operating system's own ssh client is on the
// platforms that ship one.
const SystemExecutable = "/usr/bin/ssh"

// pathFacts is what the trust rule reads of a file or directory: its mode and
// the user that owns it (-1 where the platform does not say).
type pathFacts struct {
	mode  os.FileMode
	owner int
}

// replaceable reports whether a process other than root's or the user's own
// could replace what facts describes: it is owned by someone else, or its group
// or everyone may write to it. Windows has neither fact, and nothing there is
// reported.
func (facts pathFacts) replaceable(goos string, user int) bool {
	if goos == "windows" {
		return false
	}
	return (facts.owner != 0 && facts.owner != user) || facts.mode.Perm()&0o022 != 0
}

// ResolveTrusted finds the ssh a background caller runs without anyone watching.
// It is the system's own (preferred, normally SystemExecutable) when that is a
// regular executable file. Otherwise it is what lookPath finds, held to
// Resolve's rule, followed through every symbolic link to the file itself, and
// held to two more rules: the file and its directory are owned by root or by
// this user and are not writable by their group or by everyone, and the file is
// not under home (the user's home directory; empty means unknown and is not
// checked). So a program that a less trusted process could have put on the PATH
// is never run in the user's name. Windows has no such owner or mode bits and
// no system path; there the result is held to Resolve's rule and the home rule.
func ResolveTrusted(lookPath func(string) (string, error), preferred, home string) (string, error) {
	return resolveTrusted(lookPath, preferred, home, runtime.GOOS, os.Getuid(), inspectPath)
}

// resolveTrusted is ResolveTrusted for the platform goos and the user with the
// id user, reading files' owners and modes through inspect.
func resolveTrusted(lookPath func(string) (string, error), preferred, home, goos string, user int, inspect func(string) (pathFacts, error)) (string, error) {
	if info, err := os.Stat(preferred); err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0o111 != 0 {
		return preferred, nil
	}
	found, err := Resolve(lookPath)
	if err != nil {
		return "", err
	}
	// Resolve has found the file, so its links resolve.
	executable, _ := filepath.EvalSymlinks(found)
	if home != "" {
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			home = resolved
		}
		if strings.HasPrefix(executable, filepath.Clean(home)+string(filepath.Separator)) {
			return "", fmt.Errorf("resolve ssh executable: %q is under the home directory", executable)
		}
	}
	for _, path := range []string{executable, filepath.Dir(executable)} {
		if facts, err := inspect(path); err != nil || facts.replaceable(goos, user) {
			return "", fmt.Errorf("resolve ssh executable: %q is not owned by root or this user, or can be written by its group or by everyone", path)
		}
	}
	return executable, nil
}

// Build returns the argv for one remote WB call with the default options. host
// and user must already have passed the caller's validation; remote is the fixed
// remote command line, whose first element is the remote wb executable.
func Build(host, user string, remote []string) []string {
	return BuildWith(Options{}, host, user, remote)
}

// BuildWith is Build with the given options. The call never has a terminal
// (-T) and never prompts (BatchMode=yes). Host key checking is left to the
// user's ssh configuration and is never switched off here. The user is passed
// as a fixed `-l` pair and the host after `--`, so neither can be read as an
// option.
func BuildWith(options Options, host, user string, remote []string) []string {
	timeout := options.ConnectTimeoutSeconds
	if timeout <= 0 {
		timeout = ConnectTimeoutSeconds
	}
	arguments := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", timeout),
	}
	if options.NoForwarding {
		arguments = append(arguments, "-o", "ForwardAgent=no", "-o", "ForwardX11=no", "-o", "ClearAllForwardings=yes")
	}
	if options.Unattended {
		arguments = append(arguments, "-o", "ControlMaster=no", "-o", "RemoteCommand=none", "-o", "PermitLocalCommand=no", "-o", "LogLevel=ERROR")
	}
	if user != "" {
		arguments = append(arguments, "-l", user)
	}
	arguments = append(arguments, "--", host)
	return append(arguments, remote...)
}

// Runner runs one local command with exact stdin bytes and captured output. It
// is an interface so the SSH boundary can be exercised without a real host.
type Runner interface {
	Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error
}

// ExecRunner is the production runner.
type ExecRunner struct{}

// Run executes the command with the caller's context, so a caller-supplied
// deadline reaches the SSH process itself and not just its output readers.
func (ExecRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	return run(ctx, executable, args, stdin, stdout, stderr, false)
}

// run is the one place this package starts a process.
func run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer, group bool) error {
	return prepare(ctx, executable, args, stdin, stdout, stderr, group).Run()
}

// prepare is the command run starts. With group it is given the allow-listed
// environment, runs in a session and process group of its own, which the end of ctx
// kills whole, and has a bounded wait for its output pipes.
func prepare(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer, group bool) *exec.Cmd {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	if group {
		command.Env = commandEnvironment(executable)
		command.WaitDelay = groupWaitDelay
		ownGroup(command)
	}
	return command
}

// groupWaitDelay is how long GroupRunner waits, after it has killed the
// process group, for the output pipes to close before it closes them itself.
const groupWaitDelay = 2 * time.Second

// GroupRunner is the runner for a background caller, such as a daemon. It runs
// the command in a process group of its own and, when ctx ends, kills the whole
// group (ssh and any helper it started, a ProxyCommand say), not ssh alone. Run
// returns only after the process has been waited for, so no call leaves a
// zombie, and a helper that survives with the output pipes open cannot hold Run
// for longer than groupWaitDelay. The command is given an allow-listed
// environment (allowedEnvironment), not the caller's, and a session of its own,
// so that neither ssh nor anything it starts has a terminal to prompt on. On Windows there is no process group to signal:
// only the process itself (ssh.exe) is killed, and a helper it started (a
// ProxyCommand, say) may outlive it.
type GroupRunner struct{}

// safePath is the PATH a GroupRunner's command is given, before the directory
// of the executable itself.
const safePath = "/usr/bin:/bin:/usr/sbin:/sbin"

// passedEnvironment is the variables a GroupRunner's command inherits, and
// passedOnWindows the ones it inherits there besides: ssh.exe needs them to
// start and to find the user's ssh configuration.
var (
	passedEnvironment = []string{"HOME", "USER", "LOGNAME", "SSH_AUTH_SOCK"}
	passedOnWindows   = []string{"SystemRoot", "USERPROFILE"}
)

// commandEnvironment is the environment a GroupRunner gives executable.
func commandEnvironment(executable string) []string {
	return allowedEnvironment(runtime.GOOS, os.Environ(), pathDirectory(executable, runtime.GOOS, inspectPath))
}

// pathDirectory is the directory of executable when it may be added to the
// command's PATH, else "": a directory the user (or anyone but root) can write
// to is not added, so nothing placed there is found by name by ssh or by a
// command ssh starts. Windows has no such facts, and its directory is added.
func pathDirectory(executable, goos string, inspect func(string) (pathFacts, error)) string {
	directory := filepath.Dir(executable)
	if !filepath.IsAbs(executable) {
		return ""
	}
	if goos == "windows" {
		return directory
	}
	if facts, err := inspect(directory); err != nil || facts.replaceable(goos, 0) {
		return ""
	}
	return directory
}

// allowedEnvironment is the environment a GroupRunner gives its command on the
// platform goos, made from environ (the caller's, as os.Environ returns it) and
// directory (pathDirectory's answer). It is an allow-list: HOME, USER, LOGNAME
// and SSH_AUTH_SOCK as they are, when set; a fixed PATH; and LANG=C, so that
// what ssh says is not translated. Everything else is dropped: DISPLAY and
// SSH_ASKPASS (nothing may prompt), GIT_* and WB_* variables, tokens and
// whatever else the daemon was started with.
//
// The PATH is the system directories and directory. On Windows it is the
// system's OpenSSH directory, System32 and directory, under the caller's
// SystemRoot, and SystemRoot and USERPROFILE are passed too; names are matched
// there without regard to case, as Windows does.
func allowedEnvironment(goos string, environ []string, directory string) []string {
	windows := goos == "windows"
	value := func(name string) (string, bool) {
		for _, entry := range environ {
			key, held, _ := strings.Cut(entry, "=")
			if held != "" && (key == name || (windows && strings.EqualFold(key, name))) {
				return held, true
			}
		}
		return "", false
	}
	separator, directories := ":", strings.Split(safePath, ":")
	passed := passedEnvironment
	if windows {
		root, found := value("SystemRoot")
		if !found {
			root = `C:\Windows`
		}
		separator, directories = ";", []string{root + `\System32\OpenSSH`, root + `\System32`}
		passed = slices.Concat(passedOnWindows, passedEnvironment)
	}
	if directory != "" && !slices.Contains(directories, directory) {
		directories = append(directories, directory)
	}
	allowed := []string{"PATH=" + strings.Join(directories, separator), "LANG=C"}
	for _, name := range passed {
		if held, found := value(name); found {
			allowed = append(allowed, name+"="+held)
		}
	}
	return allowed
}

// Run executes the command as ExecRunner does, in its own session and group.
func (GroupRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	return run(ctx, executable, args, stdin, stdout, stderr, true)
}

// LimitedBuffer accumulates output up to a byte limit and records whether the
// limit was reached, so a remote response can never make WB hold unbounded
// memory and an over-limit response is reported rather than truncated silently.
type LimitedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

// NewLimitedBuffer returns a buffer that accepts up to limit bytes and reports
// every write as successful, so the writer is never blocked or failed by the
// bound itself.
func NewLimitedBuffer(limit int) *LimitedBuffer {
	return &LimitedBuffer{limit: limit}
}

func (b *LimitedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := b.limit - b.buffer.Len()
	if remaining <= 0 {
		b.exceeded = b.exceeded || len(value) > 0
		return written, nil
	}
	if len(value) > remaining {
		_, _ = b.buffer.Write(value[:remaining])
		b.exceeded = true
		return written, nil
	}
	_, _ = b.buffer.Write(value)
	return written, nil
}

// Bytes returns what was retained.
func (b *LimitedBuffer) Bytes() []byte { return b.buffer.Bytes() }

// TailBuffer keeps the last limit bytes written to it and records whether
// anything before them was discarded. It is for output whose end matters: a
// long banner cannot push the line that says why a call failed out of it. Like
// LimitedBuffer it reports every write as successful.
type TailBuffer struct {
	kept      []byte
	limit     int
	discarded bool
}

// NewTailBuffer returns a buffer that keeps the last limit bytes.
func NewTailBuffer(limit int) *TailBuffer { return &TailBuffer{limit: limit} }

func (b *TailBuffer) Write(value []byte) (int, error) {
	b.kept = append(b.kept, value...)
	if over := len(b.kept) - b.limit; over > 0 {
		b.kept = append(b.kept[:0], b.kept[over:]...)
		b.discarded = true
	}
	return len(value), nil
}

// Bytes returns the bytes that were kept.
func (b *TailBuffer) Bytes() []byte { return b.kept }

// Discarded reports whether earlier output was dropped.
func (b *TailBuffer) Discarded() bool { return b.discarded }

// Exceeded reports whether output past the limit was discarded.
func (b *LimitedBuffer) Exceeded() bool { return b.exceeded }

// SanitizeDiagnostic renders remote stderr as one bounded line safe to include
// in a local error message or log: every character that is not printable (a
// control, a format character such as a bidirectional override or a zero-width
// space, a line or paragraph separator, an invalid byte) becomes a space.
func SanitizeDiagnostic(raw []byte, truncated bool) string {
	diagnostic := strings.Map(func(value rune) rune {
		if !unicode.IsPrint(value) || value == utf8.RuneError {
			return ' '
		}
		return value
	}, string(raw))
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	if len(diagnostic) > MaxDiagnosticBytes {
		truncated = true
	}
	if truncated {
		diagnostic = truncateUTF8(diagnostic, MaxDiagnosticBytes-len("..."))
		diagnostic = strings.TrimSpace(diagnostic) + "..."
	}
	return diagnostic
}

func truncateUTF8(value string, maxBytes int) string {
	if len(value) <= maxBytes {
		return value
	}
	for maxBytes > 0 && !isUTF8LeadingByte(value[maxBytes]) {
		maxBytes--
	}
	return value[:maxBytes]
}

func isUTF8LeadingByte(value byte) bool { return value&0xc0 != 0x80 }
