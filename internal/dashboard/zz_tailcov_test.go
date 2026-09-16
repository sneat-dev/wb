package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
)

func tailCovWriteManifest(t *testing.T, worktree, effortID, repository, agentID, agentRuntime, createdAt string) {
	t.Helper()
	directory := filepath.Join(worktree, ".wb", "local")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	manifest := "version: 1\n" +
		"effort_id: " + effortID + "\n" +
		"effort_kind: task\n" +
		"repository: " + repository + "\n" +
		"worktree: " + worktree + "\n" +
		"branch: agent/" + effortID + "\n" +
		"base: main\n" +
		"base_sha: " + strings.Repeat("a", 40) + "\n" +
		"created_at: " + createdAt + "\n" +
		"agent_id: " + agentID + "\n" +
		"agent_runtime: " + agentRuntime + "\n" +
		"provenance: created\n"
	if err := os.WriteFile(filepath.Join(directory, "manifest.yaml"), []byte(manifest), 0o600); err != nil {
		t.Fatal(err)
	}
}

func tailCovWriteHeartbeat(t *testing.T, worktree string, at time.Time) {
	t.Helper()
	directory := filepath.Join(worktree, ".wb", "local")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	record, err := json.Marshal(map[string]any{"at": at.UTC(), "command": "wb run -- true"})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, "heartbeat.json"), record, 0o600); err != nil {
		t.Fatal(err)
	}
}

func tailCovWriteRunEvents(t *testing.T, worktree string, lines ...string) {
	t.Helper()
	directory := filepath.Join(worktree, ".wb", "local", "run")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	content := strings.Join(lines, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(directory, "events.jsonl"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

// tailCovFleet creates one canonical repository with a .worktrees root.
func tailCovFleet(t *testing.T, projectsRoot string) string {
	t.Helper()
	repository := filepath.Join(projectsRoot, "acme", "widgets")
	if err := os.MkdirAll(filepath.Join(repository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(repository, ".worktrees"), 0o755); err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestTailCovHealthReportsHubStateAndOmitsZeroIdentity(t *testing.T) {
	handler := NewHandler(Options{
		ProjectsRoot: t.TempDir(),
		Version:      "test",
		Hub: func(context.Context) HubHealth {
			return HubHealth{Mounted: true, Polling: true, RepositoriesPolled: 3, PollIntervalSeconds: 15}
		},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/health", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("health status = %d", response.Code)
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if _, present := payload["daemon_pid"]; present {
		t.Fatalf("zero daemon pid must be omitted: %#v", payload)
	}
	if _, present := payload["scheduler_generation"]; present {
		t.Fatalf("zero scheduler generation must be omitted: %#v", payload)
	}
	hub, ok := payload["hub"].(map[string]any)
	if !ok {
		t.Fatalf("hub payload = %#v", payload["hub"])
	}
	if hub["mounted"] != true || hub["polling"] != true || hub["repositories_polled"] != float64(3) {
		t.Fatalf("hub = %#v", hub)
	}
}

func TestTailCovOverviewDescribesEveryWorktreeAndRunEvent(t *testing.T) {
	projectsRoot := t.TempDir()
	repository := tailCovFleet(t, projectsRoot)
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)

	active := filepath.Join(repository, ".worktrees", "task-a")
	stale := filepath.Join(repository, ".worktrees", "task-b")
	broken := filepath.Join(repository, ".worktrees", "task-broken")
	for _, directory := range []string{active, stale, broken} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	tailCovWriteManifest(t, active, "task-a", "acme/widgets", "agent-7", "codex", "2026-09-06T10:00:00Z")
	tailCovWriteHeartbeat(t, active, now.Add(-time.Minute))
	tailCovWriteRunEvents(t, active,
		`{"schema_version":1,"timestamp":"2026-09-06T11:00:00Z","operation_id":"op-1","state":"requested","kind":"test"}`,
		`{"schema_version":1,"timestamp":"2026-09-06T11:01:00Z","operation_id":"op-1","state":"succeeded","kind":"test","duration_ms":60000}`)

	tailCovWriteManifest(t, stale, "task-b", "acme/widgets", "", "", "2026-09-01T10:00:00Z")
	// A directory without a manifest is a diagnostic, not a worktree.
	// A plain file inside .worktrees is skipped outright.
	if err := os.WriteFile(filepath.Join(repository, ".worktrees", "notes.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	// A second repository proves the comparator's repository-ordering branch.
	otherRepository := filepath.Join(projectsRoot, "acme", "gadgets")
	otherWorktree := filepath.Join(otherRepository, ".worktrees", "task-c")
	if err := os.MkdirAll(filepath.Join(otherRepository, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(otherWorktree, 0o755); err != nil {
		t.Fatal(err)
	}
	tailCovWriteManifest(t, otherWorktree, "task-c", "acme/gadgets", "agent-9", "claude", "2026-09-06T11:30:00Z")

	handler := NewHandler(Options{ProjectsRoot: projectsRoot, Version: "test", Now: func() time.Time { return now }})
	overview := tailCovLoadOverview(t, handler)

	if overview.SchemaVersion != APISchemaVersion || !overview.GeneratedAt.Equal(now) {
		t.Fatalf("overview header = %#v", overview)
	}
	if overview.Diagnostics != 1 {
		t.Fatalf("diagnostics = %d, want the manifest-less directory counted once", overview.Diagnostics)
	}
	if len(overview.Worktrees) != 3 {
		t.Fatalf("worktrees = %#v", overview.Worktrees)
	}
	wantOrder := []string{"task-c", "task-a", "task-b"}
	for index, want := range wantOrder {
		if overview.Worktrees[index].Task != want {
			t.Fatalf("worktrees are not sorted by repository then task: %#v", overview.Worktrees)
		}
	}
	first := overview.Worktrees[1]
	if first.OwnerState != "active" || first.Owner != "codex/agent-7" || first.Repository != "acme/widgets" {
		t.Fatalf("active worktree = %#v", first)
	}
	if !first.LastActivityAt.Equal(now.Add(-time.Minute)) || first.AgeSeconds != int64((2*time.Hour).Seconds()) {
		t.Fatalf("active worktree activity = %#v", first)
	}
	second := overview.Worktrees[2]
	if second.OwnerState != "idle" || second.Owner != "" {
		t.Fatalf("stale worktree = %#v", second)
	}
	if !second.LastActivityAt.Equal(time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC)) {
		t.Fatalf("stale activity = %s, want the manifest creation time", second.LastActivityAt)
	}
	if overview.Operations.Operations != 1 || overview.Operations.WallMS != 60000 {
		t.Fatalf("operations = %#v", overview.Operations)
	}

	// A second request inside the default cache TTL is served from the cache.
	cached := tailCovLoadOverview(t, handler)
	if !cached.GeneratedAt.Equal(overview.GeneratedAt) || cached.Diagnostics != overview.Diagnostics {
		t.Fatalf("cached overview = %#v", cached)
	}
}

func TestTailCovOverviewReportsUnreadableWorktreesDirectory(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("running as root: directory permission bits do not deny reads")
	}
	projectsRoot := t.TempDir()
	repository := tailCovFleet(t, projectsRoot)
	worktreesRoot := filepath.Join(repository, ".worktrees")
	if err := os.Chmod(worktreesRoot, 0o000); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(worktreesRoot, 0o755) })

	handler := NewHandler(Options{ProjectsRoot: projectsRoot, Version: "test"})
	overview := tailCovLoadOverview(t, handler)
	if overview.Diagnostics != 1 {
		t.Fatalf("diagnostics = %d, want the unreadable .worktrees directory counted", overview.Diagnostics)
	}
}

func TestTailCovOverviewFailsOnCorruptRunTelemetry(t *testing.T) {
	projectsRoot := t.TempDir()
	repository := tailCovFleet(t, projectsRoot)
	worktree := filepath.Join(repository, ".worktrees", "task-a")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	tailCovWriteManifest(t, worktree, "task-a", "acme/widgets", "agent-7", "codex", "2026-09-06T10:00:00Z")
	tailCovWriteRunEvents(t, worktree, "{not json")

	handler := NewHandler(Options{ProjectsRoot: projectsRoot, Version: "test"})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, body = %s", response.Code, response.Body.String())
	}
	var payload map[string]any
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if payload["error"] != "overview_unavailable" {
		t.Fatalf("payload = %#v", payload)
	}
	if message, _ := payload["message"].(string); !strings.Contains(message, "run telemetry") {
		t.Fatalf("message = %q", message)
	}
}

func TestTailCovOverviewUsesHomeCacheWhenNoIndexPathIsConfigured(t *testing.T) {
	projectsRoot := t.TempDir()
	repository := tailCovFleet(t, projectsRoot)
	worktree := filepath.Join(repository, ".worktrees", "task-a")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	tailCovWriteManifest(t, worktree, "task-a", "acme/widgets", "agent-7", "codex", "2026-09-06T10:00:00Z")

	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)
	handler := NewHandler(Options{ProjectsRoot: projectsRoot, Version: "test"})
	overview := tailCovLoadOverview(t, handler)
	if overview.Diagnostics != 0 {
		t.Fatalf("diagnostics = %d, want none", overview.Diagnostics)
	}
	if _, err := os.Stat(filepath.Join(home, "cache", "fleet-inventory-v1.json")); err != nil {
		t.Fatalf("default home cache index was not written: %v", err)
	}
}

func TestTailCovOverviewCountsUnavailableWBHomeAsADiagnostic(t *testing.T) {
	projectsRoot := t.TempDir()
	tailCovFleet(t, projectsRoot)

	// WB_HOME pointing at a regular file cannot be created as a directory.
	homeFile := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(homeFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(wbhome.EnvOverride, homeFile)

	handler := NewHandler(Options{ProjectsRoot: projectsRoot, Version: "test"})
	overview := tailCovLoadOverview(t, handler)
	if overview.Diagnostics != 1 {
		t.Fatalf("diagnostics = %d, want the unavailable WB home counted once", overview.Diagnostics)
	}
}

func TestTailCovBuildOverviewRejectsUnreadableProjectsRoot(t *testing.T) {
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	if _, err := BuildOverview(context.Background(), filepath.Join(t.TempDir(), "absent"), "test", now); err == nil {
		t.Fatal("BuildOverview must fail for a missing projects root")
	}

	projectsRoot := t.TempDir()
	tailCovFleet(t, projectsRoot)
	overview, err := BuildOverview(context.Background(), projectsRoot, "1.2.3", now)
	if err != nil {
		t.Fatal(err)
	}
	if overview.Machine.Version != "1.2.3" || !overview.GeneratedAt.Equal(now) {
		t.Fatalf("overview = %#v", overview)
	}
}

func tailCovLoadOverview(t *testing.T, handler http.Handler) Overview {
	t.Helper()
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/api/v1/overview", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("overview status = %d, body = %s", response.Code, response.Body.String())
	}
	var overview Overview
	if err := json.Unmarshal(response.Body.Bytes(), &overview); err != nil {
		t.Fatal(err)
	}
	return overview
}
