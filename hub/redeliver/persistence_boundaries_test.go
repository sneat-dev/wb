package redeliver

import (
	"context"
	"errors"
	"github.com/sneat-dev/wb/hub"
	"net/http"
	"testing"
	"time"
)

func TestFailedRedeliveryEvidencePublicationPreservesSpentAttempts(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"attempt limit", "uncounted timestamp", "rejected final attempt", "uncounted API failure"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
			api := newFakeGitHubAppAPI(func() time.Time { return now })
			status := http.StatusInternalServerError
			if phase == "uncounted timestamp" || phase == "uncounted API failure" {
				status = http.StatusBadGateway
			}
			previousID := api.seed("guid", "push", status, now.Add(-2*time.Hour))
			api.seed("guid", "push", status, now.Add(-time.Minute))
			server := api.server()
			t.Cleanup(server.Close)
			store := newFakeStore()
			previous := hub.WebhookRedeliveryRecord{GUID: "guid", Attempts: 1, LastAttemptAt: now.Add(-time.Hour), LastAttemptDeliveryID: previousID, FirstDeliveredAt: now.Add(-2 * time.Hour)}
			if phase == "attempt limit" {
				previous.Attempts = MaxAttempts
			}
			if phase == "rejected final attempt" {
				previous.Attempts = MaxAttempts - 1
			}
			store.records["guid"] = previous
			wantCalls, wantAttempts, wantClass := 0, previous.Attempts, "store"
			fault := errors.New("evidence store unavailable")
			switch phase {
			case "rejected final attempt":
				api.redeliverStatus = http.StatusUnprocessableEntity
				store.saveErrAtCall = map[int]error{2: fault}
				wantCalls, wantAttempts = 1, MaxAttempts
			case "uncounted API failure":
				api.redeliverStatus = http.StatusInternalServerError
				wantCalls, wantClass = 1, "server_error"
			default:
				store.saveErr = fault
			}
			sweeper := New(Options{AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM, Client: server.Client(), APIBaseURL: server.URL, Store: store, Now: func() time.Time { return now }})
			sweeper.Sweep(context.Background())
			got, found := store.get("guid")
			if !found || got.Attempts != wantAttempts || got.Abandoned {
				t.Fatalf("failed evidence changed attempts: %+v found=%t", got, found)
			}
			if status := sweeper.Status(); status.LastFailureClass != wantClass || status.Abandoned != 0 || status.Redelivered != 0 {
				t.Fatalf("failure status=%+v", status)
			}
			if calls := len(api.redeliveredIDs()); calls != wantCalls {
				t.Fatalf("redelivery calls=%d want=%d", calls, wantCalls)
			}
			if wantCalls == 0 && got != previous {
				t.Fatalf("failed prepublication changed durable evidence: %+v", got)
			}
		})
	}
}

func TestBlankDeliveryGUIDCannotBeRedeliveredOrPersisted(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	api := newFakeGitHubAppAPI(func() time.Time { return now })
	api.seed("", "push", http.StatusInternalServerError, now.Add(-time.Minute))
	server := api.server()
	t.Cleanup(server.Close)
	store := newFakeStore()
	sweeper := New(Options{AppID: 1, PrivateKeyPEM: testAppPrivateKeyPEM, Client: server.Client(), APIBaseURL: server.URL, Store: store, Now: func() time.Time { return now }})
	sweeper.Sweep(context.Background())
	if len(api.redeliveredIDs()) != 0 {
		t.Fatal("blank GUID was redelivered")
	}
	if _, found := store.get(""); found {
		t.Fatal("blank GUID acquired durable evidence")
	}
	if status := sweeper.Status(); status.Redelivered != 0 || status.Abandoned != 0 || status.Uncounted != 0 || status.LastFailureClass != "" {
		t.Fatalf("blank GUID counted=%+v", status)
	}
}
