package agentmessage

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/gofrs/flock"
)

func notifyWaiter(path string) {
	conn, err := net.DialTimeout("unixgram", path, time.Second)
	if err != nil {
		return
	} // Offline recipients recover from the durable inbox.
	defer func() { _ = conn.Close() }()
	_ = conn.SetWriteDeadline(time.Now().Add(time.Second))
	_, _ = conn.Write([]byte{1})
}

// Receive returns up to 100 unacknowledged messages. A held receive completes
// immediately on delivery; the harness must run it asynchronously to be woken by
// its completion. Exactly one waiter may own a given inbox at a time.
func (s Store) Receive(ctx context.Context, agent string, wait time.Duration) ([]Message, error) {
	if wait < 0 || wait > 24*time.Hour {
		return nil, fmt.Errorf("wait must be between zero and 24h")
	}
	if wait == 0 {
		return s.pending(agent)
	}
	if err := ValidateAgent(agent); err != nil {
		return nil, err
	}
	path := filepath.Join(s.Root, agent, "wait.sock")
	if len(path) > 103 {
		return nil, fmt.Errorf("message wait socket path is %d bytes; portable maximum is 103 bytes; shorten the projects root or agent name, or receive without --wait", len(path))
	}
	dir, err := s.directory(agent)
	if err != nil {
		return nil, err
	}
	_ = dir.Close()
	return s.receiveWait(ctx, agent, wait, path)
}

func (s Store) receiveWait(ctx context.Context, agent string, wait time.Duration, path string) ([]Message, error) {
	conn, release, err := s.openWaitSocket(agent, path)
	if err != nil {
		return nil, err
	}
	defer release()
	return s.waitOn(ctx, agent, wait, conn)
}

// openWaitSocket shares exclusive inbox waiter ownership between receiving and
// forwarding. Callers install the listener before scanning for pending work.
func (s Store) openWaitSocket(agent, path string) (*net.UnixConn, func(), error) {
	lock := flock.New(filepath.Join(s.Root, agent, "wait.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, nil, err
	}
	if !locked {
		return nil, nil, fmt.Errorf("another receiver is already waiting for agent %s", agent)
	}
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		_ = lock.Unlock()
		return nil, nil, err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		_ = lock.Unlock()
		return nil, nil, err
	}
	return conn, func() { _ = conn.Close(); _ = os.Remove(path); _ = lock.Unlock() }, nil
}

func (s Store) waitOn(ctx context.Context, agent string, wait time.Duration, conn *net.UnixConn) ([]Message, error) {
	waitCtx, cancel := context.WithTimeout(ctx, wait)
	defer cancel()
	stop := context.AfterFunc(waitCtx, func() { _ = conn.Close() })
	defer stop()
	for {
		messages, readErr := s.pending(agent)
		if readErr != nil || len(messages) > 0 {
			return messages, readErr
		}
		var signal [1]byte
		if _, _, err := conn.ReadFromUnix(signal[:]); err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if waitCtx.Err() != nil {
				return []Message{}, nil
			}
			return nil, err
		}
	}
}
