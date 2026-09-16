//go:build !windows

package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sneat-dev/wb/internal/daemon"
)

// cwWtLockFixture makes a daemon fixture root with a secure runtime directory
// already in place, and returns the controller for it.
func cwWtLockFixture(t *testing.T) (string, daemonController, daemonDependencies) {
	t.Helper()
	root := cwWtDaemonRoot(t)
	t.Setenv("WB_HOME", filepath.Join(root, "wb-home"))
	previousRoot := projectsRoot
	projectsRoot = root
	t.Cleanup(func() { projectsRoot = previousRoot })
	deps := daemonTestDependencies(t, root)
	controller := newDaemonController(deps, root)
	if err := secureDaemonRuntime(root); err != nil {
		t.Fatalf("secureDaemonRuntime: %v", err)
	}
	return root, controller, deps
}

func cwWtWriteDaemonFile(t *testing.T, path, body string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestCwWtDaemonStateLockHappyPathAndFailures(t *testing.T) {
	_, controller, _ := cwWtLockFixture(t)

	release, err := controller.stateLock()
	if err != nil {
		t.Fatalf("stateLock: %v", err)
	}
	release()
	// The lock file survives and is reusable.
	if _, err := controller.stateLock(); err != nil {
		t.Fatalf("second stateLock: %v", err)
	}

	// A projects root whose .wb is a regular file cannot be secured.
	blocked := cwWtDaemonRoot(t)
	if err := os.WriteFile(filepath.Join(blocked, ".wb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonController(daemonTestDependencies(t, blocked), blocked).stateLock(); err == nil || !strings.Contains(err.Error(), "secure daemon runtime") {
		t.Fatalf("stateLock with a blocked runtime = %v", err)
	}

	// A directory in place of the lock file cannot be opened.
	directoryRoot, directoryController, _ := cwWtLockFixture(t)
	if err := os.Mkdir(mustDaemonPath(t, daemonStateLockPath, directoryRoot), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := directoryController.stateLock(); err == nil || !strings.Contains(err.Error(), "open daemon state lock") {
		t.Fatalf("stateLock over a directory = %v", err)
	}

	// A lock file that is not owner-only is refused, not chmod'ed.
	permRoot, permController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonStateLockPath, permRoot), "", 0o644)
	if _, err := permController.stateLock(); err == nil || !strings.Contains(err.Error(), "validate daemon state lock permissions") {
		t.Fatalf("stateLock over a 0644 lock = %v", err)
	}

	// A hard-linked lock file is refused: flock on a shared inode would let a
	// second path bypass the lock.
	linkRoot, linkController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonStateLockPath, linkRoot), "", 0o600)
	if err := os.Link(mustDaemonPath(t, daemonStateLockPath, linkRoot), mustDaemonPath(t, daemonStateLockPath, linkRoot)+".link"); err != nil {
		t.Fatal(err)
	}
	if _, err := linkController.stateLock(); err == nil || !strings.Contains(err.Error(), "single-link owner-only regular file") {
		t.Fatalf("stateLock over a hard-linked lock = %v", err)
	}

	// A runtime directory with the wrong mode is refused.
	modeRoot := cwWtDaemonRoot(t)
	previousRoot := projectsRoot
	projectsRoot = modeRoot
	defer func() { projectsRoot = previousRoot }()
	if err := os.MkdirAll(filepath.Join(modeRoot, ".wb", "runtime"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, err := newDaemonController(daemonTestDependencies(t, modeRoot), modeRoot).stateLock(); err == nil || !strings.Contains(err.Error(), "secure daemon runtime") {
		t.Fatalf("stateLock with a 0755 runtime = %v", err)
	}
}

func TestCwWtDaemonLifecycleLockHappyPathAndOwners(t *testing.T) {
	_, controller, deps := cwWtLockFixture(t)

	release, err := controller.lifecycleLock()
	if err != nil {
		t.Fatalf("lifecycleLock: %v", err)
	}
	release()
	// The sidecar must now record pid=0 after release.
	owner, err := os.ReadFile(mustDaemonPath(t, daemonLifecycleOwnerPath, controller.root))
	if err != nil {
		t.Fatalf("read lifecycle owner: %v", err)
	}
	if string(owner) != "pid=0\n" {
		t.Fatalf("released lifecycle owner = %q", owner)
	}

	// A live owner process blocks the transition.
	liveRoot, liveController, liveDeps := cwWtLockFixture(t)
	_ = liveRoot
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, liveController.root), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, liveController.root), "pid="+itoa(os.Getpid())+"\n", 0o600)
	liveDeps.alive = func(pid int) bool { return pid == os.Getpid() }
	liveController.deps = liveDeps
	if _, err := liveController.lifecycleLock(); err == nil || !strings.Contains(err.Error(), "names live process") {
		t.Fatalf("lifecycleLock with a live owner = %v", err)
	}

	// A dead owner with an unsafe durable state is refused.
	unsafeRoot, unsafeController, unsafeDeps := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, unsafeController.root), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, unsafeController.root), "pid=999999\n", 0o600)
	unsafeDeps.alive = func(int) bool { return false }
	unsafeController.deps = unsafeDeps
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, unsafeController.root)}).Save(
		daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "t", deps.now())); err != nil {
		t.Fatal(err)
	}
	if _, err := unsafeController.lifecycleLock(); err == nil || !strings.Contains(err.Error(), "recovery is unsafe") {
		t.Fatalf("lifecycleLock with an unsafe state = %v", err)
	}
	_ = unsafeRoot

	// A dead owner with no durable state is reclaimed safely.
	reclaimRoot, reclaimController, reclaimDeps := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, reclaimController.root), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, reclaimController.root), "pid=999999\n", 0o600)
	reclaimDeps.alive = func(int) bool { return false }
	reclaimController.deps = reclaimDeps
	release, err = reclaimController.lifecycleLock()
	if err != nil {
		t.Fatalf("lifecycleLock reclaiming a dead owner: %v", err)
	}
	release()
	_ = reclaimRoot
}

func itoa(value int) string {
	if value == 0 {
		return "0"
	}
	negative := value < 0
	if negative {
		value = -value
	}
	var digits []byte
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	if negative {
		return "-" + string(digits)
	}
	return string(digits)
}

func TestCwWtDaemonOpenLifecycleLockBranches(t *testing.T) {
	root, controller, _ := cwWtLockFixture(t)

	// No lock and no create request is not an error.
	file, found, created, err := controller.openLifecycleLock(false)
	if err != nil || file != nil || found || created {
		t.Fatalf("openLifecycleLock(false) with no lock = (%v, %t, %t, %v)", file, found, created, err)
	}

	// A blocked runtime fails before the lock is touched.
	blocked := cwWtDaemonRoot(t)
	if err := os.WriteFile(filepath.Join(blocked, ".wb"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := newDaemonController(daemonTestDependencies(t, blocked), blocked).openLifecycleLock(true); err == nil || !strings.Contains(err.Error(), "secure daemon runtime") {
		t.Fatalf("openLifecycleLock on a blocked runtime = %v", err)
	}
	// cwWtDaemonRoot pinned the home at the blocked fixture; put it back so the
	// lock paths below resolve against the fixture this test owns.
	pinDaemonHome(t, root)

	// A directory where the lock belongs cannot be opened. The daemon creates
	// its runtime directory when it starts, so the fixture has to place it
	// before it can put a directory where the lock file belongs.
	if err := os.MkdirAll(mustDaemonPath(t, daemon.RuntimeDir, root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(mustDaemonPath(t, daemonLifecycleLockPath, root), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := controller.openLifecycleLock(true); err == nil || !strings.Contains(err.Error(), "open daemon lifecycle lock") {
		t.Fatalf("openLifecycleLock over a directory = %v", err)
	}

	// A pre-existing non-owner-only lock is refused.
	permRoot, permController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, permRoot), "", 0o644)
	if _, _, _, err := permController.openLifecycleLock(true); err == nil || !strings.Contains(err.Error(), "validate daemon lifecycle lock permissions") {
		t.Fatalf("openLifecycleLock over a 0644 lock = %v", err)
	}

	// A hard-linked lock is refused.
	linkRoot, linkController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, linkRoot), "", 0o600)
	if err := os.Link(mustDaemonPath(t, daemonLifecycleLockPath, linkRoot), mustDaemonPath(t, daemonLifecycleLockPath, linkRoot)+".link"); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := linkController.openLifecycleLock(true); err == nil || !strings.Contains(err.Error(), "single-link owner-only regular file") {
		t.Fatalf("openLifecycleLock over a hard-linked lock = %v", err)
	}
}

func TestCwWtDaemonLifecycleOwnerPIDBranches(t *testing.T) {
	root, controller, _ := cwWtLockFixture(t)

	// Legacy metadata lives in the lock file itself when no sidecar exists.
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, root), "pid=12345\n", 0o600)
	file, _, _, err := controller.openLifecycleLock(true)
	if err != nil {
		t.Fatalf("openLifecycleLock: %v", err)
	}
	pid, err := controller.lifecycleOwnerPID(file)
	_ = file.Close()
	if err != nil || pid != 12345 {
		t.Fatalf("legacy owner pid = (%d, %v)", pid, err)
	}

	// An empty legacy lock means no owner.
	emptyRoot, emptyController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, emptyRoot), "", 0o600)
	emptyFile, _, _, err := emptyController.openLifecycleLock(true)
	if err != nil {
		t.Fatal(err)
	}
	pid, err = emptyController.lifecycleOwnerPID(emptyFile)
	_ = emptyFile.Close()
	if err != nil || pid != 0 {
		t.Fatalf("empty legacy lock = (%d, %v)", pid, err)
	}

	// Ambiguous legacy metadata is an error.
	badRoot, badController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, badRoot), "not-a-pid\n", 0o600)
	badFile, _, _, err := badController.openLifecycleLock(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := badController.lifecycleOwnerPID(badFile); err == nil || !strings.Contains(err.Error(), "ambiguous ownership metadata") {
		t.Fatalf("ambiguous legacy lock = %v", err)
	}
	_ = badFile.Close()

	// A sidecar with no metadata is an error.
	noMetaRoot, noMetaController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, noMetaRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, noMetaRoot), "", 0o600)
	noMetaFile, _, _, err := noMetaController.openLifecycleLock(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := noMetaController.lifecycleOwnerPID(noMetaFile); err == nil || !strings.Contains(err.Error(), "no ownership metadata") {
		t.Fatalf("sidecar without metadata = %v", err)
	}
	_ = noMetaFile.Close()

	// Ambiguous sidecar metadata is an error.
	ambigRoot, ambigController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, ambigRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, ambigRoot), "pid=abc\n", 0o600)
	ambigFile, _, _, err := ambigController.openLifecycleLock(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ambigController.lifecycleOwnerPID(ambigFile); err == nil {
		t.Fatal("an unparsable sidecar must be reported")
	}
	_ = ambigFile.Close()

	// A sidecar with the wrong permissions is refused.
	permRoot, permController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, permRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, permRoot), "pid=1\n", 0o644)
	permFile, _, _, err := permController.openLifecycleLock(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := permController.lifecycleOwnerPID(permFile); err == nil || !strings.Contains(err.Error(), "validate daemon lifecycle owner permissions") {
		t.Fatalf("sidecar with wrong permissions = %v", err)
	}
	_ = permFile.Close()

	// A sidecar that is a directory is refused as not a regular file.
	dirRoot, dirController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, dirRoot), "", 0o600)
	if err := os.Mkdir(mustDaemonPath(t, daemonLifecycleOwnerPath, dirRoot), 0o600); err != nil {
		t.Fatal(err)
	}
	dirFile, _, _, err := dirController.openLifecycleLock(true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := dirController.lifecycleOwnerPID(dirFile); err == nil || !strings.Contains(err.Error(), "single-link owner-only regular file") {
		t.Fatalf("sidecar that is a directory = %v", err)
	}
	_ = dirFile.Close()
}

func TestCwWtDaemonWriteLifecycleOwnerPIDFailure(t *testing.T) {
	root, controller, _ := cwWtLockFixture(t)
	// A directory where the sidecar belongs makes the atomic rename fail.
	if err := os.Mkdir(mustDaemonPath(t, daemonLifecycleOwnerPath, root), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := controller.writeLifecycleOwnerPID(7); err == nil || !strings.Contains(err.Error(), "replace daemon lifecycle owner") {
		t.Fatalf("writeLifecycleOwnerPID over a directory = %v", err)
	}
}

func TestCwWtDaemonStableLifecycleState(t *testing.T) {
	root, controller, deps := cwWtLockFixture(t)

	// No state at all is stable.
	if status, err := controller.stableLifecycleState(); err != nil || status != "" {
		t.Fatalf("no state = (%q, %v)", status, err)
	}

	store := daemon.Store{Path: mustDaemonPath(t, daemonStatePath, root)}
	ready := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "t", deps.now())
	ready.MarkReady(4242, deps.now())
	if err := store.Save(ready); err != nil {
		t.Fatal(err)
	}
	deps.alive = func(int) bool { return false }
	controller.deps = deps
	if status, err := controller.stableLifecycleState(); err == nil || status != daemon.StatusReady {
		t.Fatalf("dead ready state = (%q, %v)", status, err)
	}
	controller.deps.alive = func(int) bool { return true }
	if status, err := controller.stableLifecycleState(); err != nil || status != daemon.StatusReady {
		t.Fatalf("live ready state = (%q, %v)", status, err)
	}

	stopped := ready
	stopped.Status = daemon.StatusStopped
	stopped.PID = 9
	if err := store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	if status, err := controller.stableLifecycleState(); err == nil || status != daemon.StatusStopped {
		t.Fatalf("stopped state naming a process = (%q, %v)", status, err)
	}
	stopped.PID = 0
	if err := store.Save(stopped); err != nil {
		t.Fatal(err)
	}
	if status, err := controller.stableLifecycleState(); err != nil || status != daemon.StatusStopped {
		t.Fatalf("stopped state = (%q, %v)", status, err)
	}
}

func TestCwWtDaemonRecoverLifecycleLockBranches(t *testing.T) {
	// No lock at all.
	noLockRoot, noLockController, _ := cwWtLockFixture(t)
	result, err := noLockController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "no_lock" || result.LockPresent {
		t.Fatalf("recover with no lock = (%+v, %v)", result, err)
	}
	_ = noLockRoot

	// A lock with no owner at all.
	noOwnerRoot, noOwnerController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, noOwnerRoot), "", 0o600)
	result, err = noOwnerController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "no_stale_owner" || result.OwnerPID != 0 {
		t.Fatalf("recover with an idle lock = (%+v, %v)", result, err)
	}

	// A lock whose owner sidecar is ambiguous.
	ambigRoot, ambigController, _ := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, ambigRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, ambigRoot), "pid=nope\n", 0o600)
	result, err = ambigController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "ambiguous_owner" {
		t.Fatalf("recover with an ambiguous owner = (%+v, %v)", result, err)
	}

	// A lock naming a live owner.
	liveRoot, liveController, liveDeps := cwWtLockFixture(t)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, liveRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, liveRoot), "pid=12345\n", 0o600)
	liveDeps.alive = func(pid int) bool { return pid == 12345 }
	liveController.deps = liveDeps
	result, err = liveController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "owner_alive" || !result.OwnerAlive {
		t.Fatalf("recover with a live owner = (%+v, %v)", result, err)
	}

	// A dead owner with no durable state, dry run then apply.
	deadRoot, deadController, deadDeps := cwWtLockFixture(t)
	deadDeps.alive = func(int) bool { return false }
	deadController.deps = deadDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, deadRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, deadRoot), "pid=999999\n", 0o600)
	result, err = deadController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "interrupted_before_state" || !result.Eligible || result.Applied {
		t.Fatalf("recover before state, dry run = (%+v, %v)", result, err)
	}
	result, err = deadController.RecoverLifecycleLock(context.Background(), true)
	if err != nil || !result.Applied {
		t.Fatalf("recover before state, apply = (%+v, %v)", result, err)
	}

	// A dead owner over an unsafe ready state.
	unsafeRoot, unsafeController, unsafeDeps := cwWtLockFixture(t)
	unsafeDeps.alive = func(int) bool { return false }
	unsafeController.deps = unsafeDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, unsafeRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, unsafeRoot), "pid=999999\n", 0o600)
	unsafeReady := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "t", unsafeDeps.now())
	unsafeReady.MarkReady(4242, unsafeDeps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, unsafeRoot)}).Save(unsafeReady); err != nil {
		t.Fatal(err)
	}
	result, err = unsafeController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "unsafe_state" || result.Eligible {
		t.Fatalf("recover over an unsafe ready state = (%+v, %v)", result, err)
	}
}

func TestCwWtDaemonRecoverStartingAndDraining(t *testing.T) {
	// Starting with no recorded PID inside the readiness grace period.
	graceRoot, graceController, graceDeps := cwWtLockFixture(t)
	graceDeps.alive = func(int) bool { return false }
	graceController.deps = graceDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, graceRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, graceRoot), "pid=999999\n", 0o600)
	starting := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "t", graceDeps.now())
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, graceRoot)}).Save(starting); err != nil {
		t.Fatal(err)
	}
	result, err := graceController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "startup_grace_period" {
		t.Fatalf("recover inside the grace period = (%+v, %v)", result, err)
	}

	// Starting with no PID, past the grace period, whose API still answers.
	apiRoot, apiController, apiDeps := cwWtLockFixture(t)
	apiDeps.alive = func(int) bool { return false }
	apiController.deps = apiDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, apiRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, apiRoot), "pid=999999\n", 0o600)
	provenance, err := apiController.provenance()
	if err != nil {
		t.Fatal(err)
	}
	old := daemon.NewStarting(nil, "127.0.0.1:0", provenance, "t", apiDeps.now().Add(-time.Hour))
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, apiRoot)}).Save(old); err != nil {
		t.Fatal(err)
	}
	result, err = apiController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "startup_api_reachable" {
		t.Fatalf("recover with a reachable API = (%+v, %v)", result, err)
	}

	// Starting with no PID, past the grace period, no API, dry run then apply.
	interruptedRoot, interruptedController, interruptedDeps := cwWtLockFixture(t)
	interruptedDeps.alive = func(int) bool { return false }
	interruptedDeps.health = func(context.Context, string) error { return errors.New("cwWt: unreachable") }
	interruptedController.deps = interruptedDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, interruptedRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, interruptedRoot), "pid=999999\n", 0o600)
	provenance, err = interruptedController.provenance()
	if err != nil {
		t.Fatal(err)
	}
	interrupted := daemon.NewStarting(nil, "127.0.0.1:0", provenance, "t", interruptedDeps.now().Add(-time.Hour))
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, interruptedRoot)}).Save(interrupted); err != nil {
		t.Fatal(err)
	}
	result, err = interruptedController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "interrupted_start" || !result.Eligible {
		t.Fatalf("interrupted start dry run = (%+v, %v)", result, err)
	}
	result, err = interruptedController.RecoverLifecycleLock(context.Background(), true)
	if err != nil || !result.Applied {
		t.Fatalf("interrupted start apply = (%+v, %v)", result, err)
	}

	// A live startup PID owned by a different executable is unverified.
	foreignRoot, foreignController, foreignDeps := cwWtLockFixture(t)
	foreignDeps.alive = func(pid int) bool { return pid == 4242 }
	foreignController.deps = foreignDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, foreignRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, foreignRoot), "pid=999999\n", 0o600)
	foreign := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{Executable: "/somewhere/else"}, "t", foreignDeps.now().Add(-time.Hour))
	foreign.PID = 4242
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, foreignRoot)}).Save(foreign); err != nil {
		t.Fatal(err)
	}
	result, err = foreignController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "startup_process_unverified" {
		t.Fatalf("foreign live startup = (%+v, %v)", result, err)
	}

	// A live startup PID of this binary whose health check fails is unverified.
	unhealthyRoot, unhealthyController, unhealthyDeps := cwWtLockFixture(t)
	unhealthyDeps.alive = func(pid int) bool { return pid == 4242 }
	unhealthyDeps.ownedHealth = func(context.Context, string, int, uint64) error { return errors.New("cwWt: unhealthy") }
	unhealthyController.deps = unhealthyDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, unhealthyRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, unhealthyRoot), "pid=999999\n", 0o600)
	provenance, err = unhealthyController.provenance()
	if err != nil {
		t.Fatal(err)
	}
	unhealthy := daemon.NewStarting(nil, "127.0.0.1:0", provenance, "t", unhealthyDeps.now().Add(-time.Hour))
	unhealthy.PID = 4242
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, unhealthyRoot)}).Save(unhealthy); err != nil {
		t.Fatal(err)
	}
	result, err = unhealthyController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "startup_process_unverified" {
		t.Fatalf("unhealthy live startup = (%+v, %v)", result, err)
	}

	// A healthy live startup of this binary is adopted.
	healthyRoot, healthyController, healthyDeps := cwWtLockFixture(t)
	healthyDeps.alive = func(pid int) bool { return pid == 4242 }
	healthyDeps.ownedHealth = func(context.Context, string, int, uint64) error { return nil }
	healthyController.deps = healthyDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, healthyRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, healthyRoot), "pid=999999\n", 0o600)
	provenance, err = healthyController.provenance()
	if err != nil {
		t.Fatal(err)
	}
	healthy := daemon.NewStarting(nil, "127.0.0.1:0", provenance, "t", healthyDeps.now().Add(-time.Hour))
	healthy.PID = 4242
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, healthyRoot)}).Save(healthy); err != nil {
		t.Fatal(err)
	}
	result, err = healthyController.RecoverLifecycleLock(context.Background(), true)
	if err != nil || result.Reason != "orphaned_healthy_start" || !result.Applied {
		t.Fatalf("healthy live startup = (%+v, %v)", result, err)
	}
	if state, found, err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, healthyRoot)}).Load(); err != nil || !found || state.Status != daemon.StatusReady {
		t.Fatalf("healthy startup state after apply = (%+v, %t, %v)", state, found, err)
	}

	// Draining with a live process is refused; with a dead one it is retired.
	drainRoot, drainController, drainDeps := cwWtLockFixture(t)
	drainDeps.alive = func(pid int) bool { return pid == 4242 }
	drainController.deps = drainDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, drainRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, drainRoot), "pid=999999\n", 0o600)
	draining := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "t", drainDeps.now())
	draining.Status = daemon.StatusDraining
	draining.PID = 4242
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, drainRoot)}).Save(draining); err != nil {
		t.Fatal(err)
	}
	result, err = drainController.RecoverLifecycleLock(context.Background(), false)
	if err != nil || result.Reason != "drain_process_alive" {
		t.Fatalf("live drain = (%+v, %v)", result, err)
	}
	drainController.deps.alive = func(int) bool { return false }
	result, err = drainController.RecoverLifecycleLock(context.Background(), true)
	if err != nil || result.Reason != "interrupted_drain" || !result.Applied {
		t.Fatalf("interrupted drain = (%+v, %v)", result, err)
	}

	// An unrecognized durable status is an error, never a guess.
	oddRoot, oddController, oddDeps := cwWtLockFixture(t)
	oddDeps.alive = func(int) bool { return false }
	oddController.deps = oddDeps
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleLockPath, oddRoot), "", 0o600)
	cwWtWriteDaemonFile(t, mustDaemonPath(t, daemonLifecycleOwnerPath, oddRoot), "pid=999999\n", 0o600)
	odd := daemon.NewStarting(nil, "127.0.0.1:0", daemon.Provenance{}, "t", oddDeps.now())
	odd.Status = "sideways"
	if err := (daemon.Store{Path: mustDaemonPath(t, daemonStatePath, oddRoot)}).Save(odd); err != nil {
		t.Fatal(err)
	}
	if _, err := oddController.RecoverLifecycleLock(context.Background(), false); err == nil || !strings.Contains(err.Error(), "unsupported daemon lifecycle state") {
		t.Fatalf("unsupported lifecycle state = %v", err)
	}
}
