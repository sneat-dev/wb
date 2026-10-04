package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/daemonruntime"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/cockpit"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/dashboard"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/nodeidentity"
	"github.com/sneat-dev/wb/internal/peers"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/repositoryevents"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

// daemonruntime.LaunchdLabel is wb's own launch agent label (darwin only). It lives
// here, not in daemon_process_darwin.go, because platform-independent handoff
// logic (stopAndReplace) needs to tell wb's own self-managed launchd job —
// which it already knows how to re-bootstrap with a new binary — from a
// foreign one it must hand off to instead, on every platform this package
// builds for (sneat-dev/wb#622 review item 1).

// daemonEnvVarsSuppliedBySupervisors lists the environment variables systemd
// and launchd are documented to set on a process they exec, so a detached
// child this build starts itself never inherits stale evidence of a
// supervision it does not actually have (sneat-dev/wb#622 review item 3).

// daemonChildEnvironment is the environment a detached daemon child starts
// with: this process's own environment, minus the variables a supervisor
// would have set. It is used by every startDaemonProcess that inherits the
// environment via os/exec (unix and windows); darwin's launchd job gets an
// explicitly-built environment already and never inherits these by construction.
// daemonRefuseTestBinary refuses to install a supervisor job for, or launch,
// an executable that is a Go test binary. A peers-lane test once reached the
// real startDaemonProcess and overwrote the founder's real
// ~/Library/LaunchAgents/dev.sneat.wb.daemon.plist with a go-build `wb.test`
// path, taking the real daemon down (sneat-dev/wb#622). Every
// startDaemonProcess calls this before doing anything else — before writing a
// plist or unit, and before launching a process — on every platform.
// testing.Testing() catches a test binary regardless of its name;
// the ".test" suffix additionally catches an embedding case where
// testing.Testing() might not (for example a binary built with `go test -c`
// and then run outside of `go test`, which testing.Testing() cannot see).

// runSystemctl is the seam every systemctl invocation in this file goes
// through, so a test can fake systemd's own responses instead of reaching a
// real systemd user manager (mirrors runLaunchctl in
// daemon_process_darwin.go; sneat-dev/wb#622 review items 2 and 5).
//
// Bounded by runSystemctlTimeout: `wb daemon status` and `wb daemon
// start`/`restart`'s existence check must never hang indefinitely on an
// unresponsive or wedged systemd user manager (sneat-dev/wb#622 review round
// 3, item M4) — a status check is exactly the kind of call an operator
// expects back promptly even when something is badly wrong.
//
// exec.CommandContext on its own only kills the DIRECT child; if systemctl
// (or a fake standing in for it) itself forks a grandchild that inherits the
// stdout/stderr pipes CombinedOutput reads, killing the direct child does
// not close those pipes, and Wait blocks until the grandchild independently
// exits — which can be far longer than runSystemctlTimeout, or never
// (sneat-dev/wb#622 review round 4: this exact shape hung Linux CI for 35
// minutes via this seam's own test). cmd.WaitDelay (Go 1.20+) bounds how
// long Wait, after the process is killed, waits for the output-copying
// goroutines before forcibly closing the pipes and returning anyway — the
// general fix, not just a test-only workaround, since a REAL wedged
// systemctl could fork a real grandchild the same way.

// runSystemctlTimeout bounds every runSystemctl invocation. It is a variable
// so a test can shrink it rather than waiting the real bound out.

// daemonIsSystemRunningStates lists the `systemctl --user is-system-running`
// answers that mean the systemd user manager itself is genuinely present and
// answering, whatever shape its own units are currently in. Anything else —
// an empty answer (systemctl absent, or the command could not even run), a
// state this build does not recognize, or "offline" — counts as not present,
// which is the safer default for a check whose only job is to lift an
// otherwise-permanent refusal (sneat-dev/wb#622 review item 5: the previous
// version treated ANY non-empty, non-"offline" answer as present, which
// could not distinguish a real manager state from unrecognized noise).

// daemonSupervisorPresent independently checks whether the named supervisor
// can still be shown to exist, so a stale recorded supervisor does not refuse
// `wb daemon start`/`restart` forever (sneat-dev/wb#622 review item 4). It
// reports what it could confirm, never what it merely failed to disprove: an
// unreachable check (the wrong platform, the tool missing, a timeout) reports
// absent, which is the safer default for a check whose only job is to lift an
// otherwise-permanent refusal.
//
// It does not know a systemd unit's name — nothing in this build records
// one (see State.SupervisorLabel's doc) — so for systemd it can only check
// that a systemd --user manager is reachable at all, which is enough to
// confirm the common stale shape (the user's systemd session, or the whole
// host, is simply gone). For launchd, SupervisorLabel names the exact job, so
// `launchctl print` can check that job specifically and return its name.

// daemonDefaultSystemdUnit is the systemd user unit name `wb daemon status`
// checks for the sneat-dev/wb#617 detector when no override is configured.

// daemonSystemdUnitName names the systemd user unit `wb daemon status`
// checks: the WB_DAEMON_SYSTEMD_UNIT environment variable when the operator
// has set one, or daemonDefaultSystemdUnit otherwise. This build has no other
// daemon configuration file to read a unit name from (sneat-dev/wb#622
// review item 2). A configured value missing the ".service" suffix (an
// operator writing "wb-daemon" rather than "wb-daemon.service") is
// normalized by appending it: systemctl accepts either form, but this
// build's own unit-identity comparisons (ParseCgroupUnit's cgroup path
// component, State.SystemdUnit) always carry the suffix, and an unnormalized
// configured value would never match either (sneat-dev/wb#622 review round
// 3, item M3).
//
// This is the CALLING invocation's own environment, which is not necessarily
// the daemon's: prefer State.SystemdUnit — the unit the daemon observed
// itself running inside at its own serve startup — wherever a live daemon's
// own record is available (see Status's use of it).

// daemonObservedSystemdUnitState runs `systemctl --user show -p
// ActiveState,Result,NRestarts <unit>` and parses the result, so `wb daemon
// status` can compare a specific unit's own state against what the process
// actually answering the port recorded about its own start
// (sneat-dev/wb#622 review item 2). known=false on any query failure —
// systemctl entirely absent (exec.LookPath fails inside runSystemctl's
// default implementation), the user manager unreachable, or the unit simply
// not found — never treated as "the unit is healthy".

// daemonHeartbeatInterval is the daemon's maintenance checkpoint: how often it
// proves its runtime directory still exists, and writes a line saying so. It is
// a variable only so a test can reach the checkpoint without waiting ten
// seconds for it.

// daemonHubStatus reports the self-hosted bench hub. It is read from wb.yaml
// rather than from the daemon's state file: `wb daemon status` runs in a
// different process from `wb daemon serve`, and the hub section is the same
// declaration both of them act on, so reading it here needs no new schema and
// cannot drift from what a restart would mount.

// daemonHubRedeliverySweep reports the missed-webhook recovery sweep's last
// completed pass in `wb daemon status`. LastSweepAt and LastFailureAt are
// pointers so JSON omits them before anything has happened yet.

// daemonHubEventMarker names the last repository event the hub received or
// the daemon acknowledged.

// daemonPublicState is intentionally narrower than the private state file:
// owner tokens fence a replacement process and must not appear in terminal
// logs, CI artifacts, or dashboard-adjacent JSON.

func newDaemonCmd(inv *invocation) *cobra.Command {
	return newDaemonCmdWithDependencies(inv, defaultDaemonDependencies())
}

func newDaemonCmdWithDependencies(inv *invocation, deps daemonDependencies) *cobra.Command {
	command := &cobra.Command{Use: "daemon", Short: "Operate WB's local loopback dashboard and scheduler lifecycle"}
	command.AddCommand(newDaemonServeCmd(inv, deps), newDaemonStartCmd(inv, deps), newDaemonStatusCmd(inv, deps), newDaemonStopCmd(inv, deps), newDaemonRestartCmd(inv, deps), newDaemonRecoverCmd(inv, deps), newDaemonOperationCmd(inv, deps))
	return command
}

func newDaemonServeCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
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
				return usageError(err.Error())
			}
			// --managed-start is how `wb daemon start` claims the starting
			// record without naming a path: the supervisor unit persists the
			// mode, and the daemon resolves its own runtime directory at
			// startup. --lifecycle-state stays accepted for compatibility, and
			// its presence is reported because a pinned path outlives the home
			// it was pinned from.
			pinnedStatePath := stateFile
			managedStart := managedStartFlag || stateFile != ""
			if stateFile == "" {
				resolved, resolveErr := daemonruntime.StatePath(inv.projectsRoot)
				if resolveErr != nil {
					return resolveErr
				}
				stateFile = resolved
			}
			reportPinnedLifecycleState(command.ErrOrStderr(), pinnedStatePath, stateFile)
			state, found, err := (daemon.Store{Path: stateFile}).Load()
			if err != nil {
				return err
			}
			ownerToken := ""
			if managedStart && (!found || state.Status != daemon.StatusStarting) {
				return errors.New("managed daemon startup no longer owns a starting lifecycle state")
			}
			if found && state.Status == daemon.StatusStarting {
				ownerToken = state.OwnerToken
			} else {
				ownerToken, err = deps.Token()
				if err != nil {
					return err
				}
			}
			return serveDashboard(inv, command, deps, listenAddress, daemon.Store{Path: stateFile}, ownerToken, quiet, managedStart)
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

// reportPinnedLifecycleState reports an explicitly supplied lifecycle state
// path. A pinned path is honoured for compatibility, but it is never silent:
// the pinned shape is what let a daemon keep writing into the home a later
// WB_HOME move had abandoned.
func reportPinnedLifecycleState(out io.Writer, pinned, resolved string) {
	if strings.TrimSpace(pinned) == "" {
		return
	}
	_, _ = fmt.Fprintf(out, "daemon lifecycle state is pinned to %s by an explicit --lifecycle-state; an unpinned daemon would resolve %s\n", pinned, resolved)
}

func newDaemonStartCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var listen, format string
	var jsonOut, forceDetached, replaceOtherRoot bool
	command := &cobra.Command{Use: "start", Short: "Start the local WB daemon, or hand off to the installed WB binary", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon start: %s\n", phase)
			}
			result, err := newDaemonController(deps, inv.projectsRoot).WithReplaceOtherRoot(replaceOtherRoot).StartWithProgress(command.Context(), listen, progress, forceDetached)
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
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

func newDaemonStatusCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "status", Short: "Report which home owns the local daemon, its provenance, and its reachability", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result, err := newDaemonController(deps, inv.projectsRoot).Status(command.Context())
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonStopCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "stop", Short: "Drain and stop the local WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result, err := newDaemonController(deps, inv.projectsRoot).Stop(command.Context())
			if err != nil {
				return err
			}
			// A systemd Restart=always (or launchd KeepAlive) unit undoes a
			// plain stop on its own schedule; naming the supervisor's own
			// stop command is the only way this actually stays stopped.
			switch result.State.Supervisor {
			case daemon.SupervisorSystemd:
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon stop: this daemon is recorded as owned by a systemd user service; its Restart=always policy will bring it back — use `systemctl --user stop %s` to actually stop it\n", daemonruntime.DaemonSystemdUnitName(os.Getenv))
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
					_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon stop: this daemon is recorded as owned by a launchd agent; its KeepAlive policy will bring it back — use `launchctl bootout gui/%d/%s` to actually stop it\n", os.Getuid(), label)
				}
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonRestartCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut, ifRunning, forceDetached, replaceOtherRoot bool
	command := &cobra.Command{Use: "restart", Short: "Drain, hand off the durable queue, and start the installed WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon restart: %s\n", phase)
			}
			result, err := newDaemonController(deps, inv.projectsRoot).WithReplaceOtherRoot(replaceOtherRoot).RestartWithProgress(command.Context(), ifRunning, progress, forceDetached)
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().BoolVar(&ifRunning, "if-running", false, "succeed without starting when no managed daemon is running")
	command.Flags().BoolVar(&forceDetached, "force-detached", false, "start a detached daemon even though the runtime's recorded owner is a systemd or launchd supervisor")
	command.Flags().BoolVar(&replaceOtherRoot, "replace-other-root", false, replaceOtherRootUsage)
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonRecoverCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
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
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result, err := newDaemonController(deps, inv.projectsRoot).RecoverLifecycleLock(command.Context(), apply)
			if err != nil {
				return err
			}
			if format == "json" {
				err = writeJSONTo(command.OutOrStdout(), result)
			} else {
				_, err = fmt.Fprintf(command.OutOrStdout(), "daemon recover: eligible=%t, applied=%t, lock_present=%t, owner_pid=%d, owner_alive=%t, state=%s, reason=%s, detail=%q\n",
					result.Eligible, result.Applied, result.LockPresent, result.OwnerPID, result.OwnerAlive, result.StateStatus, result.Reason, result.Detail)
			}
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

func daemonOutputFormat(format string, jsonOut bool) (string, error) {
	if jsonOut {
		if format != "text" && format != "json" {
			return "", fmt.Errorf("--json cannot be combined with --format=%s", format)
		}
		return "json", nil
	}
	if err := requireOutputFormat(format, "text", "json"); err != nil {
		return "", err
	}
	return format, nil
}

// formatOptionalTime renders a nil or zero time as "never", the way an
// operator reading `wb daemon status` before the first sweep has run
// expects, rather than a misleading 0001-01-01 timestamp.
func formatOptionalTime(at *time.Time) string {
	if at == nil || at.IsZero() {
		return "never"
	}
	return at.Format(time.RFC3339)
}

func writeDaemonResult(out io.Writer, format string, result daemonResult) error {
	if format == "json" {
		return writeJSONTo(out, result)
	}
	status := "absent"
	if result.Managed {
		status = string(result.State.Status)
	}
	if result.ReportedState != "" {
		status = result.ReportedState
	}
	_, err := fmt.Fprintf(out, "daemon %s: state=%s, process_manager_running=%t, api_reachable=%t, direct_transport_reachable=%t, installed_provenance=%t", result.Action, status, result.ProcessManagerRunning, result.Reachable, result.DirectTransportReachable, result.ProvenanceMatches)
	if err == nil && result.Identity != "" {
		_, err = fmt.Fprintf(out, ", identity=%s, ready_verified=%t", result.Identity, result.ReadyVerified)
	}
	if err == nil && result.RuntimeDir != "" {
		_, err = fmt.Fprintf(out, ", runtime=%s", result.RuntimeDir)
	}
	if err == nil && result.SocketPath != "" {
		_, err = fmt.Fprintf(out, ", socket=%s", result.SocketPath)
	}
	if err == nil && result.IdentityDetail != "" {
		_, err = fmt.Fprintf(out, ", identity_detail=%q", result.IdentityDetail)
	}
	if err == nil && result.State.StoppedReason != "" {
		_, err = fmt.Fprintf(out, ", stopped_reason=%q", result.State.StoppedReason)
	}
	if err == nil && result.LegacyRuntime != nil {
		_, err = fmt.Fprintf(out, ", legacy_runtime=%q", result.LegacyRuntime.RuntimeDir)
	}
	if err == nil && result.Managed {
		_, err = fmt.Fprintf(out, ", supervisor=%s", result.State.Supervisor)
	}
	if err == nil && result.SupervisorMismatch != "" {
		_, err = fmt.Fprintf(out, ", supervisor_mismatch=%q", result.SupervisorMismatch)
	}
	if err == nil && result.Warning != "" {
		_, err = fmt.Fprintf(out, ", warning=%q", result.Warning)
	}
	if err == nil && result.ReachabilityTransport != "" {
		_, err = fmt.Fprintf(out, ", api_transport=%s", result.ReachabilityTransport)
	}
	if err == nil && result.DirectTransportError != "" {
		_, err = fmt.Fprintf(out, ", direct_transport_error=%q", result.DirectTransportError)
	}
	if err == nil && result.ReachabilityError != "" {
		_, err = fmt.Fprintf(out, ", api_probe_error=%q", result.ReachabilityError)
	}
	if err == nil {
		_, err = fmt.Fprintf(out, ", hub_mounted=%t", result.Hub.Mounted)
	}
	if err == nil && result.Hub.Mounted {
		_, err = fmt.Fprintf(out, ", hub_engine=%s, hub_store=%q, hub_listen=%s", result.Hub.Engine, result.Hub.Store, result.Hub.Listen)
	}
	if err == nil && result.Hub.Mounted {
		_, err = fmt.Fprintf(out, ", hub_polling=%t, hub_poll_interval=%s, hub_repositories_polled=%d, hub_webhook=%t", result.Hub.Polling, result.Hub.PollInterval, result.Hub.RepositoriesPolled, result.Hub.Webhook)
	}
	if err == nil && result.Hub.WebhookPublicURL != "" {
		_, err = fmt.Fprintf(out, ", hub_webhook_public_url=%s", result.Hub.WebhookPublicURL)
	}
	if err == nil && result.Hub.LastEventReceived != nil {
		_, err = fmt.Fprintf(out, ", hub_last_event_received=%q", result.Hub.LastEventReceived.ID)
	}
	if err == nil && result.Hub.LastEventAcknowledged != nil {
		_, err = fmt.Fprintf(out, ", hub_last_event_acknowledged=%q", result.Hub.LastEventAcknowledged.ID)
	}
	if err == nil && result.Hub.WebhookRedelivery != nil {
		_, err = fmt.Fprintf(out, ", hub_webhook_redelivery_last_sweep=%s, hub_webhook_redelivered=%d, hub_webhook_abandoned=%d, hub_webhook_redelivered_uncounted=%d",
			formatOptionalTime(result.Hub.WebhookRedelivery.LastSweepAt), result.Hub.WebhookRedelivery.Redelivered, result.Hub.WebhookRedelivery.Abandoned, result.Hub.WebhookRedelivery.Uncounted)
	}
	if err == nil && result.Hub.WebhookRedelivery != nil && result.Hub.WebhookRedelivery.LastFailureAt != nil {
		_, err = fmt.Fprintf(out, ", hub_webhook_redelivery_last_failure=%s, hub_webhook_redelivery_last_failure_class=%s",
			formatOptionalTime(result.Hub.WebhookRedelivery.LastFailureAt), result.Hub.WebhookRedelivery.LastFailureClass)
	}
	if err == nil {
		_, err = fmt.Fprintln(out)
	}
	return err
}

// withReplaceOtherRoot returns the controller with the explicit
// --replace-other-root request set.

// The daemon's runtime paths all resolve through WB's one home resolver, so a
// WB_HOME move moves the daemon with every other subsystem. They previously
// joined the projects root with a literal ".wb", which is how a live daemon
// ended up serving a socket inside a directory the rest of WB had abandoned.

// lifecycleLock serializes short control-plane transitions. A daemon process
// never holds it, so health/status remain available while a replacement drains.
// The file is a stable inode and is never removed: flock is released by the
// kernel if WB dies, while the recorded PID remains evidence for recover.

// writeLifecycleOwnerPIDInjected is writeLifecycleOwnerPID's test seam
// (task-9 PR-2): every production call site reaches it only through
// writeLifecycleOwnerPID, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged; a test passes its own Injector
// directly to reach a create/chmod/write/sync/close/rename failure
// branch deterministically.

// hubStatus reads the hub section the way serveDashboard will. A configuration
// error is reported as "not mounted" rather than failing status: the operator
// needs status most when serve is refusing to start.

// daemonHubHealth reads the hub block a running daemon publishes on its
// health endpoint.

// refuseDetachedStartUnderSupervisor names the remedy for the runtime's last
// recorded supervisor, or "" to let the detached start proceed. Ownership is
// read from the last recorded state rather than from a currently-alive
// process: the common case this exists for is a supervised daemon that is not
// alive right now (crashed, or between the drain and the supervisor's own
// restart), where starting a detached replacement would still leave two
// owners racing the runtime once the supervisor catches up.
//
// A stale record must not lock an operator out of `wb daemon start` forever
// (sneat-dev/wb#622 review item 4): before refusing, it asks
// deps.SupervisorPresent whether the recorded supervisor can still be shown
// to exist at all, and does not refuse when it cannot. forceDetached is the
// explicit escape hatch for the cases that check cannot resolve either way.

// effectiveSupervisor resolves what a LIVE daemon should be treated as
// supervised by for a stop-and-replace decision, falling back to an
// independent observation when the recorded supervisor is empty, legacy, or
// `none` (all three normalize to SupervisorNone via state.ReportedSupervisor).
//
// Without this fallback, the FIRST self-update into a build that records
// this field at all recreates #617 on a systemd host: a pre-#622 `wb daemon
// serve` never wrote the field at all, and even a build that does write it
// reports `none` for any systemd older than 248 (see DetectSupervisor's
// doc) — in both cases the daemon has been supervised the whole time, but
// its own record cannot say so. `wb daemon restart` (and the self-update
// hook, which just shells out to it) would then SIGTERM the real,
// systemd-managed process and launch a detached --managed-start child,
// which fails to bind once systemd's own Restart=always brings the
// original back (sneat-dev/wb#622 review round 3, item S1).
//
// The fallback only applies while the daemon is ALIVE: state.PID's cgroup is
// only meaningful for a running process, and `wb daemon status` already
// uses the identical cgroup check as its own double-owner fallback (see
// Status's use of observedSupervisor), so this reuses the exact same
// evidence rather than inventing a second one.
//
// launchd has no equivalent independent check in this build: confirming "is
// state.PID's parent PID 1, and what launchd label (if any) does it have"
// would require reading an ARBITRARY pid's parent and XPC service name from
// a SEPARATE process, which nothing here implements (the self-detection
// DetectSupervisor performs only works for the CALLING process's own PID).
// An empty/legacy record for a launchd-supervised daemon is therefore NOT
// upgraded here, and callers must not assume launchd gets the same
// protection systemd does.

// daemonSupervisedInstalledBinaryMatches reads a supervised daemon's recorded
// executable from disk RIGHT NOW and reports whether it already has this
// build's own content — the shape a genuine self-update produces, since it
// replaces that same path in place (sneat-dev/wb#622 review item 6). A
// caller with an unrelated binary (a worktree build, an older or newer
// installed CLI) must not be treated as authorized to restart a production
// supervised daemon merely because its own binary differs from the recorded
// provenance: that is equally true of any unrelated invocation. Shared by
// Start's implicit-caller check and stopAndReplace's explicit-caller refusal
// so the two paths' notion of "matches" can never drift apart.

// stopAndReplace drains the running daemon and then brings the replacement
// up. A supervised daemon (state.Supervisor recorded at its own startup) is
// handed to its supervisor to restart with the new binary rather than
// replaced by a detached process here: every path that would otherwise stop a
// daemon and start a replacement — `wb daemon restart`, the self-update
// after-update hook (which shells out to `wb daemon restart --if-running`),
// and Start's executable-handoff branch once it has confirmed the recorded
// binary already matches (see Start's own earlier check for the mismatched
// case, which never reaches here) — calls this one function, so the
// hand-to-supervisor rule cannot drift between them (sneat-dev/wb#617).
//
// wb's own self-managed launchd job (state.SupervisorLabel ==
// daemonruntime.LaunchdLabel) is deliberately NOT treated as "supervised" here: it is
// wb's own re-bootstrap-and-kickstart cycle (cmd/wb/daemon_process_darwin.go)
// that already correctly restarts it with a new binary, and there is no
// separate external supervisor to hand off to (sneat-dev/wb#622 review item
// 1 — every mac daemon's own launchd job otherwise satisfied Supervisor ==
// launchd, which made every mac restart, including the self-update hook,
// bootout the daemon's own job and then wait forever for a replacement
// nothing would ever start).

// daemonSupervisorRestartTimeout bounds how long stopAndReplace waits for a
// supervisor to bring the daemon back after stopping it. It is a variable so
// a test can shrink it rather than waiting the real bound out.

// daemonSupervisorPollInterval is how often waitForSupervisorReplacement polls
// the lifecycle record while waiting. It is a variable for the same reason.

// waitForSupervisorReplacement polls the lifecycle record — never the port —
// for evidence that the supervisor's own replacement process is up and is
// running the executable this handoff is targeting: Ready, alive, the same
// binary, an unrecycled process generation, and its own health endpoint
// answering as that exact process. It never starts a process itself.
//
// A ready, alive replacement running a DIFFERENT binary than expected fails
// fast with a precise message rather than waiting out the full bound doing
// nothing useful — it is evidence that something else (a racing restart, or a
// third, unrelated build) already won, not that the supervisor is merely
// slow (sneat-dev/wb#622 review item 6). A supervisor that does not come back
// at all within the bound is reported as a distinct, actionable timeout
// (sneat-dev/wb#617 item 3). The context is watched throughout, so a caller
// that cancels does not have to wait out the bound either.

// markStoppedIfUnchanged writes MarkStopped only if the durable record still
// names the exact PID and owner token expected — under stateLock, so the
// check-then-write is atomic with any other writer. A supervisor can bring a
// replacement up fast enough to have already overwritten the record with its
// own Starting/Ready generation by the time this process confirms the OLD
// pid is dead; without this guard, this stale write would clobber that
// replacement's record with a Stopped one bearing the old PID
// (sneat-dev/wb#622 review minor: stop()'s final MarkStopped save). When the
// record has already moved on, it is returned unchanged rather than
// overwritten.

// daemonProcessStartedAt observes the process generation WB is about to record.
// A platform that cannot answer records the zero time, which status reports as
// unknown rather than as a match.

func serveDashboard(inv *invocation, command *cobra.Command, deps daemonDependencies, address string, store daemon.Store, ownerToken string, quiet, managedStart bool) (serveErr error) {
	location, err := daemonruntime.ResolveLocation(inv.projectsRoot)
	if err != nil {
		return err
	}
	listen := deps.listen
	if listen == nil {
		listen = net.Listen
	}
	listener, err := listen("tcp", address)
	if err != nil {
		// A held endpoint is a distinct, actionable condition, not a transient
		// error: another daemon (or an unrelated process) already owns it, and
		// starting a second detached daemon of our own would only race it.
		if daemonruntime.DaemonAddressInUse(err) {
			return fmt.Errorf("daemon endpoint %s is already held by another process: %w; stop it (or point this daemon at another --listen) instead of starting a second daemon", address, err)
		}
		return fmt.Errorf("listen for WB daemon on %s: %w", address, err)
	}
	closeListener := sync.OnceFunc(func() { _ = listener.Close() })
	defer closeListener()
	// The name that was asked for may have resolved to an interface that is not
	// loopback (a name pinned elsewhere, /etc/hosts): serve only what is bound there.
	if err := requireLoopbackBound(listener.Addr()); err != nil {
		return err
	}
	provenance, err := newDaemonController(deps, inv.projectsRoot).Provenance()
	if err != nil {
		return err
	}
	controller := newDaemonController(deps, inv.projectsRoot)
	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		return err
	}
	state, found, err := store.Load()
	if err != nil {
		releaseState()
		return err
	}
	if managedStart && (!found || state.Status != daemon.StatusStarting || state.OwnerToken != ownerToken) {
		releaseState()
		return errors.New("managed daemon startup ownership was superseded")
	}
	// Supervisor detection reads this process's own environment (and its own
	// pid/ppid): only the process a supervisor actually exec'd sees the
	// variables it sets, so this must run in the child, not be inferred later
	// by a reader in a different process (sneat-dev/wb#617).
	getpid, getppid := deps.Getpid, deps.Getppid
	if getpid == nil {
		getpid = os.Getpid
	}
	if getppid == nil {
		getppid = os.Getppid
	}
	supervisorKind, supervisorExecPID, supervisorLabel := daemon.DetectSupervisor(deps.Getenv, getpid(), getppid())
	// Recorded ALONGSIDE detection, in the same process, for the same reason:
	// only this process can read its own /proc/self/cgroup, and a later, separate
	// `wb daemon status` invocation has no portable way to ask for anyone
	// else's (sneat-dev/wb#622 review round 3, item M3). Best-effort: an
	// empty result (this platform's check is not implemented, or this
	// process is not in a service unit's own cgroup) leaves the field empty,
	// and a reader falls back to its own configured/default guess.
	observedCgroupUnit := deps.ObservedCgroupUnit
	if observedCgroupUnit == nil {
		observedCgroupUnit = daemon.ObservedCgroupUnit
	}
	systemdUnit, _ := observedCgroupUnit(getpid())
	if !managedStart {
		state = daemon.NewStartingAt(daemonruntime.OptionalState(state, found), address, provenance, ownerToken, location.Home, location.StatePath, deps.Now())
		state.Supervisor, state.SupervisorExecPID, state.SupervisorLabel = supervisorKind, supervisorExecPID, supervisorLabel
		state.SystemdUnit = systemdUnit
		state.MarkReadyWithProcess(os.Getpid(), daemonruntime.ProcessStartedAt(os.Getpid()), deps.Now())
	} else {
		state.WBHome, state.StatePath = location.Home, location.StatePath
		state.Supervisor, state.SupervisorExecPID, state.SupervisorLabel = supervisorKind, supervisorExecPID, supervisorLabel
		state.SystemdUnit = systemdUnit
		state.MarkStartingPID(os.Getpid(), deps.Now())
	}
	if err := store.Save(state); err != nil {
		releaseState()
		return err
	}
	releaseState()
	localListener, err := daemonruntime.ListenLocal(inv.projectsRoot)
	if err != nil {
		return err
	}
	defer func() { _ = localListener.Close() }()
	rawExecutionPolicyPath, err := daemon.RawExecutionPolicyPath()
	if err != nil {
		return fmt.Errorf("resolve daemon raw-execution policy: %w", err)
	}
	operationsDirectory, err := daemon.OperationsDir(inv.projectsRoot)
	if err != nil {
		return fmt.Errorf("resolve daemon operation store: %w", err)
	}
	queue, err := daemon.NewService(inv.projectsRoot, operationsDirectory, collectVersion().Version, fmt.Sprint(state.Queue.Generation), func() error {
		return daemon.RequireRawExecutionPolicy(rawExecutionPolicyPath, inv.projectsRoot)
	})
	if err != nil {
		return fmt.Errorf("load durable daemon queue: %w", err)
	}
	if managedStart {
		releaseState, err := controller.AcquireStateLock()
		if err != nil {
			return err
		}
		current, ok, err := store.Load()
		if err != nil {
			releaseState()
			return err
		}
		if !ok || current.Status != daemon.StatusStarting || current.OwnerToken != ownerToken || current.PID != os.Getpid() {
			releaseState()
			return errors.New("daemon startup ownership was superseded before serving")
		}
		state = current
		releaseState()
	}
	defer func() {
		closeListener()
		_ = controller.MarkStoppedIfOwned(ownerToken)
	}()
	hubConfigPath := wbconfig.DefaultPath
	if deps.HubConfigPath != nil {
		hubConfigPath = deps.HubConfigPath
	}
	// Narration goes to stderr, which is where the detached daemon's log file
	// already points, so there is no second writer to mirror into.
	narrator := narrate.Writer{Out: command.ErrOrStderr(), Quiet: quiet}
	// Every daemon carries a stable node ID (peer-connectivity#req:node-
	// identity), generated once here and again — idempotently — on the
	// laptop's first `wb peers join`. A failure to create it is reported
	// (always, --quiet included: this is a startup diagnostic on stderr, not
	// one of the per-event console lines --quiet silences) but does not stop
	// the daemon: node identity matters once Task 2 opens a session, not to
	// any surface this task adds. Task 2 makes this fatal instead.
	//
	// The path is built from location.Home rather than calling
	// nodeidentity.Load(projectsRoot, nil) a second time: location was
	// already resolved once, above, from the same projectsRoot every other
	// value in this function depends on, so there is exactly one resolution
	// to get right (and exactly one place a test already has to guard, per
	// the daemon-launch incident) rather than two that could silently
	// diverge if the global were ever empty.
	if _, err := nodeidentity.LoadFile(nodeidentity.PathFromHome(location.Home), nil); err != nil {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), "wb: node identity unavailable:", err)
	}
	// A malformed cockpit: section is an operator mistake worth refusing to
	// start over, not a surface to silently run with defaults.
	cockpitConfig, err := wbconfig.LoadCockpit(hubConfigPath())
	if err != nil {
		return fmt.Errorf("load the cockpit configuration: %w", err)
	}
	mount, err := mountHub(command.Context(), hubConfigPath(), address, narrator, deps.hubTuning)
	if err != nil {
		return fmt.Errorf("mount the bench hub: %w", err)
	}
	defer func() { _ = mount.Close() }()
	// Best-effort: an unresolved log path only disables /api/v1/log (503, which
	// only the owner is told), it never blocks the daemon from serving
	// everything else.
	// daemonStartLogPath is the cross-platform accessor (daemonLogPath is
	// !darwin-only; darwin's launchd unit owns a fixed, home-derived path).
	logPath, _ := daemonruntime.DaemonStartLogPath(inv.projectsRoot)
	// The peers read API is mounted unconditionally — with or without a hub
	// section — per peer-connectivity#req:peers-api's "mounted...on every
	// node": a laptop-only install answers "no downstream peers" instead of
	// falling through to the dashboard's HTML index (the bug a laptop-side
	// `wb peers list`/`get` hit before this fallback existed).
	peersSource := mount.peersSource()
	if peersSource == nil {
		peersSource = emptyPeersSource{}
	}
	peersHandler := peers.NewHandler("/api/v1/peers", peersSource, peersViewerAuthorize(localIdentityID))
	cockpitServer := newCockpitServer(address, cockpitConfig)
	fleetOptions := cockpitFleetOptions(inv.projectsRoot, location.Home, hubConfigPath(), cockpitConfig, command.ErrOrStderr(), os.Hostname)
	fleetOptions.Remotes = withoutOwnAddress(fleetOptions.Remotes, address, fleetOptions.Logf)
	fleetSnapshotter := registerCockpitFleet(cockpitServer, fleetOptions)
	mount.serveExportOf(fleetSnapshotter, cockpitConfig)
	// A hub write is the owner's alone: the same session check the log uses.
	mount.authorizeOwnerWith(cockpitServer.IsOwner)
	server := &http.Server{Handler: dashboard.NewHandler(dashboard.Options{
		Home: cockpit.PagePrefix, Version: collectVersion().Version,
		DaemonPID: os.Getpid(), SchedulerGeneration: state.Queue.Generation,
		Mounts: cockpitServer.MountsWith(mount.handlers()), Hub: mount.hubHealth(), LogPath: logPath,
		// The log is file content: only Cockpit's owner session reads it
		// (cockpit#req:daemon-log-is-owner-only).
		Owner: cockpitServer.IsOwner,
		Peers: peersHandler,
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	rpcPath, rpcHandler := daemonv1connect.NewDaemonServiceHandler(queue)
	rpcMux := http.NewServeMux()
	rpcMux.Handle(rpcPath, daemonruntime.AuthenticatedHandler(ownerToken, rpcHandler))
	// The peer admin routes share the same owner-token authentication and the
	// same unix-socket listener, and are therefore never reachable over the
	// TCP dashboard listener (peer-connectivity#req:admin-requires-owner-
	// credential).
	rpcMux.Handle(peersRPCPrefix, daemonruntime.AuthenticatedHandler(ownerToken, newPeerAdminHTTPHandler(mount)))
	// So does the route that mints a Cockpit login code: holding the owner
	// token is what entitles a caller to an owner session
	// (cockpit#req:owner-session). The file bridge below is handed this mux
	// too, but dispatches only the DaemonService procedures
	// daemonFilePrepareRequest lists, so it never reaches this route.
	rpcMux.Handle(cockpit.LoginCodeRPCPath, daemonruntime.AuthenticatedHandler(ownerToken, cockpitServer.LoginCodeHandler()))
	fileBridge, err := daemonruntime.NewFileBridgeServer(inv.projectsRoot, ownerToken, fmt.Sprint(state.Queue.Generation), rpcMux)
	if err != nil {
		return fmt.Errorf("prepare daemon file bridge: %w", err)
	}
	rpcServer := &http.Server{Handler: rpcMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := daemonruntime.SignalContext(command.Context())
	defer stop()
	// The snapshotter lives as long as the daemon and is stopped, and waited
	// for, on the way out so no refresh outlives the daemon's state.
	defer fleetSnapshotter.Start(ctx)()
	queue.StartLeaseRecovery(ctx)
	if err := startRepositoryEventReceiver(ctx, inv.projectsRoot, hubConfigPath(), command.ErrOrStderr()); err != nil {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), "repository event receiver disabled:", err)
	}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
		_ = rpcServer.Shutdown(shutdown)
	}()
	// The poller is bound to the server's context, so a shutdown stops it
	// without a second lifecycle to get wrong.
	mount.startPolling(ctx)
	// The sweep is bound to the same shutdown context, so it stops when the
	// poller does with no second lifecycle to get wrong.
	mount.startRedeliverySweep(ctx)
	if _, err := fmt.Fprintf(command.OutOrStdout(), "WB dashboard: http://%s\n", listener.Addr()); err != nil {
		return err
	}
	if line := mount.StartLine(); line != "" {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), line)
	}
	errorsCh := make(chan error, 4)
	go func() { errorsCh <- classifyServeResult(server.Serve(listener)) }()
	go func() { errorsCh <- classifyServeResult(rpcServer.Serve(localListener)) }()
	go func() { errorsCh <- classifyServeResult(fileBridge.Serve(ctx)) }()
	go func() {
		if err := daemonruntime.RuntimeGuard(command.ErrOrStderr(), ctx, address, store, state, ownerToken, deps.GuardTicker); err != nil {
			errorsCh <- err
		}
	}()
	return awaitDaemonServeResult(<-errorsCh)
}

// errCleanDaemonShutdown is the single sentinel every serving goroutine's
// benign shutdown outcome is normalized to (see classifyServeResult), so
// serveDashboard returns nil down exactly one code path no matter which
// goroutine's result the select above happens to read first.
var errCleanDaemonShutdown = errors.New("daemon: clean shutdown")

// classifyServeResult normalizes the three benign outcomes a serving
// goroutine can report once ctx is cancelled — nil (fileBridge.Serve, which
// already treats ctx.Done as success), http.ErrServerClosed, and
// net.ErrClosed (both from server.Serve/rpcServer.Serve after Shutdown or
// listener.Close) — to errCleanDaemonShutdown. Which of the serving
// goroutines finishes first after cancellation is decided by OS scheduling;
// without this normalization, the "clean shutdown" branch in
// awaitDaemonServeResult was only taken when an HTTP server happened to win
// that race, not when fileBridge.Serve's already-nil result did. Any other
// error is returned unchanged, so a real serve failure still propagates
// exactly as before.
func classifyServeResult(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return errCleanDaemonShutdown
	}
	return err
}

// awaitDaemonServeResult reduces the first classified result read from
// errorsCh to serveDashboard's return value: nil after a clean shutdown,
// the original error otherwise.
func awaitDaemonServeResult(err error) error {
	if errors.Is(err, errCleanDaemonShutdown) {
		return nil
	}
	return err
}

// daemonRuntimeGuard keeps the heartbeat flowing while proving the runtime
// directory it heartbeats from still exists.
//
// A daemon whose runtime directory was removed underneath it must stop: it
// otherwise keeps writing into a path that no longer belongs to any WB home,
// which is how a stranded daemon stayed invisible while looking healthy. It
// returns an error so the process exits non-zero, and records the reason first
// so a supervisor restart does not erase the only evidence of it.

// daemonRuntimeIntact proves the daemon is still writing where it says it is:
// the directory holding its lifecycle record must still be a real directory,
// and the record itself must still be there. The check follows the record
// rather than the resolved home so an explicitly pinned state path is covered
// by the same proof.

func startRepositoryEventReceiver(ctx context.Context, projectsRoot, configPath string, out io.Writer) error {
	eventQueue, err := repositoryevents.NewQueue(projectsRoot)
	if err != nil {
		return err
	}
	progress := func(message string) { _, _ = fmt.Fprintln(out, message) }
	go eventQueue.Run(ctx, repositoryevents.SyncProcessor{ProjectsRoot: projectsRoot}, progress)

	config, err := remotestate.LoadConfig(configPath)
	if err != nil {
		var unconfigured *remotestate.UnconfiguredError
		if errors.As(err, &unconfigured) {
			return nil
		}
		return err
	}
	if config.Provider != "hub" {
		return nil
	}
	provider, err := hub.New(hub.Options{BaseURL: config.URL, Machine: config.Machine, TokenFile: config.TokenFile})
	if err != nil {
		return err
	}
	runtimeDir, runtimeErr := daemon.RuntimeDir(projectsRoot)
	if runtimeErr != nil {
		return runtimeErr
	}
	cursorPath := filepath.Join(runtimeDir, "daemon", "repository-events", "cursor.json")
	receiver := repositoryevents.Receiver{Source: provider, Queue: eventQueue, Cursor: repositoryevents.CursorStore{Path: cursorPath}, Progress: progress}
	go receiver.Run(ctx)
	return nil
}

// requireLoopbackBound refuses an address a listener was actually bound to
// unless it is a TCP address on the loopback interface, whatever name the
// --listen value used to ask for it.
func requireLoopbackBound(bound net.Addr) error {
	tcp, ok := bound.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return usageError(fmt.Sprintf("the daemon is bound to %s, which is not a loopback address; it was not started, publish it through an authenticated tunnel instead", bound))
	}
	return nil
}

type daemonDependencies struct {
	daemonruntime.Dependencies
	listen    func(string, string) (net.Listener, error)
	hubTuning *hubTuning
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{Dependencies: daemonruntime.DefaultDependencies(usageError)}
}
func newDaemonController(deps daemonDependencies, root string) daemonruntime.Controller {
	return daemonruntime.NewController(deps.Dependencies, root)
}

type daemonResult = daemonruntime.Result
type daemonHubStatus = daemonruntime.HubStatus
type daemonRecoveryResult = daemonruntime.RecoveryResult
