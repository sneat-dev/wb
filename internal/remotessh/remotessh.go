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
	"path/filepath"
	"strings"
	"unicode"

	procrunner "github.com/sneat-dev/wb/internal/runner"
)

const (
	// ExecutableName is the local SSH client WB invokes.
	ExecutableName = "ssh"
	// ConnectTimeoutSeconds bounds connection establishment, so an unreachable
	// host fails in seconds rather than hanging a supervisor.
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

// Build returns the argv for one remote WB call. host and user must already
// have passed the caller's validation; remote is the fixed remote command line,
// whose first element is the remote wb executable.
func Build(host, user string, remote []string) []string {
	arguments := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", ConnectTimeoutSeconds),
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
type ExecRunner struct {
	// Runner is task-8's process seam (internal/runner.Runner), which needs
	// its RunWithInput operation: OpenSSH's request travels over stdin (see
	// the package doc), something none of Runner's other four operations
	// could carry. Nil resolves to the production runner.Runner
	// (resolveRunner); a unit test injects runnertest.Fake.
	Runner procrunner.Runner
}

// Run executes the command with the caller's context, so a caller-supplied
// deadline reaches the SSH process itself and not just its output readers.
// stdout/stderr are always internal/agents.remotessh.LimitedBuffer in this
// repository, whose Write never fails and whose truncation is
// order-independent, so capturing the child's full output through
// RunWithInput and writing it to the caller's writers in one shot each is
// behaviorally identical to exec.Cmd streaming into them directly.
func (e ExecRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	result, err := resolveRunner(e.Runner).RunWithInput(ctx, "", stdin, executable, args...)
	if _, writeErr := io.WriteString(stdout, result.Stdout); writeErr != nil && err == nil {
		err = writeErr
	}
	if _, writeErr := io.WriteString(stderr, result.Stderr); writeErr != nil && err == nil {
		err = writeErr
	}
	return err
}

// resolveRunner defaults r to the production runner.Runner when the caller
// left it unset.
func resolveRunner(r procrunner.Runner) procrunner.Runner {
	if r != nil {
		return r
	}
	return procrunner.New()
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
