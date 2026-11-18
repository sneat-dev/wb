package cmdfleet

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/prinventory"
)

func TestFleetPRsReportsInventoryWaitsOnStderr(t *testing.T) {
	t.Parallel()
	deps := fakeDependencies()
	deps.Inventory = func(_ context.Context, options prinventory.Options) prinventory.Report {
		options.Notify("waiting 21s for the search rate limit reset")
		return prinventory.Report{Complete: true}
	}
	command := New(testRuntime(&shared.Flags{}), deps)
	_, stderr, err := execute(t, command, "prs")
	if err != nil || !strings.Contains(stderr, "waiting 21s") {
		t.Fatalf("err = %v stderr = %q", err, stderr)
	}
}
