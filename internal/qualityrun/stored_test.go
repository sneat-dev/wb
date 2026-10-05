package qualityrun

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/hub"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/hubstore"
	"github.com/sneat-dev/wb/internal/quality"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type recordStore struct {
	records         []hub.StoredRepositoryCoverage
	listErr, getErr error
	get             func(string) (hub.StoredRepositoryCoverage, bool, error)
}

func (s recordStore) SaveCoverage(context.Context, hub.StoredRepositoryCoverage) error { return nil }
func (s recordStore) GetCoverage(_ context.Context, name string) (hub.StoredRepositoryCoverage, bool, error) {
	if s.get != nil {
		return s.get(name)
	}
	for _, r := range s.records {
		if r.Repository == name {
			return r, true, nil
		}
	}
	return hub.StoredRepositoryCoverage{}, false, s.getErr
}
func (s recordStore) ListCoverage(context.Context) ([]hub.StoredRepositoryCoverage, error) {
	return append([]hub.StoredRepositoryCoverage(nil), s.records...), s.listErr
}

type countCloser struct{ calls int }

func (c *countCloser) Close() error { c.calls++; return errors.New("ignored close failure") }
func storeOps(store hub.RepositoryCoverageStore) storeOperations {
	return storeOperations{Open: func(context.Context) (hub.RepositoryCoverageStore, io.Closer, error) { return store, nil, nil }, OriginURL: func(string) (string, error) { return "", errors.New("no origin") }}
}
func TestStoredCoverageFiltersAndAggregatesImmutableRecords(t *testing.T) {
	t.Parallel()
	// Parent-owned records are immutable: reads copy the list and assembly creates
	// fresh report/module slices. Each subtest owns its operations/result state.
	records := []hub.StoredRepositoryCoverage{{Repository: "sneat-dev/wb", Status: quality.StatusPassed, SHA: strings.Repeat("1", 40), Ref: "refs/heads/main", ReportedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC), Statements: 1000, Covered: 850, Percentage: 85, Modules: []quality.ModuleCoverageSummary{{Path: ".", Statements: 1000, Covered: 850, Percentage: 85}}}, {Repository: "sneat-co/app", Status: quality.StatusPassed, Statements: 500, Covered: 450, Percentage: 90}}
	for _, tc := range []struct {
		name, match, regex         string
		count, statements, covered int
	}{{"all", "", "", 2, 1500, 1300}, {"glob", "sneat-co/*", "", 1, 500, 450}, {"regex", "", ".*wb$", 1, 1000, 850}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			result, err := storedWith(t.Context(), StoredRequest{Fleet: true, Match: tc.match, Regex: tc.regex}, storeOps(recordStore{records: records}))
			if err != nil {
				t.Fatal(err)
			}
			if len(result.Report.Repositories) != tc.count || result.Report.Statements != tc.statements || result.Report.Covered != tc.covered {
				t.Fatalf("report=%+v", result.Report)
			}
			if tc.name == "regex" && (len(result.Report.Repositories[0].Modules) != 1 || result.Report.Repositories[0].Modules[0].Percentage != 85) {
				t.Fatalf("modules=%+v", result.Report.Repositories[0].Modules)
			}
		})
	}
}
func TestStoredCoverageOrderFailuresAndCloser(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("store failure")
	for _, tc := range []struct {
		name      string
		request   StoredRequest
		store     recordStore
		openErr   error
		want      string
		noRecords bool
	}{{name: "open", openErr: sentinel, want: "store failure"}, {name: "list", request: StoredRequest{Fleet: true}, store: recordStore{listErr: sentinel}, want: "store failure"}, {name: "get", request: StoredRequest{Path: "repo"}, store: recordStore{getErr: sentinel}, want: "store failure"}, {name: "empty precedes regex", request: StoredRequest{Fleet: true, Regex: "[", ReportDir: "unusable"}, noRecords: true}, {name: "regex", request: StoredRequest{Fleet: true, Regex: "["}, store: recordStore{records: []hub.StoredRepositoryCoverage{{Repository: "acme/app"}}}, want: "invalid --regex"}, {name: "match", request: StoredRequest{Fleet: true, Match: "other/*"}, store: recordStore{records: []hub.StoredRepositoryCoverage{{Repository: "acme/app"}}}, want: "no CI coverage reports match"}, {name: "missing", request: StoredRequest{Path: "org/missing"}, want: "no CI coverage found for repository org/missing"}, {name: "fallback list ignored", request: StoredRequest{Path: "missing"}, store: recordStore{listErr: sentinel}, want: "no CI coverage found for repository missing"}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			closer := &countCloser{}
			ops := storeOps(tc.store)
			ops.Open = func(context.Context) (hub.RepositoryCoverageStore, io.Closer, error) {
				return tc.store, closer, tc.openErr
			}
			result, err := storedWith(t.Context(), tc.request, ops)
			if tc.want != "" {
				if err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("error=%v want=%s", err, tc.want)
				}
			} else if err != nil || result.NoRecords != tc.noRecords {
				t.Fatalf("result=%+v error=%v", result, err)
			}
			wantClose := 1
			if tc.openErr != nil {
				wantClose = 0
			}
			if closer.calls != wantClose {
				t.Fatalf("close count=%d", closer.calls)
			}
		})
	}
}
func TestStoredCoverageNormalizesPrefixesAndSuffixes(t *testing.T) {
	t.Parallel()
	store := recordStore{records: []hub.StoredRepositoryCoverage{{Repository: "Sneat-Dev/WB", Statements: 200, Covered: 180, Percentage: 90}}}
	for _, path := range []string{"WB", "github.com/Sneat-Dev/WB"} {
		result, err := storedWith(t.Context(), StoredRequest{Path: path}, storeOps(store))
		if err != nil {
			t.Fatal(err)
		}
		if len(result.Report.Repositories) != 1 || result.Report.Repositories[0].Repository != "Sneat-Dev/WB" || result.Report.Percentage != 90 {
			t.Fatalf("report=%+v", result.Report)
		}
	}
	result, err := storedWith(t.Context(), StoredRequest{Fleet: true, Match: "Sneat-Dev/*"}, storeOps(store))
	if err != nil || len(result.Report.Repositories) != 1 {
		t.Fatalf("full prefix match=%+v %v", result, err)
	}
}
func TestStoredCoveragePersistsReportsAndPropagatesWriteFailure(t *testing.T) {
	t.Parallel()
	store := recordStore{records: []hub.StoredRepositoryCoverage{{Repository: "acme/app", Statements: 10, Covered: 8, Percentage: 80}}}
	dir := t.TempDir()
	result, err := storedWith(t.Context(), StoredRequest{Fleet: true, ReportDir: dir}, storeOps(store))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"coverage.md", "coverage.yaml"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatal(err)
		}
	}
	if result.Artifacts.Report.Path != filepath.Join(dir, "coverage.yaml") || len(result.Artifacts.Report.SHA256) != 64 {
		t.Fatalf("artifacts=%+v", result.Artifacts)
	}
	blocked := filepath.Join(t.TempDir(), "blocked")
	if err := os.WriteFile(blocked, nil, 0600); err != nil {
		t.Fatal(err)
	}
	for _, fleet := range []bool{false, true} {
		if _, err := storedWith(t.Context(), StoredRequest{Path: "acme/app", Fleet: fleet, ReportDir: blocked}, storeOps(store)); err == nil {
			t.Fatal("blocked persistence accepted")
		}
	}
}
func TestStoredCoverageFallbackStopsAtFirstCaseInsensitiveMatch(t *testing.T) {
	t.Parallel()
	store := recordStore{records: []hub.StoredRepositoryCoverage{{Repository: "unrelated/nope"}, {Repository: "ACME/APP", Percentage: 80}, {Repository: "other/app", Percentage: 90}}}
	result, err := storedWith(t.Context(), StoredRequest{Path: "app"}, storeOps(store))
	if err != nil || len(result.Report.Repositories) != 1 || result.Report.Repositories[0].Repository != "ACME/APP" {
		t.Fatalf("result=%+v %v", result, err)
	}
}
func TestStoredCoverageReadsParentOwnedMemoryStore(t *testing.T) {
	t.Parallel()
	db, closer, err := hubstore.Open(t.Context(), hubconfig.Store{Engine: hubconfig.EngineMemory})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = closer.Close() })
	store := hub.NewRepositoryCoverageStore(db)
	records := []hub.StoredRepositoryCoverage{{Repository: "sneat-dev/wb", Status: quality.StatusPassed, Statements: 1000, Covered: 850, Percentage: 85, Modules: []quality.ModuleCoverageSummary{{Path: ".", Statements: 1000, Covered: 850, Percentage: 85}}}, {Repository: "sneat-co/app", Status: quality.StatusPassed, Statements: 500, Covered: 450, Percentage: 90}}
	for _, record := range records {
		if err := store.SaveCoverage(t.Context(), record); err != nil {
			t.Fatal(err)
		}
	}
	// Setup finishes before the read-only children. Get/List allocate records;
	// no report directory means reads do not write artifacts, caches or metadata.
	for _, tc := range []struct {
		name                  string
		request               StoredRequest
		count, total, covered int
	}{{"fleet", StoredRequest{Fleet: true}, 2, 1500, 1300}, {"repository", StoredRequest{Path: "sneat-dev/wb"}, 1, 1000, 850}, {"short-name", StoredRequest{Path: "wb"}, 1, 1000, 850}, {"filter", StoredRequest{Fleet: true, Match: "sneat-co/*"}, 1, 500, 450}} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := storedWith(t.Context(), tc.request, storeOps(store))
			if err != nil || len(got.Report.Repositories) != tc.count || got.Report.Statements != tc.total || got.Report.Covered != tc.covered {
				t.Fatalf("report=%+v err=%v", got, err)
			}
			if tc.name == "repository" && (len(got.Report.Repositories[0].Modules) != 1 || got.Report.Repositories[0].Modules[0].Covered != 850) {
				t.Fatal(got.Report)
			}
		})
	}
}
