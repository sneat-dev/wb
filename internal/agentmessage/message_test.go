package agentmessage

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func sampleMessage() Message {
	return Message{Version: 1, ID: "0123456789abcdef0123456789abcdef", From: "vm", To: "mac", Body: "Please review.\nReply using wb."}
}
func testStore(t *testing.T) Store {
	t.Helper()
	path, err := os.MkdirTemp("", "wb-msg-")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(path) })
	return Store{Root: filepath.Join(path, "messages")}
}
func mustDeliver(t *testing.T, s Store, m Message) Receipt {
	t.Helper()
	r, e := s.Deliver(context.Background(), m)
	if e != nil {
		t.Fatal(e)
	}
	return r
}

func TestMessagesRemainPendingUntilAcknowledgedAndCannotChangeOnRetry(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	r := mustDeliver(t, s, m)
	if r.ID != m.ID || r.Status != "durable" || r.Wake != "not-bound" {
		t.Fatalf("%+v", r)
	}
	mustDeliver(t, s, m)
	for range 2 {
		messages, e := s.Receive(context.Background(), m.To, 0)
		if e != nil || len(messages) != 1 || messages[0] != m {
			t.Fatalf("%+v %v", messages, e)
		}
	}
	changed := m
	changed.Body = "different"
	if _, e := s.Deliver(context.Background(), changed); e == nil {
		t.Fatal("reused ID changed content")
	}
	if e := s.Ack(m.To, m.ID); e != nil {
		t.Fatal(e)
	}
	if e := s.Ack(m.To, m.ID); e != nil {
		t.Fatal(e)
	}
	r = mustDeliver(t, s, m)
	if r.Wake != "acknowledged" {
		t.Fatal(r)
	}
	messages, e := s.Receive(context.Background(), m.To, 0)
	if e != nil || len(messages) != 0 {
		t.Fatalf("%+v %v", messages, e)
	}
}

func TestDeliverKeepsDurableReceiptWhenNativeWakeFails(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	s.Notify = func(_ context.Context, got Message) (string, error) {
		if got != m {
			t.Fatal(got)
		}
		return "ambiguous", errors.New("connection lost")
	}
	r, e := s.Deliver(context.Background(), m)
	if e == nil || r.Status != "durable" || r.Wake != "ambiguous" {
		t.Fatalf("%+v %v", r, e)
	}
	messages, e := s.Receive(context.Background(), m.To, 0)
	if e != nil || len(messages) != 1 {
		t.Fatalf("%+v %v", messages, e)
	}
}

func TestReceiveWakesImmediatelyAndRecoversStaleSocket(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	dir, e := s.directory(m.To)
	if e != nil {
		t.Fatal(e)
	}
	_ = dir.Close()
	path := filepath.Join(s.Root, m.To, "wait.sock")
	if e = os.WriteFile(path, []byte("stale"), 0600); e != nil {
		t.Fatal(e)
	}
	result := make(chan error, 1)
	go func() {
		messages, e := s.Receive(context.Background(), m.To, 5*time.Second)
		if e == nil && (len(messages) != 1 || messages[0] != m) {
			e = errors.New("wrong message")
		}
		result <- e
	}()
	deadline := time.Now().Add(3 * time.Second)
	for {
		info, e := os.Stat(path)
		if e == nil && info.Mode()&os.ModeSocket != 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener not ready")
		}
		time.Sleep(time.Millisecond)
	}
	mustDeliver(t, s, m)
	select {
	case e := <-result:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("delivery failed to wake receiver")
	}
	if _, e = os.Stat(path); !errors.Is(e, os.ErrNotExist) {
		t.Fatalf("socket not cleaned: %v", e)
	}
}

func TestReceiveTimeoutCancellationAndCompetingWaiter(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	messages, e := s.Receive(context.Background(), "mac", time.Millisecond)
	if e != nil || len(messages) != 0 {
		t.Fatalf("%v %v", messages, e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = s.Receive(ctx, "mac", time.Second); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
	lock := flock.New(filepath.Join(s.Root, "mac", "wait.lock"))
	if e = lock.Lock(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { _ = lock.Unlock() })
	if _, e = s.Receive(context.Background(), "mac", time.Second); e == nil {
		t.Fatal("concurrent waiter accepted")
	}
	for _, wait := range []time.Duration{-1, 25 * time.Hour} {
		if _, e = s.Receive(context.Background(), "mac", wait); e == nil {
			t.Fatal("invalid timeout")
		}
	}
}

func TestMessageValidationAndStrictEnvelope(t *testing.T) {
	t.Parallel()
	m := sampleMessage()
	raw, _ := json.Marshal(m)
	got, e := Decode(strings.NewReader(string(raw)))
	if e != nil || got != m {
		t.Fatalf("%+v %v", got, e)
	}
	for _, body := range []string{"", strings.Repeat("x", MaxBodyBytes+1), string([]byte{255})} {
		bad := m
		bad.Body = body
		if bad.Validate() == nil {
			t.Fatal("invalid body accepted")
		}
	}
	for _, change := range []func(*Message){func(m *Message) { m.Version = 2 }, func(m *Message) { m.ID = "../bad" }, func(m *Message) { m.From = "../bad" }, func(m *Message) { m.To = "../bad" }} {
		bad := m
		change(&bad)
		if bad.Validate() == nil {
			t.Fatal("invalid identity accepted")
		}
	}
	for _, raw := range []string{"{}", "{", string(raw) + " {}", strings.Replace(string(raw), `"version":1`, `"extra":true,"version":1`, 1), strings.Repeat("x", MaxEnvelopeBytes+1), string([]byte{255})} {
		if _, e := Decode(strings.NewReader(raw)); e == nil {
			t.Fatal("invalid envelope accepted")
		}
	}
	if _, e := Decode(errorReader{}); e == nil {
		t.Fatal("read error lost")
	}
	if e := ValidateID(NewID()); e != nil {
		t.Fatal(e)
	}
}

type errorReader struct{}

func (errorReader) Read([]byte) (int, error) { return 0, errors.New("read failed") }

func TestSSHUsesFixedCommandAndStdinAndValidatesReceipt(t *testing.T) {
	t.Parallel()
	m := sampleMessage()
	for _, mode := range []string{"ok", "wake-error", "bad-receipt", "oversize"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := SSH{Config: sessionmove.SSHConfig{Host: "mac", User: "agent", WBPath: "/usr/local/bin/wb"}, Run: func(_ context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
				if name != "ssh" || !strings.Contains(strings.Join(args, " "), "StrictHostKeyChecking=yes") || strings.Contains(strings.Join(args, " "), m.Body) {
					t.Fatal(args)
				}
				decoded, e := Decode(stdin)
				if e != nil || decoded != m {
					t.Fatal(decoded, e)
				}
				if mode == "bad-receipt" {
					_, _ = io.WriteString(stdout, `{}`)
					return nil
				}
				if mode == "oversize" {
					_, e := io.WriteString(stdout, strings.Repeat("x", MaxEnvelopeBytes+1))
					return e
				}
				_ = json.NewEncoder(stdout).Encode(Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: "submitted"})
				if mode == "wake-error" {
					return errors.New("remote failed")
				}
				return nil
			}}
			r, e := s.Send(context.Background(), m)
			if mode == "ok" {
				if e != nil || r.ID != m.ID {
					t.Fatal(r, e)
				}
			} else if e == nil {
				t.Fatal("failure swallowed")
			}
		})
	}
	s := SSH{Config: sessionmove.SSHConfig{Host: "bad;host"}}
	if _, e := s.Send(context.Background(), m); e == nil {
		t.Fatal("host accepted")
	}
	s.Config.Host = "mac"
	bad := m
	bad.Version = 9
	if _, e := s.Send(context.Background(), bad); e == nil {
		t.Fatal("message accepted")
	}
}

func TestInboxRefusesUnsafeDirectoriesAndMalformedEntries(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := sampleMessage()
	for _, mode := range []string{"relative", "public", "symlink", "file", "bad-name", "bad-message", "wrong-id", "wrong-target", "bad-ack", "ack-dir", "oversize", "message-dir"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			m := sampleMessage()
			s := testStore(t)
			if mode == "relative" {
				s.Root = "relative"
			} else {
				dir, e := s.directory(m.To)
				if e != nil {
					t.Fatal(e)
				}
				_ = dir.Close()
			}
			inbox := filepath.Join(s.Root, m.To)
			path := filepath.Join(inbox, m.ID+".json")
			raw, _ := json.Marshal(m)
			switch mode {
			case "public":
				_ = os.Chmod(s.Root, 0755)
			case "symlink":
				_ = os.Remove(inbox)
				_ = os.Symlink(t.TempDir(), inbox)
			case "file":
				_ = os.Remove(inbox)
				_ = os.WriteFile(inbox, []byte("x"), 0600)
			case "bad-name":
				path = filepath.Join(inbox, "bad.json")
				_ = os.WriteFile(path, raw, 0600)
			case "bad-message":
				_ = os.WriteFile(path, []byte("broken"), 0600)
			case "wrong-id":
				m.ID = NewID()
				raw, _ = json.Marshal(m)
				_ = os.WriteFile(path, raw, 0600)
			case "wrong-target":
				m.To = "other"
				raw, _ = json.Marshal(m)
				_ = os.WriteFile(path, raw, 0600)
			case "bad-ack", "ack-dir":
				mustDeliver(t, s, m)
				ack := filepath.Join(inbox, m.ID+".ack")
				if mode == "bad-ack" {
					_ = os.WriteFile(ack, []byte("wrong"), 0600)
				} else {
					_ = os.Mkdir(ack, 0700)
				}
			case "oversize":
				_ = os.WriteFile(path, []byte(strings.Repeat("x", MaxEnvelopeBytes+1)), 0600)
			case "message-dir":
				_ = os.Mkdir(path, 0700)
			}
			if _, e := s.Receive(ctx, "mac", 0); e == nil {
				t.Fatal("unsafe inbox accepted")
			}
		})
	}
	s := testStore(t)
	if _, e := s.Deliver(ctx, Message{}); e == nil {
		t.Fatal("invalid delivery")
	}
	cancelled, cancel := context.WithCancel(ctx)
	cancel()
	if _, e := s.Deliver(cancelled, m); e == nil {
		t.Fatal("cancel ignored")
	}
	if e := s.Ack("mac", "bad"); e == nil {
		t.Fatal("bad ID")
	}
	if e := s.Ack("../x", m.ID); e == nil {
		t.Fatal("bad agent")
	}
	if e := s.Ack("mac", m.ID); e == nil {
		t.Fatal("missing message")
	}
}

func TestWaitNotificationDoesNotRequireOnlineReceiver(t *testing.T) {
	t.Parallel()
	notifyWaiter(filepath.Join(t.TempDir(), "absent.sock"))
}

func TestStorageFailuresNeverAcknowledgeOrNotifyCorruptInbox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	m := sampleMessage()
	for _, mode := range []string{"root-file", "public-agent", "bad-ack", "ack-dir", "wrong-identity"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			m := sampleMessage()
			switch mode {
			case "root-file":
				if e := os.WriteFile(s.Root, []byte("x"), 0600); e != nil {
					t.Fatal(e)
				}
			case "public-agent":
				d, e := s.directory(m.To)
				if e != nil {
					t.Fatal(e)
				}
				_ = d.Close()
				if e = os.Chmod(filepath.Join(s.Root, m.To), 0755); e != nil {
					t.Fatal(e)
				}
			case "bad-ack", "ack-dir":
				mustDeliver(t, s, m)
				p := filepath.Join(s.Root, m.To, m.ID+".ack")
				if mode == "bad-ack" {
					_ = os.WriteFile(p, []byte("wrong"), 0600)
				} else {
					_ = os.Mkdir(p, 0700)
				}
			case "wrong-identity":
				mustDeliver(t, s, m)
				bad := m
				bad.To = "other"
				raw, _ := json.Marshal(bad)
				_ = os.WriteFile(filepath.Join(s.Root, m.To, m.ID+".json"), raw, 0600)
				if e := s.Ack(m.To, m.ID); e == nil {
					t.Fatal("wrong inbox acknowledged")
				}
				return
			}
			if _, e := s.Deliver(ctx, m); e == nil {
				t.Fatal("corruption accepted")
			}
		})
	}
	s := testStore(t)
	dir, e := s.directory(m.To)
	if e != nil {
		t.Fatal(e)
	}
	_ = dir.Close()
	if _, e := pendingIn(dir, m.To); e == nil {
		t.Fatal("closed directory read accepted")
	}
	if _, e := decodeFile(dir); e == nil {
		t.Fatal("closed message read accepted")
	}
	for range 101 {
		next := m
		next.ID = NewID()
		mustDeliver(t, s, next)
	}
	messages, e := s.Receive(ctx, m.To, 0)
	if e != nil || len(messages) != 100 {
		t.Fatal(len(messages), e)
	}
}

func TestWaitRefusesBrokenLockSocketAndClosedConnection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, mode := range []string{"bad-agent", "lock-directory", "socket-directory", "long-path", "closed"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			if mode == "bad-agent" {
				if _, e := s.Receive(ctx, "../bad", time.Second); e == nil {
					t.Fatal("bad inbox")
				}
				return
			}
			if mode == "long-path" {
				s.Root = filepath.Join(t.TempDir(), strings.Repeat("x", 100), "messages")
			}
			dir, e := s.directory("mac")
			if e != nil {
				t.Fatal(e)
			}
			_ = dir.Close()
			switch mode {
			case "lock-directory":
				_ = os.Mkdir(filepath.Join(s.Root, "mac", "wait.lock"), 0700)
			case "socket-directory":
				p := filepath.Join(s.Root, "mac", "wait.sock")
				_ = os.Mkdir(p, 0700)
				_ = os.WriteFile(filepath.Join(p, "child"), []byte("x"), 0600)
			case "closed":
				p := filepath.Join(t.TempDir(), "s")
				conn, e := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: p, Net: "unixgram"})
				if e != nil {
					t.Fatal(e)
				}
				_ = conn.Close()
				if _, e = s.waitOn(ctx, "mac", time.Second, conn); e == nil {
					t.Fatal("closed socket accepted")
				}
				return
			}
			if _, e := s.Receive(ctx, "mac", time.Second); e == nil {
				t.Fatal("broken wait accepted")
			}
		})
	}
}

func TestSSHDefaultRunnerFailsClosedWithoutAReceipt(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e := (SSH{Config: sessionmove.SSHConfig{Host: "not-contacted"}}).Send(ctx, sampleMessage()); e == nil {
		t.Fatal("cancelled SSH accepted")
	}
	var output boundedOutput
	if _, e := output.Write(make([]byte, MaxEnvelopeBytes+1)); e == nil {
		t.Fatal("oversized response accepted")
	}
}
