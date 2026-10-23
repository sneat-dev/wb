package daemonhost

import (
	"context"
	"testing"
	"time"

	"github.com/sneat-dev/wb/hub/redeliver"
)

func TestRedeliveryStartUsesActualRunAndStopsItsPrivateSleep(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	started, stopped := make(chan struct{}), make(chan struct{})
	// This is an operation-dispatch test: actual Run has no configured store/API,
	// while the per-instance existing Sleep seam proves cancellation custody.
	sweeper := redeliver.New(redeliver.Options{Sleep: func(ctx context.Context, _ time.Duration) error {
		close(started)
		<-ctx.Done()
		close(stopped)
		return ctx.Err()
	}})
	mount := &hubMount{Webhook: &webhookMode{sweeper: sweeper}}
	t.Cleanup(func() {
		cancel()
		select {
		case <-stopped:
		case <-time.After(time.Second):
			t.Error("private sweeper sleep did not stop")
		}
	})
	mount.startRedeliverySweep(ctx)
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("actual sweep Run was not dispatched")
	}
	cancel()
	select {
	case <-stopped:
	case <-time.After(time.Second):
		t.Fatal("private sweep sleep did not observe cancellation")
	}
}
