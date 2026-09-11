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
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/dashboard"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/remotestate"
	"github.com/sneat-dev/wb/internal/remotestate/hub"
	"github.com/sneat-dev/wb/internal/repositoryevents"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const (
	daemonDefaultListen           = "127.0.0.1:8766"
	daemonReadyTimeout            = 5 * time.Second
	daemonStopTimeout             = 5 * time.Second
	daemonHeartbeatInterval       = 10 * time.Second
	daemonRestartProgressInterval = 8 * time.Second
)

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
	Polling               bool                  `json:"polling"`
	PollInterval          string                `json:"poll_interval,omitempty"`
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
	bridgeHealth  func(context.Context, string, string) error
	restartTicker func(time.Duration) (<-chan time.Time, func())
	rawPolicy     func(string) (bool, string, error)
	localClient   func(string, string) (*http.Client, error)
	hubConfigPath func() string
	hubHealth     func(context.Context, string) (daemonHubStatus, error)
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
	command.AddCommand(newDaemonServeCmd(deps), newDaemonStartCmd(deps), newDaemonStatusCmd(deps), newDaemonStopCmd(deps), newDaemonRestartCmd(deps), newDaemonOperationCmd(deps))
	return command
}

func newDaemonServeCmd(deps daemonDependencies) *cobra.Command {
	var listenAddress, stateFile string
	var quiet bool
	command := &cobra.Command{
		Use: "serve", Short: "Serve the read-only dashboard and API on a loopback address",
		Long: `Serve WB's embedded operations dashboard and versioned read-only API.

The listener is loopback-only. Publish it to registered machines through a
protected Cloudflare Tunnel or another authenticated reverse proxy; do not bind
the daemon directly to a public interface. Mutating operation RPCs are served
only through the separately authenticated local transport.

When ~/.config/wb/wb.yaml has a hub: section, the same listener also serves the
bench hub API under /v0/workbench/ and the embedded bench dashboard under
/bench/, on the DALgo store engine that section names. Without a hub: section
nothing changes, and the dashboard needs no sign-in because only this machine
can reach the loopback address it is bound to.

With hub.github.token_file set, a poller reads every repository this machine
publishes and narrates one line on stderr for each webhook delivery and each
poll observation: the event, the repository, and what the hub did with it.
--quiet silences those console lines. wb daemon start never passes it, so a
detached daemon's log file keeps every line.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireLoopbackAddress(listenAddress); err != nil {
				return usageError(err.Error())
			}
			if stateFile == "" {
				stateFile = daemonStatePath(projectsRoot)
			}
			state, found, err := (daemon.Store{Path: stateFile}).Load()
			if err != nil {
				return err
			}
			ownerToken := ""
			if found && state.Status == daemon.StatusStarting {
				ownerToken = state.OwnerToken
			} else {
				ownerToken, err = deps.token()
				if err != nil {
					return err
				}
			}
			return serveDashboard(command, deps, listenAddress, daemon.Store{Path: stateFile}, ownerToken, quiet)
		},
	}
	command.Flags().StringVar(&listenAddress, "listen", daemonDefaultListen, "loopback listen address")
	command.Flags().StringVar(&stateFile, "lifecycle-state", "", "private lifecycle state path (used by daemon start)")
	_ = command.Flags().MarkHidden("lifecycle-state")
	command.Flags().BoolVar(&quiet, "quiet", false, "silence the hub's per-event console lines (the daemon log file still records them)")
	return command
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
	command := &cobra.Command{Use: "status", Short: "Report local daemon reachability and exact executable provenance", Args: cobra.NoArgs,
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
	_, err := fmt.Fprintf(out, "daemon %s: state=%s, process_manager_running=%t, api_reachable=%t, direct_transport_reachable=%t, installed_provenance=%t", result.Action, status, result.ProcessManagerRunning, result.Reachable, result.DirectTransportReachable, result.ProvenanceMatches)
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
		_, err = fmt.Fprintf(out, ", hub_polling=%t, hub_poll_interval=%s, hub_repositories_polled=%d", result.Hub.Polling, result.Hub.PollInterval, result.Hub.RepositoriesPolled)
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
	return daemonController{deps: deps, store: daemon.Store{Path: daemonStatePath(root)}, root: root}
}
func daemonStatePath(root string) string {
	return filepath.Join(root, ".wb", "runtime", "daemon-state.json")
}
func daemonLogPath(root string) string { return filepath.Join(root, ".wb", "runtime", "daemon.log") }

// lifecycleLock serializes short control-plane transitions. A daemon process
// never holds it, so health/status remain available while a replacement drains.
// If a machine dies while the lock exists we refuse rather than guessing which
// process owns the transition; the durable state record remains the evidence
// for the forthcoming repair command.
func (controller daemonController) lifecycleLock() (func(), error) {
	path := filepath.Join(controller.root, ".wb", "runtime", "daemon.lifecycle.lock")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if errors.Is(err, os.ErrExist) {
		return nil, fmt.Errorf("a daemon lifecycle transition is already in progress; run `wb daemon status` and retry after it reaches a terminal state")
	}
	if err != nil {
		return nil, err
	}
	_, _ = fmt.Fprintf(file, "pid=%d\n", os.Getpid())
	return func() { _ = file.Close(); _ = os.Remove(path) }, nil
}

func (controller daemonController) provenance() (daemon.Provenance, error) {
	executable, err := controller.deps.executable()
	if err != nil {
		return daemon.Provenance{}, err
	}
	version := controller.deps.version()
	return daemon.ProvenanceForExecutable(executable, version.Version, version.Revision, version.Built)
}

func (controller daemonController) Status(ctx context.Context) (daemonResult, error) {
	state, found, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	result := daemonResult{Action: "status", Managed: found, State: publicDaemonState(state)}
	result.Hub = controller.hubStatus(ctx, state.Listen)
	if !found {
		return result, nil
	}
	alive := state.PID > 0 && controller.deps.alive(state.PID)
	result.ProcessManagerRunning = alive
	if !alive && (state.Status == daemon.StatusReady || state.Status == daemon.StatusDraining) {
		state.MarkStopped(controller.deps.now())
		if err := controller.store.Save(state); err != nil {
			return daemonResult{}, err
		}
		result.State = publicDaemonState(state)
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
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	result.ProvenanceMatches = state.Provenance.SameBinary(current)
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
	state, found, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	if found && state.Status == daemon.StatusReady && state.PID > 0 && controller.deps.alive(state.PID) && state.Listen == listen {
		if state.Provenance.SameBinary(current) {
			result := daemonResult{Action: "start", Managed: true, ProcessManagerRunning: true, ProvenanceMatches: true, State: publicDaemonState(state), AlreadyRunning: true}
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
	starting := daemon.NewStarting(previous, listen, provenance, token, controller.deps.now())
	if err := controller.store.Save(starting); err != nil {
		return daemonResult{}, err
	}
	args := []string{"--projects-root", controller.root, "daemon", "serve", "--listen", listen, "--lifecycle-state", controller.store.Path}
	pid, err := controller.deps.start(provenance.Executable, args, daemonLogPath(controller.root))
	if err != nil {
		starting.MarkStopped(controller.deps.now())
		_ = controller.store.Save(starting)
		return daemonResult{}, err
	}
	// The spawned child owns the Starting -> Ready transition. Do not save the
	// parent's stale Starting value here: the child may have become ready before
	// start returned, and overwriting that state would make this wait time out.
	deadline := controller.deps.now().Add(daemonReadyTimeout)
	for controller.deps.now().Before(deadline) {
		state, found, loadErr := controller.store.Load()
		if loadErr != nil {
			return daemonResult{}, loadErr
		}
		if found && state.OwnerToken == token && state.Status == daemon.StatusReady && state.PID == pid && controller.deps.health(ctx, listen) == nil {
			return daemonResult{Action: action, Managed: true, ProcessManagerRunning: true, Reachable: true, DirectTransportReachable: true, ReachabilityTransport: "direct", ProvenanceMatches: true, State: publicDaemonState(state), AutomaticVersionHandoff: handoff}, nil
		}
		controller.deps.sleep(50 * time.Millisecond)
	}
	return daemonResult{}, fmt.Errorf("daemon did not become ready within %s; inspect %s", daemonReadyTimeout, daemonLogPath(controller.root))
}

func serveDashboard(command *cobra.Command, deps daemonDependencies, address string, store daemon.Store, ownerToken string, quiet bool) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return fmt.Errorf("listen for WB daemon: %w", err)
	}
	provenance, err := newDaemonController(deps, projectsRoot).provenance()
	if err != nil {
		_ = listener.Close()
		return err
	}
	state, found, err := store.Load()
	if err != nil {
		_ = listener.Close()
		return err
	}
	if !found || state.OwnerToken != ownerToken {
		state = daemon.NewStarting(optionalDaemonState(state, found), address, provenance, ownerToken, deps.now())
	}
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
	queue, err := daemon.NewService(projectsRoot, collectVersion().Version, fmt.Sprint(state.Queue.Generation), func() error {
		return daemon.RequireRawExecutionPolicy(rawExecutionPolicyPath, projectsRoot)
	})
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("load durable daemon queue: %w", err)
	}
	state.Listen, state.Provenance = address, provenance
	state.MarkReady(os.Getpid(), deps.now())
	if err := store.Save(state); err != nil {
		_ = listener.Close()
		return err
	}
	defer func() {
		current, ok, loadErr := store.Load()
		if loadErr == nil && ok && current.OwnerToken == ownerToken {
			current.MarkStopped(deps.now())
			_ = store.Save(current)
		}
	}()
	hubConfigPath := wbconfig.DefaultPath
	if deps.hubConfigPath != nil {
		hubConfigPath = deps.hubConfigPath
	}
	// Narration goes to stderr, which is where the detached daemon's log file
	// already points, so there is no second writer to mirror into.
	narrator := narrate.Writer{Out: command.ErrOrStderr(), Quiet: quiet}
	mount, err := mountHub(command.Context(), hubConfigPath(), address, narrator)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("mount the bench hub: %w", err)
	}
	defer func() { _ = mount.Close() }()
	server := &http.Server{Handler: dashboard.NewHandler(dashboard.Options{ProjectsRoot: projectsRoot, Version: collectVersion().Version, Mounts: mount.handlers(), Hub: mount.hubHealth()}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
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
	if err := startRepositoryEventReceiver(ctx, projectsRoot, command.ErrOrStderr()); err != nil {
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
	go daemonHeartbeat(command.ErrOrStderr(), ctx, address)
	if _, err := fmt.Fprintf(command.OutOrStdout(), "WB dashboard: http://%s\n", listener.Addr()); err != nil {
		_ = listener.Close()
		return err
	}
	if line := mount.StartLine(); line != "" {
		_, _ = fmt.Fprintln(command.ErrOrStderr(), line)
	}
	errorsCh := make(chan error, 3)
	go func() { errorsCh <- server.Serve(listener) }()
	go func() { errorsCh <- rpcServer.Serve(localListener) }()
	go func() { errorsCh <- fileBridge.Serve(ctx) }()
	err = <-errorsCh
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func startRepositoryEventReceiver(ctx context.Context, projectsRoot string, out io.Writer) error {
	eventQueue, err := repositoryevents.NewQueue(projectsRoot)
	if err != nil {
		return err
	}
	progress := func(message string) { _, _ = fmt.Fprintln(out, message) }
	go eventQueue.Run(ctx, repositoryevents.SyncProcessor{ProjectsRoot: projectsRoot}, progress)

	config, err := remotestate.LoadConfig(wbconfig.DefaultPath())
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
	cursorPath := filepath.Join(projectsRoot, ".wb", "runtime", "daemon", "repository-events", "cursor.json")
	receiver := repositoryevents.Receiver{Source: provider, Queue: eventQueue, Cursor: repositoryevents.CursorStore{Path: cursorPath}, Progress: progress}
	go receiver.Run(ctx)
	return nil
}

func daemonHeartbeat(out io.Writer, ctx context.Context, address string) {
	ticker := time.NewTicker(daemonHeartbeatInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_, _ = fmt.Fprintf(out, "daemon heartbeat: ready %s\n", address)
		}
	}
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
