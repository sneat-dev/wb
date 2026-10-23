package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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

// TestHealthCarriesNoPathAndNothingOutsideItsFieldSet feeds the health route's
// sources a sentinel wherever a path could leak from (the log path), and
// requires the route an anonymous local reader may read to answer none of it,
// and exactly its pinned field set, while what it is for does arrive.
func TestHealthCarriesNoPathAndNothingOutsideItsFieldSet(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	logPath := filepath.Join(t.TempDir(), planted+"daemon.log")
	handler := NewHandler(Options{
		Version: "1.2.3", DaemonPID: 4242, SchedulerGeneration: 7,
		LogPath: logPath,
		Hub: func(context.Context) HubHealth {
			return HubHealth{Mounted: true, Polling: true, RepositoriesPolled: 3, LastEventReceived: &HubDeliveryMarker{ID: "delivery-1", Event: "push", OccurredAt: now}}
		},
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, loopbackRequest("/api/v1/health"))
	if recorder.Code != http.StatusOK {
		t.Fatalf("health = %d %s", recorder.Code, recorder.Body.String())
	}
	var health map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &health); err != nil {
		t.Fatal(err)
	}
	body := recorder.Body.String()
	for name, values := range recorder.Header() {
		body += name + strings.Join(values, "")
	}
	if strings.Contains(body, planted) || strings.Contains(body, logPath) {
		t.Errorf("health carries a path or a forbidden source field: %s", body)
	}
	// What the route is for does arrive, so the test is not vacuous.
	if !strings.Contains(body, `"wb_version":"1.2.3"`) || !strings.Contains(body, `"repositories_polled":3`) {
		t.Errorf("health lacks its identity or hub state: %s", body)
	}
	// The field sets, pinned: a field added to the route is added here deliberately.
	if got, want := keysOf(health), []string{"daemon_pid", "hub", "machine", "scheduler_generation", "schema_version", "status", "wb_version"}; !slices.Equal(got, want) {
		t.Errorf("health fields = %v, want exactly %v", got, want)
	}
	for name, test := range map[string]struct {
		value any
		want  []string
	}{
		"HubHealth":          {HubHealth{}, []string{"last_event_acknowledged", "last_event_received", "mounted", "poll_interval_seconds", "polling", "repositories_polled", "webhook_redelivery"}},
		"HubDeliveryMarker":  {HubDeliveryMarker{}, []string{"event", "id", "occurred_at"}},
		"HubRedeliverySweep": {HubRedeliverySweep{}, []string{"abandoned", "last_failure_at", "last_failure_class", "last_sweep_at", "redelivered", "uncounted"}},
	} {
		if got := jsonNames(test.value); !slices.Equal(got, test.want) {
			t.Errorf("%s fields = %v, want exactly %v", name, got, test.want)
		}
	}
}
