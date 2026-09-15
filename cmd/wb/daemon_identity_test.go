package main

import (
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"

	"github.com/sneat-dev/wb/internal/daemon"
	"github.com/sneat-dev/wb/internal/wbhome"
)

// AC: runtime-moves-with-the-home
func TestDaemonRuntimeLocationFollowsTheHome(t *testing.T) {
	root := daemonTestRoot(t)
	home := t.TempDir()
	t.Setenv(wbhome.EnvOverride, home)

	location, err := resolveDaemonLocation(root)
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
	// A reader can name the endpoint without starting anything, and status
	// reports exactly that endpoint.
	result, err := newDaemonController(daemonTestDependencies(t, root), root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !daemonSamePath(result.SocketPath, location.SocketPath) || !daemonSamePath(result.RuntimeDir, location.RuntimeDir) {
		t.Fatalf("status reported runtime %q and socket %q, want %q and %q", result.RuntimeDir, result.SocketPath, location.RuntimeDir, location.SocketPath)
	}
}

// AC: runtime-moves-with-the-home (the reported socket path is the bound one)
func TestDaemonSocketPathIsTheEndpointTheDaemonBinds(t *testing.T) {
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
	pinDaemonHome(t, root)
	listener, err := listenDaemonLocal(root)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = listener.Close() }()
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

// AC: status-cannot-be-fooled-by-a-stranger
func TestDaemonStatusReportsAReachableDaemonFromAnotherHome(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.WBHome = filepath.Join(string(filepath.Separator), "an", "abandoned", "home")
	state.MarkReady(900, time.Now())
	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 900 }
	deps.health = func(context.Context, string) error { return nil }

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Identity != identityForeignHome {
		t.Fatalf("identity = %q, want %q (%s)", result.Identity, identityForeignHome, result.IdentityDetail)
	}
	if result.ReadyVerified || result.ReportedState != "unverified" {
		t.Fatalf("foreign daemon reported ready: %#v", result)
	}
	if !result.Reachable || result.ProcessManagerRunning {
		t.Fatalf("reachability and ownership were folded together: %#v", result)
	}
	for _, want := range []string{"an/abandoned/home", result.WBHome} {
		if !strings.Contains(result.IdentityDetail, want) {
			t.Fatalf("identity detail %q does not name %q", result.IdentityDetail, want)
		}
	}
	var text bytes.Buffer
	if err := writeDaemonResult(&text, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=unverified", "identity=foreign_home", "ready_verified=false", "api_reachable=true"} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text status %q does not contain %q", text.String(), want)
		}
	}
	// The record is evidence, not something this home may rewrite.
	stored, found, err := store.Load()
	if err != nil || !found || stored.Status != daemon.StatusReady || stored.WBHome != state.WBHome {
		t.Fatalf("foreign record was modified: %#v, %t, %v", stored, found, err)
	}
}

// AC: status-cannot-be-fooled-by-a-stranger (provenance is its own condition)
func TestDaemonStatusDoesNotFoldAProvenanceMismatchIntoReady(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}).Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 900 }
	deps.health = func(context.Context, string) error { return nil }

	result, err := newDaemonController(deps, root).Status(context.Background())
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

// A record written before home identity cannot be shown to belong to this home,
// so it is reported as unverified rather than assumed to match.
func TestDaemonStatusReportsAnUnrecordedRecordAsUnverified(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	current, err := controller.provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemon.NewStarting(nil, daemonDefaultListen, current, "owner", time.Now())
	state.MarkReady(900, time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 900 }

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

// A record that names a different state file describes a daemon this home does
// not read, so it cannot be presented as this home's daemon either.
func TestDaemonStatusRefusesARecordThatNamesAnotherStatePath(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	current, err := controller.provenance()
	if err != nil {
		t.Fatal(err)
	}
	state := daemonTestState(t, root, daemonDefaultListen, current, "owner", time.Now())
	state.StatePath = filepath.Join(string(filepath.Separator), "elsewhere", "daemon-state.json")
	state.MarkReady(900, time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(pid int) bool { return pid == 900 }

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

// AC: a-leftover-daemon-cannot-be-silently-doubled
func TestDaemonStartRefusesWhileADaemonServesTheLegacyRuntimeDirectory(t *testing.T) {
	root := daemonTestRoot(t)
	// The legacy directory is only legacy when it is not the home this build
	// writes to, so the fixture resolves its own home elsewhere.
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	deps.alive = func(pid int) bool { return pid == 4321 }
	legacyDir := daemon.LegacyRuntimeDir(root)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "legacy-owner", time.Now())
	legacy.MarkReady(4321, time.Now())
	if err := (daemon.Store{Path: daemon.LegacyStatePath(root)}).Save(legacy); err != nil {
		t.Fatal(err)
	}
	before := daemonTestDirectorySnapshot(t, legacyDir)
	deps.start = func(string, []string, string) (int, error) {
		t.Fatal("a refused start must not launch a daemon")
		return 0, nil
	}

	result, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen)
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
	if _, err := os.Stat(mustDaemonPath(t, daemonStatePath, root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the refused start wrote a record into the current home: %v", err)
	}
}

// AC: a-leftover-daemon-cannot-be-silently-doubled (a held loopback endpoint)
func TestServeDashboardNamesTheEndpointItCouldNotBind(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = held.Close() }()
	address := held.Addr().String()
	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}

	err = serveDashboard(&cobra.Command{}, deps, address, store, "owner-token", true, false)
	if err == nil {
		t.Fatal("serving on a held endpoint must fail")
	}
	for _, want := range []string{address, "already held"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("bind failure %q does not contain %q", err.Error(), want)
		}
	}
	if _, found, loadErr := store.Load(); loadErr != nil || found {
		t.Fatalf("a failed bind must not record a lifecycle state: found=%t err=%v", found, loadErr)
	}
}

// AC: a-daemon-outlives-its-directory-only-by-stopping
func TestDaemonStopsAndSaysWhyWhenItsRuntimeDirectoryVanishes(t *testing.T) {
	root := daemonTestRoot(t)
	controller := newDaemonController(daemonTestDependencies(t, root), root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", time.Now())
	state.MarkReadyWithProcess(os.Getpid(), daemonProcessStartedAt(os.Getpid()), time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	runtimeDir := filepath.Dir(controller.store.Path)
	if err := os.RemoveAll(runtimeDir); err != nil {
		t.Fatal(err)
	}
	previous := daemonHeartbeatInterval
	daemonHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { daemonHeartbeatInterval = previous })

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := daemonRuntimeGuard(&out, ctx, daemonDefaultListen, controller.store, state, "owner-token")
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
	root := daemonTestRoot(t)
	controller := newDaemonController(daemonTestDependencies(t, root), root)
	state := daemonTestState(t, root, daemonDefaultListen, daemon.Provenance{Executable: "wb", SHA256: "hash", Version: "test"}, "owner-token", time.Now())
	if err := controller.store.Save(state); err != nil {
		t.Fatal(err)
	}
	previous := daemonHeartbeatInterval
	daemonHeartbeatInterval = time.Millisecond
	t.Cleanup(func() { daemonHeartbeatInterval = previous })

	var out bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancel()
	if err := daemonRuntimeGuard(&out, ctx, daemonDefaultListen, controller.store, state, "owner-token"); err != nil {
		t.Fatalf("an intact runtime directory must not stop the daemon: %v", err)
	}
	if !strings.Contains(out.String(), "daemon heartbeat: ready") {
		t.Fatalf("heartbeat log = %q", out.String())
	}
}

// AC: units-do-not-bake-in-a-resolved-path
func TestDaemonStartPersistsNoResolvedRuntimePath(t *testing.T) {
	root := daemonTestRoot(t)
	deps := daemonTestDependencies(t, root)
	original := deps.start
	var args []string
	var logPath string
	deps.start = func(executable string, launched []string, log string) (int, error) {
		args, logPath = append([]string(nil), launched...), log
		return original(executable, launched, log)
	}
	if _, err := newDaemonController(deps, root).Start(context.Background(), daemonDefaultListen); err != nil {
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
	// The supervisor's log is the daemon's own log only where no unit records
	// it; on darwin it is a stable location outside WB's runtime directory, so
	// a later home move cannot leave a unit writing into an abandoned one.
	if daemonStartLogIsResolvedRuntimePath() && strings.Contains(logPath, runtimeDir) {
		t.Fatalf("supervisor log %q pins the runtime directory", logPath)
	}
}

func TestReportPinnedLifecycleStateIsNeverSilent(t *testing.T) {
	var out bytes.Buffer
	reportPinnedLifecycleState(&out, "", "/home/a/.wb/runtime/daemon-state.json")
	if out.String() != "" {
		t.Fatalf("an unpinned daemon reported %q", out.String())
	}
	reportPinnedLifecycleState(&out, "/old/daemon-state.json", "/home/a/.wb/runtime/daemon-state.json")
	for _, want := range []string{"/old/daemon-state.json", "/home/a/.wb/runtime/daemon-state.json"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("pinned report %q does not name %q", out.String(), want)
		}
	}
}

func daemonTestContains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

// daemonTestDirectorySnapshot lists a directory tree with file sizes, so a test
// can prove nothing was created, changed, or removed.
func daemonTestDirectorySnapshot(t *testing.T, root string) string {
	t.Helper()
	var lines []string
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		lines = append(lines, path+" "+info.Mode().String())
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

// The failure this feature was written after: a daemon answers, this home
// records nothing, and the leftover lives in the runtime directory WB used to
// write. Both facts must be reportable at once.
func TestDaemonStatusNamesAnUnrecordedAnswerAndTheLegacyEndpoint(t *testing.T) {
	root := daemonTestRoot(t)
	// The home this invocation resolves is not the legacy directory, so the
	// leftover is genuinely elsewhere.
	t.Setenv(wbhome.EnvOverride, filepath.Join(root, "wb-home"))
	deps := daemonTestDependencies(t, root)
	deps.alive = func(pid int) bool { return pid == 700 }
	legacyDir := daemon.LegacyRuntimeDir(root)
	if err := os.MkdirAll(legacyDir, 0o700); err != nil {
		t.Fatal(err)
	}
	legacy := daemon.NewStarting(nil, daemonDefaultListen, daemon.Provenance{Executable: "old", SHA256: "old", Version: "old"}, "legacy-owner", time.Now())
	legacy.MarkReady(700, time.Now())
	if err := (daemon.Store{Path: daemon.LegacyStatePath(root)}).Save(legacy); err != nil {
		t.Fatal(err)
	}

	result, err := newDaemonController(deps, root).Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if result.Managed || result.Identity != identityAbsent || result.ReportedState != "absent" {
		t.Fatalf("status of a home with no daemon = %#v", result)
	}
	if !result.Reachable {
		t.Fatal("something answering on the configured endpoint must be reported")
	}
	if result.LegacyRuntime == nil || result.LegacyRuntime.RuntimeDir != legacyDir {
		t.Fatalf("legacy endpoint = %#v, want %s", result.LegacyRuntime, legacyDir)
	}
	var text bytes.Buffer
	if err := writeDaemonResult(&text, "text", result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"state=absent", "api_reachable=true", "identity=absent", "legacy_runtime="} {
		if !strings.Contains(text.String(), want) {
			t.Fatalf("text status %q does not contain %q", text.String(), want)
		}
	}
}
