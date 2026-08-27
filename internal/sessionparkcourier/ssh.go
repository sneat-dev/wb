// Package sessionparkcourier transports canonical parked-session envelopes.
// Source admission/finalization remains in sessionpark.Store and target
// reconstruction remains in sessionparkreceive.
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
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
	"github.com/sneat-dev/wb/internal/sessionpark"
	"github.com/sneat-dev/wb/internal/sessionparkreceive"
)

const (
	sshExecutableName  = "ssh"
	remoteWBCommand    = "wb"
	sshConnectTimeout  = 10
	sshDeliveryTimeout = 2 * time.Minute
	maxSSHStdoutBytes  = 2 << 20
	maxSSHStderrBytes  = 64 << 10
)

type Result struct {
	Receipt sessionpark.Receipt
	Replay  bool
}

// remoteStderrFileName is the private journal file that receives unredacted
// remote stderr on a failed delivery.
const remoteStderrFileName = "remote-stderr.log"

// Options carries the two host-side concerns the courier itself must not
// decide: where an ignored-configuration warning is surfaced, and which
// private directory may receive remote diagnostics.
type Options struct {
	// Warn receives operator warnings about configuration that park ignores.
	// A nil Warn discards them.
	Warn io.Writer
	// DiagnosticDir is a private per-resume directory that receives remote
	// stderr on failure. An empty DiagnosticDir disables capture entirely, in
	// which case a failure reports that stderr was suppressed rather than
	// printing it.
	DiagnosticDir string
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

type SSHDeliverer struct {
	config     sessionmove.SSHConfig
	executable string
	runner     commandRunner
	options    Options
}

func NewSSHDeliverer(config sessionmove.SSHConfig, options Options) (*SSHDeliverer, error) {
	return newSSHDeliverer(config, exec.LookPath, execCommandRunner{}, options)
}

func newSSHDeliverer(config sessionmove.SSHConfig, lookPath func(string) (string, error), runner commandRunner, options Options) (*SSHDeliverer, error) {
	if err := config.Validate(); err != nil {
		return nil, err
	}
	// sessionmove.SSHConfig retains wb_path for the legacy session-move
	// protocol, which is a different verb with different requirements. Park
	// always uses a fixed non-shell argv, so wb_path cannot apply here.
	//
	// Rejecting it outright would punish an operator who set the key
	// legitimately for session move. Silently ignoring it would be its own
	// failure: the key would read as configured and behave as not. Warning is
	// what makes ignoring it honest.
	if config.WBPath != "" && options.Warn != nil {
		fmt.Fprintf(options.Warn, "warning: ssh.wb_path is set but ignored by `session park`\n  (park requires a fixed non-shell argv); it still applies to `session move`.\n")
	}
	if lookPath == nil {
		return nil, fmt.Errorf("resolve ssh executable: executable lookup is unavailable")
	}
	executable, err := lookPath(sshExecutableName)
	if err != nil {
		return nil, fmt.Errorf("resolve ssh executable: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return nil, fmt.Errorf("resolve ssh executable: result is not one clean absolute path")
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("resolve ssh executable: result is not one executable regular file")
	}
	if runner == nil {
		return nil, fmt.Errorf("run SSH parked-session delivery: command runner is unavailable")
	}
	return &SSHDeliverer{config: config, executable: executable, runner: runner, options: options}, nil
}

func (deliverer *SSHDeliverer) Deliver(ctx context.Context, raw []byte) (Result, error) {
	var result Result
	envelope, err := sessionpark.DecodeEnvelope(raw)
	if err != nil {
		return result, fmt.Errorf("validate SSH parked-session envelope: %w", err)
	}
	canonical, err := sessionpark.EncodeEnvelope(envelope)
	if err != nil || !bytes.Equal(canonical, raw) {
		return result, fmt.Errorf("validate SSH parked-session envelope: envelope must use WB canonical JSON encoding")
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", fmt.Sprintf("ConnectTimeout=%d", sshConnectTimeout)}
	if deliverer.config.User != "" {
		args = append(args, "-l", deliverer.config.User)
	}
	args = append(args, "--", deliverer.config.Host, remoteWBCommand, "--non-interactive", "session", "receive-park", "--format", "json")
	stdout := boundedBuffer{limit: maxSSHStdoutBytes}
	stderr := boundedBuffer{limit: maxSSHStderrBytes}
	deliveryContext, cancel := context.WithTimeout(ctx, sshDeliveryTimeout)
	defer cancel()
	if err := deliverer.runner.Run(deliveryContext, deliverer.executable, args, raw, &stdout, &stderr); err != nil {
		if contextErr := deliveryContext.Err(); contextErr != nil {
			return result, fmt.Errorf("SSH parked-session delivery failed: %w", contextErr)
		}
		// Remote stderr is never echoed to terminal output or CI logs: it may
		// carry a receiver diagnostic derived from private continuation or
		// credentials, and sanitizing control characters makes text safe to
		// print, not safe to disclose. It is written unredacted to the private
		// local journal instead, and only its path is reported.
		return result, fmt.Errorf("courier failed on target host: %w\n  %s", err, deliverer.captureRemoteStderr(stderr.Bytes()))
	}
	if stdout.exceeded {
		return result, fmt.Errorf("SSH parked-session response exceeds %d bytes", maxSSHStdoutBytes)
	}
	remote, err := decodeReceiverResult(stdout.Bytes())
	if err != nil {
		return result, fmt.Errorf("validate SSH parked-session response: %w", err)
	}
	digest := sessionmove.DigestBytes(raw)
	if remote.ResumeID != envelope.Request.ResumeID || remote.Digest != digest ||
		remote.Phase != sessionparkreceive.PhaseCompleted || remote.Receipt == nil {
		return result, fmt.Errorf("validate SSH parked-session response: response lacks the exact completed target receipt")
	}
	if err := sessionpark.ValidateReceipt(*remote.Receipt, envelope.Request, digest); err != nil {
		return result, fmt.Errorf("validate SSH parked-session response: %w", err)
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

// captureRemoteStderr writes unredacted remote stderr to the private local
// journal and returns a caller-safe sentence naming the path. It never returns
// the diagnostic itself: if the journal cannot be written, the diagnostic is
// dropped rather than disclosed.
func (deliverer *SSHDeliverer) captureRemoteStderr(diagnostic []byte) string {
	if deliverer.options.DiagnosticDir == "" || len(diagnostic) == 0 {
		return "remote stderr suppressed; journal unavailable"
	}
	if err := os.MkdirAll(deliverer.options.DiagnosticDir, 0o700); err != nil {
		return "remote stderr suppressed; journal unavailable"
	}
	path := filepath.Join(deliverer.options.DiagnosticDir, remoteStderrFileName)
	if err := os.WriteFile(path, diagnostic, 0o600); err != nil {
		return "remote stderr suppressed; journal unavailable"
	}
	return "remote stderr written to:\n  " + path
}
