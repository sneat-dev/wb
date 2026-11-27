// Package cmddesk is the `wb desk` command family (spec/features/desk-connect).
package cmddesk

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/sneat-dev/wb/api/desk"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/deskrun"
	"github.com/spf13/cobra"
)

// Operations are the domain operations the family calls.
type Operations struct {
	Connect       func(context.Context, deskrun.ConnectRequest) (deskrun.ConnectResult, error)
	Disconnect    func(ctx context.Context, projectsRoot string, noRestart bool) (deskrun.DisconnectResult, error)
	Status        func(context.Context) (deskrun.StatusResult, error)
	ExportTaskKey func(out io.Writer, stdoutIsTerminal bool) error
	IsTerminal    func(any) bool
	Now           func() time.Time
}

// New returns `wb desk`.
func New(runtime shared.Runtime, operations Operations) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "desk",
		Short: "Connect this machine to Sneat Dev Desk",
		Long: `wb desk connects this machine's WB daemon to Sneat Dev Desk (https://desk.sneat.dev),
the hosted overview of your machines, repositories and teams. The daemon then
publishes this machine's state, reduced to its detail level, over outbound HTTPS.

  wb desk connect           approve this machine in the browser and install its credential
  wb desk status            show the connection and Desk's last publish and heartbeat
  wb desk disconnect        revoke the credential at Desk and remove it here
  wb desk task-key export   hand your task key to your next machine

At the default detail level, repositories, no task, branch or worktree name
leaves the machine: task names travel as keyed hashes under a task key that
Desk never holds.`,
	}
	cmd.AddCommand(newConnect(runtime, operations), newStatus(runtime, operations),
		newDisconnect(runtime, operations), newTaskKey(runtime, operations))
	return cmd
}

func newConnect(runtime shared.Runtime, operations Operations) *cobra.Command {
	var deskURL, machine, detail, space string
	var noBrowser, noRestart, taskKeyStdin, jsonOut bool
	command := &cobra.Command{
		Use:   "connect",
		Short: "Connect this machine to Desk with a browser-approved code",
		Long: `Prints a short code and opens Desk in the browser. After you approve the
machine there (personal only, or personal plus a team space), the credential is
verified, stored in a private file, the desk section of wb.yaml is updated
without touching other settings, and a running daemon is restarted.

On a machine that is already connected, --detail alone changes what the
machine sends without connecting again:

  wb desk connect
  wb desk connect --detail full
  wb desk task-key export | ssh vm1 wb desk connect --task-key-stdin`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := operations.Connect(command.Context(), deskrun.ConnectRequest{
				ProjectsRoot: runtime.Flags().ProjectsRoot, DeskURL: deskURL, Machine: machine,
				Detail: desk.Level(strings.TrimSpace(detail)), DetailChanged: command.Flags().Changed("detail"),
				SpaceHint: space, NoBrowser: noBrowser, CanOpen: operations.IsTerminal(command.ErrOrStderr()),
				NoRestart: noRestart, TaskKeyStdin: taskKeyStdin, Input: command.InOrStdin(), Notice: command.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			return writeConnect(command.OutOrStdout(), result, jsonOut)
		},
	}
	command.Flags().StringVar(&deskURL, "desk", "", "Desk origin (default: desk.url, else https://desk.sneat.dev)")
	command.Flags().StringVar(&machine, "machine", "", "name for this machine (default: remote.machine, else the hostname)")
	command.Flags().StringVar(&detail, "detail", string(desk.LevelRepositories), "what this machine sends: repositories or full")
	command.Flags().StringVar(&space, "space", "", "preselect a team or company space on the approval page")
	command.Flags().BoolVar(&noBrowser, "no-browser", false, "print the address instead of opening a browser")
	command.Flags().BoolVar(&noRestart, "no-restart", false, "do not restart a running daemon")
	command.Flags().BoolVar(&taskKeyStdin, "task-key-stdin", false, "adopt the task key piped in from your other machine")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func newStatus(runtime shared.Runtime, operations Operations) *cobra.Command {
	var jsonOut bool
	command := &cobra.Command{
		Use:   "status",
		Short: "Show this machine's Desk connection",
		Long: `Reads the desk section of wb.yaml and Desk's record of this machine: its
spaces and shares, and when Desk last accepted a snapshot and a heartbeat.
Exits 1 when the machine is not connected or Desk reports an error.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := operations.Status(command.Context())
			if err != nil {
				return err
			}
			if err := writeStatus(command.OutOrStdout(), result, jsonOut, operations.Now()); err != nil {
				return err
			}
			if !result.Connected {
				return runtime.ExitError(shared.ExitFindings, "this machine is not connected to Desk")
			}
			if result.Problem != "" {
				return runtime.ExitError(shared.ExitFindings, result.Problem)
			}
			if result.DeskError != "" {
				return runtime.ExitError(shared.ExitFindings, "Desk answered "+string(result.DeskError))
			}
			return nil
		},
	}
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func newDisconnect(runtime shared.Runtime, operations Operations) *cobra.Command {
	var noRestart, jsonOut bool
	command := &cobra.Command{
		Use:   "disconnect",
		Short: "Revoke this machine's Desk credential and remove it",
		Long: `Revokes the credential at Desk, removes the credential file and the desk
fields of wb.yaml that connect wrote, and restarts a running daemon. When Desk
cannot be reached the local credential is removed anyway, the result says
revoked: false, and the command exits 1: remove the machine in Desk too.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			result, err := operations.Disconnect(command.Context(), runtime.Flags().ProjectsRoot, noRestart)
			if err != nil {
				return err
			}
			if err := writeDisconnect(command.OutOrStdout(), result, jsonOut); err != nil {
				return err
			}
			if !result.Revoked {
				return runtime.ExitError(shared.ExitFindings, "Desk could not be reached: remove this machine in Desk as well")
			}
			return nil
		},
	}
	command.Flags().BoolVar(&noRestart, "no-restart", false, "do not restart a running daemon")
	shared.AddJSONFormatFlags(command, &jsonOut)
	return command
}

func newTaskKey(_ shared.Runtime, operations Operations) *cobra.Command {
	group := &cobra.Command{Use: "task-key", Short: "Manage the task key your machines share"}
	group.AddCommand(&cobra.Command{
		Use:   "export",
		Short: "Write your task key to a pipe for your next machine",
		Long: `Writes this machine's Desk task key to stdout so your next machine can adopt
it, and refuses when stdout is a terminal. Machines that share a task key let
Desk recognise the same task on both without learning its name:

  wb desk task-key export | ssh vm1 wb desk connect --task-key-stdin`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			return operations.ExportTaskKey(command.OutOrStdout(), operations.IsTerminal(command.OutOrStdout()))
		},
	})
	return group
}

func writeConnect(out io.Writer, result deskrun.ConnectResult, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	if result.DetailOnly {
		_, err := fmt.Fprintf(out, "Desk detail is now %s\n  config   %s\n  daemon   %s\n", result.Detail, result.ConfigPath, restarted(result.DaemonRestarted))
		return err
	}
	spaces := result.PersonalSpaceID + " (personal)"
	for _, share := range result.Shares {
		spaces += fmt.Sprintf(", %s (%s, %s)", share.SpaceID, share.Level, share.State)
	}
	_, err := fmt.Fprintf(out, "Connected %s to %s\n  spaces      %s\n  detail      %s\n  credential  %s\n  task key    %s%s\n  config      %s\n  daemon      %s\n",
		result.Machine, result.DeskURL, spaces, result.Detail, result.CredentialFile, result.TaskKeyID,
		map[bool]string{true: " (new)", false: ""}[result.TaskKeyCreated], result.ConfigPath, restarted(result.DaemonRestarted))
	return err
}

func restarted(yes bool) string {
	if yes {
		return "restarted if running"
	}
	return "restart skipped: run wb daemon restart for a running daemon to pick it up"
}

func age(at *time.Time, now time.Time) string {
	if at == nil {
		return "never"
	}
	return now.Sub(*at).Round(time.Second).String() + " ago"
}

func writeStatus(out io.Writer, result deskrun.StatusResult, jsonOut bool, now time.Time) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	if !result.Connected {
		_, err := fmt.Fprintf(out, "Not connected to Desk. Run: %s\n", result.Fix)
		return err
	}
	text := fmt.Sprintf("Connected to %s\n  machine     %s\n  detail      %s\n  space       %s (personal)\n",
		result.DeskURL, result.MachineID, result.Detail, result.PersonalSpaceID)
	for _, share := range result.Shares {
		text += fmt.Sprintf("  shared      %s (%s, %s)\n", share.SpaceID, share.Level, share.State)
	}
	if result.Paused {
		text += "  publishing  paused (desk.enabled: false)\n"
	}
	if result.Problem != "" {
		text += fmt.Sprintf("  problem     %s\n  fix         %s\n", result.Problem, result.Fix)
	} else if result.DeskError != "" {
		text += fmt.Sprintf("  desk_error  %s\n", result.DeskError)
		if result.Fix != "" {
			text += fmt.Sprintf("  fix         %s\n", result.Fix)
		}
	} else {
		text += fmt.Sprintf("  published   %s\n  heartbeat   %s\n", age(result.LastSnapshotAt, now), age(result.LastHeartbeatAt, now))
	}
	_, err := io.WriteString(out, text)
	return err
}

func writeDisconnect(out io.Writer, result deskrun.DisconnectResult, jsonOut bool) error {
	if jsonOut {
		return json.NewEncoder(out).Encode(result)
	}
	_, err := fmt.Fprintf(out, "Disconnected from Desk\n  revoked     %t\n  credential  %s (removed)\n  config      %s\n  daemon      %s\n",
		result.Revoked, result.CredentialFile, result.ConfigPath, restarted(result.DaemonRestarted))
	return err
}
