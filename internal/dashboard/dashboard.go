// Package dashboard serves WB's local read-only operations dashboard and API.
package dashboard

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/discover"
	"github.com/sneat-dev/wb/internal/runlog"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const APISchemaVersion = 1

type Options struct {
	ProjectsRoot string
	Version      string
	Now          func() time.Time
	CacheTTL     time.Duration
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

type Overview struct {
	SchemaVersion int            `json:"schema_version"`
	GeneratedAt   time.Time      `json:"generated_at"`
	Machine       Machine        `json:"machine"`
	Operations    runlog.Summary `json:"operations"`
	Worktrees     []Worktree     `json:"worktrees"`
	Diagnostics   int            `json:"diagnostics"`
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
	server := &service{options: options}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /", server.index)
	mux.HandleFunc("GET /api/v1/health", server.health)
	mux.HandleFunc("GET /api/v1/overview", server.overview)
	return securityHeaders(mux)
}

func (server *service) index(writer http.ResponseWriter, _ *http.Request) {
	writer.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = writer.Write([]byte(indexHTML))
}

func (server *service) health(writer http.ResponseWriter, _ *http.Request) {
	name, _ := os.Hostname()
	writeJSON(writer, http.StatusOK, map[string]any{
		"schema_version": APISchemaVersion,
		"status":         "ready",
		"machine":        name,
		"wb_version":     server.options.Version,
	})
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
	overview, err := BuildOverview(ctx, server.options.ProjectsRoot, server.options.Version, now)
	if err != nil {
		return Overview{}, err
	}
	server.cached = overview
	server.cachedAt = now
	return overview, nil
}

// BuildOverview joins local worktree inventory with governed-command events.
func BuildOverview(_ context.Context, projectsRoot, version string, now time.Time) (Overview, error) {
	repositories, err := discover.ScanLocal(projectsRoot)
	if err != nil {
		return Overview{}, err
	}
	name, _ := os.Hostname()
	overview := Overview{
		SchemaVersion: APISchemaVersion,
		GeneratedAt:   now.UTC(),
		Machine:       Machine{Name: name, Version: version},
	}
	var events []runlog.Event
	for _, repository := range repositories {
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

func writeJSON(writer http.ResponseWriter, status int, value any) {
	writer.Header().Set("Content-Type", "application/json; charset=utf-8")
	writer.Header().Set("Cache-Control", "no-store")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(value)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Content-Security-Policy", "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; connect-src 'self'; frame-ancestors 'none'")
		writer.Header().Set("Referrer-Policy", "no-referrer")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		writer.Header().Set("X-Frame-Options", "DENY")
		next.ServeHTTP(writer, request)
	})
}
