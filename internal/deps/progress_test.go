package deps

import (
	"context"
	"testing"
	"time"

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

func TestRunBumpReportsDiscoveryAndPlanningProgress(t *testing.T) {
	var events []progress.Event
	_, err := RunBump(context.Background(), []ReleaseEvent{{
		Dependency: "example.com/provider",
		Version:    "v1.0.0",
		CheckedAt:  time.Now(),
	}}, []Repository{{Slug: "example/archived", Archived: true}}, BumpOptions{
		NoRegistry: true,
		Options: Options{
			GitHubDir: t.TempDir(), DryRun: true, Parallel: 1,
			Progress: func(event progress.Event) { events = append(events, event) },
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) < 3 || events[0].Phase != "discover_graph" || events[0].State != progress.Started {
		t.Fatalf("unexpected progress events: %#v", events)
	}
	foundRepository := false
	foundPlan := false
	for _, event := range events {
		if event.Phase == "discover_graph" && event.Repository == "example/archived" && event.Completed == 1 && event.Total == 1 {
			foundRepository = true
		}
		if event.Phase == "plan_wave" && event.State == progress.Running && event.Wave == 1 {
			foundPlan = true
		}
	}
	if !foundRepository || !foundPlan {
		t.Fatalf("progress events = %#v", events)
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
