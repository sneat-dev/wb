package main

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

	"github.com/spf13/cobra"
	"github.com/strongo/cli-helpers/daemonlifecycle"

	"github.com/sneat-dev/wb/hub/narrate"
	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/dashboard"
	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/gen/wb/daemon/v1/daemonv1connect"
	"github.com/sneat-dev/wb/internal/hubconfig"
	"github.com/sneat-dev/wb/internal/nodeidentity"
	"github.com/sneat-dev/wb/internal/peers"
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

// daemonLaunchdLabel is wb's own launch agent label (darwin only). It lives
// here, not in daemon_process_darwin.go, because platform-independent handoff
// logic (stopAndReplace) needs to tell wb's own self-managed launchd job —
// which it already knows how to re-bootstrap with a new binary — from a
// foreign one it must hand off to instead, on every platform this package
// builds for (sneat-dev/wb#622 review item 1).
const daemonLaunchdLabel = "dev.sneat.wb.daemon"

// daemonEnvVarsSuppliedBySupervisors lists the environment variables systemd
// and launchd are documented to set on a process they exec, so a detached
// child this build starts itself never inherits stale evidence of a
// supervision it does not actually have (sneat-dev/wb#622 review item 3).
var daemonEnvVarsSuppliedBySupervisors = map[string]bool{
	"INVOCATION_ID":    true,
	"SYSTEMD_EXEC_PID": true,
	"JOURNAL_STREAM":   true,
	"XPC_SERVICE_NAME": true,
}

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
func daemonRefuseTestBinary(executable string) error {
	if testing.Testing() || strings.HasSuffix(filepath.Base(executable), ".test") {
		return fmt.Errorf("refusing to install a supervisor job for, or launch, a Go test binary as the WB daemon (%s); this guard exists because a test previously overwrote a real launchd job and took a real daemon down", executable)
	}
	return nil
}

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
var runSystemctl = func(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), runSystemctlTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, "systemctl", args...) //nolint:gosec // fixed argv plus a configured/default unit name, no shell.
	command.WaitDelay = 2 * time.Second
	return command.CombinedOutput()
}

// runSystemctlTimeout bounds every runSystemctl invocation. It is a variable
// so a test can shrink it rather than waiting the real bound out.
var runSystemctlTimeout = 3 * time.Second

// daemonIsSystemRunningStates lists the `systemctl --user is-system-running`
// answers that mean the systemd user manager itself is genuinely present and
// answering, whatever shape its own units are currently in. Anything else —
// an empty answer (systemctl absent, or the command could not even run), a
// state this build does not recognize, or "offline" — counts as not present,
// which is the safer default for a check whose only job is to lift an
// otherwise-permanent refusal (sneat-dev/wb#622 review item 5: the previous
// version treated ANY non-empty, non-"offline" answer as present, which
// could not distinguish a real manager state from unrecognized noise).
var daemonIsSystemRunningStates = map[string]bool{
	"running":      true,
	"degraded":     true,
	"starting":     true,
	"initializing": true,
	"maintenance":  true,
	"stopping":     true,
}

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
func daemonSupervisorPresent(supervisor daemon.Supervisor, label string) (present bool, unitName string) {
	switch supervisor {
	case daemon.SupervisorSystemd:
		output, _ := runSystemctl("--user", "is-system-running")
		return daemonIsSystemRunningStates[strings.TrimSpace(string(output))], ""
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
		ctx, cancel := context.WithTimeout(context.Background(), runSystemctlTimeout)
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

// daemonDefaultSystemdUnit is the systemd user unit name `wb daemon status`
// checks for the sneat-dev/wb#617 detector when no override is configured.
const daemonDefaultSystemdUnit = "wb-daemon.service"

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
func daemonSystemdUnitName(getenv func(string) string) string {
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

// daemonObservedSystemdUnitState runs `systemctl --user show -p
// ActiveState,Result,NRestarts <unit>` and parses the result, so `wb daemon
// status` can compare a specific unit's own state against what the process
// actually answering the port recorded about its own start
// (sneat-dev/wb#622 review item 2). known=false on any query failure —
// systemctl entirely absent (exec.LookPath fails inside runSystemctl's
// default implementation), the user manager unreachable, or the unit simply
// not found — never treated as "the unit is healthy".
func daemonObservedSystemdUnitState(unit string) (daemon.SystemdUnitState, bool) {
	trimmedUnit := strings.TrimSpace(unit)
	if trimmedUnit == "" {
		return daemon.SystemdUnitState{}, false
	}
	output, err := runSystemctl("--user", "show", "-p", "ActiveState,Result,NRestarts", trimmedUnit)
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
		if found && daemonEnvVarsSuppliedBySupervisors[name] {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}

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
	// WebhookRedelivery is the missed-webhook recovery sweep's last completed
	// pass. Like RepositoriesPolled, it comes from the running daemon's
	// health endpoint, because only the serving process has it.
	WebhookRedelivery *daemonHubRedeliverySweep `json:"webhook_redelivery,omitempty"`
}

// daemonHubRedeliverySweep reports the missed-webhook recovery sweep's last
// completed pass in `wb daemon status`. LastSweepAt and LastFailureAt are
// pointers so JSON omits them before anything has happened yet.
type daemonHubRedeliverySweep struct {
	LastSweepAt      *time.Time `json:"last_sweep_at,omitempty"`
	Redelivered      int        `json:"redelivered"`
	Abandoned        int        `json:"abandoned"`
	Uncounted        int        `json:"uncounted"`
	LastFailureAt    *time.Time `json:"last_failure_at,omitempty"`
	LastFailureClass string     `json:"last_failure_class,omitempty"`
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
	// Supervisor is what the reporting record's process observed about its own
	// start (REQ: report-supervisor). It is always one of "systemd", "launchd",
	// or "none" — never blank — so a reader never has to guess what an absent
	// value means.
	Supervisor daemon.Supervisor `json:"supervisor,omitempty"`
	// SupervisorLabel is the supervisor's own identity for the job (currently
	// only a launchd job label). See daemon.State.SupervisorLabel.
	SupervisorLabel string `json:"supervisor_label,omitempty"`
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
		Supervisor: state.ReportedSupervisor(), SupervisorLabel: state.SupervisorLabel,
		Queue: daemonPublicQueue{SchemaVersion: state.Queue.SchemaVersion, Generation: state.Queue.Generation,
			Owner: state.Queue.Owner, HandoffFrom: state.Queue.HandoffFrom, HandoffAt: state.Queue.HandoffAt},
	}
}

type daemonDependencies struct {
	now        func() time.Time
	executable func() (string, error)
	start      func(string, []string, string) (int, error)
	alive      func(int) bool
	stop       func(pid int, supervisor daemon.Supervisor, supervisorLabel string) error
	sleep      func(time.Duration)
	// lockNow is a dedicated clock seam for stateLock's short retry deadline.
	// It must never be `now` (which the production default strips to a
	// UTC, non-monotonic reading via time.Time.UTC(), a wall-clock time that
	// an NTP step or a VM resume can jump under a waiter's feet). The default
	// is plain time.Now, whose monotonic reading makes the 5s deadline
	// immune to wall-clock adjustments, matching Go's own recommendation for
	// measuring elapsed time (see time.Since and the "Monotonic Clocks"
	// section of the time package doc).
	lockNow       func() time.Time
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
	// getenv, getpid, and getppid are the seams `daemon serve` reads a
	// supervisor's own evidence through (INVOCATION_ID, SYSTEMD_EXEC_PID,
	// XPC_SERVICE_NAME, and this process's own pid/ppid, which distinguish a
	// supervisor's own child from a process that merely inherited its
	// environment), so a test never needs a real systemd or launchd to
	// exercise supervisor detection.
	getenv  func(string) string
	getpid  func() int
	getppid func() int
	// observedSupervisor independently observes evidence about a running PID
	// that this build did not itself write, for `wb daemon status` to compare
	// against what that process recorded about its own start. See
	// internal/daemon.ObservedCgroupSupervisor.
	observedSupervisor func(int) (daemon.Supervisor, bool)
	// processStartTime observes a process's own start time, so a stale
	// hard-coded test PID cannot collide with a real, unrelated process this
	// build did not create — the default reads the real OS the way
	// daemon.ProcessStartTime already does; a test overrides it instead of
	// hoping its fake PIDs are never real ones (sneat-dev/wb#622 review: tests
	// must not depend on the real process table for correctness).
	processStartTime func(int) (time.Time, bool)
	// supervisorPresent independently checks whether the recorded supervisor
	// can still be shown to exist at all, so `wb daemon start`/`restart` do
	// not refuse forever on a stale record naming a supervisor that is long
	// gone (sneat-dev/wb#622 review item 4).
	supervisorPresent func(supervisor daemon.Supervisor, label string) (present bool, unitName string)
	// systemdUnitName and systemdUnitState are the sneat-dev/wb#617 detector's
	// own seams: systemdUnitName resolves which systemd user unit `wb daemon
	// status` checks (config, or daemonDefaultSystemdUnit), and
	// systemdUnitState queries that unit's own state directly
	// (`systemctl --user show`), independent of cgroup membership — the
	// detector that can see a live host's confirmed shape: an orphaned,
	// unsupervised daemon (recorded supervisor=none, a session scope — not
	// the unit's own cgroup) serving the port while its systemd unit sat
	// failed and crash-looping (sneat-dev/wb#622 review item 2).
	systemdUnitName  func() string
	systemdUnitState func(unit string) (daemon.SystemdUnitState, bool)
	// observedCgroupUnit lets `daemon serve` record the systemd unit it is
	// ACTUALLY running inside (from its own /proc/self/cgroup) at startup,
	// so a later `wb daemon status` invocation can prefer that over its own
	// configured/default guess — which is wrong whenever the status
	// invocation's own environment does not carry the same
	// WB_DAEMON_SYSTEMD_UNIT the daemon itself was supervised under
	// (sneat-dev/wb#622 review round 3, item M3).
	observedCgroupUnit func(pid int) (string, bool)
}

func defaultDaemonDependencies() daemonDependencies {
	return daemonDependencies{
		now:          func() time.Time { return time.Now().UTC() },
		lockNow:      time.Now,
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
		getenv:  os.Getenv,
		getpid:  os.Getpid,
		getppid: os.Getppid,
		observedSupervisor: func(pid int) (daemon.Supervisor, bool) {
			return daemon.ObservedCgroupSupervisor(pid, daemonSystemdUnitName(os.Getenv))
		},
		processStartTime:   daemon.ProcessStartTime,
		supervisorPresent:  daemonSupervisorPresent,
		systemdUnitName:    func() string { return daemonSystemdUnitName(os.Getenv) },
		systemdUnitState:   daemonObservedSystemdUnitState,
		observedCgroupUnit: daemon.ObservedCgroupUnit,
	}
}

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
				resolved, resolveErr := daemonStatePath(inv.projectsRoot)
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
			return serveDashboard(inv, command, deps, listenAddress, daemon.Store{Path: stateFile}, ownerToken, quiet, managedStart)
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

func newDaemonStartCmd(inv *invocation, deps daemonDependencies) *cobra.Command {
	var listen, format string
	var jsonOut, forceDetached bool
	command := &cobra.Command{Use: "start", Short: "Start the local WB daemon, or hand off to the installed WB binary", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon start: %s\n", phase)
			}
			result, err := newDaemonController(deps, inv.projectsRoot).StartWithProgress(command.Context(), listen, progress, forceDetached)
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().StringVar(&listen, "listen", daemonDefaultListen, "loopback listen address")
	command.Flags().BoolVar(&forceDetached, "force-detached", false, "start a detached daemon even though the runtime's recorded owner is a systemd or launchd supervisor")
	command.Flags().StringVar(&format, "format", "text", "stdout format: text or json")
	command.Flags().BoolVar(&jsonOut, "json", false, "shortcut for --format=json")
	return command
}

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
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon stop: this daemon is recorded as owned by a systemd user service; its Restart=always policy will bring it back — use `systemctl --user stop %s` to actually stop it\n", daemonSystemdUnitName(os.Getenv))
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
				if label != "" && label != daemonLaunchdLabel {
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
	var jsonOut, ifRunning, forceDetached bool
	command := &cobra.Command{Use: "restart", Short: "Drain, hand off the durable queue, and start the installed WB daemon", Args: cobra.NoArgs,
		RunE: func(command *cobra.Command, _ []string) error {
			format, err := daemonOutputFormat(format, jsonOut)
			if err != nil {
				return usageError(err.Error())
			}
			progress := func(phase string) {
				_, _ = fmt.Fprintf(command.ErrOrStderr(), "wb: daemon restart: %s\n", phase)
			}
			result, err := newDaemonController(deps, inv.projectsRoot).RestartWithProgress(command.Context(), ifRunning, progress, forceDetached)
			if err != nil {
				return err
			}
			return writeDaemonResult(command.OutOrStdout(), format, result)
		}}
	command.Flags().BoolVar(&ifRunning, "if-running", false, "succeed without starting when no managed daemon is running")
	command.Flags().BoolVar(&forceDetached, "force-detached", false, "start a detached daemon even though the runtime's recorded owner is a systemd or launchd supervisor")
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
	deadline := controller.deps.lockNow().Add(daemonReadyTimeout)
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
		if controller.deps.lockNow().After(deadline) {
			_ = file.Close()
			return nil, fmt.Errorf("another process held the daemon state lock for %s", daemonReadyTimeout)
		}
		controller.deps.sleep(20 * time.Millisecond)
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
	return controller.writeLifecycleOwnerPIDInjected(pid, nil)
}

// writeLifecycleOwnerPIDInjected is writeLifecycleOwnerPID's test seam
// (task-9 PR-2): every production call site reaches it only through
// writeLifecycleOwnerPID, which always passes a nil *filewrite.Injector,
// so production behaviour is unchanged; a test passes its own Injector
// directly to reach a create/chmod/write/sync/close/rename failure
// branch deterministically.
func (controller daemonController) writeLifecycleOwnerPIDInjected(pid int, inj *filewrite.Injector) error {
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
	if err := daemonlifecycle.ProtectOwnerOnly(temporaryName); err != nil {
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
		if recorded == daemon.SupervisorNone && controller.deps.systemdUnitState != nil && controller.deps.systemdUnitName != nil {
			// Prefer the daemon's OWN observed unit (State.SystemdUnit,
			// recorded from its own /proc/self/cgroup at serve startup) over
			// this invocation's own configured/default guess: a status
			// invocation run without the same WB_DAEMON_SYSTEMD_UNIT the
			// daemon itself was supervised under would otherwise query the
			// WRONG unit and see a false "no systemd service membership"
			// (sneat-dev/wb#622 review round 3, item M3).
			unit := state.SystemdUnit
			if strings.TrimSpace(unit) == "" {
				unit = controller.deps.systemdUnitName()
			}
			if unitState, known := controller.deps.systemdUnitState(unit); known && daemon.SystemdUnitLooksOrphaned(unitState) {
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
		if result.SupervisorMismatch == "" && controller.deps.observedSupervisor != nil {
			if observed, known := controller.deps.observedSupervisor(state.PID); known {
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
	if live.WebhookRedelivery != nil {
		status.WebhookRedelivery = &daemonHubRedeliverySweep{
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
	return controller.StartWithProgress(ctx, listen, nil, false)
}

func (controller daemonController) StartWithProgress(ctx context.Context, listen string, progress func(string), forceDetached bool) (daemonResult, error) {
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
			"refusing to start a daemon while a daemon still serves the legacy runtime directory: %s; stop that daemon first (wb daemon stop, with the --projects-root it was started under) rather than letting it keep serving an abandoned home",
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
	if found && identity == identityCurrent && alive && state.Status == daemon.StatusReady {
		if state.Listen != listen {
			// The daemon IS running, on a different --listen than requested —
			// never phrased as "if it is not running" (sneat-dev/wb#622
			// review item 15), and refused/allowed the same way as any other
			// live-under-a-supervisor case below.
			if refusal := controller.refuseDetachedStartUnderSupervisor(state, found, true, listen, forceDetached); refusal != "" {
				return daemonResult{Action: "start", Managed: true, State: publicDaemonState(state), Identity: identity, IdentityDetail: identityDetail}, errors.New(refusal)
			}
		} else if state.Provenance.SameBinary(current) {
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
		} else {
			// A live daemon of a different binary is an executable-handoff
			// path: when it is supervised, the supervisor restarts it with
			// the new binary rather than this process starting a detached
			// replacement behind the supervisor's back (sneat-dev/wb#617).
			supervisor := controller.effectiveSupervisor(state)
			supervised := supervisor == daemon.SupervisorSystemd ||
				(supervisor == daemon.SupervisorLaunchd && state.SupervisorLabel != daemonLaunchdLabel)
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
				result := daemonResult{
					Action: "start", Managed: true, ProcessManagerRunning: true,
					State: publicDaemonState(state), Identity: identity, IdentityDetail: identityDetail,
					ReportedState: string(state.Status),
					Warning: fmt.Sprintf(
						"the running supervised daemon's executable (%s) does not match this invocation's own binary (%s); leaving it running rather than restarting it — this invocation is not the one that should manage its lifecycle",
						state.Provenance.Executable, current.Executable),
				}
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
			return controller.stopAndReplace(ctx, state, listen, current, "start", progress)
		}
	}
	// Nothing alive to hand off from. A runtime this build's own records show
	// as owned by a supervisor must be started through that supervisor, not by
	// a detached process this command would spawn itself.
	if refusal := controller.refuseDetachedStartUnderSupervisor(state, found, false, listen, forceDetached); refusal != "" {
		return daemonResult{Action: "start", Managed: found, State: publicDaemonState(state)}, errors.New(refusal)
	}
	return controller.launch(ctx, optionalDaemonState(state, found), listen, current, "start", found)
}

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
// deps.supervisorPresent whether the recorded supervisor can still be shown
// to exist at all, and does not refuse when it cannot. forceDetached is the
// explicit escape hatch for the cases that check cannot resolve either way.
func (controller daemonController) refuseDetachedStartUnderSupervisor(state daemon.State, found, alive bool, requestedListen string, forceDetached bool) string {
	if !found || forceDetached {
		return ""
	}
	supervisor := state.ReportedSupervisor()
	if supervisor == daemon.SupervisorNone {
		return ""
	}
	if supervisor == daemon.SupervisorLaunchd && state.SupervisorLabel == daemonLaunchdLabel {
		// wb's own self-managed launchd job: `wb daemon start` IS how this
		// gets (re)started from cold on a mac (bootstrap+kickstart), so there
		// is no separate supervisor to defer to (sneat-dev/wb#622 review item
		// 1's shape, applied to the refusal path too — without this, a
		// crashed mac daemon could never be started by `wb daemon start`
		// again).
		return ""
	}
	present, unitName := true, ""
	if controller.deps.supervisorPresent != nil {
		present, unitName = controller.deps.supervisorPresent(supervisor, state.SupervisorLabel)
	}
	if !present {
		return ""
	}
	situation := fmt.Sprintf("this runtime's last recorded owner (as of %s)", state.UpdatedAt.UTC().Format(time.RFC3339))
	if alive {
		situation = fmt.Sprintf("this runtime is already running (pid %d, listening on %s rather than the requested %s), and its recorded owner", state.PID, state.Listen, requestedListen)
	}
	switch supervisor {
	case daemon.SupervisorSystemd:
		name := unitName
		if strings.TrimSpace(name) == "" {
			name = daemonSystemdUnitName(os.Getenv)
		}
		return fmt.Sprintf(
			"refusing to start a detached daemon: %s is a systemd user service; restart it through that supervisor instead of `wb daemon start` — e.g. `systemctl --user restart %s` — or pass --force-detached to override",
			situation, name)
	case daemon.SupervisorLaunchd:
		label := unitName
		if strings.TrimSpace(label) == "" {
			label = state.SupervisorLabel
		}
		if strings.TrimSpace(label) == "" {
			label = daemonLaunchdLabel
		}
		return fmt.Sprintf(
			"refusing to start a detached daemon: %s is a launchd agent; restart it through that supervisor instead of `wb daemon start` — e.g. `launchctl kickstart -k gui/%d/%s` — or pass --force-detached to override",
			situation, os.Getuid(), label)
	default:
		return ""
	}
}

func optionalDaemonState(state daemon.State, found bool) *daemon.State {
	if !found {
		return nil
	}
	return &state
}

func (controller daemonController) Restart(ctx context.Context, ifRunning bool) (daemonResult, error) {
	return controller.RestartWithProgress(ctx, ifRunning, nil, false)
}

func (controller daemonController) RestartWithProgress(ctx context.Context, ifRunning bool, progress func(string), forceDetached bool) (daemonResult, error) {
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
		// Nothing alive to hand off from. A runtime this build's own records
		// show as owned by a supervisor must be started through that
		// supervisor, not by a detached process this command would spawn.
		if refusal := controller.refuseDetachedStartUnderSupervisor(state, found, false, daemonListenOrDefault(state.Listen), forceDetached); refusal != "" {
			return daemonResult{Action: "restart", Managed: found, State: publicDaemonState(state)}, errors.New(refusal)
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
	current, err := controller.provenance()
	if err != nil {
		return daemonResult{}, err
	}
	return controller.stopAndReplace(ctx, state, daemonListenOrDefault(state.Listen), current, "restart", progress)
}

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
func (controller daemonController) effectiveSupervisor(state daemon.State) daemon.Supervisor {
	recorded := state.ReportedSupervisor()
	if recorded != daemon.SupervisorNone {
		return recorded
	}
	if state.PID <= 0 || controller.deps.observedSupervisor == nil {
		return recorded
	}
	if observed, known := controller.deps.observedSupervisor(state.PID); known && observed == daemon.SupervisorSystemd {
		return daemon.SupervisorSystemd
	}
	return recorded
}

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
func daemonSupervisedInstalledBinaryMatches(state daemon.State, current daemon.Provenance) bool {
	installedNow, readErr := os.ReadFile(state.Provenance.Executable)
	if readErr != nil {
		return false
	}
	digest := sha256.Sum256(installedNow)
	return hex.EncodeToString(digest[:]) == current.SHA256
}

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
// daemonLaunchdLabel) is deliberately NOT treated as "supervised" here: it is
// wb's own re-bootstrap-and-kickstart cycle (cmd/wb/daemon_process_darwin.go)
// that already correctly restarts it with a new binary, and there is no
// separate external supervisor to hand off to (sneat-dev/wb#622 review item
// 1 — every mac daemon's own launchd job otherwise satisfied Supervisor ==
// launchd, which made every mac restart, including the self-update hook,
// bootout the daemon's own job and then wait forever for a replacement
// nothing would ever start).
func (controller daemonController) stopAndReplace(ctx context.Context, state daemon.State, listen string, current daemon.Provenance, action string, progress func(string)) (daemonResult, error) {
	supervisor := controller.effectiveSupervisor(state)
	supervised := supervisor == daemon.SupervisorSystemd ||
		(supervisor == daemon.SupervisorLaunchd && state.SupervisorLabel != daemonLaunchdLabel)
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
			return daemonResult{Action: action, Managed: true, State: publicDaemonState(state), ReportedState: string(state.Status)},
				fmt.Errorf(
					"refusing to touch the supervised daemon on %s: its recorded executable %s is not this build (this build is %s); leaving it running — if this is a genuine self-update, the installed executable at that path should already have this build's content",
					state.Listen, state.Provenance.Executable, current.Executable)
		}
	}
	var stopErr error
	controller.restartPhase(progress, fmt.Sprintf("draining daemon pid %d", state.PID), func() { _, stopErr = controller.stop(ctx, state) })
	if stopErr != nil {
		return daemonResult{}, stopErr
	}
	if !supervised {
		stopped, _, err := controller.store.Load()
		if err != nil {
			return daemonResult{}, err
		}
		var result daemonResult
		controller.restartPhase(progress, "starting replacement daemon", func() {
			result, err = controller.launch(ctx, &stopped, listen, current, action, true)
		})
		return result, err
	}
	var result daemonResult
	var waitErr error
	controller.restartPhase(progress, "waiting for the supervisor to restart the daemon", func() {
		result, waitErr = controller.waitForSupervisorReplacement(ctx, supervisor, current, action)
	})
	return result, waitErr
}

// daemonSupervisorRestartTimeout bounds how long stopAndReplace waits for a
// supervisor to bring the daemon back after stopping it. It is a variable so
// a test can shrink it rather than waiting the real bound out.
var daemonSupervisorRestartTimeout = 30 * time.Second

// daemonSupervisorPollInterval is how often waitForSupervisorReplacement polls
// the lifecycle record while waiting. It is a variable for the same reason.
var daemonSupervisorPollInterval = 200 * time.Millisecond

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
func (controller daemonController) waitForSupervisorReplacement(ctx context.Context, supervisor daemon.Supervisor, current daemon.Provenance, action string) (daemonResult, error) {
	deadline := controller.deps.now().Add(daemonSupervisorRestartTimeout)
	processStartTime := controller.deps.processStartTime
	if processStartTime == nil {
		processStartTime = daemon.ProcessStartTime
	}
	for {
		select {
		case <-ctx.Done():
			return daemonResult{}, ctx.Err()
		default:
		}
		state, found, err := controller.store.Load()
		if err == nil && found && state.Status == daemon.StatusReady && state.PID > 0 && controller.deps.alive(state.PID) {
			if !state.Provenance.SameBinary(current) {
				return daemonResult{}, fmt.Errorf(
					"the %s supervisor brought back a different executable than this handoff targeted (recorded %s, this build %s); not adopting it — check what the supervisor unit is actually running",
					supervisor, state.Provenance.Executable, current.Executable)
			}
			observed, observedKnown := processStartTime(state.PID)
			if match, known := state.ProcessGenerationMatches(observed, observedKnown); !known || match {
				if controller.ownedHealth(ctx, state.Listen, state.PID, state.Queue.Generation) == nil {
					location, _ := resolveDaemonLocation(controller.root)
					return daemonResult{
						Action: action, Managed: true, ProcessManagerRunning: true, Reachable: true,
						DirectTransportReachable: true, ReachabilityTransport: "direct", ProvenanceMatches: true,
						State: publicDaemonState(state), AutomaticVersionHandoff: true, Identity: identityCurrent,
						ReadyVerified: true, ReportedState: string(state.Status),
						WBHome: location.Home, RuntimeDir: location.RuntimeDir, SocketPath: location.SocketPath, StatePath: location.StatePath,
					}, nil
				}
			}
		}
		if !controller.deps.now().Before(deadline) {
			return daemonResult{}, fmt.Errorf(
				"the %s supervisor did not restart the daemon with the new executable within %s; the previous process is stopped and no detached replacement was started — check the daemon's supervisor unit",
				supervisor, daemonSupervisorRestartTimeout)
		}
		controller.deps.sleep(daemonSupervisorPollInterval)
	}
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
	processStartTime := controller.deps.processStartTime
	if processStartTime == nil {
		processStartTime = daemon.ProcessStartTime
	}
	if state.PID > 0 && controller.deps.alive(state.PID) {
		observed, observedKnown := processStartTime(state.PID)
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
		stopped, err := controller.markStoppedIfUnchanged(state, state.PID, state.OwnerToken)
		if err != nil {
			return daemonResult{}, err
		}
		return daemonResult{Action: "stop", Managed: true, State: publicDaemonState(stopped)}, nil
	}
	if err := controller.deps.stop(state.PID, state.ReportedSupervisor(), state.SupervisorLabel); err != nil {
		return daemonResult{}, fmt.Errorf("request daemon drain for pid %d: %w", state.PID, err)
	}
	deadline := controller.deps.now().Add(daemonStopTimeout)
	for controller.deps.now().Before(deadline) {
		if !controller.deps.alive(state.PID) {
			stopped, err := controller.markStoppedIfUnchanged(state, state.PID, state.OwnerToken)
			if err != nil {
				return daemonResult{}, err
			}
			return daemonResult{Action: "stop", Managed: true, State: publicDaemonState(stopped)}, nil
		}
		controller.deps.sleep(50 * time.Millisecond)
	}
	return daemonResult{}, fmt.Errorf("daemon pid %d did not stop within %s; it remains draining to preserve durable queue ownership", state.PID, daemonStopTimeout)
}

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
func (controller daemonController) markStoppedIfUnchanged(expected daemon.State, expectedPID int, expectedOwnerToken string) (daemon.State, error) {
	release, err := controller.stateLock()
	if err != nil {
		return daemon.State{}, err
	}
	defer release()
	current, found, err := controller.store.Load()
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
	current.MarkStopped(controller.deps.now())
	if err := controller.store.Save(current); err != nil {
		return daemon.State{}, err
	}
	return current, nil
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

func serveDashboard(inv *invocation, command *cobra.Command, deps daemonDependencies, address string, store daemon.Store, ownerToken string, quiet, managedStart bool) (serveErr error) {
	location, err := resolveDaemonLocation(inv.projectsRoot)
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
	provenance, err := newDaemonController(deps, inv.projectsRoot).provenance()
	if err != nil {
		_ = listener.Close()
		return err
	}
	controller := newDaemonController(deps, inv.projectsRoot)
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
	// Supervisor detection reads this process's own environment (and its own
	// pid/ppid): only the process a supervisor actually exec'd sees the
	// variables it sets, so this must run in the child, not be inferred later
	// by a reader in a different process (sneat-dev/wb#617).
	getpid, getppid := deps.getpid, deps.getppid
	if getpid == nil {
		getpid = os.Getpid
	}
	if getppid == nil {
		getppid = os.Getppid
	}
	supervisorKind, supervisorExecPID, supervisorLabel := daemon.DetectSupervisor(deps.getenv, getpid(), getppid())
	// Recorded ALONGSIDE detection, in the same process, for the same reason:
	// only this process can read its own /proc/self/cgroup, and a later, separate
	// `wb daemon status` invocation has no portable way to ask for anyone
	// else's (sneat-dev/wb#622 review round 3, item M3). Best-effort: an
	// empty result (this platform's check is not implemented, or this
	// process is not in a service unit's own cgroup) leaves the field empty,
	// and a reader falls back to its own configured/default guess.
	observedCgroupUnit := deps.observedCgroupUnit
	if observedCgroupUnit == nil {
		observedCgroupUnit = daemon.ObservedCgroupUnit
	}
	systemdUnit, _ := observedCgroupUnit(getpid())
	if !managedStart {
		state = daemon.NewStartingAt(optionalDaemonState(state, found), address, provenance, ownerToken, location.Home, location.StatePath, deps.now())
		state.Supervisor, state.SupervisorExecPID, state.SupervisorLabel = supervisorKind, supervisorExecPID, supervisorLabel
		state.SystemdUnit = systemdUnit
		state.MarkReadyWithProcess(os.Getpid(), daemonProcessStartedAt(os.Getpid()), deps.now())
	} else {
		state.WBHome, state.StatePath = location.Home, location.StatePath
		state.Supervisor, state.SupervisorExecPID, state.SupervisorLabel = supervisorKind, supervisorExecPID, supervisorLabel
		state.SystemdUnit = systemdUnit
		state.MarkStartingPID(os.Getpid(), deps.now())
	}
	if err := store.Save(state); err != nil {
		releaseState()
		_ = listener.Close()
		return err
	}
	releaseState()
	localListener, err := listenDaemonLocal(inv.projectsRoot)
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
	operationsDirectory, err := daemon.OperationsDir(inv.projectsRoot)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("resolve daemon operation store: %w", err)
	}
	queue, err := daemon.NewService(inv.projectsRoot, operationsDirectory, collectVersion().Version, fmt.Sprint(state.Queue.Generation), func() error {
		return daemon.RequireRawExecutionPolicy(rawExecutionPolicyPath, inv.projectsRoot)
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
	logPath, _ := daemonStartLogPath(inv.projectsRoot)
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
	server := &http.Server{Handler: dashboard.NewHandler(dashboard.Options{
		ProjectsRoot: inv.projectsRoot, Version: collectVersion().Version,
		DaemonPID: os.Getpid(), SchedulerGeneration: state.Queue.Generation,
		Mounts: mount.handlers(), Hub: mount.hubHealth(), LogPath: logPath,
		Peers: peersHandler,
	}), ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	rpcPath, rpcHandler := daemonv1connect.NewDaemonServiceHandler(queue)
	rpcMux := http.NewServeMux()
	rpcMux.Handle(rpcPath, authenticatedDaemonHandler(ownerToken, rpcHandler))
	// The peer admin routes share the same owner-token authentication and the
	// same unix-socket listener, and are therefore never reachable over the
	// TCP dashboard listener (peer-connectivity#req:admin-requires-owner-
	// credential).
	rpcMux.Handle(peersRPCPrefix, authenticatedDaemonHandler(ownerToken, newPeerAdminHTTPHandler(mount)))
	fileBridge, err := newDaemonFileBridgeServer(inv.projectsRoot, ownerToken, fmt.Sprint(state.Queue.Generation), rpcMux)
	if err != nil {
		_ = listener.Close()
		return fmt.Errorf("prepare daemon file bridge: %w", err)
	}
	rpcServer := &http.Server{Handler: rpcMux, ReadHeaderTimeout: 5 * time.Second, IdleTimeout: 60 * time.Second}
	ctx, stop := signalDaemonContext(command.Context())
	defer stop()
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
		_ = listener.Close()
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
		if err := daemonRuntimeGuard(command.ErrOrStderr(), ctx, address, store, state, ownerToken); err != nil {
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
