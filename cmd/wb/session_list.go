package main

import (
	"encoding/json"
	"fmt"
	"io"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
)

func newSessionListCmd() *cobra.Command {
	var format string
	var onlyLive bool
	command := &cobra.Command{
		Use:   "list",
		Short: "List agent sessions that registered on this machine",
		Long: `List agent sessions that registered on this machine.

Each row shows what the session declared, which WB binary took the
registration, and whether its process is still running. A session that never
registered does not appear: WB reports what it was told, not what it guessed.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			directory, err := sessionDir()
			if err != nil {
				return err
			}
			views, err := session.List(directory)
			if err != nil {
				return err
			}
			if onlyLive {
				live := make([]session.View, 0, len(views))
				for _, view := range views {
					if view.State == session.StateLive {
						live = append(live, view)
					}
				}
				views = live
			}
			if format == "json" {
				encoder := json.NewEncoder(command.OutOrStdout())
				encoder.SetIndent("", "  ")
				return encoder.Encode(views)
			}
			return renderSessions(command.OutOrStdout(), views)
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&onlyLive, "live", false, "show only sessions whose process is still running")
	return command
}

func renderSessions(out io.Writer, views []session.View) error {
	if len(views) == 0 {
		_, err := fmt.Fprintln(out, "no session has registered; run wb session register at session start")
		return err
	}
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "PID\tRUNTIME\tMODEL\tWB\tSTARTED\tSTATE"); err != nil {
		return err
	}
	for _, view := range views {
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\n",
			view.PID, orDash(view.Runtime), orDash(view.Model), orDash(view.WBVersion),
			view.StartedAt.Local().Format("2006-01-02 15:04"), view.State); err != nil {
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
