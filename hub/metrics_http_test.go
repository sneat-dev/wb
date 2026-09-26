package hub

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

type failingMetricsStore struct {
	failTypes   bool
	failList    bool
	failGet     bool
	failSave    bool
	emptyReturn bool
}

func (f failingMetricsStore) SaveMetric(ctx context.Context, metric RepositoryMetric) error {
	if f.failSave {
		return errors.New("save failed")
	}
	return nil
}

func (f failingMetricsStore) GetMetric(ctx context.Context, repository, metricType string) (RepositoryMetric, bool, error) {
	if f.failGet {
		return RepositoryMetric{}, false, errors.New("get failed")
	}
	return RepositoryMetric{}, false, nil
}

func (f failingMetricsStore) ListMetrics(ctx context.Context, metricType string) ([]RepositoryMetric, error) {
	if f.failList {
		return nil, errors.New("list failed")
	}
	if f.emptyReturn {
		return nil, nil
	}
	return []RepositoryMetric{}, nil
}

func (f failingMetricsStore) ListMetricTypes(ctx context.Context) ([]MetricTypeDefinition, error) {
	if f.failTypes {
		return nil, errors.New("types failed")
	}
	if f.emptyReturn {
		return nil, nil
	}
	return []MetricTypeDefinition{}, nil
}

func TestMetricsEndpoints(t *testing.T) {
	t.Parallel()

	// 1. Metrics unavailable when Metrics is nil
	handlerWithoutMetrics := NewHandler(HandlerOptions{})
	req := httptest.NewRequest(http.MethodGet, MetricsPath, nil)
	rec := httptest.NewRecorder()
	handlerWithoutMetrics.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s without store status = %d, want %d", MetricsPath, rec.Code, http.StatusServiceUnavailable)
	}

	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/types", nil)
	rec = httptest.NewRecorder()
	handlerWithoutMetrics.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s/types without store status = %d, want %d", MetricsPath, rec.Code, http.StatusServiceUnavailable)
	}

	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/owner/repo", nil)
	rec = httptest.NewRecorder()
	handlerWithoutMetrics.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s/owner/repo without store status = %d, want %d", MetricsPath, rec.Code, http.StatusServiceUnavailable)
	}

	req = httptest.NewRequest(http.MethodPost, MetricsPath, bytes.NewReader([]byte("{}")))
	rec = httptest.NewRecorder()
	handlerWithoutMetrics.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("POST %s without store status = %d, want %d", MetricsPath, rec.Code, http.StatusServiceUnavailable)
	}

	// 2. Configure with in-memory store
	backend := newFirestoreMemoryBackend()
	covBackend := newFirestoreMemoryBackend()
	covStore := NewRepositoryCoverageStore(covBackend)
	metricsStore := NewRepositoryMetricsStore(backend, covStore)
	handler := NewHandler(HandlerOptions{Metrics: metricsStore})

	// GET /types
	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/types", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/types status = %d, want %d", MetricsPath, rec.Code, http.StatusOK)
	}
	var types []MetricTypeDefinition
	if err := json.NewDecoder(rec.Body).Decode(&types); err != nil {
		t.Fatalf("decode types: %v", err)
	}
	if len(types) == 0 {
		t.Error("expected non-empty metric types list")
	}

	// GET empty list
	req = httptest.NewRequest(http.MethodGet, MetricsPath, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s status = %d, want %d", MetricsPath, rec.Code, http.StatusOK)
	}
	var emptyList []RepositoryMetric
	if err := json.NewDecoder(rec.Body).Decode(&emptyList); err != nil {
		t.Fatalf("decode empty list: %v", err)
	}
	if len(emptyList) != 0 {
		t.Errorf("empty list len = %d, want 0", len(emptyList))
	}

	// POST invalid JSON
	req = httptest.NewRequest(http.MethodPost, MetricsPath, bytes.NewReader([]byte("{invalid-json")))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST %s invalid JSON status = %d, want %d", MetricsPath, rec.Code, http.StatusBadRequest)
	}

	// POST invalid payload (missing repo)
	req = httptest.NewRequest(http.MethodPost, MetricsPath, bytes.NewReader([]byte(`{"metric_type":"test"}`)))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("POST %s missing repo status = %d, want %d", MetricsPath, rec.Code, http.StatusBadRequest)
	}

	// POST valid metric record
	metric := RepositoryMetric{
		Repository: "sneat-dev/wb",
		MetricType: MetricTypeCommitsPerDay,
		ReportedAt: time.Now().UTC(),
		Value:      5.5,
		Dimensions: []MetricDimension{
			{Name: "2026-09-26", Value: 6},
		},
	}
	payload, _ := json.Marshal(metric)
	req = httptest.NewRequest(http.MethodPost, MetricsPath, bytes.NewReader(payload))
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST %s valid record status = %d, want %d: %s", MetricsPath, rec.Code, http.StatusCreated, rec.Body.String())
	}

	// GET single metric (found)
	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/sneat-dev/wb?type="+MetricTypeCommitsPerDay, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s/sneat-dev/wb status = %d, want %d", MetricsPath, rec.Code, http.StatusOK)
	}
	var gotMetric RepositoryMetric
	if err := json.NewDecoder(rec.Body).Decode(&gotMetric); err != nil {
		t.Fatalf("decode metric: %v", err)
	}
	if gotMetric.Repository != "github.com/sneat-dev/wb" || gotMetric.Value != 5.5 {
		t.Errorf("got metric mismatch: %+v", gotMetric)
	}

	// Test getMetric directly with URL path fallback (when PathValue is empty)
	recDirect := httptest.NewRecorder()
	reqDirect := httptest.NewRequest(http.MethodGet, MetricsPath+"/sneat-dev/wb?type="+MetricTypeCommitsPerDay, nil)
	api := apiHandler{options: HandlerOptions{Metrics: metricsStore}}
	api.getMetric(recDirect, reqDirect)
	if recDirect.Code != http.StatusOK {
		t.Errorf("direct getMetric status = %d, want 200", recDirect.Code)
	}

	// GET single metric (not found)
	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/other/repo?type=unknown", nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET %s/other/repo status = %d, want %d", MetricsPath, rec.Code, http.StatusNotFound)
	}

	// GET list with query type
	req = httptest.NewRequest(http.MethodGet, MetricsPath+"?type="+MetricTypeCommitsPerDay, nil)
	rec = httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET %s?type=... status = %d, want %d", MetricsPath, rec.Code, http.StatusOK)
	}
	var list []RepositoryMetric
	if err := json.NewDecoder(rec.Body).Decode(&list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Errorf("list len = %d, want 1", len(list))
	}
}

func TestMetricsErrorsAndAuthorization(t *testing.T) {
	t.Parallel()

	// 1. Authorization rejection
	authHandler := NewHandler(HandlerOptions{
		ViewerResolver: testViewerResolver{resolve: func(*http.Request) (Viewer, error) { return Viewer{Authenticated: false}, nil }},
		Metrics:        NewRepositoryMetricsStore(newFirestoreMemoryBackend(), nil),
	})

	for _, path := range []string{MetricsPath, MetricsPath + "/types", MetricsPath + "/owner/repo"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		rec := httptest.NewRecorder()
		authHandler.ServeHTTP(rec, req)
		if rec.Code != http.StatusUnauthorized {
			t.Errorf("GET %s unauth status = %d, want %d", path, rec.Code, http.StatusUnauthorized)
		}
	}

	reqPost := httptest.NewRequest(http.MethodPost, MetricsPath, bytes.NewReader([]byte("{}")))
	recPost := httptest.NewRecorder()
	authHandler.ServeHTTP(recPost, reqPost)
	if recPost.Code != http.StatusUnauthorized {
		t.Errorf("POST %s unauth status = %d, want %d", MetricsPath, recPost.Code, http.StatusUnauthorized)
	}

	// 2. Store failures
	failStore := failingMetricsStore{failTypes: true, failList: true, failGet: true}
	failHandler := NewHandler(HandlerOptions{Metrics: failStore})

	req := httptest.NewRequest(http.MethodGet, MetricsPath+"/types", nil)
	rec := httptest.NewRecorder()
	failHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s/types failure status = %d, want 503", MetricsPath, rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, MetricsPath, nil)
	rec = httptest.NewRecorder()
	failHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s failure status = %d, want 503", MetricsPath, rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/owner/repo", nil)
	rec = httptest.NewRecorder()
	failHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("GET %s/owner/repo failure status = %d, want 503", MetricsPath, rec.Code)
	}

	// 3. Nil slices in successful store responses
	nilStore := failingMetricsStore{emptyReturn: true}
	nilHandler := NewHandler(HandlerOptions{Metrics: nilStore})

	req = httptest.NewRequest(http.MethodGet, MetricsPath+"/types", nil)
	rec = httptest.NewRecorder()
	nilHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET %s/types nil slice status = %d, want 200", MetricsPath, rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, MetricsPath, nil)
	rec = httptest.NewRecorder()
	nilHandler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Errorf("GET %s nil slice status = %d, want 200", MetricsPath, rec.Code)
	}
}
