package githubapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

type testReadModel struct{ visibility Visibility }

func (model testReadModel) Dashboard(context.Context, Viewer) (Access[Dashboard], error) {
	return Access[Dashboard]{Visibility: model.visibility, Value: Dashboard{Summary: Summary{Repositories: 3}}}, nil
}
func (model testReadModel) Stats(_ context.Context, _ Viewer, scope Scope, id string) (Access[Stat], error) {
	return Access[Stat]{Visibility: model.visibility, Value: Stat{Scope: scope, ID: id, DisplayName: "private-name"}}, nil
}
func (model testReadModel) Series(_ context.Context, _ Viewer, scope Scope, id, metric string) (Access[Series], error) {
	return Access[Series]{Visibility: model.visibility, Value: Series{Scope: scope, ID: id, Metric: metric}}, nil
}
func (model testReadModel) Leaderboard(context.Context, Viewer, string) (Access[Leaderboard], error) {
	return Access[Leaderboard]{Visibility: model.visibility, Value: Leaderboard{}}, nil
}
func (model testReadModel) LatestMerges(context.Context, Viewer, int) (Access[[]LatestMerge], error) {
	return Access[[]LatestMerge]{Visibility: model.visibility, Value: []LatestMerge{}}, nil
}

type testDeliveries struct {
	claimed bool
	wakeups []Wakeup
}

func (store *testDeliveries) HasDelivery(_ context.Context, _ string) (bool, error) {
	return store.claimed, nil
}
func (store *testDeliveries) CommitDeliveryAndWakeup(_ context.Context, _ string, wakeup Wakeup) (bool, error) {
	if store.claimed {
		return false, nil
	}
	store.claimed = true
	store.wakeups = append(store.wakeups, wakeup)
	return true, nil
}

type testReader struct{ deliveries []WebhookDelivery }

func (reader *testReader) Refresh(_ context.Context, delivery WebhookDelivery) error {
	reader.deliveries = append(reader.deliveries, delivery)
	return nil
}

type testEvents struct {
	replay []Event
	live   chan Event
	filter EventFilter
}

func (events *testEvents) Replay(_ context.Context, _ EventFilter) ([]Event, error) {
	return events.replay, nil
}
func (events *testEvents) Subscribe(_ context.Context, filter EventFilter) (<-chan Event, error) {
	events.filter = filter
	return events.live, nil
}

func TestHandlerReturnsPublicDashboardAndExactCORS(t *testing.T) {
	handler := NewHandler(HandlerOptions{Service: Service{ReadModel: testReadModel{visibility: VisibilityPublic}}})
	request := httptest.NewRequest(http.MethodGet, APIPrefix+"/dashboard", nil)
	request.Header.Set("Origin", UIOrigin)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != UIOrigin {
		t.Errorf("allow origin = %q, want %q", got, UIOrigin)
	}
	if !strings.Contains(response.Body.String(), `"repositories":3`) {
		t.Errorf("response omitted dashboard summary: %s", response.Body.String())
	}
}

func TestHandlerHidesPrivateSubjectsFromAnonymousViewer(t *testing.T) {
	handler := NewHandler(HandlerOptions{Service: Service{ReadModel: testReadModel{visibility: VisibilityPrivate}}})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/stats/repository/secret", nil))

	if response.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusNotFound)
	}
	if strings.Contains(response.Body.String(), "private-name") {
		t.Fatalf("private subject leaked in response: %s", response.Body.String())
	}
}

func TestHandlerRejectsUnapprovedCORSPreflight(t *testing.T) {
	handler := NewHandler(HandlerOptions{})
	request := httptest.NewRequest(http.MethodOptions, APIPrefix+"/dashboard", nil)
	request.Header.Set("Origin", "https://attacker.example")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusForbidden)
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("unexpected CORS origin %q", got)
	}
}

func TestWebhookRefreshesBeforeDurableDedupedWakeup(t *testing.T) {
	secret := []byte("webhook-secret")
	store := &testDeliveries{}
	reader := &testReader{}
	handler := NewHandler(HandlerOptions{Service: Service{
		WebhookSecret:       secret,
		Deliveries:          store,
		AuthoritativeReader: reader,
	}})
	body := `{"repository":{"full_name":"sneat-dev/wb"}}`
	request := httptest.NewRequest(http.MethodPost, APIPrefix+"/github/webhook", strings.NewReader(body))
	request.Header.Set("X-GitHub-Delivery", "delivery-1")
	request.Header.Set("X-GitHub-Event", "pull_request")
	request.Header.Set("X-Hub-Signature-256", signed(secret, []byte(body)))
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusAccepted, response.Body.String())
	}
	if len(reader.deliveries) != 1 {
		t.Fatalf("authoritative reads = %d, want 1", len(reader.deliveries))
	}
	if len(store.wakeups) != 1 || store.wakeups[0].Key != "sneat-dev/wb" {
		t.Fatalf("wakeups = %#v", store.wakeups)
	}

	duplicateRequest := httptest.NewRequest(http.MethodPost, APIPrefix+"/github/webhook", strings.NewReader(body))
	duplicateRequest.Header = request.Header.Clone()
	duplicate := httptest.NewRecorder()
	handler.ServeHTTP(duplicate, duplicateRequest.WithContext(context.Background()))
	if duplicate.Code != http.StatusAccepted || len(store.wakeups) != 1 || len(reader.deliveries) != 1 {
		t.Fatalf("duplicate response = %d, wakeups = %#v, reads = %#v", duplicate.Code, store.wakeups, reader.deliveries)
	}
}

func TestWebhookRejectsUnsignedPayload(t *testing.T) {
	handler := NewHandler(HandlerOptions{Service: Service{
		WebhookSecret:       []byte("secret"),
		Deliveries:          &testDeliveries{},
		AuthoritativeReader: &testReader{},
	}})
	request := httptest.NewRequest(http.MethodPost, APIPrefix+"/github/webhook", strings.NewReader(`{}`))
	request.Header.Set("X-GitHub-Delivery", "delivery-1")
	request.Header.Set("X-GitHub-Event", "push")
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if response.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestEventStreamReplaysWithMonotonicCursorAndFiltersPrivateEvents(t *testing.T) {
	live := make(chan Event, 2)
	live <- Event{ID: 4, Type: EventCI, Visibility: VisibilityPrivate, Payload: []byte(`{"secret":true}`)}
	live <- Event{ID: 5, Type: EventCleanup, Visibility: VisibilityPublic, Payload: []byte(`{"done":true}`)}
	close(live)
	source := &testEvents{
		replay: []Event{
			{ID: 2, Type: EventQueue, Visibility: VisibilityPublic, Payload: []byte(`{"queued":1}`)},
			{ID: 3, Type: EventJobProgress, Visibility: VisibilityPrivate, Payload: []byte(`{"secret":true}`)},
		},
		live: live,
	}
	service := Service{Events: source}
	replay, updates, err := service.EventStream(context.Background(), Viewer{}, EventFilter{After: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(replay) != 1 || replay[0].ID != 2 {
		t.Fatalf("replay = %#v, want public event 2 only", replay)
	}
	if source.filter.After != 3 {
		t.Fatalf("subscribe after = %d, want 3", source.filter.After)
	}
	var got []Event
	for event := range updates {
		got = append(got, event)
	}
	if len(got) != 1 || got[0].ID != 5 || got[0].Type != EventCleanup {
		t.Fatalf("live updates = %#v, want public event 5 only", got)
	}
}

func TestEventsSSEUsesCursorAndEventNames(t *testing.T) {
	live := make(chan Event)
	close(live)
	source := &testEvents{replay: []Event{{ID: 8, Type: EventDaemonGeneration, Visibility: VisibilityPublic, Payload: []byte(`{"generation":2}`)}}, live: live}
	handler := NewHandler(HandlerOptions{Service: Service{Events: source}})
	request := httptest.NewRequest(http.MethodGet, APIPrefix+"/events?after=7", nil)
	request.Header.Set("Origin", UIOrigin)
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, request)

	if got := response.Header().Get("Content-Type"); got != "text/event-stream" {
		t.Fatalf("content type = %q", got)
	}
	if body := response.Body.String(); !strings.Contains(body, "id: 8\nevent: daemon.generation\ndata: {\"generation\":2}\n") {
		t.Fatalf("SSE body = %q", body)
	}
	if source.filter.After != 8 {
		t.Fatalf("subscribe after = %d, want 8", source.filter.After)
	}
}

func signed(secret, body []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}
