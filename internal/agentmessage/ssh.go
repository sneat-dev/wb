package agentmessage

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

type CommandRunner func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error

type SSH struct {
	Config      sessionmove.SSHConfig
	Run         CommandRunner
	ReportRetry func(error)
}

func runCommand(ctx context.Context, name string, args []string, stdin io.Reader, stdout, _ io.Writer) error {
	return runCommandWithRunner(ctx, runner.New(), name, args, stdin, stdout)
}

func runCommandWithRunner(ctx context.Context, processRunner runner.Runner, name string, args []string, stdin io.Reader, stdout io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	input, err := io.ReadAll(io.LimitReader(stdin, MaxEnvelopeBytes+1))
	if err != nil {
		return err
	}
	if len(input) > MaxEnvelopeBytes {
		return errors.New("command input exceeds message limit")
	}
	outputLimit := 100*MaxEnvelopeBytes + 4096
	if bounded, ok := stdout.(*boundedOutput); ok {
		outputLimit = MaxEnvelopeBytes
		if bounded.limit > 0 {
			outputLimit = bounded.limit
		}
	}
	result, runErr := processRunner.RunOpts(ctx, "", runner.RunOptions{
		Stdin: input, WaitDelay: 3 * time.Second, StdoutLimit: outputLimit, DiscardStderr: true,
	}, name, args...)
	count, writeErr := io.WriteString(stdout, result.Stdout)
	if writeErr != nil {
		return writeErr
	}
	if count != len(result.Stdout) {
		return io.ErrShortWrite
	}
	return runErr
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
	if decodeErr != nil || !receipt.validFor(m) {
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
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("local SSH executable is unavailable; delivery is unconfirmed; check SSH installation and the agent's PATH before retrying the same --id and exact message")
	}
	if errors.Is(err, exec.ErrDot) {
		return fmt.Errorf("local SSH executable resolves through the current directory and was refused; delivery is unconfirmed; configure a trusted SSH executable on PATH before retrying the same --id and exact message")
	}
	if errors.Is(err, os.ErrPermission) {
		return fmt.Errorf("local SSH execution was denied; delivery is unconfirmed; inspect the agent's existing execution permissions before retrying the same --id and exact message")
	}
	if errors.Is(err, runner.ErrOutputTooLarge) {
		return fmt.Errorf("SSH response exceeded the output limit; delivery is unconfirmed; inspect remote protocol compatibility before retrying the same --id and exact message")
	}
	var exited interface{ ExitCode() int }
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
