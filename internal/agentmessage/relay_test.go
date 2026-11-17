package agentmessage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/sessionmove"
)

func relayRunner(t *testing.T, m Message, mode string, calls *int) CommandRunner {
	t.Helper()
	return func(ctx context.Context, name string, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
		*calls++
		joined := strings.Join(args, " ")
		if name != "ssh" || !strings.Contains(joined, "StrictHostKeyChecking=yes") {
			t.Fatal(args)
		}
		if mode == "transport-error" {
			return errors.New("offline")
		}
		if strings.Contains(joined, "message receive") {
			if !strings.Contains(joined, "--wait 1m") {
				t.Fatal(args)
			}
			switch mode {
			case "malformed":
				_, _ = io.WriteString(stdout, "broken")
			case "wrong-agent":
				m.To = "other"
				_ = json.NewEncoder(stdout).Encode([]Message{m})
			case "bad-message":
				m.Version = 99
				_ = json.NewEncoder(stdout).Encode([]Message{m})
			case "too-many":
				messages := make([]Message, 101)
				_ = json.NewEncoder(stdout).Encode(messages)
			default:
				_ = json.NewEncoder(stdout).Encode([]Message{m})
			}
			return nil
		}
		if !strings.Contains(joined, "message ack "+m.ID+" --agent "+m.To) {
			t.Fatal(args)
		}
		if mode == "ack-error" {
			return errors.New("ack lost")
		}
		if mode == "bad-ack" {
			_, _ = io.WriteString(stdout, `{}`)
		} else {
			_, _ = fmt.Fprintf(stdout, `{"id":%q,"status":"acknowledged"}`, m.ID)
		}
		return nil
	}
}

func TestRelayAcknowledgesOnlyDurableLocalCopies(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"ok", "ambiguous", "rejected", "preflight-failed", "local-failed", "ack-error", "bad-ack", "transport-error", "malformed", "wrong-agent", "bad-message", "too-many"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			m := sampleMessage()
			calls := 0
			switch mode {
			case "ambiguous", "rejected", "preflight-failed":
				s.Notify = func(context.Context, Message) (string, error) { return mode, errors.New("wake failed") }
			case "local-failed":
				s.Root = "relative"
			}
			ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm", User: "agent", WBPath: "/usr/local/bin/wb"}, Run: relayRunner(t, m, mode, &calls)}
			e := ssh.Relay(context.Background(), s, m.To, true)
			if mode == "ok" || mode == "ambiguous" || mode == "rejected" {
				if e != nil || calls != 2 {
					t.Fatal(calls, e)
				}
				messages, e := s.Receive(context.Background(), m.To, 0)
				if e != nil || len(messages) != 1 {
					t.Fatal(messages, e)
				}
			} else if e == nil {
				t.Fatal("failure lost")
			}
			if mode == "local-failed" {
				if calls != 1 {
					t.Fatal("remote acknowledged before safe local delivery")
				}
			}
		})
	}
}

func TestRelayReconnectsAndCancelsBackoff(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	calls := 0
	reports := 0
	ssh := SSH{ReportRetry: func(err error) {
		reports++
		if err == nil {
			t.Fatal("missing retry reason")
		}
	}, Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(_ context.Context, _ string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
		calls++
		if calls == 1 {
			return errors.New("temporary offline")
		}
		cancel()
		_, _ = io.WriteString(stdout, "[]")
		return nil
	}}
	if e := ssh.Relay(ctx, s, m.To, false); !errors.Is(e, context.Canceled) || calls != 2 || reports != 1 {
		t.Fatal(calls, e, reports)
	}
	ctx, cancel = context.WithTimeout(context.Background(), time.Millisecond)
	t.Cleanup(cancel)
	ssh.Run = func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		return errors.New("offline")
	}
	if e := ssh.Relay(ctx, s, m.To, false); !errors.Is(e, context.DeadlineExceeded) {
		t.Fatal(e)
	}
}

func TestRelayRejectsUnsafeAddressesAndCancelledNativeTransport(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	if e := (SSH{}).Relay(context.Background(), s, "mac", true); e == nil {
		t.Fatal("missing host accepted")
	}
	ssh := SSH{Config: sessionmove.SSHConfig{Host: "not-contacted"}}
	if e := ssh.Relay(context.Background(), s, "../bad", true); e == nil {
		t.Fatal("bad agent accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if e := ssh.Relay(ctx, s, "mac", true); e == nil {
		t.Fatal("cancel ignored")
	}
	if e := ssh.relayCall(ctx, []string{"receive", "--agent", "mac", "--wait", "1m"}, io.Discard); e == nil {
		t.Fatal("cancelled SSH call accepted")
	}
}

func TestRelayCopiesWholeBatchDespiteOneFailedWake(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	messages := []Message{sampleMessage(), sampleMessage(), sampleMessage()}
	for i := range messages {
		messages[i].ID = fmt.Sprintf("%032x", i+1)
	}
	store.Notify = func(_ context.Context, m Message) (string, error) {
		if m.ID == messages[0].ID {
			return "preflight-failed", errors.New("stale binding")
		}
		return "submitted", nil
	}
	var acked []string
	ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(_ context.Context, _ string, args []string, _ io.Reader, stdout, stderr io.Writer) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "message receive") {
			return json.NewEncoder(stdout).Encode(messages)
		}
		id := args[len(args)-3]
		acked = append(acked, id)
		local, err := store.Receive(context.Background(), "mac", 0)
		if err != nil || len(local) < 1 {
			t.Fatalf("ack before local persistence: %v %v", local, err)
		}
		_, err = fmt.Fprintf(stdout, `{"id":%q,"status":"acknowledged"}`, id)
		return err
	}}
	err := ssh.Relay(context.Background(), store, "mac", true)
	if err == nil || !strings.Contains(err.Error(), "stale binding") {
		t.Fatal(err)
	}
	local, err := store.Receive(context.Background(), "mac", 0)
	if err != nil || len(local) != 3 {
		t.Fatal(local, err)
	}
	if len(acked) != 3 || acked[0] != messages[0].ID || acked[1] != messages[1].ID || acked[2] != messages[2].ID {
		t.Fatal(acked)
	}
}

func TestRelayTransfersBeyondFirstHundredDespiteStaleBinding(t *testing.T) {
	t.Parallel()
	remote := testStore(t)
	local := testStore(t)
	ctx := context.Background()
	for range 101 {
		m := sampleMessage()
		m.ID = NewID()
		mustDeliver(t, remote, m)
	}
	notifyCalls := 0
	local.Notify = func(context.Context, Message) (string, error) {
		notifyCalls++
		return "preflight-failed", errors.New("stale binding")
	}
	batches := 0
	acked := 0
	ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(_ context.Context, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
		joined := strings.Join(args, " ")
		if strings.Contains(joined, "message receive") {
			batches++
			messages, err := remote.Receive(ctx, "mac", 0)
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(messages)
		}
		id := args[len(args)-3]
		if err := remote.Ack("mac", id); err != nil {
			return err
		}
		acked++
		return json.NewEncoder(out).Encode(map[string]string{"id": id, "status": "acknowledged"})
	}}
	for range 2 {
		if err := ssh.Relay(ctx, local, "mac", true); err == nil {
			t.Fatal("stale wake diagnostic lost")
		}
	}
	remaining, err := remote.Receive(ctx, "mac", 0)
	if err != nil || len(remaining) != 0 || batches != 2 || acked != 101 {
		t.Fatal(len(remaining), batches, acked, err)
	}
	entries, err := os.ReadDir(filepath.Join(local.Root, "mac"))
	if err != nil {
		t.Fatal(err)
	}
	copies := 0
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".json") {
			copies++
		}
	}
	if copies != 101 {
		t.Fatal(copies)
	}
	if notifyCalls != 3 {
		t.Fatalf("got %d wake probes: want one per remote batch and one for the local retry stage", notifyCalls)
	}
}

func TestRelayRecoversWakeLocallyAfterRemoteTransferAck(t *testing.T) {
	t.Parallel()
	local := testStore(t)
	m := sampleMessage()
	attempts := 0
	received := false
	acks := 0
	local.Notify = func(context.Context, Message) (string, error) {
		attempts++
		if attempts == 1 {
			return "preflight-failed", errors.New("offline")
		}
		return "submitted", nil
	}
	ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(_ context.Context, _ string, args []string, _ io.Reader, out, _ io.Writer) error {
		if strings.Contains(strings.Join(args, " "), "message receive") {
			messages := []Message{}
			if !received {
				messages = append(messages, m)
				received = true
			}
			return json.NewEncoder(out).Encode(messages)
		}
		acks++
		return json.NewEncoder(out).Encode(map[string]string{"id": m.ID, "status": "acknowledged"})
	}}
	if err := ssh.Relay(context.Background(), local, "mac", true); err == nil {
		t.Fatal("initial wake failure lost")
	}
	if err := ssh.Relay(context.Background(), local, "mac", true); err != nil {
		t.Fatal(err)
	}
	if attempts != 2 || acks != 1 {
		t.Fatal(attempts, acks)
	}
}

func TestLocalWakeRetryHonorsCancellationAndStorageFailure(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	m := sampleMessage()
	mustDeliver(t, store, m)
	store.Notify = func(context.Context, Message) (string, error) {
		t.Fatal("cancelled wake called adapter")
		return "", nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := store.retryWake(ctx, m.To); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	store.Root = "relative"
	if err := store.retryWake(context.Background(), m.To); err == nil {
		t.Fatal("invalid storage ignored")
	}
	ssh := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
		t.Fatal("cancelled relay called SSH")
		return nil
	}}
	if err := ssh.Relay(ctx, store, "mac", true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

func TestLocalWakeRetryReestablishesDurabilityBeforeNotification(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	m := sampleMessage()
	mustDeliver(t, store, m)
	store.Notify = func(context.Context, Message) (string, error) {
		t.Fatal("wake called before directory sync")
		return "", nil
	}
	store.syncDirectory = func(*os.File) error { return errors.New("sync failed") }
	if err := store.retryWake(context.Background(), m.To); err == nil {
		t.Fatal("failed local durability ignored")
	}
}

func TestWakeRetryReachesBeyondHundredUnacknowledgedFencedMessages(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	for range 101 {
		m := sampleMessage()
		m.ID = NewID()
		mustDeliver(t, store, m)
	}
	firstBatch, err := store.Receive(context.Background(), "mac", 0)
	if err != nil || len(firstBatch) != 100 {
		t.Fatal(len(firstBatch), err)
	}
	fenced := make(map[string]bool, 101)
	for _, m := range firstBatch {
		fenced[m.ID] = true
	}
	calls := 0
	submissions := 0
	store.Notify = func(_ context.Context, m Message) (string, error) {
		calls++
		if !fenced[m.ID] {
			submissions++
			fenced[m.ID] = true
		}
		return "submitted", nil
	}
	if err = store.retryWake(context.Background(), "mac"); err != nil {
		t.Fatal(err)
	}
	if calls != 101 || submissions != 1 || len(fenced) != 101 {
		t.Fatalf("calls=%d submissions=%d fenced=%d", calls, submissions, len(fenced))
	}
	if err = store.retryWake(context.Background(), "mac"); err != nil {
		t.Fatal(err)
	}
	if submissions != 1 {
		t.Fatalf("fenced message resubmitted: %d", submissions)
	}
	visible, err := store.Receive(context.Background(), "mac", 0)
	if err != nil || len(visible) != 100 {
		t.Fatal("receive lost its batch limit", len(visible), err)
	}
}

func TestWakeRetryStopsBeforeReadingLaterBodiesWhenBindingFails(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	for range 3 {
		m := sampleMessage()
		m.ID = NewID()
		mustDeliver(t, s, m)
	}
	dir, err := s.directory("mac")
	if err != nil {
		t.Fatal(err)
	}
	entries, err := dir.ReadDir(-1)
	_ = dir.Close()
	if err != nil {
		t.Fatal(err)
	}
	// The last directory entry is deliberately unreadable as an envelope.
	// A stopped wake pass must never decode it after the first preflight failure.
	if err := os.WriteFile(filepath.Join(s.Root, "mac", entries[len(entries)-1].Name()), []byte("not an envelope"), 0600); err != nil {
		t.Fatal(err)
	}
	failure := errors.New("binding unavailable")
	calls := 0
	s.Notify = func(context.Context, Message) (string, error) { calls++; return "preflight-failed", failure }
	if err := s.retryWake(context.Background(), "mac"); !errors.Is(err, failure) || calls != 1 {
		t.Fatalf("wake recovery eagerly read later bodies: calls=%d err=%v", calls, err)
	}
}

func TestCancelledWakeRetryDoesNotDecodePendingBodies(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	mustDeliver(t, s, m)
	if err := os.WriteFile(filepath.Join(s.Root, m.To, m.ID+".json"), []byte("invalid envelope"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Notify = func(context.Context, Message) (string, error) { t.Fatal("cancelled recovery notified"); return "", nil }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := s.retryWake(ctx, m.To); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled recovery decoded storage first: %v", err)
	}
}

func TestWakeRecoveryReportsCorruptPendingEnvelope(t *testing.T) {
	t.Parallel()
	s := testStore(t)
	m := sampleMessage()
	mustDeliver(t, s, m)
	if err := os.WriteFile(filepath.Join(s.Root, m.To, m.ID+".json"), []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	s.Notify = func(context.Context, Message) (string, error) {
		t.Fatal("corrupt envelope notified")
		return "submitted", nil
	}
	if err := s.retryWake(context.Background(), m.To); err == nil {
		t.Fatal("corrupt inbox accepted")
	}
}

func TestRelayDoesNotAcknowledgeUnrecognizedLocalWakeReceipt(t *testing.T) {
	t.Parallel()
	for _, wake := range []string{"", "private-unknown-outcome"} {
		t.Run(wake, func(t *testing.T) {
			t.Parallel()
			s := testStore(t)
			m := sampleMessage()
			s.Notify = func(context.Context, Message) (string, error) { return wake, nil }
			transport := SSH{Config: sessionmove.SSHConfig{Host: "vm"}, Run: func(context.Context, string, []string, io.Reader, io.Writer, io.Writer) error {
				t.Fatal("invalid receipt acknowledged")
				return nil
			}}
			if _, err := transport.relayMessage(context.Background(), s, m.To, m); err == nil || strings.Contains(err.Error(), "private") {
				t.Fatal("receipt accepted or leaked", err)
			}
			messages, err := s.Receive(context.Background(), m.To, 0)
			if err != nil || len(messages) != 1 {
				t.Fatal("local durable copy lost", messages, err)
			}
		})
	}
}
