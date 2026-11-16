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
		err := store.retryWake(ctx, agent)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if batchErr := s.relayBatch(ctx, store, agent); err == nil {
			err = batchErr
		}
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
	var firstError error
	for _, m := range messages {
		if err := s.relayMessage(ctx, store, agent, m); err != nil && firstError == nil {
			firstError = err
		}
	}
	return firstError
}

func (s SSH) relayMessage(ctx context.Context, store Store, agent string, m Message) error {
	receipt, err := store.Deliver(ctx, m)
	if receipt.ID != m.ID || receipt.Digest != m.Digest() || receipt.Status != "durable" {
		return fmt.Errorf("relay local delivery unconfirmed: %w", err)
	}
	wakeErr := err
	if receipt.Wake == "ambiguous" || receipt.Wake == "rejected" {
		wakeErr = nil
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
	return wakeErr
}

// retryWake uses the local durable inbox, so a stale native binding never
// reserves remote receive slots or requires retransferring message bodies.
func (s Store) retryWake(ctx context.Context, agent string) error {
	if s.Notify == nil {
		return nil
	}
	messages, err := s.pending(agent)
	if err != nil {
		return err
	}
	var firstError error
	for _, m := range messages {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		receipt, err := s.Deliver(ctx, m)
		if err != nil && receipt.Wake != "ambiguous" && receipt.Wake != "rejected" && firstError == nil {
			firstError = err
		}
	}
	return firstError
}
