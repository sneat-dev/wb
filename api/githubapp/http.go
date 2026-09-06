package githubapp

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxWebhookBodyBytes = 1 << 20

// ViewerResolver binds the host authentication and membership system to the
// Workbench domain. The WB domain never accepts identity headers directly.
type ViewerResolver interface {
	Viewer(*http.Request) (Viewer, error)
}

// HandlerOptions supplies the narrow host bindings for the public API.
type HandlerOptions struct {
	Service        Service
	ViewerResolver ViewerResolver
	AllowedOrigin  string
}

// NewHandler returns the Workbench GitHub App API under APIPrefix. It permits
// the Sneat Workbench browser origin only; GitHub webhooks have no CORS need.
func NewHandler(options HandlerOptions) http.Handler {
	if options.AllowedOrigin == "" {
		options.AllowedOrigin = UIOrigin
	}
	handler := apiHandler{options: options}
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+APIPrefix+"/dashboard", handler.dashboard)
	mux.HandleFunc("GET "+APIPrefix+"/stats/{scope}/{id}", handler.stats)
	mux.HandleFunc("GET "+APIPrefix+"/series", handler.series)
	mux.HandleFunc("GET "+APIPrefix+"/leaderboards", handler.leaderboard)
	mux.HandleFunc("GET "+APIPrefix+"/latest-merges", handler.latestMerges)
	mux.HandleFunc("GET "+APIPrefix+"/events", handler.events)
	mux.HandleFunc("POST "+APIPrefix+"/github/webhook", handler.webhook)
	return cors(options.AllowedOrigin, mux)
}

type apiHandler struct{ options HandlerOptions }

func (handler apiHandler) viewer(request *http.Request) (Viewer, error) {
	if handler.options.ViewerResolver == nil {
		return Viewer{}, nil
	}
	return handler.options.ViewerResolver.Viewer(request)
}

func (handler apiHandler) dashboard(writer http.ResponseWriter, request *http.Request) {
	viewer, ok := resolveViewer(writer, request, handler.viewer)
	if !ok {
		return
	}
	value, err := handler.options.Service.Dashboard(request.Context(), viewer)
	writeResult(writer, value, err)
}

func (handler apiHandler) stats(writer http.ResponseWriter, request *http.Request) {
	viewer, ok := resolveViewer(writer, request, handler.viewer)
	if !ok {
		return
	}
	scope := Scope(request.PathValue("scope"))
	if !validScope(scope) || request.PathValue("id") == "" {
		writeError(writer, http.StatusBadRequest, "invalid_stats_scope")
		return
	}
	value, err := handler.options.Service.Stats(request.Context(), viewer, scope, request.PathValue("id"))
	writeResult(writer, value, err)
}

func (handler apiHandler) series(writer http.ResponseWriter, request *http.Request) {
	viewer, ok := resolveViewer(writer, request, handler.viewer)
	if !ok {
		return
	}
	scope := Scope(request.URL.Query().Get("scope"))
	id, metric := request.URL.Query().Get("id"), request.URL.Query().Get("metric")
	if !validScope(scope) || id == "" || metric == "" {
		writeError(writer, http.StatusBadRequest, "scope_id_and_metric_are_required")
		return
	}
	value, err := handler.options.Service.Series(request.Context(), viewer, scope, id, metric)
	writeResult(writer, value, err)
}

func (handler apiHandler) leaderboard(writer http.ResponseWriter, request *http.Request) {
	viewer, ok := resolveViewer(writer, request, handler.viewer)
	if !ok {
		return
	}
	metric := request.URL.Query().Get("metric")
	if metric == "" {
		writeError(writer, http.StatusBadRequest, "metric_is_required")
		return
	}
	value, err := handler.options.Service.Leaderboard(request.Context(), viewer, metric)
	writeResult(writer, value, err)
}

func (handler apiHandler) latestMerges(writer http.ResponseWriter, request *http.Request) {
	viewer, ok := resolveViewer(writer, request, handler.viewer)
	if !ok {
		return
	}
	limit := 20
	if raw := request.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(writer, http.StatusBadRequest, "limit_must_be_between_1_and_100")
			return
		}
		limit = parsed
	}
	value, err := handler.options.Service.LatestMerges(request.Context(), viewer, limit)
	writeResult(writer, value, err)
}

func (handler apiHandler) events(writer http.ResponseWriter, request *http.Request) {
	viewer, ok := resolveViewer(writer, request, handler.viewer)
	if !ok {
		return
	}
	filter, err := eventFilter(request)
	if err != nil {
		writeError(writer, http.StatusBadRequest, "invalid_event_cursor")
		return
	}
	replay, live, err := handler.options.Service.EventStream(request.Context(), viewer, filter)
	if err != nil {
		writeResult(writer, nil, err)
		return
	}
	flusher, ok := writer.(http.Flusher)
	if !ok {
		writeError(writer, http.StatusInternalServerError, "streaming_not_supported")
		return
	}
	writer.Header().Set("Content-Type", "text/event-stream")
	writer.Header().Set("Cache-Control", "no-cache")
	writer.Header().Set("Connection", "keep-alive")
	for _, event := range replay {
		writeSSE(writer, event)
	}
	flusher.Flush()
	for {
		select {
		case <-request.Context().Done():
			return
		case event, open := <-live:
			if !open {
				return
			}
			writeSSE(writer, event)
			flusher.Flush()
		}
	}
}

func eventFilter(request *http.Request) (EventFilter, error) {
	filter := EventFilter{
		Repository: request.URL.Query().Get("repo"),
		Task:       request.URL.Query().Get("task"),
		Operation:  request.URL.Query().Get("operation"),
		Session:    request.URL.Query().Get("session"),
		Severity:   request.URL.Query().Get("severity"),
	}
	raw := request.URL.Query().Get("after")
	if raw == "" {
		raw = request.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		raw = "0"
	}
	cursor, err := strconv.ParseUint(raw, 10, 64)
	if err != nil {
		return EventFilter{}, err
	}
	filter.After = cursor
	if rawSince := request.URL.Query().Get("since"); rawSince != "" {
		filter.Since, err = time.Parse(time.RFC3339, rawSince)
		if err != nil {
			return EventFilter{}, err
		}
	}
	return filter, nil
}

func writeSSE(writer http.ResponseWriter, event Event) {
	payload, _ := json.Marshal(event.Payload)
	_, _ = writer.Write([]byte("id: " + strconv.FormatUint(event.ID, 10) + "\n"))
	_, _ = writer.Write([]byte("event: " + string(event.Type) + "\n"))
	_, _ = writer.Write([]byte("data: " + string(payload) + "\n\n"))
}

func (handler apiHandler) webhook(writer http.ResponseWriter, request *http.Request) {
	request.Body = http.MaxBytesReader(writer, request.Body, maxWebhookBodyBytes)
	payload, err := io.ReadAll(request.Body)
	if err != nil {
		writeError(writer, http.StatusRequestEntityTooLarge, "webhook_payload_too_large")
		return
	}
	delivery := WebhookDelivery{
		ID:      request.Header.Get("X-GitHub-Delivery"),
		Event:   request.Header.Get("X-GitHub-Event"),
		Payload: payload,
	}
	var envelope struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if json.Unmarshal(payload, &envelope) == nil {
		delivery.Repository = envelope.Repository.FullName
	}
	accepted, processErr := handler.options.Service.ProcessWebhook(request.Context(), delivery, request.Header.Get("X-Hub-Signature-256"))
	if processErr != nil {
		if strings.Contains(processErr.Error(), "signature") {
			writeError(writer, http.StatusUnauthorized, "invalid_webhook_signature")
			return
		}
		writeResult(writer, nil, processErr)
		return
	}
	writeJSON(writer, http.StatusAccepted, map[string]bool{"accepted": accepted})
}

func resolveViewer(writer http.ResponseWriter, request *http.Request, resolver func(*http.Request) (Viewer, error)) (Viewer, bool) {
	viewer, err := resolver(request)
	if err != nil {
		writeError(writer, http.StatusUnauthorized, "viewer_unavailable")
		return Viewer{}, false
	}
	return viewer, true
}

func validScope(scope Scope) bool {
	return scope == ScopeRepository || scope == ScopeOrganization || scope == ScopeUser
}

func cors(allowedOrigin string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		origin := request.Header.Get("Origin")
		if origin != "" {
			writer.Header().Add("Vary", "Origin")
		}
		if origin == allowedOrigin {
			writer.Header().Set("Access-Control-Allow-Origin", allowedOrigin)
			writer.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			writer.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type")
		}
		if request.Method == http.MethodOptions {
			if origin != allowedOrigin {
				writeError(writer, http.StatusForbidden, "origin_not_allowed")
				return
			}
			writer.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(writer, request)
	})
}

func writeResult(writer http.ResponseWriter, value any, err error) {
	if err == nil {
		writeJSON(writer, http.StatusOK, value)
		return
	}
	if errors.Is(err, ErrPrivateData) {
		// 404 makes private subject names and counts non-enumerable.
		writeError(writer, http.StatusNotFound, "not_found")
		return
	}
	if errors.Is(err, ErrNoReadModel) || errors.Is(err, ErrNoWebhook) {
		writeError(writer, http.StatusServiceUnavailable, "control_plane_not_configured")
		return
	}
	writeError(writer, http.StatusInternalServerError, "control_plane_error")
}

func writeError(writer http.ResponseWriter, status int, code string) {
	writeJSON(writer, status, map[string]string{"error": code})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}
