package agentmessagewake

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/coder/websocket"
)

// rpcRejectionError identifies a matching, well-formed server rejection. It
// deliberately retains no server-provided message or data.
type rpcRejectionError struct{ method string }

func (e *rpcRejectionError) Error() string { return fmt.Sprintf("codex rejected %s", e.method) }

type connection interface {
	Call(string, any) (json.RawMessage, error)
	Notify(string) error
	Close()
}

type codexConnection struct {
	ctx       context.Context
	socket    *websocket.Conn
	transport *http.Transport
	nextID    int
}

func dialCodex(ctx context.Context, path string) (connection, error) {
	transport := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", path)
	}}
	socket, _, err := websocket.Dial(ctx, "ws://localhost", &websocket.DialOptions{HTTPClient: &http.Client{Transport: transport, Timeout: 10 * time.Second}})
	if err != nil {
		transport.CloseIdleConnections()
		return nil, err
	}
	socket.SetReadLimit(1 << 20)
	return &codexConnection{ctx: ctx, socket: socket, transport: transport}, nil
}

func (c *codexConnection) Close() { _ = c.socket.CloseNow(); c.transport.CloseIdleConnections() }

func (c *codexConnection) Notify(method string) error {
	payload, _ := json.Marshal(map[string]string{"method": method})
	return c.socket.Write(c.ctx, websocket.MessageText, payload)
}

func (c *codexConnection) Call(method string, params any) (json.RawMessage, error) {
	c.nextID++
	payload, err := json.Marshal(map[string]any{"id": c.nextID, "method": method, "params": params})
	if err != nil {
		return nil, errors.New("invalid Codex request")
	}
	if err = c.socket.Write(c.ctx, websocket.MessageText, payload); err != nil {
		return nil, errors.New("codex request transport failed")
	}
	for n := 0; n < 128; n++ {
		kind, raw, readErr := c.socket.Read(c.ctx)
		if readErr != nil {
			return nil, errors.New("codex response transport failed")
		}
		var response struct {
			ID     json.RawMessage `json:"id"`
			Result json.RawMessage `json:"result"`
			Error  json.RawMessage `json:"error"`
			Method string          `json:"method"`
		}
		if kind != websocket.MessageText || json.Unmarshal(raw, &response) != nil {
			return nil, errors.New("invalid Codex response")
		}
		if len(response.ID) == 0 && response.Method != "" {
			continue
		}
		if string(response.ID) != strconv.Itoa(c.nextID) {
			return nil, errors.New("codex response ID did not match")
		}
		if len(response.Error) > 0 && string(response.Error) != "null" {
			var rejection struct {
				Code    *int    `json:"code"`
				Message *string `json:"message"`
			}
			if json.Unmarshal(response.Error, &rejection) != nil || rejection.Code == nil || rejection.Message == nil || (len(response.Result) > 0 && string(response.Result) != "null") {
				return nil, errors.New("invalid Codex rejection response")
			}
			return nil, &rpcRejectionError{method: method}
		}
		if len(response.Result) == 0 || string(response.Result) == "null" {
			return nil, errors.New("codex response omitted its result")
		}
		return response.Result, nil
	}
	return nil, errors.New("codex response notification limit exceeded")
}
