package worktreeclaims

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

// CleanupLockPorts supplies only the observations and preparation policy owned by
// the worktrees facade. One acquisition owns one set of ports.
type CleanupLockPorts struct {
	PrepareTask   func(home, task string) (*CleanupTask, error)
	ProcessIsDead func(int) bool
	PID           func() int
	// AfterTaskOpen is an operation-local test seam for a path replacement
	// between descriptor acquisition and the final path validation.
	AfterTaskOpen func()
}
type CleanupTask struct {
	WorktreesPath    string
	TaskPath         string
	Worktrees        *os.File
	Task             *os.File
	Lock             OperationLock
	AfterPurgeRewind func()
	AfterPurgeRead   func()
}

func (task *CleanupTask) Close() {
	if task == nil {
		return
	}
	if task.Task != nil {
		_ = task.Task.Close()
	}
	if task.Worktrees != nil {
		_ = task.Worktrees.Close()
	}
}
func (task *CleanupTask) Validate() error {
	if !worktreesecure.DirectoryStillMatches(task.WorktreesPath, task.Worktrees) {
		return fmt.Errorf("cleanup worktrees root path changed: %s", task.WorktreesPath)
	}
	if !worktreesecure.DirectoryStillMatches(task.TaskPath, task.Task) {
		return fmt.Errorf("cleanup task path changed: %s", task.TaskPath)
	}
	return nil
}
func (ports CleanupLockPorts) AcquireCleanupTaskAt(worktreesRoot, taskName string) (*CleanupTask, error) {
	return ports.AcquireCleanupTaskAtReclaimingInterrupted(worktreesRoot, taskName, false)
}
func (ports CleanupLockPorts) AcquireCleanupTaskAtOrCreate(worktreesRoot, taskName string) (*CleanupTask, error) {
	task, err := ports.AcquireCleanupTaskAt(worktreesRoot, taskName)
	if err == nil || !errors.Is(err, os.ErrNotExist) {
		return task, err
	}
	home := filepath.Dir(filepath.Clean(worktreesRoot))
	task, err = ports.PrepareTask(home, taskName)
	if err != nil {
		return nil, err
	}
	lock, err := AcquireLockAt(task.Task, taskName, ports.PID())
	if err != nil {
		task.Close()
		return nil, err
	}
	task.Lock = lock
	return task, nil
}
func PurgeTerminalTaskLockDebris(task *CleanupTask) {
	if task == nil || task.Task == nil {
		return
	}
	if _, err := task.Task.Seek(0, 0); err != nil {
		return
	}
	if task.AfterPurgeRewind != nil {
		task.AfterPurgeRewind()
	}
	entries, err := task.Task.ReadDir(-1)
	if err != nil {
		return
	}
	if task.AfterPurgeRead != nil {
		task.AfterPurgeRead()
	}
	retired := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasPrefix(name, ".wb-retired-lock-") {
			return
		}
		retired = append(retired, name)
	}
	for _, name := range retired {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(task.Task.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			continue
		}
		if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
			continue
		}
		_ = unix.Unlinkat(int(task.Task.Fd()), name, 0)
	}
}
func (ports CleanupLockPorts) AcquireCleanupTaskAtReclaimingInterrupted(worktreesRoot, taskName string, reclaimInterrupted bool) (*CleanupTask, error) {
	worktrees, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open cleanup worktrees root %s: %w", worktreesRoot, err)
	}
	task, err := worktreesecure.OpenDirectoryAtNoFollow(int(worktrees.Fd()), taskName, "wb-cleanup-task", "open cleanup task "+taskName+" without following links")
	if err != nil {
		_ = worktrees.Close()
		return nil, err
	}
	handle := &CleanupTask{WorktreesPath: worktreesRoot, TaskPath: filepath.Join(worktreesRoot, taskName), Worktrees: worktrees, Task: task}
	if ports.AfterTaskOpen != nil {
		ports.AfterTaskOpen()
	}
	if err := handle.Validate(); err != nil {
		handle.Close()
		return nil, err
	}
	lock, err := AcquireLockAtReclaimingInterrupted(task, reclaimInterrupted, taskName, ports.PID())
	if err != nil {
		handle.Close()
		return nil, fmt.Errorf("lock cleanup task %s: %w", taskName, err)
	}
	handle.Lock = lock
	return handle, nil
}

type InterruptedLockRecovery struct {
	Task          string `json:"task"`
	WorktreesRoot string `json:"worktrees_root"`
	Path          string `json:"path"`
	PID           int    `json:"pid"`
	Disposition   string `json:"disposition"`
	Applied       bool   `json:"applied"`
	Reason        string `json:"reason,omitempty"`
}

func (ports CleanupLockPorts) ReclaimNamedInterruptedCleanupTask(resolution wbhome.Resolution, taskName string) (*CleanupTask, *InterruptedLockRecovery, error) {
	roots := make([]string, 0, 1)
	seenRoots := make(map[string]bool)
	for _, layout := range resolution.Read {
		root := filepath.Clean(lifecycleTaskLockRoot(resolution.Write.Home, layout))
		if seenRoots[root] {
			continue
		}
		seenRoots[root] = true
		worktrees, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(root, false)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, nil, fmt.Errorf("open recovery worktrees root %s: %w", root, err)
		}
		fd, openErr := unix.Openat(int(worktrees.Fd()), taskName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
		_ = worktrees.Close()
		if openErr != nil {
			if errors.Is(openErr, unix.ENOENT) {
				continue
			}
			return nil, nil, fmt.Errorf("open recovery task %s without following links: %w", taskName, openErr)
		}
		_ = unix.Close(fd)
		roots = append(roots, root)
	}
	if len(roots) != 1 {
		return nil, nil, fmt.Errorf("interrupted recovery for task %q requires exactly one WB task directory, found %d", taskName, len(roots))
	}
	handle, err := ports.AcquireCleanupTaskAtReclaimingInterruptedLock(roots[0], taskName)
	if err != nil {
		return nil, nil, err
	}
	pid, validateErr := InterruptedTaskLockPID(handle.Lock.file, taskName, ports.ProcessIsDead)
	if validateErr != nil {
		handle.PreserveLock()
		handle.Close()
		return nil, nil, validateErr
	}
	recovery := &InterruptedLockRecovery{Task: taskName, WorktreesRoot: roots[0], Path: filepath.Join(roots[0], taskName, ".lock"), PID: pid, Disposition: "validated", Reason: "exact interrupted lock has a conclusively dead owner PID"}
	return handle, recovery, nil
}
func lifecycleTaskLockRoot(home string, layout wbhome.Layout) string {
	current := filepath.Join(home, "worktrees")
	if layout.Local || (!layout.Legacy && filepath.Clean(layout.WorktreesRoot) != filepath.Clean(current)) {
		return current
	}
	return layout.WorktreesRoot
}
func (ports CleanupLockPorts) AcquireCleanupTaskAtReclaimingInterruptedLock(worktreesRoot, taskName string) (*CleanupTask, error) {
	worktrees, err := worktreesecure.OpenAbsoluteDirectoryNoFollow(worktreesRoot, false)
	if err != nil {
		return nil, fmt.Errorf("open recovery worktrees root %s: %w", worktreesRoot, err)
	}
	task, err := worktreesecure.OpenDirectoryAtNoFollow(int(worktrees.Fd()), taskName, "wb-recovery-task", "open recovery task "+taskName+" without following links")
	if err != nil {
		_ = worktrees.Close()
		return nil, err
	}
	handle := &CleanupTask{WorktreesPath: worktreesRoot, TaskPath: filepath.Join(worktreesRoot, taskName), Worktrees: worktrees, Task: task}
	if ports.AfterTaskOpen != nil {
		ports.AfterTaskOpen()
	}
	if err := handle.Validate(); err != nil {
		handle.Close()
		return nil, err
	}
	lock, err := ReclaimInterruptedLock(task, true)
	if err != nil {
		handle.Close()
		return nil, fmt.Errorf("recover interrupted cleanup task %s: %w", taskName, err)
	}
	handle.Lock = lock
	return handle, nil
}
func (task *CleanupTask) PreserveLock() {
	if task == nil || task.Lock.file == nil {
		return
	}
	_ = task.Lock.file.Close()
	task.Lock = OperationLock{}
}
func (task *CleanupTask) ValidateHeldLock() error {
	if task == nil || task.Task == nil || task.Lock.file == nil || !LockEntryStillMatches(task.Task, ".lock", task.Lock.identity) {
		return fmt.Errorf("interrupted cleanup lock changed after recovery")
	}
	return nil
}
func ValidateRecoveredCleanupLock(recovered bool, task *CleanupTask) error {
	if !recovered {
		return nil
	}
	return task.ValidateHeldLock()
}
func InterruptedTaskLockPID(file *os.File, task string, processIsDead func(int) bool) (int, error) {
	if file == nil {
		return 0, fmt.Errorf("interrupted task %q lock descriptor is unavailable", task)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("seek interrupted task %q lock: %w", task, err)
	}
	contents, err := io.ReadAll(io.LimitReader(file, 4097))
	if err != nil || len(contents) > 4096 {
		return 0, fmt.Errorf("interrupted task %q lock metadata is invalid", task)
	}
	lines := strings.Split(string(contents), "\n")
	if len(lines) != 3 || lines[2] != "" || lines[0] != "operation="+task {
		return 0, fmt.Errorf("interrupted task %q lock metadata is invalid", task)
	}
	pid, err := strconv.Atoi(strings.TrimPrefix(lines[1], "pid="))
	if err != nil || pid <= 0 || lines[1] != fmt.Sprintf("pid=%d", pid) {
		return 0, fmt.Errorf("interrupted task %q lock metadata is invalid", task)
	}
	if !processIsDead(pid) {
		return 0, fmt.Errorf("interrupted task %q lock owner PID %d is live or ambiguous", task, pid)
	}
	return pid, nil
}
