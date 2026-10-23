package hub

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

func TestRepositoryMetricsStoreCRUD(t *testing.T) {
	t.Parallel()

	backend := newFirestoreMemoryBackend()
	coverageBackend := newFirestoreMemoryBackend()
	coverageStore := NewRepositoryCoverageStore(coverageBackend)
	store := NewRepositoryMetricsStore(backend, coverageStore)
	ctx := context.Background()

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	metric := RepositoryMetric{
		Repository: "sneat-dev/wb",
		MetricType: MetricTypeCommitsPerDay,
		ReportedAt: now,
		Value:      7.5,
		Dimensions: []MetricDimension{
			{Name: "2026-09-26", Value: 8, FormattedValue: "8", Status: StatusPassed},
			{Name: "2026-09-25", Value: 7, FormattedValue: "7", Status: StatusPassed},
		},
	}

	// 1. Initial Get -> false
	got, found, err := store.GetMetric(ctx, "sneat-dev/wb", MetricTypeCommitsPerDay)
	if err != nil {
		t.Fatalf("GetMetric: %v", err)
	}
	if found {
		t.Fatalf("expected found=false for missing metric, got %+v", got)
	}

	// 2. SaveMetric
	if err := store.SaveMetric(ctx, metric); err != nil {
		t.Fatalf("SaveMetric: %v", err)
	}
	metric2 := RepositoryMetric{
		Repository: "sneat-dev/dalgo",
		MetricType: MetricTypeCommitsPerDay,
		ReportedAt: now,
		Value:      4.0,
	}
	if err := store.SaveMetric(ctx, metric2); err != nil {
		t.Fatalf("SaveMetric 2: %v", err)
	}

	// 3. GetMetric
	got, found, err = store.GetMetric(ctx, "sneat-dev/wb", MetricTypeCommitsPerDay)
	if err != nil || !found {
		t.Fatalf("GetMetric: found=%v, err=%v", found, err)
	}
	if got.Repository != "github.com/sneat-dev/wb" {
		t.Errorf("Repository = %q, want github.com/sneat-dev/wb", got.Repository)
	}
	if got.Owner != "sneat-dev" || got.Name != "wb" {
		t.Errorf("Owner/Name = (%q, %q)", got.Owner, got.Name)
	}
	if got.Status != StatusPassed {
		t.Errorf("Status = %q, want %q", got.Status, StatusPassed)
	}
	if got.FormattedValue != "7.5 commits/day" {
		t.Errorf("FormattedValue = %q, want %q", got.FormattedValue, "7.5 commits/day")
	}

	// 4. ListMetrics
	list, err := store.ListMetrics(ctx, MetricTypeCommitsPerDay)
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("ListMetrics count = %d, want 2", len(list))
	}

	// 5. ListMetricTypes
	types, err := store.ListMetricTypes(ctx)
	if err != nil {
		t.Fatalf("ListMetricTypes: %v", err)
	}
	if len(types) < 2 {
		t.Errorf("expected at least 2 metric types, got %d", len(types))
	}
}

func TestRepositoryMetricsStoreCoverageFallback(t *testing.T) {
	t.Parallel()

	backend := newFirestoreMemoryBackend()
	coverageBackend := newFirestoreMemoryBackend()
	coverageStore := NewRepositoryCoverageStore(coverageBackend)
	store := NewRepositoryMetricsStore(backend, coverageStore)
	ctx := context.Background()

	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)

	// Save coverage in coverageStore
	covRecord := StoredRepositoryCoverage{
		Repository:     "sneat-dev/wb",
		SHA:            "c3825a81",
		Ref:            "refs/heads/main",
		WorkflowRunID:  1234,
		WorkflowRunURL: "https://github.com/sneat-dev/wb/actions/runs/1234",
		ReportedAt:     now,
		Status:         quality.StatusPassed,
		Statements:     200,
		Covered:        180,
		Percentage:     90.0,
		Packages: map[string]quality.PackageSummary{
			"hub/narrate": {Statements: 100, Covered: 95, Percentage: 95.0},
			"cmd/wb":      {Statements: 100, Covered: 85, Percentage: 85.0},
		},
	}
	if err := coverageStore.SaveCoverage(ctx, covRecord); err != nil {
		t.Fatalf("SaveCoverage: %v", err)
	}

	// 1. GetMetric for coverage falls back to coverageStore
	got, found, err := store.GetMetric(ctx, "sneat-dev/wb", MetricTypeCoverage)
	if err != nil || !found {
		t.Fatalf("GetMetric fallback: found=%v, err=%v", found, err)
	}
	if got.Value != 90.0 {
		t.Errorf("Value = %v, want 90.0", got.Value)
	}
	if got.Status != StatusPassed {
		t.Errorf("Status = %q, want %q", got.Status, StatusPassed)
	}
	if len(got.Dimensions) != 2 {
		t.Fatalf("Dimensions len = %d, want 2", len(got.Dimensions))
	}
	if got.Dimensions[0].Name != "cmd/wb" || got.Dimensions[1].Name != "hub/narrate" {
		t.Errorf("Dimensions sort order incorrect: got %s, %s", got.Dimensions[0].Name, got.Dimensions[1].Name)
	}

	// Also query with empty string metricType (defaults to coverage)
	gotDef, foundDef, errDef := store.GetMetric(ctx, "sneat-dev/wb", "")
	if errDef != nil || !foundDef || gotDef.Value != 90.0 {
		t.Errorf("GetMetric with empty metricType failed: found=%v, err=%v", foundDef, errDef)
	}

	// Also query with alias "coverage"
	gotAlias, foundAlias, errAlias := store.GetMetric(ctx, "sneat-dev/wb", "coverage")
	if errAlias != nil || !foundAlias || gotAlias.Value != 90.0 {
		t.Errorf("GetMetric with 'coverage' alias failed: found=%v, err=%v", foundAlias, errAlias)
	}

	// Missing repo returns found=false
	_, missingFound, _ := store.GetMetric(ctx, "other/repo", MetricTypeCoverage)
	if missingFound {
		t.Error("expected found=false for missing repo")
	}

	// 2. ListMetrics merges coverageStore items
	list, err := store.ListMetrics(ctx, MetricTypeCoverage)
	if err != nil {
		t.Fatalf("ListMetrics: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("ListMetrics count = %d, want 1", len(list))
	}
	if list[0].Repository != "github.com/sneat-dev/wb" {
		t.Errorf("Repository = %q", list[0].Repository)
	}

	// Also call with empty metricType
	listDef, errDef := store.ListMetrics(ctx, "")
	if errDef != nil || len(listDef) != 1 {
		t.Errorf("ListMetrics with empty metricType failed: len=%d, err=%v", len(listDef), errDef)
	}

	// 3. Explicit metric in repositoryMetricsStore overrides coverage fallback
	customMetric := RepositoryMetric{
		Repository: "sneat-dev/wb",
		MetricType: MetricTypeCoverage,
		Value:      99.9,
	}
	if err := store.SaveMetric(ctx, customMetric); err != nil {
		t.Fatalf("SaveMetric: %v", err)
	}
	overridden, found, err := store.GetMetric(ctx, "sneat-dev/wb", MetricTypeCoverage)
	if err != nil || !found {
		t.Fatalf("GetMetric overridden: %v", err)
	}
	if overridden.Value != 99.9 {
		t.Errorf("expected overridden Value=99.9, got %v", overridden.Value)
	}

	listOverridden, _ := store.ListMetrics(ctx, MetricTypeCoverage)
	if len(listOverridden) != 1 || listOverridden[0].Value != 99.9 {
		t.Errorf("ListMetrics overridden expected value 99.9, got %v", listOverridden[0].Value)
	}
}

func TestCoverageToRepositoryMetricTiers(t *testing.T) {
	t.Parallel()

	// Modules only (no packages), test warning and failure thresholds
	warnRecord := StoredRepositoryCoverage{
		Repository: "owner/warn-repo",
		Percentage: 72.0,
		Modules: []quality.ModuleCoverageSummary{
			{Path: "mod-b", Statements: 50, Covered: 36, Percentage: 72.0},
			{Path: "mod-a", Statements: 50, Covered: 45, Percentage: 90.0},
		},
	}
	m := CoverageToRepositoryMetric(warnRecord)
	if m.Status != StatusWarning {
		t.Errorf("Status = %q, want %q", m.Status, StatusWarning)
	}
	if len(m.Dimensions) != 2 || m.Dimensions[0].Name != "mod-a" {
		t.Errorf("Modules sorted dimensions error: got %+v", m.Dimensions)
	}

	// Failed record
	failRecord := StoredRepositoryCoverage{
		Repository: "owner/fail-repo",
		Percentage: 45.0,
		Packages: map[string]quality.PackageSummary{
			"pkg1": {Statements: 100, Covered: 45, Percentage: 45.0},
			"pkg2": {Statements: 100, Covered: 75, Percentage: 75.0},
		},
	}
	mFail := CoverageToRepositoryMetric(failRecord)
	if mFail.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", mFail.Status, StatusFailed)
	}
	if mFail.Dimensions[0].Status != StatusFailed {
		t.Errorf("Dimension 0 status = %q, want %q", mFail.Dimensions[0].Status, StatusFailed)
	}
	if mFail.Dimensions[1].Status != StatusWarning {
		t.Errorf("Dimension 1 status = %q, want %q", mFail.Dimensions[1].Status, StatusWarning)
	}
}

func TestRepositoryMetricsStoreFormattingAndStatus(t *testing.T) {
	t.Parallel()

	backend := newFirestoreMemoryBackend()
	store := NewRepositoryMetricsStore(backend, nil)
	ctx := context.Background()

	// 1. Unknown metric type with no def -> StatusNeutral, default float format
	unknownMetric := RepositoryMetric{
		Repository: "sneat-dev/wb",
		MetricType: "custom_score",
		Value:      42.42,
	}
	if err := store.SaveMetric(ctx, unknownMetric); err != nil {
		t.Fatalf("SaveMetric: %v", err)
	}
	got, found, _ := store.GetMetric(ctx, "sneat-dev/wb", "custom_score")
	if !found {
		t.Fatal("custom metric not found")
	}
	if got.Status != StatusNeutral {
		t.Errorf("Status = %q, want %q", got.Status, StatusNeutral)
	}
	if got.FormattedValue != "42.4" {
		t.Errorf("FormattedValue = %q, want 42.4", got.FormattedValue)
	}

	// 2. formatMetricValue types
	if got := formatMetricValue(85.5, FormatPercentage, "%"); got != "85.5%" {
		t.Errorf("percentage format = %q", got)
	}
	if got := formatMetricValue(42, FormatInteger, ""); got != "42" {
		t.Errorf("integer format = %q", got)
	}
	if got := formatMetricValue(12300, FormatDuration, "ms"); got != "12.3s" {
		t.Errorf("duration format = %q", got)
	}

	// 3. evaluateMetricStatus higher is better vs lower is better
	defHigh := MetricTypeDefinition{
		GoodThreshold:  80,
		WarnThreshold:  50,
		HigherIsBetter: true,
	}
	if evaluateMetricStatus(85, defHigh) != StatusPassed {
		t.Error("expected passed")
	}
	if evaluateMetricStatus(60, defHigh) != StatusWarning {
		t.Error("expected warning")
	}
	if evaluateMetricStatus(40, defHigh) != StatusFailed {
		t.Error("expected failed")
	}

	defLow := MetricTypeDefinition{
		GoodThreshold:  10,
		WarnThreshold:  20,
		HigherIsBetter: false,
	}
	if evaluateMetricStatus(5, defLow) != StatusPassed {
		t.Error("expected passed")
	}
	if evaluateMetricStatus(15, defLow) != StatusWarning {
		t.Error("expected warning")
	}
	if evaluateMetricStatus(25, defLow) != StatusFailed {
		t.Error("expected failed")
	}
}

func TestRepositoryMetricsStoreErrorsAndUnavailable(t *testing.T) {
	t.Parallel()

	// Nil backend
	store := NewRepositoryMetricsStore(nil, nil)
	ctx := context.Background()

	if err := store.SaveMetric(ctx, RepositoryMetric{Repository: "a/b", MetricType: "test"}); !errors.Is(err, errRepositoryMetricsStoreUnavailable) {
		t.Errorf("SaveMetric with nil backend err = %v", err)
	}
	if _, _, err := store.GetMetric(ctx, "a/b", "test"); !errors.Is(err, errRepositoryMetricsStoreUnavailable) {
		t.Errorf("GetMetric with nil backend err = %v", err)
	}
	if _, err := store.ListMetrics(ctx, "test"); !errors.Is(err, errRepositoryMetricsStoreUnavailable) {
		t.Errorf("ListMetrics with nil backend err = %v", err)
	}

	// Validation
	backend := newFirestoreMemoryBackend()
	validStore := NewRepositoryMetricsStore(backend, nil)

	if err := validStore.SaveMetric(ctx, RepositoryMetric{Repository: ""}); err == nil {
		t.Error("expected error for empty repository")
	}
	if err := validStore.SaveMetric(ctx, RepositoryMetric{Repository: "a/b", MetricType: ""}); err == nil {
		t.Error("expected error for empty metric type")
	}

	// Backend errors
	backend.failSet = func(col, id string) error { return errors.New("boom set") }
	if err := validStore.SaveMetric(ctx, RepositoryMetric{Repository: "a/b", MetricType: "t"}); err == nil {
		t.Error("expected Set error")
	}

	backend.failSet = nil
	backend.failGet = func(col, id string) error { return errors.New("boom get") }
	if _, _, err := validStore.GetMetric(ctx, "a/b", "t"); err == nil {
		t.Error("expected Get error")
	}

	backend.failGet = nil
	backend.failQuery = func(col string) error { return errors.New("boom query") }
	if _, err := validStore.ListMetrics(ctx, "t"); err == nil {
		t.Error("expected Query error")
	}

	// Coverage store errors during fallback
	covBackend := newFirestoreMemoryBackend()
	covStore := NewRepositoryCoverageStore(covBackend)
	storeWithCov := NewRepositoryMetricsStore(newFirestoreMemoryBackend(), covStore)

	covBackend.failGet = func(col, id string) error { return errors.New("boom cov get") }
	if _, _, err := storeWithCov.GetMetric(ctx, "a/b", MetricTypeCoverage); err == nil {
		t.Error("expected error when coverage fallback Get fails")
	}

	covBackend.failGet = nil
	covBackend.failQuery = func(col string) error { return errors.New("boom cov query") }
	if _, err := storeWithCov.ListMetrics(ctx, MetricTypeCoverage); err == nil {
		t.Error("expected error when coverage fallback Query fails")
	}
}
