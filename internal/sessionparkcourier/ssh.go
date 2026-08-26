// Package sessionparkcourier transports canonical parked-session envelopes.
// It owns only the fixed courier boundary: source admission/finalization stays
// in sessionpark.Store and target reconstruction/liveness stays in
// sessionparkreceive.
package sessionparkcourier

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
)

const (
	sshExecutableName      = "ssh"
	defaultRemoteWBCommand = "wb"
	sshConnectTimeout      = 10
	sshDeliveryTimeout     = 2 * time.Minute
	maxSSHStdoutBytes      = 2 << 20
	maxSSHStderrBytes      = 64 << 10
	maxSSHDiagnosticBytes  = 1024
)

// Deliverer preserves one canonical parked-session envelope and returns only
// its independently validated target receipt.
type Deliverer interface {
	Deliver(context.Context, []byte) (Result, error)
}

type Result struct {
	Receipt sessionpark.Receipt
	Replay  bool
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
		return nil, fmt.Errorf("run ssh parked-session delivery: command runner is unavailable")
	}
	return &sshDeliverer{config: config, executable: executable, runner: runner}, nil
}

func (deliverer *sshDeliverer) Deliver(ctx context.Context, raw []byte) (Result, error) {
	var result Result
	envelope, err := sessionpark.DecodeEnvelope(raw)
	if err != nil {
		return result, fmt.Errorf("validate ssh parked-session envelope: %w", err)
	}
	canonical, err := sessionpark.EncodeEnvelope(envelope)
	if err != nil || !bytes.Equal(canonical, raw) {
		return result, fmt.Errorf("validate ssh parked-session envelope: envelope must use WB canonical JSON encoding")
	}
	remoteWB := deliverer.config.WBPath
	if remoteWB == "" {
		remoteWB = defaultRemoteWBCommand
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", sshConnectTimeout)}
	if deliverer.config.User != "" {
		args = append(args, "-l", deliverer.config.User)
	}
	args = append(args, "--", deliverer.config.Host, remoteWB, "--non-interactive", "session", "receive-park", "--format", "json")
	var stdout, stderr boundedBuffer
	stdout.limit = maxSSHStdoutBytes
	stderr.limit = maxSSHStderrBytes
	deliveryContext, cancel := context.WithTimeout(ctx, sshDeliveryTimeout)
	defer cancel()
	if err := deliverer.runner.Run(deliveryContext, deliverer.executable, args, raw, &stdout, &stderr); err != nil {
		if contextErr := deliveryContext.Err(); contextErr != nil {
			return result, fmt.Errorf("ssh parked-session delivery to %s: %w", deliverer.config.Host, contextErr)
		}
		diagnostic := sanitizeDiagnostic(stderr.Bytes(), stderr.exceeded)
		if diagnostic == "" {
			return result, fmt.Errorf("ssh parked-session delivery to %s: %w", deliverer.config.Host, err)
		}
		return result, fmt.Errorf("ssh parked-session delivery to %s: %w: %s", deliverer.config.Host, err, diagnostic)
	}
	if stdout.exceeded {
		return result, fmt.Errorf("ssh parked-session response from %s exceeds %d bytes", deliverer.config.Host, maxSSHStdoutBytes)
	}
	remote, err := decodeReceiverResult(stdout.Bytes())
	if err != nil {
		return result, fmt.Errorf("validate ssh parked-session response from %s: %w", deliverer.config.Host, err)
	}
	digest := sessionmove.DigestBytes(raw)
	if remote.ResumeID != envelope.Request.ResumeID || remote.Digest != digest || remote.Phase != sessionparkreceive.PhaseCompleted || remote.Receipt == nil {
		return result, fmt.Errorf("validate ssh parked-session response from %s: response lacks the exact completed target receipt", deliverer.config.Host)
	}
	if err := sessionpark.ValidateReceipt(*remote.Receipt, envelope.Request, digest); err != nil {
		return result, fmt.Errorf("validate ssh parked-session response from %s: %w", deliverer.config.Host, err)
	}
	return Result{Receipt: *remote.Receipt, Replay: remote.Replay}, nil
}

func decodeReceiverResult(raw []byte) (sessionparkreceive.Result, error) {
	var result sessionparkreceive.Result
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return result, fmt.Errorf("unexpected trailing JSON value")
		}
		return result, err
	}
	return result, nil
}

type boundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *boundedBuffer) Write(value []byte) (int, error) {
	written := len(value)
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.exceeded = buffer.exceeded || len(value) > 0
		return written, nil
	}
	if len(value) > remaining {
		_, _ = buffer.buffer.Write(value[:remaining])
		buffer.exceeded = true
		return written, nil
	}
	_, _ = buffer.buffer.Write(value)
	return written, nil
}

func (buffer *boundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

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
