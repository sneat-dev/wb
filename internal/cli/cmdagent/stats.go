package cmdagent

import (
	"fmt"
	"io"
	"strings"

	"github.com/sneat-dev/wb/internal/agentrun"
	"github.com/sneat-dev/wb/internal/agents"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/spf13/cobra"
)

func NewStats(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var jsonOut bool
	var repository, sessionID string
	command := &cobra.Command{
		Use:   "stats",
		Short: "Summarize dispatched agent usage by caller session and repository",
		Long: `Summarize WB-dispatched agent runs by calling session, repository, and
session/repository pair. The report counts recorded runs, duration and tool
calls; token and cost totals include only runs whose harness reported them.
An empty session ID means an older or unattributed run.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			runs, err := deps.List(command.Context(), agentrun.ListRequest{ProjectsRoot: runtime.Flags().ProjectsRoot})
			if err != nil {
				return err
			}
			selected := make([]agents.Result, 0, len(runs))
			for _, run := range runs {
				if repository != "" && run.Repository != repository {
					continue
				}
				if sessionID != "" && run.Caller.ID != sessionID && run.Caller.WBSessionID != sessionID {
					continue
				}
				selected = append(selected, run)
			}
			report := agents.SummarizeRuns(selected)
			if jsonOut {
				return writeAgentJSON(command.OutOrStdout(), report)
			}
			return printAgentStats(command.OutOrStdout(), report)
		},
	}
	deps.Discovery(command, "usage tokens time duration cost session repository stats offload")
	command.Flags().StringVar(&repository, "repo", "", "include only this exact owner/repository")
	command.Flags().StringVar(&sessionID, "session", "", "include only this native or WB session ID")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func printAgentStats(writer io.Writer, report agents.StatsReport) error {
	if _, err := fmt.Fprintf(writer, "runs=%d completed=%d failed=%d duration=%s tool_calls=%d usage=%d/%d estimated_usd=%.4f cost=%d/%d\n",
		report.Total.Runs, report.Total.Completed, report.Total.Failed,
		formatAgentDuration(report.Total.DurationMS), report.Total.ToolCalls, report.Total.UsageRuns, report.Total.Runs,
		report.Total.EstimatedUSD,
		report.Total.CostRuns, report.Total.Runs); err != nil {
		return err
	}
	for _, section := range []struct {
		name string
		rows []agents.StatsBucket
	}{{"sessions", report.BySession}, {"repositories", report.ByRepository}, {"session/repository", report.BySessionRepository}} {
		if _, err := fmt.Fprintln(writer, section.name+":"); err != nil {
			return err
		}
		for _, row := range section.rows {
			label := strings.TrimSpace(row.SessionID + " " + row.Repository)
			if label == "" {
				label = "(unattributed)"
			}
			if row.SessionName != "" {
				label += " (" + row.SessionName + ")"
			}
			if _, err := fmt.Fprintf(writer, "  %s\truns=%d duration=%s tool_calls=%d input=%d output=%d usage=%d/%d estimated_usd=%.4f cost=%d/%d\n",
				label, row.Runs, formatAgentDuration(row.DurationMS), row.ToolCalls, row.InputTokens,
				row.OutputTokens, row.UsageRuns, row.Runs, row.EstimatedUSD, row.CostRuns, row.Runs); err != nil {
				return err
			}
		}
	}
	return nil
}

func formatAgentDuration(milliseconds int64) string {
	return fmt.Sprintf("%.1fs", float64(milliseconds)/1000)
}
