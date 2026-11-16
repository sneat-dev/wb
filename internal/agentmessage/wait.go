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
	dir, err := s.directory(agent)
	if err != nil {
		return nil, err
	}
	_ = dir.Close()
	path := filepath.Join(s.Root, agent, "wait.sock")
	lock := flock.New(filepath.Join(s.Root, agent, "wait.lock"))
	locked, err := lock.TryLock()
	if err != nil {
		return nil, err
	}
	if !locked {
		return nil, fmt.Errorf("another receiver is already waiting for agent %s", agent)
	}
	defer func() { _ = lock.Unlock() }()
	if err = os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	conn, err := net.ListenUnixgram("unixgram", &net.UnixAddr{Name: path, Net: "unixgram"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = conn.Close(); _ = os.Remove(path) }()
	return s.waitOn(ctx, agent, wait, conn)
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
