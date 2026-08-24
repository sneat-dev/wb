package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sessionWorktreeLister is the worktrees.List seam. Tests override it with a
// fixture (or a lister that panics, to prove a code path never scans) so
// they stay hermetic without building a real worktree tree.
var sessionWorktreeLister = worktrees.List

func newSessionListCmd() *cobra.Command {
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
actually worked on without it having to declare that separately.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, args []string) error {
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			directory, err := sessionDirForRead()
			if err != nil {
				return err
			}
			return runSessionList(directory, projectsRoot, onlyLive, format == "json", command.OutOrStdout(), command.ErrOrStderr())
		},
	}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&onlyLive, "live", false, "show only sessions whose process is still running")
	return command
}

// runSessionList lists registered sessions and, for those that will actually
// be rendered, enriches each with the efforts/worktrees/branches it worked
// on. It stays read-only — the caller resolves directory without creating
// WB's home — and never fails on derivation: a worktrees.List error degrades
// the derived columns to "-" rather than failing the command.
func runSessionList(directory, projectsRoot string, onlyLive, jsonOut bool, out, errOut io.Writer) error {
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
	if len(views) == 0 {
		// A JSON consumer gets a parseable empty list; the guidance goes to a
		// human on stderr either way it cannot corrupt the document.
		if jsonOut {
			_, _ = fmt.Fprintln(errOut, "no session has registered; run wb session register at session start")
			return json.NewEncoder(out).Encode([]sessionRow{})
		}
		_, err := fmt.Fprintln(out, "no session has registered; run wb session register at session start")
		return err
	}

	results, err := sessionWorktreeLister(context.Background(), worktrees.ListOptions{ProjectsRoot: projectsRoot})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "derive worktree attribution: %v\n", err)
		results = nil
	}
	rows := attributeSessions(views, results)

	if jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	return renderSessions(out, rows)
}

func renderSessions(out io.Writer, rows []sessionRow) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "PID\tRUNTIME\tMODEL\tWB\tSTARTED\tEFFORTS\tWORKTREES\tBRANCHES\tSTATE"); err != nil {
		return err
	}
	for _, row := range rows {
		worktreeCount := "-"
		if len(row.Worktrees) > 0 {
			worktreeCount = strconv.Itoa(len(row.Worktrees))
		}
		if _, err := fmt.Fprintf(writer, "%d\t%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n",
			row.PID, orDash(row.Runtime), orDash(row.Model), orDash(row.WBVersion),
			row.StartedAt.Local().Format("2006-01-02 15:04"),
			condense(row.Efforts, 24), worktreeCount, condense(row.Branches, 24), row.State); err != nil {
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
