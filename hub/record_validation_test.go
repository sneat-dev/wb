package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/quality"
)

// hostileValues is what a client that wants markup in a dashboard page would
// put in a record: an element with a handler, a break out of a quoted handler,
// of an attribute and of a class, and a script address.
var hostileValues = []string{
	`<img src=x onerror=alert(1)>`,
	`'");alert(1)//`,
	`" onmouseover="alert(1)`,
	`passed" onclick="alert(1)`,
	"`${alert(1)}`",
	`a\b`,
	"line\nbreak",
	`</script><script>alert(1)</script>`,
}

func validMetric() RepositoryMetric {
	return RepositoryMetric{
		Repository: "Sneat-Dev/wb", Owner: "sneat-dev", Name: "wb", MetricType: MetricTypeCommitsPerDay,
		ReportedAt: time.Unix(1_700_000_000, 0).UTC(), Ref: "refs/heads/feature/x+1@2", SHA: "0123456789abcdef0123456789abcdef01234567",
		Value: 3.5, FormattedValue: "3.5 commits/day", Status: StatusWarning,
		Metadata: map[string]any{
			"statements": float64(100), "covered": 40, "workflow_run_id": int64(7), "flaky": false, "note": nil,
			"runner": "ubuntu-24.04", "workflow_run_url": "https://github.com/sneat-dev/wb/actions/runs/7",
		},
		Dimensions: []MetricDimension{
			{Name: "github.com/sneat-dev/wb/internal/cockpit", Value: 100, FormattedValue: "100.0%", Status: StatusPassed, Details: map[string]any{"statements": 10, "covered": 10}},
			{Name: "2026-10-01", Value: 2},
		},
	}
}

func validCoverage() StoredRepositoryCoverage {
	return StoredRepositoryCoverage{
		Repository: "github.com/sneat-dev/wb", Owner: "sneat-dev", Name: "wb", Ref: "main", SHA: "0123456", WorkflowRunID: 7,
		WorkflowRunURL: "https://github.com/sneat-dev/wb/actions/runs/7", Status: quality.StatusPassed, Statements: 10, Covered: 9, Percentage: 90,
		Modules:  []quality.ModuleCoverageSummary{{Path: ".", Statements: 10, Covered: 9, Percentage: 90}},
		Packages: map[string]quality.PackageSummary{"github.com/sneat-dev/wb/hub": {Statements: 10, Covered: 9, Percentage: 90}},
	}
}

// hostileMetrics is a valid metric with one field at a time replaced by value.
func hostileMetrics(value string) map[string]RepositoryMetric {
	change := func(apply func(*RepositoryMetric)) RepositoryMetric {
		metric := validMetric()
		apply(&metric)
		return metric
	}
	return map[string]RepositoryMetric{
		"repository":                change(func(m *RepositoryMetric) { m.Repository = value }),
		"repository with a path":    change(func(m *RepositoryMetric) { m.Repository = "sneat-dev/wb/" + value }),
		"owner":                     change(func(m *RepositoryMetric) { m.Owner = value }),
		"name":                      change(func(m *RepositoryMetric) { m.Name = value }),
		"metric_type":               change(func(m *RepositoryMetric) { m.MetricType = value }),
		"ref":                       change(func(m *RepositoryMetric) { m.Ref = value }),
		"sha":                       change(func(m *RepositoryMetric) { m.SHA = value }),
		"formatted_value":           change(func(m *RepositoryMetric) { m.FormattedValue = value }),
		"status":                    change(func(m *RepositoryMetric) { m.Status = MetricStatus(value) }),
		"a metadata value":          change(func(m *RepositoryMetric) { m.Metadata["statements"] = value }),
		"a metadata key":            change(func(m *RepositoryMetric) { m.Metadata[value] = 1 }),
		"a nested metadata value":   change(func(m *RepositoryMetric) { m.Metadata["covered"] = map[string]any{"x": value} }),
		"a metadata list":           change(func(m *RepositoryMetric) { m.Metadata["covered"] = []any{value} }),
		"the workflow run address":  change(func(m *RepositoryMetric) { m.Metadata["workflow_run_url"] = "javascript:" + value }),
		"a dimension's name":        change(func(m *RepositoryMetric) { m.Dimensions[0].Name = value }),
		"a dimension's status":      change(func(m *RepositoryMetric) { m.Dimensions[0].Status = MetricStatus(value) }),
		"a dimension's formatted":   change(func(m *RepositoryMetric) { m.Dimensions[1].FormattedValue = value }),
		"a dimension's detail":      change(func(m *RepositoryMetric) { m.Dimensions[0].Details["covered"] = value }),
		"a dimension's detail key":  change(func(m *RepositoryMetric) { m.Dimensions[0].Details[value] = 1 }),
		"a dimension's nested data": change(func(m *RepositoryMetric) { m.Dimensions[0].Details["covered"] = map[string]any{"x": value} }),
	}
}

func hostileCoverage(value string) map[string]StoredRepositoryCoverage {
	change := func(apply func(*StoredRepositoryCoverage)) StoredRepositoryCoverage {
		record := validCoverage()
		apply(&record)
		return record
	}
	return map[string]StoredRepositoryCoverage{
		"repository":       change(func(r *StoredRepositoryCoverage) { r.Repository = value }),
		"owner":            change(func(r *StoredRepositoryCoverage) { r.Owner = value }),
		"name":             change(func(r *StoredRepositoryCoverage) { r.Name = value }),
		"ref":              change(func(r *StoredRepositoryCoverage) { r.Ref = value }),
		"sha":              change(func(r *StoredRepositoryCoverage) { r.SHA = value }),
		"status":           change(func(r *StoredRepositoryCoverage) { r.Status = quality.Status(value) }),
		"workflow_run_url": change(func(r *StoredRepositoryCoverage) { r.WorkflowRunURL = "https://github.com/" + value }),
		"a script address": change(func(r *StoredRepositoryCoverage) { r.WorkflowRunURL = "javascript:alert(1)" }),
		"a module's path":  change(func(r *StoredRepositoryCoverage) { r.Modules[0].Path = value }),
		"a package's name": change(func(r *StoredRepositoryCoverage) { r.Packages[value] = quality.PackageSummary{} }),
	}
}

func post(handler http.Handler, path string, record any) *httptest.ResponseRecorder {
	body, _ := json.Marshal(record)
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body)))
	return recorder
}

// A metric whose field holds markup, in any field, is refused with a closed
// code that repeats nothing of it, and is not stored: the write side of what
// the dashboard pages are built to survive.
func TestAMetricWithAValueOutsideItsFieldsFormIsRefusedAndNotStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewRepositoryMetricsStore(newFirestoreMemoryBackend(), nil)
	handler := NewHandler(HandlerOptions{Metrics: store})
	for _, value := range hostileValues {
		for field, metric := range hostileMetrics(value) {
			recorder := post(handler, MetricsPath, metric)
			if recorder.Code != http.StatusBadRequest || strings.TrimSpace(recorder.Body.String()) != `{"error":"invalid_metric_record"}` {
				t.Errorf("%s = %q: POST = %d %s, want 400 invalid_metric_record and nothing else", field, value, recorder.Code, recorder.Body.String())
			}
			// The store refuses it too, whoever calls it, and names the field, never the value.
			err := store.SaveMetric(ctx, metric)
			if !errors.Is(err, errInvalidMetricRecord) || strings.Contains(err.Error(), value) {
				t.Errorf("%s = %q: SaveMetric = %v, want the refusal with no value in it", field, value, err)
			}
		}
	}
	if stored, err := store.ListMetrics(ctx, ""); err != nil || len(stored) != 0 {
		t.Fatalf("after the refusals the store holds %d metrics (%v), want none", len(stored), err)
	}

	// What a real reporter sends is stored, and bounds are bounds.
	if recorder := post(handler, MetricsPath, validMetric()); recorder.Code != http.StatusCreated {
		t.Fatalf("a valid metric = %d %s", recorder.Code, recorder.Body.String())
	}
	if stored, found, err := store.GetMetric(ctx, "sneat-dev/wb", MetricTypeCommitsPerDay); err != nil || !found || stored.Repository != "github.com/sneat-dev/wb" || len(stored.Dimensions) != 2 {
		t.Fatalf("the valid metric was stored as %+v (%v, %v)", stored, found, err)
	}
	for name, change := range map[string]func(*RepositoryMetric){
		"a long formatted value":         func(m *RepositoryMetric) { m.FormattedValue = strings.Repeat("9", maxFormattedValueLen+1) },
		"a long metadata value":          func(m *RepositoryMetric) { m.Metadata["note"] = strings.Repeat("a", maxRecordTextBytes+1) },
		"text that is not UTF-8":         func(m *RepositoryMetric) { m.Metadata["note"] = "\xff\xfe" },
		"a dimension with no name":       func(m *RepositoryMetric) { m.Dimensions[0].Name = "" },
		"an http workflow address":       func(m *RepositoryMetric) { m.Metadata["workflow_run_url"] = "http://github.com/x" },
		"an address with no host":        func(m *RepositoryMetric) { m.Metadata["workflow_run_url"] = "https:///x" },
		"an address that will not parse": func(m *RepositoryMetric) { m.Metadata["workflow_run_url"] = "https://%zz" },
		"too many dimensions": func(m *RepositoryMetric) {
			m.Dimensions = make([]MetricDimension, maxRecordDimensions+1)
		},
		"too many metadata entries": func(m *RepositoryMetric) {
			for index := range maxRecordMapEntries + 1 {
				m.Metadata["k"+strings.Repeat("x", index)] = index
			}
		},
	} {
		metric := validMetric()
		change(&metric)
		if err := validateMetricRecord(metric); !errors.Is(err, errInvalidMetricRecord) {
			t.Errorf("%s: validation = %v, want a refusal", name, err)
		}
	}
	// An empty record is left to the store's own "is required" refusals.
	if err := validateMetricRecord(RepositoryMetric{}); err != nil {
		t.Errorf("an empty metric: validation = %v, want it left to the store", err)
	}
}

// The same for a coverage report, which the metrics page shows as the
// test_coverage metric.
func TestACoverageReportWithAValueOutsideItsFieldsFormIsRefusedAndNotStored(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	store := NewRepositoryCoverageStore(newFirestoreMemoryBackend())
	handler := NewHandler(HandlerOptions{Coverage: store})
	for _, value := range hostileValues {
		for field, record := range hostileCoverage(value) {
			recorder := post(handler, CoveragePath, record)
			if recorder.Code != http.StatusBadRequest || strings.TrimSpace(recorder.Body.String()) != `{"error":"invalid_coverage_record"}` {
				t.Errorf("%s = %q: POST = %d %s, want 400 invalid_coverage_record and nothing else", field, value, recorder.Code, recorder.Body.String())
			}
			err := store.SaveCoverage(ctx, record)
			if !errors.Is(err, errInvalidCoverageRecord) || strings.Contains(err.Error(), value) {
				t.Errorf("%s = %q: SaveCoverage = %v, want the refusal with no value in it", field, value, err)
			}
		}
	}
	if stored, err := store.ListCoverage(ctx); err != nil || len(stored) != 0 {
		t.Fatalf("after the refusals the store holds %d reports (%v), want none", len(stored), err)
	}
	if recorder := post(handler, CoveragePath, validCoverage()); recorder.Code != http.StatusCreated {
		t.Fatalf("a valid coverage report = %d %s", recorder.Code, recorder.Body.String())
	}
	// The metric built from the stored report is itself a valid metric.
	stored, found, err := store.GetCoverage(ctx, "sneat-dev/wb")
	if err != nil || !found || validateMetricRecord(CoverageToRepositoryMetric(stored)) != nil {
		t.Fatalf("the stored report %+v (%v, %v) does not make a valid metric: %v", stored, found, err, validateMetricRecord(CoverageToRepositoryMetric(stored)))
	}
	for name, change := range map[string]func(*StoredRepositoryCoverage){
		"a module with no path":  func(r *StoredRepositoryCoverage) { r.Modules[0].Path = "" },
		"a package with no name": func(r *StoredRepositoryCoverage) { r.Packages[""] = quality.PackageSummary{} },
		"too many modules": func(r *StoredRepositoryCoverage) {
			r.Modules = make([]quality.ModuleCoverageSummary, maxRecordDimensions+1)
		},
		"an http workflow address": func(r *StoredRepositoryCoverage) { r.WorkflowRunURL = "http://github.com/x" },
	} {
		record := validCoverage()
		change(&record)
		if err := validateCoverageRecord(record); !errors.Is(err, errInvalidCoverageRecord) {
			t.Errorf("%s: validation = %v, want a refusal", name, err)
		}
	}
	if err := validateCoverageRecord(StoredRepositoryCoverage{}); err != nil {
		t.Errorf("an empty report: validation = %v, want it left to the store", err)
	}
}
