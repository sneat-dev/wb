//go:build e2e && windows

package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Microsoft/go-winio"
	"golang.org/x/sys/windows"
)

func TestWindowsHerdrNamedPipeRoundTripAndCancellation(t *testing.T) {
	recorded := filepath.Join(t.TempDir(), "owned-herdr.sock")
	name, err := herdrPipeName(recorded)
	if err != nil {
		t.Fatal(err)
	}
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := winio.ListenPipe(name, &winio.PipeConfig{SecurityDescriptor: "D:P(A;;GA;;;" + user.User.Sid.String() + ")", InputBufferSize: 4096, OutputBufferSize: 4096})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
	body := `{"body":"` + strings.Repeat(`\u0001`, 24<<10) + `x"}`
	captured := make(chan string, 1)
	serverDone := make(chan error, 1)
	go func() {
		for stage := 0; stage < 2; stage++ {
			conn, err := listener.Accept()
			if err != nil {
				serverDone <- err
				return
			}
			_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
			raw, err := bufio.NewReader(conn).ReadBytes('\n')
			if err != nil {
				_ = conn.Close()
				serverDone <- err
				return
			}
			var request map[string]any
			if err := json.Unmarshal(raw, &request); err != nil {
				_ = conn.Close()
				serverDone <- err
				return
			}
			if stage == 1 {
				params, _ := request["params"].(map[string]any)
				text, _ := params["text"].(string)
				captured <- text
			}
			_, err = io.WriteString(conn, socketSuccessReply(request))
			_ = conn.Close()
			if err != nil {
				serverDone <- err
				return
			}
		}
		serverDone <- nil
	}()
	client := &Client{socketPath: recorded, timeout: 5 * time.Second}
	ack, err := client.AgentPromptSocket(context.Background(), "w1:p7", body)
	if err != nil || ack.HarnessSessionID() != "native-owned" {
		t.Fatalf("native pipe prompt: %#v %v", ack, err)
	}
	if got := <-captured; got != body {
		t.Fatal("native named pipe changed exact large text")
	}
	if err := <-serverDone; err != nil {
		t.Fatal(err)
	}
	// A real pipe connection that never replies must unblock when the caller
	// cancels; Close cancels overlapped I/O rather than leaving a blocked reader.
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()
	stalled := make(chan struct{})
	go func() {
		defer close(stalled)
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		_, _ = bufio.NewReader(conn).ReadBytes('\n')
		<-ctx.Done()
	}()
	began := time.Now()
	_, err = client.callPromptSocket(ctx, "owned-cancel", "ping", struct{}{})
	if !errors.Is(err, context.DeadlineExceeded) || time.Since(began) > time.Second {
		t.Fatalf("native pipe read did not cancel: %v", err)
	}
	select {
	case <-stalled:
	case <-time.After(time.Second):
		t.Fatal("owned pipe fixture did not stop")
	}
}
