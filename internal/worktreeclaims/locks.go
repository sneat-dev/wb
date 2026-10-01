package worktreeclaims

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/repopath"
	"github.com/sneat-dev/wb/internal/worktreelayout"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/filewrite"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreesecure"
)

func randomHexToken(byteCount int) string {
	b := make([]byte, byteCount)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type OperationLock struct {
	directory     *os.File
	file          *os.File
	identity      ManagedLockIdentity
	beforeRelease func()
	interrupted   bool
}

// OperationLockPorts carries operation-local fault and race seams. Empty ports
// use the real filesystem; tests can stop at one precise descriptor boundary.
type OperationLockPorts struct {
	AfterClaim           func()
	AfterOpen            func(*os.File)
	AfterHold            func(*os.File)
	AfterInspect         func(*os.File)
	AfterReclaimOpen     func(*os.File)
	AfterReclaimIdentity func(*os.File)
	AfterRetiredRewind   func(*os.File)
	MoveRetired          func(*os.File, string, ManagedLockIdentity) (*os.File, error)
	MoveQuarantine       func(*os.File, string, ManagedLockIdentity) (*os.File, error)
	RetiredToken         func() string
	StatDirectory        func(int, *unix.Stat_t) error
}

func firstOperationLockPorts(values []OperationLockPorts) OperationLockPorts {
	if len(values) == 0 {
		return OperationLockPorts{}
	}
	return values[0]
}

type ManagedLockIdentity struct {
	device uint64
	inode  uint64
}

func NewManagedLockIdentity(device, inode uint64) ManagedLockIdentity {
	return ManagedLockIdentity{device: device, inode: inode}
}

func (identity ManagedLockIdentity) Components() (device, inode uint64) {
	return identity.device, identity.inode
}

func NewOperationLock(directory, file *os.File, identity ManagedLockIdentity, beforeRelease func(), interrupted bool) OperationLock {
	return OperationLock{directory: directory, file: file, identity: identity, beforeRelease: beforeRelease, interrupted: interrupted}
}

func (lock OperationLock) Components() (directory, file *os.File, identity ManagedLockIdentity, beforeRelease func(), interrupted bool) {
	return lock.directory, lock.file, lock.identity, lock.beforeRelease, lock.interrupted
}

// AcquireLockAt is the descriptor-relative form used while creating a new
// operation. It never follows a worktrees or task ancestor that was swapped
// after the operation directory was opened.
func AcquireLockAt(operationDirectory *os.File, operation string, pid int) (OperationLock, error) {
	return AcquireLockAtReclaimingInterrupted(operationDirectory, false, operation, pid)
}

// HeldOperationLock is a descriptor-anchored operation lock for another WB
// subsystem that needs the same no-follow, liveness, and successor-preserving
// behavior as managed worktree operations.
type HeldOperationLock struct {
	lock *OperationLock
}

// AcquireOperationLock acquires the `.lock` entry below directory. When
// reclaimInterrupted is true, an unheld, single-link regular remnant is held
// for the caller to validate before resuming. Call Preserve when validation
// fails; it closes the descriptor without changing that ambiguous remnant.
func AcquireOperationLock(directory *os.File, reclaimInterrupted bool, pid int) (*HeldOperationLock, error) {
	lock, err := AcquireLockAtReclaimingInterrupted(directory, reclaimInterrupted, "", pid)
	if err != nil {
		return nil, err
	}
	return &HeldOperationLock{lock: &lock}, nil
}

// File returns the held lock descriptor. It remains owned by the lock.
func (lock *HeldOperationLock) File() *os.File {
	if lock == nil || lock.lock == nil {
		return nil
	}
	return lock.lock.file
}

// ReclaimedInterrupted reports whether the lock was a lingering `.lock`
// remnant rather than a fresh or properly retired entry.
func (lock *HeldOperationLock) ReclaimedInterrupted() bool {
	return lock != nil && lock.lock != nil && lock.lock.interrupted
}

// Release retires the exact held inode with a descriptor-relative no-replace
// move. It cannot unlink a successor lock installed after acquisition.
//
// The returned error matters to callers whose audit record claims terminal
// ownership: a late successor or a failed quarantine is not a release.
func (lock *HeldOperationLock) Release() error {
	if lock == nil || lock.lock == nil {
		return nil
	}
	err := lock.lock.Release()
	lock.lock = nil
	return err
}

// Preserve leaves the currently named lock entry untouched. It is for a
// caller that acquired an unheld remnant but could not prove ownership.
func (lock *HeldOperationLock) Preserve() {
	if lock == nil || lock.lock == nil {
		return
	}
	_ = lock.lock.file.Close()
	lock.lock = nil
}

// AcquireLockAtReclaimingInterrupted separates the two conditions a lingering
// .lock can mean. A live operation holds an exclusive kernel lock on that
// file, which the kernel drops when its process dies; an interrupted one
// leaves the entry with nothing holding it. Existence alone cannot tell them
// apart, and treating both as fatal is what stranded an interrupted cleanup:
// leaving .lock behind IS how interruption presents, so the resume path could
// never take the lock it needs to finish.
//
// reclaimInterrupted is therefore granted only to a caller holding its own
// durable record of exactly what remains, which it revalidates independently
// before deleting anything (see resumeLifecycleBacklog). Every other caller
// still refuses, so an interruption whose remnants nobody can describe keeps
// demanding attention. A live holder is refused in both modes.
func AcquireLockAtReclaimingInterrupted(operationDirectory *os.File, reclaimInterrupted bool, operation string, pid int, options ...OperationLockPorts) (OperationLock, error) {
	ports := firstOperationLockPorts(options)
	file, reused, err := ClaimRetiredLock(operationDirectory, ports)
	if err != nil {
		return OperationLock{}, err
	}
	if ports.AfterClaim != nil {
		ports.AfterClaim()
	}
	if !reused {
		fd, openErr := unix.Openat(int(operationDirectory.Fd()), ".lock", unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0o600)
		if openErr != nil {
			if errors.Is(openErr, unix.EEXIST) {
				return ReclaimInterruptedLock(operationDirectory, reclaimInterrupted, ports)
			}
			return OperationLock{}, fmt.Errorf("acquire secure worktree operation lock: %w", openErr)
		}
		file = os.NewFile(uintptr(fd), "wb-worktree-operation-lock")
	}
	if ports.AfterOpen != nil {
		ports.AfterOpen(file)
	}
	if err := HoldOperationLock(file); err != nil {
		_ = file.Close()
		return OperationLock{}, err
	}
	if ports.AfterHold != nil {
		ports.AfterHold(file)
	}
	// The lock proves no other WB process is working in this directory. It does
	// not prove the directory is still reachable: an empty task namespace can be
	// retired between the moment this operation opened it and the moment it took
	// the lock, and creating .lock inside an unlinked directory succeeds, so
	// without this check the operation would build a whole task hierarchy no
	// path can reach. A directory that has been unlinked has no links left —
	// neither its entry in the parent nor its own "." — which is exactly the
	// distinction the descriptor can still make after the name is gone.
	// Refusing here makes the race a retryable error instead of silent loss,
	// and is what allows a terminal cleanup to retire the namespace it emptied
	// rather than leaving one empty shell behind per finished task.
	var directory unix.Stat_t
	statDirectory := unix.Fstat
	if ports.StatDirectory != nil {
		statDirectory = ports.StatDirectory
	}
	if err := statDirectory(int(operationDirectory.Fd()), &directory); err != nil {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf("inspect secure worktree operation directory: %w", err)
	}
	if directory.Nlink == 0 {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf("worktree operation directory was retired while this operation was starting; run the command again")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf("inspect secure worktree operation lock: %w", err)
	}
	lock := OperationLock{
		directory: operationDirectory,
		file:      file,
		identity:  ManagedLockIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)},
	}
	if ports.AfterInspect != nil {
		ports.AfterInspect(file)
	}
	// Fresh and retired-reused locks record exact ownership so a later
	// --resume-interrupted path can prove the owner PID is dead. Reclaimed
	// interrupted remnants keep their original metadata for that proof.
	if strings.TrimSpace(operation) != "" {
		if err := WriteOperationLockMetadata(file, operation, pid); err != nil {
			_ = file.Close()
			return OperationLock{}, err
		}
	}
	return lock, nil
}

type LockMetadataPorts struct {
	AfterTruncate func()
	AfterSeek     func()
	AfterWrite    func()
}

func WriteOperationLockMetadata(file *os.File, operation string, pid int, options ...LockMetadataPorts) error {
	var ports LockMetadataPorts
	if len(options) > 0 {
		ports = options[0]
	}
	operation = strings.TrimSpace(operation)
	if operation == "" || strings.ContainsAny(operation, "\n\r\x00") {
		return fmt.Errorf("operation lock metadata requires a single-line operation name")
	}
	if err := file.Truncate(0); err != nil {
		return fmt.Errorf("initialize worktree operation lock: %w", err)
	}
	if ports.AfterTruncate != nil {
		ports.AfterTruncate()
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("seek worktree operation lock: %w", err)
	}
	if ports.AfterSeek != nil {
		ports.AfterSeek()
	}
	if _, err := fmt.Fprintf(file, "operation=%s\npid=%d\n", operation, pid); err != nil {
		return fmt.Errorf("write worktree operation lock metadata: %w", err)
	}
	if ports.AfterWrite != nil {
		ports.AfterWrite()
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync worktree operation lock metadata: %w", err)
	}
	return nil
}

// ErrOperationLockHeld is the sentinel behind "already active" contention on
// an operation lock, distinguished from every other acquisition failure so a
// caller that wants a more specific, task-named refusal (see Create's use of
// this below) can recognize exactly this condition with errors.Is rather than
// matching on error text.
var ErrOperationLockHeld = errors.New("worktree operation is already active in another process")

// HoldOperationLock takes the exclusive kernel lock this operation keeps for
// its whole lifetime, so a concurrent WB process is refused while it runs and
// the kernel releases it if the process dies. Closing the descriptor releases
// it, which every release path already does.
func HoldOperationLock(file *os.File) error {
	if err := unix.Flock(int(file.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		if errors.Is(err, unix.EWOULDBLOCK) {
			return ErrOperationLockHeld
		}
		return fmt.Errorf("hold secure worktree operation lock: %w", err)
	}
	return nil
}

// ReclaimInterruptedLock inspects an existing .lock without creating,
// replacing, or following one. It reports the accurate condition even when it
// refuses, so an operator can tell "another WB is running" from "a previous WB
// died here" instead of reading one message that means either.
func ReclaimInterruptedLock(operationDirectory *os.File, reclaimInterrupted bool, options ...OperationLockPorts) (OperationLock, error) {
	ports := firstOperationLockPorts(options)
	fd, err := unix.Openat(int(operationDirectory.Fd()), ".lock", unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		// The entry vanished or is not a plain file WB may hold; stay closed.
		return OperationLock{}, fmt.Errorf("worktree operation is already active or was interrupted")
	}
	file := os.NewFile(uintptr(fd), "wb-worktree-operation-lock")
	if ports.AfterReclaimOpen != nil {
		ports.AfterReclaimOpen(file)
	}
	identity, err := ExclusivelyOwnedLockIdentity(file)
	if err != nil {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf("worktree operation is already active or was interrupted")
	}
	if ports.AfterReclaimIdentity != nil {
		ports.AfterReclaimIdentity(file)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf("inspect existing worktree operation lock: %w", err)
	}
	// O_EXCL publishes the new directory entry immediately before its creator
	// can take flock and write ownership metadata. A contender that flocks that
	// still-empty inode can beat the creator, causing the creator to lose and
	// then classifying its own briefly held lock as an interrupted remnant. In a
	// create stampede every contender can do that in turn, leaving no winner.
	// Empty metadata is never safe to reclaim (resume cannot prove an owner), so
	// treat this creation window as live contention and leave the exact inode
	// untouched for its creator.
	if info.Size() == 0 {
		_ = file.Close()
		return OperationLock{}, ErrOperationLockHeld
	}
	if err := HoldOperationLock(file); err != nil {
		_ = file.Close()
		return OperationLock{}, err
	}
	if ports.AfterHold != nil {
		ports.AfterHold(file)
	}
	// Nothing held it: this is an interrupted remnant, not a live operation.
	if !reclaimInterrupted {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf(
			"worktree operation was interrupted and left its lock behind; " +
				"the durable cleanup backlog for this task can finish it",
		)
	}
	// Re-verify the entry still names this exact file now that the lock is
	// held, so a swap between open and lock cannot be inherited.
	if !LockEntryStillMatches(operationDirectory, ".lock", identity) {
		_ = file.Close()
		return OperationLock{}, fmt.Errorf("secure worktree operation lock changed while being reclaimed")
	}
	return OperationLock{directory: operationDirectory, file: file, identity: identity, interrupted: true}, nil
}

func ClaimRetiredLock(directory *os.File, options ...OperationLockPorts) (*os.File, bool, error) {
	ports := firstOperationLockPorts(options)
	if _, err := directory.Seek(0, 0); err != nil {
		return nil, false, fmt.Errorf("rewind secure operation directory for lock reap: %w", err)
	}
	if ports.AfterRetiredRewind != nil {
		ports.AfterRetiredRewind(directory)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, false, fmt.Errorf("read secure operation directory for lock reap: %w", err)
	}
	for _, entry := range entries {
		if !strings.HasPrefix(entry.Name(), ".wb-retired-lock-") {
			continue
		}
		fd, openErr := unix.Openat(int(directory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if openErr != nil {
			continue
		}
		retired := os.NewFile(uintptr(fd), "wb-retired-lock-claim")
		identity, identityErr := ExclusivelyOwnedLockIdentity(retired)
		if identityErr != nil {
			_ = retired.Close()
			continue
		}
		move := func() (*os.File, error) { return MoveExpectedLockNoReplace(directory, entry.Name(), ".lock", identity) }
		if ports.MoveRetired != nil {
			move = func() (*os.File, error) { return ports.MoveRetired(directory, entry.Name(), identity) }
		}
		claimed, moveErr := move()
		_ = retired.Close()
		if errors.Is(moveErr, unix.EEXIST) {
			return nil, false, nil // an active lock won; do not inspect another retirement.
		}
		if moveErr != nil {
			if claimed != nil {
				_ = claimed.Close()
			}
			if errors.Is(moveErr, worktreesecure.ErrDirectoryMoveIdentityChanged) {
				return nil, false, fmt.Errorf("reclaim retired operation lock %s: %w", entry.Name(), moveErr)
			}
			continue // stale/replaced retirement remains untouched.
		}
		return claimed, true, nil
	}
	return nil, false, nil
}

func (lock OperationLock) Release(options ...OperationLockPorts) error {
	if lock.file == nil || lock.directory == nil {
		return nil
	}
	defer func() { _ = lock.file.Close() }()
	if !LockEntryStillMatches(lock.directory, ".lock", lock.identity) {
		return fmt.Errorf("operation lock changed before retirement")
	}
	if lock.beforeRelease != nil {
		lock.beforeRelease()
	}
	if err := QuarantineLockEntry(lock.directory, lock.identity, options...); err != nil {
		return fmt.Errorf("quarantine operation lock: %w", err)
	}
	// A fork or duplicate can retain this open-file-description after Close.
	// Unlock only the owned inode after its exact retirement has succeeded.
	if err := unix.Flock(int(lock.file.Fd()), unix.LOCK_UN); err != nil {
		return fmt.Errorf("unlock retired operation lock: %w", err)
	}
	return nil
}

func LockEntryStillMatches(directory *os.File, name string, expected ManagedLockIdentity) bool {
	var stat unix.Stat_t
	if err := unix.Fstatat(int(directory.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return false
	}
	return stat.Mode&unix.S_IFMT == unix.S_IFREG && uint64(stat.Dev) == expected.device && uint64(stat.Ino) == expected.inode
}

func LockIdentity(file *os.File) (ManagedLockIdentity, error) {
	if file == nil {
		return ManagedLockIdentity{}, fmt.Errorf("lock descriptor is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return ManagedLockIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return ManagedLockIdentity{}, fmt.Errorf("lock descriptor is not a regular file")
	}
	return ManagedLockIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

// ExclusivelyOwnedLockIdentity accepts only a retirement WB could have made
// itself. A lock is created with one directory entry and a rename preserves
// that count. In particular, never claim a hard-linked lookalike: even though
// lock acquisition is read-only, leaving an unowned entry untouched keeps the
// namespace and the external file fully outside WB's lifecycle.
func ExclusivelyOwnedLockIdentity(file *os.File) (ManagedLockIdentity, error) {
	if file == nil {
		return ManagedLockIdentity{}, fmt.Errorf("lock descriptor is unavailable")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil {
		return ManagedLockIdentity{}, err
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG {
		return ManagedLockIdentity{}, fmt.Errorf("lock descriptor is not a regular file")
	}
	if stat.Nlink != 1 {
		return ManagedLockIdentity{}, fmt.Errorf("lock retirement has %d links", stat.Nlink)
	}
	return ManagedLockIdentity{device: uint64(stat.Dev), inode: uint64(stat.Ino)}, nil
}

type MoveExpectedLockHooks struct {
	AfterMove     func()
	AfterOpen     func()
	BeforeRestore func()
}

func MoveExpectedLockNoReplace(directory *os.File, fromName, toName string, expected ManagedLockIdentity, hooks ...MoveExpectedLockHooks) (*os.File, error) {
	if !LockEntryStillMatches(directory, fromName, expected) {
		return nil, fmt.Errorf("%w: operation lock %s changed before move", worktreesecure.ErrDirectoryMoveIdentityChanged, fromName)
	}
	if err := filewrite.RenameNoReplace(int(directory.Fd()), fromName, int(directory.Fd()), toName, nil); err != nil {
		return nil, err
	}
	var hook MoveExpectedLockHooks
	if len(hooks) > 0 {
		hook = hooks[0]
	}
	if hook.AfterMove != nil {
		hook.AfterMove()
	}
	fd, err := unix.Openat(int(directory.Fd()), toName, unix.O_RDWR|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open moved operation lock %s: %w", toName, err)
	}
	moved := os.NewFile(uintptr(fd), "wb-moved-operation-lock")
	if hook.AfterOpen != nil {
		hook.AfterOpen()
	}
	actual, identityErr := LockIdentity(moved)
	sourceAbsent, absentErr := worktreesecure.NoFollowChildAbsent(int(directory.Fd()), fromName)
	if identityErr == nil && actual == expected && absentErr == nil && sourceAbsent {
		return moved, nil
	}
	if absentErr != nil {
		_ = moved.Close()
		return nil, fmt.Errorf("inspect operation lock %s after move: %w", fromName, absentErr)
	}
	if !sourceAbsent {
		return moved, fmt.Errorf("%w: operation lock %s was recreated after no-replace move", worktreesecure.ErrDirectoryMoveIdentityChanged, fromName)
	}
	_ = moved.Close()
	if hook.BeforeRestore != nil {
		hook.BeforeRestore()
	}
	if restoreErr := filewrite.RenameNoReplace(int(directory.Fd()), toName, int(directory.Fd()), fromName, nil); restoreErr != nil {
		return nil, fmt.Errorf("%w: operation lock %s changed before restoration: %v", worktreesecure.ErrDirectoryMoveIdentityChanged, toName, restoreErr)
	}
	return nil, fmt.Errorf("%w: operation lock %s was not the expected file", worktreesecure.ErrDirectoryMoveIdentityChanged, toName)
}

// QuarantineLockEntry retires the exact lock inode. It never unlinks `.lock`,
// so a successor created after the final authorization cannot be deleted by a
// previous operation finishing late.
func QuarantineLockEntry(directory *os.File, expected ManagedLockIdentity, options ...OperationLockPorts) error {
	ports := firstOperationLockPorts(options)
	for attempt := 0; attempt < 16; attempt++ {
		token := randomHexToken(16)
		if ports.RetiredToken != nil {
			token = ports.RetiredToken()
		}
		name := ".wb-retired-lock-" + token
		move := func() (*os.File, error) { return MoveExpectedLockNoReplace(directory, ".lock", name, expected) }
		if ports.MoveQuarantine != nil {
			move = func() (*os.File, error) { return ports.MoveQuarantine(directory, name, expected) }
		}
		moved, err := move()
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			if moved != nil {
				_ = moved.Close()
			}
			return err
		}
		return moved.Close()
	}
	return fmt.Errorf("create collision-free retired lock name")
}

// CanonicalDirMatchesRepository reports whether dir is a valid canonical clone
// placement for repository below projectsRoot. It accepts the host-qualified
// <root>/{host}/{owner}/{repository} placement, and the legacy
// <root>/{owner}/{repository} placement for an unqualified coordinate, so a
// durable record written before the host level existed keeps validating.
func CanonicalDirMatchesRepository(projectsRoot, repository, dir string) bool {
	address, err := worktreelayout.SplitRepositoryAddress(repository)
	if err != nil {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(projectsRoot), filepath.Clean(dir))
	if err != nil {
		return false
	}
	parts := strings.Split(filepath.ToSlash(relative), "/")
	switch len(parts) {
	case 2:
		return address.Host == "" && strings.EqualFold(parts[0], address.Org) && strings.EqualFold(parts[1], address.Repo)
	case 3:
		// A host-qualified coordinate must sit under its own literal host. An
		// unqualified {owner}/{repository} coordinate carries no host to check,
		// so any literal forge level is accepted: the durable record's own
		// CanonicalDir is the authority for where that clone was placed.
		if !repopath.IsForgeHost(parts[0]) {
			return false
		}
		if address.Host != "" && !strings.EqualFold(parts[0], address.Host) {
			return false
		}
		return strings.EqualFold(parts[1], address.Org) && strings.EqualFold(parts[2], address.Repo)
	default:
		return false
	}
}
