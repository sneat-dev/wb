package dashboard

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestMetricsDashboardRoutes(t *testing.T) {
	t.Parallel()

	handler := NewHandler(Options{
		ProjectsRoot: t.TempDir(),
		Version:      "1.0.0",
	})

	// 1. GET /metrics returns the metrics HTML
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /metrics status = %d, want 200", rec.Code)
	}
	if contentType := rec.Header().Get("Content-Type"); !strings.HasPrefix(contentType, "text/html") {
		t.Errorf("content type = %q, want text/html", contentType)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "WB Metrics") {
		t.Errorf("body does not contain 'WB Metrics'")
	}
	if !strings.Contains(body, "Repositories with Least Coverage") {
		t.Errorf("body does not contain 'Repositories with Least Coverage'")
	}
	if !strings.Contains(body, "Most Active Repositories") {
		t.Errorf("body does not contain 'Most Active Repositories'")
	}
	if !strings.Contains(body, "Active &amp; Abandoned Worktrees") {
		t.Errorf("body does not contain 'Active & Abandoned Worktrees'")
	}

	// 2. GET /coverage redirects to /metrics?type=test_coverage
	reqCov := httptest.NewRequest(http.MethodGet, "/coverage", nil)
	recCov := httptest.NewRecorder()
	handler.ServeHTTP(recCov, reqCov)
	if recCov.Code != http.StatusFound {
		t.Fatalf("GET /coverage status = %d, want 302", recCov.Code)
	}
	if loc := recCov.Header().Get("Location"); loc != "/metrics?type=test_coverage" {
		t.Errorf("redirect location = %q, want /metrics?type=test_coverage", loc)
	}

	// 3. Direct method calls on server
	server := &service{}
	recDirect := httptest.NewRecorder()
	server.metrics(recDirect, httptest.NewRequest("GET", "/metrics", nil))
	if recDirect.Code != http.StatusOK || !strings.Contains(recDirect.Body.String(), "WB Metrics") {
		t.Errorf("direct metrics call failed")
	}

	recDirectRedir := httptest.NewRecorder()
	server.coverageRedirect(recDirectRedir, httptest.NewRequest("GET", "/coverage", nil))
	if recDirectRedir.Code != http.StatusFound {
		t.Errorf("direct coverageRedirect status = %d, want 302", recDirectRedir.Code)
	}
}
