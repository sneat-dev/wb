package sessionmove

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

const RouteSchemaVersion = 1
const routeFileName = "route.json"
const maxRouteBytes = 16 << 10

const SuccessorAddressSchemaVersion = 1
const successorAddressesDirName = "successors"
const maxSuccessorAddressBytes = 32 << 10

// Route is the immutable source-side courier address selected before the
// first delivery attempt. Resume always reuses it, so config/default changes
// cannot switch an ambiguous handoff to another courier or SSH host.
type Route struct {
	SchemaVersion int               `json:"schema_version"`
	HandoffID     string            `json:"handoff_id"`
	RequestDigest Digest            `json:"request_digest"`
	TargetMachine string            `json:"target_machine"`
	Courier       Courier           `json:"courier"`
	SSH           *SSHConfig        `json:"ssh,omitempty"`
	Synchestra    *SynchestraConfig `json:"synchestra,omitempty"`
}

// SuccessorAddress is the durable courier-neutral address of one completed
// successor. It is keyed by stable WB session ID so later messaging and a
// handoff-back request do not depend on a harness-native session identifier.
type SuccessorAddress struct {
	SchemaVersion          int       `json:"schema_version"`
	SuccessorWBSessionID   string    `json:"successor_wb_session_id"`
	PredecessorWBSessionID string    `json:"predecessor_wb_session_id"`
	HandoffID              string    `json:"handoff_id"`
	RequestDigest          Digest    `json:"request_digest"`
	SourceMachine          string    `json:"source_machine"`
	TargetMachine          string    `json:"target_machine"`
	SourceWorkLogReference string    `json:"source_work_log_reference"`
	TargetWorkLogReference string    `json:"target_work_log_reference"`
	TmuxName               string    `json:"tmux_name"`
	Runtime                string    `json:"runtime"`
	Model                  string    `json:"model,omitempty"`
	NativeHarnessID        string    `json:"native_harness_id,omitempty"`
	AttemptID              string    `json:"attempt_id"`
	AttemptIndex           uint64    `json:"attempt_index"`
	PID                    int       `json:"pid"`
	PinnedCommit           string    `json:"pinned_commit"`
	StartedAt              time.Time `json:"started_at"`
	Route                  Route     `json:"route"`
}

func (s Store) RequestBytes(handoffID string) (Request, Digest, []byte, error) {
	request, digest, raw, handoff, err := s.openRouteHandoff(handoffID)
	if err != nil {
		return Request{}, "", nil, err
	}
	_ = handoff.Close()
	return request, digest, raw, nil
}

func (s Store) SaveRoute(route Route) (Route, bool, error) {
	request, digest, _, handoff, err := s.openRouteHandoff(route.HandoffID)
	if err != nil {
		return Route{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	route.SchemaVersion = RouteSchemaVersion
	if route.RequestDigest != digest || route.TargetMachine != request.TargetMachine {
		return Route{}, false, fmt.Errorf("%w: courier route does not match admitted request", ErrHandoffConflict)
	}
	if err := validateCourierRoute(route); err != nil {
		return Route{}, false, err
	}
	raw, err := marshalJSON(route)
	if err != nil {
		return Route{}, false, err
	}
	created, err := publishRouteImmutableAt(handoff, raw)
	if err != nil {
		return Route{}, false, err
	}
	if created {
		return route, false, nil
	}
	existingRaw, err := readRouteFileAt(handoff, routeFileName, maxRouteBytes, "durable courier route")
	if err != nil {
		return Route{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return Route{}, false, fmt.Errorf("%w: handoff %s already has a different immutable courier route", ErrHandoffConflict, route.HandoffID)
	}
	existing, err := decodeAndValidateRoute(existingRaw, request, digest)
	return existing, true, err
}

func (s Store) LoadRoute(handoffID string) (Route, error) {
	request, digest, _, handoff, err := s.openRouteHandoff(handoffID)
	if err != nil {
		return Route{}, err
	}
	defer func() { _ = handoff.Close() }()
	raw, err := readRouteFileAt(handoff, routeFileName, maxRouteBytes, "durable courier route")
	if err != nil {
		return Route{}, err
	}
	return decodeAndValidateRoute(raw, request, digest)
}

// LoadRouteUnderLock reads the courier route from the exact handoff aggregate
// retained by the held execution fence.
func (s Store) LoadRouteUnderLock(lock *ExecutionLock, handoffID string, digest Digest) (Route, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return Route{}, fmt.Errorf("load route under exact admitted execution authority: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	return loadRouteAt(handoff, request, digest)
}

// SaveSuccessorAddressUnderLock publishes the stable completed-successor
// index under the exact Store root retained by this handoff's execution lock.
func (s Store) SaveSuccessorAddressUnderLock(lock *ExecutionLock, handoffID string, digest Digest, receipt Receipt) (SuccessorAddress, bool, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return SuccessorAddress{}, false, fmt.Errorf("save successor address under exact admitted execution authority: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	if err := ValidateReceiptForRequest(receipt, request, digest); err != nil {
		return SuccessorAddress{}, false, err
	}
	durableReceipt, _, err := loadReceiptAt(handoff, request, digest)
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	if durableReceipt == nil {
		return SuccessorAddress{}, false, fmt.Errorf("successor address requires a durable completion receipt")
	}
	durableRaw, err := EncodeReceipt(*durableReceipt)
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	suppliedRaw, err := EncodeReceipt(receipt)
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	if !bytes.Equal(durableRaw, suppliedRaw) {
		return SuccessorAddress{}, false, fmt.Errorf("%w: successor address receipt differs from durable completion receipt", ErrHandoffConflict)
	}
	route, err := loadRouteAt(handoff, request, digest)
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	root, err := lock.RetainStoreRootForStore(s.Root, request, digest)
	if err != nil {
		return SuccessorAddress{}, false, fmt.Errorf("retain exact store root for successor address: %w", err)
	}
	defer func() { _ = root.Close() }()
	addresses, err := openSuccessorAddressesAt(root, true)
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	defer func() { _ = addresses.Close() }()
	address := successorAddressFor(request, digest, *durableReceipt, route)
	if err := validateSuccessorAddress(address, receipt.SuccessorWBSessionID); err != nil {
		return SuccessorAddress{}, false, err
	}
	raw, err := marshalJSON(address)
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	if len(raw) > maxSuccessorAddressBytes {
		return SuccessorAddress{}, false, fmt.Errorf("successor address exceeds %d bytes", maxSuccessorAddressBytes)
	}
	name := successorAddressFileName(receipt.SuccessorWBSessionID)
	created, err := publishImmutableAt(addresses, name, raw, 0o600)
	if err != nil {
		return SuccessorAddress{}, false, fmt.Errorf("publish immutable successor address: %w", err)
	}
	existingRaw, err := readImmutableAt(addresses, name, maxSuccessorAddressBytes, "successor address")
	if err != nil {
		return SuccessorAddress{}, false, err
	}
	if !bytes.Equal(existingRaw, raw) {
		return SuccessorAddress{}, false, fmt.Errorf("%w: successor WB session %s already has a different address", ErrHandoffConflict, receipt.SuccessorWBSessionID)
	}
	existing, err := decodeAndValidateSuccessorAddress(existingRaw, receipt.SuccessorWBSessionID)
	return existing, !created, err
}

// LoadSuccessorAddress resolves a stable successor WB session ID without
// consulting current courier configuration.
func (s Store) LoadSuccessorAddress(successorWBSessionID string) (SuccessorAddress, error) {
	if err := validateID("successor_wb_session_id", successorWBSessionID); err != nil {
		return SuccessorAddress{}, err
	}
	root, err := s.openRoot(false)
	if err != nil {
		return SuccessorAddress{}, err
	}
	defer func() { _ = root.Close() }()
	addresses, err := openSuccessorAddressesAt(root, false)
	if err != nil {
		return SuccessorAddress{}, err
	}
	defer func() { _ = addresses.Close() }()
	raw, err := readImmutableAt(addresses, successorAddressFileName(successorWBSessionID), maxSuccessorAddressBytes, "successor address")
	if err != nil {
		return SuccessorAddress{}, err
	}
	address, err := decodeAndValidateSuccessorAddress(raw, successorWBSessionID)
	if err != nil {
		return SuccessorAddress{}, err
	}
	handoff, err := openHandoffAtRoot(root, address.HandoffID)
	if err != nil {
		return SuccessorAddress{}, fmt.Errorf("corroborate successor address handoff: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	request, digest, _, err := loadRequestAt(handoff, address.HandoffID)
	if err != nil {
		return SuccessorAddress{}, err
	}
	if digest != address.RequestDigest {
		return SuccessorAddress{}, fmt.Errorf("%w: successor address request digest does not match exact admitted handoff", ErrHandoffConflict)
	}
	return corroborateSuccessorAddressAt(handoff, request, digest, successorWBSessionID, raw)
}

// LoadSuccessorAddressUnderLock corroborates the immutable completed-successor
// index against the exact request, receipt, and route retained by a held
// execution fence. Source custody uses this proof immediately before sealing
// the predecessor Work Log terminal.
func (s Store) LoadSuccessorAddressUnderLock(lock *ExecutionLock, handoffID string, digest Digest) (SuccessorAddress, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return SuccessorAddress{}, fmt.Errorf("load successor address under exact admitted execution authority: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	root, err := lock.RetainStoreRootForStore(s.Root, request, digest)
	if err != nil {
		return SuccessorAddress{}, fmt.Errorf("retain exact store root for successor address: %w", err)
	}
	defer func() { _ = root.Close() }()
	addresses, err := openSuccessorAddressesAt(root, false)
	if err != nil {
		return SuccessorAddress{}, err
	}
	defer func() { _ = addresses.Close() }()
	raw, err := readImmutableAt(addresses, successorAddressFileName(request.SuccessorWBSessionID), maxSuccessorAddressBytes, "successor address")
	if err != nil {
		return SuccessorAddress{}, err
	}
	return corroborateSuccessorAddressAt(handoff, request, digest, request.SuccessorWBSessionID, raw)
}

func corroborateSuccessorAddressAt(handoff *os.File, request Request, digest Digest, successorWBSessionID string, raw []byte) (SuccessorAddress, error) {
	address, err := decodeAndValidateSuccessorAddress(raw, successorWBSessionID)
	if err != nil {
		return SuccessorAddress{}, err
	}
	if address.HandoffID != request.HandoffID || address.RequestDigest != digest {
		return SuccessorAddress{}, fmt.Errorf("%w: successor address request identity does not match exact admitted handoff", ErrHandoffConflict)
	}
	receipt, _, err := loadReceiptAt(handoff, request, digest)
	if err != nil {
		return SuccessorAddress{}, err
	}
	if receipt == nil {
		return SuccessorAddress{}, fmt.Errorf("%w: successor address has no durable completion receipt", ErrHandoffConflict)
	}
	route, err := loadRouteAt(handoff, request, digest)
	if err != nil {
		return SuccessorAddress{}, err
	}
	expectedRaw, err := marshalJSON(successorAddressFor(request, digest, *receipt, route))
	if err != nil {
		return SuccessorAddress{}, err
	}
	if !bytes.Equal(raw, expectedRaw) {
		return SuccessorAddress{}, fmt.Errorf("%w: successor address does not match exact request, receipt, and route", ErrHandoffConflict)
	}
	return address, nil
}

func successorAddressFor(request Request, digest Digest, receipt Receipt, route Route) SuccessorAddress {
	return SuccessorAddress{
		SchemaVersion:        SuccessorAddressSchemaVersion,
		SuccessorWBSessionID: receipt.SuccessorWBSessionID, PredecessorWBSessionID: receipt.PredecessorWBSessionID,
		HandoffID: request.HandoffID, RequestDigest: digest, SourceMachine: request.SourceMachine, TargetMachine: receipt.TargetMachine,
		SourceWorkLogReference: request.WorkLogReference, TargetWorkLogReference: receipt.TargetWorkLogReference,
		TmuxName: receipt.TmuxName, Runtime: receipt.Runtime, Model: receipt.Model, NativeHarnessID: receipt.NativeHarnessID,
		AttemptID: receipt.AttemptID, AttemptIndex: receipt.AttemptIndex, PID: receipt.PID,
		PinnedCommit: receipt.PinnedCommit, StartedAt: receipt.StartedAt, Route: route,
	}
}

func decodeAndValidateRoute(raw []byte, request Request, digest Digest) (Route, error) {
	var route Route
	if err := decodeJSON(raw, &route); err != nil {
		return Route{}, err
	}
	if route.SchemaVersion != RouteSchemaVersion || route.HandoffID != request.HandoffID || route.RequestDigest != digest || route.TargetMachine != request.TargetMachine {
		return Route{}, fmt.Errorf("%w: durable courier route does not match admitted request", ErrHandoffConflict)
	}
	if err := validateCourierRoute(route); err != nil {
		return Route{}, err
	}
	return route, nil
}

func validateCourierRoute(route Route) error {
	switch route.Courier {
	case CourierSSH:
		if route.SSH == nil || route.Synchestra != nil {
			return errors.New("ssh courier route must contain only one configured ssh address")
		}
		return route.SSH.Validate()
	case CourierSynchestra:
		if route.Synchestra == nil || route.SSH != nil {
			return errors.New("synchestra courier route must contain only one configured runner address")
		}
		return route.Synchestra.Validate()
	default:
		return errors.New("durable courier route is unsupported by this WB build")
	}
}

func loadRouteAt(handoff *os.File, request Request, digest Digest) (Route, error) {
	raw, err := readImmutableAt(handoff, routeFileName, maxRouteBytes, "durable courier route")
	if err != nil {
		return Route{}, err
	}
	return decodeAndValidateRoute(raw, request, digest)
}

func openSuccessorAddressesAt(root *os.File, create bool) (*os.File, error) {
	if root == nil {
		return nil, fmt.Errorf("open successor addresses: exact Store root is required")
	}
	if create {
		if err := unix.Mkdirat(int(root.Fd()), successorAddressesDirName, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create successor addresses directory: %w", err)
		}
	}
	fd, err := unix.Openat(int(root.Fd()), successorAddressesDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open successor addresses directory: %w", err)
	}
	directory := os.NewFile(uintptr(fd), "wb-session-successor-addresses")
	if directory == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap successor addresses directory")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
		_ = directory.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect successor addresses directory: %w", err)
		}
		return nil, fmt.Errorf("successor addresses directory is not mode 0700")
	}
	return directory, nil
}

func successorAddressFileName(successorWBSessionID string) string {
	return successorWBSessionID + ".json"
}

func decodeAndValidateSuccessorAddress(raw []byte, successorWBSessionID string) (SuccessorAddress, error) {
	var address SuccessorAddress
	if err := decodeJSON(raw, &address); err != nil {
		return SuccessorAddress{}, fmt.Errorf("decode successor address: %w", err)
	}
	if err := validateSuccessorAddress(address, successorWBSessionID); err != nil {
		return SuccessorAddress{}, err
	}
	return address, nil
}

func validateSuccessorAddress(address SuccessorAddress, successorWBSessionID string) error {
	if address.SchemaVersion != SuccessorAddressSchemaVersion {
		return fmt.Errorf("successor address schema_version %d is unsupported", address.SchemaVersion)
	}
	for field, value := range map[string]string{
		"successor_wb_session_id":   address.SuccessorWBSessionID,
		"predecessor_wb_session_id": address.PredecessorWBSessionID,
		"handoff_id":                address.HandoffID, "source_machine": address.SourceMachine, "target_machine": address.TargetMachine,
		"tmux_name": address.TmuxName,
	} {
		if err := validateID(field, value); err != nil {
			return err
		}
	}
	if address.SuccessorWBSessionID != successorWBSessionID {
		return fmt.Errorf("%w: successor address key %s contains session %s", ErrHandoffConflict, successorWBSessionID, address.SuccessorWBSessionID)
	}
	if err := address.RequestDigest.validate(); err != nil {
		return err
	}
	sourceReference, err := ParseWorkLogReference(address.SourceWorkLogReference)
	if err != nil {
		return err
	}
	reference, err := ParseWorkLogReference(address.TargetWorkLogReference)
	if err != nil {
		return err
	}
	claimID, err := ExternalHandoffClaimID(address.RequestDigest, address.SuccessorWBSessionID)
	if err != nil {
		return err
	}
	if reference.EffortID != sourceReference.EffortID || reference.RunID != sourceReference.RunID || reference.ClaimID != claimID {
		return fmt.Errorf("%w: successor address target Work Log claim is not deterministic", ErrHandoffConflict)
	}
	if address.TmuxName != "wb-session-"+address.SuccessorWBSessionID {
		return fmt.Errorf("%w: successor address tmux name is not deterministic", ErrHandoffConflict)
	}
	if strings.TrimSpace(address.Runtime) == "" || strings.ContainsAny(address.Runtime, "\r\n") {
		return fmt.Errorf("successor address runtime must be non-empty and single-line")
	}
	if err := validateLaunchAttemptIdentity(address.AttemptID, address.AttemptIndex, address.PID); err != nil {
		return fmt.Errorf("successor address launch attempt identity: %w", err)
	}
	if !gitObjectID.MatchString(address.PinnedCommit) || address.StartedAt.IsZero() {
		return fmt.Errorf("successor address launch identity is incomplete")
	}
	if address.Route.SchemaVersion != RouteSchemaVersion || address.Route.HandoffID != address.HandoffID ||
		address.Route.RequestDigest != address.RequestDigest || address.Route.TargetMachine != address.TargetMachine {
		return fmt.Errorf("%w: successor address route does not match completed handoff", ErrHandoffConflict)
	}
	if err := validateCourierRoute(address.Route); err != nil {
		return err
	}
	return nil
}

// openRouteHandoff retains the admitted handoff directory descriptor used for
// all subsequent route operations. Each path boundary refuses symlinks, and
// the request is read once from one bounded regular-file descriptor.
func (s Store) openRouteHandoff(handoffID string) (Request, Digest, []byte, *os.File, error) {
	handoff, err := s.openHandoff(handoffID, false)
	if err != nil {
		return Request{}, "", nil, nil, err
	}
	request, digest, raw, err := loadRequestAt(handoff, handoffID)
	if err != nil {
		_ = handoff.Close()
		return Request{}, "", nil, nil, err
	}
	return request, digest, raw, handoff, nil
}

func readRouteFileAt(directory *os.File, name string, maximum int64, description string) ([]byte, error) {
	return readImmutableAt(directory, name, maximum, description)
}

func publishRouteImmutableAt(directory *os.File, raw []byte) (bool, error) {
	if len(raw) > maxRouteBytes {
		return false, fmt.Errorf("courier route exceeds %d bytes", maxRouteBytes)
	}
	return publishImmutableAt(directory, routeFileName, raw, 0o600)
}
