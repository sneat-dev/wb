package fleetsync

import (
	"context"
	"fmt"
	"github.com/sneat-dev/wb/internal/testenv"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/discover"
)

func TestSyncBatchDeliversEachNativeResultAndJoinsCanceledFeeder(t *testing.T) {
	t.Parallel()
	repos := []discover.Repo{{Org: "acme", Name: "one", TransferError: "one refused"}, {Org: "acme", Name: "two", TransferError: "two refused"}}
	var mu sync.Mutex
	started, done := map[string]int{}, map[string]int{}
	results := Batch(context.Background(), repos, BatchOptions{ProjectsRoot: t.TempDir(), Workers: 2, DryRun: true, PruneArchived: true}, BatchObserver{Started: func(r discover.Repo) { mu.Lock(); defer mu.Unlock(); started[r.Name]++ }, Done: func(r Result) { mu.Lock(); defer mu.Unlock(); done[r.Repo.Name]++ }})
	if len(results) != 2 || !reflect.DeepEqual(started, map[string]int{"one": 1, "two": 1}) || !reflect.DeepEqual(started, done) {
		t.Fatalf("results=%v started=%v done=%v", results, started, done)
	}
	for _, r := range results {
		if r.Status != Failed || r.Err.Error() != r.Repo.Name+" refused" {
			t.Fatal(r)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := Batch(ctx, repos, BatchOptions{Workers: 1, StopPendingOnCancel: true}, BatchObserver{}); len(got) > len(repos) {
		t.Fatal(got)
	}
	if got := Batch(context.Background(), nil, BatchOptions{Workers: 1}, BatchObserver{}); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestSyncBatchWithoutWorkersPreservesNoWorkResult(t *testing.T) {
	t.Parallel()
	for _, workers := range []int{0, -1} {
		t.Run(fmt.Sprint(workers), func(t *testing.T) {
			t.Parallel()
			called := false
			got := Batch(context.Background(), []discover.Repo{{Org: "acme", Name: "nonempty"}}, BatchOptions{Workers: workers}, BatchObserver{Started: func(discover.Repo) { called = true }, Done: func(Result) { called = true }})
			if got != nil || called {
				t.Fatalf("results=%v observerCalled=%v", got, called)
			}
		})
	}
}

func TestSyncBatchCanceledPlainRunPreservesEveryNativeFailureResult(t *testing.T) {
	t.Parallel()
	projects := t.TempDir()
	repos := make([]discover.Repo, 0, 2)
	for _, name := range []string{"one", "two"} {
		path := initTestRepository(t, filepath.Join(projects, "acme", name))
		testenv.Git(t, path, "config", "--local", "wb.skip-sync", "not-a-bool")
		repos = append(repos, discover.Repo{Org: "acme", Name: name, Path: path, Remote: true})
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	results := Batch(ctx, repos, BatchOptions{ProjectsRoot: projects, Workers: 1}, BatchObserver{})
	if len(results) != len(repos) {
		t.Fatalf("results=%v wanted both selected repositories", results)
	}
	seen := map[string]bool{}
	for _, result := range results {
		if result.Status != Failed || result.Err == nil || !strings.Contains(result.Err.Error(), "not-a-bool") || seen[result.Repo.Slug()] {
			t.Fatalf("result=%+v seen=%v", result, seen)
		}
		seen[result.Repo.Slug()] = true
	}
	if !seen["acme/one"] || !seen["acme/two"] {
		t.Fatal(seen)
	}
}
