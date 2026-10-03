package main

import (
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/session"
)

var errAtWrite = errors.New("write failed")

type failAtCallWriter struct {
	calls  int
	failAt int
}

func (w *failAtCallWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAt {
		return 0, errAtWrite
	}
	return len(p), nil
}

func TestRenderSessionsPropagatesAWriteFailure(t *testing.T) {
	t.Parallel()
	rows := []sessionRow{{
		View: session.View{
			Record: session.Record{WBSessionID: "s1", Machine: "mac", PID: 1, StartedAt: time.Now()},
			State:  session.StateLive,
		},
	}}

	t.Run("header", func(t *testing.T) {
		t.Parallel()
		err := renderSessions(&failAtCallWriter{failAt: 1}, rows)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("renderSessions (header) returned %v, want errAtWrite", err)
		}
	})
	t.Run("row", func(t *testing.T) {
		t.Parallel()
		err := renderSessions(&failAtCallWriter{failAt: 2}, rows)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("renderSessions (row) returned %v, want errAtWrite", err)
		}
	})
}
