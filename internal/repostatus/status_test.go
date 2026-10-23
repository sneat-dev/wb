package repostatus

import (
	"errors"
	"reflect"
	"sync"
	"testing"

	"github.com/sneat-dev/wb/internal/gitops"
	"github.com/sneat-dev/wb/internal/reposelection"
)

func TestCollectSelectionFailureKeepsIdentityAndDoesNotStartOrInspect(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("select")
	request := reposelection.Request{Path: "x", Parallel: 3}
	report, err := collectWith(request, Observer{Start: func(int) { t.Fatal("started") }}, func(actual reposelection.Request) ([]reposelection.Target, error) {
		if actual != request {
			t.Fatal(actual)
		}
		return nil, sentinel
	}, func(string) (gitops.RepoStatus, error) { t.Fatal("inspected"); return gitops.RepoStatus{}, nil })
	if err != sentinel || !reflect.DeepEqual(report, Index{}) {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
func TestCollectPreservesAllStateFieldsOrderingAndConcurrentCompletion(t *testing.T) {
	t.Parallel()
	states := map[string]gitops.RepoStatus{
		"dirty": {Modified: []string{"a"}, Untracked: []string{"b"}, Conflicted: []string{"c"}, Unpushed: []string{"d"}, UnpushedBranches: []gitops.UnpushedBranch{{Branch: "topic", Worktree: "wt", Commits: []string{"d"}}}, Stashed: []string{"e"}},
		"clean": {},
	}
	targets := []reposelection.Target{{Repository: "acme/dirty", Path: "dirty"}, {Repository: "acme/clean", Path: "clean"}, {Repository: "acme/error", Path: "error"}}
	var mu sync.Mutex
	completed := map[string]Row{}
	started := 0
	report, err := collectWith(reposelection.Request{Parallel: 3}, Observer{Start: func(total int) { started = total }, Complete: func(target reposelection.Target, row Row) {
		mu.Lock()
		defer mu.Unlock()
		completed[target.Repository] = row
	}}, func(reposelection.Request) ([]reposelection.Target, error) { return targets, nil }, func(path string) (gitops.RepoStatus, error) {
		if path == "error" {
			return gitops.RepoStatus{}, errors.New("bad Git")
		}
		return states[path], nil
	})
	state := states["dirty"]
	want := Index{SchemaVersion: 1, Repositories: []Row{
		{Repository: "acme/dirty", Path: "dirty", Status: "attention", Summary: state.Summary(), Modified: state.Modified, Untracked: state.Untracked, Conflicted: state.Conflicted, Unpushed: state.Unpushed, UnpushedBranches: state.UnpushedBranches, Stashed: state.Stashed},
		{Repository: "acme/clean", Path: "clean", Status: "clean", Summary: states["clean"].Summary()},
		{Repository: "acme/error", Path: "error", Status: "error", Error: "bad Git"},
	}}
	if err != nil || started != 3 || !reflect.DeepEqual(report, want) || len(completed) != 3 {
		t.Fatalf("report=%+v started=%d completed=%v err=%v", report, started, completed, err)
	}
	for _, row := range want.Repositories {
		if !reflect.DeepEqual(completed[row.Repository], row) {
			t.Fatal(completed)
		}
	}
}
func TestEmptySelectionReturnsNonNilRowsAndNilObserversAreSafe(t *testing.T) {
	t.Parallel()
	report, err := collectWith(reposelection.Request{Parallel: 1}, Observer{}, func(reposelection.Request) ([]reposelection.Target, error) { return nil, nil }, func(string) (gitops.RepoStatus, error) { t.Fatal("inspected"); return gitops.RepoStatus{}, nil })
	if err != nil || report.SchemaVersion != 1 || report.Repositories == nil || len(report.Repositories) != 0 {
		t.Fatalf("report=%+v err=%v", report, err)
	}
	rows := inspectTargetsWith([]reposelection.Target{{Path: "missing"}}, 1, nil, func(string) (gitops.RepoStatus, error) { return gitops.RepoStatus{}, errors.New("missing") })
	if len(rows) != 1 || rows[0].Error != "missing" {
		t.Fatal(rows)
	}
}
func TestCollectActualDefaultSelectionAndGitInspection(t *testing.T) {
	t.Parallel()
	path := initTestRepository(t, t.TempDir())
	report, err := Collect(reposelection.Request{Path: path, Parallel: 1}, Observer{})
	if err != nil || len(report.Repositories) != 1 || report.Repositories[0].Status != "clean" {
		t.Fatalf("report=%+v err=%v", report, err)
	}
}
