package agentmessage

import (
	"bytes"
	"context"
	"encoding/json"
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
	bytes.Buffer
	limit int
}

func (b *boundedOutput) Write(p []byte) (int, error) {
	limit := b.limit
	if limit == 0 {
		limit = MaxEnvelopeBytes
	}
	if b.Len()+len(p) > limit {
		return 0, fmt.Errorf("remote response exceeds limit")
	}
	return b.Buffer.Write(p)
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
		return Receipt{}, fmt.Errorf("remote delivery is unconfirmed; retry the same --id and message")
	}
	if err != nil {
		return receipt, fmt.Errorf("message is durable but remote wake failed; retry the same --id and message to inspect its fenced outcome")
	}
	return receipt, nil
}
