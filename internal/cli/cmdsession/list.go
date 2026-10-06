package cmdsession

import (
	"encoding/json"
	"fmt"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/sessionrun"
	"github.com/spf13/cobra"
	"io"
	"strconv"
	"text/tabwriter"
)

func NewList(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	var onlyLive bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List agent sessions that registered on this machine",
		Long: `List agent sessions that registered on this machine.

Each row shows what the session declared, which WB binary took the
registration, and whether its process is still running. A session that never
registered does not appear: WB reports what it was told, not what it guessed.

EFFORTS, WORKTREES, and BRANCHES are derived by matching worktree owner
registrations to the session's declared PID, so they show what the session
actually worked on without it having to declare that separately.

For a parked row, --format json includes parked_session_id. Pass that value,
not wb_session_id, to wb session resume.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := shared.RequireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			rows, err := deps.List(command.Context(), sessionrun.ListRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, OnlyLive: onlyLive}, func(warning string) { _, _ = fmt.Fprint(command.ErrOrStderr(), warning) })
			if err != nil {
				return err
			}
			if len(rows) == 0 {
				if format == "json" {
					_, _ = fmt.Fprintln(command.ErrOrStderr(), "no session has registered; run wb session register at session start")
					return json.NewEncoder(command.OutOrStdout()).Encode([]sessionrun.Row{})
				}
				_, err := fmt.Fprintln(command.OutOrStdout(), "no session has registered; run wb session register at session start")
				return err
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(rows)
			}
			return renderSessions(command.OutOrStdout(), rows)

		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&onlyLive, "live", false, "show only sessions whose process is still running")
	return command
}
func renderSessions(out io.Writer, rows []sessionrun.Row) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	// A fresh tabwriter buffers this fixed 12-cell header: it contains no
	// formfeed or single-cell line that could flush to the bound output.
	// Dynamic rows and the final Flush below still return writer failures.
	_, _ = fmt.Fprintln(writer, "SESSION\tMACHINE\tPID\tRUNTIME\tMODEL\tWB\tSTARTED\tEFFORTS\tWORKTREES\tBRANCHES\tSTATE\tWAITING")
	for _, row := range rows {
		worktreeCount := "-"
		if len(row.Worktrees) > 0 {
			worktreeCount = strconv.Itoa(len(row.Worktrees))
		}
		if _, err := fmt.Fprintf(writer, "%s\t%s\t%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			orDash(row.WBSessionID), orDash(row.Machine), row.PID, orDash(row.Runtime), orDash(row.Model), orDash(row.WBVersion),
			row.StartedAt.Local().Format("2006-01-02 15:04"),
			condense(row.Efforts, 24), worktreeCount, condense(row.Branches, 24), row.State,
			condense(row.Waiting, 32)); err != nil {
			return err
		}
	}
	return writer.Flush()
}
func orDash(value string) string {
	if value == "" {
		return "-"
	}
	return value
}
func condense(values []string, max int) string {
	switch len(values) {
	case 0:
		return "-"
	case 1:
		runes := []rune(values[0])
		if len(runes) > max {
			return string(runes[:max]) + "…"
		}
		return values[0]
	default:
		return strconv.Itoa(len(values))
	}
}
