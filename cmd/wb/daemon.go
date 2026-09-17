package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/daemonlifecycle"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/dashboard"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/repositoryevents"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const (
	daemonDefaultListen           = "127.0.0.1:8766"
	daemonReadyTimeout            = 5 * time.Second
	daemonStopTimeout             = 5 * time.Second
	daemonRestartProgressInterval = 8 * time.Second
)

// daemonHeartbeatInterval is the daemon's maintenance checkpoint: how often it
// proves its runtime directory still exists, and writes a line saying so. It is
// a variable only so a test can reach the checkpoint without waiting ten
// seconds for it.
var daemonHeartbeatInterval = 10 * time.Second

type daemonResult struct {
	Action                   string            `json:"action"`
	Managed                  bool              `json:"managed"`
	ProcessManagerRunning    bool              `json:"process_manager_running"`
	Reachable                bool              `json:"reachable"`
	ReachabilityError        string            `json:"reachability_error,omitempty"`
	ReachabilityTransport    string            `json:"reachability_transport,omitempty"`
	DirectTransportReachable bool              `json:"direct_transport_reachable"`
	DirectTransportError     string            `json:"direct_transport_error,omitempty"`
	ProvenanceMatches        bool              `json:"provenance_matches_installed"`
	State                    daemonPublicState `json:"state,omitempty"`
	AlreadyRunning           bool              `json:"already_running,omitempty"`
	AutomaticVersionHandoff  bool              `json:"automatic_version_handoff,omitempty"`
	Hub                      daemonHubStatus   `json:"hub"`

	// Identity answers "whose daemon is this?" separately from "did something
	// answer on the endpoint?". ReadyVerified is the only condition under which
	// status may present a daemon as ready; ReportedState is what a reader
	// should act on, and never claims ready that was not verified.
	Identity       daemonIdentity `json:"identity,omitempty"`
	IdentityDetail string         `json:"identity_detail,omitempty"`
	ReadyVerified  bool           `json:"ready_verified"`
	ReportedState  string         `json:"reported_state,omitempty"`

	// The runtime location this invocation resolved. Status reports it so an
	// operator can name the endpoint and the record without starting anything.
	WBHome     string `json:"wb_home,omitempty"`
	RuntimeDir string `json:"runtime_path,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	StatePath  string `json:"state_path,omitempty"`

	// LegacyRuntime names a daemon still serving the runtime directory WB used
	// before it resolved its home. Its presence is why a start was refused.
	LegacyRuntime *daemonLegacyEndpoint `json:"legacy_runtime,omitempty"`
}

// daemonHubStatus reports the self-hosted bench hub. It is read from wb.yaml
// rather than from the daemon's state file: `wb daemon status` runs in a
// different process from `wb daemon serve`, and the hub section is the same
// declaration both of them act on, so reading it here needs no new schema and
// cannot drift from what a restart would mount.
type daemonHubStatus struct {
	Mounted bool   `json:"mounted"`
	Engine  string `json:"engine,omitempty"`
	Store   string `json:"store,omitempty"`
	Listen  string `json:"listen,omitempty"`
	// Polling and PollInterval come from the same declaration a restart would
	// act on. RepositoriesPolled and the delivery markers come from the
	// running daemon's health endpoint, because only the serving process has
	// them: reading the hub store from this process would open a second
	// writer to the operator's inGitDB project.
	Polling      bool   `json:"polling"`
	PollInterval string `json:"poll_interval,omitempty"`
	// Webhook and WebhookPublicURL also come from the declaration: an App is
	// configured or it is not, and the URL is where the operator's tunnel must
	// forward GitHub's deliveries.
	Webhook               bool                  `json:"webhook"`
	WebhookPublicURL      string                `json:"webhook_public_url,omitempty"`
	RepositoriesPolled    int                   `json:"repositories_polled"`
	LastEventReceived     *daemonHubEventMarker `json:"last_event_received,omitempty"`
	LastEventAcknowledged *daemonHubEventMarker `json:"last_event_acknowledged,omitempty"`
}

// daemonHubEventMarker names the last repository event the hub received or
// the daemon acknowledged.
type daemonHubEventMarker struct {
	ID         string    `json:"id,omitempty"`
	Event      string    `json:"event,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

// daemonPublicState is intentionally narrower than the private state file:
// owner tokens fence a replacement process and must not appear in terminal
// logs, CI artifacts, or dashboard-adjacent JSON.
type daemonPublicState struct {
	SchemaVersion int               `json:"schema_version"`
	Status        daemon.Status     `json:"status"`
	PID           int               `json:"pid,omitempty"`
	Listen        string            `json:"listen"`
	Provenance    daemon.Provenance `json:"provenance"`
	Queue         daemonPublicQueue `json:"queue"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at"`
	// WBHome, StatePath and StoppedReason are the record's own account of where
	// it lives and, for a stop it did not choose, why. They are part of the
	// public projection because identity is exactly what a reader must be able
	// to check.
	WBHome        string `json:"wb_home,omitempty"`
	StatePath     string `json:"state_path,omitempty"`
	StoppedReason string `json:"stopped_reason,omitempty"`
}

type daemonPublicQueue struct {
	SchemaVersion int                `json:"schema_version"`
	Generation    uint64             `json:"generation"`
	Owner         daemon.Provenance  `json:"owner"`
	HandoffFrom   *daemon.Provenance `json:"handoff_from,omitempty"`
	HandoffAt     *time.Time         `json:"handoff_at,omitempty"`
}

func publicDaemonState(state daemon.State) daemonPublicState {
	return daemonPublicState{
		SchemaVersion: state.SchemaVersion, Status: state.Status, PID: state.PID,
		Listen: state.Listen, Provenance: state.Provenance, StartedAt: state.StartedAt, UpdatedAt: state.UpdatedAt,
		WBHome: state.WBHome, StatePath: state.StatePath, StoppedReason: state.StoppedReason,
		Queue: daemonPublicQueue{SchemaVersion: state.Queue.SchemaVersion, Generation: state.Queue.Generation,
			Owner: state.Queue.Owner, HandoffFrom: state.Queue.HandoffFrom, HandoffAt: state.Queue.HandoffAt},
	}
}

type daemonDependencies struct {
	now           func() time.Time
	executable    func() (string, error)
	start         func(string, []string, string) (int, error)
	alive         func(int) bool
	stop          func(int) error
	sleep         func(time.Duration)
	version       func() versionInfo
	token         func() (string, error)
	health        func(context.Context, string) error
	ownedHealth   func(context.Context, string, int, uint64) error
	bridgeHealth  func(context.Context, string, string) error
	restartTicker func(time.Duration) (<-chan time.Time, func())
	rawPolicy     func(string) (bool, string, error)
	localClient   func(string, string) (*http.Client, error)
	hubConfigPath func() string
	hubHealth     func(context.Context, string) (daemonHubStatus, error)
	// hubTuning is nil everywhere but the whole-journey end-to-end test; see
	// the type's documentation.
	hubTuning *hubTuning
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{
		now:          func() time.Time { return time.Now().UTC() },
		executable:   os.Executable,
		start:        startDaemonProcess,
		alive:        daemonProcessAlive,
		stop:         stopDaemonProcess,
		sleep:        time.Sleep,
		version:      collectVersion,
		token:        daemonOwnerToken,
		health:       daemonHealthy,
		ownedHealth:  daemonOwnedHealthy,
		bridgeHealth: daemonFileBridgeHealthy,
		restartTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		localClient:   daemonLocalHTTPClient,
		hubConfigPath: wbconfig.DefaultPath,
		hubHealth:     daemonHubHealth,
		rawPolicy: func(root string) (bool, string, error) {
			path, err := daemon.RawExecutionPolicyPath()
			if err != nil {
				return false, "", err
			}
			allowed, err := daemon.LoadRawExecutionPolicy(path, root)
			return allowed, path, err
		},
	}
}

func newDaemonCmd() *cobra.Command { return newDaemonCmdWithDependencies(defaultDaemonDependencies()) }

func newDaemonCmdWithDependencies(deps daemonDependencies) *cobra.Command {
	command := &cobra.Command{Use: "daemon", Short: "Operate WB's local loopback dashboard and scheduler lifecycle"}
	command.AddCommand(newDaemonServeCmd(deps), newDaemonStartCmd(deps), newDaemonStatusCmd(deps), newDaemonStopCmd(deps), newDaemonRestartCmd(deps), newDaemonRecoverCmd(deps), newDaemonOperationCmd(deps))
	return command
}

func newDaemonServeCmd(deps daemonDependencies) *cobra.Command {
	var listenAddress, stateFile string
	var quiet, managedStartFlag bool
	command := &cobra.Command{
		Use: "serve", Short: "Serve the read-only dashboard and API on a loopback address",
		Long: `Serve WB's embedded operations dashboard and versioned read-only API.

The listener is loopback-only. Publish it to registered machines through a
protected Cloudflare Tunnel or another authenticated reverse proxy; do not bind
the daemon directly to a public interface. Mutating operation RPCs are served
only through the separately authenticated local transport.

When ~/.config/wb/wb.yaml has a hub: section, the same listener also serves the
bench hub API under /v0/workbench/ and the embedded bench dashboard under
/workbench/, on the DALgo store engine that section names. Without a hub: section
nothing changes, and the dashboard needs no sign-in because only this machine
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
			if err := requireLoopbackAddress(listenAddress); err != nil {
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
				resolved, resolveErr := daemonStatePath(projectsRoot)
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
				ownerToken, err = deps.token()
				if err != nil {
					return err
				}
			}
			return serveDashboard(command, deps, listenAddress, daemon.Store{Path: stateFile}, ownerToken, quiet, managedStart)
		},
	}
	command.Flags().StringVar(&listenAddress, "listen", daemonDefaultListen, "loopback listen address")
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

func newDaemonStartCmd(deps daemonDependencies) *cobra.Command {
	var listen, format string
	var jsonOut bool
	command := &cobra.Command{Use: "start", Short: "Start the local WB daemon, or hand off to the installed WB binary", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result, err := newDaemonController(deps, projectsRoot).Start(command.Context(), listen)
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&listen, "listen", daemonDefaultListen, "loopback listen address")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonStatusCmd(deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "status", Short: "Report which home owns the local daemon, its provenance, and its reachability", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result, err := newDaemonController(deps, projectsRoot).Status(command.Context())
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonStopCmd(deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut bool
	command := &cobra.Command{Use: "stop", Short: "Drain and stop the local WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			result, err := newDaemonController(deps, projectsRoot).Stop(command.Context())
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

func newDaemonRestartCmd(deps daemonDependencies) *cobra.Command {
	var format string
	var jsonOut, ifRunning bool
	command := &cobra.Command{Use: "restart", Short: "Drain, hand off the durable queue, and start the installed WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon restart: %s\n", phase)
			}
			result, err := newDaemonController(deps, projectsRoot).RestartWithProgress(command.Context(), ifRunning, progress)
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().BoolVar(&ifRunning, "if-running", false, "succeed without starting when no managed daemon is running")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

type daemonRecoveryResult struct {
	Action      string        `json:"action"`
	Applied     bool          `json:"applied"`
	LockPath    string        `json:"lock_path"`
	OwnerPath   string        `json:"owner_path"`
	LockPresent bool          `json:"lock_present"`
	Eligible    bool          `json:"eligible"`
	OwnerPID    int           `json:"owner_pid,omitempty"`
	OwnerAlive  bool          `json:"owner_alive"`
	StateStatus daemon.Status `json:"state_status,omitempty"`
	Reason      string        `json:"reason"`
	Detail      string        `json:"detail,omitempty"`
}

var errDaemonLifecycleBusy = errors.New("daemon lifecycle transition is already in progress")

func newDaemonRecoverCmd(deps daemonDependencies) *cobra.Command {
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
			result, err := newDaemonController(deps, projectsRoot).RecoverLifecycleLock(command.Context(), apply)
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
	if err == nil {
		_, err = fmt.Fprintln(out)
	}
	return err
}

type daemonController struct {
	deps  daemonDependencies
	store daemon.Store
	root  string
}

func newDaemonController(deps daemonDependencies, root string) daemonController {
	statePath, _ := daemonStatePath(root)
	return daemonController{deps: deps, store: daemon.Store{Path: statePath}, root: root}
}

// The daemon's runtime paths all resolve through WB's one home resolver, so a
// WB_HOME move moves the daemon with every other subsystem. They previously
// joined the projects root with a literal ".wb", which is how a live daemon
// ended up serving a socket inside a directory the rest of WB had abandoned.
func daemonStatePath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, daemon.StateFileName), nil
}

func daemonLifecycleLockPath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lifecycle.lock"), nil
}

func daemonLifecycleOwnerPath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.lifecycle.owner"), nil
}

func daemonStateLockPath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.state.lock"), nil
}

func (controller daemonController) stateLock() (func(), error) {
	path, err := daemonStateLockPath(controller.root)
	if err != nil {
		return nil, err
	}
	if err := secureDaemonRuntime(controller.root); err != nil {
		return nil, fmt.Errorf("secure daemon runtime: %w", err)
	}
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	fd, err := unix.Open(path, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
	created := err == nil
	if errors.Is(err, unix.EEXIST) {
		fd, err = unix.Open(path, flags, 0o600)
	}
	if err != nil {
		return nil, fmt.Errorf("open daemon state lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, errors.New("wrap daemon state lock")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect daemon state lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("daemon state lock must be a single-link owner-only regular file: %s", path)
	}
	if created {
		err = daemonlifecycle.ProtectOwnerOnlyFile(file)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect daemon state lock: %w", err)
	}
	if err := daemonlifecycle.ValidateOwnerOnlyFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("validate daemon state lock permissions: %w", err)
	}
	deadline := time.Now().Add(daemonReadyTimeout)
	for {
		locked, err := tryLockDaemonFile(file)
		if err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("lock daemon state: %w", err)
		}
		if locked {
			return func() {
				_ = unlockDaemonFile(file)
				_ = file.Close()
			}, nil
		}
		if time.Now().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another process held the daemon state lock for %s", daemonReadyTimeout)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// lifecycleLock serializes short control-plane transitions. A daemon process
// never holds it, so health/status remain available while a replacement drains.
// The file is a stable inode and is never removed: flock is released by the
// kernel if WB dies, while the recorded PID remains evidence for recover.
func (controller daemonController) lifecycleLock() (func(), error) {
	file, _, created, err := controller.openLifecycleLock(true)
	if err != nil {
		return nil, err
	}
	pid := 0
	if !created {
		pid, err = controller.lifecycleOwnerPID(file)
		if err != nil {
			_ = file.Close()
			return nil, err
		}
	}
	if pid > 0 && controller.deps.alive(pid) {
		_ = file.Close()
		return nil, fmt.Errorf("daemon lifecycle lock names live process %d; run `wb daemon recover` to inspect it", pid)
	}
	if pid > 0 {
		if _, err := controller.stableLifecycleState(); err != nil {
			_ = file.Close()
			return nil, fmt.Errorf("daemon lifecycle lock owner %d is dead but recovery is unsafe: %w", pid, err)
		}
	}
	if err := controller.writeLifecycleOwnerPID(os.Getpid()); err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() {
		_ = controller.writeLifecycleOwnerPID(0)
		_ = unlockDaemonFile(file)
		_ = file.Close()
	}, nil
}

func (controller daemonController) openLifecycleLock(create bool) (*os.File, bool, bool, error) {
	path, err := daemonLifecycleLockPath(controller.root)
	if err != nil {
		return nil, false, false, err
	}
	if create {
		if err := secureDaemonRuntime(controller.root); err != nil {
			return nil, false, false, fmt.Errorf("secure daemon runtime: %w", err)
		}
	}
	flags := unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if create {
		flags |= unix.O_CREAT
	}
	created := false
	var fd int
	if create {
		fd, err = unix.Open(path, flags|unix.O_EXCL, 0o600)
		if err == nil {
			created = true
		} else if errors.Is(err, unix.EEXIST) {
			fd, err = unix.Open(path, flags&^unix.O_CREAT, 0o600)
		}
	} else {
		fd, err = unix.Open(path, flags, 0o600)
	}
	if errors.Is(err, unix.ENOENT) && !create {
		return nil, false, false, nil
	}
	if err != nil {
		return nil, false, false, fmt.Errorf("open daemon lifecycle lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), path)
	if file == nil {
		_ = unix.Close(fd)
		return nil, false, false, errors.New("wrap daemon lifecycle lock")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("inspect daemon lifecycle lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("daemon lifecycle lock must be a single-link owner-only regular file: %s", path)
	}
	if created {
		err = daemonlifecycle.ProtectOwnerOnlyFile(file)
	}
	if err != nil {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("protect daemon lifecycle lock: %w", err)
	}
	if err := daemonlifecycle.ValidateOwnerOnlyFile(file); err != nil {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("validate daemon lifecycle lock permissions: %w", err)
	}
	locked, err := tryLockDaemonFile(file)
	if err != nil {
		_ = file.Close()
		return nil, true, created, fmt.Errorf("lock daemon lifecycle transition: %w", err)
	}
	if !locked {
		_ = file.Close()
		return nil, true, created, fmt.Errorf("%w; run `wb daemon status` and retry after it reaches a terminal state", errDaemonLifecycleBusy)
	}
	return file, true, created, nil
}

func lifecycleLockPID(file *os.File) (pid int, empty bool, err error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, false, err
	}
	data, err := io.ReadAll(io.LimitReader(file, 65))
	if err != nil {
		return 0, false, err
	}
	if len(data) == 0 {
		return 0, true, nil
	}
	text := string(data)
	if len(data) > 64 || !strings.HasPrefix(text, "pid=") || !strings.HasSuffix(text, "\n") || strings.Count(text, "\n") != 1 {
		return 0, false, errors.New("daemon lifecycle lock has ambiguous ownership metadata")
	}
	pid, err = strconv.Atoi(strings.TrimSuffix(strings.TrimPrefix(text, "pid="), "\n"))
	if err != nil || pid < 0 {
		return 0, false, errors.New("daemon lifecycle lock has ambiguous ownership metadata")
	}
	return pid, false, nil
}

func (controller daemonController) lifecycleOwnerPID(legacyLock *os.File) (int, error) {
	path, err := daemonLifecycleOwnerPath(controller.root)
	if err != nil {
		return 0, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if errors.Is(err, unix.ENOENT) {
		pid, empty, legacyErr := lifecycleLockPID(legacyLock)
		if legacyErr != nil {
			return 0, legacyErr
		}
		if empty {
			// An empty stable inode with no sidecar is the crash-safe initial
			// state: no lifecycle mutation can occur before the sidecar write.
			return 0, nil
		}
		return pid, nil
	}
	if err != nil {
		return 0, fmt.Errorf("open daemon lifecycle owner: %w", err)
	}
	owner := os.NewFile(uintptr(fd), "wb-daemon-lifecycle-owner")
	if owner == nil {
		_ = unix.Close(fd)
		return 0, errors.New("wrap daemon lifecycle owner")
	}
	defer func() { _ = owner.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return 0, fmt.Errorf("inspect daemon lifecycle owner: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		return 0, fmt.Errorf("daemon lifecycle owner must be a single-link owner-only regular file: %s", path)
	}
	if err := validateDaemonLifecycleFilePermissions(path, uint32(stat.Mode)); err != nil {
		return 0, fmt.Errorf("validate daemon lifecycle owner permissions: %w", err)
	}
	pid, empty, err := lifecycleLockPID(owner)
	if err != nil {
		return 0, err
	}
	if empty {
		return 0, errors.New("daemon lifecycle owner has no ownership metadata")
	}
	return pid, nil
}

func (controller daemonController) writeLifecycleOwnerPID(pid int) error {
	path, err := daemonLifecycleOwnerPath(controller.root)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := os.CreateTemp(directory, ".daemon-lifecycle-owner-*")
	if err != nil {
		return fmt.Errorf("create daemon lifecycle owner: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := temporary.Chmod(0o600); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := daemonlifecycle.ProtectOwnerOnly(temporaryName); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect daemon lifecycle owner: %w", err)
	}
	if _, err := fmt.Fprintf(temporary, "pid=%d\n", pid); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write daemon lifecycle owner: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync daemon lifecycle owner: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryName, path); err != nil {
		return fmt.Errorf("replace daemon lifecycle owner: %w", err)
	}
	return nil
}

func (controller daemonController) stableLifecycleState() (daemon.Status, error) {
	state, found, err := controller.store.Load()
	if err != nil {
		return "", err
	}
	if !found {
		// Every daemon operation acquires the lifecycle lock before reading or
		// creating state, and launch persists Starting before spawning. A dead
		// owner with no state therefore performed no daemon mutation.
		return "", nil
	}
	switch state.Status {
	case daemon.StatusReady:
		if state.PID <= 0 || !controller.deps.alive(state.PID) {
			return state.Status, errors.New("daemon state says ready but its process is not alive; run `wb daemon status` before recovery")
		}
	case daemon.StatusStopped:
		if state.PID != 0 {
			return state.Status, errors.New("daemon state says stopped but still names a process")
		}
	default:
		return state.Status, fmt.Errorf("daemon lifecycle state is %s, not a stable ready or stopped state", state.Status)
	}
	return state.Status, nil
}

func (controller daemonController) RecoverLifecycleLock(ctx context.Context, apply bool) (daemonRecoveryResult, error) {
	lockPath, lockErr := daemonLifecycleLockPath(controller.root)
	if lockErr != nil {
		return daemonRecoveryResult{}, lockErr
	}
	ownerPath, ownerErr := daemonLifecycleOwnerPath(controller.root)
	if ownerErr != nil {
		return daemonRecoveryResult{}, ownerErr
	}
	result := daemonRecoveryResult{Action: "recover", LockPath: lockPath, OwnerPath: ownerPath, Reason: "no_lock"}
	file, found, _, err := controller.openLifecycleLock(false)
	if errors.Is(err, errDaemonLifecycleBusy) {
		result.LockPresent = true
		result.Reason = "active_transition"
		result.Detail = err.Error()
		return result, nil
	}
	if err != nil {
		return result, err
	}
	if !found {
		return result, nil
	}
	defer func() {
		_ = unlockDaemonFile(file)
		_ = file.Close()
	}()
	result.LockPresent = true
	pid, err := controller.lifecycleOwnerPID(file)
	if err != nil {
		result.Reason = "ambiguous_owner"
		result.Detail = err.Error()
		return result, nil
	}
	result.OwnerPID = pid
	if pid == 0 {
		result.Reason = "no_stale_owner"
		result.Detail = "the lifecycle lock is idle and has no stale owner to recover"
		return result, nil
	}
	result.OwnerAlive = controller.deps.alive(pid)
	if result.OwnerAlive {
		result.Reason = "owner_alive"
		result.Detail = fmt.Sprintf("daemon lifecycle lock owner process %d is still alive", pid)
		return result, nil
	}
	releaseState, err := controller.stateLock()
	if err != nil {
		return result, err
	}
	defer releaseState()
	state, found, err := controller.store.Load()
	if err != nil {
		return result, err
	}
	if !found {
		result.Eligible = true
		result.Reason = "interrupted_before_state"
		if apply {
			if err := controller.writeLifecycleOwnerPID(0); err != nil {
				return result, err
			}
			result.Applied = true
		}
		return result, nil
	}
	result.StateStatus = state.Status
	switch state.Status {
	case daemon.StatusReady, daemon.StatusStopped:
		if _, err := controller.stableLifecycleState(); err != nil {
			result.Reason = "unsafe_state"
			result.Detail = err.Error()
			return result, nil
		}
		result.Reason = "stale_owner_dead"
	case daemon.StatusStarting:
		if state.PID > 0 && controller.deps.alive(state.PID) {
			current, provenanceErr := controller.provenance()
			if provenanceErr != nil {
				return result, provenanceErr
			}
			if !state.Provenance.SameBinary(current) {
				result.Reason, result.Detail = "startup_process_unverified", "live daemon startup belongs to a different executable"
				return result, nil
			}
			if healthErr := controller.ownedHealth(ctx, state.Listen, state.PID, state.Queue.Generation); healthErr != nil {
				result.Reason = "startup_process_unverified"
				result.Detail = fmt.Sprintf("live daemon startup could not be verified: %v", healthErr)
				return result, nil
			}
			result.Reason = "orphaned_healthy_start"
			if apply {
				state.MarkReadyWithProcess(state.PID, daemonProcessStartedAt(state.PID), controller.deps.now())
				if err := controller.store.Save(state); err != nil {
					return result, err
				}
			}
			break
		}
		if state.PID == 0 {
			if controller.deps.now().Sub(state.UpdatedAt) < daemonReadyTimeout {
				result.Reason, result.Detail = "startup_grace_period", "daemon startup is still inside its readiness grace period"
				return result, nil
			}
			current, provenanceErr := controller.provenance()
			if provenanceErr != nil {
				return result, provenanceErr
			}
			if !state.Provenance.SameBinary(current) {
				result.Reason, result.Detail = "unfenced_startup", "daemon startup has no recorded process and belongs to a different executable; refusing unfenced recovery"
				return result, nil
			}
			if controller.deps.health(ctx, state.Listen) == nil {
				result.Reason, result.Detail = "startup_api_reachable", "daemon startup has no recorded process but its API is reachable"
				return result, nil
			}
		}
		result.Reason = "interrupted_start"
		if apply {
			token, tokenErr := controller.deps.token()
			if tokenErr != nil {
				return result, tokenErr
			}
			state.OwnerToken = token
			state.Queue.OwnerToken = token
			state.MarkStopped(controller.deps.now())
			if err := controller.store.Save(state); err != nil {
				return result, err
			}
		}
	case daemon.StatusDraining:
		if state.PID > 0 && controller.deps.alive(state.PID) {
			result.Reason = "drain_process_alive"
			result.Detail = fmt.Sprintf("daemon drain process %d is still alive", state.PID)
			return result, nil
		}
		result.Reason = "interrupted_drain"
		if apply {
			state.MarkStopped(controller.deps.now())
			if err := controller.store.Save(state); err != nil {
				return result, err
			}
		}
	default:
		return result, fmt.Errorf("unsupported daemon lifecycle state %q", state.Status)
	}
	result.Eligible = true
	if apply {
		if err := controller.writeLifecycleOwnerPID(0); err != nil {
			return result, err
		}
		result.Applied = true
	}
	return result, nil
}

func (controller daemonController) provenance() (daemon.Provenance, error) {
	executable, err := controller.deps.executable()
	if err != nil {
		return daemon.Provenance{}, err
	}
	version := controller.deps.version()
	return daemon.ProvenanceForExecutable(executable, version.Version, version.Revision, version.Built)
}

func (controller daemonController) ownedHealth(ctx context.Context, listen string, pid int, generation uint64) error {
	if pid <= 0 || !controller.deps.alive(pid) {
		return fmt.Errorf("daemon process %d is not alive", pid)
	}
	if controller.deps.ownedHealth != nil {
		return controller.deps.ownedHealth(ctx, listen, pid, generation)
	}
	return controller.deps.health(ctx, listen)
}

func (controller daemonController) markStoppedIfOwned(ownerToken string) error {
	releaseState, err := controller.stateLock()
	if err != nil {
		return err
	}
	defer releaseState()
	current, found, err := controller.store.Load()
	if err != nil || !found || current.OwnerToken != ownerToken {
		return err
	}
	current.MarkStopped(controller.deps.now())
	return controller.store.Save(current)
}

func (controller daemonController) Status(ctx context.Context) (daemonResult, error) {
	state, found, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	result := daemonResult{Action: "status", Managed: found, State: publicDaemonState(state)}
	if location, locationErr := resolveDaemonLocation(controller.root); locationErr == nil {
		result.WBHome, result.RuntimeDir, result.SocketPath, result.StatePath =
			location.Home, location.RuntimeDir, location.SocketPath, location.StatePath
	}
	// The legacy endpoint is reported even when this home has no daemon: that
	// is precisely the case in which a leftover daemon looks healthy while
	// nothing here records one.
	if legacy := detectLegacyDaemon(controller.root, controller.deps.alive); legacy.present() {
		result.LegacyRuntime = &legacy
	}
	identity, detail, alive := controller.assessIdentity(state, found)
	result.Identity, result.IdentityDetail = identity, detail
	// Liveness and ownership are separate: a process can be alive and still not
	// be this home's daemon, and reachability must stay reportable for it.
	result.ProcessManagerRunning = alive && identity == identityCurrent
	result.Hub = controller.hubStatus(ctx, state.Listen)
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	result.ProvenanceMatches = state.Provenance.SameBinary(current)
	if !found {
		result.ReportedState = "absent"
		// A daemon answering on the configured endpoint while this home records
		// nothing is exactly the shape of both observed failures: something is
		// serving, and nothing here can claim it. Reachability stays its own
		// condition, reported next to identity=absent rather than as a daemon
		// this home owns.
		if healthErr := controller.deps.health(ctx, daemonDefaultListen); healthErr == nil {
			result.Reachable = true
			result.DirectTransportReachable = true
			result.ReachabilityTransport = "direct"
		} else {
			result.DirectTransportError = healthErr.Error()
			result.ReachabilityError = healthErr.Error()
		}
		return result, nil
	}
	result.ReportedState = string(state.Status)
	if !alive && (state.Status == daemon.StatusReady || state.Status == daemon.StatusDraining) && identity == identityStopped {
		// Only a record this home owns may be retired. A foreign or unrecorded
		// record is left exactly as it was found, because overwriting it would
		// destroy the evidence the operator needs.
		state.MarkStopped(controller.deps.now())
		if err := controller.store.Save(state); err != nil {
			return daemonResult{}, err
		}
		result.State = publicDaemonState(state)
	}
	if identity == identityCurrent && alive && result.ProvenanceMatches {
		result.ReadyVerified = true
	} else if state.Status == daemon.StatusReady || state.Status == daemon.StatusStarting || state.Status == daemon.StatusDraining {
		// The record claims a daemon is up, and this home cannot show that the
		// claim is about its own process and binary. That is reported as
		// unverified rather than repeated as ready: "something answers on my
		// port" is not ownership, and the two failures this feature exists for
		// both looked exactly like this.
		result.ReportedState = "unverified"
	}
	if alive {
		if healthErr := controller.deps.health(ctx, state.Listen); healthErr == nil {
			result.Reachable = true
			result.DirectTransportReachable = true
			result.ReachabilityTransport = "direct"
		} else {
			result.DirectTransportError = healthErr.Error()
			if daemonFileBridgeFallbackAllowed(healthErr) {
				bridgeHealth := controller.deps.bridgeHealth
				if bridgeHealth == nil {
					bridgeHealth = daemonFileBridgeHealthy
				}
				if bridgeErr := bridgeHealth(ctx, controller.root, fmt.Sprint(state.Queue.Generation)); bridgeErr == nil {
					result.Reachable = true
					result.ReachabilityTransport = "file_bridge"
				} else {
					result.ReachabilityError = fmt.Sprintf("protected file bridge: %v", bridgeErr)
				}
			} else {
				result.ReachabilityError = healthErr.Error()
			}
		}
	}
	return result, nil
}

// hubStatus reads the hub section the way serveDashboard will. A configuration
// error is reported as "not mounted" rather than failing status: the operator
// needs status most when serve is refusing to start.
func (controller daemonController) hubStatus(ctx context.Context, listen string) daemonHubStatus {
	path := wbconfig.DefaultPath()
	if controller.deps.hubConfigPath != nil {
		path = controller.deps.hubConfigPath()
	}
	cfg, found, err := hubconfig.Load(path)
	if err != nil || !found {
		return daemonHubStatus{}
	}
	status := daemonHubStatus{
		Mounted: true, Engine: cfg.Store.Engine, Store: cfg.Location(), Listen: listen,
		Polling: strings.TrimSpace(cfg.GitHub.TokenFile) != "", PollInterval: cfg.GitHub.PollInterval.String(),
		Webhook: cfg.GitHub.App != nil,
	}
	if cfg.GitHub.App != nil {
		status.WebhookPublicURL = cfg.GitHub.App.PublicURL
	}
	health := controller.deps.hubHealth
	if health == nil || listen == "" {
		return status
	}
	// A daemon that is not running has nothing to report beyond the
	// declaration, so an unreachable health endpoint is not an error here.
	live, err := health(ctx, listen)
	if err != nil {
		return status
	}
	status.RepositoriesPolled = live.RepositoriesPolled
	status.LastEventReceived = live.LastEventReceived
	status.LastEventAcknowledged = live.LastEventAcknowledged
	return status
}

// daemonHubHealth reads the hub block a running daemon publishes on its
// health endpoint.
func daemonHubHealth(ctx context.Context, listen string) (daemonHubStatus, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listen+"/api/v1/health", nil)
	if err != nil {
		return daemonHubStatus{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return daemonHubStatus{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return daemonHubStatus{}, fmt.Errorf("health endpoint returned %s", response.Status)
	}
	var payload struct {
		Hub *daemonHubStatus `json:"hub"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return daemonHubStatus{}, err
	}
	if payload.Hub == nil {
		return daemonHubStatus{}, errors.New("health endpoint reported no hub")
	}
	return *payload.Hub, nil
}

func (controller daemonController) Start(ctx context.Context, listen string) (daemonResult, error) {
	release, err := controller.lifecycleLock()
	if err != nil {
		return daemonResult{}, err
	}
	defer release()
	if err := requireLoopbackAddress(listen); err != nil {
		return daemonResult{}, usageError(err.Error())
	}
	// Refuse before binding when a daemon still serves the runtime directory
	// this build no longer writes to. Starting a second daemon there would put
	// two daemons on two homes, each invisible to the other's supervisor, and
	// the legacy socket is the only evidence that the first one is alive.
	if legacy := detectLegacyDaemon(controller.root, controller.deps.alive); legacy.present() {
		return daemonResult{Action: "start", LegacyRuntime: &legacy}, fmt.Errorf(
			"refusing to start a daemon while a daemon still serves the legacy runtime directory: %s; stop that daemon first (wb daemon stop, with the WB_HOME it was started under) rather than letting it keep serving an abandoned home",
			legacy.describe())
	}
	state, found, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	identity, identityDetail, alive := controller.assessIdentity(state, found)
	if identity == identityForeignHome || identity == identityForeignStatePath {
		return daemonResult{Action: "start", Managed: true, State: publicDaemonState(state), Identity: identity, IdentityDetail: identityDetail},
			fmt.Errorf("refusing to replace a lifecycle record that belongs to another WB home: %s", identityDetail)
	}
	if found && identity == identityCurrent && alive && state.Status == daemon.StatusReady && state.Listen == listen {
		if state.Provenance.SameBinary(current) {
			result := daemonResult{Action: "start", Managed: true, ProcessManagerRunning: true, ProvenanceMatches: true, State: publicDaemonState(state), AlreadyRunning: true, Identity: identity, IdentityDetail: identityDetail, ReadyVerified: true, ReportedState: string(state.Status)}
			if healthErr := controller.deps.health(ctx, state.Listen); healthErr == nil {
				result.Reachable = true
				result.DirectTransportReachable = true
				result.ReachabilityTransport = "direct"
			} else {
				result.ReachabilityError = healthErr.Error()
				result.DirectTransportError = healthErr.Error()
			}
			return result, nil
		}
		if _, err := controller.stop(ctx, state); err != nil {
			return daemonResult{}, fmt.Errorf("handoff daemon from %s to installed %s: %w", state.Provenance.Version, current.Version, err)
		}
		found = true
		state, _, err = controller.store.Load()
		if err != nil {
			return daemonResult{}, err
		}
	}
	return controller.launch(ctx, optionalDaemonState(state, found), listen, current, "start", found)
}

func optionalDaemonState(state daemon.State, found bool) *daemon.State {
	if !found {
		return nil
	}
	return &state
}

func (controller daemonController) Restart(ctx context.Context, ifRunning bool) (daemonResult, error) {
	return controller.RestartWithProgress(ctx, ifRunning, nil)
}

func (controller daemonController) RestartWithProgress(ctx context.Context, ifRunning bool, progress func(string)) (daemonResult, error) {
	release, err := controller.lifecycleLock()
	if err != nil {
		return daemonResult{}, err
	}
	defer release()
	state, found, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	if !found || state.Status == daemon.StatusStopped || state.PID == 0 || !controller.deps.alive(state.PID) {
		if ifRunning {
			return daemonResult{Action: "restart", Managed: found, State: publicDaemonState(state)}, nil
		}
		current, err := controller.provenance()
		if err != nil {
			return daemonResult{}, err
		}
		var result daemonResult
		controller.restartPhase(progress, "starting daemon", func() {
			result, err = controller.launch(ctx, optionalDaemonState(state, found), daemonListenOrDefault(state.Listen), current, "restart", found)
		})
		return result, err
	}
	var stopErr error
	controller.restartPhase(progress, fmt.Sprintf("draining daemon pid %d", state.PID), func() { _, stopErr = controller.stop(ctx, state) })
	if stopErr != nil {
		return daemonResult{}, stopErr
	}
	stopped, _, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	var result daemonResult
	controller.restartPhase(progress, "starting replacement daemon", func() {
		result, err = controller.launch(ctx, &stopped, daemonListenOrDefault(stopped.Listen), current, "restart", true)
	})
	return result, err
}

func (controller daemonController) restartPhase(progress func(string), phase string, operation func()) {
	if progress == nil {
		operation()
		return
	}
	progress(phase)
	done := make(chan struct{})
	go func() {
		operation()
		close(done)
	}()
	tickerFactory := controller.deps.restartTicker
	if tickerFactory == nil {
		tickerFactory = func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		}
	}
	ticks, stopTicker := tickerFactory(daemonRestartProgressInterval)
	defer stopTicker()
	for {
		select {
		case <-done:
			return
		case <-ticks:
			progress(phase + " (still waiting)")
		}
	}
}
func daemonListenOrDefault(listen string) string {
	if listen == "" {
		return daemonDefaultListen
	}
	return listen
}

func (controller daemonController) Stop(ctx context.Context) (daemonResult, error) {
	release, err := controller.lifecycleLock()
	if err != nil {
		return daemonResult{}, err
	}
	defer release()
	state, found, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	if !found || state.Status == daemon.StatusStopped || state.PID == 0 {
		return controller.withCurrentProvenance(daemonResult{Action: "stop", Managed: found, State: publicDaemonState(state)})
	}
	result, err := controller.stop(ctx, state)
	if err != nil {
		return daemonResult{}, err
	}
	return controller.withCurrentProvenance(result)
}

func (controller daemonController) withCurrentProvenance(result daemonResult) (daemonResult, error) {
	if !result.Managed {
		return result, nil
	}
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	result.ProvenanceMatches = result.State.Provenance.SameBinary(current)
	return result, nil
}

func (controller daemonController) stop(_ context.Context, state daemon.State) (daemonResult, error) {
	if state.PID > 0 && controller.deps.alive(state.PID) {
		observed, observedKnown := daemon.ProcessStartTime(state.PID)
		if match, known := state.ProcessGenerationMatches(observed, observedKnown); known && !match {
			// The PID is someone else's now. Signalling it would signal an
			// unrelated process, which is the harm the recorded start time
			// exists to prevent; the record is retired instead.
			reason := fmt.Sprintf("PID %d now belongs to a different process; not signalling it", state.PID)
			state.MarkStoppedWithReason(reason, controller.deps.now())
			if err := controller.store.Save(state); err != nil {
				return daemonResult{}, err
			}
			return daemonResult{Action: "stop", Managed: true, State: publicDaemonState(state), Identity: identityProcessRecycled, IdentityDetail: reason, ReportedState: string(daemon.StatusStopped)}, nil
		}
	}
	state.MarkDraining(controller.deps.now())
	if err := controller.store.Save(state); err != nil {
		return daemonResult{}, err
	}
	if !controller.deps.alive(state.PID) {
		state.MarkStopped(controller.deps.now())
		if err := controller.store.Save(state); err != nil {
			return daemonResult{}, err
		}
		return daemonResult{Action: "stop", Managed: true, State: publicDaemonState(state)}, nil
	}
	if err := controller.deps.stop(state.PID); err != nil {
		return daemonResult{}, fmt.Errorf("request daemon drain for pid %d: %w", state.PID, err)
	}
	deadline := controller.deps.now().Add(daemonStopTimeout)
	for controller.deps.now().Before(deadline) {
		if !controller.deps.alive(state.PID) {
			state.MarkStopped(controller.deps.now())
			if err := controller.store.Save(state); err != nil {
				return daemonResult{}, err
			}
			return daemonResult{Action: "stop", Managed: true, State: publicDaemonState(state)}, nil
		}
		controller.deps.sleep(50 * time.Millisecond)
	}
	return daemonResult{}, fmt.Errorf("daemon pid %d did not stop within %s; it remains draining to preserve durable queue ownership", state.PID, daemonStopTimeout)
}

func (controller daemonController) launch(ctx context.Context, previous *daemon.State, listen string, provenance daemon.Provenance, action string, handoff bool) (daemonResult, error) {
	token, err := controller.deps.token()
	if err != nil {
		return daemonResult{}, err
	}
	location, err := resolveDaemonLocation(controller.root)
	if err != nil {
		return daemonResult{}, err
	}
	starting := daemon.NewStartingAt(previous, listen, provenance, token, location.Home, location.StatePath, controller.deps.now())
	if err := controller.store.Save(starting); err != nil {
		return daemonResult{}, err
	}
	// The supervisor unit is written from these arguments, so they carry only
	// the inputs the operator chose. The daemon resolves its own runtime
	// directory at startup: a path resolved *here* would be baked into the unit
	// and outlive the home it was resolved from, which is exactly how a daemon
	// ended up serving a directory the rest of WB had already abandoned.
	args := []string{"--projects-root", controller.root, "daemon", "serve", "--listen", listen, "--managed-start"}
	logPath, logErr := daemonStartLogPath(controller.root)
	if logErr != nil {
		return daemonResult{}, logErr
	}
	pid, err := controller.deps.start(provenance.Executable, args, logPath)
	if err != nil {
		starting.MarkStopped(controller.deps.now())
		_ = controller.store.Save(starting)
		return daemonResult{}, err
	}
	releaseState, err := controller.stateLock()
	if err != nil {
		return daemonResult{}, err
	}
	state, found, err := controller.store.Load()
	if err != nil {
		releaseState()
		return daemonResult{}, err
	}
	if !found || state.Status != daemon.StatusStarting || state.OwnerToken != token {
		releaseState()
		return daemonResult{}, errors.New("daemon startup ownership changed before its process was recorded")
	}
	if state.PID != 0 && state.PID != pid {
		releaseState()
		return daemonResult{}, fmt.Errorf("daemon startup recorded unexpected process %d instead of %d", state.PID, pid)
	}
	state.MarkStartingPID(pid, controller.deps.now())
	if err := controller.store.Save(state); err != nil {
		releaseState()
		return daemonResult{}, err
	}
	releaseState()
	// The lifecycle controller is the only Starting -> Ready writer. The child
	// serves health while state remains Starting; this process still holds the
	// lifecycle lock, so recovery cannot race this compare-and-save transition.
	deadline := controller.deps.now().Add(daemonReadyTimeout)
	for controller.deps.now().Before(deadline) {
		if controller.ownedHealth(ctx, listen, pid, starting.Queue.Generation) != nil {
			controller.deps.sleep(50 * time.Millisecond)
			continue
		}
		releaseState, lockErr := controller.stateLock()
		if lockErr != nil {
			return daemonResult{}, lockErr
		}
		state, found, loadErr := controller.store.Load()
		if loadErr != nil {
			releaseState()
			return daemonResult{}, loadErr
		}
		if found && state.OwnerToken == token && state.Status == daemon.StatusStarting && state.PID == pid {
			state.MarkReadyWithProcess(pid, daemonProcessStartedAt(pid), controller.deps.now())
			if err := controller.store.Save(state); err != nil {
				releaseState()
				return daemonResult{}, err
			}
			releaseState()
			return daemonResult{Action: action, Managed: true, ProcessManagerRunning: true, Reachable: true, DirectTransportReachable: true, ReachabilityTransport: "direct", ProvenanceMatches: true, State: publicDaemonState(state), AutomaticVersionHandoff: handoff, Identity: identityCurrent, ReadyVerified: true, ReportedState: string(state.Status), WBHome: location.Home, RuntimeDir: location.RuntimeDir, SocketPath: location.SocketPath, StatePath: location.StatePath}, nil
		}
		releaseState()
		controller.deps.sleep(50 * time.Millisecond)
	}
	// A child that could not bind or that lost its runtime directory records
	// why before exiting, so the reason is reported here instead of being
	// reduced to "did not become ready".
	if state, found, loadErr := controller.store.Load(); loadErr == nil && found && state.StoppedReason != "" {
		return daemonResult{Action: action, Managed: true, State: publicDaemonState(state), ReportedState: string(state.Status)}, fmt.Errorf("daemon did not become ready within %s: %s", daemonReadyTimeout, state.StoppedReason)
	}
	return daemonResult{}, fmt.Errorf("daemon did not become ready within %s; inspect %s", daemonReadyTimeout, logPath)
}

// daemonProcessStartedAt observes the process generation WB is about to record.
// A platform that cannot answer records the zero time, which status reports as
// unknown rather than as a match.
func daemonProcessStartedAt(pid int) time.Time {
	started, ok := daemon.ProcessStartTime(pid)
	if !ok {
		return time.Time{}
	}
	return started
}

func serveDashboard(command *cobra.Command, deps daemonDependencies, address string, store daemon.Store, ownerToken string, quiet, managedStart bool) (serveErr error) {
	location, err := resolveDaemonLocation(projectsRoot)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp", address)
	if err != nil {
		// A held endpoint is a distinct, actionable condition, not a transient
		// error: another daemon (or an unrelated process) already owns it, and
		// starting a second detached daemon of our own would only race it.
		if daemonAddressInUse(err) {
			return fmt.Errorf("daemon endpoint %s is already held by another process: %w; stop it (or point this daemon at another --listen) instead of starting a second daemon", address, err)
		}
		return fmt.Errorf("listen for WB daemon on %s: %w", address, err)
	}
	provenance, err := newDaemonController(deps, projectsRoot).provenance()
	if err != nil {
		_ = listener.Close()
		return err
	}
	controller := newDaemonController(deps, projectsRoot)
	releaseState, err := controller.stateLock()
	if err != nil {
		_ = listener.Close()
		return err
	}
	state, found, err := store.Load()
	if err != nil {
		releaseState()
		_ = listener.Close()
		return err
	}
	if managedStart && (!found || state.Status != daemon.StatusStarting || state.OwnerToken != ownerToken) {
		releaseState()
		_ = listener.Close()
		return errors.New("managed daemon startup ownership was superseded")
	}
	if !managedStart {
		state = daemon.NewStartingAt(optionalDaemonState(state, found), address, provenance, ownerToken, location.Home, location.StatePath, deps.now())
		state.MarkReadyWithProcess(os.Getpid(), daemonProcessStartedAt(os.Getpid()), deps.now())
	} else {
		state.WBHome, state.StatePath = location.Home, location.StatePath
		state.MarkStartingPID(os.Getpid(), deps.now())
	}
	if err := store.Save(state); err != nil {
		releaseState()
		_ = listener.Close()
		return err
	}
	releaseState()
	localListener, err := listenDaemonLocal(projectsRoot)
	if err != nil {
		_ = listener.Close()
		return err
	}
	defer func() { _ = localListener.Close() }()
	rawExecutionPolicyPath, err := daemon.RawExecutionPolicyPath()
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("resolve daemon raw-execution policy: %w", err)
	}
	operationsDirectory, err := daemon.OperationsDir(projectsRoot)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("resolve daemon operation store: %w", err)
	}
	queue, err := daemon.NewService(projectsRoot, operationsDirectory, collectVersion().Version, fmt.Sprint(state.Queue.Generation), func() error {
		return daemon.RequireRawExecutionPolicy(rawExecutionPolicyPath, projectsRoot)
	})
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("load durable daemon queue: %w", err)
	}
	if managedStart {
		releaseState, err := controller.stateLock()
		if err != nil {
			_ = listener.Close()
			return err
		}
		current, ok, err := store.Load()
		if err != nil {
			releaseState()
			_ = listener.Close()
			return err
		}
		if !ok || current.Status != daemon.StatusStarting || current.OwnerToken != ownerToken || current.PID != os.Getpid() {
			releaseState()
			_ = listener.Close()
			return errors.New("daemon startup ownership was superseded before serving")
		}
		state = current
		releaseState()
	}
	defer func() {
		_ = controller.markStoppedIfOwned(ownerToken)
	}()
	hubConfigPath := wbconfig.DefaultPath
	if deps.hubConfigPath != nil {
		hubConfigPath = deps.hubConfigPath
	}
	// Narration goes to stderr, which is where the detached daemon's log file
	// already points, so there is no second writer to mirror into.
	narrator := narrate.Writer{Out: command.ErrOrStderr(), Quiet: quiet}
	mount, err := mountHub(command.Context(), hubConfigPath(), address, narrator, deps.hubTuning)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("mount the bench hub: %w", err)
	}
	defer func() { _ = mount.Close() }()
	// Best-effort: an unresolved log path only disables /api/v1/log (503),
	// it never blocks the daemon from serving everything else.
	// daemonStartLogPath is the cross-platform accessor (daemonLogPath is
	// !darwin-only; darwin's launchd unit owns a fixed, home-derived path).
	logPath, _ := daemonStartLogPath(projectsRoot)
	server := &http.Server{Handler: dashboard.NewHandler(dashboard.Options{
		ProjectsRoot: projectsRoot, Version: collectVersion().Version,
		DaemonPID: os.Getpid(), SchedulerGeneration: state.Queue.Generation,
		Mounts: mount.handlers(), Hub: mount.hubHealth(), LogPath: logPath,
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	rpcPath, rpcHandler := daemonv1connect.NewDaemonServiceHandler(queue)
	rpcMux := http.NewServeMux()
	rpcMux.Handle(rpcPath, authenticatedDaemonHandler(ownerToken, rpcHandler))
	fileBridge, err := newDaemonFileBridgeServer(projectsRoot, ownerToken, fmt.Sprint(state.Queue.Generation), rpcMux)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("prepare daemon file bridge: %w", err)
	}
	rpcServer := &http.Server{Handler: rpcMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signalDaemonContext(command.Context())
	defer stop()
	queue.StartLeaseRecovery(ctx)
	if err := startRepositoryEventReceiver(ctx, projectsRoot, hubConfigPath(), command.ErrOrStderr()); err != nil {
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
	if _, err := fmt.Fprintf(command.OutOrStdout(), "WB dashboard: http://%s\n", listener.Addr()); err != nil {
		_ = listener.Close()
		return err
	}
	if line := mount.StartLine(); line != "" {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), line)
	}
	errorsCh := make(chan error, 4)
	go func() { errorsCh <- server.Serve(listener) }()
	go func() { errorsCh <- rpcServer.Serve(localListener) }()
	go func() { errorsCh <- fileBridge.Serve(ctx) }()
	go func() {
		if err := daemonRuntimeGuard(command.ErrOrStderr(), ctx, address, store, state, ownerToken); err != nil {
			errorsCh <- err
		}
	}()
	err = <-errorsCh
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
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
func daemonRuntimeGuard(out io.Writer, ctx context.Context, address string, store daemon.Store, owned daemon.State, ownerToken string) error {
	ticker := time.NewTicker(daemonHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			if err := daemonRuntimeIntact(store); err != nil {
				reason := err.Error()
				_, _ = fmt.Fprintf(out, "daemon stopping: %s\n", reason)
				stopped := owned
				if current, found, loadErr := store.Load(); loadErr == nil && found {
					stopped = current
				}
				if stopped.PID == os.Getpid() && (ownerToken == "" || stopped.OwnerToken == ownerToken) {
					stopped.MarkStoppedWithReason(reason, time.Now().UTC())
					_ = store.Save(stopped)
				}
				return err
			}
			_, _ = fmt.Fprintf(out, "daemon heartbeat: ready %s\n", address)
		}
	}
}

// daemonRuntimeIntact proves the daemon is still writing where it says it is:
// the directory holding its lifecycle record must still be a real directory,
// and the record itself must still be there. The check follows the record
// rather than the resolved home so an explicitly pinned state path is covered
// by the same proof.
func daemonRuntimeIntact(store daemon.Store) error {
	directory := filepath.Dir(store.Path)
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("daemon runtime directory %s is gone (%v); this daemon no longer belongs to a WB home", directory, err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("daemon runtime directory %s is no longer a real directory", directory)
	}
	if _, err := os.Lstat(store.Path); err != nil {
		return fmt.Errorf("daemon lifecycle state %s is gone (%v)", store.Path, err)
	}
	return nil
}

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

func daemonOwnerToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
func daemonHealthy(ctx context.Context, listen string) error {
	requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listen+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	return nil
}

func daemonOwnedHealthy(ctx context.Context, listen string, pid int, generation uint64) error {
	requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listen+"/api/v1/health", nil)
	if err != nil {
		return err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned %s", response.Status)
	}
	var payload struct {
		PID        int    `json:"daemon_pid"`
		Generation uint64 `json:"scheduler_generation"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return err
	}
	if payload.PID != pid || payload.Generation != generation {
		return fmt.Errorf("health endpoint belongs to pid %d generation %d, want pid %d generation %d", payload.PID, payload.Generation, pid, generation)
	}
	return nil
}
func requireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid --listen address %q: %w", address, err)
	}
	if host == "localhost" {
		return nil
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("--listen must use localhost or a loopback IP; publish it through an authenticated tunnel")
	}
	return nil
}
