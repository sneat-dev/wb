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
	"strings"
	"time"
	"unicode"
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

// run is the one place this package starts a process. With group it runs the
// command in a process group of its own, which the end of ctx kills whole, and
// bounds the wait for its output pipes.
func run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer, group bool) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	if group {
		command.WaitDelay = groupWaitDelay
		ownGroup(command)
	}
	return command.Run()
}

// groupWaitDelay is how long GroupRunner waits, after it has killed the
// process group, for the output pipes to close before it closes them itself.
const groupWaitDelay = 2 * time.Second

// GroupRunner is the runner for a background caller, such as a daemon. It runs
// the command in a process group of its own and, when ctx ends, kills the whole
// group (ssh and any helper it started, a ProxyCommand say), not ssh alone. Run
// returns only after the process has been waited for, so no call leaves a
// zombie, and a helper that survives with the output pipes open cannot hold Run
// for longer than groupWaitDelay. On Windows there is no process group to
// signal, and the process itself is killed.
type GroupRunner struct{}

// Run executes the command as ExecRunner does, in its own process group.
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

// Exceeded reports whether output past the limit was discarded.
func (b *LimitedBuffer) Exceeded() bool { return b.exceeded }

// SanitizeDiagnostic renders remote stderr as one bounded, control-character-free
// line safe to include in a local error message.
func SanitizeDiagnostic(raw []byte, truncated bool) string {
	diagnostic := strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
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
