package cmddaemon

import (
	"fmt"

	"github.com/sneat-dev/wb/internal/cli/daemonview"
	"github.com/sneat-dev/wb/internal/cli/shared"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/daemonruntime"
	"github.com/spf13/cobra"
)

func newServe(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var listenAddress, stateFile string
	var quiet, managedStartFlag bool
	command := &cobra.Command{
		Use: "serve", Short: "Serve Cockpit and the read-only API on a loopback address",
		Long: `Serve Cockpit (WB's web interface, at /cockpit/; the root redirects there) and the versioned read-only API.

The listener is loopback-only. Publish it to registered machines through a
protected Cloudflare Tunnel or another authenticated reverse proxy; do not bind
the daemon directly to a public interface. Mutating operation RPCs are served
only through the separately authenticated local transport.

When ~/.config/wb/wb.yaml has a hub: section, the same listener also serves the
bench hub API under /v0/workbench/ and the embedded bench dashboard under
/workbench/, on the DALgo store engine that section names. Without a hub: section
nothing changes, and the bench dashboard needs no sign-in because only this machine
can reach the loopback address it is bound to.

With hub.github.token_file set, a poller reads every repository this machine
publishes and narrates one line on stderr for each webhook delivery and each
poll observation: the event, the repository, and what the hub did with it.
--quiet silences those console lines. wb daemon start never passes it, so a
detached daemon's log file keeps every line.

With hub.github.app set, signed GitHub deliveries at
/v0/workbench/github/webhook are verified and enqueued, the poller is not
started at all (only the repository an event names is pulled), and the start
line ends with webhook=on and the public URL. wb starts no tunnel: forward that URL to this listener yourself
with your own cloudflared or ngrok credentials — see hub/README.md.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := daemonruntime.RequireLoopbackAddress(listenAddress); err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			return deps.Serve(command.Context(), ServeRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Listen: listenAddress, LifecycleState: stateFile, Quiet: quiet, ManagedStart: managedStartFlag}, command.OutOrStdout(), command.ErrOrStderr())
		},
	}
	command.Flags().StringVar(&listenAddress, "listen", daemonruntime.DefaultListen, "loopback listen address")
	command.Flags().StringVar(&stateFile, "lifecycle-state", "", "private lifecycle state path (used by daemon start)")
	_ = command.Flags().MarkHidden("lifecycle-state")
	command.Flags().BoolVar(&managedStartFlag, "managed-start", false, "join the starting lifecycle state this build resolves for its own home (used by daemon start)")
	_ = command.Flags().MarkHidden("managed-start")
	command.Flags().BoolVar(&quiet, "quiet", false, "silence the hub's per-event console lines (the daemon log file still records them)")
	return command
}

func newStart(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var listen, format string
	var jsonOut, forceDetached, replaceOtherRoot bool
	command := &cobra.Command{Use: "start", Short: "Start the local WB daemon, or hand off to the installed WB binary", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon start: %s\n", phase)
			}
			result, err := deps.Start(command.Context(), StartRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, Listen: listen, ForceDetached: forceDetached, ReplaceOtherRoot: replaceOtherRoot}, progress)
			if err != nil {
				return err
			}
			return daemonview.Result(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&listen, "listen", daemonruntime.DefaultListen, "loopback listen address")
	command.Flags().BoolVar(&forceDetached, "force-detached", false, "start a detached daemon even though the runtime's recorded owner is a systemd or launchd supervisor")
	command.Flags().BoolVar(&replaceOtherRoot, "replace-other-root", false, replaceOtherRootUsage)
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

// replaceOtherRootUsage is the one help line for --replace-other-root.
const replaceOtherRootUsage = "on macOS, replace the launchd service registered for a different projects root (refused without this flag)"

func newStatus(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "status", Short: "Report which home owns the local daemon, its provenance, and its reachability", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			result, err := deps.Status(command.Context(), runtime.Flags().ProjectsRoot)
			if err != nil {
				return err
			}
			return daemonview.Result(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newStop(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "stop", Short: "Drain and stop the local WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			result, err := deps.Stop(command.Context(), runtime.Flags().ProjectsRoot)
			if err != nil {
				return err
			}
			// A systemd Restart=always (or launchd KeepAlive) unit undoes a
			// plain stop on its own schedule; naming the supervisor's own
			// stop command is the only way this actually stays stopped.
			switch result.State.Supervisor {
			case daemon.SupervisorSystemd:
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon stop: this daemon is recorded as owned by a systemd user service; its Restart=always policy will bring it back — use `systemctl --user stop %s` to actually stop it\n", deps.SystemdUnit())
			case daemon.SupervisorLaunchd:
				label := result.State.SupervisorLabel
				// wb's own self-managed launchd job (and an old/legacy record
				// with no label at all, which predates any foreign-job
				// concept and is therefore almost certainly wb's own too) is
				// NOT a case where this hint is true: Stop() already boots
				// wb's own job out completely — unlike a FOREIGN job's
				// KeepAlive, nothing is left behind to bring it back, so
				// telling the operator it will return is false on every Mac
				// stop (sneat-dev/wb#622 review item 1).
				if label != "" && label != daemonruntime.LaunchdLabel {
					_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon stop: this daemon is recorded as owned by a launchd agent; its KeepAlive policy will bring it back — use `launchctl bootout gui/%d/%s` to actually stop it\n", deps.Getuid(), label)
				}
			}
			return daemonview.Result(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newRestart(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var format string
	var jsonOut, ifRunning, forceDetached, replaceOtherRoot bool
	command := &cobra.Command{Use: "restart", Short: "Drain, hand off the durable queue, and start the installed WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon restart: %s\n", phase)
			}
			result, err := deps.Restart(command.Context(), RestartRequest{ProjectsRoot: runtime.Flags().ProjectsRoot, IfRunning: ifRunning, ForceDetached: forceDetached, ReplaceOtherRoot: replaceOtherRoot}, progress)
			if err != nil {
				return err
			}
			return daemonview.Result(command.OutOrStdout(), format, result)
		}}
	command.Flags().BoolVar(&ifRunning, "if-running", false, "succeed without starting when no managed daemon is running")
	command.Flags().BoolVar(&forceDetached, "force-detached", false, "start a detached daemon even though the runtime's recorded owner is a systemd or launchd supervisor")
	command.Flags().BoolVar(&replaceOtherRoot, "replace-other-root", false, replaceOtherRootUsage)
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newRecover(runtime shared.Runtime, deps Dependencies) *cobra.Command {
	var apply bool
	var format string
	var jsonOut bool
	command := &cobra.Command{
		Use:          "recover",
		Short:        "Inspect or recover an interrupted daemon lifecycle transition",
		SilenceUsage: true,
		Long: `Inspect the daemon lifecycle lock left by an interrupted WB process.

The default is a dry-run. Recovery refuses a live or ambiguous lock owner and
requires durable evidence that the interrupted transition can be fenced safely.
--apply keeps the stable lock inode, clears its stale owner record, and reconciles
durable lifecycle state: a verified healthy child becomes ready, while a proven
interrupted start or drain becomes stopped. It never deletes an active lock path.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := shared.SelectJSONFormat(format, jsonOut)
			if err != nil {
				return runtime.ExitError(shared.ExitUsage, err.Error())
			}
			result, err := deps.Recover(command.Context(), runtime.Flags().ProjectsRoot, apply)
			if err != nil {
				return err
			}
			err = daemonview.Recovery(command.OutOrStdout(), format, result)
			if err != nil {
				return err
			}
			if apply && result.LockPresent && !result.Eligible && result.Reason != "no_stale_owner" {
				return fmt.Errorf("daemon lifecycle recovery refused: %s: %s", result.Reason, result.Detail)
			}
			return nil
		},
	}
	command.Flags().BoolVar(&apply, "apply", false, "recover the proven stale lifecycle lock (default is dry-run)")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}
