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
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "1.2.3", DaemonPID: 123, SchedulerGeneration: 45})

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

// TestPeersOptionIsMountedWithoutConflictingTheCatchAllIndex is a regression
// test for a real startup panic this task introduced and fixed: NewHandler
// once registered Peers at the unqualified pattern "/api/v1/peers", which
// conflicts with the "GET /" catch-all index route registered just above it
// ("matches more methods... but has a more specific path") — Go 1.22's
// ServeMux panics on that ambiguity at registration time, which means every
// `wb daemon serve` would have panicked on startup, hub or no hub, since
// serveDashboard always sets Peers. The route must be GET-qualified, must
// answer for both the list path and a nested detail path, and must leave
// every other route (the index, health) unaffected.
func TestPeersOptionIsMountedWithoutConflictingTheCatchAllIndex(t *testing.T) {
	var peersCalls []string
	peers := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		peersCalls = append(peersCalls, r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"schema_version":1,"peers":[]}`))
	})
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "1.2.3", Peers: peers})

	for _, path := range []string{"/api/v1/peers", "/api/v1/peers/machine_1"} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
		if response.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", path, response.Code)
		}
	}
	if len(peersCalls) != 2 {
		t.Fatalf("peers handler calls = %v, want both paths reached", peersCalls)
	}

	// The index and health routes must still answer as before.
	index := httptest.NewRecorder()
	handler.ServeHTTP(index, httptest.NewRequest(http.MethodGet, "/", nil))
	if index.Code != http.StatusOK {
		t.Fatalf("index status = %d, want 200", index.Code)
	}
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("health status = %d, want 200", health.Code)
	}
}

// TestPeersOptionOmittedLeavesTheRouteUnmounted proves the nil default (a
// caller that has not set Peers, matching every caller before this task)
// keeps its previous behaviour: no /api/v1/peers route exists, so it falls
// through to the catch-all index like any other unknown path.
func TestPeersOptionOmittedLeavesTheRouteUnmounted(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "1.2.3"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/peers", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), "WB operations") {
		t.Fatalf("status = %d, body = %q, want the catch-all index", response.Code, response.Body.String())
	}
}

func TestDashboardHealthReportsDaemonIdentity(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "1.2.3", DaemonPID: 123, SchedulerGeneration: 45})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
	var payload struct {
		PID        int    `json:"daemon_pid"`
		Generation uint64 `json:"scheduler_generation"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload.PID != 123 || payload.Generation != 45 {
		t.Fatalf("health identity = %#v", payload)
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

// TestMountsAreServedNextToTheExistingRoutes is what lets `wb daemon serve`
// host the bench hub and its dashboard on the same loopback listener without
// moving anything that was already there.
func TestMountsAreServedNextToTheExistingRoutes(t *testing.T) {
	mounted := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		_, _ = writer.Write([]byte("mounted " + request.URL.Path))
	})
	handler := NewHandler(Options{
		ProjectsRoot: t.TempDir(),
		Version:      "test",
		Mounts: map[string]http.Handler{
			"/v0/workbench/": mounted,
			"/workbench/":    mounted,
			// Refused shapes: a prefix must start and end with "/", and a nil
			// handler is ignored rather than panicking the mux.
			"no-slash/": mounted,
			"/no-slash": mounted,
			"/nil/":     nil,
		},
	})

	for _, target := range []string{"/v0/workbench/github/status", "/workbench/dashboard/", "/workbench"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, target, nil))
		if recorder.Code != http.StatusOK || !strings.HasPrefix(recorder.Body.String(), "mounted ") {
			t.Fatalf("%s = %d %q", target, recorder.Code, recorder.Body.String())
		}
	}

	// The pre-existing routes keep answering exactly as before.
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), `"status"`) {
		t.Fatalf("health = %d %q", recorder.Code, recorder.Body.String())
	}
	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/nil/", nil))
	if recorder.Code != http.StatusOK || strings.HasPrefix(recorder.Body.String(), "mounted ") {
		t.Fatalf("a nil mount was registered: %d %q", recorder.Code, recorder.Body.String())
	}
}

// TestNoMountsLeavesTheHandlerUnchanged is the promise made to every operator
// who does not self-host.
func TestNoMountsLeavesTheHandlerUnchanged(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "test"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/workbench/dashboard/", nil))
	if recorder.Code != http.StatusOK || !strings.Contains(recorder.Body.String(), "<") {
		t.Fatalf("/workbench/dashboard/ = %d; want the catch-all index page", recorder.Code)
	}
}

func TestLogIsUnavailableWithoutALogPath(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "test"})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", recorder.Code)
	}
}

func TestLogServesATailOfTheRuntimeLogFile(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "daemon.log")
	if err := os.WriteFile(logPath, []byte("line one\nline two\nline three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "test", LogPath: logPath})

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if !strings.HasPrefix(recorder.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("content type = %q", recorder.Header().Get("Content-Type"))
	}
	if recorder.Header().Get("X-Log-Truncated") != "false" {
		t.Fatalf("truncated = %q; want false for a short file", recorder.Header().Get("X-Log-Truncated"))
	}
	if recorder.Body.String() != "line one\nline two\nline three\n" {
		t.Fatalf("body = %q", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log?tail=9", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("status = %d", recorder.Code)
	}
	if recorder.Header().Get("X-Log-Truncated") != "true" {
		t.Fatalf("truncated = %q; want true once tail cuts the file short", recorder.Header().Get("X-Log-Truncated"))
	}
	if recorder.Body.String() != "ne three\n" {
		t.Fatalf("tail body = %q", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log?tail=not-a-number", nil))
	if recorder.Code != http.StatusBadRequest {
		t.Fatalf("status = %d; want 400 for an invalid tail", recorder.Code)
	}
}

func TestLogReportsUnavailableWhenTheFileIsMissing(t *testing.T) {
	handler := NewHandler(Options{ProjectsRoot: t.TempDir(), Version: "test", LogPath: filepath.Join(t.TempDir(), "missing.log")})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log", nil))
	if recorder.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d; want 503", recorder.Code)
	}
}
