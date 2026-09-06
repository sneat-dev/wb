package dashboard

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDashboardServesUIAndHealth(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "1.2.3"})

	for _, test := range []struct {
		path        string
		contentType string
		contains    string
	}{
		{path: "/", contentType: "text/html", contains: "WB operations"},
		{path: "/api/v1/health", contentType: "application/json", contains: `"status":"ready"`},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d", test.path, response.Code)
		}
		if contentType := response.Header().Get("Content-Type"); !strings.HasPrefix(contentType, test.contentType) {
			t.Errorf("%s content type = %q", test.path, contentType)
		}
		if !strings.Contains(response.Body.String(), test.contains) {
			t.Errorf("%s body does not contain %q", test.path, test.contains)
		}
		if response.Header().Get("Content-Security-Policy") == "" {
			t.Errorf("%s omitted security headers", test.path)
		}
	}
}

func TestOverviewPersistsReadOnlyFleetIndex(t *testing.T) {
	projectsRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(projectsRoot, "acme", "widgets", ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	indexPath := filepath.Join(t.TempDir(), "fleet-inventory.json")
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	handler := NewHandler(Options{
		ProjectsRoot: projectsRoot, Version: "test",
		CacheTTL: time.Second, InventoryIndexPath: indexPath, InventoryIndexTTL: time.Minute,
		Now: func() time.Time { return now },
	})
	load := func() Overview {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil)
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("overview status = %d, body = %s", response.Code, response.Body.String())
		}
		var overview Overview
		if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
			t.Fatal(err)
		}
		return overview
	}
	first := load()
	if _, err := os.Stat(indexPath); err != nil {
		t.Fatalf("persisted fleet index: %v", err)
	}
	if first.Inventory.CacheHit || first.Inventory.SourceFingerprint == "" {
		t.Fatalf("first inventory = %#v", first.Inventory)
	}
	now = now.Add(2 * time.Second)
	second := load()
	if !second.Inventory.CacheHit || !second.Inventory.ObservedAt.Equal(first.GeneratedAt) {
		t.Fatalf("second inventory = %#v, first generated at %s", second.Inventory, first.GeneratedAt)
	}
}
