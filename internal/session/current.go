package session

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/unixcompat"
)

// The global lock order is parked bundle -> all member journals -> PID slot.
// Registry writers acquire only the last lock and never acquire worktree locks.
// The lock file is persistent: unlinking it would create two independent locks.
func lockPID(dir string, pid int) (func(), error) {
	return lockPIDUsing(dir, pid, lockRegistryFile)
}

func lockPIDUsing(dir string, pid int, acquire func(*os.File) (func(), error)) (func(), error) {
	if pid <= 0 {
		return nil, fmt.Errorf("session registry lock requires a positive PID")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("create session directory: %w", err)
	}
	// Keep the registry directory's public one-record-per-entry contract.
	lockDir := filepath.Join(filepath.Dir(dir), "."+filepath.Base(dir)+"-locks")
	if err := os.MkdirAll(lockDir, 0o700); err != nil {
		return nil, fmt.Errorf("create session registry lock directory: %w", err)
	}
	directoryFD, err := unix.Open(lockDir, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open session registry lock directory: %w", err)
	}
	directory := os.NewFile(uintptr(directoryFD), lockDir)
	defer func() { _ = directory.Close() }()
	if err := protectRegistryPrivate(directory); err != nil {
		return nil, err
	}
	if err := checkRegistryPrivacy(directory, 0o700, true); err != nil {
		return nil, err
	}
	fd, err := unix.Openat(directoryFD, strconv.Itoa(pid)+".lock", unix.O_RDWR|unix.O_CREAT|unix.O_NOFOLLOW|unix.O_CLOEXEC|unix.O_NONBLOCK, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open session registry lock: %w", err)
	}
	file := os.NewFile(uintptr(fd), filepath.Join(lockDir, strconv.Itoa(pid)+".lock"))
	if err := protectRegistryPrivate(file); err != nil {
		_ = file.Close()
		return nil, err
	}
	if err := checkRegistryPrivacy(file, 0o600, false); err != nil {
		_ = file.Close()
		return nil, err
	}
	unlock, err := acquire(file)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	return func() { unlock(); _ = file.Close() }, nil
}

// ResolveCurrentForProcess authenticates a registered ancestor, including the
// original parked chat. It never invents a PID, identity, or registration.
func ResolveCurrentForProcess(dir string, startPID int) (Record, bool, error) {
	return resolveCurrentForProcess(dir, startPID, FindHarnessAncestor, os.LookupEnv)
}

func matchCurrentNative(record Record, lookup func(string) (string, bool)) error {
	native, err := CallerNativeID(record.Runtime, lookup)
	if err != nil {
		return err
	}
	if native == "" {
		return fmt.Errorf("current chat has no corroborating native harness ID")
	}
	if native != record.NativeHarnessID {
		return fmt.Errorf("registered PID belongs to another native chat")
	}
	return nil
}

func resolveCurrentForProcess(dir string, startPID int, observe func(int) (int, string), lookup func(string) (string, bool)) (Record, bool, error) {
	pid, runtime := observe(startPID)
	if pid <= 1 || (runtime != "codex" && runtime != "claude-code") {
		return Record{}, false, nil
	}
	native, err := CallerNativeID(runtime, lookup)
	if err != nil {
		return Record{}, false, err
	}
	if native == "" {
		return Record{}, false, fmt.Errorf("current chat has no corroborating native harness ID")
	}
	record, live, err := lookupCallerNative(dir, pid, runtime, native)
	if err != nil {
		return Record{}, false, err
	}
	if record.PID == 0 {
		return Record{}, false, nil
	}
	if !live {
		return Record{}, false, fmt.Errorf("registered caller does not match the observed live harness")
	}
	return record, true, nil
}

func sameCurrentProcess(a, b Record) bool {
	return a.PID > 0 && a.PID == b.PID && !a.ProcessStartedAt.IsZero() && a.ProcessStartedAt.Equal(b.ProcessStartedAt) &&
		a.NativeHarnessID != "" && a.NativeHarnessID == b.NativeHarnessID && a.Runtime == b.Runtime && a.Machine == b.Machine && a.Scope == b.Scope
}

// ProveGenerationGone refuses unknown native evidence and protects a live
// process even if its registry row has been pruned or replaced.
func ProveGenerationGone(record Record) error {
	if record.PID <= 0 || record.ProcessStartedAt.IsZero() || state(record) != StateGone {
		return fmt.Errorf("prepared actor generation is live or cannot be proved gone")
	}
	return nil
}

// ObserveProcessGeneration exposes native evidence for WB-owned child receipts.
func ObserveProcessGeneration(pid int) (time.Time, bool) { return processStartTime(pid) }

// BindNativeHarness adds only the Herdr-observed native ID to the exact live
// registered child. Concurrent metadata or ownership changes are conflicts.
func BindNativeHarness(dir string, expected Record, nativeID string) (Record, error) {
	if nativeID == "" || strings.TrimSpace(nativeID) != nativeID || len(nativeID) > 4096 || strings.ContainsAny(nativeID, "\x00\r\n") {
		return Record{}, fmt.Errorf("invalid observed native harness ID")
	}
	if err := expected.Terminal.Validate(); err != nil {
		return Record{}, err
	}
	unlock, err := lockPID(dir, expected.PID)
	if err != nil {
		return Record{}, err
	}
	defer unlock()
	current, live, err := LookupRecordExact(dir, expected)
	if err != nil {
		return Record{}, err
	}
	if !live || !nativeBindingCompatible(expected, current, nativeID) {
		return Record{}, fmt.Errorf("native harness observation conflicts with registered child")
	}
	current.NativeHarnessID = nativeID
	raw, err := json.MarshalIndent(current, "", "  ")
	if err != nil {
		return Record{}, err
	}
	if err := writeRecordAtomic(dir, current, append(raw, '\n')); err != nil {
		return Record{}, err
	}
	return current, nil
}

// WithActiveRecipient holds the registry transition barrier across the final
// terminal observation and one authorized submission. expected is an immutable
// recipient snapshot, including its logical WB identity and native namespace.
// The callback must not acquire journals, publish receipts, or mutate registry
// state: callers publish their durable send intent before entering this barrier.
func WithActiveRecipient(ctx context.Context, dir string, expected Record, submit func() error) error {
	if submit == nil {
		return fmt.Errorf("message recipient submission callback is required")
	}
	unlock, err := lockPIDUsing(dir, expected.PID, func(file *os.File) (func(), error) {
		return lockRegistryFileContext(ctx, file)
	})
	if err != nil {
		return err
	}
	defer unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	current, live, err := LookupActiveRecordExact(dir, expected)
	if err != nil {
		return err
	}
	if !live || !reflect.DeepEqual(current, expected) {
		return fmt.Errorf("registered message recipient changed before submission")
	}
	return submit()
}

// WithCurrentTransition serializes same-PID adoption against registration,
// lifecycle marking and pruning. The caller holds all member custody locks.
// commit atomically publishes only the prepared successor. A crash before or
// after commit can retry the identical preparation; another registration wins
// a conflict rather than being overwritten. The callback must not call Register.
func WithCurrentTransition(dir string, caller, successor Record, apply func(commit func() error) error) error {
	if !sameCurrentProcess(caller, successor) || caller.WBSessionID == successor.WBSessionID || successor.PredecessorWBSessionID == "" || successor.HandoffID == "" {
		return fmt.Errorf("current transition requires one prepared fresh successor")
	}
	unlock, err := lockPID(dir, caller.PID)
	if err != nil {
		return err
	}
	defer unlock()
	check := func() (Record, error) {
		key := caller
		key.WBSessionID = ""
		current, live, err := LookupRecordExact(dir, key)
		if err != nil {
			return Record{}, err
		}
		if !live || !sameCurrentProcess(caller, current) || (!reflect.DeepEqual(current, caller) && !reflect.DeepEqual(current, successor)) {
			return Record{}, fmt.Errorf("current harness registration changed during pickup")
		}
		return current, nil
	}
	if _, err := check(); err != nil {
		return err
	}
	commit := func() error {
		current, err := check()
		if err != nil {
			return err
		}
		if current.WBSessionID == successor.WBSessionID {
			return nil
		}
		raw, err := json.MarshalIndent(successor, "", "  ")
		if err != nil {
			return err
		}
		return writeRecordAtomic(dir, successor, append(raw, '\n'))
	}
	if err := apply(commit); err != nil {
		return err
	}
	current, err := check()
	if err != nil {
		return err
	}
	if current.WBSessionID != successor.WBSessionID {
		return fmt.Errorf("current pickup completed without successor registration")
	}
	return nil
}
