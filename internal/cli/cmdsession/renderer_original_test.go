package cmdsession

import (
	"errors"
	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"testing"
	"time"
)

func TestRenderSessionsPropagatesAWriteFailure(t *testing.T) {
	t.Parallel()
	errAtWrite := errors.New("write failed")
	rows := []sessionrun.Row{{
		View: session.View{
			Record: session.Record{WBSessionID: "s1", Machine: "mac", PID: 1, StartedAt: time.Now()},
			State:  session.StateLive,
		},
	}}

	t.Run("header", func(t *testing.T) {
		t.Parallel()
		err := renderSessions(&boundaryFailWriteNumber{at: 1, err: errAtWrite}, rows)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("renderSessions (header) returned %v, want errAtWrite", err)
		}
	})
	t.Run("row", func(t *testing.T) {
		t.Parallel()
		err := renderSessions(&boundaryFailWriteNumber{at: 2, err: errAtWrite}, rows)
		if !errors.Is(err, errAtWrite) {
			t.Fatalf("renderSessions (row) returned %v, want errAtWrite", err)
		}
	})
}
