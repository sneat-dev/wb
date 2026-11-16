package agentmessage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

type CommandRunner func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

type SSH struct {
	Config      sessionmove.SSHConfig
	Run         CommandRunner
	ReportRetry func(error)
}

func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdin = stdin
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

type boundedOutput struct {
	buffer bytes.Buffer
	limit  int
}

func (b *boundedOutput) Bytes() []byte { return b.buffer.Bytes() }

func (b *boundedOutput) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = MaxEnvelopeBytes
	}
	if b.buffer.Len()+len(p) > limit {
		return 0, fmt.Errorf("remote response exceeds limit")
	}
	return b.buffer.Write(p)
}

// Send uses authenticated OpenSSH with strict host-key verification. Message
// bytes appear only on stdin, never in remote command text or process arguments.
func (s SSH) Send(ctx context.Context, m Message) (Receipt, error) {
	if err := s.Config.Validate(); err != nil {
		return Receipt{}, err
	}
	if err := m.Validate(); err != nil {
		return Receipt{}, err
	}
	remoteWB := s.Config.WBPath
	if remoteWB == "" {
		remoteWB = "wb"
	}
	args := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"}
	if s.Config.User != "" {
		args = append(args, "-l", s.Config.User)
	}
	args = append(args, "--", s.Config.Host, remoteWB, "--non-interactive", "message", "deliver")
	raw, _ := json.Marshal(m)
	var stdout, stderr boundedOutput
	run := s.Run
	if run == nil {
		run = runCommand
	}
	deliveryCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	err := run(deliveryCtx, "ssh", args, bytes.NewReader(raw), &stdout, &stderr)
	var receipt Receipt
	decodeErr := json.Unmarshal(stdout.Bytes(), &receipt)
	if decodeErr != nil || receipt.ID != m.ID || receipt.Digest != m.Digest() || receipt.Status != "durable" {
		return Receipt{}, unconfirmedDelivery(deliveryCtx, err)
	}
	if err != nil {
		if receipt.Wake == "rejected" {
			return receipt, fmt.Errorf("message is durable but native wake was rejected; inspect recipient inbox and binding; this ID will not be resubmitted")
		}
		if receipt.Wake == "ambiguous" {
			return receipt, fmt.Errorf("message is durable but native wake is ambiguous; inspect recipient inbox; this ID is fenced against resubmission")
		}
		return receipt, fmt.Errorf("message is durable but remote wake failed; retry the same --id and message to inspect its fenced outcome")
	}
	return receipt, nil
}

// Classify only process state. Remote stderr, stdout, and runner error text may
// contain message bytes and must never be echoed in a transport diagnostic.
func unconfirmedDelivery(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("SSH delivery was cancelled or timed out; outcome is unconfirmed; inspect connectivity before retrying the same --id and exact message")
	}
	var exited *exec.ExitError
	if errors.As(err, &exited) {
		code := exited.ExitCode()
		if code >= 0 && code != 255 {
			return fmt.Errorf("remote WB command exited with status %d without a matching durable receipt; inspect remote setup or a conflicting message ID before retrying", code)
		}
		return fmt.Errorf("SSH transport exited with status %d; delivery is unconfirmed; inspect connectivity and host authentication before retrying the same --id and exact message", code)
	}
	if err == nil {
		return fmt.Errorf("remote command exited successfully without a matching durable receipt; inspect remote WB protocol compatibility before retrying")
	}
	return fmt.Errorf("SSH delivery is unconfirmed (process status unavailable); inspect local SSH setup and connectivity before retrying the same --id and exact message")
}
