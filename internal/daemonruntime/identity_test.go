package daemonruntime

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

func TestDaemonRuntimeLocationFollowsTheHome(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	home, err := wbhome.Root(root)
	if err != nil {
		t.Fatal(err)
	}

	location, err := ResolveLocation(root)
	if err != nil {
		t.Fatal(err)
	}
	if !daemonSamePath(location.Home, home) {
		t.Fatalf("resolved home = %s, want %s", location.Home, home)
	}
	if !daemonSamePath(location.RuntimeDir, filepath.Join(home, daemon.RuntimeDirName)) {
		t.Fatalf("runtime directory = %s, want a child of %s", location.RuntimeDir, home)
	}
	if filepath.Dir(location.SocketPath) != location.RuntimeDir || filepath.Dir(location.StatePath) != location.RuntimeDir {
		t.Fatalf("socket %s and state %s must both live in %s", location.SocketPath, location.StatePath, location.RuntimeDir)
	}
	// A different projects root resolves a different home and runtime, so the
	// daemon follows the root rather than a machine-wide default.
	otherRoot := daemonTestRoot(t)
	otherHome, err := wbhome.Root(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	otherLocation, err := ResolveLocation(otherRoot)
	if err != nil {
		t.Fatal(err)
	}
	if !daemonSamePath(otherLocation.Home, otherHome) || daemonSamePath(otherLocation.RuntimeDir, location.RuntimeDir) {
		t.Fatalf("runtime %q / home %q did not follow projects root %q", otherLocation.RuntimeDir, otherLocation.Home, otherRoot)
	}
	// A reader can name the endpoint without starting anything, and status
	// reports exactly that endpoint.
	result, err := NewController(daemonTestDependencies(t, root), root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !daemonSamePath(result.SocketPath, location.SocketPath) || !daemonSamePath(result.RuntimeDir, location.RuntimeDir) {
		t.Fatalf("status reported runtime %q and socket %q, want %q and %q", result.RuntimeDir, result.SocketPath, location.RuntimeDir, location.SocketPath)
	}
}

func TestDaemonSocketPathIsTheEndpointTheDaemonBinds(t *testing.T) {
	t.Parallel()
	if daemonLocalNetwork != "unix" {
		t.Skip("the local endpoint is not a filesystem socket on this platform")
	}
	// The local endpoint is a filesystem socket with a platform length limit,
	// so this fixture lives directly under /tmp rather than in the longer
	// per-test temporary directory.
	root, err := os.MkdirTemp("/tmp", "wb-sock-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	listener, err := ListenLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address, err := daemonLocalAddress(root)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(address)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSocket == 0 {
		t.Fatalf("%s is not a socket", address)
	}
	if permissions := info.Mode().Perm(); permissions != 0o600 {
		t.Fatalf("socket permissions = %o", permissions)
	}
}

func TestDaemonStatusDoesNotFoldAProvenanceMismatchIntoReady(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, StatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 900 }
	deps.Health = func(context.Context, string) error { return nil }

	result, err := NewController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity != identityCurrent {
		t.Fatalf("identity = %q (%s)", result.Identity, result.IdentityDetail)
	}
	if result.ProvenanceMatches {
		t.Fatal("the fixture record was expected to differ from this build")
	}
	if result.ReadyVerified || result.ReportedState != "unverified" {
		t.Fatalf("a build mismatch was folded into ready: %#v", result)
	}
	if !result.Reachable {
		t.Fatal("reachability must stay reportable on its own")
	}
}

func TestDaemonStatusReportsAnUnrecordedRecordAsUnverified(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	current, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemon.NewStartingAt(nil, DefaultListen, current, "owner", "", "", time.Now())
	state.MarkReady(900, time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 900 }

	result, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity != identityUnrecorded || result.ReadyVerified || result.ReportedState != "unverified" {
		t.Fatalf("unrecorded record status = %#v", result)
	}
	if !strings.Contains(result.IdentityDetail, "restart the daemon") {
		t.Fatalf("identity detail = %q", result.IdentityDetail)
	}
}

func TestDaemonStatusRefusesARecordThatNamesAnotherStatePath(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := NewController(deps, root)
	current, err := controller.Provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, DefaultListen, current, "owner", time.Now())
	state.StatePath = filepath.Join(string(filepath.Separator), "elsewhere", "daemon-state.json")
	state.MarkReady(900, time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	deps.Alive = func(pid int) bool { return pid == 900 }

	result, err := controller.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity != identityForeignStatePath || result.ReadyVerified || result.ReportedState != "unverified" {
		t.Fatalf("status = %#v", result)
	}
	if !strings.Contains(result.IdentityDetail, "elsewhere") {
		t.Fatalf("identity detail = %q", result.IdentityDetail)
	}
}

//nolint:paralleltest // This native fixture or its helper changes process-wide HOME, PATH or supervisor environment; testing restores it.
func TestDaemonStartRefusesWhileADaemonServesTheLegacyRuntimeDirectory(t *testing.T) {
	root := daemonTestRoot(t)
	// The current home is <root>/.wb now, so the leftover daemon is the one
	// still serving the retired default state home $HOME/.wb.
	legacyDir := daemonLegacyFixture(t)
	deps := daemonTestDependencies(t, root)
	deps.Alive = func(pid int) bool { return pid == 4321 }
	legacy := daemon.NewStartingAt(nil, DefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "legacy-owner", "", "", time.Now())
	legacy.MarkReady(4321, time.Now())
	if err := (daemon.Store{Path: daemonLegacyStatePath(legacyDir)}).Save(legacy); err != nil {
		t.Fatal(err)
	}
	before := daemonTestDirectorySnapshot(t, legacyDir)
	deps.Start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	result, err := NewController(deps, root).Start(context.Background(), DefaultListen)
	if err == nil {
		t.Fatal("starting while the legacy endpoint serves must be refused")
	}
	for _, want := range []string{legacyDir, "4321", "legacy"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("refusal %q does not name %q", err.Error(), want)
		}
	}
	if result.LegacyRuntime == nil || !result.LegacyRuntime.StateLive || result.LegacyRuntime.StatePID != 4321 {
		t.Fatalf("refusal did not report the legacy endpoint: %#v", result.LegacyRuntime)
	}
	if after := daemonTestDirectorySnapshot(t, legacyDir); after != before {
		t.Fatalf("the legacy runtime directory was disturbed:\nbefore %s\nafter  %s", before, after)
	}
	if _, err := os.Stat(mustDaemonPath(t, StatePath, root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused start wrote a record into the current home: %v", err)
	}
}

func TestDaemonStopsAndSaysWhyWhenItsRuntimeDirectoryVanishes(t *testing.T) {
	t.Parallel()
	guardInterval := 10 * time.Second
	guardTicker := func(time.Duration) (<-chan time.Time, func()) {
		ticker := time.NewTicker(guardInterval)
		return ticker.C, ticker.Stop
	}

	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", time.Now())
	state.MarkReadyWithProcess(os.Getpid(), ProcessStartedAt(os.Getpid()), time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Dir(controller.store.Path)
	if err := os.RemoveAll(runtimeDir); err != nil {
		t.Fatal(err)
	}
	previous := guardInterval
	guardInterval = time.Millisecond
	t.Cleanup(func() { guardInterval = previous })

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	err := RuntimeGuard(&out, ctx, DefaultListen, controller.store, state, "owner-token", guardTicker)
	if err == nil {
		t.Fatal("a daemon whose runtime directory vanished must stop")
	}
	if !strings.Contains(err.Error(), runtimeDir) || !strings.Contains(out.String(), "daemon stopping:") {
		t.Fatalf("stop was not explained: err=%v out=%q", err, out.String())
	}
	// The reason is recorded where a later reader finds it, even though that
	// means recreating the directory the daemon just lost: a supervisor
	// restart must not be able to erase the only evidence of it.
	stored, found, loadErr := controller.store.Load()
	if loadErr != nil || !found || stored.StoppedReason == "" || stored.Status != daemon.StatusStopped {
		t.Fatalf("stored stop reason = %#v, %t, %v", stored, found, loadErr)
	}
	if !strings.Contains(stored.StoppedReason, runtimeDir) {
		t.Fatalf("recorded reason %q does not name %s", stored.StoppedReason, runtimeDir)
	}
}

func TestDaemonRuntimeGuardKeepsHeartbeatingWhileItsDirectoryExists(t *testing.T) {
	t.Parallel()
	guardInterval := 10 * time.Second
	guardTicker := func(time.Duration) (<-chan time.Time, func()) {
		ticker := time.NewTicker(guardInterval)
		return ticker.C, ticker.Stop
	}

	root := daemonTestRoot(t)
	controller := NewController(daemonTestDependencies(t, root), root)
	state := daemonTestState(t, root, DefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	previous := guardInterval
	guardInterval = time.Millisecond
	t.Cleanup(func() { guardInterval = previous })

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	t.Cleanup(cancel)
	if err := RuntimeGuard(&out, ctx, DefaultListen, controller.store, state, "owner-token", guardTicker); err != nil {
		t.Fatalf("an intact runtime directory must not stop the daemon: %v", err)
	}
	if !strings.Contains(out.String(), "daemon heartbeat: ready") {
		t.Fatalf("heartbeat log = %q", out.String())
	}
}

func TestDaemonStartPersistsNoResolvedRuntimePath(t *testing.T) {
	t.Parallel()
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	original := deps.Start
	var args []string
	var logPath string
	deps.Start = func(executable string, launched []string, log string) (int, error) {
		args, logPath = append([]string(nil), launched...), log
		return original(executable, launched, log)
	}
	if _, err := NewController(deps, root).Start(context.Background(), DefaultListen); err != nil {
		t.Fatal(err)
	}
	if !daemonTestContains(args, "--managed-start") {
		t.Fatalf("daemon start args = %v, want --managed-start", args)
	}
	runtimeDir := mustDaemonPath(t, func(root string) (string, error) { return daemon.RuntimeDir(root) }, root)
	for _, argument := range args {
		if strings.Contains(argument, runtimeDir) || strings.HasSuffix(argument, daemon.StateFileName) {
			t.Fatalf("daemon start pinned a resolved runtime path: %v", args)
		}
	}
	// A supervisor that records the log path must not record a home-derived
	// one, or a later home move leaves the unit writing into an abandoned
	// directory. Where no unit records it, the log is the daemon's own runtime
	// log and the launcher opens it.
	if daemonSupervisorRecordsStartLog() {
		if strings.Contains(logPath, runtimeDir) {
			t.Fatalf("supervisor log %q would pin the runtime directory in a unit", logPath)
		}
	} else if !strings.Contains(logPath, runtimeDir) {
		t.Fatalf("log %q is neither recorded by a supervisor nor the runtime log", logPath)
	}
}
