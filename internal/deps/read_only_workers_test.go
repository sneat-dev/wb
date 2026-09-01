package deps

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestReadOnlyWorkerCountFloorsOnlyDefaultParallel(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name             string
		parallel         int
		parallelExplicit bool
		jobs             int
		want             int
	}{
		{name: "default parallel takes the read-only floor", parallel: 1, jobs: 100, want: defaultReadOnlyParallel},
		{name: "floor never exceeds the job count", parallel: 1, jobs: 2, want: 2},
		{name: "explicit serial parallel stays serial", parallel: 1, parallelExplicit: true, jobs: 100, want: 1},
		{name: "explicit low parallel stays authoritative", parallel: 2, parallelExplicit: true, jobs: 100, want: 2},
		{name: "wide parallel wins over the floor", parallel: 16, jobs: 100, want: 16},
		{name: "wide explicit parallel is capped by jobs", parallel: 16, parallelExplicit: true, jobs: 3, want: 3},
		{name: "zero jobs still leaves one worker", parallel: 1, jobs: 0, want: 1},
		{name: "unnormalized zero parallel takes the floor", parallel: 0, jobs: 100, want: defaultReadOnlyParallel},
		{name: "explicit unnormalized zero parallel leaves one worker", parallel: 0, parallelExplicit: true, jobs: 100, want: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := readOnlyWorkerCount(test.parallel, test.parallelExplicit, test.jobs); got != test.want {
				t.Fatalf("readOnlyWorkerCount(%d, %t, %d) = %d, want %d", test.parallel, test.parallelExplicit, test.jobs, got, test.want)
			}
		})
	}
}

// registryBarrier fails a lookup that is not joined by size-1 concurrent
// lookups within the timeout, proving a pool really runs that wide without
// sleeping on the happy path.
func registryBarrier(size int) func() error {
	var mu sync.Mutex
	waiting := 0
	release := make(chan struct{})
	return func() error {
		mu.Lock()
		waiting++
		if waiting == size {
			close(release)
		}
		mu.Unlock()
		select {
		case <-release:
			return nil
		case <-time.After(10 * time.Second):
			return errors.New("worker pool never reached the expected read-only concurrency")
		}
	}
}

func staleRefreshEvents(now time.Time, count int) []ReleaseEvent {
	events := make([]ReleaseEvent, 0, count)
	for index := range count {
		events = append(events, ReleaseEvent{
			Dependency: fmt.Sprintf("example.com/stale-%d", index), Version: "v1.2.0",
			Source: "observed_release", CheckedAt: now.Add(-10 * time.Minute),
		})
	}
	return events
}

func TestRefreshStaleReleaseEventsRunsRegistryLookupsConcurrently(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	barrier := registryBarrier(defaultReadOnlyParallel)
	events, refreshes, err := refreshStaleReleaseEvents(context.Background(), staleRefreshEvents(now, defaultReadOnlyParallel), BumpOptions{
		Options:      Options{Parallel: 1},
		RefreshAfter: 5 * time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(context.Context, string) (string, error) {
			if err := barrier(); err != nil {
				return "", err
			}
			return "v1.3.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(refreshes) != defaultReadOnlyParallel {
		t.Fatalf("refreshes = %+v", refreshes)
	}
	for index, event := range events {
		if event.Version != "v1.3.0" || event.Source != "refreshed_latest" {
			t.Fatalf("event %d = %+v", index, event)
		}
		if refreshes[index].Dependency != event.Dependency || refreshes[index].After != "v1.3.0" {
			t.Fatalf("refresh %d = %+v, want deterministic event order", index, refreshes[index])
		}
	}
}

func TestRefreshStaleReleaseEventsHonorsExplicitSerialParallel(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	var inFlight, overlapped, calls atomic.Int32
	_, refreshes, err := refreshStaleReleaseEvents(context.Background(), staleRefreshEvents(now, 3), BumpOptions{
		Options:      Options{Parallel: 1, ParallelExplicit: true},
		RefreshAfter: 5 * time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(context.Context, string) (string, error) {
			calls.Add(1)
			if inFlight.Add(1) > 1 {
				overlapped.Store(1)
			}
			time.Sleep(2 * time.Millisecond)
			inFlight.Add(-1)
			return "v1.3.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if overlapped.Load() != 0 {
		t.Fatal("explicit --parallel 1 must keep registry rechecks serial")
	}
	if calls.Load() != 3 || len(refreshes) != 3 {
		t.Fatalf("calls = %d, refreshes = %+v", calls.Load(), refreshes)
	}
}

func TestRefreshStaleReleaseEventsJoinsLookupFailuresDeterministically(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 1, 12, 0, 0, 0, time.UTC)
	events, refreshes, err := refreshStaleReleaseEvents(context.Background(), []ReleaseEvent{
		{Dependency: "example.com/broken", Version: "v1.2.0", Source: "observed_release", CheckedAt: now.Add(-10 * time.Minute)},
		{Dependency: "example.com/healthy", Version: "v1.2.0", Source: "observed_release", CheckedAt: now.Add(-10 * time.Minute)},
	}, BumpOptions{
		Options:      Options{Parallel: 1},
		RefreshAfter: 5 * time.Minute,
		Now:          func() time.Time { return now },
		LatestGoVersion: func(_ context.Context, module string) (string, error) {
			if module == "example.com/broken" {
				return "", errors.New("registry unavailable")
			}
			return "v1.3.0", nil
		},
	})
	if err == nil || !strings.Contains(err.Error(), "example.com/broken@v1.2.0") || !strings.Contains(err.Error(), "registry unavailable") {
		t.Fatalf("err = %v", err)
	}
	if len(refreshes) != 1 || refreshes[0].Dependency != "example.com/healthy" || refreshes[0].After != "v1.3.0" {
		t.Fatalf("refreshes = %+v", refreshes)
	}
	if events[0].Dependency != "example.com/broken" || events[0].Version != "v1.2.0" {
		t.Fatalf("failed event must stay unrefreshed: %+v", events[0])
	}
	if events[1].Version != "v1.3.0" {
		t.Fatalf("healthy event must still refresh: %+v", events[1])
	}
}

func TestCaptureReleaseBaselinesObservesModulesConcurrently(t *testing.T) {
	t.Parallel()
	graph := goFleetGraph{requirements: map[string][]goFleetRequirement{}}
	affected := map[string]map[string]bool{"acme/provider": {}}
	for index := range defaultReadOnlyParallel {
		module := fmt.Sprintf("example.com/module-%d", index)
		graph.requirements[module] = []goFleetRequirement{{Repository: "acme/consumer"}}
		affected["acme/provider"][module] = true
	}
	barrier := registryBarrier(defaultReadOnlyParallel)
	observations, err := captureReleaseBaselines(context.Background(), graph, affected, BumpOptions{
		Options: Options{Parallel: 1},
		LatestGoVersion: func(context.Context, string) (string, error) {
			if err := barrier(); err != nil {
				return "", err
			}
			return "v0.9.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(observations) != defaultReadOnlyParallel {
		t.Fatalf("observations = %+v", observations)
	}
	for module, observation := range observations {
		if observation.Module != module || observation.Repository != "acme/provider" || observation.Before != "v0.9.0" || observation.Status != "baseline" || !observation.RequireNewer {
			t.Fatalf("observation for %s = %+v", module, observation)
		}
	}
}

func TestResumeReleaseObservationsObservesReleasesConcurrently(t *testing.T) {
	t.Parallel()
	pending := make([]ReleaseObservation, 0, defaultReadOnlyParallel)
	for index := range defaultReadOnlyParallel {
		pending = append(pending, ReleaseObservation{
			Module: fmt.Sprintf("example.com/provider-%d", index), Repository: "acme/provider", Before: "v1.0.0",
		})
	}
	barrier := registryBarrier(defaultReadOnlyParallel)
	observations, err := resumeReleaseObservations(context.Background(), pending, BumpOptions{
		Options:      Options{Parallel: 1, Timeout: 30 * time.Second},
		PollInterval: time.Millisecond,
		LatestGoVersion: func(context.Context, string) (string, error) {
			if err := barrier(); err != nil {
				return "", err
			}
			return "v1.1.0", nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	for index, observation := range observations {
		if observation.Status != "released" || observation.After != "v1.1.0" {
			t.Fatalf("observation %d = %+v", index, observation)
		}
	}
}
