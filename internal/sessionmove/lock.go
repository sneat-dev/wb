package sessionmove

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"golang.org/x/sys/unix"
)

const executionLockFileName = "receive.lock"
const maxExecutionLockRequestBytes = 1 << 20

// ExecutionLock serializes resumable target-side execution for one admitted
// handoff. The stable lock inode is retained instead of unlinked on release;
// removing a flock file permits two waiters to lock different inodes.
type ExecutionLock struct {
	mu          sync.Mutex
	root        *os.File
	handoff     *os.File
	requestFile *os.File
	file        *os.File
	rootPath    string
	handoffID   string
	digest      Digest
	request     Request
}

// HeldForSession adapts the existing request-bound execution proof to the
// courier-neutral sessionauthority.Fence interface. The final HeldForStore
// call still reopens and descriptor-compares the exact store, aggregate,
// request file, and stable flock inode; this method does not weaken authority
// to an ID plus digest assertion.
func (lock *ExecutionLock) HeldForSession(expectedRoot, aggregateID, digest string) bool {
	if lock == nil {
		return false
	}
	lock.mu.Lock()
	request, requestDigest := lock.request, lock.digest
	lock.mu.Unlock()
	if request.HandoffID != aggregateID || string(requestDigest) != digest {
		return false
	}
	return lock.HeldForStore(expectedRoot, request, requestDigest)
}

// RetainSessionDir returns a duplicate of the same descriptor-authenticated
// handoff directory already exposed to legacy session-move callers.
func (lock *ExecutionLock) RetainSessionDir(expectedRoot, aggregateID, digest string) (*os.File, error) {
	if lock == nil {
		return nil, fmt.Errorf("execution lock is required")
	}
	lock.mu.Lock()
	request, requestDigest := lock.request, lock.digest
	lock.mu.Unlock()
	if request.HandoffID != aggregateID || string(requestDigest) != digest {
		return nil, fmt.Errorf("execution lock does not bind the requested session aggregate")
	}
	return lock.RetainHandoffForStore(expectedRoot, request, requestDigest)
}

// HeldForStore reports whether this process still owns the execution fence
// for the exact admitted request in the exact canonical Store root. Path text
// alone is not authority: the root, handoff directory, and stable lock entry
// must still name the retained filesystem objects acquired after admission.
func (lock *ExecutionLock) HeldForStore(expectedRoot string, request Request, digest Digest) bool {
	if lock == nil {
		return false
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil || lock.root == nil || lock.handoff == nil || lock.requestFile == nil || lock.handoffID != request.HandoffID ||
		lock.digest != digest || lock.request != request {
		return false
	}
	if expectedRoot == "" || expectedRoot != strings.TrimSpace(expectedRoot) {
		return false
	}
	expectedRoot, err := filepath.Abs(expectedRoot)
	if err != nil || filepath.Clean(expectedRoot) != lock.rootPath {
		return false
	}
	rootFD, err := unix.Open(expectedRoot, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	root := os.NewFile(uintptr(rootFD), "wb-session-receive-authority-root-check")
	if root == nil {
		_ = unix.Close(rootFD)
		return false
	}
	defer func() { _ = root.Close() }()
	if !sameFile(lock.root, root) {
		return false
	}
	handoffFD, err := unix.Openat(rootFD, request.HandoffID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	handoff := os.NewFile(uintptr(handoffFD), "wb-session-receive-authority-handoff-check")
	if handoff == nil {
		_ = unix.Close(handoffFD)
		return false
	}
	defer func() { _ = handoff.Close() }()
	if !sameFile(lock.handoff, handoff) {
		return false
	}
	requestFD, err := unix.Openat(handoffFD, requestFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	requestFile := os.NewFile(uintptr(requestFD), "wb-session-receive-authority-request-check")
	if requestFile == nil {
		_ = unix.Close(requestFD)
		return false
	}
	defer func() { _ = requestFile.Close() }()
	if !sameFile(lock.requestFile, requestFile) {
		return false
	}
	admitted, err := readAdmittedRequestFile(lock.requestFile, request.HandoffID, digest)
	if err != nil || admitted != request {
		return false
	}
	fileFD, err := unix.Openat(handoffFD, executionLockFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return false
	}
	file := os.NewFile(uintptr(fileFD), "wb-session-receive-authority-lock-check")
	if file == nil {
		_ = unix.Close(fileFD)
		return false
	}
	defer func() { _ = file.Close() }()
	return sameFile(lock.file, file)
}

// RetainHandoffForStore returns a CLOEXEC duplicate of the exact admitted
// handoff directory retained by this held execution fence. Callers use the
// descriptor as authority for a multi-step transaction so a later path swap
// cannot split reads, locks, and immutable publications across directories.
// The caller owns the returned file.
func (lock *ExecutionLock) RetainHandoffForStore(expectedRoot string, request Request, digest Digest) (*os.File, error) {
	if !lock.HeldForStore(expectedRoot, request, digest) {
		return nil, fmt.Errorf("execution lock does not retain the exact admitted handoff directory")
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.handoff == nil {
		return nil, fmt.Errorf("execution lock handoff directory is closed")
	}
	fd, err := unix.Dup(int(lock.handoff.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate admitted handoff directory: %w", err)
	}
	unix.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "wb-session-retained-handoff-authority")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap retained handoff directory")
	}
	return file, nil
}

// RetainStoreRootForStore returns a CLOEXEC duplicate of the exact Store root
// retained with this admitted handoff. It is used for indexes whose key spans
// handoff aggregates while the held execution fence supplies authority.
func (lock *ExecutionLock) RetainStoreRootForStore(expectedRoot string, request Request, digest Digest) (*os.File, error) {
	if !lock.HeldForStore(expectedRoot, request, digest) {
		return nil, fmt.Errorf("execution lock does not retain the exact admitted handoff store root")
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.root == nil {
		return nil, fmt.Errorf("execution lock store root is closed")
	}
	fd, err := unix.Dup(int(lock.root.Fd()))
	if err != nil {
		return nil, fmt.Errorf("duplicate admitted handoff store root: %w", err)
	}
	unix.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "wb-session-retained-store-root-authority")
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap retained handoff store root")
	}
	return file, nil
}

// AcquireExecutionLock waits interruptibly for the per-handoff receive fence.
// Callers admit and authenticate exact request bytes before taking this lock.
func (s Store) AcquireExecutionLock(ctx context.Context, handoffID string, digest Digest) (*ExecutionLock, error) {
	if err := validateID("handoff_id", handoffID); err != nil {
		return nil, err
	}
	if err := digest.validate(); err != nil {
		return nil, fmt.Errorf("request digest: %w", err)
	}
	if s.Root == "" || s.Root != strings.TrimSpace(s.Root) {
		return nil, fmt.Errorf("handoff store root is required")
	}
	rootPath, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve handoff store root: %w", err)
	}
	rootPath = filepath.Clean(rootPath)
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open admitted handoff store root: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), "wb-session-receive-store-root")
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, fmt.Errorf("wrap admitted handoff store root")
	}
	handoffFD, err := unix.Openat(rootFD, handoffID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open admitted handoff execution directory: %w", err)
	}
	handoff := os.NewFile(uintptr(handoffFD), "wb-session-receive-handoff")
	if handoff == nil {
		_ = unix.Close(handoffFD)
		_ = root.Close()
		return nil, fmt.Errorf("wrap admitted handoff execution directory")
	}
	request, requestFile, err := admittedRequestAt(handoff, handoffID, digest)
	if err != nil {
		_ = handoff.Close()
		_ = root.Close()
		return nil, err
	}
	fd, err := openExecutionLockAt(handoffFD)
	if err != nil {
		_ = requestFile.Close()
		_ = handoff.Close()
		_ = root.Close()
		return nil, fmt.Errorf("open handoff execution lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-receive-lock")
	if file == nil {
		_ = unix.Close(fd)
		_ = requestFile.Close()
		_ = handoff.Close()
		_ = root.Close()
		return nil, fmt.Errorf("wrap handoff execution lock")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 {
		_ = file.Close()
		_ = requestFile.Close()
		_ = handoff.Close()
		_ = root.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect handoff execution lock: %w", err)
		}
		return nil, fmt.Errorf("handoff execution lock is not one regular file")
	}
	if err := unix.Fchmod(fd, 0o600); err != nil {
		_ = file.Close()
		_ = requestFile.Close()
		_ = handoff.Close()
		_ = root.Close()
		return nil, fmt.Errorf("secure handoff execution lock: %w", err)
	}
	for {
		err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
		if err == nil {
			return &ExecutionLock{
				root: root, handoff: handoff, requestFile: requestFile, file: file, rootPath: rootPath,
				handoffID: handoffID, digest: digest, request: request,
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			_ = file.Close()
			_ = requestFile.Close()
			_ = handoff.Close()
			_ = root.Close()
			return nil, fmt.Errorf("lock handoff execution: %w", err)
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			_ = file.Close()
			_ = requestFile.Close()
			_ = handoff.Close()
			_ = root.Close()
			return nil, fmt.Errorf("wait for handoff execution lock: %w", ctx.Err())
		case <-timer.C:
		}
	}
}

// openExecutionLockAt installs the stable flock inode without asking multiple
// first-time callers to race through O_CREAT on the same pathname. Darwin can
// transiently report ENOENT for that race. Opening first and then creating
// exclusively gives every loser an unambiguous signal to reopen the winner's
// inode; WB never unlinks this file.
func openExecutionLockAt(handoffFD int) (int, error) {
	const flags = unix.O_RDWR | unix.O_CLOEXEC | unix.O_NOFOLLOW
	for attempts := 0; attempts < 3; attempts++ {
		fd, err := unix.Openat(handoffFD, executionLockFileName, flags, 0)
		if err == nil {
			return fd, nil
		}
		if !errors.Is(err, unix.ENOENT) {
			return -1, err
		}
		fd, err = unix.Openat(handoffFD, executionLockFileName, flags|unix.O_CREAT|unix.O_EXCL, 0o600)
		if err == nil {
			return fd, nil
		}
		if !errors.Is(err, unix.EEXIST) {
			return -1, err
		}
	}
	return -1, fmt.Errorf("stable execution lock creation did not converge")
}

func admittedRequestAt(handoff *os.File, handoffID string, digest Digest) (Request, *os.File, error) {
	fd, err := unix.Openat(int(handoff.Fd()), requestFileName, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return Request{}, nil, fmt.Errorf("open admitted handoff request: %w", err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-receive-admitted-request")
	if file == nil {
		_ = unix.Close(fd)
		return Request{}, nil, fmt.Errorf("wrap admitted handoff request")
	}
	request, err := readAdmittedRequestFile(file, handoffID, digest)
	if err != nil {
		_ = file.Close()
		return Request{}, nil, err
	}
	return request, file, nil
}

func readAdmittedRequestFile(file *os.File, handoffID string, digest Digest) (Request, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Nlink != 1 || stat.Size > maxExecutionLockRequestBytes {
		if err != nil {
			return Request{}, fmt.Errorf("inspect admitted handoff request: %w", err)
		}
		return Request{}, fmt.Errorf("admitted handoff request is not one bounded immutable file")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return Request{}, fmt.Errorf("seek admitted handoff request: %w", err)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxExecutionLockRequestBytes+1))
	if err != nil || len(raw) > maxExecutionLockRequestBytes {
		if err != nil {
			return Request{}, fmt.Errorf("read admitted handoff request: %w", err)
		}
		return Request{}, fmt.Errorf("admitted handoff request exceeds %d bytes", maxExecutionLockRequestBytes)
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		return Request{}, fmt.Errorf("decode admitted handoff request: %w", err)
	}
	storedDigest := DigestBytes(raw)
	if request.HandoffID != handoffID || storedDigest != digest {
		return Request{}, fmt.Errorf("%w: admitted handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, storedDigest, digest)
	}
	return request, nil
}

func sameFile(first, second *os.File) bool {
	if first == nil || second == nil {
		return false
	}
	firstInfo, firstErr := first.Stat()
	secondInfo, secondErr := second.Stat()
	return firstErr == nil && secondErr == nil && os.SameFile(firstInfo, secondInfo)
}

// Close releases the execution fence. It is idempotent for defer-friendly use.
func (lock *ExecutionLock) Close() error {
	if lock == nil {
		return nil
	}
	lock.mu.Lock()
	defer lock.mu.Unlock()
	if lock.file == nil {
		return nil
	}
	file := lock.file
	handoff := lock.handoff
	requestFile := lock.requestFile
	root := lock.root
	lock.file = nil
	lock.handoff = nil
	lock.requestFile = nil
	lock.root = nil
	unlockErr := unix.Flock(int(file.Fd()), unix.LOCK_UN)
	closeErr := file.Close()
	requestErr := requestFile.Close()
	handoffErr := handoff.Close()
	rootErr := root.Close()
	return errors.Join(unlockErr, closeErr, requestErr, handoffErr, rootErr)
}
