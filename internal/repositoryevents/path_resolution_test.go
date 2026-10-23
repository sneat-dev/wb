package repositoryevents

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/api/githubapp/repositoryevent"
)

func TestProcessRejectsMissingProjectsRoot(t *testing.T) {
	t.Parallel()
	for _, renamed := range []bool{false, true} {
		event := receiverEvent("event-1")
		if renamed {
			event.Reason = repositoryevent.ReasonRepositoryRenamed
			event.PreviousRepository = "github.com/acme/previous"
		}
		if _, err := (SyncProcessor{}).Process(context.Background(), event, ProcessState{}); err == nil {
			t.Fatal("accepted empty projects root")
		}
	}
}

func TestProcessReportsDestinationResolutionFailureAfterOldCheckoutDisappears(t *testing.T) {
	t.Parallel()
	event := receiverEvent("event-1")
	event.Reason = repositoryevent.ReasonRepositoryRenamed
	event.PreviousRepository = "github.com/acme/previous"
	failure := errors.New("destination root unavailable")
	old := filepath.Join(t.TempDir(), "missing-old-checkout")
	calls := 0
	_, err := (SyncProcessor{}).processResolved(context.Background(), event, ProcessState{}, func(_, repository string) (string, error) {
		calls++
		if calls == 1 && repository == "acme/previous" {
			return old, nil
		}
		return "", failure
	})
	if !errors.Is(err, failure) || calls != 2 {
		t.Fatalf("resolution = %v, calls = %d", err, calls)
	}
}
