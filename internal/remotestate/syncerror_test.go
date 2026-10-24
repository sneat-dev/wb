package remotestate

import (
	"errors"
	"strings"
	"testing"
)

func TestSyncErrorNamesTheStoreAndRemedyAndUnwraps(t *testing.T) {
	t.Parallel()
	cause := errors.New("rebase refused")
	err := error(&SyncError{Store: "/p/team/state", Remedy: "do the thing", Err: cause})
	for _, want := range []string{"/p/team/state", "not syncing", "rebase refused", "do the thing"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("Error() = %q, want it to mention %q", err, want)
		}
	}
	if !errors.Is(err, cause) {
		t.Fatal("SyncError does not unwrap to its cause")
	}
}
