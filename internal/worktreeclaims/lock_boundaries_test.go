package worktreeclaims

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

func TestOperationLockRejectsReplacedEntryWithoutDeletingSuccessor(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "task", 12)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(path, ".lock"), filepath.Join(path, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, ".lock"), []byte("successor"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := lock.Release(); err == nil || !strings.Contains(err.Error(), "changed") {
		t.Fatalf("replaced lock release = %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(path, ".lock")); err != nil || string(data) != "successor" {
		t.Fatalf("successor changed: %q, %v", data, err)
	}
}

func TestCleanupRecoveryNeedsOneNamedDeadOwner(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.MkdirAll(filepath.Join(worktreesRoot, "task"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: root}, Read: []wbhome.Layout{{Home: root, WorktreesRoot: worktreesRoot}}}
	ports := CleanupLockPorts{PID: func() int { return 321 }, ProcessIsDead: func(pid int) bool { return pid == 321 }}
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task"); err == nil {
		t.Fatal("missing lock was recovered")
	}
	lockPath := filepath.Join(worktreesRoot, "task", ".lock")
	if err := os.WriteFile(lockPath, []byte("operation=task\npid=321\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	task, evidence, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task")
	if err != nil || evidence == nil || evidence.PID != 321 || evidence.Task != "task" {
		t.Fatalf("named recovery: %+v, %v", evidence, err)
	}
	if err := ValidateRecoveredCleanupLock(true, task); err != nil {
		t.Fatal(err)
	}
	task.PreserveLock()
	task.Close()
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("preserved lock disappeared: %v", err)
	}
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(wbhome.Resolution{Write: resolution.Write}, "task"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("ambiguous/missing namespace = %v", err)
	}
}

func TestInterruptedOwnerEvidenceRejectsAmbiguousMetadata(t *testing.T) {
	t.Parallel()
	directory, path := openLockDirectory(t)
	path = filepath.Join(path, "owner")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	for _, content := range []string{"", "operation=other\npid=99\n", "operation=task\npid=0\n", "operation=task\npid=01\n", "operation=task\npid=x\n", "operation=task\npid=99"} {
		if err := file.Truncate(0); err != nil {
			t.Fatal(err)
		}
		if _, err := file.WriteAt([]byte(content), 0); err != nil {
			t.Fatal(err)
		}
		if _, err := InterruptedTaskLockPID(file, "task", func(int) bool { return true }); err == nil {
			t.Fatalf("accepted metadata %q", content)
		}
	}
	if _, err := InterruptedTaskLockPID(nil, "task", func(int) bool { return true }); err == nil {
		t.Fatal("accepted absent descriptor")
	}
	if err := file.Truncate(0); err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteAt([]byte("operation=task\npid=99\n"), 0); err != nil {
		t.Fatal(err)
	}
	if _, err := InterruptedTaskLockPID(file, "task", func(int) bool { return false }); err == nil || !strings.Contains(err.Error(), "live or ambiguous") {
		t.Fatalf("live owner accepted: %v", err)
	}
	state, _ := DiagnoseTaskLock(filepath.Dir(path), "task", func(int) bool { return true })
	if state != LockOwnerNone {
		t.Fatalf("unexpected diagnosis without .lock: %s", state)
	}
	if _, err := directory.Stat(); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryRegistrationLockContentionIsBounded(t *testing.T) {
	t.Parallel()
	directory, _ := openLockDirectory(t)
	held, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, time.Now, time.Sleep, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = held.Release() })
	clock := time.Now()
	now := func() time.Time { return clock }
	sleep := func(delay time.Duration) { clock = clock.Add(delay) }
	if _, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, now, sleep, 40*time.Millisecond); err == nil || !strings.Contains(err.Error(), "held") {
		t.Fatalf("contended registration = %v", err)
	}
	if err := held.Release(); err != nil {
		t.Fatal(err)
	}
	if err := held.Release(); err != nil {
		t.Fatalf("idempotent release: %v", err)
	}
	expiredNow := func() time.Time { clock = clock.Add(time.Second); return clock }
	if _, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, expiredNow, sleep, 0); err == nil || !strings.Contains(err.Error(), "held") {
		t.Fatalf("expired deadline = %v", err)
	}
}

func TestCleanupAcquisitionAndDiagnosticsRefuseUnsafeInputs(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := CleanupLockPorts{PID: func() int { return 7 }, ProcessIsDead: func(int) bool { return false }, PrepareTask: func(home, task string) (*CleanupTask, error) { return nil, errors.New("preparation denied") }}
	if _, err := ports.AcquireCleanupTaskAtOrCreate(worktreesRoot, "missing"); err == nil || !strings.Contains(err.Error(), "preparation denied") {
		t.Fatalf("missing shell preparation = %v", err)
	}
	if _, err := ports.AcquireCleanupTaskAtReclaimingInterrupted(worktreesRoot, "missing", false); err == nil {
		t.Fatal("missing task accepted")
	}
	if err := os.Mkdir(filepath.Join(worktreesRoot, "task"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(worktreesRoot, "task", ".lock"), []byte("bad"), 0o600); err != nil {
		t.Fatal(err)
	}
	state, _ := DiagnoseTaskLock(filepath.Join(worktreesRoot, "task"), "task", ports.ProcessIsDead)
	if state != LockOwnerUnreadable || !strings.Contains(LockedReason(state, 0, ""), "cannot be established") {
		t.Fatalf("ambiguous owner: %s", state)
	}
	if got := LockedReason(LockOwnerLive, 7, ""); !strings.Contains(got, "PID 7") {
		t.Fatal(got)
	}
	if got := LockedReason(LockOwnerNone, 0, ""); !strings.Contains(got, "active or interrupted") {
		t.Fatal(got)
	}
	if err := ValidateRecoveredCleanupLock(false, nil); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRecoveredCleanupLock(true, nil); err == nil {
		t.Fatal("accepted nil recovered task")
	}
	if _, err := ports.AcquireCleanupTaskAt(worktreesRoot, "task"); err == nil {
		t.Fatal("ambiguous lock accepted")
	}
	recovered, err := ports.AcquireCleanupTaskAtReclaimingInterruptedLock(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := InterruptedTaskLockPID(recovered.Lock.file, "task", ports.ProcessIsDead); err == nil {
		t.Fatal("ambiguous owner evidence accepted")
	}
	recovered.PreserveLock()
	recovered.Close()
	if got := fmt.Sprint(LockOwnerDead); got != "dead" {
		t.Fatal(got)
	}
}

func TestCleanupLockPreservesForeignDebrisAndRejectsPathSwap(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	worktreesRoot := filepath.Join(root, "worktrees")
	taskPath := filepath.Join(worktreesRoot, "task")
	if err := os.MkdirAll(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := CleanupLockPorts{PID: func() int { return 5 }, ProcessIsDead: func(int) bool { return true }}
	task, err := ports.AcquireCleanupTaskAt(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Lock.Release(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(taskPath, "foreign"), []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	PurgeTerminalTaskLockDebris(task)
	entries, err := os.ReadDir(taskPath)
	if err != nil || len(entries) != 2 {
		t.Fatalf("foreign debris was removed: %v, %v", entries, err)
	}
	if err := os.Rename(taskPath, filepath.Join(worktreesRoot, "moved")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := task.Validate(); err == nil || !strings.Contains(err.Error(), "task path changed") {
		t.Fatalf("task substitution = %v", err)
	}
	task.Close()
	if err := os.Rename(worktreesRoot, filepath.Join(root, "moved-root")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := task.Validate(); err == nil || !strings.Contains(err.Error(), "worktrees root path changed") {
		t.Fatalf("root substitution = %v", err)
	}
	PurgeTerminalTaskLockDebris(nil)
	(*CleanupTask)(nil).Close()
	(*CleanupTask)(nil).PreserveLock()
}

func TestOperationLockRejectsUnsafeExistingEntries(t *testing.T) {
	t.Parallel()
	for name, prepare := range map[string]func(string) error{
		"empty":   func(path string) error { return os.WriteFile(path, nil, 0o600) },
		"symlink": func(path string) error { return os.Symlink("target", path) },
		"hardlink": func(path string) error {
			target := filepath.Join(filepath.Dir(path), "target")
			if err := os.WriteFile(target, []byte("operation=task\npid=1\n"), 0o600); err != nil {
				return err
			}
			return os.Link(target, path)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory, root := openLockDirectory(t)
			if err := prepare(filepath.Join(root, ".lock")); err != nil {
				t.Fatal(err)
			}
			if _, err := AcquireLockAtReclaimingInterrupted(directory, true, "task", 1); err == nil {
				t.Fatal("unsafe existing lock accepted")
			}
		})
	}
	directory, _ := openLockDirectory(t)
	if err := WriteOperationLockMetadata(directory, "task", 1); err == nil {
		t.Fatal("metadata written to directory")
	}
}

func TestWorkLogLockRejectsUnsafePathAndMissingRecords(t *testing.T) {
	t.Parallel()
	home := testCanonicalTemp(t)
	valid := func(value string) bool { return value != "" && value != ".." && !strings.ContainsAny(value, "/\\") }
	if _, _, err := OpenWorkLogRun(home, "../bad", "run", true, valid); err == nil {
		t.Fatal("invalid effort accepted")
	}
	if _, err := OpenWorkLogOutbox(home, "../bad", true, valid); err == nil {
		t.Fatal("invalid outbox effort accepted")
	}
	if _, err := OpenWorkLogOutbox(home, "missing", false, valid); err == nil {
		t.Fatal("missing outbox accepted without create")
	}
	run, _, err := OpenWorkLogRun(home, "effort", "run", true, valid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	if _, err := ReadWorkLogClaimAt[map[string]any](run, "missing", valid); err == nil {
		t.Fatal("missing claim accepted")
	}
	if _, err := ReadWorkLogTerminalAt[map[string]any](run, "missing", valid); err == nil {
		t.Fatal("missing terminal accepted")
	}
	if err := os.Mkdir(filepath.Join(home, "worklogs", "effort", "runs", "run", "locks"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(home, "worklogs", "effort", "runs", "run", "locks", "claim.lock"), 0o700); err != nil {
		t.Fatal(err)
	}
	if unlock, err := LockClaim(run, "claim", valid); err == nil {
		unlock()
		t.Fatal("directory was accepted as claim lock")
	}
	if _, err := OpenLockedWorkLogRun(home, "effort", "run", "claim", false, valid); err == nil {
		t.Fatal("failed claim fence still returned a run")
	}
}

func TestCleanupTaskAcquisitionRechecksOpenedDirectory(t *testing.T) {
	t.Parallel()
	for _, recoverLock := range []bool{false, true} {
		t.Run(fmt.Sprint(recoverLock), func(t *testing.T) {
			t.Parallel()
			root := testCanonicalTemp(t)
			worktreesRoot := filepath.Join(root, "worktrees")
			taskPath := filepath.Join(worktreesRoot, "task")
			if err := os.MkdirAll(taskPath, 0o700); err != nil {
				t.Fatal(err)
			}
			ports := CleanupLockPorts{PID: func() int { return 7 }, AfterTaskOpen: func() {
				if err := os.Rename(taskPath, filepath.Join(worktreesRoot, "moved")); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(taskPath, 0o700); err != nil {
					t.Fatal(err)
				}
			}}
			var err error
			if recoverLock {
				_, err = ports.AcquireCleanupTaskAtReclaimingInterruptedLock(worktreesRoot, "task")
			} else {
				_, err = ports.AcquireCleanupTaskAtReclaimingInterrupted(worktreesRoot, "task", false)
			}
			if err == nil || !strings.Contains(err.Error(), "path changed") {
				t.Fatalf("task replacement = %v", err)
			}
		})
	}
}

func TestCleanupCreateOnlyPreparesMissingShell(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	prepare := func(home, name string) (*CleanupTask, error) {
		path := filepath.Join(worktreesRoot, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			return nil, err
		}
		worktrees, err := os.Open(worktreesRoot)
		if err != nil {
			return nil, err
		}
		directory, err := os.Open(path)
		if err != nil {
			_ = worktrees.Close()
			return nil, err
		}
		return &CleanupTask{WorktreesPath: worktreesRoot, TaskPath: path, Worktrees: worktrees, Task: directory}, nil
	}
	ports := CleanupLockPorts{PID: func() int { return 33 }, PrepareTask: prepare}
	task, err := ports.AcquireCleanupTaskAtOrCreate(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := task.Lock.Release(); err != nil {
		t.Fatal(err)
	}
	task.Close()
	if _, err := os.Stat(filepath.Join(worktreesRoot, "task")); err != nil {
		t.Fatal(err)
	}
	second, err := ports.AcquireCleanupTaskAtOrCreate(worktreesRoot, "task")
	if err != nil {
		t.Fatal(err)
	}
	if err := second.Lock.Release(); err != nil {
		t.Fatal(err)
	}
	second.Close()
}

func TestNamedRecoveryRejectsOtherAndAmbiguousNamespaces(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	ports := CleanupLockPorts{PID: func() int { return 7 }, ProcessIsDead: func(int) bool { return true }}
	layout := wbhome.Layout{Home: root, WorktreesRoot: worktreesRoot}
	resolution := wbhome.Resolution{Write: layout, Read: []wbhome.Layout{layout}}
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing task = %v", err)
	}
	if err := os.Symlink("elsewhere", filepath.Join(worktreesRoot, "task")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task"); err == nil || !strings.Contains(err.Error(), "without following links") {
		t.Fatalf("symlink task = %v", err)
	}
	if err := os.Remove(filepath.Join(worktreesRoot, "task")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(worktreesRoot, "task"), 0o700); err != nil {
		t.Fatal(err)
	}
	legacyRoot := filepath.Join(root, "legacy")
	if err := os.MkdirAll(filepath.Join(legacyRoot, "task"), 0o700); err != nil {
		t.Fatal(err)
	}
	resolution.Read = append(resolution.Read, wbhome.Layout{Home: legacyRoot, WorktreesRoot: legacyRoot, Legacy: true})
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task"); err == nil || !strings.Contains(err.Error(), "found 2") {
		t.Fatalf("two task roots = %v", err)
	}
}

func TestOwnerMetadataAndDiagnosticsRejectOversizedOrUnreadableFiles(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	file, err := os.Create(filepath.Join(root, ".lock"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte(strings.Repeat("x", 4097))); err != nil {
		t.Fatal(err)
	}
	if _, err := InterruptedTaskLockPID(file, "task", func(int) bool { return true }); err == nil {
		t.Fatal("oversized owner evidence accepted")
	}
	if state, _ := DiagnoseTaskLock(root, "task", func(int) bool { return true }); state != LockOwnerUnreadable {
		t.Fatalf("oversized diagnostic = %s", state)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := InterruptedTaskLockPID(file, "task", func(int) bool { return true }); err == nil {
		t.Fatal("closed owner descriptor accepted")
	}
	if err := os.Remove(filepath.Join(root, ".lock")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("other", filepath.Join(root, ".lock")); err != nil {
		t.Fatal(err)
	}
	if state, _ := DiagnoseTaskLock(root, "task", func(int) bool { return true }); state != LockOwnerNone {
		t.Fatalf("symlink diagnostic = %s", state)
	}
}

func TestRetiredLockReclamationSkipsForeignEntries(t *testing.T) {
	t.Parallel()
	directory, root := openLockDirectory(t)
	if err := os.Symlink("missing", filepath.Join(root, ".wb-retired-lock-link")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "external"), []byte("untouched"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(filepath.Join(root, "external"), filepath.Join(root, ".wb-retired-lock-hardlink")); err != nil {
		t.Fatal(err)
	}
	if file, reused, err := ClaimRetiredLock(directory); err != nil || reused || file != nil {
		t.Fatalf("foreign retirement claimed: %v %v %v", file, reused, err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "external")); err != nil || string(data) != "untouched" {
		t.Fatalf("external file changed: %q %v", data, err)
	}
}

func TestOperationLockPortsExerciseDescriptorFaults(t *testing.T) {
	t.Parallel()
	for name, ports := range map[string]OperationLockPorts{
		"open":           {AfterClaim: func() {}},
		"hold":           {AfterOpen: func(file *os.File) { _ = file.Close() }},
		"directory-stat": {AfterHold: func(*os.File) {}},
		"lock-stat":      {AfterHold: func(file *os.File) { _ = file.Close() }},
		"metadata":       {AfterInspect: func(file *os.File) { _ = file.Close() }},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory, _ := openLockDirectory(t)
			if name == "open" {
				ports.AfterClaim = func() { _ = directory.Close() }
			}
			if name == "directory-stat" {
				ports.AfterHold = func(*os.File) { _ = directory.Close() }
			}
			if _, err := AcquireLockAtReclaimingInterrupted(directory, false, "task", 1, ports); err == nil {
				t.Fatal("descriptor fault was accepted")
			}
		})
	}
}

func TestInterruptedLockReclaimRechecksDescriptorAndEntry(t *testing.T) {
	t.Parallel()
	for name, ports := range map[string]OperationLockPorts{
		"identity":  {AfterReclaimOpen: func(file *os.File) { _ = file.Close() }},
		"stat":      {AfterReclaimIdentity: func(file *os.File) { _ = file.Close() }},
		"successor": {},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory, root := openLockDirectory(t)
			path := filepath.Join(root, ".lock")
			if err := os.WriteFile(path, []byte("operation=task\npid=1\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			if name == "successor" {
				ports.AfterHold = func(*os.File) {
					if err := os.Rename(path, filepath.Join(root, "old")); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(path, []byte("successor"), 0o600); err != nil {
						t.Fatal(err)
					}
				}
			}
			if _, err := ReclaimInterruptedLock(directory, true, ports); err == nil {
				t.Fatal("reclaimed changed descriptor")
			}
		})
	}
}

func TestRetiredLockClaimReportsMoveIdentityAndReadFailures(t *testing.T) {
	t.Parallel()
	directory, root := openLockDirectory(t)
	if _, _, err := ClaimRetiredLock(directory, OperationLockPorts{AfterRetiredRewind: func(file *os.File) { _ = file.Close() }}); err == nil || !strings.Contains(err.Error(), "read secure operation directory") {
		t.Fatalf("closed directory read = %v", err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	retired := filepath.Join(root, ".wb-retired-lock-test")
	if err := os.WriteFile(retired, []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ClaimRetiredLock(directory, OperationLockPorts{MoveRetired: func(*os.File, string, ManagedLockIdentity) (*os.File, error) {
		return nil, worktreesecure.ErrDirectoryMoveIdentityChanged
	}}); !errors.Is(err, worktreesecure.ErrDirectoryMoveIdentityChanged) {
		t.Fatalf("changed retirement = %v", err)
	}
	claimed, reused, err := ClaimRetiredLock(directory, OperationLockPorts{MoveRetired: func(*os.File, string, ManagedLockIdentity) (*os.File, error) {
		file, openErr := os.Open(retired)
		if openErr != nil {
			return nil, openErr
		}
		return file, errors.New("transient move failure")
	}})
	if err != nil || reused || claimed != nil {
		t.Fatalf("transient retirement = %v %v %v", claimed, reused, err)
	}
}

func TestOperationLockRetirementReportsQuarantineFailure(t *testing.T) {
	t.Parallel()
	directory, root := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "task", 1)
	if err != nil {
		t.Fatal(err)
	}
	ports := OperationLockPorts{MoveQuarantine: func(*os.File, string, ManagedLockIdentity) (*os.File, error) { return nil, unix.EEXIST }}
	if err := lock.Release(ports); err == nil || !strings.Contains(err.Error(), "collision-free") {
		t.Fatalf("retirement collision = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); err != nil {
		t.Fatalf("lock entry lost on retirement failure: %v", err)
	}
}

func TestOperationLockRejectsRetiredDirectoryAndMetadataWriteFaults(t *testing.T) {
	t.Parallel()
	directory, _ := openLockDirectory(t)
	ports := OperationLockPorts{StatDirectory: func(_ int, stat *unix.Stat_t) error { stat.Nlink = 0; return nil }}
	if _, err := AcquireLockAtReclaimingInterrupted(directory, false, "task", 1, ports); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired directory = %v", err)
	}
	file, err := os.CreateTemp(testCanonicalTemp(t), "metadata-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	for _, test := range []struct {
		name  string
		ports LockMetadataPorts
	}{
		{"seek", LockMetadataPorts{AfterTruncate: func() { _ = file.Close() }}},
		{"write", LockMetadataPorts{AfterSeek: func() { _ = file.Close() }}},
		{"sync", LockMetadataPorts{AfterWrite: func() { _ = file.Close() }}},
	} {
		t.Run(test.name, func(t *testing.T) {
			opened, err := os.OpenFile(file.Name(), os.O_RDWR, 0)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = opened.Close() }()
			ports := test.ports
			if ports.AfterTruncate != nil {
				ports.AfterTruncate = func() { _ = opened.Close() }
			}
			if ports.AfterSeek != nil {
				ports.AfterSeek = func() { _ = opened.Close() }
			}
			if ports.AfterWrite != nil {
				ports.AfterWrite = func() { _ = opened.Close() }
			}
			if err := WriteOperationLockMetadata(opened, "task", 1, ports); err == nil || !strings.Contains(err.Error(), test.name) {
				t.Fatalf("%s fault = %v", test.name, err)
			}
		})
	}
}

func TestCleanupPurgeSkipsChangedAndLinkedRetirements(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	taskPath := filepath.Join(root, "task")
	if err := os.Mkdir(taskPath, 0o700); err != nil {
		t.Fatal(err)
	}
	openTask := func() *CleanupTask {
		file, err := os.Open(taskPath)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		return &CleanupTask{Task: file}
	}
	retired := filepath.Join(taskPath, ".wb-retired-lock-test")
	if err := os.WriteFile(retired, []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	task := openTask()
	task.AfterPurgeRead = func() { _ = os.Remove(retired) }
	PurgeTerminalTaskLockDebris(task)
	if err := os.WriteFile(retired, []byte("retired"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(retired, filepath.Join(root, "external")); err != nil {
		t.Fatal(err)
	}
	PurgeTerminalTaskLockDebris(openTask())
	if _, err := os.Stat(retired); err != nil {
		t.Fatalf("hardlinked retirement removed: %v", err)
	}
	closed := openTask()
	if err := closed.Task.Close(); err != nil {
		t.Fatal(err)
	}
	PurgeTerminalTaskLockDebris(closed)
	readFault := openTask()
	readFault.AfterPurgeRewind = func() { _ = readFault.Task.Close() }
	PurgeTerminalTaskLockDebris(readFault)
}

func TestRepositoryRegistrationRetriesOnlyValidatedTransientOpen(t *testing.T) {
	t.Parallel()
	directory, _ := openLockDirectory(t)
	now := time.Now
	ports := RepositoryRegistrationPorts{Openat: func(int, string, int, uint32) (int, error) { return -1, unix.ENOENT }}
	if _, err := AcquireRepositoryRegistrationLock(directory, func() error { return errors.New("changed git directory") }, now, time.Sleep, time.Second, ports); err == nil || !strings.Contains(err.Error(), "canonical validation") {
		t.Fatalf("invalid canonical = %v", err)
	}
	clock := time.Now()
	fakeNow := func() time.Time { return clock }
	fakeSleep := func(delay time.Duration) { clock = clock.Add(delay) }
	if _, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, fakeNow, fakeSleep, 0, ports); err == nil || !strings.Contains(err.Error(), "open repository registration lock after") {
		t.Fatalf("transient timeout = %v", err)
	}
	badFlock := RepositoryRegistrationPorts{Flock: func(int, int) error { return errors.New("bad descriptor") }}
	if _, err := AcquireRepositoryRegistrationLock(directory, func() error { return nil }, now, time.Sleep, time.Second, badFlock); err == nil || !strings.Contains(err.Error(), "hold repository registration lock") {
		t.Fatalf("flock failure = %v", err)
	}
}

func TestClaimFenceReportsKernelLockFailure(t *testing.T) {
	t.Parallel()
	home := testCanonicalTemp(t)
	valid := func(value string) bool { return value != "" }
	run, _, err := OpenWorkLogRun(home, "effort", "run", true, valid)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = run.Close() })
	if _, err := LockClaim(run, "claim", valid, ClaimLockPorts{AfterOpen: func(fd int) { _ = unix.Close(fd) }}); err == nil {
		t.Fatal("closed claim descriptor accepted")
	}
}

func TestCleanupRejectsUnavailableRootsAndPreparedLockConflict(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	missing := filepath.Join(root, "missing")
	ports := CleanupLockPorts{PID: func() int { return 1 }}
	if _, err := ports.AcquireCleanupTaskAt(missing, "task"); err == nil {
		t.Fatal("missing cleanup root accepted")
	}
	if _, err := ports.AcquireCleanupTaskAtReclaimingInterruptedLock(missing, "task"); err == nil {
		t.Fatal("missing recovery root accepted")
	}
	worktreesRoot := filepath.Join(root, "worktrees")
	if err := os.Mkdir(worktreesRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ports.AcquireCleanupTaskAtReclaimingInterruptedLock(worktreesRoot, "missing"); err == nil {
		t.Fatal("missing recovery task accepted")
	}
	ports.PrepareTask = func(home, name string) (*CleanupTask, error) {
		path := filepath.Join(worktreesRoot, name)
		if err := os.Mkdir(path, 0o700); err != nil {
			return nil, err
		}
		if err := os.WriteFile(filepath.Join(path, ".lock"), []byte("operation=task\npid=1\n"), 0o600); err != nil {
			return nil, err
		}
		worktrees, err := os.Open(worktreesRoot)
		if err != nil {
			return nil, err
		}
		directory, err := os.Open(path)
		if err != nil {
			_ = worktrees.Close()
			return nil, err
		}
		return &CleanupTask{WorktreesPath: worktreesRoot, TaskPath: path, Worktrees: worktrees, Task: directory}, nil
	}
	if _, err := ports.AcquireCleanupTaskAtOrCreate(worktreesRoot, "task"); err == nil {
		t.Fatal("prepared conflicting lock accepted")
	}
}

func TestNamedRecoverySkipsMissingRootAndRejectsUnreadableRoot(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	missingRoot := filepath.Join(root, "missing")
	badRoot := filepath.Join(root, "file")
	if err := os.WriteFile(badRoot, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ports := CleanupLockPorts{}
	resolution := wbhome.Resolution{Write: wbhome.Layout{Home: root}, Read: []wbhome.Layout{{Home: root, WorktreesRoot: missingRoot, Legacy: true}}}
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task"); err == nil || !strings.Contains(err.Error(), "exactly one") {
		t.Fatalf("missing root = %v", err)
	}
	resolution.Read = append(resolution.Read, wbhome.Layout{Home: root, WorktreesRoot: badRoot, Legacy: true})
	if _, _, err := ports.ReclaimNamedInterruptedCleanupTask(resolution, "task"); err == nil || !strings.Contains(err.Error(), "open recovery worktrees root") {
		t.Fatalf("non-directory root = %v", err)
	}
}

func TestQuarantineReportsMovedDescriptorFailureAndFixedName(t *testing.T) {
	t.Parallel()
	directory, root := openLockDirectory(t)
	lock, err := AcquireLockAt(directory, "task", 1)
	if err != nil {
		t.Fatal(err)
	}
	ports := OperationLockPorts{RetiredToken: func() string { return "fixed" }, MoveQuarantine: func(_ *os.File, _ string, _ ManagedLockIdentity) (*os.File, error) {
		file, err := os.Open(filepath.Join(root, ".lock"))
		if err != nil {
			return nil, err
		}
		return file, errors.New("move failed")
	}}
	if err := lock.Release(ports); err == nil || !strings.Contains(err.Error(), "move failed") {
		t.Fatalf("failed move = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, ".lock")); err != nil {
		t.Fatalf("lock lost after failed move: %v", err)
	}
}

func TestCanonicalPlacementRefusesMixedRootsAndNonForgeHosts(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	if CanonicalDirMatchesRepository("relative", "acme/app", filepath.Join(root, "acme", "app")) {
		t.Fatal("mixed absolute and relative roots accepted")
	}
	if CanonicalDirMatchesRepository(root, "acme/app", filepath.Join(root, "not-a-host", "acme", "app")) {
		t.Fatal("non-forge host accepted")
	}
}

func TestWorkLogOutboxRequiresExistingHierarchyWhenNotCreating(t *testing.T) {
	t.Parallel()
	root := testCanonicalTemp(t)
	valid := func(value string) bool { return value != "" }
	if _, err := OpenWorkLogOutbox(filepath.Join(root, "missing"), "effort", false, valid); err == nil {
		t.Fatal("missing home accepted")
	}
	if err := os.Mkdir(filepath.Join(root, "worklogs"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenWorkLogOutbox(root, "missing", false, valid); err == nil {
		t.Fatal("missing effort accepted")
	}
}

func TestHeldOperationLockRefusesLiveHolder(t *testing.T) {
	t.Parallel()
	directory, _ := openLockDirectory(t)
	first, err := AcquireOperationLock(directory, false, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = first.Release() })
	if _, err := AcquireOperationLock(directory, false, 2); !errors.Is(err, ErrOperationLockHeld) {
		t.Fatalf("live holder = %v", err)
	}
}
