package agentmessage

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"time"
)

// Relay holds an outbound SSH receive on a reachable mailbox machine. It
// acknowledges the relay copy only after durable local delivery; local agent
// processing still requires a separate local acknowledgment.
func (s SSH) Relay(ctx context.Context, store Store, agent string, once bool) error {
	if err := s.Config.Validate(); err != nil {
		return err
	}
	if err := ValidateAgent(agent); err != nil {
		return err
	}
	for {
		err := s.relayBatch(ctx, store, agent)
		if once {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err != nil {
			if s.ReportRetry != nil {
				s.ReportRetry(err)
			}
			timer := time.NewTimer(2 * time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
	}
}

func (s SSH) relayCall(ctx context.Context, args []string, output io.Writer) error {
	remoteWB := s.Config.WBPath
	if remoteWB == "" {
		remoteWB = "wb"
	}
	argv := []string{"-T", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "ConnectTimeout=10"}
	if s.Config.User != "" {
		argv = append(argv, "-l", s.Config.User)
	}
	argv = append(argv, "--", s.Config.Host, remoteWB, "--non-interactive", "message")
	argv = append(argv, args...)
	run := s.Run
	if run == nil {
		run = runCommand
	}
	var diagnostic boundedOutput
	callCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	if err := run(callCtx, "ssh", argv, bytes.NewReader(nil), output, &diagnostic); err != nil {
		return fmt.Errorf("relay SSH command failed: %w", err)
	}
	return nil
}

func (s SSH) relayBatch(ctx context.Context, store Store, agent string) error {
	// receive returns at most 100 envelopes. Bound aggregate output before JSON
	// decoding, including worst-case JSON escaping of each bounded body.
	var output boundedOutput
	output.limit = 100*MaxEnvelopeBytes + 4096
	if err := s.relayCall(ctx, []string{"receive", "--agent", agent, "--wait", "1m"}, &output); err != nil {
		return err
	}
	var messages []Message
	if err := json.Unmarshal(output.Bytes(), &messages); err != nil {
		return fmt.Errorf("invalid relay inbox response: %w", err)
	}
	if len(messages) > 100 {
		return fmt.Errorf("relay inbox exceeds 100 messages")
	}
	for _, m := range messages {
		if err := m.Validate(); err != nil {
			return err
		}
		if m.To != agent {
			return fmt.Errorf("relay received a message for another agent")
		}
	}
	for _, m := range messages {
		receipt, err := store.Deliver(ctx, m)
		if receipt.ID != m.ID || receipt.Digest != m.Digest() || receipt.Status != "durable" {
			return fmt.Errorf("relay local delivery unconfirmed: %w", err)
		}
		if err != nil && receipt.Wake != "ambiguous" {
			return fmt.Errorf("relay retained remote copy after local wake failure: %w", err)
		}
		var ack boundedOutput
		if err = s.relayCall(ctx, []string{"ack", m.ID, "--agent", agent}, &ack); err != nil {
			return err
		}
		var result struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		}
		if err = json.Unmarshal(ack.Bytes(), &result); err != nil || result.ID != m.ID || result.Status != "acknowledged" {
			return fmt.Errorf("relay acknowledgment is unconfirmed; message may be replayed safely")
		}
	}
	return nil
}
