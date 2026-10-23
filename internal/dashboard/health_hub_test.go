package dashboard

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestHealthReportsHubStateAndOmitsZeroIdentity(t *testing.T) {
	t.Parallel()
	handler := NewHandler(Options{
		Version: "test",
		Hub: func(context.Context) HubHealth {
			return HubHealth{Mounted: true, Polling: true, RepositoriesPolled: 3, PollIntervalSeconds: 15}
		},
	})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, loopbackRequest("/api/v1/health"))
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
