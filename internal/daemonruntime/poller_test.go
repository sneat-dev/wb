package daemonruntime

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDaemonStatusReportsPollingFromTheRunningDaemon(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	configPath := memoryHubConfig(t)
	deps.HubConfigPath = func() string { return configPath }
	at := time.Date(2026, 9, 11, 14, 0, 0, 0, time.UTC)
	deps.HubHealth = func(context.Context, string) (HubStatus, error) {
		return HubStatus{
			RepositoriesPolled: 7,
			LastEventReceived:  &HubEventMarker{ID: "poll:acme_app:default_branch_updated:abc", Event: "default_branch_updated", OccurredAt: at},
			LastEventAcknowledged: &HubEventMarker{
				ID: "poll:acme_app:default_branch_updated:abc", Event: "default_branch_updated", OccurredAt: at,
			},
		}, nil
	}
	status := NewController(deps, root).hubStatus(context.Background(), "127.0.0.1:8765")
	if !status.Polling || status.PollInterval != "20m0s" || status.RepositoriesPolled != 7 {
		t.Fatalf("hub status = %+v", status)
	}
	if status.LastEventReceived == nil || status.LastEventAcknowledged == nil {
		t.Fatalf("delivery markers = %+v", status)
	}

	// The daemon is not running: the declaration still answers, and the live
	// numbers stay zero rather than failing status.
	deps.HubHealth = func(context.Context, string) (HubStatus, error) {
		return HubStatus{}, errors.New("connection refused")
	}
	offline := NewController(deps, root).hubStatus(context.Background(), "127.0.0.1:8765")
	if !offline.Polling || offline.RepositoriesPolled != 0 || offline.LastEventReceived != nil {
		t.Fatalf("offline hub status = %+v", offline)
	}

	// A daemon that has never been started has no listen address to ask.
	if unaddressed := NewController(deps, root).hubStatus(context.Background(), ""); unaddressed.RepositoriesPolled != 0 {
		t.Fatalf("unaddressed hub status = %+v", unaddressed)
	}
	deps.HubHealth = nil
	if unwired := NewController(deps, root).hubStatus(context.Background(), "127.0.0.1:8765"); unwired.RepositoriesPolled != 0 {
		t.Fatalf("unwired hub status = %+v", unwired)
	}
}

func TestDaemonHubHealthReadsTheServingDaemon(t *testing.T) {
	ctx := context.Background()
	for _, testCase := range []struct {
		name    string
		handler http.HandlerFunc
		wantErr bool
		want    int
	}{
		{
			name: "a hub block is returned",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"status":"ready","hub":{"mounted":true,"polling":true,"repositories_polled":3}}`))
			},
			want: 3,
		},
		{
			name: "a daemon without a hub says so",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte(`{"status":"ready"}`))
			},
			wantErr: true,
		},
		{
			name:    "an error status is not an answer",
			handler: func(writer http.ResponseWriter, _ *http.Request) { writer.WriteHeader(http.StatusInternalServerError) },
			wantErr: true,
		},
		{
			name: "a body that is not JSON is not an answer",
			handler: func(writer http.ResponseWriter, _ *http.Request) {
				_, _ = writer.Write([]byte("<html>"))
			},
			wantErr: true,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			server := httptest.NewServer(testCase.handler)
			defer server.Close()
			status, err := daemonHubHealth(ctx, strings.TrimPrefix(server.URL, "http://"))
			if testCase.wantErr {
				if err == nil {
					t.Fatalf("hub health = %+v, want an error", status)
				}
				return
			}
			if err != nil || status.RepositoriesPolled != testCase.want {
				t.Fatalf("hub health = %+v, %v", status, err)
			}
		})
	}

	if _, err := daemonHubHealth(ctx, "127.0.0.1:0"); err == nil {
		t.Fatal("an unreachable daemon answered")
	}
	if _, err := daemonHubHealth(ctx, "\x7f"); err == nil {
		t.Fatal("an unbuildable request was made")
	}
}
