// Package dashboard serves WB's local read-only operations dashboard and API.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const APISchemaVersion = 1

type Options struct {
	ProjectsRoot        string
	Version             string
	DaemonPID           int
	SchedulerGeneration uint64
	Now                 func() time.Time
	CacheTTL            time.Duration
	InventoryIndexPath  string
	InventoryIndexTTL   time.Duration
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
	// serves a tail of it directly — the daemon is the log's only authority,
	// so a reverse proxy in front of it never needs disk access of its own.
	// Empty disables the endpoint (503).
	LogPath string
	// Peers serves /api/v1/peers and /api/v1/peers/{id}
	// (peer-connectivity#req:peers-api's "mounted...on every node"). The
	// caller always supplies one, backed by an empty-list source when this
	// daemon has no hub mounted, so a laptop-only install answers "no
	// downstream peers" instead of falling through to the index page below.
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

type Machine struct {
	Name    string `json:"name"`
	Version string `json:"wb_version"`
}

type Worktree struct {
	Task           string    `json:"task"`
	Repository     string    `json:"repository"`
	Branch         string    `json:"branch"`
	Owner          string    `json:"owner,omitempty"`
	OwnerState     string    `json:"owner_state"`
	AgeSeconds     int64     `json:"age_seconds,omitempty"`
	LastActivityAt time.Time `json:"last_activity_at,omitempty"`
}

type Inventory struct {
	SourceFingerprint string    `json:"source_fingerprint,omitempty"`
	ObservedAt        time.Time `json:"observed_at,omitempty"`
	CacheHit          bool      `json:"cache_hit"`
}

type Overview struct {
	SchemaVersion int            `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Machine       Machine        `json:"machine"`
	Operations    runlog.Summary `json:"operations"`
	Worktrees     []Worktree     `json:"worktrees"`
	Diagnostics   int            `json:"diagnostics"`
	Inventory     Inventory      `json:"inventory"`
}

type service struct {
	options  Options
	mu       sync.Mutex
	cached   Overview
	cachedAt time.Time
}

// NewHandler returns the dashboard UI and versioned read-only API.
func NewHandler(options Options) http.Handler {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.CacheTTL <= 0 {
		options.CacheTTL = 10 * time.Second
	}
	if options.InventoryIndexTTL <= 0 {
		options.InventoryIndexTTL = time.Minute
	}
	server := &service{options: options}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.index)
	mux.HandleFunc("GET /metrics", server.metrics)
	mux.HandleFunc("GET /coverage", server.coverageRedirect)
	mux.HandleFunc("GET /api/v1/health", server.health)
	mux.HandleFunc("GET /api/v1/overview", server.overview)
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
// the mux's catch-all "GET /" index conflicts with any subtree pattern under
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

func (server *service) index(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(indexHTML))
}

func (server *service) metrics(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(metricsHTML))
}

func (server *service) coverageRedirect(writer http.ResponseWriter, request *http.Request) {
	http.Redirect(writer, request, "/metrics?type=test_coverage", http.StatusFound)
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

func (server *service) overview(writer http.ResponseWriter, request *http.Request) {
	overview, err := server.load(request.Context())
	if err != nil {
		writeJSON(writer, http.StatusInternalServerError, map[string]any{
			"schema_version": APISchemaVersion,
			"error":          "overview_unavailable",
			"message":        err.Error(),
		})
		return
	}
	writeJSON(writer, http.StatusOK, overview)
}

func (server *service) load(ctx context.Context) (Overview, error) {
	server.mu.Lock()
	defer server.mu.Unlock()
	now := server.options.Now().UTC()
	if !server.cachedAt.IsZero() && now.Sub(server.cachedAt) < server.options.CacheTTL {
		return server.cached, nil
	}
	indexPath := server.options.InventoryIndexPath
	indexPathUnavailable := false
	if indexPath == "" {
		if home, err := wbhome.EnsureRoot(server.options.ProjectsRoot); err == nil {
			indexPath = filepath.Join(home, "cache", "fleet-inventory-v1.json")
		} else {
			indexPathUnavailable = true
		}
	}
	overview, err := buildOverview(ctx, server.options.ProjectsRoot, server.options.Version, now, discover.LocalIndexOptions{
		CachePath: indexPath,
		MaxAge:    server.options.InventoryIndexTTL,
		Now:       func() time.Time { return now },
	})
	if err != nil {
		return Overview{}, err
	}
	if indexPathUnavailable {
		overview.Diagnostics++
	}
	server.cached = overview
	server.cachedAt = now
	return overview, nil
}

// BuildOverview joins local worktree inventory with governed-command events.
func BuildOverview(ctx context.Context, projectsRoot, version string, now time.Time) (Overview, error) {
	return buildOverview(ctx, projectsRoot, version, now, discover.LocalIndexOptions{})
}

func buildOverview(_ context.Context, projectsRoot, version string, now time.Time, indexOptions discover.LocalIndexOptions) (Overview, error) {
	indexed, err := discover.ScanLocalIndexed(projectsRoot, indexOptions)
	if err != nil {
		return Overview{}, err
	}
	name, _ := os.Hostname()
	overview := Overview{
		SchemaVersion: APISchemaVersion,
		GeneratedAt:   now.UTC(),
		Machine:       Machine{Name: name, Version: version},
		Diagnostics:   len(indexed.Diagnostics),
		Inventory: Inventory{
			SourceFingerprint: indexed.SourceFingerprint,
			ObservedAt:        indexed.ObservedAt,
			CacheHit:          indexed.CacheHit,
		},
	}
	var events []runlog.Event
	for _, repository := range indexed.Repositories {
		root := filepath.Join(repository.Path, ".worktrees")
		entries, readErr := os.ReadDir(root)
		if os.IsNotExist(readErr) {
			continue
		}
		if readErr != nil {
			overview.Diagnostics++
			continue
		}
		for _, entry := range entries {
			if !entry.IsDir() {
				continue
			}
			worktreePath := filepath.Join(root, entry.Name())
			manifest, manifestErr := worktrees.ReadManifest(worktreePath)
			if manifestErr != nil {
				overview.Diagnostics++
				continue
			}
			activity := worktrees.HeartbeatAt(worktreePath)
			if activity.IsZero() {
				activity = manifest.CreatedAt
			}
			ownerState := "idle"
			if now.Sub(activity) <= worktrees.DefaultSessionFreshness {
				ownerState = "active"
			}
			owner := strings.Trim(manifest.AgentID, "/")
			if manifest.AgentRuntime != "" && owner != "" {
				owner = manifest.AgentRuntime + "/" + owner
			} else if owner == "" {
				owner = manifest.Initiator
			}
			overview.Worktrees = append(overview.Worktrees, Worktree{
				Task: manifest.EffortID, Repository: manifest.Repository,
				Branch: manifest.Branch, Owner: owner, OwnerState: ownerState,
				AgeSeconds:     int64(now.Sub(manifest.CreatedAt).Seconds()),
				LastActivityAt: activity,
			})
			path := filepath.Join(worktreePath, ".wb", "local", "run", "events.jsonl")
			worktreeEvents, eventErr := runlog.Read(path)
			if eventErr != nil {
				return Overview{}, fmt.Errorf("read run telemetry for %s: %w", manifest.Repository, eventErr)
			}
			events = append(events, worktreeEvents...)
		}
	}
	sort.Slice(overview.Worktrees, func(i, j int) bool {
		if overview.Worktrees[i].Repository == overview.Worktrees[j].Repository {
			return overview.Worktrees[i].Task < overview.Worktrees[j].Task
		}
		return overview.Worktrees[i].Repository < overview.Worktrees[j].Repository
	})
	overview.Operations = runlog.Summarize(events, now.AddDate(0, 0, -14))
	return overview, nil
}

// log serves a tail of the daemon's own runtime log file as plain text, so a
// reverse proxy in front of the daemon never reads the file from disk itself.
// ?tail=<bytes> requests fewer or more than defaultLogTailBytes, capped at
// maxLogTailBytes.
func (server *service) log(writer http.ResponseWriter, request *http.Request) {
	if server.options.LogPath == "" {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
			"schema_version": APISchemaVersion,
			"error":          "log_unavailable",
			"message":        "this daemon was not started with a runtime log path",
		})
		return
	}
	tail := int64(defaultLogTailBytes)
	if raw := request.URL.Query().Get("tail"); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeJSON(writer, http.StatusBadRequest, map[string]any{
				"schema_version": APISchemaVersion,
				"error":          "invalid_tail",
				"message":        "tail must be a positive number of bytes",
			})
			return
		}
		tail = parsed
		if tail > maxLogTailBytes {
			tail = maxLogTailBytes
		}
	}
	file, err := os.Open(server.options.LogPath)
	if err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
			"schema_version": APISchemaVersion,
			"error":          "log_unavailable",
			"message":        err.Error(),
		})
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
			"schema_version": APISchemaVersion,
			"error":          "log_unavailable",
			"message":        err.Error(),
		})
		return
	}
	start := info.Size() - tail
	truncated := start > 0
	if start < 0 {
		start = 0
	}
	if _, err := file.Seek(start, io.SeekStart); err != nil {
		writeJSON(writer, http.StatusServiceUnavailable, map[string]any{
			"schema_version": APISchemaVersion,
			"error":          "log_unavailable",
			"message":        err.Error(),
		})
		return
	}
	writer.Header().Set("Content-Type", "text/plain; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.Header().Set("X-Log-Truncated", strconv.FormatBool(truncated))
	writer.WriteHeader(http.StatusOK)
	_, _ = io.Copy(writer, file)
}

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		// frame-ancestors 'self' / SAMEORIGIN: the dashboard may frame its own
		// pages (e.g. a wrapper page embedding /workbench/dashboard/ and
		// /api/v1/log side by side), but no other origin may frame it.
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'self'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "SAMEORIGIN")
		next.ServeHTTP(writer, request)
	})
}
