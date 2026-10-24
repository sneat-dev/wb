package agents

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// leaseDirName is the private directory holding one lease file per worktree.
const leaseDirName = "leases"

// ErrLeaseUnsupported reports that this platform cannot express an exclusive
// file lock, so shared-checkout execution must be refused rather than raced.
var ErrLeaseUnsupported = errors.New("exclusive worktree leases are not supported on this platform")

// LeaseInfo is the diagnostic body of a lease file. It is never the authority:
// the authority is the kernel's lock on the retained inode.
type LeaseInfo struct {
	SchemaVersion int       `json:"schema_version"`
	AgentID       string    `json:"agent_id"`
	ExperimentID  string    `json:"experiment_id,omitempty"`
	Repository    string    `json:"repository,omitempty"`
	WorktreeDir   string    `json:"worktree_dir"`
	OwnerPID      int       `json:"owner_pid,omitempty"`
	ClaimedAt     time.Time `json:"claimed_at"`
}

// LeaseHeldError reports that another live execution owns a worktree. It names
// the owner so the caller can await or stop that run instead of guessing.
type LeaseHeldError struct {
	Info LeaseInfo
	Path string
}

func (err *LeaseHeldError) Error() string {
	if err.Info.AgentID == "" {
		return fmt.Sprintf("worktree is already claimed by another execution (lease %s)", err.Path)
	}
	return fmt.Sprintf("worktree is already claimed by execution %s (lease %s); await or stop it before starting another writable run there", err.Info.AgentID, err.Path)
}

// Lease is an exclusive writer claim on one worktree. The open descriptor is
// what holds the claim, so the claim survives as long as any process inheriting
// it lives.
type Lease struct {
	mutex  sync.Mutex
	file   *os.File
	path   string
	info   LeaseInfo
	closed bool
}

// LeasePath is the retained lease file for a worktree directory. It is keyed on
// the symlink-resolved path so two spellings of one checkout cannot take two
// leases.
func (store Store) LeasePath(worktreeDir string) (string, error) {
	resolved, err := realWorktreePath(worktreeDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(store.Root, leaseDirName, worktreeKey(resolved)+".lock"), nil
}

// LeaseHolder reads the current lease body without taking the lock. It is
// advisory only, for reporting and pruning.
func (store Store) LeaseHolder(worktreeDir string) (LeaseInfo, bool, error) {
	path, err := store.LeasePath(worktreeDir)
	if err != nil {
		return LeaseInfo{}, false, err
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return LeaseInfo{}, false, nil
		}
		return LeaseInfo{}, false, err
	}
	var info LeaseInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return LeaseInfo{}, true, nil
	}
	return info, true, nil
}

// AcquireLease takes the exclusive writer claim for a worktree, failing with
// LeaseHeldError when another execution holds it.
func (store Store) AcquireLease(worktreeDir, agentID, experimentID, repository string, now time.Time) (*Lease, error) {
	if !fileLockingSupported {
		return nil, ErrLeaseUnsupported
	}
	path, err := store.LeasePath(worktreeDir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create agent lease directory: %w", err)
	}
	// The inode is retained: unlinking a lock file lets two waiters lock two
	// different inodes, which is exactly the race the lease exists to prevent.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open agent lease %s: %w", path, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if isLockBusy(err) {
			return nil, &LeaseHeldError{Info: readLeaseInfo(path), Path: path}
		}
		return nil, fmt.Errorf("lock agent lease %s: %w", path, err)
	}
	info := LeaseInfo{
		SchemaVersion: schemaVersion, AgentID: agentID, ExperimentID: experimentID,
		Repository: repository, WorktreeDir: worktreeDir, ClaimedAt: now.UTC(),
	}
	lease := &Lease{file: file, path: path, info: info}
	if err := lease.writeInfo(info); err != nil {
		_ = lease.Release()
		return nil, err
	}
	return lease, nil
}

// File is the descriptor a dispatcher passes to the detached owner. Inheriting
// it is what keeps the claim held until the last worker process exits.
func (lease *Lease) File() *os.File {
	if lease == nil {
		return nil
	}
	return lease.file
}

// Path is the retained lease file path.
func (lease *Lease) Path() string {
	if lease == nil {
		return ""
	}
	return lease.path
}

// Info returns the last body written for this lease.
func (lease *Lease) Info() LeaseInfo {
	if lease == nil {
		return LeaseInfo{}
	}
	return lease.info
}

// SetOwner records the detached owner's PID for diagnostics after the spawn.
func (lease *Lease) SetOwner(ownerPID int) error {
	if lease == nil {
		return nil
	}
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	info := lease.info
	info.OwnerPID = ownerPID
	if err := lease.writeInfoLocked(info); err != nil {
		return err
	}
	lease.info = info
	return nil
}

func (lease *Lease) writeInfo(info LeaseInfo) error {
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	return lease.writeInfoLocked(info)
}

func (lease *Lease) writeInfoLocked(info LeaseInfo) error {
	raw, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("encode agent lease: %w", err)
	}
	raw = append(raw, '\n')
	if err := lease.file.Truncate(0); err != nil {
		return fmt.Errorf("truncate agent lease %s: %w", lease.path, err)
	}
	if _, err := lease.file.WriteAt(raw, 0); err != nil {
		return fmt.Errorf("write agent lease %s: %w", lease.path, err)
	}
	if err := lease.file.Sync(); err != nil {
		return fmt.Errorf("sync agent lease %s: %w", lease.path, err)
	}
	return nil
}

// Release drops the claim. The file is never removed, so a concurrent acquirer
// can never lock a different inode than the one it opened.
func (lease *Lease) Release() error {
	if lease == nil {
		return nil
	}
	lease.mutex.Lock()
	defer lease.mutex.Unlock()
	if lease.closed {
		return nil
	}
	lease.closed = true
	unlockErr := unlockFile(lease.file)
	closeErr := lease.file.Close()
	return errors.Join(unlockErr, closeErr)
}

// realWorktreePath resolves a worktree path through symlinks so one checkout
// cannot be claimed under two names. A path whose tail does not exist yet (a
// worktree about to be created) is resolved through its nearest existing
// ancestor.
func realWorktreePath(path string) (string, error) {
	trimmed := filepath.Clean(strings.TrimSpace(path))
	if trimmed == "" || trimmed == "." || trimmed == string(filepath.Separator) {
		return "", fmt.Errorf("agent lease requires a worktree directory")
	}
	resolved, err := filepath.EvalSymlinks(trimmed)
	if err == nil {
		return resolved, nil
	}
	ancestor := trimmed
	remainder := ""
	for {
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("resolve worktree path %s: %w", path, err)
		}
		remainder = filepath.Join(filepath.Base(ancestor), remainder)
		ancestor = parent
		resolvedAncestor, ancestorErr := filepath.EvalSymlinks(ancestor)
		if ancestorErr != nil {
			continue
		}
		return filepath.Join(resolvedAncestor, remainder), nil
	}
}

// worktreeKey is a stable, path-safe digest of a resolved worktree path.
func worktreeKey(resolvedPath string) string {
	digest := sha256.Sum256([]byte(resolvedPath))
	return hex.EncodeToString(digest[:])
}

func readLeaseInfo(path string) LeaseInfo {
	raw, err := os.ReadFile(path)
	if err != nil {
		return LeaseInfo{}
	}
	var info LeaseInfo
	if err := json.Unmarshal(raw, &info); err != nil {
		return LeaseInfo{}
	}
	return info
}
