package fleet

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// measureRoot names the projects root TestMeasureFullAndUnchangedRefresh
// measures; the test is skipped while it is unset. measureSample optionally
// limits the measurement to the first N repositories of that root, for a root
// too large to finish in one run.
const (
	measureRoot   = "WB_COCKPIT_MEASURE_ROOT"
	measureSample = "WB_COCKPIT_MEASURE_SAMPLE"
)

// sampledRepositories passes the first limit repositories of the scan.
type sampledRepositories struct {
	RepositoryCollector
	limit int
}

func (s sampledRepositories) Repositories(ctx context.Context) ([]discover.Repo, error) {
	repositories, err := s.RepositoryCollector.Repositories(ctx)
	if s.limit > 0 && len(repositories) > s.limit {
		repositories = repositories[:s.limit]
	}
	return repositories, err
}

// TestMeasureFullAndUnchangedRefresh is how DefaultInterval was chosen. Against
// the projects root named by WB_COCKPIT_MEASURE_ROOT, with the production
// local collectors and no remote provider, it prints the time until the
// document first lists a repository, the time for the whole first pass, and the
// time for a second pass in which no fingerprint moved. Those collectors
// contact no network and write nothing inside a repository: they read files,
// run `git --no-optional-locks for-each-ref`, and write only the clone
// inventory cache, which this test puts in a temporary directory.
//
//	WB_COCKPIT_MEASURE_ROOT=$HOME/projects wb run -- go test ./internal/cockpit/fleet \
//	    -run TestMeasureFullAndUnchangedRefresh -v -count=1 -timeout 10m
func TestMeasureFullAndUnchangedRefresh(t *testing.T) {
	root := os.Getenv(measureRoot)
	if root == "" {
		t.Skipf("set %s to a projects root to measure", measureRoot)
	}
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}
	collectors := LocalCollectors{ProjectsRoot: root, Home: home, IndexCachePath: filepath.Join(t.TempDir(), "index.json")}.Collectors(nil)
	limit, _ := strconv.Atoi(os.Getenv(measureSample))
	collectors.Repositories = sampledRepositories{RepositoryCollector: collectors.Repositories, limit: limit}
	snapshotter := New(Options{Machine: "measure", Collectors: collectors, Logf: t.Logf})

	started := time.Now()
	done := make(chan error, 1)
	go func() { done <- snapshotter.Refresh(t.Context()) }()
	var first time.Duration
	for first == 0 {
		select {
		case err = <-done:
			first = time.Since(started)
			done <- err
		default:
			if snapshotter.Document().RepositoriesScanned > 0 {
				first = time.Since(started)
			}
			time.Sleep(time.Millisecond)
		}
	}
	err = <-done
	full := time.Since(started)
	document := snapshotter.Document()
	fmt.Printf("MEASURE first partial document: %v\n", first)
	fmt.Printf("MEASURE full first pass: %v for %d repositories, %d worktrees, %d branches, %d errors (error: %v)\n",
		full, len(document.Repositories), len(document.Worktrees), len(document.Branches), countErrors(document), err)

	started = time.Now()
	err = snapshotter.Refresh(t.Context())
	fmt.Printf("MEASURE unchanged refresh over all repositories: %v (error: %v)\n", time.Since(started), err)
}

func countErrors(document Document) int {
	return countWhere(document.Repositories, func(repository Repository) bool { return repository.Error != "" })
}
