package prinventory

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	fn    func([]string) ([]byte, error)
}

func (f *fakeRunner) Run(_ context.Context, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), args...))
	return f.fn(args)
}

func TestSearchQueryDoesNotImplicitlyExcludeArchivedRepositories(t *testing.T) {
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "archived:false") {
			t.Fatalf("default query unexpectedly excludes archived repositories: %v", args)
		}
		return []byte(`[{"total_count":0,"items":[]}]`), nil
	}}
	result := Inventory(context.Background(), Options{
		Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner,
		Now: func() time.Time { return time.Date(2026, 8, 30, 0, 0, 0, 0, time.UTC) },
	})
	if result.Complete != true || len(result.Diagnostics) != 0 {
		t.Fatalf("result = %+v", result)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("calls = %d, want one owner query", len(runner.calls))
	}
	if result.EffectiveFilters.IncludeArchived != true {
		t.Fatalf("filters = %+v, want archived included", result.EffectiveFilters)
	}
}

func TestSearchQueryExplicitlyExcludesArchivedRepositories(t *testing.T) {
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if !strings.Contains(strings.Join(args, " "), "archived:false") {
			t.Fatalf("explicit archive exclusion missing: %v", args)
		}
		return []byte(`[{"total_count":0,"items":[]}]`), nil
	}}
	result := Inventory(context.Background(), Options{
		Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner,
		ExcludeArchived: true,
	})
	if result.EffectiveFilters.IncludeArchived {
		t.Fatalf("filters = %+v, want archived excluded", result.EffectiveFilters)
	}
}

func TestInventoryFullyInventoriesMoreThanFifteenOwners(t *testing.T) {
	owners := make([]Owner, 20)
	for i := range owners {
		owners[i] = Owner{Login: "org-" + string(rune('a'+i)), Qualifier: "org"}
	}
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		return []byte(`[{"total_count":1,"items":[{"id":"1","number":1,"title":"open","html_url":"https://github.com/acme/app/pull/1","repository_url":"https://api.github.com/repos/acme/app","user":{"login":"author"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","draft":false}]}]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: owners, Runner: runner})
	if !result.Complete {
		t.Fatalf("inventory incomplete: %+v", result.Diagnostics)
	}
	if result.Counts.OwnersRequested != 20 || result.Counts.OwnersCompleted != 20 {
		t.Fatalf("counts = %+v", result.Counts)
	}
	if len(result.PullRequests) != 1 {
		t.Fatalf("deduped PRs = %d, want one", len(result.PullRequests))
	}
}

func TestInventoryReconcilesPartialOwnerResultsAndKeepsDiagnostics(t *testing.T) {
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if strings.Contains(strings.Join(args, " "), "org-broken") {
			return nil, errors.New("rate limited")
		}
		return []byte(`[{"total_count":0,"items":[]}]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: []Owner{
		{Login: "org-good", Qualifier: "org"}, {Login: "org-broken", Qualifier: "org"},
	}, Runner: runner})
	if result.Complete {
		t.Fatal("partial result reported complete")
	}
	if len(result.Diagnostics) != 1 || result.Diagnostics[0].Owner != "org-broken" {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
	if result.Counts.OwnersCompleted != 1 || result.Counts.OwnersFailed != 1 {
		t.Fatalf("counts = %+v", result.Counts)
	}
}

func TestInventoryAppliesImmutableCreatedBeforeCutoff(t *testing.T) {
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		return []byte(`[{"total_count":2,"items":[
{"id":"1","number":1,"title":"before","html_url":"https://github.com/acme/app/pull/1","repository_url":"https://api.github.com/repos/acme/app","user":{"login":"a"},"created_at":"2026-08-10T23:59:59Z","updated_at":"2026-08-11T00:00:00Z"},
{"id":"2","number":2,"title":"after","html_url":"https://github.com/acme/app/pull/2","repository_url":"https://api.github.com/repos/acme/app","user":{"login":"a"},"created_at":"2026-08-11T00:00:01Z","updated_at":"2026-08-11T00:00:02Z"}]}]`), nil
	}}
	cutoff := "2026-08-11T00:00:00Z"
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner, CreatedBefore: cutoff})
	if len(result.PullRequests) != 1 || result.PullRequests[0].Number != 1 {
		t.Fatalf("PRs = %+v", result.PullRequests)
	}
	if result.EffectiveFilters.CreatedBefore != cutoff {
		t.Fatalf("cutoff = %q", result.EffectiveFilters.CreatedBefore)
	}
}

func TestInventoryNoonCutoffKeepsEarlierPRsOnTheSameDay(t *testing.T) {
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		query := strings.Join(args, " ")
		if strings.Contains(query, "created:<2026-08-11") {
			t.Fatalf("day-granular exclusive query would omit the cutoff day's earlier PR: %v", args)
		}
		return []byte(`[{"total_count":2,"items":[
{"id":"1","number":1,"title":"morning","html_url":"https://github.com/acme/app/pull/1","repository_url":"https://api.github.com/repos/acme/app","user":{"login":"a"},"created_at":"2026-08-11T09:00:00Z","updated_at":"2026-08-11T09:00:00Z"},
{"id":"2","number":2,"title":"afternoon","html_url":"https://github.com/acme/app/pull/2","repository_url":"https://api.github.com/repos/acme/app","user":{"login":"a"},"created_at":"2026-08-11T13:00:00Z","updated_at":"2026-08-11T13:00:00Z"}]}]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner, CreatedBefore: "2026-08-11T12:00:00Z"})
	if len(result.PullRequests) != 1 || result.PullRequests[0].Number != 1 {
		t.Fatalf("PRs = %+v, want only the morning PR", result.PullRequests)
	}
}

func TestInventoryDetailFailureIsVisibleAndIncomplete(t *testing.T) {
	runner := &fakeRunner{fn: func(args []string) ([]byte, error) {
		if len(args) > 0 && args[0] == "pr" {
			return nil, errors.New("details unavailable")
		}
		return []byte(`[{"total_count":1,"items":[{"id":"1","number":1,"title":"open","html_url":"https://github.com/acme/app/pull/1","repository_url":"https://api.github.com/repos/acme/app","user":{"login":"author"},"created_at":"2026-01-01T00:00:00Z","updated_at":"2026-01-02T00:00:00Z","draft":false,"pull_request":{"url":"https://api.github.com/repos/acme/app/pulls/1"}}]}]`), nil
	}}
	result := Inventory(context.Background(), Options{Owners: []Owner{{Login: "acme", Qualifier: "org"}}, Runner: runner})
	if result.Complete || result.Counts.OwnersFailed != 1 || len(result.PullRequests) != 1 {
		t.Fatalf("result = %+v, want one retained PR and one failed owner", result)
	}
	if len(result.Diagnostics) != 1 || !strings.Contains(result.Diagnostics[0].Message, "details failed") {
		t.Fatalf("diagnostics = %+v", result.Diagnostics)
	}
}

func TestMarkdownAndJSONIncludeStableSnapshotMetadataAndPRFields(t *testing.T) {
	now := time.Date(2026, 8, 30, 12, 34, 56, 0, time.UTC)
	report := Report{
		SchemaVersion: 1, SnapshotAt: now,
		Owners:           []Owner{{Login: "acme", Qualifier: "org"}},
		EffectiveFilters: Filters{State: "open", IncludeArchived: true},
		Counts:           Counts{OwnersRequested: 1, OwnersCompleted: 1, PullRequests: 1},
		PullRequests:     []PullRequest{{Repository: "acme/app", Number: 3, Title: "Fix", URL: "https://github.com/acme/app/pull/3", Author: "alice", Draft: true, Mergeable: "CONFLICTING", MergeStateStatus: "dirty", Checks: []Check{{Name: "ci", Status: "COMPLETED", Conclusion: "FAILURE"}}}},
	}
	b, err := json.Marshal(report)
	if err != nil || !strings.Contains(string(b), `"mergeable":"CONFLICTING"`) || !strings.Contains(string(b), `"draft":true`) {
		t.Fatalf("JSON = %s, err=%v", b, err)
	}
	md := RenderMarkdown(report)
	for _, want := range []string{"2026-08-30T12:34:56Z", "acme", "acme/app", "CONFLICTING", "FAILURE"} {
		if !strings.Contains(md, want) {
			t.Errorf("Markdown missing %q:\n%s", want, md)
		}
	}
}
