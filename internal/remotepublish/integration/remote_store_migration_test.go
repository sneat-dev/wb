package integration

import (
	"context"
	"github.com/sneat-dev/wb/internal/remotepublish"
	"github.com/sneat-dev/wb/internal/remotestate"
	"testing"
)

// A reader whose projects root cannot be resolved reports it instead of
// claiming an empty fleet.
func TestActiveStoreReaderReportsAnUnresolvableStore(t *testing.T) {
	t.Parallel()
	reader := remotepublish.ActiveStoreReader{Config: remotestate.Config{Provider: "git", Repo: "team/wb-state"}}
	if _, err := reader.ReadLocal(context.Background()); err == nil {
		t.Fatal("ReadLocal with no projects root succeeded")
	}
}
