// Package sessioncourier delivers WB-owned session handoff protocol values.
// A courier transports exact request bytes and validates the target response;
// it does not own handoff state or source custody.
package sessioncourier

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
	if len(raw) == 0 {
		return result, fmt.Errorf("ssh session request must not be empty")
	}
	if len(raw) > maxSSHRequestBytes {
		return result, fmt.Errorf("ssh session request exceeds %d bytes", maxSSHRequestBytes)
	}
	request, err := sessionmove.DecodeRequest(raw)
	if err != nil {
		return result, fmt.Errorf("validate ssh session request: %w", err)
	}
	canonicalRequest, err := sessionmove.EncodeRequest(request)
	if err != nil {
		return result, fmt.Errorf("encode canonical ssh session request: %w", err)
	}
	if !bytes.Equal(raw, canonicalRequest) {
		return result, fmt.Errorf("ssh session request must use WB's canonical JSON encoding")
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
	result, err = decodeSSHResult(stdout.Bytes())
	if err != nil {
		return sessionreceive.Result{}, fmt.Errorf("validate ssh response from %s: %w", d.config.Host, err)
	}
	if err := validateSSHResult(result, request, raw); err != nil {
		return sessionreceive.Result{}, fmt.Errorf("validate ssh response from %s: %w", d.config.Host, err)
	}
	return result, nil
}

func decodeSSHResult(raw []byte) (sessionreceive.Result, error) {
	var result sessionreceive.Result
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		return result, fmt.Errorf("decode one session receive result: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return sessionreceive.Result{}, fmt.Errorf("session receive result has a trailing JSON value")
		}
		return sessionreceive.Result{}, fmt.Errorf("decode trailing session receive output: %w", err)
	}
	return result, nil
}

func validateSSHResult(result sessionreceive.Result, request sessionmove.Request, raw []byte) error {
	wantRequest, err := sessionmove.EncodeRequest(request)
	if err != nil {
		return err
	}
	gotRequest, err := sessionmove.EncodeRequest(result.Request)
	if err != nil {
		return fmt.Errorf("response request is invalid: %w", err)
	}
	if !bytes.Equal(gotRequest, wantRequest) {
		return fmt.Errorf("response request does not match the delivered handoff")
	}
	wantDigest := sessionmove.DigestBytes(raw)
	if result.Digest != wantDigest {
		return fmt.Errorf("response request_digest %q does not match exact delivered bytes %q", result.Digest, wantDigest)
	}
	if result.Phase != sessionmove.PhaseCompleted {
		return fmt.Errorf("response phase %q is not %q", result.Phase, sessionmove.PhaseCompleted)
	}
	if result.Worktree != nil && result.Worktree.Commit != request.BundleCommit {
		return fmt.Errorf("response pinned worktree commit does not match bundle_commit %q", request.BundleCommit)
	}
	receipt := result.Receipt
	if receipt == nil {
		return fmt.Errorf("response phase %q does not include a completion receipt", result.Phase)
	}
	if err := sessionmove.ValidateReceiptForRequest(*receipt, request, wantDigest); err != nil {
		return fmt.Errorf("response receipt is invalid: %w", err)
	}
	successor := result.Successor
	if successor == nil {
		if !result.Replay {
			return fmt.Errorf("fresh completed response does not include a successor launch result")
		}
		return nil
	}
	if successor.HandoffID != request.HandoffID {
		return fmt.Errorf("response successor handoff_id %q does not match %q", successor.HandoffID, request.HandoffID)
	}
	if successor.WBSessionID != request.SuccessorWBSessionID {
		return fmt.Errorf("response successor wb_session_id %q does not match %q", successor.WBSessionID, request.SuccessorWBSessionID)
	}
	if successor.PredecessorWBSessionID != request.PredecessorWBSessionID {
		return fmt.Errorf("response successor predecessor_wb_session_id %q does not match %q", successor.PredecessorWBSessionID, request.PredecessorWBSessionID)
	}
	if successor.TargetMachine != request.TargetMachine {
		return fmt.Errorf("response successor target_machine %q does not match %q", successor.TargetMachine, request.TargetMachine)
	}
	wantRuntime := strings.TrimSpace(request.RequestedHarness)
	if wantRuntime == "" {
		wantRuntime = strings.TrimSpace(request.SourceRuntime)
	}
	if successor.Runtime != wantRuntime {
		return fmt.Errorf("response successor runtime %q does not match requested runtime %q", successor.Runtime, wantRuntime)
	}
	wantModel := ""
	if wantRuntime == strings.TrimSpace(request.SourceRuntime) {
		wantModel = strings.TrimSpace(request.SourceModel)
	}
	if successor.Model != wantModel {
		return fmt.Errorf("response successor model %q does not match expected model %q", successor.Model, wantModel)
	}
	wantTmuxName := "wb-session-" + request.SuccessorWBSessionID
	if successor.TmuxName != wantTmuxName {
		return fmt.Errorf("response successor tmux_name %q does not match %q", successor.TmuxName, wantTmuxName)
	}
	if successor.PinnedCommit != request.BundleCommit {
		return fmt.Errorf("response successor pinned_commit %q does not match bundle_commit %q", successor.PinnedCommit, request.BundleCommit)
	}
	if !filepath.IsAbs(successor.WorktreeDir) || filepath.Clean(successor.WorktreeDir) != successor.WorktreeDir {
		return fmt.Errorf("response successor worktree_dir %q is not a clean absolute path", successor.WorktreeDir)
	}
	if successor.PID <= 0 || successor.StartedAt.IsZero() {
		return fmt.Errorf("response successor does not identify one live started process")
	}
	if result.Worktree != nil && successor.WorktreeDir != result.Worktree.WorktreeDir {
		return fmt.Errorf("response successor worktree %q does not match received worktree %q", successor.WorktreeDir, result.Worktree.WorktreeDir)
	}
	if successor.TargetWorkLogRef != receipt.TargetWorkLogReference {
		return fmt.Errorf("response successor target_work_log_ref %q does not match receipt target_work_log_reference %q", successor.TargetWorkLogRef, receipt.TargetWorkLogReference)
	}
	if successor.AttemptID != receipt.AttemptID || successor.AttemptIndex != receipt.AttemptIndex || successor.PID != receipt.PID {
		return fmt.Errorf("response successor launch attempt does not match completion receipt")
	}
	if successor.HandoffID != receipt.HandoffID || successor.WBSessionID != receipt.SuccessorWBSessionID ||
		successor.PredecessorWBSessionID != receipt.PredecessorWBSessionID || successor.TargetMachine != receipt.TargetMachine {
		return fmt.Errorf("response successor identity does not match completion receipt")
	}
	if successor.TmuxName != receipt.TmuxName {
		return fmt.Errorf("response successor tmux_name %q does not match receipt %q", successor.TmuxName, receipt.TmuxName)
	}
	if successor.Runtime != receipt.Runtime || successor.Model != receipt.Model || successor.NativeHarnessID != receipt.NativeHarnessID {
		return fmt.Errorf("response successor harness identity does not match completion receipt")
	}
	if successor.PinnedCommit != receipt.PinnedCommit {
		return fmt.Errorf("response successor pinned_commit %q does not match receipt %q", successor.PinnedCommit, receipt.PinnedCommit)
	}
	if !successor.StartedAt.Equal(receipt.StartedAt) {
		return fmt.Errorf("response receipt started_at %s does not match successor %s", receipt.StartedAt, successor.StartedAt)
	}
	return nil
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
