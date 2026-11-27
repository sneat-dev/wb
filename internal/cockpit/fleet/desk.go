package fleet

import (
	"context"
	"slices"

	"github.com/sneat-dev/wb/api/desk"
)

// DeskSource is what the Desk publisher reads from the snapshotter: this
// machine's export and its throughput block, which the export leaves out
// (desk-connect#req:publish-on-change).
type DeskSource interface {
	Export(metricsOnly bool) Envelope
	LocalThroughput() *Throughput
}

// DeskPublisher publishes this machine's state to Sneat Dev Desk after a
// successful pass. Its Diagnostic is a desk.ErrorCode or empty; any other text
// is never shown.
type DeskPublisher interface {
	Publish(ctx context.Context, source DeskSource)
	Diagnostic() string
}

// LocalThroughput is this machine's throughput block, nil until one is known.
func (s *Snapshotter) LocalThroughput() *Throughput {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.throughput
}

func deskErrorCode(diagnostic string) string {
	if slices.Contains(desk.ErrorCodes(), desk.ErrorCode(diagnostic)) {
		return diagnostic
	}
	return ""
}

// startDeskPublish hands the pass to the Desk publisher off the pass, one
// hand-off at a time, and shows its diagnostic on this machine's entry
// (desk-connect#req:revocation-is-respected).
func (s *Snapshotter) startDeskPublish(ctx context.Context) {
	if s.desk == nil || !s.deskBusy.CompareAndSwap(false, true) {
		return
	}
	s.side.Add(1)
	go func() {
		defer s.side.Done()
		defer s.deskBusy.Store(false)
		_ = catch(func() error { s.desk.Publish(ctx, s); return nil })
		diagnostic := deskErrorCode(s.desk.Diagnostic())
		s.mu.Lock()
		defer s.mu.Unlock()
		if diagnostic != s.deskDiag {
			s.deskDiag = diagnostic
			s.publishMaybeLocked()
		}
	}()
}
