package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/dashboard"
)

const (
	daemonDefaultListen     = "127.0.0.1:8766"
	daemonReadyTimeout      = 5 * time.Second
	daemonStopTimeout       = 5 * time.Second
	daemonHeartbeatInterval = 10 * time.Second
)

type daemonResult struct {
	Action                  string            `json:"action"`
	Managed                 bool              `json:"managed"`
	Reachable               bool              `json:"reachable"`
	ProvenanceMatches       bool              `json:"provenance_matches_installed"`
	State                   daemonPublicState `json:"state,omitempty"`
	AlreadyRunning          bool              `json:"already_running,omitempty"`
	AutomaticVersionHandoff bool              `json:"automatic_version_handoff,omitempty"`
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
	now        func() time.Time
	executable func() (string, error)
	start      func(string, []string, string) (int, error)
	alive      func(int) bool
	stop       func(int) error
	sleep      func(time.Duration)
	version    func() versionInfo
	token      func() (string, error)
	health     func(context.Context, string) bool
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{
		now:        func() time.Time { return time.Now().UTC() },
		executable: os.Executable,
		start:      startDaemonProcess,
		alive:      daemonProcessAlive,
		stop:       stopDaemonProcess,
		sleep:      time.Sleep,
		version:    collectVersion,
		token:      daemonOwnerToken,
		health:     daemonHealthy,
	}
}

func newDaemonCmd() *cobra.Command { return newDaemonCmdWithDependencies(defaultDaemonDependencies()) }

func newDaemonCmdWithDependencies(deps daemonDependencies) *cobra.Command {
	command := &cobra.Command{Use: "daemon", Short: "Operate WB's local loopback dashboard and scheduler lifecycle"}
	command.AddCommand(newDaemonServeCmd(deps), newDaemonStartCmd(deps), newDaemonStatusCmd(deps), newDaemonStopCmd(deps), newDaemonRestartCmd(deps))
	return command
}

func newDaemonServeCmd(deps daemonDependencies) *cobra.Command {
	var listenAddress, stateFile, ownerToken string
	command := &cobra.Command{
		Use: "serve", Short: "Serve the read-only dashboard and API on a loopback address",
		Long: `Serve WB's embedded operations dashboard and versioned read-only API.

The listener is loopback-only. Publish it to registered machines through a
protected Cloudflare Tunnel or another authenticated reverse proxy; do not bind
the daemon directly to a public interface. The HTTP dashboard is the lifecycle
MVP transport; future ConnectRPC/gRPC and MCP adapters consume the same durable
queue lifecycle contract rather than this state file.`,
		Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			if err := requireLoopbackAddress(listenAddress); err != nil {
				return usageError(err.Error())
			}
			if stateFile == "" {
				stateFile = daemonStatePath(projectsRoot)
			}
			if ownerToken == "" {
				var err error
				ownerToken, err = deps.token()
				if err != nil {
					return err
				}
			}
			return serveDashboard(command, deps, listenAddress, daemon.Store{Path: stateFile}, ownerToken)
		},
	}
	command.Flags().StringVar(&listenAddress, "listen", daemonDefaultListen, "loopback listen address")
	command.Flags().StringVar(&stateFile, "lifecycle-state", "", "private lifecycle state path (used by daemon start)")
	command.Flags().StringVar(&ownerToken, "owner-token", "", "private lifecycle owner token (used by daemon start)")
	_ = command.Flags().MarkHidden("lifecycle-state")
	_ = command.Flags().MarkHidden("owner-token")
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
			result, err := newDaemonController(deps, projectsRoot).Restart(command.Context(), ifRunning)
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
	status := "stopped"
	if result.Managed {
		status = string(result.State.Status)
	}
	_, err := fmt.Fprintf(out, "daemon %s: %s (reachable=%t, installed_provenance=%t)\n", result.Action, status, result.Reachable, result.ProvenanceMatches)
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
	if !found {
		return result, nil
	}
	alive := state.PID > 0 && controller.deps.alive(state.PID)
	if !alive && (state.Status == daemon.StatusReady || state.Status == daemon.StatusDraining) {
		state.MarkStopped(controller.deps.now())
		if err := controller.store.Save(state); err != nil {
			return daemonResult{}, err
		}
		result.State = publicDaemonState(state)
	}
	result.Reachable = alive && controller.deps.health(ctx, state.Listen)
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	result.ProvenanceMatches = state.Provenance.SameBinary(current)
	return result, nil
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
	if found && state.Status == daemon.StatusReady && state.PID > 0 && controller.deps.alive(state.PID) && controller.deps.health(ctx, state.Listen) && state.Listen == listen {
		if state.Provenance.SameBinary(current) {
			return daemonResult{Action: "start", Managed: true, Reachable: true, ProvenanceMatches: true, State: publicDaemonState(state), AlreadyRunning: true}, nil
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
		return controller.launch(ctx, optionalDaemonState(state, found), daemonListenOrDefault(state.Listen), current, "restart", found)
	}
	if _, err := controller.stop(ctx, state); err != nil {
		return daemonResult{}, err
	}
	stopped, _, err := controller.store.Load()
	if err != nil {
		return daemonResult{}, err
	}
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	return controller.launch(ctx, &stopped, daemonListenOrDefault(stopped.Listen), current, "restart", true)
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
	args := []string{"--projects-root", controller.root, "daemon", "serve", "--listen", listen, "--lifecycle-state", controller.store.Path, "--owner-token", token}
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
		if found && state.OwnerToken == token && state.Status == daemon.StatusReady && state.PID == pid && controller.deps.health(ctx, listen) {
			return daemonResult{Action: action, Managed: true, Reachable: true, ProvenanceMatches: true, State: publicDaemonState(state), AutomaticVersionHandoff: handoff}, nil
		}
		controller.deps.sleep(50 * time.Millisecond)
	}
	return daemonResult{}, fmt.Errorf("daemon did not become ready within %s; inspect %s", daemonReadyTimeout, daemonLogPath(controller.root))
}

func serveDashboard(command *cobra.Command, deps daemonDependencies, address string, store daemon.Store, ownerToken string) error {
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
	server := &http.Server{Handler: dashboard.NewHandler(dashboard.Options{ProjectsRoot: projectsRoot, Version: collectVersion().Version}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signalDaemonContext(command.Context())
	defer stop()
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	go daemonHeartbeat(command.ErrOrStderr(), ctx, address)
	if _, err := fmt.Fprintf(command.OutOrStdout(), "WB dashboard: http://%s\n", listener.Addr()); err != nil {
		_ = listener.Close()
		return err
	}
	err = server.Serve(listener)
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
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
func daemonHealthy(ctx context.Context, listen string) bool {
	requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listen+"/api/v1/health", nil)
	if err != nil {
		return false
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return false
	}
	defer func() { _ = response.Body.Close() }()
	return response.StatusCode == http.StatusOK
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
