// Package dashboard serves the daemon's loopback listener: the read-only JSON
// API, the redirect from the root to Cockpit, and the mounted subtrees (Cockpit
// and the hub). Its server-rendered operations pages are retired.
package dashboard

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/loopbackhost"
)

const APISchemaVersion = 1

type Options struct {
	Version             string
	DaemonPID           int
	SchedulerGeneration uint64
	// Home is the path the listener's bare address redirects to: Cockpit's
	// mount, which the daemon fills in. It is a field rather than an import
	// because the Cockpit web package's own tests build this handler. Empty
	// leaves the root unrouted, a 404.
	Home string
	// Mounts attaches extra subtrees to the same loopback listener, keyed by
	// the path prefix each one owns (it must start and end with "/"). A
	// self-hosted bench uses it for the hub API under /v0/workbench/ and the
	// embedded dashboard under /workbench/; without a hub section the map is
	// empty and the served routes are exactly what they were.
	Mounts map[string]http.Handler
	// Hub reports the live state of a self-hosted bench hub for
	// /api/v1/health. `wb daemon status` runs in a different process from
	// `wb daemon serve`, so the numbers only this process knows — how many
	// repositories the last poll tick read, and the delivery markers the hub's
	// own StatusService resolves — reach it through the health endpoint rather
	// than by opening the hub's store a second time. Nil when there is no hub.
	Hub func(context.Context) HubHealth
	// LogPath is the daemon's own runtime log file. When set, /api/v1/log
	// serves a tail of it to the owner — the daemon is the log's only
	// authority, so a reverse proxy in front of it never needs disk access of
	// its own. Empty disables the endpoint (503, to the owner only).
	LogPath string
	// Owner reports whether a request acts as the owner principal. It is the
	// seam the daemon fills with Cockpit's own session check
	// (cockpit.Server.IsOwner), so this package neither imports Cockpit nor
	// has a second authentication mechanism. The log is file content, and
	// file content is the owner's alone (cockpit#req:daemon-log-is-owner-only):
	// with no Owner the log route refuses every request.
	Owner func(*http.Request) bool
	// Peers serves /api/v1/peers and /api/v1/peers/{id}
	// (peer-connectivity#req:peers-api's "mounted...on every node"). The
	// caller always supplies one, backed by an empty-list source when this
	// daemon has no hub mounted, so a laptop-only install answers "no
	// downstream peers" instead of answering 404.
	// A nil value keeps the previous behaviour (unmounted, 404s into the
	// index) purely as a defensive default; every real caller sets it.
	Peers http.Handler
}

// defaultLogTailBytes bounds an unqualified /api/v1/log request. It is large
// enough for a useful scrollback without letting one request read an
// unbounded multi-GB log file into memory.
const defaultLogTailBytes = 256 << 10

// maxLogTailBytes bounds an explicit ?tail= request the same way.
const maxLogTailBytes = 4 << 20

// HubHealth is the self-hosted bench hub's live state, as /api/v1/health
// reports it. Every field is derived from the hub's own services; none of it
// is a secret.
type HubHealth struct {
	Mounted               bool               `json:"mounted"`
	Polling               bool               `json:"polling"`
	PollIntervalSeconds   float64            `json:"poll_interval_seconds,omitempty"`
	RepositoriesPolled    int                `json:"repositories_polled"`
	LastEventReceived     *HubDeliveryMarker `json:"last_event_received,omitempty"`
	LastEventAcknowledged *HubDeliveryMarker `json:"last_event_acknowledged,omitempty"`
	// WebhookRedelivery is the missed-webhook recovery sweep's last completed
	// pass, or nil without a configured GitHub App.
	WebhookRedelivery *HubRedeliverySweep `json:"webhook_redelivery,omitempty"`
}

// HubRedeliverySweep is the missed-webhook recovery sweep's last completed
// pass, as /api/v1/health reports it. LastSweepAt and LastFailureAt are
// pointers so JSON omits them before anything has happened yet, rather than
// rendering the zero time; a non-pointer time.Time's zero value is not what
// encoding/json's omitempty treats as empty.
type HubRedeliverySweep struct {
	LastSweepAt *time.Time `json:"last_sweep_at,omitempty"`
	Redelivered int        `json:"redelivered"`
	Abandoned   int        `json:"abandoned"`
	// Uncounted is how many redeliver calls the last pass made without
	// evidence the operator's endpoint is answering at all, so they were not
	// spent against the 3-attempt limit. A sustained non-zero value is what
	// makes an ongoing outage visible even though nothing is being
	// abandoned for it.
	Uncounted int `json:"uncounted"`
	// LastFailureAt and LastFailureClass are sticky: they report the most
	// recent failure even after a later sweep succeeds, so an operator can
	// tell "this has failed before" from a snapshot taken well afterward.
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	LastFailureClass string     `json:"last_failure_class,omitempty"`
}

// HubDeliveryMarker names one repository event and when the hub handled it.
type HubDeliveryMarker struct {
	ID         string    `json:"id,omitempty"`
	Event      string    `json:"event,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type service struct {
	options Options
}

// NewHandler returns the daemon listener's versioned read-only API, the
// redirect from its root to Cockpit, and every mounted subtree (Cockpit and
// the hub).
func NewHandler(options Options) http.Handler {
	server := &service{options: options}
	mux := http.NewServeMux()
	if options.Home != "" {
		mux.HandleFunc("GET /{$}", loopbackOnly(server.root))
	}
	mux.HandleFunc("GET /api/v1/health", loopbackOnly(server.health))
	mux.HandleFunc("GET /api/v1/log", server.log)
	if options.Peers != nil {
		// GET-qualified patterns: an unqualified "/api/v1/peers" pattern
		// conflicts with the mux's own "GET /" catch-all registered above
		// ("matches more methods... but has a more specific path" — Go
		// 1.22's ServeMux refuses that ambiguity outright, panicking at
		// startup). options.Peers already answers 405 to a non-GET request
		// on its own (internal/peers.NewHandler's method check); a
		// non-GET request that never reaches it instead gets the mux's
		// ordinary 404, which is an acceptable, harmless difference for a
		// route with no non-GET method at all.
		mux.Handle("GET /api/v1/peers", options.Peers)
		mux.Handle("GET /api/v1/peers/", options.Peers)
	}
	return securityHeaders(withMounts(options.Mounts, mux))
}

// withMounts routes a prefix to its own handler before the dashboard mux sees
// the request. It is a prefix check rather than extra mux patterns because
// the mux's root route conflicts with any subtree pattern under
// Go's routing precedence rules, and because a mounted subtree serves every
// method — the hub answers POST on enrollment and webhook paths.
func withMounts(mounts map[string]http.Handler, next http.Handler) http.Handler {
	routes := make(map[string]http.Handler, len(mounts))
	for prefix, handler := range mounts {
		if handler == nil || !strings.HasPrefix(prefix, "/") || !strings.HasSuffix(prefix, "/") {
			continue
		}
		routes[prefix] = handler
	}
	if len(routes) == 0 {
		return next
	}
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		for prefix, handler := range routes {
			// "/workbench" reaches the same mount as "/workbench/": the trailing
			// slash is what an operator omits, and the mounted handler is the
			// one that knows where to redirect them.
			if request.URL.Path == strings.TrimSuffix(prefix, "/") || strings.HasPrefix(request.URL.Path, prefix) {
				handler.ServeHTTP(writer, request)
				return
			}
		}
		next.ServeHTTP(writer, request)
	})
}

// root sends the bare address of the daemon's listener to Cockpit, which is
// the daemon's one web interface. Only the exact path "/" answers: every other
// path nothing owns is the mux's ordinary 404, where it used to be the retired
// operations page.
func (server *service) root(writer http.ResponseWriter, request *http.Request) {
	http.Redirect(writer, request, server.options.Home, http.StatusFound)
}

func (server *service) health(writer http.ResponseWriter, request *http.Request) {
	name, _ := os.Hostname()
	payload := map[string]any{
		"schema_version": APISchemaVersion,
		"status":         "ready",
		"machine":        name,
		"wb_version":     server.options.Version,
	}
	if server.options.DaemonPID > 0 {
		payload["daemon_pid"] = server.options.DaemonPID
	}
	if server.options.SchedulerGeneration > 0 {
		payload["scheduler_generation"] = server.options.SchedulerGeneration
	}
	if server.options.Hub != nil {
		payload["hub"] = server.options.Hub(request.Context())
	}
	writeJSON(writer, http.StatusOK, payload)
}

// misdirected is the one message a request on another host name is answered
// with.
const misdirected = "this route answers only on a loopback host name"

// loopbackOnly refuses a request whose Host header does not name a loopback
// host with status 421, before next runs (cockpit#req:host-header-check): the
// dashboard's own JSON routes hold the machine's name, its daemon's process id
// and the names of its worktrees, and a page that rebinds DNS to the loopback
// address must not read them. The rule is the one Cockpit's guard applies.
func loopbackOnly(next http.HandlerFunc) http.HandlerFunc {
	return func(writer http.ResponseWriter, request *http.Request) {
		if !loopbackhost.Request(request) {
			writeJSON(writer, http.StatusMisdirectedRequest, map[string]any{
				"schema_version": APISchemaVersion,
				"error":          "misdirected_request",
				"message":        misdirected,
			})
			return
		}
		next(writer, request)
	}
}

// log serves a tail of the daemon's own runtime log file as plain text to the
// owner, so a reverse proxy in front of the daemon never reads the file from
// disk itself. ?tail=<bytes> requests fewer or more than defaultLogTailBytes,
// capped at maxLogTailBytes.
func (server *service) log(writer http.ResponseWriter, request *http.Request) {
	server.logOpened(writer, request, os.Open)
}

// logOpened is log over an injectable open. The owner check comes before
// everything else, so a request that is not the owner's learns nothing: not
// whether a log path is configured, not whether its query is valid, and the
// file is never opened for it (cockpit#req:daemon-log-is-owner-only). Every
// refusal and failure carries a closed code and a fixed message; the error an
// open, stat or seek returns names the file's path and is never echoed.
func (server *service) logOpened(writer http.ResponseWriter, request *http.Request, open func(string) (*os.File, error)) {
	if server.options.Owner == nil {
		writeLogError(writer, http.StatusForbidden, "log_owner_check_unavailable", "this daemon has no owner check for the runtime log, so it serves it to nobody")
		return
	}
	if !server.options.Owner(request) {
		writeLogError(writer, http.StatusUnauthorized, "owner_session_required", "the runtime log is the owner's alone; run `wb cockpit` to sign in")
		return
	}
	if server.options.LogPath == "" {
		writeLogError(writer, http.StatusServiceUnavailable, "log_unavailable", "this daemon was not started with a runtime log path")
		return
	}
	tail := int64(defaultLogTailBytes)
	if raw := request.URL.Query().Get("tail"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeLogError(writer, http.StatusBadRequest, "invalid_tail", "tail must be a positive number of bytes")
			return
		}
		tail = min(parsed, maxLogTailBytes)
	}
	file, err := open(server.options.LogPath)
	if err != nil {
		writeLogError(writer, http.StatusServiceUnavailable, "log_unavailable", logUnreadable)
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		writeLogError(writer, http.StatusServiceUnavailable, "log_unavailable", logUnreadable)
		return
	}
	start := info.Size() - tail
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		writeLogError(writer, http.StatusServiceUnavailable, "log_unavailable", logUnreadable)
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Log-Truncated", strconv.FormatBool(truncated))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, file)
}

// logUnreadable is the one message a failed open, stat or seek of the runtime
// log is answered with.
const logUnreadable = "the runtime log file cannot be read"

// writeLogError answers the log route with a closed code and a fixed message,
// in the JSON error shape of this API, never stored.
func writeLogError(writer http.ResponseWriter, status int, code, message string) {
	writeJSON(writer, status, map[string]any{
		"schema_version": APISchemaVersion,
		"error":          code,
		"message":        message,
	})
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

// Policy is the Content-Security-Policy of every response of the dashboard
// listener that does not set its own.
const Policy = "default-src 'self'; script-src 'self'; object-src 'none'; base-uri 'self'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'"

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// frame-ancestors 'self' / SAMEORIGIN: the dashboard may frame its own
		// pages (e.g. a wrapper page embedding /workbench/dashboard/ and
		// /api/v1/log side by side), but no other origin may frame it.
		//
		// script-src 'self' with no 'unsafe-inline': a script runs only when it
		// is a file of this origin. The pages on this origin share one storage
		// with Cockpit, which holds the owner's session key
		// (cockpit#req:session-key), so markup injected into any of them must
		// not be able to run: an inline script, an inline event handler and a
		// javascript: address are all refused. A mount that needs another
		// policy sets its own after this one.
		writer.Header().Set("Content-Security-Policy", Policy)
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(writer, request)
	})
}
