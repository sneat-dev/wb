package daemonhost

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

var errCleanDaemonShutdown = errors.New("daemon: clean shutdown")

func (host *Host) Serve(ctx context.Context, r Request, out, errOut io.Writer) error {
	deps := host.runtimeDependencies()
	pinnedStatePath := r.LifecycleState
	managedStart := r.ManagedStart || pinnedStatePath != ""
	stateFile := pinnedStatePath
	if stateFile == "" {
		resolved, err := daemonruntime.StatePath(r.ProjectsRoot)
		if err != nil {
			return err
		}
		stateFile = resolved
	}
	reportPinnedLifecycleState(errOut, pinnedStatePath, stateFile)
	state, found, err := host.effects.loadState(daemon.Store{Path: stateFile})
	if err != nil {
		return err
	}
	if managedStart && (!found || state.Status != daemon.StatusStarting) {
		return errors.New("managed daemon startup no longer owns a starting lifecycle state")
	}
	ownerToken := ""
	if found && state.Status == daemon.StatusStarting {
		ownerToken = state.OwnerToken
	} else {
		ownerToken, err = deps.Token()
		if err != nil {
			return err
		}
	}
	return host.servePrepared(r.ProjectsRoot, ctx, out, errOut, deps, r.Listen, daemon.Store{Path: stateFile}, ownerToken, r.Quiet, managedStart)
}

func reportPinnedLifecycleState(out io.Writer, pinned, resolved string) {
	if strings.TrimSpace(pinned) == "" {
		return
	}
	_, _ = fmt.Fprintf(out, "daemon lifecycle state is pinned to %s by an explicit --lifecycle-state; an unpinned daemon would resolve %s\n", pinned, resolved)
}

func (host *Host) servePrepared(projectsRoot string, ctx context.Context, out, errOut io.Writer, deps daemonDependencies, address string, store daemon.Store, ownerToken string, quiet, managedStart bool) (serveErr error) {
	location, err := daemonruntime.ResolveLocation(projectsRoot)
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
	if err := requireLoopbackBound(listener.Addr(), deps.UsageError); err != nil {
		return err
	}
	provenance, err := daemonruntime.NewController(deps.Dependencies, projectsRoot).Provenance()
	if err != nil {
		return err
	}
	controller := daemonruntime.NewController(deps.Dependencies, projectsRoot)
	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		return err
	}
	state, found, err := host.effects.loadState(store)
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
	if err := host.effects.saveState(store, state); err != nil {
		releaseState()
		return err
	}
	releaseState()
	localListener, err := daemonruntime.ListenLocal(projectsRoot)
	if err != nil {
		return err
	}
	defer func() { _ = localListener.Close() }()
	rawExecutionPolicyPath, err := host.effects.rawPolicyPath()
	if err != nil {
		return fmt.Errorf("resolve daemon raw-execution policy: %w", err)
	}
	operationsDirectory, err := host.effects.operationsDir(projectsRoot)
	if err != nil {
		return fmt.Errorf("resolve daemon operation store: %w", err)
	}
	queue, err := daemon.NewService(projectsRoot, operationsDirectory, deps.Version().Version, fmt.Sprint(state.Queue.Generation), func() error {
		return daemon.RequireRawExecutionPolicy(rawExecutionPolicyPath, projectsRoot)
	})
	if err != nil {
		return fmt.Errorf("load durable daemon queue: %w", err)
	}
	if managedStart {
		releaseState, err := controller.AcquireStateLock()
		if err != nil {
			return err
		}
		current, ok, err := host.effects.loadState(store)
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
	narrator := narrate.Writer{Out: errOut, Quiet: quiet}
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
		_, _ = fmt.Fprintln(errOut, "wb: node identity unavailable:", err)
	}
	// A malformed cockpit: section is an operator mistake worth refusing to
	// start over, not a surface to silently run with defaults.
	cockpitConfig, err := wbconfig.LoadCockpit(hubConfigPath())
	if err != nil {
		return fmt.Errorf("load the cockpit configuration: %w", err)
	}
	mount, err := mountHub(ctx, hubConfigPath(), address, narrator, deps.hubTuning)
	if err != nil {
		return fmt.Errorf("mount the bench hub: %w", err)
	}
	defer func() { _ = mount.Close() }()
	// Best-effort: an unresolved log path only disables /api/v1/log (503, which
	// only the owner is told), it never blocks the daemon from serving
	// everything else.
	// daemonStartLogPath is the cross-platform accessor (daemonLogPath is
	// !darwin-only; darwin's launchd unit owns a fixed, home-derived path).
	logPath, _ := daemonruntime.DaemonStartLogPath(projectsRoot)
	// The peers read API is mounted unconditionally — with or without a hub
	// section — per peer-connectivity#req:peers-api's "mounted...on every
	// node": a laptop-only install answers "no downstream peers" instead of
	// falling through to the dashboard's HTML index (the bug a laptop-side
	// `wb peers list`/`get` hit before this fallback existed).
	peersSource := mount.peersSource()
	if peersSource == nil {
		peersSource = emptyPeersSource{}
	}
	peersHandler := peers.NewHandler("/api/v1/peers", peersSource, peersViewerAuthorize())
	cockpitServer := newCockpitServer(address, cockpitConfig)
	fleetOptions := host.deps.FleetOptions(projectsRoot, location.Home, hubConfigPath(), cockpitConfig, errOut, os.Hostname)
	fleetOptions.Remotes = withoutOwnAddress(fleetOptions.Remotes, address, fleetOptions.Logf)
	fleetSnapshotter := registerCockpitFleet(cockpitServer, fleetOptions)
	mount.serveExportOf(fleetSnapshotter, cockpitConfig)
	// A hub write is the owner's alone: the same session check the log uses.
	mount.authorizeOwnerWith(cockpitServer.IsOwner)
	server := &http.Server{Handler: dashboard.NewHandler(dashboard.Options{
		Home: cockpit.PagePrefix, Version: deps.Version().Version,
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
	fileBridge, err := daemonruntime.NewFileBridgeServer(projectsRoot, ownerToken, fmt.Sprint(state.Queue.Generation), rpcMux)
	if err != nil {
		return fmt.Errorf("prepare daemon file bridge: %w", err)
	}
	rpcServer := &http.Server{Handler: rpcMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := daemonruntime.SignalContext(ctx)
	defer stop()
	// The snapshotter lives as long as the daemon and is stopped, and waited
	// for, on the way out so no refresh outlives the daemon's state.
	defer fleetSnapshotter.Start(ctx)()
	queue.StartLeaseRecovery(ctx)
	if err := startRepositoryEventReceiver(ctx, projectsRoot, hubConfigPath(), errOut); err != nil {
		_, _ = fmt.Fprintln(errOut, "repository event receiver disabled:", err)
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
	if _, err := fmt.Fprintf(out, "WB dashboard: http://%s\n", listener.Addr()); err != nil {
		return err
	}
	if line := mount.StartLine(); line != "" {
		_, _ = fmt.Fprintln(errOut, line)
	}
	errorsCh := make(chan error, 4)
	go func() { errorsCh <- classifyServeResult(server.Serve(listener)) }()
	go func() { errorsCh <- classifyServeResult(rpcServer.Serve(localListener)) }()
	go func() { errorsCh <- classifyServeResult(fileBridge.Serve(ctx)) }()
	go func() {
		if err := daemonruntime.RuntimeGuard(errOut, ctx, address, store, state, ownerToken, deps.GuardTicker); err != nil {
			errorsCh <- err
		}
	}()
	return awaitDaemonServeResult(<-errorsCh)
}

func classifyServeResult(err error) error {
	if err == nil || errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return errCleanDaemonShutdown
	}
	return err
}

func awaitDaemonServeResult(err error) error {
	if errors.Is(err, errCleanDaemonShutdown) {
		return nil
	}
	return err
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

func requireLoopbackBound(bound net.Addr, usageError func(string) error) error {
	tcp, ok := bound.(*net.TCPAddr)
	if !ok || !tcp.IP.IsLoopback() {
		return usageError(fmt.Sprintf("the daemon is bound to %s, which is not a loopback address; it was not started, publish it through an authenticated tunnel instead", bound))
	}
	return nil
}
