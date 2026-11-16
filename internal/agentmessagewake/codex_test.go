package agentmessagewake

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func unixServer(t *testing.T, serve func(context.Context, *websocket.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "wb-codex-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	path := filepath.Join(dir, "server.sock")
	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, acceptErr := websocket.Accept(w, r, nil)
		if acceptErr != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		t.Cleanup(cancel)
		serve(ctx, conn)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	return path
}

func TestNativeCodexConnectionInitializesAndSubmitsOnlyExactThread(t *testing.T) {
	t.Parallel()
	path := unixServer(t, func(ctx context.Context, conn *websocket.Conn) {
		for {
			_, raw, err := conn.Read(ctx)
			if err != nil {
				return
			}
			var req struct {
				ID     int             `json:"id"`
				Method string          `json:"method"`
				Params json.RawMessage `json:"params"`
			}
			if err = json.Unmarshal(raw, &req); err != nil {
				t.Error(err)
				return
			}
			if req.Method == "initialized" {
				continue
			}
			result := `{}`
			switch req.Method {
			case "initialize":
			case "thread/read":
				var p struct {
					ThreadID     string `json:"threadId"`
					IncludeTurns bool   `json:"includeTurns"`
				}
				_ = json.Unmarshal(req.Params, &p)
				if p.ThreadID != "thread-1" || p.IncludeTurns {
					t.Error("incorrect metadata query")
				}
				result = `{"thread":{"id":"thread-1","status":{"type":"idle"}}}`
			case "turn/start":
				var params struct {
					Input []struct {
						Text string `json:"text"`
					} `json:"input"`
				}
				_ = json.Unmarshal(req.Params, &params)
				if len(params.Input) != 1 || !strings.Contains(params.Input[0].Text, "wb --projects-root '/projects/with '\"'\"' quote' message receive --agent mac") {
					t.Error("missing correctly quoted receiver projects root")
				}
				if strings.Contains(string(req.Params), "private message") {
					t.Error("raw body in notification")
				}
				result = `{"turn":{"id":"turn-1"}}`
			default:
				t.Errorf("unexpected method %s", req.Method)
			}
			reply, _ := json.Marshal(map[string]any{"id": req.ID, "result": json.RawMessage(result)})
			if err = conn.Write(ctx, websocket.MessageText, reply); err != nil {
				return
			}
		}
	})
	s := Service{Root: filepath.Join(t.TempDir(), "wake"), ProjectsRoot: "/projects/with ' quote"}
	if err := s.Bind(context.Background(), "mac", path, "thread-1"); err != nil {
		t.Fatal(err)
	}
	if status, err := s.Notify(context.Background(), testMessage()); err != nil || status != Submitted {
		t.Fatal(status, err)
	}
}

func TestCodexResponseValidationRejectsAmbiguousFrames(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, reply string
		kind        websocket.MessageType
		count       int
	}{
		{"invalid-json", "broken", websocket.MessageText, 1},
		{"binary", `{"id":1,"result":{}}`, websocket.MessageBinary, 1},
		{"wrong-id", `{"id":2,"result":{}}`, websocket.MessageText, 1},
		{"server-error", `{"id":1,"error":{"message":"private server text"}}`, websocket.MessageText, 1},
		{"missing-result", `{"id":1}`, websocket.MessageText, 1},
		{"null-result", `{"id":1,"result":null}`, websocket.MessageText, 1},
		{"notifications", `{"method":"event","params":{"private":"text"}}`, websocket.MessageText, 128},
		{"oversized", strings.Repeat("x", (1<<20)+1), websocket.MessageText, 1},
		{"closed", "", websocket.MessageText, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := unixServer(t, func(ctx context.Context, conn *websocket.Conn) {
				_, _, _ = conn.Read(ctx)
				for i := 0; i < tt.count; i++ {
					if err := conn.Write(ctx, tt.kind, []byte(tt.reply)); err != nil {
						return
					}
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			t.Cleanup(cancel)
			conn, err := dialCodex(ctx, path)
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(conn.Close)
			if _, err = conn.Call("thread/read", map[string]string{"threadId": "thread-1"}); err == nil || strings.Contains(err.Error(), "private server text") {
				t.Fatal("response accepted or leaked", err)
			}
		})
	}
}

func TestCodexIgnoresNotificationsAndAcceptsMatchingResult(t *testing.T) {
	t.Parallel()
	path := unixServer(t, func(ctx context.Context, conn *websocket.Conn) {
		_, _, _ = conn.Read(ctx)
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"method":"event"}`))
		_ = conn.Write(ctx, websocket.MessageText, []byte(`{"id":1,"error":null,"result":{"ok":true}}`))
	})
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	conn, err := dialCodex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(conn.Close)
	raw, err := conn.Call("test", struct{}{})
	if err != nil || string(raw) != `{"ok":true}` {
		t.Fatal(string(raw), err)
	}
}

func TestCodexTransportErrorsAndInvalidRequestAreBounded(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	t.Cleanup(cancel)
	if _, err := dialCodex(ctx, filepath.Join(t.TempDir(), "missing.sock")); err == nil {
		t.Fatal("missing socket")
	}
	path := unixServer(t, func(ctx context.Context, conn *websocket.Conn) {
		for {
			if _, _, err := conn.Read(ctx); err != nil {
				return
			}
		}
	})
	conn, err := dialCodex(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = conn.Call("bad", make(chan int)); err == nil {
		t.Fatal("invalid params")
	}
	if err = conn.Notify("initialized"); err != nil {
		t.Fatal(err)
	}
	conn.Close()
	if err = conn.Notify("initialized"); err == nil {
		t.Fatal("closed notification")
	}
	if _, err = conn.Call("thread/read", struct{}{}); err == nil {
		t.Fatal("closed call")
	}
}
