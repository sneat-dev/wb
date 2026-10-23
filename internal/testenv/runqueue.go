package testenv

import (
	"github.com/sneat-dev/wb/internal/runqueue"
	"testing"
	"time"
)

// WaitForQueued anchors holder-release timing to an actual live waiting ticket.
func WaitForQueued(t *testing.T, root string, budget int) {
	t.Helper()
	waitForQueued(t, root, budget, time.Now, time.Sleep)
}

func waitForQueued(t testing.TB, root string, budget int, now func() time.Time, sleep func(time.Duration)) {
	t.Helper()
	deadline := now().Add(30 * time.Second)
	for {
		if runqueue.Peek(root, budget).Total > 0 {
			return
		}
		if now().After(deadline) {
			t.Errorf("no waiter registered on the queue within %s; the queued path cannot be observed", 30*time.Second)
			return
		}
		sleep(time.Millisecond)
	}
}
