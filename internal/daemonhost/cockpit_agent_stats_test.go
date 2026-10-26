package daemonhost

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

func TestCockpitAgentSessionStatsRequiresOwnerAndSummarizesRuns(t *testing.T) {
	t.Parallel()
	const address = "127.0.0.1:8766"
	server := newCockpitServer(address, wbconfig.DefaultCockpitConfig())
	input, output, cost := 120, 30, 0.25
	reads := 0
	server.HandleOwner(http.MethodGet, agentSessionStatsPath, "", agentSessionStatsHandler("/projects", func(_ context.Context, request agentrun.ListRequest) ([]agents.Result, error) {
		reads++
		if request.ProjectsRoot != "/projects" {
			t.Fatalf("wrong projects root: %+v", request)
		}
		if delta := time.Since(request.Since); delta < 29*24*time.Hour || delta > 31*24*time.Hour {
			t.Fatalf("wrong default window: %v", request.Since)
		}
		return []agents.Result{
			{Caller: agents.CallerSession{ID: "thread-one", Name: "builder"}, Repository: "acme/app", Status: agents.StatusCompleted,
				DurationMS: 1500, ToolCalls: 3, Telemetry: agents.Telemetry{Tokens: &agents.Tokens{Input: &input, Output: &output}}, Cost: agents.CostRecord{USD: &cost}},
			{Caller: agents.CallerSession{ID: "thread-one"}, Repository: "acme/lib", Status: agents.StatusFailed},
			{Repository: "acme/app", Status: agents.StatusRunning},
		}, nil
	}))
	mounts := server.Mounts()
	request := httptest.NewRequest(http.MethodGet, agentSessionStatsPath, nil)
	request.Host = address
	anonymous := httptest.NewRecorder()
	mounts[cockpit.APIPrefix].ServeHTTP(anonymous, request)
	if anonymous.Code != http.StatusUnauthorized || reads != 0 {
		t.Fatalf("anonymous request = %d with %d store reads", anonymous.Code, reads)
	}
	issued, err := server.MintLoginCode()
	if err != nil {
		t.Fatal(err)
	}
	login := httptest.NewRequest(http.MethodGet, cockpit.LoginPath+"?code="+issued.Code, nil)
	login.Host = address
	loggedIn := httptest.NewRecorder()
	mounts[cockpit.PagePrefix].ServeHTTP(loggedIn, login)
	cookies := loggedIn.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("login gave %d cookies", len(cookies))
	}
	ownerRequest := httptest.NewRequest(http.MethodGet, agentSessionStatsPath, nil)
	ownerRequest.Host = address
	ownerRequest.AddCookie(cookies[0])
	ownerRequest.Header.Set(cockpit.SessionKeyHeader, issued.Key)
	owner := httptest.NewRecorder()
	mounts[cockpit.APIPrefix].ServeHTTP(owner, ownerRequest)
	if owner.Code != http.StatusOK || reads != 1 || owner.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("owner request = %d, reads=%d, cache=%q", owner.Code, reads, owner.Header().Get("Cache-Control"))
	}
	var report struct {
		agents.StatsReport
		WindowDays int `json:"window_days"`
	}
	if err := json.Unmarshal(owner.Body.Bytes(), &report); err != nil {
		t.Fatal(err)
	}
	if report.WindowDays != 30 || report.Total.Runs != 3 || report.Total.UsageRuns != 1 || report.Total.EstimatedUSD != cost ||
		len(report.BySessionRepository) != 3 || report.BySessionRepository[1].SessionName != "builder" {
		t.Fatalf("session/repository report = %+v", report)
	}
	if strings.Contains(owner.Body.String(), "task") || strings.Contains(owner.Body.String(), "log") {
		t.Fatalf("usage response leaked unrelated run data: %s", owner.Body.String())
	}
}

func TestCockpitAgentSessionStatsWindowValidation(t *testing.T) {
	t.Parallel()
	reads := 0
	handler := agentSessionStatsHandler("/projects", func(_ context.Context, request agentrun.ListRequest) ([]agents.Result, error) {
		reads++
		if delta := time.Since(request.Since); delta < 6*24*time.Hour || delta > 8*24*time.Hour {
			t.Fatalf("wrong seven-day window: %v", request.Since)
		}
		return nil, nil
	})
	for _, query := range []string{"?days=1", "?days=abc", "?days=-7"} {
		response := httptest.NewRecorder()
		handler(response, httptest.NewRequest(http.MethodGet, agentSessionStatsPath+query, nil))
		if response.Code != http.StatusBadRequest || reads != 0 || response.Header().Get("Content-Type") != "application/json" {
			t.Fatalf("%s: response %d, reads %d", query, response.Code, reads)
		}
	}
	response := httptest.NewRecorder()
	handler(response, httptest.NewRequest(http.MethodGet, agentSessionStatsPath+"?days=7", nil))
	if response.Code != http.StatusOK || reads != 1 || !strings.Contains(response.Body.String(), `"window_days":7`) {
		t.Fatalf("valid window: %d, reads %d, body %s", response.Code, reads, response.Body.String())
	}
}

func TestCockpitAgentSessionStatsReturnsGenericStoreAndEncodingErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		list   agentRunsList
		status int
		code   string
	}{
		{"store", func(context.Context, agentrun.ListRequest) ([]agents.Result, error) {
			return nil, errors.New("private filesystem path")
		}, http.StatusServiceUnavailable, "agent_runs_unavailable"},
		{"encoding", func(context.Context, agentrun.ListRequest) ([]agents.Result, error) {
			bad := math.NaN()
			return []agents.Result{{Cost: agents.CostRecord{USD: &bad}}}, nil
		}, http.StatusInternalServerError, ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			request := httptest.NewRequest(http.MethodGet, agentSessionStatsPath, nil)
			response := httptest.NewRecorder()
			agentSessionStatsHandler("/projects", test.list)(response, request)
			if response.Code != test.status || strings.Contains(response.Body.String(), "private filesystem path") ||
				(test.code != "" && !strings.Contains(response.Body.String(), test.code)) {
				t.Fatalf("response = %d %q", response.Code, response.Body.String())
			}
		})
	}
}
