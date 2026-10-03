package landingcontext

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sneat-dev/wb/internal/streams"
)

// An operator holds a pull request in whichever form their source gave them:
// what they typed, what a report printed, or what they copied from a browser.
// Every one of them addresses the same pull request, and making the caller
// normalize it is how a URL ends up inside an API path.
func TestPRLandFleetEventLogDoesNotMakeTheNextLandingGuardFailClosed(t *testing.T) {
	root := t.TempDir()
	t.Setenv("WB_PROJECTS_ROOT", root)
	home := filepath.Join(root, ".wb")

	log, streamName := Events(root, "acme/app")
	if streamName != "" {
		t.Fatalf("stream name = %q, want an outside-stream landing", streamName)
	}
	if err := log.Append(streams.Event{Verb: "wb pr land", Outcome: "refused"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(home, "streams", FleetEventLogName, "events.jsonl")); err != nil {
		t.Fatalf("fleet event log was not appended: %v", err)
	}

	if err := CheckRepository(root, "acme/app"); err != nil {
		t.Fatalf("next landing guard rejected only the fleet event log: %v", err)
	}
}
