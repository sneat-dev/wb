package agentmessage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/gofrs/flock"
	"github.com/sneat-dev/wb/internal/sessionmove"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
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
	if _, e := pendingIn(dir, m.To, 100); e == nil {
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
			if mode == "long-path" {
				if _, e := s.Receive(ctx, "mac", time.Second); e == nil || !strings.Contains(e.Error(), "103 bytes") {
					t.Fatal(e)
				}
				if _, e := os.Stat(filepath.Join(s.Root, "mac", "wait.lock")); !errors.Is(e, os.ErrNotExist) {
					t.Fatal("length refusal created lock")
				}
				if _, e := s.receiveWait(ctx, "mac", time.Second, filepath.Join(s.Root, "mac", "wait.sock")); e == nil {
					t.Fatal("native long socket accepted")
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

func TestSendDiagnosesProcessPhaseWithoutLeakingRemoteContent(t *testing.T) {
	t.Parallel()
	for _, code := range []int{1, 2, 127, 255} {
		t.Run(fmt.Sprint(code), func(t *testing.T) {
			t.Parallel()
			processErr := fakeExitStatus(code)
			ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error { return processErr }}
			_, err := ssh.Send(context.Background(), sampleMessage())
			if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("status %d", code)) {
				t.Fatal(err)
			}
			if code != 255 && strings.Contains(err.Error(), "retrying the same --id") {
				t.Fatal("permanent rejection suggests blind retry")
			}
		})
	}
	for _, wake := range []string{"rejected", "ambiguous"} {
		t.Run(wake, func(t *testing.T) {
			t.Parallel()
			m := sampleMessage()
			ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(_ context.Context, _ string, _ []string, _ io.Reader, out, diagnostic io.Writer) error {
				_, _ = io.WriteString(diagnostic, "private message content")
				_ = json.NewEncoder(out).Encode(Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: wake})
				return errors.New("private message content")
			}}
			receipt, err := ssh.Send(context.Background(), m)
			if err == nil || receipt.Status != "durable" || !strings.Contains(err.Error(), wake) || strings.Contains(err.Error(), "private message content") {
				t.Fatal(receipt, err)
			}
		})
	}
}

func TestLongAgentNameStillSupportsNonWaitingInbox(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	agent := strings.Repeat("a", 64)
	store.Root = filepath.Join(store.Root, strings.Repeat("r", 60))
	if _, err := store.Receive(context.Background(), agent, time.Second); err == nil || !strings.Contains(err.Error(), "103 bytes") {
		t.Fatal(err)
	}
	if _, err := os.Stat(store.Root); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("path preflight mutated store")
	}
	if messages, err := store.Receive(context.Background(), agent, 0); err != nil || len(messages) != 0 {
		t.Fatal(messages, err)
	}
}

func TestReceiveWaitRejectsUnavailableStore(t *testing.T) {
	t.Parallel()
	if _, err := (Store{Root: "relative"}).Receive(context.Background(), "mac", time.Second); err == nil {
		t.Fatal("invalid store accepted")
	}
}

func TestOutputBoundCannotBeBypassedByIOOptimizations(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"string", "copy"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			var output boundedOutput
			output.limit = 8
			var err error
			if mode == "string" {
				_, err = io.WriteString(&output, "0123456789")
			} else {
				_, err = io.Copy(&output, struct{ io.Reader }{strings.NewReader("0123456789")})
			}
			if err == nil || len(output.Bytes()) > 8 {
				t.Fatal(err, len(output.Bytes()))
			}
		})
	}
}

func TestDurableReceiptAndAckRequireDirectorySyncOnEveryRetry(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	m := sampleMessage()
	mustDeliver(t, store, m)
	calls := 0
	store.syncDirectory = func(*os.File) error { calls++; return errors.New("directory sync failed") }
	receipt, err := store.Deliver(context.Background(), m)
	if err == nil || receipt.Status == "durable" || calls != 1 {
		t.Fatal(receipt, err, calls)
	}
	if err = store.Ack(m.To, m.ID); err == nil || calls != 2 {
		t.Fatal(err, calls)
	}
	if err = store.Ack(m.To, m.ID); err == nil || calls != 3 {
		t.Fatal("idempotent ack omitted sync", err, calls)
	}
	store.syncDirectory = nil
	if err = store.Ack(m.To, m.ID); err != nil {
		t.Fatal(err)
	}
}

func TestPrivateDirectoryCreationRepairsParentDurabilityOnRetry(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	syncs := 0
	syncErr := errors.New("parent sync failed")
	sync := func(int) error { syncs++; return syncErr }
	if err = mkdirPrivate(int(dir.Fd()), "child", unix.Mkdirat, sync); !errors.Is(err, syncErr) {
		t.Fatal(err)
	}
	if err = mkdirPrivate(int(dir.Fd()), "child", unix.Mkdirat, sync); !errors.Is(err, syncErr) || syncs != 2 {
		t.Fatal(err, syncs)
	}
	if err = mkdirPrivate(-1, "child", unix.Mkdirat, sync); err == nil || syncs != 2 {
		t.Fatal(err, syncs)
	}
}

func TestAckPublicationFailureNeverReportsSuccess(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	m := sampleMessage()
	mustDeliver(t, store, m)
	if err := os.Mkdir(filepath.Join(store.Root, m.To, m.ID+".ack"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := store.Ack(m.To, m.ID); err == nil {
		t.Fatal("ack directory collision accepted")
	}
}

func TestExistingAncestorsTolerateOnlyUnsupportedDirectorySync(t *testing.T) {
	t.Parallel()
	for _, syncErr := range []error{syscall.EINVAL, syscall.ENOTSUP, syscall.EROFS, syscall.EIO} {
		t.Run(syncErr.Error(), func(t *testing.T) {
			t.Parallel()
			sync := func(int) error { return fmt.Errorf("parent sync: %w", syncErr) }
			existing := mkdirPrivate(1, "existing", func(int, string, uint32) error { return unix.EEXIST }, sync)
			if errors.Is(syncErr, syscall.EIO) {
				if !errors.Is(existing, syncErr) {
					t.Fatal(existing)
				}
			} else if !errors.Is(existing, unix.EEXIST) {
				t.Fatal(existing)
			}
			created := mkdirPrivate(1, "new", func(int, string, uint32) error { return nil }, sync)
			if !errors.Is(created, syncErr) {
				t.Fatal("new directory accepted unsynced parent", created)
			}
		})
	}
}

type fakeExitStatus int

func (status fakeExitStatus) Error() string { return "process failed" }
func (status fakeExitStatus) ExitCode() int { return int(status) }
