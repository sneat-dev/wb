package waitrun

import (
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
	"os"
	"testing"
	"time"
)

func TestActualRegistrationReleasesMetadata(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	registry := DefaultRegistry()
	release := registry.RegisterWait(Registration{ProjectsRoot: projects, Kind: "pr", Targets: []Reference{{Selector: "acme/app#9"}}, Until: "changed", Slice: time.Minute})
	home, err := wbhome.EnsureRoot(projects)
	if err != nil {
		t.Fatal(err)
	}
	records, err := waitregistry.List(home, waitregistry.Options{})
	if err != nil || len(records) != 1 || records[0].Stale || records[0].PID != os.Getpid() {
		t.Fatal(records, err)
	}
	release()
	records, err = waitregistry.List(home, waitregistry.Options{})
	if err != nil || len(records) != 0 {
		t.Fatal(records, err)
	}
}
