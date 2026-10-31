package herdr

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"path/filepath"
	"time"
	"unicode/utf8"
)

const originalTUIProtocol = 22
const maxOriginalSocketRequestBytes = 1 << 20
const maxOriginalSocketReplyBytes = 1 << 20

// AgentPromptSocket uses the original TUI0.9.1 newline-delimited API for a
// single authorized submission. This avoids operating-system per-argv limits
// for canonical ASCII messages. It never retries after writing a request.
// Advisory PaneSendText retains its separate unsubmitted CLI contract.
func (c *Client) AgentPromptSocket(ctx context.Context, target, text string) (Agent, error) {
	if ctx == nil {
		return Agent{}, fmt.Errorf("private socket prompt requires context")
	}
	if !utf8.ValidString(target) || !utf8.ValidString(text) {
		return Agent{}, fmt.Errorf("%w: private socket prompt requires valid UTF-8", ErrInvalidPromptText)
	}
	if err := ValidateTarget("agent target", target); err != nil {
		return Agent{}, err
	}
	if err := ValidatePromptText(text); err != nil {
		return Agent{}, err
	}
	if len(text) > 512<<10 {
		return Agent{}, fmt.Errorf("%w: socket prompt exceeds bounded text limit", ErrInvalidPromptText)
	}
	if c == nil || !filepath.IsAbs(c.socketPath) || filepath.Clean(c.socketPath) != c.socketPath {
		return Agent{}, fmt.Errorf("%w: actionable socket prompt requires an explicit recorded endpoint", ErrServerUnreachable)
	}
	ping, err := c.callPromptSocket(ctx, "wb:message:ping", "ping", struct{}{})
	if err != nil {
		return Agent{}, err
	}
	var status struct {
		Type     string `json:"type"`
		Version  string `json:"version"`
		Protocol int    `json:"protocol"`
	}
	if json.Unmarshal(ping, &status) != nil || status.Type != "pong" || status.Version != "0.9.1" || status.Protocol != originalTUIProtocol {
		return Agent{}, fmt.Errorf("%w: actionable messages require original Herdr TUI0.9.1 protocol22", ErrUnsupportedVersion)
	}
	params := struct {
		Target string `json:"target"`
		Text   string `json:"text"`
	}{target, text}
	raw, err := c.callPromptSocket(ctx, "wb:message:prompt", "agent.prompt", params)
	if err != nil {
		return Agent{}, err
	}
	var body struct {
		Type  string         `json:"type"`
		Agent rawPaneOrAgent `json:"agent"`
	}
	if json.Unmarshal(raw, &body) != nil || body.Type != "agent_prompted" || !body.Agent.valid() || body.Agent.PaneID != target {
		return Agent{}, fmt.Errorf("%w: invalid private prompt acknowledgment", ErrUnparseableOutput)
	}
	return body.Agent.toAgent(), nil
}

func (c *Client) callPromptSocket(ctx context.Context, id, method string, params any) (json.RawMessage, error) {
	request := struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{id, method, params}
	raw, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	if len(raw)+1 > maxOriginalSocketRequestBytes {
		return nil, fmt.Errorf("%w: private socket request exceeds original protocol limit", ErrInvalidPromptText)
	}
	callCtx, cancel := context.WithTimeout(ctx, c.effectiveTimeout())
	defer cancel()
	dial := c.socketDial
	if dial == nil {
		dial = dialHerdrSocket
	}
	conn, err := dial(callCtx, c.socketPath)
	if err != nil {
		return nil, privateSocketFailure(callCtx, err)
	}
	defer func() { _ = conn.Close() }()
	stop := context.AfterFunc(callCtx, func() { _ = conn.Close() })
	defer stop()
	deadline, _ := callCtx.Deadline()
	if err := conn.SetDeadline(deadline); err != nil {
		return nil, privateSocketFailure(callCtx, err)
	}
	if _, err := io.Copy(conn, bytes.NewReader(append(raw, '\n'))); err != nil {
		return nil, privateSocketFailure(callCtx, err)
	}
	reader := bufio.NewReader(io.LimitReader(conn, maxOriginalSocketReplyBytes+1))
	reply, err := reader.ReadBytes('\n')
	if len(reply) > maxOriginalSocketReplyBytes {
		return nil, fmt.Errorf("%w: private socket reply exceeds limit", ErrUnparseableOutput)
	}
	if err != nil {
		return nil, privateSocketFailure(callCtx, err)
	}
	var envelope herdrEnvelope
	decoder := json.NewDecoder(bytes.NewReader(reply))
	decoder.DisallowUnknownFields()
	if decoder.Decode(&envelope) != nil || envelope.ID != id || (envelope.Error == nil) == (len(envelope.Result) == 0) {
		return nil, fmt.Errorf("%w: invalid private socket reply identity/envelope", ErrUnparseableOutput)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return nil, fmt.Errorf("%w: private socket reply contains trailing data", ErrUnparseableOutput)
	}
	if envelope.Error != nil {
		if envelope.Error.Code == "" || envelope.Error.Message == "" {
			return nil, fmt.Errorf("%w: invalid private socket refusal", ErrUnparseableOutput)
		}
		return nil, c.mapHerdrError([]string{"agent", "prompt"}, *envelope.Error)
	}
	return envelope.Result, nil
}

// Keep low-level endpoint errors and private payload bytes out of diagnostics.
func privateSocketFailure(ctx context.Context, cause error) error {
	if ctx.Err() != nil {
		return fmt.Errorf("herdr private socket request: %w", ctx.Err())
	}
	var timeout net.Error
	if deadline, ok := ctx.Deadline(); ok && !time.Now().Before(deadline) && errors.As(cause, &timeout) && timeout.Timeout() {
		return fmt.Errorf("herdr private socket request: %w", context.DeadlineExceeded)
	}
	return fmt.Errorf("%w: private socket request failed", ErrServerUnreachable)
}

// The injected seam returns a bounded net.Conn; production chooses the
// platform's original local socket/named-pipe mapping from recorded identity.
type socketDialer func(context.Context, string) (net.Conn, error)
