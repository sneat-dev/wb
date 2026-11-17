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
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

func forwardTransport(t *testing.T, deliver func(Message) (Receipt, error)) SSH {
	t.Helper()
	return SSH{Config: sessionmove.SSHConfig{Host: "fixed-peer", User: "operator", WBPath: "/opt/wb"}, Run: func(_ context.Context, name string, args []string, input io.Reader, stdout, _ io.Writer) error {
		if name != "ssh" || strings.Join(args, " ") != "-T -o BatchMode=yes -o StrictHostKeyChecking=yes -o ConnectTimeout=10 -l operator -- fixed-peer /opt/wb --non-interactive message deliver" {
			t.Fatalf("unexpected transport command: %s %v", name, args)
		}
		m, err := Decode(input)
		if err != nil {
			t.Fatal(err)
		}
		receipt, err := deliver(m)
		if encodeErr := json.NewEncoder(stdout).Encode(receipt); encodeErr != nil {
			t.Fatal(encodeErr)
		}
		return err
	}}
}

func queueForward(t *testing.T, store Store, m Message) {
	t.Helper()
	if _, err := store.Deliver(context.Background(), m); err != nil {
		t.Fatal(err)
	}
}

func TestForwardAcknowledgesOnlyConfirmedDurabilityAndTerminalWake(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"submitted", "not-bound", "acknowledged", "ambiguous", "rejected", "preflight-failed", "preflight-zero-exit", "other-wake-failure", "submitted-error", "unknown-zero-exit", "", "no-receipt", "wrong-id", "wrong-digest", "wrong-status"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			local := testStore(t)
			m := sampleMessage()
			queueForward(t, local, m)
			calls := 0
			transport := forwardTransport(t, func(got Message) (Receipt, error) {
				calls++
				if got != m {
					t.Fatal("immutable envelope changed")
				}
				r := Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: mode}
				switch mode {
				case "no-receipt":
					return Receipt{}, errors.New(m.Body)
				case "wrong-id":
					r.Wake = "submitted"
					r.ID = NewID()
				case "wrong-digest":
					r.Wake = "submitted"
					r.Digest = "wrong"
				case "wrong-status":
					r.Wake = "submitted"
					r.Status = "received"
				case "submitted-error":
					r.Wake = "submitted"
					return r, errors.New(m.Body)
				case "preflight-zero-exit":
					r.Wake = "preflight-failed"
				case "ambiguous", "rejected", "preflight-failed", "other-wake-failure":
					return r, errors.New(m.Body)
				}
				return r, nil
			})
			err := transport.Forward(context.Background(), local, m.From, m.To, true)
			confirmed := mode == "submitted" || mode == "not-bound" || mode == "acknowledged" || mode == "ambiguous" || mode == "rejected"
			if (err == nil) != confirmed || calls != 1 {
				t.Fatalf("calls=%d confirmed=%v err=%v", calls, confirmed, err)
			}
			if err != nil && strings.Contains(err.Error(), m.Body) {
				t.Fatal("message leaked into diagnostic")
			}
			pending, err := local.Receive(context.Background(), m.To, 0)
			if err != nil || (len(pending) == 0) != confirmed {
				t.Fatalf("unexpected pending count=%d err=%v", len(pending), err)
			}
			if confirmed {
				queueForward(t, local, m)
				if err := transport.Forward(context.Background(), local, m.From, m.To, true); err != nil || calls != 1 {
					t.Fatal("acknowledged envelope was sent again", calls, err)
				}
			}
		})
	}
}

func TestForwardRetriesSameRemoteEnvelopeUntilWakePreflightRecovers(t *testing.T) {
	t.Parallel()
	local, remote := testStore(t), testStore(t)
	m := sampleMessage()
	queueForward(t, local, m)
	wakes := 0
	remote.Notify = func(context.Context, Message) (string, error) {
		wakes++
		if wakes == 1 {
			return "preflight-failed", errors.New("offline binding")
		}
		return "submitted", nil
	}
	transport := forwardTransport(t, func(got Message) (Receipt, error) {
		if got != m {
			t.Fatal("retry changed immutable message")
		}
		return remote.Deliver(context.Background(), got)
	})
	if err := transport.Forward(context.Background(), local, m.From, m.To, true); err == nil {
		t.Fatal("preflight failure lost")
	}
	if err := transport.Forward(context.Background(), local, m.From, m.To, true); err != nil {
		t.Fatal(err)
	}
	pending, err := remote.Receive(context.Background(), m.To, 0)
	if err != nil || len(pending) != 1 || pending[0] != m || wakes != 2 {
		t.Fatal("remote durability is not processing acknowledgment", len(pending), wakes, err)
	}
}

func TestForwardPagesPastRetainedFailuresAndAcknowledgedHistory(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	for i := range 205 {
		m := sampleMessage()
		m.ID = fmt.Sprintf("%032x", i)
		queueForward(t, store, m)
		if i < 100 {
			if err := store.Ack(m.To, m.ID); err != nil {
				t.Fatal(err)
			}
		}
	}
	calls := 0
	transport := forwardTransport(t, func(m Message) (Receipt, error) {
		calls++
		wake := "submitted"
		if m.ID < fmt.Sprintf("%032x", 200) {
			wake = "preflight-failed"
		}
		return Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: wake}, nil
	})
	dir, err := store.directory("mac")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	pages := 0
	err = transport.forwardPages(context.Background(), store, "vm", "mac", dir, func(limit int) ([]os.DirEntry, error) {
		if limit != 100 {
			t.Fatal("unbounded directory read")
		}
		pages++
		return dir.ReadDir(limit)
	})
	if err == nil || calls != 105 || pages < 4 {
		t.Fatal("retained entries starved later pages", calls, pages, err)
	}
	for i := 200; i < 205; i++ {
		if _, err := os.Stat(filepath.Join(store.Root, "mac", fmt.Sprintf("%032x.ack", i))); err != nil {
			t.Fatal("later message not acknowledged", err)
		}
	}
}

func TestForwardRejectsUnsafeRouteAndHonorsCancellation(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	transport := SSH{Config: sessionmove.SSHConfig{Host: "fixed-peer"}}
	for _, route := range [][2]string{{"../bad", "mac"}, {"vm", "../bad"}, {"same", "same"}} {
		if err := transport.Forward(context.Background(), store, route[0], route[1], true); err == nil {
			t.Fatal("invalid route accepted")
		}
	}
	if err := (SSH{}).Forward(context.Background(), store, "vm", "mac", true); err == nil {
		t.Fatal("invalid SSH endpoint accepted")
	}
	if err := transport.Forward(context.Background(), Store{Root: "relative"}, "vm", "mac", true); err == nil {
		t.Fatal("unsafe inbox accepted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.Forward(ctx, store, "vm", "mac", true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	m := sampleMessage()
	queueForward(t, store, m)
	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	transport = forwardTransport(t, func(m Message) (Receipt, error) {
		cancel()
		return Receipt{ID: m.ID, Digest: m.Digest(), Status: "durable", Wake: "submitted"}, nil
	})
	if err := transport.Forward(ctx, store, m.From, m.To, true); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, m.To, m.ID+".ack")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancellation acknowledged queued envelope", err)
	}
}

func TestForwardPreservesMalformedMismatchedAndUnreadableEntries(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"filename", "json", "id", "to", "from", "ack-digest", "ack-directory"} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			store := testStore(t)
			m := sampleMessage()
			queueForward(t, store, m)
			name := m.ID + ".json"
			switch mode {
			case "filename":
				name = "private-diagnostic.json"
			case "id":
				m.ID = NewID()
			case "to":
				m.To = "other"
			case "from":
				m.From = "other"
			}
			raw, _ := json.Marshal(m)
			if mode == "json" {
				raw = []byte(`{"private-diagnostic":`)
			}
			path := filepath.Join(store.Root, "mac", name)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			ackPath := filepath.Join(store.Root, "mac", sampleMessage().ID+".ack")
			if mode == "ack-digest" {
				if err := os.WriteFile(ackPath, []byte("private-diagnostic"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if mode == "ack-directory" {
				if err := os.Mkdir(ackPath, 0700); err != nil {
					t.Fatal(err)
				}
			}
			transport := forwardTransport(t, func(Message) (Receipt, error) {
				// The invalid-name fixture also retains the original valid file.
				if mode != "filename" {
					t.Fatal("invalid entry reached transport")
				}
				return Receipt{}, errors.New("offline")
			})
			err := transport.Forward(context.Background(), store, "vm", "mac", true)
			if err == nil || strings.Contains(err.Error(), "private-diagnostic") {
				t.Fatal("missing or unsafe diagnostic", err)
			}
		})
	}
}

func TestForwardLocalAckFailureReplaysWithoutLosingRemoteMessage(t *testing.T) {
	t.Parallel()
	local, remote := testStore(t), testStore(t)
	m := sampleMessage()
	queueForward(t, local, m)
	ackPath := filepath.Join(local.Root, m.To, m.ID+".ack")
	calls := 0
	transport := forwardTransport(t, func(got Message) (Receipt, error) {
		calls++
		if calls == 1 {
			if err := os.Mkdir(ackPath, 0700); err != nil {
				t.Fatal(err)
			}
		}
		return remote.Deliver(context.Background(), got)
	})
	if err := transport.Forward(context.Background(), local, m.From, m.To, true); err == nil {
		t.Fatal("local ACK failure lost")
	}
	if err := os.Remove(ackPath); err != nil {
		t.Fatal(err)
	}
	if err := transport.Forward(context.Background(), local, m.From, m.To, true); err != nil || calls != 2 {
		t.Fatal(calls, err)
	}
	got, err := remote.Receive(context.Background(), m.To, 0)
	if err != nil || len(got) != 1 || got[0] != m {
		t.Fatal("remote replay did not deduplicate", len(got), err)
	}
}

func TestForwardContinuousRetryAndIdleCancellation(t *testing.T) {
	t.Parallel()
	for _, queued := range []bool{true, false} {
		t.Run(fmt.Sprint(queued), func(t *testing.T) {
			t.Parallel()
			store := testStore(t)
			m := sampleMessage()
			if queued {
				queueForward(t, store, m)
			}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			calls, reports := 0, 0
			transport := forwardTransport(t, func(Message) (Receipt, error) {
				calls++
				if calls == 2 {
					cancel()
				}
				return Receipt{}, errors.New("private body")
			})
			transport.ReportRetry = func(err error) {
				reports++
				if strings.Contains(err.Error(), "private body") {
					t.Fatal("retry diagnostic leaked transport content")
				}
			}
			if !queued {
				queueForward(t, store, m)
				transport.ReportRetry = func(error) { cancel() }
			}
			if err := transport.Forward(ctx, store, m.From, m.To, false); !errors.Is(err, context.Canceled) {
				t.Fatal(err)
			}
			if queued && (calls != 2 || reports != 1) {
				t.Fatal(calls, reports)
			}
		})
	}
}

func TestForwardIdleEnqueueWakesAndExcludesCompetingWaiter(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	m := sampleMessage()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	sent := make(chan Message, 1)
	done := make(chan error, 1)
	transport := forwardTransport(t, func(got Message) (Receipt, error) {
		sent <- got
		return Receipt{ID: got.ID, Digest: got.Digest(), Status: "durable", Wake: "submitted"}, nil
	})
	go func() { done <- transport.Forward(ctx, store, m.From, m.To, false) }()
	path := filepath.Join(store.Root, m.To, "wait.sock")
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forward listener not ready")
		}
		time.Sleep(time.Millisecond)
	}
	if _, err := store.Receive(ctx, m.To, time.Second); err == nil {
		t.Fatal("competing receiver acquired forwarder's inbox")
	}
	if err := transport.Forward(ctx, store, m.From, m.To, false); err == nil {
		t.Fatal("competing forwarder acquired inbox")
	}
	// Allow the initial empty scan to settle; delivery must wake an idle listener
	// within this sub-second budget, not wait for the two-second failure backoff.
	time.Sleep(20 * time.Millisecond)
	queueForward(t, store, m)
	select {
	case got := <-sent:
		if got != m {
			t.Fatal("forwarded a different message")
		}
	case <-time.After(time.Second):
		t.Fatal("enqueue did not wake idle forwarder promptly")
	}
	// Wait for ACK before cancellation so this also verifies an idle, clean
	// forwarder can be cancelled while blocked waiting for its next datagram.
	for {
		if _, err := os.Stat(filepath.Join(store.Root, m.To, m.ID+".ack")); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("forwarder did not acknowledge durable delivery")
		}
		time.Sleep(time.Millisecond)
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("listener socket not released", err)
	}
}

func TestForwardListenerErrorsAndFailureBackoff(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	transport := SSH{Config: sessionmove.SSHConfig{Host: "fixed-peer"}}
	if err := transport.Forward(context.Background(), Store{Root: "/" + strings.Repeat("x", 110)}, "vm", "mac", false); err == nil {
		t.Fatal("overlong socket path accepted")
	}
	if err := transport.Forward(context.Background(), Store{Root: "relative"}, "vm", "mac", false); err == nil {
		t.Fatal("relative inbox accepted")
	}
	dir, err := store.directory("mac")
	if err != nil {
		t.Fatal(err)
	}
	_ = dir.Close()
	conn, release, err := store.openWaitSocket("mac", filepath.Join(store.Root, "mac", "wait.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	_ = conn.Close()
	if err := transport.forwardListen(context.Background(), store, "vm", "mac", conn, forwardRescanInterval); err == nil {
		t.Fatal("closed wake listener ignored")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.forwardListen(ctx, store, "vm", "mac", conn, forwardRescanInterval); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	release()
	m := sampleMessage()
	queueForward(t, store, m)
	calls := 0
	transport = forwardTransport(t, func(Message) (Receipt, error) {
		calls++
		return Receipt{}, errors.New("offline")
	})
	ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
	t.Cleanup(cancel)
	if err := transport.Forward(ctx, store, m.From, m.To, false); !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
		t.Fatal("failed backlog spun instead of backing off", calls, err)
	}
}

func TestForwardEmptyInboxCancellationReleasesListener(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	t.Cleanup(cancel)
	transport := forwardTransport(t, func(Message) (Receipt, error) {
		t.Error("empty inbox invoked transport")
		return Receipt{}, errors.New("unexpected transport")
	})
	if err := transport.Forward(ctx, store, "vm", "mac", false); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(store.Root, "mac", "wait.sock")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled idle listener retained its socket", err)
	}
}

type forwardTestWaiter struct {
	deadline func(time.Time) error
	read     func([]byte) (int, *net.UnixAddr, error)
}

func (w forwardTestWaiter) SetReadDeadline(deadline time.Time) error { return w.deadline(deadline) }
func (w forwardTestWaiter) ReadFromUnix(p []byte) (int, *net.UnixAddr, error) {
	return w.read(p)
}

func TestForwardRecoversDurableEnqueueWithoutNotification(t *testing.T) {
	t.Parallel()
	local, remote := testStore(t), testStore(t)
	m := sampleMessage()
	dir, err := local.directory(m.To)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	conn, release, err := local.openWaitSocket(m.To, filepath.Join(local.Root, m.To, "wait.sock"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(release)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	t.Cleanup(cancel)
	stop := context.AfterFunc(ctx, func() { _ = conn.Close() })
	t.Cleanup(func() { stop() })
	calls, waits := 0, 0
	transport := forwardTransport(t, func(got Message) (Receipt, error) {
		calls++
		if got != m {
			t.Fatal("fallback changed immutable envelope")
		}
		return remote.Deliver(ctx, got)
	})
	transport.ReportRetry = func(error) { t.Error("idle rescan reported a transport failure") }
	waiter := forwardTestWaiter{
		deadline: func(deadline time.Time) error {
			waits++
			if waits == 1 {
				// The initial empty scan has finished. Reproduce publication
				// followed by a sender crash before notifyWaiter: use the same
				// durable file primitive as Deliver, but deliberately send no hint.
				if err := filewrite.WriteJSONImmutableAt(dir, m.ID+".json", m, true, nil); err != nil {
					t.Fatal(err)
				}
				if err := local.sync(dir); err != nil {
					t.Fatal(err)
				}
			}
			return conn.SetReadDeadline(deadline)
		},
		read: func(p []byte) (int, *net.UnixAddr, error) {
			if waits == 3 {
				// Two genuine socket timeouts have caused recovery followed by
				// another scan of the ACKed message. Neither required a signal.
				cancel()
				_ = conn.Close()
			}
			return conn.ReadFromUnix(p)
		},
	}
	if err := transport.forwardListen(ctx, local, m.From, m.To, waiter, time.Millisecond); !errors.Is(err, context.Canceled) {
		t.Fatal("missed notification was not recovered before test deadline", err)
	}
	if calls != 1 || waits != 3 {
		t.Fatal("fallback duplicated delivery or did not rescan", calls, waits)
	}
	pending, err := local.Receive(context.Background(), m.To, 0)
	if err != nil || len(pending) != 0 {
		t.Fatal("durable transfer not acknowledged locally", len(pending), err)
	}
	pending, err = remote.Receive(context.Background(), m.To, 0)
	if err != nil || len(pending) != 1 || pending[0] != m {
		t.Fatal("remote processing receipt conflated with transfer", len(pending), err)
	}
}

func TestForwardReadFailureRemainsAnErrorWithRecoveryDeadline(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	readCalled := false
	waiter := forwardTestWaiter{
		deadline: func(time.Time) error { return nil },
		read: func([]byte) (int, *net.UnixAddr, error) {
			readCalled = true
			return 0, nil, errors.New("private socket diagnostic")
		},
	}
	err := (SSH{}).forwardListen(context.Background(), store, "vm", "mac", waiter, time.Minute)
	if !readCalled || err == nil || strings.Contains(err.Error(), "private socket diagnostic") {
		t.Fatal("non-timeout failure was hidden or disclosed", readCalled, err)
	}
}

func TestForwardPageReadFailureAndCancellationPreserveQueue(t *testing.T) {
	t.Parallel()
	store := testStore(t)
	m := sampleMessage()
	queueForward(t, store, m)
	dir, err := store.directory(m.To)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = dir.Close() })
	transport := SSH{}
	readFailure := func(int) ([]os.DirEntry, error) { return nil, errors.New(m.Body) }
	if err := transport.forwardPages(context.Background(), store, m.From, m.To, dir, readFailure); err == nil || strings.Contains(err.Error(), m.Body) {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := transport.forwardPages(ctx, store, m.From, m.To, dir, readFailure); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	ctx, cancel = context.WithCancel(context.Background())
	t.Cleanup(cancel)
	entries, err := dir.ReadDir(100)
	if err != nil {
		t.Fatal(err)
	}
	if err := transport.forwardPages(ctx, store, m.From, m.To, dir, func(int) ([]os.DirEntry, error) {
		cancel()
		return entries, io.EOF
	}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
