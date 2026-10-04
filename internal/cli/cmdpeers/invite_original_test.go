package cmdpeers

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/sneat-dev/wb/internal/peersrun"
)

var errAtWrite = errors.New("write refused")

type failAtCallWriter struct{ failAt, calls int }

func (w *failAtCallWriter) Write(p []byte) (int, error) {
	w.calls++
	if w.calls == w.failAt {
		return 0, errAtWrite
	}
	return len(p), nil
}
func TestPeersInviteReportsAWriteFailureAfterARescuedToken(t *testing.T) {
	t.Parallel()
	deps := Dependencies{Invite: func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
		return peersrun.InviteResult{Output: peersrun.InviteOutput{Name: "laptop", Token: "secret", Rotated: true}, AttemptedTokenFile: "/absolute/token", TokenWriteError: errors.New("write failed")}, nil
	}}
	for callIndex := 1; callIndex <= 3; callIndex++ {
		t.Run(fmt.Sprintf("call-%d", callIndex), func(t *testing.T) {
			t.Parallel()
			out := &failAtCallWriter{failAt: callIndex}
			err := runInvite(context.Background(), deps, "root", "laptop", true, "relative-token", false, out)
			if !errors.Is(err, errAtWrite) {
				t.Fatalf("failed call%d err=%v", callIndex, err)
			}
		})
	}
}
func TestPeersInviteReportsAWriteFailureOnTheSuccessHeaderLine(t *testing.T) {
	t.Parallel()
	deps := Dependencies{Invite: func(context.Context, peersrun.InviteRequest) (peersrun.InviteResult, error) {
		return peersrun.InviteResult{Output: peersrun.InviteOutput{Name: "laptop", Token: "secret", Rotated: true}, AttemptedTokenFile: "/absolute/token", TokenWriteError: nil}, nil
	}}
	out := &failAtCallWriter{failAt: 1}
	err := runInvite(context.Background(), deps, "root", "laptop", true, "", false, out)
	if !errors.Is(err, errAtWrite) {
		t.Fatalf("error=%v", err)
	}
}
