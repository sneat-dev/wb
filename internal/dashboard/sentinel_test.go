package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
)

// planted marks every value that the dashboard's two JSON routes must never
// repeat: they are read with no session, on the loopback address.
const planted = "SENTINEL-"

// keysOf is the sorted keys of a decoded JSON object.
func keysOf(object map[string]any) []string {
	keys := make([]string, 0, len(object))
	for key := range object {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// jsonNames is the sorted JSON field names of a struct type.
func jsonNames(value any) []string {
	kind := reflect.TypeOf(value)
	var names []string
	for index := range kind.NumField() {
		name, _, _ := strings.Cut(kind.Field(index).Tag.Get("json"), ",")
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// TestHealthAndOverviewCarryNoPathAndNothingOutsideTheirFieldSets feeds the
// dashboard's sources a sentinel wherever a path, a commit, an argument digest
// or an identifier of a command could leak from (the projects root itself, a
// worktree's recorded path and base, its run telemetry), and requires the two
// routes an anonymous local reader may read to answer none of it, and exactly
// their pinned field sets, while what they are for does arrive.
func TestHealthAndOverviewCarryNoPathAndNothingOutsideTheirFieldSets(t *testing.T) {
	t.Parallel()
	projectsRoot := filepath.Join(t.TempDir(), planted+"projects-root")
	repository := tailCovFleet(t, projectsRoot)
	worktree := filepath.Join(repository, ".worktrees", "task-a")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	// The manifest records the worktree's own path, which holds the sentinel root.
	tailCovWriteManifest(t, worktree, "task-a", "acme/widgets", "agent-7", "codex", "2026-09-06T10:00:00Z")
	tailCovWriteRunEvents(t, worktree,
		`{"schema_version":1,"timestamp":"2026-09-06T11:00:00Z","operation_id":"`+planted+`operation","state":"requested","kind":"test","args_sha256":"`+planted+`args-digest","argument_count":3,"repository":"acme/widgets","effort_id":"task-a","run_id":"`+planted+`run"}`,
		`{"schema_version":1,"timestamp":"2026-09-06T11:01:00Z","operation_id":"`+planted+`operation","state":"succeeded","kind":"test","args_sha256":"`+planted+`args-digest","argument_count":3,"repository":"acme/widgets","effort_id":"task-a","run_id":"`+planted+`run","duration_ms":1500,"exit_code":0}`,
	)
	// The heartbeat records the command line that was run: its time is read, the
	// command never repeated.
	heartbeat, _ := json.Marshal(map[string]any{"at": now.Add(-time.Minute), "command": "wb run -- deploy --token " + planted + "secret"})
	if err := os.WriteFile(filepath.Join(worktree, ".wb", "local", "heartbeat.json"), heartbeat, 0o600); err != nil {
		t.Fatal(err)
	}
	handler := NewHandler(Options{
		ProjectsRoot: projectsRoot, Version: "1.2.3", DaemonPID: 4242, SchedulerGeneration: 7,
		Now: func() time.Time { return now }, InventoryIndexPath: filepath.Join(t.TempDir(), planted+"index.json"),
		LogPath: filepath.Join(t.TempDir(), planted+"daemon.log"),
		Hub: func(context.Context) HubHealth {
			return HubHealth{Mounted: true, Polling: true, RepositoriesPolled: 3, LastEventReceived: &HubDeliveryMarker{ID: "delivery-1", Event: "push", OccurredAt: now}}
		},
	})
	read := func(target string) (string, map[string]any) {
		t.Helper()
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, loopbackRequest(target))
		if recorder.Code != http.StatusOK {
			t.Fatalf("%s = %d %s", target, recorder.Code, recorder.Body.String())
		}
		var decoded map[string]any
		if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
			t.Fatal(err)
		}
		headers := ""
		for name, values := range recorder.Header() {
			headers += name + strings.Join(values, "")
		}
		return recorder.Body.String() + headers, decoded
	}
	healthBody, health := read("/api/v1/health")
	overviewBody, overview := read("/api/v1/overview")
	for target, body := range map[string]string{"/api/v1/health": healthBody, "/api/v1/overview": overviewBody} {
		if strings.Contains(body, planted) || strings.Contains(body, projectsRoot) || strings.Contains(body, ".worktrees") {
			start := max(strings.Index(body, planted), 0)
			t.Errorf("%s carries a path or a forbidden source field: ...%s...", target, body[start:min(len(body), start+80)])
		}
	}
	// What the routes are for does arrive, so the test is not vacuous.
	for _, want := range []string{`"task":"task-a"`, `"repository":"acme/widgets"`, `"owner":"codex/agent-7"`, `"operations":1`, `"kind":"test"`} {
		if !strings.Contains(overviewBody, want) {
			t.Errorf("the overview lacks %s: %s", want, overviewBody)
		}
	}
	// The field sets, pinned: a field added to either route is added here deliberately.
	if got, want := keysOf(health), []string{"daemon_pid", "hub", "machine", "scheduler_generation", "schema_version", "status", "wb_version"}; !slices.Equal(got, want) {
		t.Errorf("health fields = %v, want exactly %v", got, want)
	}
	for name, test := range map[string]struct {
		value any
		want  []string
	}{
		"Overview":           {Overview{}, []string{"diagnostics", "generated_at", "inventory", "machine", "operations", "schema_version", "worktrees"}},
		"Machine":            {Machine{}, []string{"name", "wb_version"}},
		"Worktree":           {Worktree{}, []string{"age_seconds", "branch", "last_activity_at", "owner", "owner_state", "repository", "task"}},
		"Inventory":          {Inventory{}, []string{"cache_hit", "observed_at", "source_fingerprint"}},
		"HubHealth":          {HubHealth{}, []string{"last_event_acknowledged", "last_event_received", "mounted", "poll_interval_seconds", "polling", "repositories_polled", "webhook_redelivery"}},
		"HubDeliveryMarker":  {HubDeliveryMarker{}, []string{"event", "id", "occurred_at"}},
		"HubRedeliverySweep": {HubRedeliverySweep{}, []string{"abandoned", "last_failure_at", "last_failure_class", "last_sweep_at", "redelivered", "uncounted"}},
	} {
		if got := jsonNames(test.value); !slices.Equal(got, test.want) {
			t.Errorf("%s fields = %v, want exactly %v", name, got, test.want)
		}
	}
	if got, want := keysOf(overview), jsonNames(Overview{}); !slices.Equal(got, want) {
		t.Errorf("the overview answered the fields %v, want %v", got, want)
	}
	operations, _ := overview["operations"].(map[string]any)
	if got, want := keysOf(operations), []string{"failed", "kinds", "operations", "running", "since", "system_cpu_ms", "user_cpu_ms", "wall_ms"}; !slices.Equal(got, want) {
		t.Errorf("the overview's operations answered the fields %v, want %v", got, want)
	}
}
