package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/waitregistry"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// sessionWorktreeLister is the worktrees.List seam. Tests override it with a
// fixture (or a lister that panics, to prove a code path never scans) so
// they stay hermetic without building a real worktree tree.
var sessionWorktreeLister = worktrees.List

func newSessionListCmd(inv *invocation) *cobra.Command {
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
			if err := requireOutputFormat(format, "text", "json"); err != nil {
				return err
			}
			directory, err := sessionDirForRead(inv)
			if err != nil {
				return err
			}
			return runSessionList(directory, inv.projectsRoot, onlyLive, format == "json", command.OutOrStdout(), command.ErrOrStderr())
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
	// A wait is only visible if something reports it. Attribution failures
	// degrade the column rather than failing the listing, exactly as the
	// worktree derivation above does.
	attributeSessionWaits(rows, projectsRoot, errOut)

	if jsonOut {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(rows)
	}
	return renderSessions(out, rows)
}

// attributeSessionWaits joins outstanding wait records to their sessions. A
// stale record is shown with a marker rather than dropped: a waiter that died
// without clearing its record means nothing is watching for that event, which
// is the case most worth surfacing here.
func attributeSessionWaits(rows []sessionRow, projectsRoot string, errOut io.Writer) {
	home, err := wbhome.Root(projectsRoot)
	if err != nil {
		return
	}
	records, err := waitregistry.List(home, waitregistry.Options{})
	if err != nil {
		_, _ = fmt.Fprintf(errOut, "derive outstanding waits: %v\n", err)
		return
	}
	waiting := map[string][]string{}
	for _, record := range records {
		label := record.Kind + " " + strings.Join(record.Targets, ",")
		if record.Stale {
			label += " (stale)"
		}
		waiting[record.WBSessionID] = append(waiting[record.WBSessionID], label)
	}
	for index := range rows {
		rows[index].Waiting = waiting[rows[index].WBSessionID]
	}
}

func renderSessions(out io.Writer, rows []sessionRow) error {
	writer := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)
	if _, err := fmt.Fprintln(writer, "SESSION\tMACHINE\tPID\tRUNTIME\tMODEL\tWB\tSTARTED\tEFFORTS\tWORKTREES\tBRANCHES\tSTATE\tWAITING"); err != nil {
		return err
	}
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
