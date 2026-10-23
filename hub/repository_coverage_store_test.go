package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestRepositoryCoverageStoreCRUD(t *testing.T) {
	t.Parallel()

	backend := newFirestoreMemoryBackend()
	store := NewRepositoryCoverageStore(backend)
	ctx := context.Background()

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	record := StoredRepositoryCoverage{
		Repository:     "sneat-dev/wb",
		SHA:            "abcdef012345",
		Ref:            "refs/heads/main",
		WorkflowRunID:  123,
		WorkflowRunURL: "https://github.com/sneat-dev/wb/actions/runs/123",
		ReportedAt:     now,
		Status:         quality.StatusPassed,
		Statements:     100,
		Covered:        88,
		Percentage:     88.0,
		Modules: []quality.ModuleCoverageSummary{
			{Path: ".", Statements: 100, Covered: 88, Percentage: 88.0},
		},
		Packages: map[string]quality.PackageSummary{
			"cmd/wb": {Statements: 100, Covered: 88, Percentage: 88.0},
		},
	}

	// 1. Initial Get on empty store -> found=false
	got, found, err := store.GetCoverage(ctx, "sneat-dev/wb")
	if err != nil {
		t.Fatalf("GetCoverage: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing repository, got true (%+v)", got)
	}

	// 2. SaveCoverage
	if err := store.SaveCoverage(ctx, record); err != nil {
		t.Fatalf("SaveCoverage: %v", err)
	}

	// 3. GetCoverage with short and canonical names
	for _, queryRepo := range []string{"sneat-dev/wb", "github.com/sneat-dev/wb", " sneat-dev/wb "} {
		got, found, err = store.GetCoverage(ctx, queryRepo)
		if err != nil {
			t.Fatalf("GetCoverage(%q): %v", queryRepo, err)
		}
		if !found {
			t.Fatalf("GetCoverage(%q): expected found=true", queryRepo)
		}
		if got.Repository != "github.com/sneat-dev/wb" {
			t.Errorf("Repository = %q, want %q", got.Repository, "github.com/sneat-dev/wb")
		}
		if got.Owner != "sneat-dev" || got.Name != "wb" {
			t.Errorf("Owner/Name = (%q, %q), want (sneat-dev, wb)", got.Owner, got.Name)
		}
		if got.Statements != 100 || got.Covered != 88 || got.Percentage != 88.0 {
			t.Errorf("Coverage stats mismatch: got (%d, %d, %f)", got.Statements, got.Covered, got.Percentage)
		}
	}

	// 4. ListCoverage
	list, err := store.ListCoverage(ctx)
	if err != nil {
		t.Fatalf("ListCoverage: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListCoverage len = %d, want 1", len(list))
	}
	if list[0].Repository != "github.com/sneat-dev/wb" {
		t.Errorf("List[0].Repository = %q, want github.com/sneat-dev/wb", list[0].Repository)
	}
}

func TestRepositoryCoverageStoreUnavailable(t *testing.T) {
	t.Parallel()

	store := NewRepositoryCoverageStore(nil)
	ctx := context.Background()

	if err := store.SaveCoverage(ctx, StoredRepositoryCoverage{Repository: "a/b"}); !errors.Is(err, errRepositoryCoverageStoreUnavailable) {
		t.Errorf("SaveCoverage with nil backend error = %v, want %v", err, errRepositoryCoverageStoreUnavailable)
	}
	if _, _, err := store.GetCoverage(ctx, "a/b"); !errors.Is(err, errRepositoryCoverageStoreUnavailable) {
		t.Errorf("GetCoverage with nil backend error = %v, want %v", err, errRepositoryCoverageStoreUnavailable)
	}
	if _, err := store.ListCoverage(ctx); !errors.Is(err, errRepositoryCoverageStoreUnavailable) {
		t.Errorf("ListCoverage with nil backend error = %v, want %v", err, errRepositoryCoverageStoreUnavailable)
	}
}

func TestRepositoryCoverageStoreValidation(t *testing.T) {
	t.Parallel()

	backend := newFirestoreMemoryBackend()
	store := NewRepositoryCoverageStore(backend)
	ctx := context.Background()

	// Empty repository is rejected
	if err := store.SaveCoverage(ctx, StoredRepositoryCoverage{Repository: ""}); err == nil {
		t.Error("expected error for empty repository, got nil")
	}

	// Backend errors propagated
	backend.failSet = func(col, id string) error { return errors.New("boom set") }
	if err := store.SaveCoverage(ctx, StoredRepositoryCoverage{Repository: "owner/repo"}); err == nil || !errors.Is(err, err) {
		t.Error("expected Set error propagation")
	}

	backend.failSet = nil
	backend.failGet = func(col, id string) error { return errors.New("boom get") }
	if _, _, err := store.GetCoverage(ctx, "owner/repo"); err == nil {
		t.Error("expected Get error propagation")
	}

	backend.failGet = nil
	backend.failQuery = func(col string) error { return errors.New("boom query") }
	if _, err := store.ListCoverage(ctx); err == nil {
		t.Error("expected Query error propagation")
	}
}
