package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"
)

func socketFixtureClient(t *testing.T, respond func(map[string]any) string) *Client {
	t.Helper()
	return &Client{socketPath: "/tmp/wb-owned-fixture.sock", timeout: time.Second, socketDial: func(ctx context.Context, _ string) (net.Conn, error) {
		client, server := net.Pipe()
		go func() {
			defer func() { _ = server.Close() }()
			_ = server.SetDeadline(time.Now().Add(2 * time.Second))
			raw, err := bufio.NewReader(server).ReadBytes('\n')
			if err != nil {
				return
			}
			var request map[string]any
			if json.Unmarshal(raw, &request) != nil {
				return
			}
			reply := respond(request)
			if reply != "" {
				_, _ = io.WriteString(server, reply)
			}
		}()
		return client, nil
	}}
}

func socketSuccessReply(request map[string]any) string {
	id, _ := request["id"].(string)
	if request["method"] == "ping" {
		return `{"id":"` + id + `","result":{"type":"pong","version":"0.9.1","protocol":22}}` + "\n"
	}
	return `{"id":"` + id + `","result":{"type":"agent_prompted","agent":{"pane_id":"w1:p7","workspace_id":"w1","agent":"codex","agent_session":{"agent":"codex","kind":"id","value":"native-owned","source":"fixture"}}}}` + "\n"
}

func TestSocketPromptCarriesLargeExactASCIITextWithoutArgvOrDuplicateSubmit(t *testing.T) {
	t.Parallel()
	text := `{"body":"` + strings.Repeat(`\u0001`, 32<<10) + `x"}`
	var mu sync.Mutex
	submissions := 0
	client := socketFixtureClient(t, func(request map[string]any) string {
		if request["method"] == "agent.prompt" {
			params, ok := request["params"].(map[string]any)
			if !ok || params["target"] != "w1:p7" || params["text"] != text {
				t.Errorf("framed prompt changed canonical text")
			}
			mu.Lock()
			submissions++
			mu.Unlock()
		}
		return socketSuccessReply(request)
	})
	agent, err := client.AgentPromptSocket(context.Background(), "w1:p7", text)
	if err != nil || agent.PaneID != "w1:p7" || agent.HarnessSessionID() != "native-owned" {
		t.Fatalf("large exact socket prompt: %#v %v", agent, err)
	}
	mu.Lock()
	defer mu.Unlock()
	if submissions != 1 {
		t.Fatalf("authorized submissions=%d", submissions)
	}
}

func TestSocketPromptRejectsFramingIdentityAndProtocolErrors(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"wrong-id", "trailing-json", "empty-envelope", "mixed-result-error", "unknown-field", "oversize", "bad-ping-version", "bad-ping-protocol", "bad-prompt-kind", "wrong-prompt-pane", "malformed-agent", "refusal", "invalid-refusal", "closed"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			client := socketFixtureClient(t, func(request map[string]any) string {
				base := socketSuccessReply(request)
				switch kind {
				case "wrong-id":
					return strings.Replace(base, `"id":"`+request["id"].(string)+`"`, `"id":"foreign"`, 1)
				case "trailing-json":
					return strings.TrimSuffix(base, "\n") + " {}\n"
				case "empty-envelope":
					return `{"id":"` + request["id"].(string) + `"}` + "\n"
				case "mixed-result-error":
					return `{"id":"` + request["id"].(string) + `","result":{},"error":{"code":"oops","message":"private"}}` + "\n"
				case "unknown-field":
					return strings.Replace(base, `"result":`, `"unknown":1,"result":`, 1)
				case "oversize":
					return strings.Repeat("x", maxOriginalSocketReplyBytes+1) + "\n"
				case "bad-ping-version":
					return strings.Replace(base, `"version":"0.9.1"`, `"version":"9.9.9"`, 1)
				case "bad-ping-protocol":
					return strings.Replace(base, `"protocol":22`, `"protocol":23`, 1)
				case "bad-prompt-kind":
					if request["method"] == "agent.prompt" {
						return strings.Replace(base, "agent_prompted", "pane_list", 1)
					}
				case "wrong-prompt-pane":
					if request["method"] == "agent.prompt" {
						return strings.Replace(base, "w1:p7", "foreign", 1)
					}
				case "malformed-agent":
					if request["method"] == "agent.prompt" {
						return strings.Replace(base, `"pane_id":"w1:p7"`, `"pane_id":""`, 1)
					}
				case "refusal":
					return `{"id":"` + request["id"].(string) + `","error":{"code":"agent_not_found","message":"DO-NOT-LEAK-PRIVATE-TEXT"}}` + "\n"
				case "invalid-refusal":
					return `{"id":"` + request["id"].(string) + `","error":{"code":"","message":"private"}}` + "\n"
				case "closed":
					return ""
				}
				return base
			})
			_, err := client.AgentPromptSocket(context.Background(), "w1:p7", `{"kind":"text"}`)
			if err == nil || strings.Contains(err.Error(), "DO-NOT-LEAK-PRIVATE-TEXT") {
				t.Fatalf("invalid/private response accepted or leaked: %v", err)
			}
			if kind == "refusal" && !errors.Is(err, ErrUnknownTarget) {
				t.Fatalf("typed refusal lost: %v", err)
			}
		})
	}
}

func TestSocketPromptBoundsRequestAndCancelsBlockedRead(t *testing.T) {
	t.Parallel()
	client := socketFixtureClient(t, socketSuccessReply)
	for _, text := range []string{"", "line\nline", string([]byte{0xff}), strings.Repeat("x", (512<<10)+1)} {
		if _, err := client.AgentPromptSocket(context.Background(), "w1:p7", text); err == nil {
			t.Fatal("invalid prompt accepted")
		}
	}
	if _, err := client.AgentPromptSocket(context.Background(), "bad\ntarget", "text"); err == nil {
		t.Fatal("invalid target accepted")
	}
	if _, err := client.AgentPromptSocket(nil, "w1:p7", "text"); err == nil {
		t.Fatal("nil context accepted")
	}
	client.socketPath = "relative"
	if _, err := client.AgentPromptSocket(context.Background(), "w1:p7", "text"); err == nil {
		t.Fatal("ambient/relative endpoint accepted")
	}
	client.socketPath = "/tmp/owned.sock"
	if _, err := client.callPromptSocket(context.Background(), "fixed", "agent.prompt", map[string]string{"text": strings.Repeat("\\", 512<<10)}); err == nil {
		t.Fatal("oversized framed request accepted")
	}
	if _, err := client.callPromptSocket(context.Background(), "fixed", "agent.prompt", make(chan int)); err == nil {
		t.Fatal("unencodable request accepted")
	}
	client.socketDial = func(context.Context, string) (net.Conn, error) { return nil, errors.New("PRIVATE endpoint") }
	if _, err := client.AgentPromptSocket(context.Background(), "w1:p7", "text"); err == nil || strings.Contains(err.Error(), "PRIVATE") {
		t.Fatalf("connect error leaked: %v", err)
	}
	var server net.Conn
	client.socketDial = func(context.Context, string) (net.Conn, error) {
		left, right := net.Pipe()
		server = right
		go func() { _, _ = io.Copy(io.Discard, right) }()
		return left, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := client.AgentPromptSocket(ctx, "w1:p7", "text")
	if server != nil {
		_ = server.Close()
	}
	if err == nil || !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > time.Second {
		t.Fatalf("blocked read was not bounded/cancelled: %v", err)
	}
}
