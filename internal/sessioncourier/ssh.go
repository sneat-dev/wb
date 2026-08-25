// Package sessioncourier delivers WB-owned session handoff protocol values.
// A courier transports exact request bytes and validates the target response;
// it does not own handoff state or source custody.
package sessioncourier

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

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionreceive"
)

const (
	sshExecutableName      = "ssh"
	defaultRemoteWBCommand = "wb"
	sshConnectTimeout      = 10
	sshDeliveryTimeout     = 2 * time.Minute
	maxSSHRequestBytes     = 1 << 20
	maxSSHStdoutBytes      = 2 << 20
	maxSSHStderrBytes      = 64 << 10
	maxSSHDiagnosticBytes  = 1024
)

// Deliverer is the fixed transport boundary shared by SSH and future courier
// adapters. Implementations must preserve request bytes exactly.
type Deliverer interface {
	Deliver(context.Context, []byte) (sessionreceive.Result, error)
}

type commandRunner interface {
	Run(context.Context, string, []string, []byte, io.Writer, io.Writer) error
}

type execCommandRunner struct{}

func (execCommandRunner) Run(ctx context.Context, executable string, args []string, stdin []byte, stdout, stderr io.Writer) error {
	command := exec.CommandContext(ctx, executable, args...)
	command.Stdin = bytes.NewReader(stdin)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

type sshDeliverer struct {
	config     sessionmove.SSHConfig
	executable string
	runner     commandRunner
}

// NewSSHDeliverer validates the remote address and resolves the local SSH
// executable. Callers can therefore construct the adapter before creating a
// source checkpoint, leaving mutation until all fixed delivery inputs exist.
func NewSSHDeliverer(config sessionmove.SSHConfig) (Deliverer, error) {
	return newSSHDeliverer(config, exec.LookPath, execCommandRunner{})
}

func newSSHDeliverer(config sessionmove.SSHConfig, lookPath func(string) (string, error), runner commandRunner) (*sshDeliverer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	if lookPath == nil {
		return nil, fmt.Errorf("resolve ssh executable: executable lookup is unavailable")
	}
	executable, err := lookPath(sshExecutableName)
	if err != nil {
		return nil, fmt.Errorf("resolve ssh executable: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, fmt.Errorf("resolve ssh executable: %q is not a clean absolute path", executable)
	}
	info, err := os.Stat(executable)
	if err != nil {
		return nil, fmt.Errorf("resolve ssh executable %s: %w", executable, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("resolve ssh executable: %q is not a regular executable file", executable)
	}
	if runner == nil {
		return nil, fmt.Errorf("run ssh courier: command runner is unavailable")
	}
	return &sshDeliverer{config: config, executable: executable, runner: runner}, nil
}

// DeliverSSH is the simple production entry point for one SSH delivery.
func DeliverSSH(ctx context.Context, config sessionmove.SSHConfig, raw []byte) (sessionreceive.Result, error) {
	deliverer, err := NewSSHDeliverer(config)
	if err != nil {
		return sessionreceive.Result{}, err
	}
	return deliverer.Deliver(ctx, raw)
}

func (d *sshDeliverer) Deliver(ctx context.Context, raw []byte) (sessionreceive.Result, error) {
	var result sessionreceive.Result
	request, err := validateReceiverRequest(raw, maxSSHRequestBytes)
	if err != nil {
		return result, fmt.Errorf("validate ssh session request: %w", err)
	}

	remoteWB := d.config.WBPath
	if remoteWB == "" {
		remoteWB = defaultRemoteWBCommand
	}
	args := []string{
		"-T",
		"-o", "BatchMode=yes",
		"-o", fmt.Sprintf("ConnectTimeout=%d", sshConnectTimeout),
		"--",
		d.config.Host,
		remoteWB, "--non-interactive", "session", "receive", "--format", "json",
	}
	var stdout, stderr boundedBuffer
	stdout.limit = maxSSHStdoutBytes
	stderr.limit = maxSSHStderrBytes
	deliveryContext, cancel := context.WithTimeout(ctx, sshDeliveryTimeout)
	defer cancel()
	if err := d.runner.Run(deliveryContext, d.executable, args, raw, &stdout, &stderr); err != nil {
		if contextErr := deliveryContext.Err(); contextErr != nil {
			return result, fmt.Errorf("ssh session delivery to %s: %w", d.config.Host, contextErr)
		}
		diagnostic := sanitizeDiagnostic(stderr.Bytes(), stderr.exceeded)
		if diagnostic == "" {
			return result, fmt.Errorf("ssh session delivery to %s: %w", d.config.Host, err)
		}
		return result, fmt.Errorf("ssh session delivery to %s: %w: %s", d.config.Host, err, diagnostic)
	}
	if stdout.exceeded {
		return result, fmt.Errorf("ssh response from %s exceeds %d bytes", d.config.Host, maxSSHStdoutBytes)
	}
	result, err = decodeReceiverResult(stdout.Bytes())
	if err != nil {
		return sessionreceive.Result{}, fmt.Errorf("validate ssh response from %s: %w", d.config.Host, err)
	}
	if err := validateReceiverResult(result, request, raw); err != nil {
		return sessionreceive.Result{}, fmt.Errorf("validate ssh response from %s: %w", d.config.Host, err)
	}
	return result, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (b *boundedBuffer) Write(value []byte) (int, error) {
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

func (b *boundedBuffer) Bytes() []byte { return b.buffer.Bytes() }

func sanitizeDiagnostic(raw []byte, truncated bool) string {
	diagnostic := strings.Map(func(value rune) rune {
		if unicode.IsControl(value) {
			return ' '
		}
		return value
	}, string(raw))
	diagnostic = strings.Join(strings.Fields(diagnostic), " ")
	if len(diagnostic) > maxSSHDiagnosticBytes {
		truncated = true
	}
	if truncated {
		diagnostic = truncateUTF8(diagnostic, maxSSHDiagnosticBytes-len("..."))
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
