package agents

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"

	"github.com/sneat-dev/wb/internal/filewrite"
)

// This file owns the durable per-user single-container slot: an exclusive
// kernel lock plus an on-disk reservation. It knows nothing about Docker,
// processes or images. Proving that a container is gone is the runtime's job
// and is passed to ClearReservation as a mandatory callback.

const (
	containerSlotDirName             = "linux-execution"
	containerSlotLockFileName        = "container.lock"
	containerSlotReservationFileName = "reservation.json"
	// maxContainerReservationBytes bounds the durable JSON read before parsing.
	maxContainerReservationBytes = 8 << 10
)

var (
	// The name cannot start with "-", so it cannot become a runtime option.
	containerSlotNamePattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]{0,62}$`)
	containerSlotNoncePattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
	// containerDaemonIDPattern bounds the Docker Engine ID. Docker has printed
	// both plain hex and colon-separated forms, so the accepted alphabet is the
	// safe shared subset rather than one version's exact shape.
	containerDaemonIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9:._-]{0,127}$`)
)

// ContainerReservation is the durable identity of the one container the slot
// is about to create, or has created. It carries no credentials, no argv and no
// raw Docker output. ContainerID is empty until RecordContainerID records what
// the runtime returned. DaemonID binds the reservation to the exact Docker
// Engine that owns the container, so absence on a different daemon can never
// clear it.
type ContainerReservation struct {
	RunID       string `json:"run_id"`
	Name        string `json:"name"`
	Nonce       string `json:"nonce"`
	ContainerID string `json:"container_id,omitempty"`
	DaemonID    string `json:"daemon_id,omitempty"`
}

// ContainerSlotBusyError reports that another live holder owns the slot. It is
// a refusal, not a queue: await or stop the current owner instead of racing it.
type ContainerSlotBusyError struct {
	Path string
}

func (err *ContainerSlotBusyError) Error() string {
	return fmt.Sprintf("another container execution already holds the durable slot (%s); wait for it to finish or stop it before starting another", err.Path)
}

// ContainerSlot is one retained holder. The open descriptor and its kernel
// lock are the authority; the reservation file is the durable record a later
// holder must see.
type ContainerSlot struct {
	mutex       sync.Mutex
	file        *os.File
	stateDir    string
	reservation *ContainerReservation
	// persist is a per-slot seam for pinning the save-failure branch. It is set
	// by OpenContainerSlot to writeReservation; production never changes it.
	persist func(ContainerReservation) error
	closed  bool
}

// ContainerSlotStateDir resolves the fixed per-user default state directory.
// It is anchored to the trusted owner home, never to XDG_CONFIG_HOME, a
// worktree, profile, image, project root or policy: a variable configuration
// directory must not create several independent single-container slots for one
// user.
func ContainerSlotStateDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve the user home directory for the container slot: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" {
		return "", errors.New("resolve the user home directory for the container slot: empty path")
	}
	return filepath.Join(home, ".config", "wb", containerSlotDirName), nil
}

// OpenContainerSlot takes the durable slot under stateDir. An empty stateDir
// means the fixed per-user default, which is what production always uses; a
// non-empty one is the focused-test seam. A held slot is reported as
// *ContainerSlotBusyError, and a platform without an inherited exclusive lock
// fails closed rather than pretending to serialize.
func OpenContainerSlot(stateDir string) (*ContainerSlot, error) {
	resolved := strings.TrimSpace(stateDir)
	if resolved == "" {
		defaultDir, err := ContainerSlotStateDir()
		if err != nil {
			return nil, err
		}
		resolved = defaultDir
	}
	resolved = filepath.Clean(resolved)
	if err := prepareContainerSlotStateDir(resolved); err != nil {
		return nil, err
	}
	lockPath := filepath.Join(resolved, containerSlotLockFileName)
	if err := inspectPrivateRegularFile(lockPath); err != nil {
		return nil, err
	}
	// The lock file is never unlinked: a later holder must lock the same inode.
	file, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open container slot lock %s: %w", lockPath, err)
	}
	if err := lockFile(file); err != nil {
		_ = file.Close()
		if isLockBusy(err) {
			return nil, &ContainerSlotBusyError{Path: lockPath}
		}
		// Includes ErrLeaseUnsupported: without a real lock two holders could
		// admit two containers.
		return nil, fmt.Errorf("lock container slot %s: %w", lockPath, err)
	}
	slot := &ContainerSlot{file: file, stateDir: resolved}
	slot.persist = slot.writeReservation
	prior, found, err := slot.loadReservation()
	if err != nil {
		_ = slot.Close()
		return nil, err
	}
	if found {
		slot.reservation = &prior
	}
	return slot, nil
}

// Reservation returns a value copy of the reservation, or false when the slot
// holds none. A caller never receives a mutable alias into the authority.
func (slot *ContainerSlot) Reservation() (ContainerReservation, bool) {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.reservation == nil {
		return ContainerReservation{}, false
	}
	return *slot.reservation, true
}

// Reserve claims the slot for a container that does not exist yet. It refuses
// any existing reservation and persists the new one before a runtime may
// create a container. A failed save leaves the reservation pending, so an
// uncertain save can never look like a free slot.
func (slot *ContainerSlot) Reserve(reservation ContainerReservation) error {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.closed {
		return errors.New("reserve container slot: the slot is closed")
	}
	if slot.reservation != nil {
		return fmt.Errorf("container slot already holds reservation for run %s; clear it with an absence proof before reserving again", slot.reservation.RunID)
	}
	if err := validateContainerReservation(reservation, false); err != nil {
		return err
	}
	slot.reservation = &reservation
	if err := slot.persist(reservation); err != nil {
		return fmt.Errorf("persist container reservation for run %s: %w; the slot remains blocked until an absence proof clears it", reservation.RunID, err)
	}
	return nil
}

// RecordContainerID persists the runtime's ID for the existing reservation. It
// refuses to record anything without one, so an ID never becomes an orphan
// authority of its own. The assignment is single: re-recording the same ID is
// an idempotent success, but a different ID is refused so the first container
// identity can never be orphaned.
func (slot *ContainerSlot) RecordContainerID(containerID string) error {
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.closed {
		return errors.New("record container ID: the slot is closed")
	}
	if slot.reservation == nil {
		return errors.New("record container ID: the slot holds no reservation")
	}
	if !imageDigestHashPattern.MatchString(containerID) {
		return fmt.Errorf("container ID %q is not a full 64-character lowercase hex ID", containerID)
	}
	if recorded := slot.reservation.ContainerID; recorded != "" {
		if recorded == containerID {
			return nil
		}
		return fmt.Errorf("container slot already recorded container ID %s for run %s; refusing to replace it with %s", recorded, slot.reservation.RunID, containerID)
	}
	updated := *slot.reservation
	updated.ContainerID = containerID
	if err := slot.persist(updated); err != nil {
		return fmt.Errorf("persist container ID for run %s: %w", updated.RunID, err)
	}
	slot.reservation = &updated
	return nil
}

// ClearReservation removes the durable reservation only after confirmAbsent
// proves, under the retained lock, that the exact prior container is gone. The
// callback is mandatory: there is no boolean bypass and no Docker assumption.
// A callback error leaves the reservation unchanged.
func (slot *ContainerSlot) ClearReservation(confirmAbsent func(ContainerReservation) error) error {
	if confirmAbsent == nil {
		return errors.New("clear container reservation: an absence proof callback is required")
	}
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.closed {
		return errors.New("clear container reservation: the slot is closed")
	}
	if slot.reservation == nil {
		return errors.New("clear container reservation: the slot holds no reservation")
	}
	prior := *slot.reservation
	if err := confirmAbsent(prior); err != nil {
		return fmt.Errorf("container %s for run %s was not proven absent; reservation retained: %w", prior.ContainerID, prior.RunID, err)
	}
	path := slot.reservationPath()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove container reservation %s: %w", path, err)
	}
	if err := syncContainerSlotDir(slot.stateDir); err != nil {
		return err
	}
	slot.reservation = nil
	return nil
}

// Close releases the kernel lock and descriptor but never clears the
// reservation. The next holder sees it and must explicitly prove absence.
func (slot *ContainerSlot) Close() error {
	if slot == nil {
		return nil
	}
	slot.mutex.Lock()
	defer slot.mutex.Unlock()
	if slot.closed {
		return nil
	}
	slot.closed = true
	if slot.file == nil {
		return nil
	}
	return errors.Join(unlockFile(slot.file), slot.file.Close())
}

func (slot *ContainerSlot) reservationPath() string {
	return filepath.Join(slot.stateDir, containerSlotReservationFileName)
}

// writeReservation writes private JSON through a temp file, syncs it,
// publishes it by rename and syncs the state directory.
func (slot *ContainerSlot) writeReservation(reservation ContainerReservation) error {
	// A fixed struct of strings always marshals; the encoder has no failure
	// mode left to report.
	raw, _ := json.MarshalIndent(reservation, "", "  ")
	raw = append(raw, '\n')
	temporary, err := filewrite.CreateTemp(slot.stateDir, ".reservation-*.json.tmp", nil)
	if err != nil {
		return fmt.Errorf("stage container reservation: %w", err)
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()
	if err := filewrite.ChmodFile(temporary, 0o600, temporaryName, nil); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("protect staged container reservation: %w", err)
	}
	if err := filewrite.Write(temporary, raw, temporaryName, nil); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("write staged container reservation: %w", err)
	}
	if err := filewrite.Sync(temporary, temporaryName, nil); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("sync staged container reservation: %w", err)
	}
	if err := filewrite.Close(temporary, temporaryName, nil); err != nil {
		return fmt.Errorf("close staged container reservation: %w", err)
	}
	if err := filewrite.Rename(temporaryName, slot.reservationPath(), nil); err != nil {
		return fmt.Errorf("publish container reservation: %w", err)
	}
	return syncContainerSlotDir(slot.stateDir)
}

func (slot *ContainerSlot) loadReservation() (ContainerReservation, bool, error) {
	path := slot.reservationPath()
	if err := inspectPrivateRegularFile(path); err != nil {
		return ContainerReservation{}, false, err
	}
	file, err := os.Open(path)
	if os.IsNotExist(err) {
		return ContainerReservation{}, false, nil
	}
	if err != nil {
		return ContainerReservation{}, false, fmt.Errorf("open container reservation %s: %w", path, err)
	}
	defer func() { _ = file.Close() }()
	raw, err := io.ReadAll(io.LimitReader(file, maxContainerReservationBytes+1))
	if err != nil {
		return ContainerReservation{}, false, fmt.Errorf("read container reservation %s: %w", path, err)
	}
	if len(raw) > maxContainerReservationBytes {
		return ContainerReservation{}, false, fmt.Errorf("container reservation %s is above the %d-byte bound", path, maxContainerReservationBytes)
	}
	var reservation ContainerReservation
	if err := json.Unmarshal(raw, &reservation); err != nil {
		return ContainerReservation{}, false, fmt.Errorf("container reservation %s is corrupt: %w", path, err)
	}
	if err := validateContainerReservation(reservation, true); err != nil {
		return ContainerReservation{}, false, fmt.Errorf("container reservation %s is invalid: %w", path, err)
	}
	return reservation, true, nil
}

// prepareContainerSlotStateDir creates a missing state directory and refuses
// an existing one that is a symlink, not a directory, or not private.
func prepareContainerSlotStateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create container slot state directory %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("inspect container slot state directory %s: %w", dir, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("container slot state directory %s is a symlink; refusing an unverified location", dir)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("container slot state directory %s has insecure permissions %04o; expected private 0700", dir, info.Mode().Perm())
	}
	return nil
}

// inspectPrivateRegularFile refuses an existing name that is a symlink, not a
// regular file, or readable by anyone but its owner. A missing name is fine.
func inspectPrivateRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", path, err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("%s is a symlink; refusing an unverified file", path)
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("%s is not a regular file", path)
	}
	if info.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("%s has insecure permissions %04o; expected private 0600", path, info.Mode().Perm())
	}
	return nil
}

func syncContainerSlotDir(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open container slot state directory %s for sync: %w", path, err)
	}
	defer func() { _ = directory.Close() }()
	if err := filewrite.SyncDir(directory, nil); err != nil {
		return fmt.Errorf("sync container slot state directory %s: %w", path, err)
	}
	return nil
}

func validateContainerReservation(reservation ContainerReservation, loaded bool) error {
	if reservation.ContainerID != "" && !imageDigestHashPattern.MatchString(reservation.ContainerID) {
		return fmt.Errorf("container ID %q is not a full 64-character lowercase hex ID", reservation.ContainerID)
	}
	if reservation.DaemonID != "" && !containerDaemonIDPattern.MatchString(reservation.DaemonID) {
		return fmt.Errorf("Docker Engine ID %q is not a bounded safe identity", reservation.DaemonID)
	}
	if !loaded && reservation.DaemonID == "" {
		return errors.New("a new container reservation must bind the Docker Engine identity that will own the container")
	}
	if !loaded && reservation.ContainerID != "" {
		return errors.New("a new container reservation must have an empty container ID; record it after the runtime creates the container")
	}
	if err := validateAgentID(reservation.RunID); err != nil {
		return fmt.Errorf("container reservation run ID: %w", err)
	}
	if !containerSlotNamePattern.MatchString(reservation.Name) {
		return fmt.Errorf("container name %q is not a safe, bounded name; expected 1-63 characters from [A-Za-z0-9_.-] starting with an alphanumeric", reservation.Name)
	}
	if !containerSlotNoncePattern.MatchString(reservation.Nonce) {
		return fmt.Errorf("container nonce %q is not 32 lowercase hex characters", reservation.Nonce)
	}
	return nil
}
