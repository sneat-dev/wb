package deps

import (
	"context"
	"testing"

	"github.com/sneat-dev/wb/internal/progress"
)

func TestBuildGraphReportsRepositoryDiscoveryProgress(t *testing.T) {
	var events []progress.Event
	_, err := BuildGraph(context.Background(), []Repository{{Slug: "example/archived", Archived: true}}, GraphOptions{
		Ecosystem: EcosystemGo,
		GitHubDir: t.TempDir(),
		Parallel:  1,
		Progress:  func(event progress.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].State != progress.Started || events[1].Repository != "example/archived" || events[1].Completed != 1 || events[1].Total != 1 {
		t.Fatalf("unexpected progress events: %#v", events)
	}
}

func TestAnalyzeDriftReportsRepositoryProgress(t *testing.T) {
	var events []progress.Event
	_, err := AnalyzeDrift(context.Background(), []Repository{{Slug: "example/missing"}}, DriftOptions{
		Parallel: 1,
		Progress: func(event progress.Event) { events = append(events, event) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[0].State != progress.Started || events[1].Phase != "inspect" || events[1].Completed != 1 {
		t.Fatalf("unexpected progress events: %#v", events)
	}
}
