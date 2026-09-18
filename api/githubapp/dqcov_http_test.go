package githubapp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// dqCovReadModel is a scriptable ReadModel used to drive the HTTP boundary:
// every method returns its configured error when set, so the handler's
// disclosure and status mapping can be asserted without a real store.
type dqCovReadModel struct {
	visibility Visibility
	err        error
	limit      int
}

func (model *dqCovReadModel) Dashboard(context.Context, Viewer) (Access[Dashboard], error) {
	if model.err != nil {
		return Access[Dashboard]{}, model.err
	}
	return Access[Dashboard]{Visibility: model.visibility, Value: Dashboard{Summary: Summary{Repositories: 1}}}, nil
}

func (model *dqCovReadModel) Stats(_ context.Context, _ Viewer, scope Scope, id string) (Access[Stat], error) {
	if model.err != nil {
		return Access[Stat]{}, model.err
	}
	return Access[Stat]{Visibility: model.visibility, Value: Stat{Scope: scope, ID: id, DisplayName: "listed"}}, nil
}

func (model *dqCovReadModel) Series(_ context.Context, _ Viewer, scope Scope, id, metric string) (Access[Series], error) {
	if model.err != nil {
		return Access[Series]{}, model.err
	}
	return Access[Series]{Visibility: model.visibility, Value: Series{Scope: scope, ID: id, Metric: metric, Points: []SeriesPoint{{Value: 7}}}}, nil
}

func (model *dqCovReadModel) Leaderboard(_ context.Context, _ Viewer, metric string) (Access[Leaderboard], error) {
	if model.err != nil {
		return Access[Leaderboard]{}, model.err
	}
	return Access[Leaderboard]{Visibility: model.visibility, Value: Leaderboard{Metric: metric, Entries: []LeaderboardEntry{{Rank: 1}}}}, nil
}

func (model *dqCovReadModel) LatestMerges(_ context.Context, _ Viewer, limit int) (Access[[]LatestMerge], error) {
	model.limit = limit
	if model.err != nil {
		return Access[[]LatestMerge]{}, model.err
	}
	return Access[[]LatestMerge]{Visibility: model.visibility, Value: []LatestMerge{{Repository: "github.com/acme/app"}}}, nil
}

// dqCovViewerResolver lets a test force the host identity boundary to fail.
type dqCovViewerResolver struct{ err error }

func (resolver dqCovViewerResolver) Viewer(*http.Request) (Viewer, error) {
	return Viewer{}, resolver.err
}

// dqCovEventSource records the filters it is given and returns scripted
// results, so cursor and validation behavior is observable.
type dqCovEventSource struct {
	replay        []Event
	live          chan Event
	replayErr     error
	subscribeErr  error
	replayFilter  EventFilter
	liveFilter    EventFilter
	replayCalls   int
	subscribeCall int
}

func (source *dqCovEventSource) Replay(_ context.Context, filter EventFilter) ([]Event, error) {
	source.replayCalls++
	source.replayFilter = filter
	return source.replay, source.replayErr
}

func (source *dqCovEventSource) Subscribe(_ context.Context, filter EventFilter) (<-chan Event, error) {
	source.subscribeCall++
	source.liveFilter = filter
	if source.subscribeErr != nil {
		return nil, source.subscribeErr
	}
	return source.live, nil
}

func dqCovClosedEvents() chan Event {
	live := make(chan Event)
	close(live)
	return live
}

// dqCovNonFlusher is an http.ResponseWriter that deliberately does not
// implement http.Flusher, mirroring a host adapter that cannot stream.
type dqCovNonFlusher struct {
	header http.Header
	body   bytes.Buffer
	status int
}

func (writer *dqCovNonFlusher) Header() http.Header {
	if writer.header == nil {
		writer.header = http.Header{}
	}
	return writer.header
}

func (writer *dqCovNonFlusher) Write(data []byte) (int, error) { return writer.body.Write(data) }

func (writer *dqCovNonFlusher) WriteHeader(status int) { writer.status = status }

// dqCovFailingDeliveries is a ProjectionDeliveryStore whose durable preflight
// fails with a non-signature error.
type dqCovFailingDeliveries struct{ err error }

func (store dqCovFailingDeliveries) HasDelivery(context.Context, string) (bool, error) {
	return false, store.err
}
func (store dqCovFailingDeliveries) ClaimDelivery(context.Context, string) (bool, error) {
	return false, store.err
}
func (store dqCovFailingDeliveries) ReleaseDelivery(context.Context, string) error { return nil }
func (store dqCovFailingDeliveries) CommitDeliveryAndWakeup(context.Context, string, Wakeup) (bool, error) {
	return false, store.err
}

func TestDQCovResolveViewerFailureFailsClosedForEveryEndpoint(t *testing.T) {
	handler := NewHandler(HandlerOptions{
		Service: Service{
			ReadModel: &dqCovReadModel{},
			Worktrees: dqCovWorktreeModel{},
			Events:    &dqCovEventSource{live: dqCovClosedEvents()},
		},
		ViewerResolver: dqCovViewerResolver{err: errors.New("no host session")},
	})
	targets := []string{
		APIPrefix + "/dashboard",
		APIPrefix + "/stats/repository/github.com/acme/app",
		APIPrefix + "/series?scope=repository&id=github.com/acme/app&metric=merged",
		APIPrefix + "/leaderboards?metric=merged",
		APIPrefix + "/latest-merges",
		APIPrefix + "/worktrees",
		APIPrefix + "/events",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
			if response.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusUnauthorized, response.Body.String())
			}
			if body := response.Body.String(); !strings.Contains(body, "viewer_unavailable") {
				t.Fatalf("body = %q, want viewer_unavailable", body)
			}
		})
	}
}

func TestDQCovStatsHandlerRejectsInvalidScopeAndMissingID(t *testing.T) {
	handler := NewHandler(HandlerOptions{Service: Service{ReadModel: &dqCovReadModel{visibility: VisibilityPublic}}})
	targets := []string{
		APIPrefix + "/stats/planet/github.com/acme/app",
		APIPrefix + "/stats/repository/",
	}
	for _, target := range targets {
		t.Run(target, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
			if response.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusBadRequest, response.Body.String())
			}
			if body := response.Body.String(); !strings.Contains(body, "invalid_stats_scope") {
				t.Fatalf("body = %q, want invalid_stats_scope", body)
			}
		})
	}
}

func TestDQCovSeriesHandlerServesAndValidatesQuery(t *testing.T) {
	handler := NewHandler(HandlerOptions{Service: Service{ReadModel: &dqCovReadModel{visibility: VisibilityPublic}}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/series?scope=repository&id=github.com/acme/app&metric=merged", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var series Series
	if err := json.Unmarshal(response.Body.Bytes(), &series); err != nil {
		t.Fatal(err)
	}
	if series.Scope != ScopeRepository || series.ID != "github.com/acme/app" || series.Metric != "merged" || len(series.Points) != 1 || series.Points[0].Value != 7 {
		t.Fatalf("series = %#v", series)
	}

	for _, target := range []string{
		APIPrefix + "/series?scope=planet&id=github.com/acme/app&metric=merged",
		APIPrefix + "/series?scope=repository&metric=merged",
		APIPrefix + "/series?scope=repository&id=github.com/acme/app",
	} {
		t.Run(target, func(t *testing.T) {
			rejected := httptest.NewRecorder()
			handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, target, nil))
			if rejected.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want %d: %s", rejected.Code, http.StatusBadRequest, rejected.Body.String())
			}
			if body := rejected.Body.String(); !strings.Contains(body, "scope_id_and_metric_are_required") {
				t.Fatalf("body = %q", body)
			}
		})
	}
}

func TestDQCovLeaderboardHandlerServesAndRequiresMetric(t *testing.T) {
	handler := NewHandler(HandlerOptions{Service: Service{ReadModel: &dqCovReadModel{visibility: VisibilityPublic}}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/leaderboards?metric=landed", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
	}
	var board Leaderboard
	if err := json.Unmarshal(response.Body.Bytes(), &board); err != nil {
		t.Fatal(err)
	}
	if board.Metric != "landed" || len(board.Entries) != 1 || board.Entries[0].Rank != 1 {
		t.Fatalf("leaderboard = %#v", board)
	}

	rejected := httptest.NewRecorder()
	handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, APIPrefix+"/leaderboards", nil))
	if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "metric_is_required") {
		t.Fatalf("missing metric = %d %s", rejected.Code, rejected.Body.String())
	}
}

func TestDQCovLatestMergesHandlerBoundsLimit(t *testing.T) {
	model := &dqCovReadModel{visibility: VisibilityPublic}
	handler := NewHandler(HandlerOptions{Service: Service{ReadModel: model}})

	defaulted := httptest.NewRecorder()
	handler.ServeHTTP(defaulted, httptest.NewRequest(http.MethodGet, APIPrefix+"/latest-merges", nil))
	if defaulted.Code != http.StatusOK || model.limit != 20 {
		t.Fatalf("default status/limit = %d/%d: %s", defaulted.Code, model.limit, defaulted.Body.String())
	}
	if body := defaulted.Body.String(); !strings.Contains(body, "github.com/acme/app") {
		t.Fatalf("body = %q", body)
	}

	explicit := httptest.NewRecorder()
	handler.ServeHTTP(explicit, httptest.NewRequest(http.MethodGet, APIPrefix+"/latest-merges?limit=5", nil))
	if explicit.Code != http.StatusOK || model.limit != 5 {
		t.Fatalf("explicit status/limit = %d/%d", explicit.Code, model.limit)
	}

	for _, raw := range []string{"abc", "0", "101", "-1"} {
		t.Run(raw, func(t *testing.T) {
			rejected := httptest.NewRecorder()
			handler.ServeHTTP(rejected, httptest.NewRequest(http.MethodGet, APIPrefix+"/latest-merges?limit="+raw, nil))
			if rejected.Code != http.StatusBadRequest || !strings.Contains(rejected.Body.String(), "limit_must_be_between_1_and_100") {
				t.Fatalf("limit %q = %d %s", raw, rejected.Code, rejected.Body.String())
			}
		})
	}
}

func TestDQCovHandlerMapsUnconfiguredAndFailingControlPlane(t *testing.T) {
	unconfigured := NewHandler(HandlerOptions{})
	response := httptest.NewRecorder()
	unconfigured.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/dashboard", nil))
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "control_plane_not_configured") {
		t.Fatalf("unconfigured = %d %s", response.Code, response.Body.String())
	}

	failing := NewHandler(HandlerOptions{Service: Service{ReadModel: &dqCovReadModel{err: errors.New("read model down")}}})
	response = httptest.NewRecorder()
	failing.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/dashboard", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "control_plane_error") {
		t.Fatalf("failing = %d %s", response.Code, response.Body.String())
	}
}

func TestDQCovEventsHandlerResolvesCursorAndRejectsBadFilters(t *testing.T) {
	source := &dqCovEventSource{live: dqCovClosedEvents()}
	handler := NewHandler(HandlerOptions{Service: Service{Events: source}})
	cases := []struct {
		name   string
		target string
		header string
		want   uint64
	}{
		{name: "query cursor", target: APIPrefix + "/events?after=4", want: 4},
		{name: "last event id header", target: APIPrefix + "/events", header: "9", want: 9},
		{name: "default cursor", target: APIPrefix + "/events", want: 0},
		{name: "query wins over header", target: APIPrefix + "/events?after=6", header: "9", want: 6},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			source.liveFilter = EventFilter{}
			source.replayCalls = 0
			request := httptest.NewRequest(http.MethodGet, test.target, nil)
			if test.header != "" {
				request.Header.Set("Last-Event-ID", test.header)
			}
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, request)
			if response.Code != http.StatusOK {
				t.Fatalf("status = %d, want %d: %s", response.Code, http.StatusOK, response.Body.String())
			}
			if source.replayCalls != 1 || source.replayFilter.After != test.want {
				t.Fatalf("replay calls/after = %d/%d, want 1/%d", source.replayCalls, source.replayFilter.After, test.want)
			}
		})
	}

	for _, target := range []string{
		APIPrefix + "/events?after=notanumber",
		APIPrefix + "/events?since=yesterday",
	} {
		t.Run(target, func(t *testing.T) {
			source.replayCalls = 0
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, target, nil))
			if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "invalid_event_cursor") {
				t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
			}
			if source.replayCalls != 0 {
				t.Fatalf("invalid cursor reached the event source %d times", source.replayCalls)
			}
		})
	}
}

func TestDQCovEventsHandlerMapsStreamSetupFailure(t *testing.T) {
	source := &dqCovEventSource{replayErr: errors.New("event log down"), live: make(chan Event)}
	handler := NewHandler(HandlerOptions{Service: Service{Events: source}})
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/events", nil))
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "control_plane_error") {
		t.Fatalf("status/body = %d %s", response.Code, response.Body.String())
	}
}

func TestDQCovEventsHandlerRequiresFlusher(t *testing.T) {
	source := &dqCovEventSource{live: dqCovClosedEvents()}
	handler := NewHandler(HandlerOptions{Service: Service{Events: source}})
	writer := &dqCovNonFlusher{}
	handler.ServeHTTP(writer, httptest.NewRequest(http.MethodGet, APIPrefix+"/events", nil))
	if writer.status != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", writer.status, http.StatusInternalServerError)
	}
	if body := writer.body.String(); !strings.Contains(body, "streaming_not_supported") {
		t.Fatalf("body = %q", body)
	}
}

func TestDQCovEventsHandlerStreamsLiveAndStopsOnCancelledContext(t *testing.T) {
	source := &dqCovEventSource{live: make(chan Event, 1)}
	source.live <- Event{ID: 1, Type: EventQueue, Visibility: VisibilityPublic, Payload: []byte(`{"queued":true}`)}
	close(source.live)
	handler := NewHandler(HandlerOptions{Service: Service{Events: source}})
	response := httptest.NewRecorder()

	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, APIPrefix+"/events", nil))

	body := response.Body.String()
	if !strings.Contains(body, "id: 1\nevent: queue\ndata: {\"queued\":true}\n\n") {
		t.Fatalf("streamed body = %q", body)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	blocked := &dqCovEventSource{live: make(chan Event)}
	cancelled := httptest.NewRecorder()
	NewHandler(HandlerOptions{Service: Service{Events: blocked}}).
		ServeHTTP(cancelled, httptest.NewRequest(http.MethodGet, APIPrefix+"/events", nil).WithContext(ctx))
	if cancelled.Code != http.StatusOK {
		t.Fatalf("cancelled status = %d, want %d", cancelled.Code, http.StatusOK)
	}
}

func TestDQCovWebhookRejectsOversizedPayloadAndMapsNonSignatureFailure(t *testing.T) {
	secret := []byte("webhook-secret")
	handler := NewHandler(HandlerOptions{Service: Service{Projector: &ProjectionEngine{
		WebhookSecret: secret, Deliveries: &testDeliveries{}, Reader: &testReader{},
		Writer: &testProjectionWriter{}, AuthoritativeReader: &testReader{},
	}}})
	oversized := httptest.NewRequest(http.MethodPost, APIPrefix+"/github/webhook", strings.NewReader(strings.Repeat("x", maxWebhookBodyBytes+1)))
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, oversized)
	if response.Code != http.StatusRequestEntityTooLarge || !strings.Contains(response.Body.String(), "webhook_payload_too_large") {
		t.Fatalf("oversized = %d %s", response.Code, response.Body.String())
	}

	payload := `{"repository":{"full_name":"sneat-dev/wb"}}`
	failing := NewHandler(HandlerOptions{Service: Service{Projector: &ProjectionEngine{
		WebhookSecret: secret, Deliveries: dqCovFailingDeliveries{err: errors.New("claim store unavailable")},
		Reader: &testReader{}, Writer: &testProjectionWriter{}, AuthoritativeReader: &testReader{},
	}}})
	request := httptest.NewRequest(http.MethodPost, APIPrefix+"/github/webhook", strings.NewReader(payload))
	request.Header.Set("X-GitHub-Delivery", "delivery-1")
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-Hub-Signature-256", signed(secret, []byte(payload)))
	response = httptest.NewRecorder()
	failing.ServeHTTP(response, request)
	if response.Code != http.StatusInternalServerError || !strings.Contains(response.Body.String(), "control_plane_error") {
		t.Fatalf("processor failure = %d %s", response.Code, response.Body.String())
	}
}
