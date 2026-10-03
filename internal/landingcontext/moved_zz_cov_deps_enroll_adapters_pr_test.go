package landingcontext

import (
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/streams"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestCwDepsLandingEventLogFindsTheOwningStream(t *testing.T) {
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	projectsRoot := t.TempDir()

	// Outside every stream the event still belongs to the fleet log.
	appender, streamName := Events(projectsRoot, "acme/app")
	if streamName != "" || appender == nil {
		t.Fatalf("unstreamed landing = %v, %q", appender, streamName)
	}
	// Inside a stream it belongs to that stream's log.
	store, err := streams.Open(projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.Create(streams.Stream{Name: "cw-stream", CreatedAt: time.Now().UTC(),
		Members: []streams.Member{streams.Member{Repository: "acme/app", PullRequest: 1}}}); err != nil {
		t.Fatalf("create stream: %v", err)
	}
	appender, streamName = Events(projectsRoot, "acme/app")
	if streamName != "cw-stream" || appender == nil {
		t.Fatalf("streamed landing = %v, %q", appender, streamName)
	}
}
