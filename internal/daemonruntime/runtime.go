package daemonruntime

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/strongo/cli-helpers/daemonlifecycle"

	"github.com/sneat-dev/wb/internal/buildinfo"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/loopbackhost"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbconfig"
)

const (
	DefaultListen                 = "127.0.0.1:8766"
	daemonReadyTimeout            = 5 * time.Second
	daemonStopTimeout             = 5 * time.Second
	daemonRestartProgressInterval = 8 * time.Second
)

const LaunchdLabel = "dev.sneat.wb.daemon"

func daemonRefuseTestBinary(executable string) error {
	return daemonRefuseTestBinaryForMode(executable, testing.Testing())
}

// The production wrapper always supplies the observed process mode. The suffix
// guard remains independent of it, including in producer-local effect tests.
func daemonRefuseTestBinaryForMode(executable string, isTestProcess bool) error {
	if isTestProcess || strings.HasSuffix(filepath.Base(executable), ".test") {
		return fmt.Errorf("refusing to install a supervisor job for, or launch, a Go test binary as the WB daemon (%s); this guard exists because a test previously overwrote a real launchd job and took a real daemon down", executable)
	}
	return nil
}

func (native nativeOperations) daemonSupervisorPresent(supervisor daemon.Supervisor, label string) (present bool, unitName string) {
	switch supervisor {
	case daemon.SupervisorSystemd:
		output, _ := native.runSystemctl("--user", "is-system-running")
		return systemRunningState(strings.TrimSpace(string(output))), ""
	case daemon.SupervisorLaunchd:
		if strings.TrimSpace(label) == "" {
			return false, ""
		}
		target := fmt.Sprintf("gui/%d/%s", os.Getuid(), label)
		// Not routed through darwin's runLaunchctl seam: this file builds on
		// every platform, and runLaunchctl only exists under
		// daemon_process_darwin.go's darwin build tag. A raw exec.Command
		// here fails harmlessly (launchctl does not exist) on linux/windows,
		// exactly as it always has. Bounded the same way runSystemctl is
		// (context timeout plus WaitDelay): this existence check must not
		// hang indefinitely on a wedged launchctl either (sneat-dev/wb#622
		// review round 4).
		ctx, cancel := context.WithTimeout(context.Background(), native.commandBounds().Systemctl)
		defer cancel()
		command := exec.CommandContext(ctx, "launchctl", "print", target) //nolint:gosec // fixed argv plus a recorded launchd label, no shell.
		command.WaitDelay = 2 * time.Second
		if _, err := command.CombinedOutput(); err != nil {
			return false, label
		}
		return true, label
	default:
		return false, ""
	}
}

const daemonDefaultSystemdUnit = "wb-daemon.service"

func DaemonSystemdUnitName(getenv func(string) string) string {
	if getenv != nil {
		if configured := strings.TrimSpace(getenv("WB_DAEMON_SYSTEMD_UNIT")); configured != "" {
			if !strings.HasSuffix(configured, ".service") {
				configured += ".service"
			}
			return configured
		}
	}
	return daemonDefaultSystemdUnit
}

func (native nativeOperations) daemonObservedSystemdUnitState(unit string) (daemon.SystemdUnitState, bool) {
	trimmedUnit := strings.TrimSpace(unit)
	if trimmedUnit == "" {
		return daemon.SystemdUnitState{}, false
	}
	output, err := native.runSystemctl("--user", "show", "-p", "ActiveState,Result,NRestarts", trimmedUnit)
	if err != nil {
		return daemon.SystemdUnitState{}, false
	}
	return daemon.ParseSystemctlShow(string(output))
}

func daemonChildEnvironment() []string {
	environ := os.Environ()
	filtered := make([]string, 0, len(environ))
	for _, entry := range environ {
		name, _, found := strings.Cut(entry, "=")
		if found && supervisorEnvironmentVariable(name) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

const daemonHeartbeatInterval = 10 * time.Second

type Result struct {
	Action                   string      `json:"action"`
	Managed                  bool        `json:"managed"`
	ProcessManagerRunning    bool        `json:"process_manager_running"`
	Reachable                bool        `json:"reachable"`
	ReachabilityError        string      `json:"reachability_error,omitempty"`
	ReachabilityTransport    string      `json:"reachability_transport,omitempty"`
	DirectTransportReachable bool        `json:"direct_transport_reachable"`
	DirectTransportError     string      `json:"direct_transport_error,omitempty"`
	ProvenanceMatches        bool        `json:"provenance_matches_installed"`
	State                    PublicState `json:"state,omitempty"`
	AlreadyRunning           bool        `json:"already_running,omitempty"`
	AutomaticVersionHandoff  bool        `json:"automatic_version_handoff,omitempty"`
	Hub                      HubStatus   `json:"hub"`

	// Identity answers "whose daemon is this?" separately from "did something
	// answer on the endpoint?". ReadyVerified is the only condition under which
	// status may present a daemon as ready; ReportedState is what a reader
	// should act on, and never claims ready that was not verified.
	Identity       Identity `json:"identity,omitempty"`
	IdentityDetail string   `json:"identity_detail,omitempty"`
	ReadyVerified  bool     `json:"ready_verified"`
	ReportedState  string   `json:"reported_state,omitempty"`

	// The runtime location this invocation resolved. Status reports it so an
	// operator can name the endpoint and the record without starting anything.
	WBHome     string `json:"wb_home,omitempty"`
	RuntimeDir string `json:"runtime_path,omitempty"`
	SocketPath string `json:"socket_path,omitempty"`
	StatePath  string `json:"state_path,omitempty"`

	// LegacyRuntime names a daemon still serving the runtime directory WB used
	// before it resolved its home. Its presence is why a start was refused.
	LegacyRuntime *LegacyEndpoint `json:"legacy_runtime,omitempty"`

	// SupervisorMismatch is set when the running daemon's recorded supervisor
	// (State.Supervisor, self-reported at its own startup) disagrees with what
	// this invocation could independently observe about it right now: either
	// a specific systemd unit's own queried state (daemonObservedSystemdUnitState,
	// `systemctl --user show`, the sneat-dev/wb#617 detector this feature
	// exists for), or, when that is unavailable, the coarser cgroup-membership
	// comparison (internal/daemon.ObservedCgroupSupervisor's documented
	// limit).
	SupervisorMismatch string `json:"supervisor_mismatch,omitempty"`

	// Warning surfaces a non-fatal condition worth an operator's attention on
	// an otherwise-successful result. Used when an implicit `wb daemon start`
	// caller (`wb dashboard --local`, or an RPC client's own automatic
	// bootstrap in daemon_rpc.go) finds a live, healthy, supervised daemon
	// running a different executable than this invocation's own: touching it
	// is refused, but the caller still gets a working connection back instead
	// of an outright failure (sneat-dev/wb#622 review item 9).
	Warning string `json:"warning,omitempty"`
}

type HubStatus struct {
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
	Webhook               bool            `json:"webhook"`
	WebhookPublicURL      string          `json:"webhook_public_url,omitempty"`
	RepositoriesPolled    int             `json:"repositories_polled"`
	LastEventReceived     *HubEventMarker `json:"last_event_received,omitempty"`
	LastEventAcknowledged *HubEventMarker `json:"last_event_acknowledged,omitempty"`
	// WebhookRedelivery is the missed-webhook recovery sweep's last completed
	// pass. Like RepositoriesPolled, it comes from the running daemon's
	// health endpoint, because only the serving process has it.
	WebhookRedelivery *HubRedeliverySweep `json:"webhook_redelivery,omitempty"`
}

type HubRedeliverySweep struct {
	LastSweepAt      *time.Time `json:"last_sweep_at,omitempty"`
	Redelivered      int        `json:"redelivered"`
	Abandoned        int        `json:"abandoned"`
	Uncounted        int        `json:"uncounted"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	LastFailureClass string     `json:"last_failure_class,omitempty"`
}

type HubEventMarker struct {
	ID         string    `json:"id,omitempty"`
	Event      string    `json:"event,omitempty"`
	OccurredAt time.Time `json:"occurred_at"`
}

type PublicState struct {
	SchemaVersion int               `json:"schema_version"`
	Status        daemon.Status     `json:"status"`
	PID           int               `json:"pid,omitempty"`
	Listen        string            `json:"listen"`
	Provenance    daemon.Provenance `json:"provenance"`
	Queue         PublicQueue       `json:"queue"`
	StartedAt     time.Time         `json:"started_at,omitempty"`
	UpdatedAt     time.Time         `json:"updated_at"`
	// WBHome, StatePath and StoppedReason are the record's own account of where
	// it lives and, for a stop it did not choose, why. They are part of the
	// public projection because identity is exactly what a reader must be able
	// to check.
	WBHome        string `json:"wb_home,omitempty"`
	StatePath     string `json:"state_path,omitempty"`
	StoppedReason string `json:"stopped_reason,omitempty"`
	// Supervisor is what the reporting record's process observed about its own
	// start (REQ: report-supervisor). It is always one of "systemd", "launchd",
	// or "none" — never blank — so a reader never has to guess what an absent
	// value means.
	Supervisor daemon.Supervisor `json:"supervisor,omitempty"`
	// SupervisorLabel is the supervisor's own identity for the job (currently
	// only a launchd job label). See daemon.State.SupervisorLabel.
	SupervisorLabel string `json:"supervisor_label,omitempty"`
}

type PublicQueue struct {
	SchemaVersion int                `json:"schema_version"`
	Generation    uint64             `json:"generation"`
	Owner         daemon.Provenance  `json:"owner"`
	HandoffFrom   *daemon.Provenance `json:"handoff_from,omitempty"`
	HandoffAt     *time.Time         `json:"handoff_at,omitempty"`
}

func PublicStateOf(state daemon.State) PublicState {
	return PublicState{
		SchemaVersion: state.SchemaVersion, Status: state.Status, PID: state.PID,
		Listen: state.Listen, Provenance: state.Provenance, StartedAt: state.StartedAt, UpdatedAt: state.UpdatedAt,
		WBHome: state.WBHome, StatePath: state.StatePath, StoppedReason: state.StoppedReason,
		Supervisor: state.ReportedSupervisor(), SupervisorLabel: state.SupervisorLabel,
		Queue: PublicQueue{SchemaVersion: state.Queue.SchemaVersion, Generation: state.Queue.Generation,
			Owner: state.Queue.Owner, HandoffFrom: state.Queue.HandoffFrom, HandoffAt: state.Queue.HandoffAt},
	}
}

type Dependencies struct {
	GuardTicker func(time.Duration) (<-chan time.Time, func())
	UsageError  func(string) error
	Bounds      func() LifecycleBounds
	// listen binds the dashboard endpoint; nil means net.Listen. Tests hand it a
	// listener whose bound address is not the one that was asked for.
	Now        func() time.Time
	Executable func() (string, error)
	Start      func(string, []string, string) (int, error)
	// checkOtherRoot runs at the top of launch, before any lifecycle state is
	// written and before start is called. It refuses a start for projects root
	// `root` when the platform's one fixed-label supervisor service is already
	// registered for a different projects root, unless replace is true. A nil
	// value skips the check (tests that do not exercise it).
	CheckOtherRoot func(root string, replace bool) error
	Alive          func(int) bool
	Stop           func(pid int, supervisor daemon.Supervisor, supervisorLabel string) error
	Sleep          func(time.Duration)
	// lockNow is a dedicated clock seam for stateLock's short retry deadline.
	// It must never be `now` (which the production default strips to a
	// UTC, non-monotonic reading via time.Time.UTC(), a wall-clock time that
	// an NTP step or a VM resume can jump under a waiter's feet). The default
	// is plain time.Now, whose monotonic reading makes the 5s deadline
	// immune to wall-clock adjustments, matching Go's own recommendation for
	// measuring elapsed time (see time.Since and the "Monotonic Clocks"
	// section of the time package doc).
	LockNow       func() time.Time
	Version       func() buildinfo.Report
	Token         func() (string, error)
	Health        func(context.Context, string) error
	OwnedHealth   func(context.Context, string, int, uint64) error
	BridgeHealth  func(context.Context, string, string) error
	RestartTicker func(time.Duration) (<-chan time.Time, func())
	RawPolicy     func(string) (bool, string, error)
	LocalClient   func(string, string) (*http.Client, error)
	HubConfigPath func() string
	HubHealth     func(context.Context, string) (HubStatus, error)
	// hubTuning is nil everywhere but the whole-journey end-to-end test; see
	// the type's documentation.
	// getenv, getpid, and getppid are the seams `daemon serve` reads a
	// supervisor's own evidence through (INVOCATION_ID, SYSTEMD_EXEC_PID,
	// XPC_SERVICE_NAME, and this process's own pid/ppid, which distinguish a
	// supervisor's own child from a process that merely inherited its
	// environment), so a test never needs a real systemd or launchd to
	// exercise supervisor detection.
	Getenv  func(string) string
	Getpid  func() int
	Getppid func() int
	// observedSupervisor independently observes evidence about a running PID
	// that this build did not itself write, for `wb daemon status` to compare
	// against what that process recorded about its own start. See
	// internal/daemon.ObservedCgroupSupervisor.
	ObservedSupervisor func(int) (daemon.Supervisor, bool)
	// processStartTime observes a process's own start time, so a stale
	// hard-coded test PID cannot collide with a real, unrelated process this
	// build did not create — the default reads the real OS the way
	// daemon.ProcessStartTime already does; a test overrides it instead of
	// hoping its fake PIDs are never real ones (sneat-dev/wb#622 review: tests
	// must not depend on the real process table for correctness).
	ProcessStartTime func(int) (time.Time, bool)
	// supervisorPresent independently checks whether the recorded supervisor
	// can still be shown to exist at all, so `wb daemon start`/`restart` do
	// not refuse forever on a stale record naming a supervisor that is long
	// gone (sneat-dev/wb#622 review item 4).
	SupervisorPresent func(supervisor daemon.Supervisor, label string) (present bool, unitName string)
	// systemdUnitName and systemdUnitState are the sneat-dev/wb#617 detector's
	// own seams: systemdUnitName resolves which systemd user unit `wb daemon
	// status` checks (config, or daemonDefaultSystemdUnit), and
	// systemdUnitState queries that unit's own state directly
	// (`systemctl --user show`), independent of cgroup membership — the
	// detector that can see a live host's confirmed shape: an orphaned,
	// unsupervised daemon (recorded supervisor=none, a session scope — not
	// the unit's own cgroup) serving the port while its systemd unit sat
	// failed and crash-looping (sneat-dev/wb#622 review item 2).
	SystemdUnitName  func() string
	SystemdUnitState func(unit string) (daemon.SystemdUnitState, bool)
	// observedCgroupUnit lets `daemon serve` record the systemd unit it is
	// ACTUALLY running inside (from its own /proc/self/cgroup) at startup,
	// so a later `wb daemon status` invocation can prefer that over its own
	// configured/default guess — which is wrong whenever the status
	// invocation's own environment does not carry the same
	// WB_DAEMON_SYSTEMD_UNIT the daemon itself was supervised under
	// (sneat-dev/wb#622 review round 3, item M3).
	ObservedCgroupUnit func(pid int) (string, bool)
}

func DefaultDependencies(usageError func(string) error) Dependencies {
	native := defaultNativeOperations()
	return Dependencies{GuardTicker: newRuntimeTicker, UsageError: usageError, Bounds: DefaultLifecycleBounds,
		Now:            func() time.Time { return time.Now().UTC() },
		LockNow:        time.Now,
		Executable:     os.Executable,
		Start:          native.startDaemonProcess,
		CheckOtherRoot: native.daemonCheckOtherRoot,
		Alive:          native.processAlive,
		Stop:           native.stopDaemonProcess,
		Sleep:          time.Sleep,
		Version:        buildinfo.Snapshot,
		Token:          daemonOwnerToken,
		Health:         daemonHealthy,
		OwnedHealth:    daemonOwnedHealthy,
		BridgeHealth:   daemonFileBridgeHealthy,
		RestartTicker: func(interval time.Duration) (<-chan time.Time, func()) {
			ticker := time.NewTicker(interval)
			return ticker.C, ticker.Stop
		},
		LocalClient:   LocalHTTPClient,
		HubConfigPath: wbconfig.DefaultPath,
		HubHealth:     daemonHubHealth,
		RawPolicy: func(root string) (bool, string, error) {
			return daemonRawPolicyWithResolver(root, daemon.RawExecutionPolicyPath)
		},
		Getenv:  os.Getenv,
		Getpid:  os.Getpid,
		Getppid: os.Getppid,
		ObservedSupervisor: func(pid int) (daemon.Supervisor, bool) {
			return daemon.ObservedCgroupSupervisor(pid, DaemonSystemdUnitName(os.Getenv))
		},
		ProcessStartTime:   daemon.ProcessStartTime,
		SupervisorPresent:  native.daemonSupervisorPresent,
		SystemdUnitName:    func() string { return DaemonSystemdUnitName(os.Getenv) },
		SystemdUnitState:   native.daemonObservedSystemdUnitState,
		ObservedCgroupUnit: daemon.ObservedCgroupUnit,
	}
}

func daemonRawPolicyWithResolver(root string, resolve func() (string, error)) (bool, string, error) {
	path, err := resolve()
	if err != nil {
		return false, "", err
	}
	allowed, err := daemon.LoadRawExecutionPolicy(path, root)
	return allowed, path, err
}

type RecoveryResult struct {
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

// controllerStateStages preserves the real store while making each durable
// checkpoint's refusal observable independently of later filesystem effects.
type controllerStateStages struct {
	load func() (daemon.State, bool, error)
	save func(daemon.State) error
}

type controllerLockStages struct {
	protectPath func(string) error
	inspect     func(int, *unix.Stat_t) error
	protect     func(*os.File) error
	lock        func(*os.File) (bool, error)
}

func nativeControllerLockStages() controllerLockStages {
	return controllerLockStages{protectPath: daemonlifecycle.ProtectOwnerOnly, inspect: unix.Fstat, protect: daemonlifecycle.ProtectOwnerOnlyFile, lock: tryLockDaemonFile}
}

type Controller struct {
	locks controllerLockStages
	state controllerStateStages
	deps  Dependencies
	store daemon.Store
	root  string
	// replaceOtherRoot is the explicit --replace-other-root request: let a start
	// for this projects root replace a supervisor service registered for a
	// different one. Zero for every implicit caller.
	replaceOtherRoot bool
}

func (controller Controller) WithReplaceOtherRoot(replace bool) Controller {
	controller.replaceOtherRoot = replace
	return controller
}

func NewController(deps Dependencies, root string) Controller {
	statePath, _ := StatePath(root)
	store := daemon.Store{Path: statePath}
	return Controller{deps: deps, store: store, root: root,
		state: controllerStateStages{load: store.Load, save: store.Save}, locks: nativeControllerLockStages()}
}

func StatePath(root string) (string, error) {
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

const daemonLifecycleOwnerFile = "daemon.lifecycle.owner"

func daemonLifecycleOwnerPath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, daemonLifecycleOwnerFile), nil
}

func daemonStateLockPath(root string) (string, error) {
	dir, err := daemon.RuntimeDir(root)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "daemon.state.lock"), nil
}

func (controller Controller) AcquireStateLock() (func(), error) {
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

	var stat unix.Stat_t
	if err := controller.locks.inspect(fd, &stat); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("inspect daemon state lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		return nil, fmt.Errorf("daemon state lock must be a single-link owner-only regular file: %s", path)
	}
	if created {
		err = controller.locks.protect(file)
	}
	if err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("protect daemon state lock: %w", err)
	}
	if err := daemonlifecycle.ValidateOwnerOnlyFile(file); err != nil {
		_ = file.Close()
		return nil, fmt.Errorf("validate daemon state lock permissions: %w", err)
	}
	deadline := controller.deps.LockNow().Add(controller.bounds().Ready)
	for {
		locked, err := controller.locks.lock(file)
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
		if controller.deps.LockNow().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another process held the daemon state lock for %s", controller.bounds().Ready)
		}
		controller.deps.Sleep(20 * time.Millisecond)
	}
}

func (controller Controller) lifecycleLock() (func(), error) {
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
	if pid > 0 && controller.deps.Alive(pid) {
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

func (controller Controller) openLifecycleLock(create bool) (*os.File, bool, bool, error) {
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

	var stat unix.Stat_t
	if err := controller.locks.inspect(fd, &stat); err != nil {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("inspect daemon lifecycle lock: %w", err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("daemon lifecycle lock must be a single-link owner-only regular file: %s", path)
	}
	if created {
		err = controller.locks.protect(file)
	}
	if err != nil {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("protect daemon lifecycle lock: %w", err)
	}
	if err := daemonlifecycle.ValidateOwnerOnlyFile(file); err != nil {
		_ = file.Close()
		return nil, false, false, fmt.Errorf("validate daemon lifecycle lock permissions: %w", err)
	}
	locked, err := controller.locks.lock(file)
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

func (controller Controller) lifecycleOwnerPID(legacyLock *os.File) (int, error) {
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

	defer func() { _ = owner.Close() }()
	var stat unix.Stat_t
	if err := controller.locks.inspect(fd, &stat); err != nil {
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

func (controller Controller) writeLifecycleOwnerPID(pid int) error {
	return controller.writeLifecycleOwnerPIDInjected(pid, nil)
}

func (controller Controller) writeLifecycleOwnerPIDInjected(pid int, inj *filewrite.Injector) error {
	path, err := daemonLifecycleOwnerPath(controller.root)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return err
	}
	temporary, err := filewrite.CreateTemp(directory, ".daemon-lifecycle-owner-*", inj)
	if err != nil {
		return fmt.Errorf("create daemon lifecycle owner: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := controller.locks.protectPath(temporaryName); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect daemon lifecycle owner: %w", err)
	}
	if err := filewrite.Write(temporary, []byte(fmt.Sprintf("pid=%d\n", pid)), temporaryName, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write daemon lifecycle owner: %w", err)
	}
	if err := filewrite.Sync(temporary, temporaryName, inj); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync daemon lifecycle owner: %w", err)
	}
	if err := filewrite.Close(temporary, temporaryName, inj); err != nil {
		return err
	}
	if err := filewrite.Rename(temporaryName, path, inj); err != nil {
		return fmt.Errorf("replace daemon lifecycle owner: %w", err)
	}
	return nil
}

func (controller Controller) stableLifecycleState() (daemon.Status, error) {
	state, found, err := controller.state.load()
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
		if state.PID <= 0 || !controller.deps.Alive(state.PID) {
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

func (controller Controller) RecoverLifecycleLock(ctx context.Context, apply bool) (RecoveryResult, error) {
	lockPath, lockErr := daemonLifecycleLockPath(controller.root)
	if lockErr != nil {
		return RecoveryResult{}, lockErr
	}
	// Both displayed paths describe the same resolved runtime snapshot.
	ownerPath := filepath.Join(filepath.Dir(lockPath), daemonLifecycleOwnerFile)
	result := RecoveryResult{Action: "recover", LockPath: lockPath, OwnerPath: ownerPath, Reason: "no_lock"}
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
	result.OwnerAlive = controller.deps.Alive(pid)
	if result.OwnerAlive {
		result.Reason = "owner_alive"
		result.Detail = fmt.Sprintf("daemon lifecycle lock owner process %d is still alive", pid)
		return result, nil
	}
	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		return result, err
	}
	defer releaseState()
	state, found, err := controller.state.load()
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
		if state.PID > 0 && controller.deps.Alive(state.PID) {
			current, provenanceErr := controller.Provenance()
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
				state.MarkReadyWithProcess(state.PID, ProcessStartedAt(state.PID), controller.deps.Now())
				if err := controller.state.save(state); err != nil {
					return result, err
				}
			}
			break
		}
		if state.PID == 0 {
			if controller.deps.Now().Sub(state.UpdatedAt) < controller.bounds().Ready {
				result.Reason, result.Detail = "startup_grace_period", "daemon startup is still inside its readiness grace period"
				return result, nil
			}
			current, provenanceErr := controller.Provenance()
			if provenanceErr != nil {
				return result, provenanceErr
			}
			if !state.Provenance.SameBinary(current) {
				result.Reason, result.Detail = "unfenced_startup", "daemon startup has no recorded process and belongs to a different executable; refusing unfenced recovery"
				return result, nil
			}
			if controller.deps.Health(ctx, state.Listen) == nil {
				result.Reason, result.Detail = "startup_api_reachable", "daemon startup has no recorded process but its API is reachable"
				return result, nil
			}
		}
		result.Reason = "interrupted_start"
		if apply {
			token, tokenErr := controller.deps.Token()
			if tokenErr != nil {
				return result, tokenErr
			}
			state.OwnerToken = token
			state.Queue.OwnerToken = token
			state.MarkStopped(controller.deps.Now())
			if err := controller.state.save(state); err != nil {
				return result, err
			}
		}
	case daemon.StatusDraining:
		if state.PID > 0 && controller.deps.Alive(state.PID) {
			result.Reason = "drain_process_alive"
			result.Detail = fmt.Sprintf("daemon drain process %d is still alive", state.PID)
			return result, nil
		}
		result.Reason = "interrupted_drain"
		if apply {
			state.MarkStopped(controller.deps.Now())
			if err := controller.state.save(state); err != nil {
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

func (controller Controller) Provenance() (daemon.Provenance, error) {
	executable, err := controller.deps.Executable()
	if err != nil {
		return daemon.Provenance{}, err
	}
	version := controller.deps.Version()
	return daemon.ProvenanceForExecutable(executable, version.Version, version.Revision, version.Built)
}

func (controller Controller) ownedHealth(ctx context.Context, listen string, pid int, generation uint64) error {
	if pid <= 0 || !controller.deps.Alive(pid) {
		return fmt.Errorf("daemon process %d is not alive", pid)
	}
	if controller.deps.OwnedHealth != nil {
		return controller.deps.OwnedHealth(ctx, listen, pid, generation)
	}
	return controller.deps.Health(ctx, listen)
}

func (controller Controller) MarkStoppedIfOwned(ownerToken string) error {
	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		return err
	}
	defer releaseState()
	current, found, err := controller.state.load()
	if err != nil || !found || current.OwnerToken != ownerToken {
		return err
	}
	current.MarkStopped(controller.deps.Now())
	return controller.state.save(current)
}

func (controller Controller) Status(ctx context.Context) (Result, error) {
	state, found, err := controller.state.load()
	if err != nil {
		return Result{}, err
	}
	result := Result{Action: "status", Managed: found, State: PublicStateOf(state)}
	if location, locationErr := ResolveLocation(controller.root); locationErr == nil {
		result.WBHome, result.RuntimeDir, result.SocketPath, result.StatePath =
			location.Home, location.RuntimeDir, location.SocketPath, location.StatePath
	}
	// The legacy endpoint is reported even when this home has no daemon: that
	// is precisely the case in which a leftover daemon looks healthy while
	// nothing here records one.
	if legacy := detectLegacyDaemon(controller.root, controller.deps.Alive); legacy.present() {
		result.LegacyRuntime = &legacy
	}
	identity, detail, alive := controller.assessIdentity(state, found)
	result.Identity, result.IdentityDetail = identity, detail
	// Liveness and ownership are separate: a process can be alive and still not
	// be this home's daemon, and reachability must stay reportable for it.
	result.ProcessManagerRunning = alive && identity == identityCurrent
	// The sneat-dev/wb#617 double-owner state is a live daemon of this home
	// whose recorded supervisor disagrees with what this build can
	// independently observe about it right now. A record this home owns and
	// a process this home can inspect are both required: a foreign or
	// unrecorded daemon is reported through Identity instead, not through
	// this check.
	if result.ProcessManagerRunning {
		recorded := state.ReportedSupervisor()
		// The precise detector this feature exists for: a SPECIFIC systemd
		// unit (the one this build is configured to expect, or
		// daemonDefaultSystemdUnit) exists and is failing to keep the daemon
		// up, while the process actually answering the port recorded no
		// supervisor at all. Confirmed live: an orphaned `daemon serve`
		// (PPID 1, running in a session scope — not the unit's own cgroup)
		// served the port while wb-daemon.service sat ActiveState=failed
		// with NRestarts=4468 — a shape the cgroup check below cannot see at
		// all, because the orphan's own cgroup shows no service membership
		// (sneat-dev/wb#622 review item 2).
		if recorded == daemon.SupervisorNone && controller.deps.SystemdUnitState != nil && controller.deps.SystemdUnitName != nil {
			// Prefer the daemon's OWN observed unit (State.SystemdUnit,
			// recorded from its own /proc/self/cgroup at serve startup) over
			// this invocation's own configured/default guess: a status
			// invocation run without the same WB_DAEMON_SYSTEMD_UNIT the
			// daemon itself was supervised under would otherwise query the
			// WRONG unit and see a false "no systemd service membership"
			// (sneat-dev/wb#622 review round 3, item M3).
			unit := state.SystemdUnit
			if strings.TrimSpace(unit) == "" {
				unit = controller.deps.SystemdUnitName()
			}
			if unitState, known := controller.deps.SystemdUnitState(unit); known && daemon.SystemdUnitLooksOrphaned(unitState) {
				result.SupervisorMismatch = fmt.Sprintf(
					"this daemon recorded supervisor=none at startup, but its systemd user unit %s is %s (NRestarts=%d); a supervisor for this home exists and is failing to keep it up (sneat-dev/wb#617)",
					unit, unitState.ActiveState, unitState.NRestarts)
			}
		}
		// A coarser, best-effort fallback: cgroup membership, not parentage —
		// a parent-PID check cannot tell "started by a systemd unit" from
		// "orphaned and reparented to PID 1", which IS systemd on every host
		// this matters for (sneat-dev/wb#622 review item 7 from the previous
		// round). Both directions are reported: recorded none while the
		// cgroup shows the expected systemd service, and recorded systemd
		// while it does not.
		if result.SupervisorMismatch == "" && controller.deps.ObservedSupervisor != nil {
			if observed, known := controller.deps.ObservedSupervisor(state.PID); known {
				switch {
				case recorded == daemon.SupervisorNone && observed == daemon.SupervisorSystemd:
					result.SupervisorMismatch = fmt.Sprintf(
						"this daemon recorded supervisor=none at startup, but process %d's cgroup shows systemd service membership; a supervisor unit for this home may exist and be fighting this process for the runtime (sneat-dev/wb#617)",
						state.PID)
				case recorded == daemon.SupervisorSystemd && observed != daemon.SupervisorSystemd:
					result.SupervisorMismatch = fmt.Sprintf(
						"this daemon recorded supervisor=systemd at startup, but process %d's cgroup shows no systemd service membership; the recorded and observed supervisor disagree",
						state.PID)
				}
			}
		}
	}
	result.Hub = controller.hubStatus(ctx, state.Listen)
	current, err := controller.Provenance()
	if err != nil {
		return Result{}, err
	}
	result.ProvenanceMatches = state.Provenance.SameBinary(current)
	if !found {
		result.ReportedState = "absent"
		// A daemon answering on the configured endpoint while this home records
		// nothing is exactly the shape of both observed failures: something is
		// serving, and nothing here can claim it. Reachability stays its own
		// condition, reported next to identity=absent rather than as a daemon
		// this home owns.
		if healthErr := controller.deps.Health(ctx, DefaultListen); healthErr == nil {
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
		state.MarkStopped(controller.deps.Now())
		if err := controller.state.save(state); err != nil {
			return Result{}, err
		}
		result.State = PublicStateOf(state)
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
		if healthErr := controller.deps.Health(ctx, state.Listen); healthErr == nil {
			result.Reachable = true
			result.DirectTransportReachable = true
			result.ReachabilityTransport = "direct"
		} else {
			result.DirectTransportError = healthErr.Error()
			if daemonFileBridgeFallbackAllowed(healthErr) {
				bridgeHealth := controller.deps.BridgeHealth
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

func (controller Controller) hubStatus(ctx context.Context, listen string) HubStatus {
	path := wbconfig.DefaultPath()
	if controller.deps.HubConfigPath != nil {
		path = controller.deps.HubConfigPath()
	}
	cfg, found, err := hubconfig.Load(path)
	if err != nil || !found {
		return HubStatus{}
	}
	status := HubStatus{
		Mounted: true, Engine: cfg.Store.Engine, Store: cfg.Location(), Listen: listen,
		Polling: strings.TrimSpace(cfg.GitHub.TokenFile) != "", PollInterval: cfg.GitHub.PollInterval.String(),
		Webhook: cfg.GitHub.App != nil,
	}
	if cfg.GitHub.App != nil {
		status.WebhookPublicURL = cfg.GitHub.App.PublicURL
	}
	health := controller.deps.HubHealth
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
	if live.WebhookRedelivery != nil {
		status.WebhookRedelivery = &HubRedeliverySweep{
			LastSweepAt:      live.WebhookRedelivery.LastSweepAt,
			Redelivered:      live.WebhookRedelivery.Redelivered,
			Abandoned:        live.WebhookRedelivery.Abandoned,
			Uncounted:        live.WebhookRedelivery.Uncounted,
			LastFailureAt:    live.WebhookRedelivery.LastFailureAt,
			LastFailureClass: live.WebhookRedelivery.LastFailureClass,
		}
	}
	return status
}

func daemonHubHealth(ctx context.Context, listen string) (HubStatus, error) {
	requestCtx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "http://"+listen+"/api/v1/health", nil)
	if err != nil {
		return HubStatus{}, err
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		return HubStatus{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return HubStatus{}, fmt.Errorf("health endpoint returned %s", response.Status)
	}
	var payload struct {
		Hub *HubStatus `json:"hub"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&payload); err != nil {
		return HubStatus{}, err
	}
	if payload.Hub == nil {
		return HubStatus{}, errors.New("health endpoint reported no hub")
	}
	return *payload.Hub, nil
}

func (controller Controller) Start(ctx context.Context, listen string) (Result, error) {
	return controller.StartWithProgress(ctx, listen, nil, false)
}

func (controller Controller) StartWithProgress(ctx context.Context, listen string, progress func(string), forceDetached bool) (Result, error) {
	release, err := controller.lifecycleLock()
	if err != nil {
		return Result{}, err
	}
	defer release()
	if err := RequireLoopbackAddress(listen); err != nil {
		return Result{}, controller.deps.UsageError(err.Error())
	}
	// Refuse before binding when a daemon still serves the runtime directory
	// this build no longer writes to. Starting a second daemon there would put
	// two daemons on two homes, each invisible to the other's supervisor, and
	// the legacy socket is the only evidence that the first one is alive.
	if legacy := detectLegacyDaemon(controller.root, controller.deps.Alive); legacy.present() {
		return Result{Action: "start", LegacyRuntime: &legacy}, fmt.Errorf(
			"refusing to start a daemon while a daemon still serves the legacy runtime directory: %s; stop that daemon first (wb daemon stop, with the --projects-root it was started under) rather than letting it keep serving an abandoned home",
			legacy.describe())
	}
	state, found, err := controller.state.load()
	if err != nil {
		return Result{}, err
	}
	current, err := controller.Provenance()
	if err != nil {
		return Result{}, err
	}
	identity, identityDetail, alive := controller.assessIdentity(state, found)
	if identity == identityForeignHome || identity == identityForeignStatePath {
		return Result{Action: "start", Managed: true, State: PublicStateOf(state), Identity: identity, IdentityDetail: identityDetail},
			fmt.Errorf("refusing to replace a lifecycle record that belongs to another WB home: %s", identityDetail)
	}
	if found && identity == identityCurrent && alive && state.Status == daemon.StatusReady {
		if state.Listen != listen {
			// The daemon IS running, on a different --listen than requested —
			// never phrased as "if it is not running" (sneat-dev/wb#622
			// review item 15), and refused/allowed the same way as any other
			// live-under-a-supervisor case below.
			if refusal := controller.refuseDetachedStartUnderSupervisor(state, found, true, listen, forceDetached); refusal != "" {
				return Result{Action: "start", Managed: true, State: PublicStateOf(state), Identity: identity, IdentityDetail: identityDetail}, errors.New(refusal)
			}
		} else if state.Provenance.SameBinary(current) {
			result := Result{Action: "start", Managed: true, ProcessManagerRunning: true, ProvenanceMatches: true, State: PublicStateOf(state), AlreadyRunning: true, Identity: identity, IdentityDetail: identityDetail, ReadyVerified: true, ReportedState: string(state.Status)}
			if healthErr := controller.deps.Health(ctx, state.Listen); healthErr == nil {
				result.Reachable = true
				result.DirectTransportReachable = true
				result.ReachabilityTransport = "direct"
			} else {
				result.ReachabilityError = healthErr.Error()
				result.DirectTransportError = healthErr.Error()
			}
			return result, nil
		} else {
			// A live daemon of a different binary is an executable-handoff
			// path: when it is supervised, the supervisor restarts it with
			// the new binary rather than this process starting a detached
			// replacement behind the supervisor's back (sneat-dev/wb#617).
			supervisor := controller.effectiveSupervisor(state)
			supervised := supervisor == daemon.SupervisorSystemd ||
				(supervisor == daemon.SupervisorLaunchd && state.SupervisorLabel != LaunchdLabel)
			if supervised && !daemonSupervisedInstalledBinaryMatches(state, current) {
				// An IMPLICIT Start reached this from a caller that never
				// asked to manage this daemon's lifecycle at all — `wb
				// dashboard --local` (dashboard.go) or an RPC client's own
				// automatic bootstrap (daemon_rpc.go's daemonOperationClient)
				// — it just wants a working connection. The daemon on the
				// other end of that connection IS alive and healthy; it is
				// only THIS invocation's own binary that differs from what
				// is recorded. Refusing outright would break every such
				// caller merely because an unrelated binary (an older/newer
				// installed CLI, a worktree build) happened to invoke them
				// (sneat-dev/wb#622 review item 9) — report the mismatch as
				// a warning on a live result instead. `wb daemon restart`
				// and the self-update hook keep the harder refusal
				// (stopAndReplace, below): an operator or a self-update
				// explicitly asking to restart deserves to be told it did
				// not happen, not a silent no-op dressed as success.
				result := Result{
					Action: "start", Managed: true, ProcessManagerRunning: true,
					State: PublicStateOf(state), Identity: identity, IdentityDetail: identityDetail,
					ReportedState: string(state.Status),
					Warning: fmt.Sprintf(
						"the running supervised daemon's executable (%s) does not match this invocation's own binary (%s); leaving it running rather than restarting it — this invocation is not the one that should manage its lifecycle",
						state.Provenance.Executable, current.Executable),
				}
				if healthErr := controller.deps.Health(ctx, state.Listen); healthErr == nil {
					result.Reachable = true
					result.DirectTransportReachable = true
					result.ReachabilityTransport = "direct"
				} else {
					result.ReachabilityError = healthErr.Error()
					result.DirectTransportError = healthErr.Error()
				}
				return result, nil
			}
			return controller.stopAndReplace(ctx, state, listen, current, "start", progress)
		}
	}
	// Nothing alive to hand off from. A runtime this build's own records show
	// as owned by a supervisor must be started through that supervisor, not by
	// a detached process this command would spawn itself.
	if refusal := controller.refuseDetachedStartUnderSupervisor(state, found, false, listen, forceDetached); refusal != "" {
		return Result{Action: "start", Managed: found, State: PublicStateOf(state)}, errors.New(refusal)
	}
	return controller.launch(ctx, OptionalState(state, found), listen, current, "start", found)
}

func (controller Controller) refuseDetachedStartUnderSupervisor(state daemon.State, found, alive bool, requestedListen string, forceDetached bool) string {
	if !found || forceDetached {
		return ""
	}
	supervisor := state.ReportedSupervisor()
	if supervisor == daemon.SupervisorNone {
		return ""
	}
	if supervisor == daemon.SupervisorLaunchd && state.SupervisorLabel == LaunchdLabel {
		// wb's own self-managed launchd job: `wb daemon start` IS how this
		// gets (re)started from cold on a mac (bootstrap+kickstart), so there
		// is no separate supervisor to defer to (sneat-dev/wb#622 review item
		// 1's shape, applied to the refusal path too — without this, a
		// crashed mac daemon could never be started by `wb daemon start`
		// again).
		return ""
	}
	present, unitName := true, ""
	if controller.deps.SupervisorPresent != nil {
		present, unitName = controller.deps.SupervisorPresent(supervisor, state.SupervisorLabel)
	}
	if !present {
		return ""
	}
	situation := fmt.Sprintf("this runtime's last recorded owner (as of %s)", state.UpdatedAt.UTC().Format(time.RFC3339))
	if alive {
		situation = fmt.Sprintf("this runtime is already running (pid %d, listening on %s rather than the requested %s), and its recorded owner", state.PID, state.Listen, requestedListen)
	}
	if supervisor == daemon.SupervisorSystemd {
		name := unitName
		if strings.TrimSpace(name) == "" {
			name = DaemonSystemdUnitName(os.Getenv)
		}
		return fmt.Sprintf(
			"refusing to start a detached daemon: %s is a systemd user service; restart it through that supervisor instead of `wb daemon start` — e.g. `systemctl --user restart %s` — or pass --force-detached to override",
			situation, name)
	}
	// ReportedSupervisor admits only none/systemd/launchd; none returned above.
	label := unitName
	if strings.TrimSpace(label) == "" {
		label = state.SupervisorLabel
	}
	if strings.TrimSpace(label) == "" {
		label = LaunchdLabel
	}
	return fmt.Sprintf(
		"refusing to start a detached daemon: %s is a launchd agent; restart it through that supervisor instead of `wb daemon start` — e.g. `launchctl kickstart -k gui/%d/%s` — or pass --force-detached to override",
		situation, os.Getuid(), label)
}

func OptionalState(state daemon.State, found bool) *daemon.State {
	if !found {
		return nil
	}
	return &state
}

func (controller Controller) RestartWithProgress(ctx context.Context, ifRunning bool, progress func(string), forceDetached bool) (Result, error) {
	release, err := controller.lifecycleLock()
	if err != nil {
		return Result{}, err
	}
	defer release()
	state, found, err := controller.state.load()
	if err != nil {
		return Result{}, err
	}
	if !found || state.Status == daemon.StatusStopped || state.PID == 0 || !controller.deps.Alive(state.PID) {
		if ifRunning {
			return Result{Action: "restart", Managed: found, State: PublicStateOf(state)}, nil
		}
		// Nothing alive to hand off from. A runtime this build's own records
		// show as owned by a supervisor must be started through that
		// supervisor, not by a detached process this command would spawn.
		if refusal := controller.refuseDetachedStartUnderSupervisor(state, found, false, daemonListenOrDefault(state.Listen), forceDetached); refusal != "" {
			return Result{Action: "restart", Managed: found, State: PublicStateOf(state)}, errors.New(refusal)
		}
		current, err := controller.Provenance()
		if err != nil {
			return Result{}, err
		}
		var result Result
		controller.restartPhase(progress, "starting daemon", func() {
			result, err = controller.launch(ctx, OptionalState(state, found), daemonListenOrDefault(state.Listen), current, "restart", found)
		})
		return result, err
	}
	current, err := controller.Provenance()
	if err != nil {
		return Result{}, err
	}
	return controller.stopAndReplace(ctx, state, daemonListenOrDefault(state.Listen), current, "restart", progress)
}

func (controller Controller) effectiveSupervisor(state daemon.State) daemon.Supervisor {
	recorded := state.ReportedSupervisor()
	if recorded != daemon.SupervisorNone {
		return recorded
	}
	if state.PID <= 0 || controller.deps.ObservedSupervisor == nil {
		return recorded
	}
	if observed, known := controller.deps.ObservedSupervisor(state.PID); known && observed == daemon.SupervisorSystemd {
		return daemon.SupervisorSystemd
	}
	return recorded
}

func daemonSupervisedInstalledBinaryMatches(state daemon.State, current daemon.Provenance) bool {
	installedNow, readErr := os.ReadFile(state.Provenance.Executable)
	if readErr != nil {
		return false
	}
	digest := sha256.Sum256(installedNow)
	return hex.EncodeToString(digest[:]) == current.SHA256
}

func (controller Controller) stopAndReplace(ctx context.Context, state daemon.State, listen string, current daemon.Provenance, action string, progress func(string)) (Result, error) {
	supervisor := controller.effectiveSupervisor(state)
	supervised := supervisor == daemon.SupervisorSystemd ||
		(supervisor == daemon.SupervisorLaunchd && state.SupervisorLabel != LaunchdLabel)
	if supervised {
		// A caller with a different wb binary than the one actually running —
		// a worktree build, or an older or newer installed CLI, reached
		// through an EXPLICIT `wb daemon restart` or the self-update hook
		// (an implicit `Start` never reaches this: see Start's own check,
		// above, which returns a live result with a warning instead
		// — sneat-dev/wb#622 review item 9) — must not restart a
		// production supervised hub on the strength of "my own binary
		// differs from its recorded provenance" alone: that is equally true
		// of an unrelated invocation. It is only safe to proceed when the
		// *installed* executable this record names has itself become our own
		// binary — the shape a genuine self-update produces, since it
		// replaces that same path in place (sneat-dev/wb#622 review item 6).
		if !daemonSupervisedInstalledBinaryMatches(state, current) {
			return Result{Action: action, Managed: true, State: PublicStateOf(state), ReportedState: string(state.Status)},
				fmt.Errorf(
					"refusing to touch the supervised daemon on %s: its recorded executable %s is not this build (this build is %s); leaving it running — if this is a genuine self-update, the installed executable at that path should already have this build's content",
					state.Listen, state.Provenance.Executable, current.Executable)
		}
	}
	var stopErr error
	controller.restartPhase(progress, fmt.Sprintf("draining daemon pid %d", state.PID), func() { _, stopErr = controller.stop(ctx, state) })
	if stopErr != nil {
		return Result{}, stopErr
	}
	if !supervised {
		stopped, _, err := controller.state.load()
		if err != nil {
			return Result{}, err
		}
		var result Result
		controller.restartPhase(progress, "starting replacement daemon", func() {
			result, err = controller.launch(ctx, &stopped, listen, current, action, true)
		})
		return result, err
	}
	var result Result
	var waitErr error
	controller.restartPhase(progress, "waiting for the supervisor to restart the daemon", func() {
		result, waitErr = controller.waitForSupervisorReplacement(ctx, supervisor, current, action)
	})
	return result, waitErr
}

func (controller Controller) waitForSupervisorReplacement(ctx context.Context, supervisor daemon.Supervisor, current daemon.Provenance, action string) (Result, error) {
	deadline := controller.deps.Now().Add(controller.bounds().SupervisorRestart)
	processStartTime := controller.deps.ProcessStartTime
	if processStartTime == nil {
		processStartTime = daemon.ProcessStartTime
	}
	for {
		select {
		case <-ctx.Done():
			return Result{}, ctx.Err()
		default:
		}
		state, found, err := controller.state.load()
		if err == nil && found && state.Status == daemon.StatusReady && state.PID > 0 && controller.deps.Alive(state.PID) {
			if !state.Provenance.SameBinary(current) {
				return Result{}, fmt.Errorf(
					"the %s supervisor brought back a different executable than this handoff targeted (recorded %s, this build %s); not adopting it — check what the supervisor unit is actually running",
					supervisor, state.Provenance.Executable, current.Executable)
			}
			observed, observedKnown := processStartTime(state.PID)
			if match, known := state.ProcessGenerationMatches(observed, observedKnown); !known || match {
				if controller.ownedHealth(ctx, state.Listen, state.PID, state.Queue.Generation) == nil {
					location, _ := ResolveLocation(controller.root)
					return Result{
						Action: action, Managed: true, ProcessManagerRunning: true, Reachable: true,
						DirectTransportReachable: true, ReachabilityTransport: "direct", ProvenanceMatches: true,
						State: PublicStateOf(state), AutomaticVersionHandoff: true, Identity: identityCurrent,
						ReadyVerified: true, ReportedState: string(state.Status),
						WBHome: location.Home, RuntimeDir: location.RuntimeDir, SocketPath: location.SocketPath, StatePath: location.StatePath,
					}, nil
				}
			}
		}
		if !controller.deps.Now().Before(deadline) {
			return Result{}, fmt.Errorf(
				"the %s supervisor did not restart the daemon with the new executable within %s; the previous process is stopped and no detached replacement was started — check the daemon's supervisor unit",
				supervisor, controller.bounds().SupervisorRestart)
		}
		controller.deps.Sleep(controller.bounds().SupervisorPoll)
	}
}

func (controller Controller) restartPhase(progress func(string), phase string, operation func()) {
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
	tickerFactory := controller.deps.RestartTicker
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
		return DefaultListen
	}
	return listen
}

func (controller Controller) Stop(ctx context.Context) (Result, error) {
	release, err := controller.lifecycleLock()
	if err != nil {
		return Result{}, err
	}
	defer release()
	state, found, err := controller.state.load()
	if err != nil {
		return Result{}, err
	}
	if !found || state.Status == daemon.StatusStopped || state.PID == 0 {
		return controller.withCurrentProvenance(Result{Action: "stop", Managed: found, State: PublicStateOf(state)})
	}
	result, err := controller.stop(ctx, state)
	if err != nil {
		return Result{}, err
	}
	return controller.withCurrentProvenance(result)
}

func (controller Controller) withCurrentProvenance(result Result) (Result, error) {
	if !result.Managed {
		return result, nil
	}
	current, err := controller.Provenance()
	if err != nil {
		return Result{}, err
	}
	result.ProvenanceMatches = result.State.Provenance.SameBinary(current)
	return result, nil
}

func (controller Controller) stop(_ context.Context, state daemon.State) (Result, error) {
	processStartTime := controller.deps.ProcessStartTime
	if processStartTime == nil {
		processStartTime = daemon.ProcessStartTime
	}
	if state.PID > 0 && controller.deps.Alive(state.PID) {
		observed, observedKnown := processStartTime(state.PID)
		if match, known := state.ProcessGenerationMatches(observed, observedKnown); known && !match {
			// The PID is someone else's now. Signalling it would signal an
			// unrelated process, which is the harm the recorded start time
			// exists to prevent; the record is retired instead.
			reason := fmt.Sprintf("PID %d now belongs to a different process; not signalling it", state.PID)
			state.MarkStoppedWithReason(reason, controller.deps.Now())
			if err := controller.state.save(state); err != nil {
				return Result{}, err
			}
			return Result{Action: "stop", Managed: true, State: PublicStateOf(state), Identity: identityProcessRecycled, IdentityDetail: reason, ReportedState: string(daemon.StatusStopped)}, nil
		}
	}
	state.MarkDraining(controller.deps.Now())
	if err := controller.state.save(state); err != nil {
		return Result{}, err
	}
	if !controller.deps.Alive(state.PID) {
		stopped, err := controller.markStoppedIfUnchanged(state, state.PID, state.OwnerToken)
		if err != nil {
			return Result{}, err
		}
		return Result{Action: "stop", Managed: true, State: PublicStateOf(stopped)}, nil
	}
	if err := controller.deps.Stop(state.PID, state.ReportedSupervisor(), state.SupervisorLabel); err != nil {
		return Result{}, fmt.Errorf("request daemon drain for pid %d: %w", state.PID, err)
	}
	deadline := controller.deps.Now().Add(controller.bounds().Stop)
	for controller.deps.Now().Before(deadline) {
		if !controller.deps.Alive(state.PID) {
			stopped, err := controller.markStoppedIfUnchanged(state, state.PID, state.OwnerToken)
			if err != nil {
				return Result{}, err
			}
			return Result{Action: "stop", Managed: true, State: PublicStateOf(stopped)}, nil
		}
		controller.deps.Sleep(50 * time.Millisecond)
	}
	return Result{}, fmt.Errorf("daemon pid %d did not stop within %s; it remains draining to preserve durable queue ownership", state.PID, controller.bounds().Stop)
}

func (controller Controller) markStoppedIfUnchanged(expected daemon.State, expectedPID int, expectedOwnerToken string) (daemon.State, error) {
	release, err := controller.AcquireStateLock()
	if err != nil {
		return daemon.State{}, err
	}
	defer release()
	current, found, err := controller.state.load()
	if err != nil {
		return daemon.State{}, err
	}
	if !found || current.PID != expectedPID || current.OwnerToken != expectedOwnerToken {
		// Something else already replaced this record; leave it alone.
		if found {
			return current, nil
		}
		return expected, nil
	}
	current.MarkStopped(controller.deps.Now())
	if err := controller.state.save(current); err != nil {
		return daemon.State{}, err
	}
	return current, nil
}

func (controller Controller) launch(ctx context.Context, previous *daemon.State, listen string, Provenance daemon.Provenance, action string, handoff bool) (Result, error) {
	if controller.deps.CheckOtherRoot != nil {
		if err := controller.deps.CheckOtherRoot(controller.root, controller.replaceOtherRoot); err != nil {
			return Result{}, err
		}
	}
	token, err := controller.deps.Token()
	if err != nil {
		return Result{}, err
	}
	location, err := ResolveLocation(controller.root)
	if err != nil {
		return Result{}, err
	}
	starting := daemon.NewStartingAt(previous, listen, Provenance, token, location.Home, location.StatePath, controller.deps.Now())
	if err := controller.state.save(starting); err != nil {
		return Result{}, err
	}
	// The supervisor unit is written from these arguments, so they carry only
	// the inputs the operator chose. The daemon resolves its own runtime
	// directory at startup: a path resolved *here* would be baked into the unit
	// and outlive the home it was resolved from, which is exactly how a daemon
	// ended up serving a directory the rest of WB had already abandoned.
	args := []string{"--projects-root", controller.root, "daemon", "serve", "--listen", listen, "--managed-start"}
	logPath, logErr := DaemonStartLogPath(controller.root)
	if logErr != nil {
		return Result{}, logErr
	}
	pid, err := controller.deps.Start(Provenance.Executable, args, logPath)
	if err != nil {
		starting.MarkStopped(controller.deps.Now())
		_ = controller.state.save(starting)
		return Result{}, err
	}
	releaseState, err := controller.AcquireStateLock()
	if err != nil {
		return Result{}, err
	}
	state, found, err := controller.state.load()
	if err != nil {
		releaseState()
		return Result{}, err
	}
	if !found || state.Status != daemon.StatusStarting || state.OwnerToken != token {
		releaseState()
		return Result{}, errors.New("daemon startup ownership changed before its process was recorded")
	}
	if state.PID != 0 && state.PID != pid {
		releaseState()
		return Result{}, fmt.Errorf("daemon startup recorded unexpected process %d instead of %d", state.PID, pid)
	}
	state.MarkStartingPID(pid, controller.deps.Now())
	if err := controller.state.save(state); err != nil {
		releaseState()
		return Result{}, err
	}
	releaseState()
	// The lifecycle controller is the only Starting -> Ready writer. The child
	// serves health while state remains Starting; this process still holds the
	// lifecycle lock, so recovery cannot race this compare-and-save transition.
	deadline := controller.deps.Now().Add(controller.bounds().Ready)
	for controller.deps.Now().Before(deadline) {
		if controller.ownedHealth(ctx, listen, pid, starting.Queue.Generation) != nil {
			controller.deps.Sleep(50 * time.Millisecond)
			continue
		}
		releaseState, lockErr := controller.AcquireStateLock()
		if lockErr != nil {
			return Result{}, lockErr
		}
		state, found, loadErr := controller.state.load()
		if loadErr != nil {
			releaseState()
			return Result{}, loadErr
		}
		if found && state.OwnerToken == token && state.Status == daemon.StatusStarting && state.PID == pid {
			state.MarkReadyWithProcess(pid, ProcessStartedAt(pid), controller.deps.Now())
			if err := controller.state.save(state); err != nil {
				releaseState()
				return Result{}, err
			}
			releaseState()
			return Result{Action: action, Managed: true, ProcessManagerRunning: true, Reachable: true, DirectTransportReachable: true, ReachabilityTransport: "direct", ProvenanceMatches: true, State: PublicStateOf(state), AutomaticVersionHandoff: handoff, Identity: identityCurrent, ReadyVerified: true, ReportedState: string(state.Status), WBHome: location.Home, RuntimeDir: location.RuntimeDir, SocketPath: location.SocketPath, StatePath: location.StatePath}, nil
		}
		releaseState()
		controller.deps.Sleep(50 * time.Millisecond)
	}
	// A child that could not bind or that lost its runtime directory records
	// why before exiting, so the reason is reported here instead of being
	// reduced to "did not become ready".
	if state, found, loadErr := controller.state.load(); loadErr == nil && found && state.StoppedReason != "" {
		return Result{Action: action, Managed: true, State: PublicStateOf(state), ReportedState: string(state.Status)}, fmt.Errorf("daemon did not become ready within %s: %s", controller.bounds().Ready, state.StoppedReason)
	}
	return Result{}, fmt.Errorf("daemon did not become ready within %s; inspect %s", controller.bounds().Ready, logPath)
}

func ProcessStartedAt(pid int) time.Time {
	return processStartedAtWithObserver(pid, daemon.ProcessStartTime)
}

func processStartedAtWithObserver(pid int, observe func(int) (time.Time, bool)) time.Time {
	started, ok := observe(pid)
	if !ok {
		return time.Time{}
	}
	return started
}

func RuntimeGuard(out io.Writer, ctx context.Context, address string, store daemon.Store, owned daemon.State, ownerToken string, tickerFactory func(time.Duration) (<-chan time.Time, func())) error {
	ticks, stop := tickerFactory(daemonHeartbeatInterval)
	defer stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticks:
			if err := RuntimeIntact(store); err != nil {
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

func RuntimeIntact(store daemon.Store) error {
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

func daemonOwnerToken() (string, error) {
	bytes := make([]byte, 16)
	// Go's crypto/rand.Read returns a full buffer or terminates the process.
	_, _ = rand.Read(bytes)
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

func RequireLoopbackAddress(address string) error {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("invalid --listen address %q: %w", address, err)
	}
	if !loopbackhost.Named(host) {
		return fmt.Errorf("--listen must use localhost or a loopback IP; publish it through an authenticated tunnel")
	}
	return nil
}

// LoadState reads the controller's resolved lifecycle record without starting it.
func (controller Controller) LoadState() (daemon.State, bool, error) { return controller.state.load() }
