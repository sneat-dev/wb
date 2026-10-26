package daemonhost

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/wbhome"
)

const agentSessionStatsPath = cockpit.APIPrefix + "agent-session-stats"

type agentRunsList func(context.Context, agentrun.ListRequest) ([]agents.Result, error)

// The usage report is local to this daemon and owner-only. It includes session
// identifiers and spending, which the anonymous fleet metadata never exposes.
func registerCockpitAgentStats(server *cockpit.Server, projectsRoot string) {
	list := agentrun.New(agentrun.Dependencies{Root: wbhome.Root}).List
	server.HandleOwner(http.MethodGet, agentSessionStatsPath, "", agentSessionStatsHandler(projectsRoot, list))
}

func agentSessionStatsHandler(projectsRoot string, list agentRunsList) cockpit.OwnerHandler {
	return func(writer http.ResponseWriter, request *http.Request) {
		days := 30
		if raw := request.URL.Query().Get("days"); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || (parsed != 7 && parsed != 30 && parsed != 90) {
				writer.Header().Set("Cache-Control", "no-store")
				writer.Header().Set("Content-Type", "application/json")
				writer.WriteHeader(http.StatusBadRequest)
				_, _ = writer.Write([]byte("{\"error\":\"invalid_days\"}\n"))
				return
			}
			days = parsed
		}
		runs, err := list(request.Context(), agentrun.ListRequest{ProjectsRoot: projectsRoot, Since: time.Now().UTC().AddDate(0, 0, -days)})
		if err != nil {
			writer.Header().Set("Cache-Control", "no-store")
			writer.Header().Set("Content-Type", "application/json")
			writer.WriteHeader(http.StatusServiceUnavailable)
			_, _ = writer.Write([]byte("{\"error\":\"agent_runs_unavailable\"}\n"))
			return
		}
		body, err := json.Marshal(struct {
			agents.StatsReport
			WindowDays int `json:"window_days"`
		}{agents.SummarizeRuns(runs), days})
		if err != nil {
			writer.Header().Set("Cache-Control", "no-store")
			writer.WriteHeader(http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		cockpit.ServePrivatePayload(writer, request, cockpit.NewPayload(body, cockpit.Gzip))
	}
}
