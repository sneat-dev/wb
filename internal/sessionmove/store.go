package sessionmove

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

// DirName is the directory under WB_HOME containing private handoff
// aggregates. Each handoff has one immutable request, zero or one immutable
// receipt, and an append-only events directory.
const DirName = "handoffs"

var (
	ErrDigestMismatch  = errors.New("handoff digest does not match the supplied bytes")
	ErrHandoffConflict = errors.New("handoff identity conflicts with durable state")
)

const (
	requestFileName = "request.json"
	receiptFileName = "receipt.json"
	eventsDirName   = "events"
	maxReceiptBytes = 1 << 20
	maxEventBytes   = 64 << 10
)

// Store persists handoff state at Root, normally <WB_HOME>/handoffs.
type Store struct {
	Root string
}

func NewStore(root string) Store { return Store{Root: root} }

// Admit durably installs exact request bytes. The first caller wins an atomic
// no-replace publication. Later callers with the same ID must present both the
// same digest and byte-identical request; they receive any existing receipt.
func (s Store) Admit(raw []byte, digest Digest) (Admission, error) {
	if err := digest.validate(); err != nil || !digest.Matches(raw) {
		return Admission{}, fmt.Errorf("%w: declared %q, computed %q", ErrDigestMismatch, digest, DigestBytes(raw))
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		return Admission{}, err
	}
	handoff, err := s.openHandoff(request.HandoffID, true)
	if err != nil {
		return Admission{}, err
	}
	defer func() { _ = handoff.Close() }()
	created, err := publishImmutableAt(handoff, requestFileName, raw, 0o600)
	if err != nil {
		return Admission{}, fmt.Errorf("persist handoff request: %w", err)
	}
	existing, existingDigest, existingRaw, err := loadRequestAt(handoff, request.HandoffID)
	if err != nil {
		return Admission{}, err
	}
	if existingDigest != digest || !bytes.Equal(existingRaw, raw) {
		return Admission{}, fmt.Errorf("%w: handoff %s already has digest %s, received %s", ErrHandoffConflict, request.HandoffID, existingDigest, digest)
	}
	if existing != request {
		return Admission{}, fmt.Errorf("%w: durable handoff request projection changed for %s", ErrHandoffConflict, request.HandoffID)
	}

	receipt, _, err := loadReceiptAt(handoff, request, digest)
	if err != nil {
		return Admission{}, err
	}
	return Admission{Request: request, Digest: digest, Replay: !created, Receipt: receipt}, nil
}

// ReadmitUnderLock revalidates exact request bytes and returns any durable
// receipt relative to the aggregate retained by a held execution fence. It is
// deliberately read-only: callers use it after waiting for the lock, when a
// path-based Admit could otherwise publish into a swapped decoy directory.
func (s Store) ReadmitUnderLock(lock *ExecutionLock, handoffID string, digest Digest, raw []byte) (Admission, error) {
	if err := digest.validate(); err != nil || !digest.Matches(raw) {
		return Admission{}, fmt.Errorf("%w: declared %q, computed %q", ErrDigestMismatch, digest, DigestBytes(raw))
	}
	supplied, err := DecodeRequest(raw)
	if err != nil {
		return Admission{}, err
	}
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return Admission{}, fmt.Errorf("re-admit under exact execution authority: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	admitted, storedDigest, storedRaw, err := loadRequestAt(handoff, handoffID)
	if err != nil {
		return Admission{}, err
	}
	if supplied != request || admitted != request || storedDigest != digest || !bytes.Equal(storedRaw, raw) {
		return Admission{}, fmt.Errorf("%w: exact request differs from retained handoff %s", ErrHandoffConflict, handoffID)
	}
	receipt, _, err := loadReceiptAt(handoff, admitted, storedDigest)
	if err != nil {
		return Admission{}, err
	}
	return Admission{Request: admitted, Digest: storedDigest, Replay: true, Receipt: receipt}, nil
}

// SaveReceipt durably publishes the target receipt without replacement.
// Repeating the identical receipt is a successful replay; any change is a
// conflict because a handoff can have only one successor identity.
func (s Store) SaveReceipt(handoffID string, digest Digest, receipt Receipt) (Receipt, bool, error) {
	handoff, err := s.openHandoff(handoffID, false)
	if err != nil {
		return Receipt{}, false, err
	}
	defer func() { _ = handoff.Close() }()
	request, storedDigest, _, err := loadRequestAt(handoff, handoffID)
	if err != nil {
		return Receipt{}, false, err
	}
	if digest != storedDigest {
		return Receipt{}, false, fmt.Errorf("%w: handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, storedDigest, digest)
	}
	return saveReceiptAt(handoff, request, storedDigest, receipt)
}

// SaveReceiptUnderLock publishes a receipt relative to the exact handoff
// directory retained by the held execution fence. Production receive and
// source-acknowledgement paths use this form so a pathname swap cannot move
// publication to a different aggregate.
func (s Store) SaveReceiptUnderLock(lock *ExecutionLock, handoffID string, digest Digest, receipt Receipt) (Receipt, bool, error) {
	request, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("save receipt under exact admitted execution authority: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	return saveReceiptAt(handoff, request, digest, receipt)
}

func saveReceiptAt(handoff *os.File, request Request, digest Digest, receipt Receipt) (Receipt, bool, error) {
	if err := ValidateReceiptForRequest(receipt, request, digest); err != nil {
		return Receipt{}, false, err
	}
	raw, err := EncodeReceipt(receipt)
	if err != nil {
		return Receipt{}, false, err
	}
	if len(raw) > maxReceiptBytes {
		return Receipt{}, false, fmt.Errorf("session move receipt exceeds %d bytes", maxReceiptBytes)
	}
	created, err := publishImmutableAt(handoff, receiptFileName, raw, 0o600)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("persist handoff receipt: %w", err)
	}
	existing, existingRaw, err := loadReceiptAt(handoff, request, digest)
	if err != nil {
		return Receipt{}, false, err
	}
	if existing == nil {
		return Receipt{}, false, fmt.Errorf("durable handoff receipt disappeared after publication")
	}
	if !bytes.Equal(existingRaw, raw) {
		return Receipt{}, false, fmt.Errorf("%w: handoff %s already has a different receipt", ErrHandoffConflict, request.HandoffID)
	}
	return *existing, !created, nil
}

// AppendEvent assigns the next sequence and creates a new immutable event
// file. It never rewrites a prior phase, including a prior failure.
func (s Store) AppendEvent(handoffID string, digest Digest, event HandoffEvent) (HandoffEvent, error) {
	handoff, err := s.openHandoff(handoffID, false)
	if err != nil {
		return HandoffEvent{}, err
	}
	defer func() { _ = handoff.Close() }()
	_, storedDigest, _, err := loadRequestAt(handoff, handoffID)
	if err != nil {
		return HandoffEvent{}, err
	}
	if digest != storedDigest {
		return HandoffEvent{}, fmt.Errorf("%w: handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, storedDigest, digest)
	}
	return appendEventAt(handoff, handoffID, storedDigest, event)
}

// AppendEventUnderLock appends relative to the exact admitted handoff
// directory retained by a held execution fence.
func (s Store) AppendEventUnderLock(lock *ExecutionLock, handoffID string, digest Digest, event HandoffEvent) (HandoffEvent, error) {
	_, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return HandoffEvent{}, fmt.Errorf("append event under exact admitted execution authority: %w", err)
	}
	defer func() { _ = handoff.Close() }()
	return appendEventAt(handoff, handoffID, digest, event)
}

func appendEventAt(handoff *os.File, handoffID string, digest Digest, event HandoffEvent) (HandoffEvent, error) {
	if !validPhase(event.Phase) {
		return HandoffEvent{}, fmt.Errorf("handoff phase %q is unsupported", event.Phase)
	}
	if event.At.IsZero() {
		return HandoffEvent{}, fmt.Errorf("handoff event time is required")
	}
	if event.Phase == PhaseCompleted {
		request, storedDigest, _, err := loadRequestAt(handoff, handoffID)
		if err != nil {
			return HandoffEvent{}, err
		}
		if storedDigest != digest {
			return HandoffEvent{}, fmt.Errorf("%w: handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, storedDigest, digest)
		}
		receipt, _, err := loadReceiptAt(handoff, request, storedDigest)
		if err != nil {
			return HandoffEvent{}, err
		}
		if receipt == nil {
			return HandoffEvent{}, fmt.Errorf("completed handoff phase requires an exact durable receipt")
		}
	}
	event.SchemaVersion = EventSchemaVersion
	event.HandoffID = handoffID
	event.RequestDigest = digest
	event.Diagnostic = strings.TrimSpace(event.Diagnostic)
	events, err := openEventsAt(handoff, true)
	if err != nil {
		return HandoffEvent{}, err
	}
	defer func() { _ = events.Close() }()

	// Concurrent appenders may select the same next sequence. Immutable
	// publication lets one win; the other rescans and appends after it.
	for attempts := 0; attempts < 100; attempts++ {
		next, err := nextEventSequenceAt(events)
		if err != nil {
			return HandoffEvent{}, err
		}
		event.Sequence = next
		raw, err := marshalJSON(event)
		if err != nil {
			return HandoffEvent{}, err
		}
		if len(raw) > maxEventBytes {
			return HandoffEvent{}, fmt.Errorf("handoff event exceeds %d bytes", maxEventBytes)
		}
		created, err := publishImmutableAt(events, eventFileName(next), raw, 0o600)
		if err != nil {
			return HandoffEvent{}, fmt.Errorf("append handoff event: %w", err)
		}
		if created {
			return event, nil
		}
	}
	return HandoffEvent{}, fmt.Errorf("append handoff event: too many concurrent writers")
}

// Load reads and validates one complete durable projection.
func (s Store) Load(handoffID string) (State, error) {
	handoff, err := s.openHandoff(handoffID, false)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = handoff.Close() }()
	return loadStateAt(handoff, handoffID, "")
}

// LoadUnderLock reads request, events, and receipt from the exact admitted
// aggregate retained by a held execution fence.
func (s Store) LoadUnderLock(lock *ExecutionLock, handoffID string, digest Digest) (State, error) {
	_, handoff, err := s.retainHandoffUnderLock(lock, handoffID, digest)
	if err != nil {
		return State{}, err
	}
	defer func() { _ = handoff.Close() }()
	return loadStateAt(handoff, handoffID, digest)
}

func loadStateAt(handoff *os.File, handoffID string, expectedDigest Digest) (State, error) {
	request, digest, _, err := loadRequestAt(handoff, handoffID)
	if err != nil {
		return State{}, err
	}
	if expectedDigest != "" && digest != expectedDigest {
		return State{}, fmt.Errorf("%w: handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, digest, expectedDigest)
	}
	events, err := loadEventsAt(handoff, request.HandoffID, digest)
	if err != nil {
		return State{}, err
	}
	receipt, _, err := loadReceiptAt(handoff, request, digest)
	if err != nil {
		return State{}, err
	}
	if receipt == nil {
		for _, event := range events {
			if event.Phase == PhaseCompleted {
				return State{}, fmt.Errorf("%w: handoff %s has a completed phase without an exact durable receipt", ErrHandoffConflict, handoffID)
			}
		}
	}
	return State{Request: request, Digest: digest, Events: events, Receipt: receipt}, nil
}

func (s Store) handoffDir(handoffID string) (string, error) {
	if strings.TrimSpace(s.Root) == "" {
		return "", fmt.Errorf("handoff store root is required")
	}
	if err := validateID("handoff_id", handoffID); err != nil {
		return "", err
	}
	return filepath.Join(s.Root, handoffID), nil
}

func (s Store) openHandoff(handoffID string, create bool) (*os.File, error) {
	if _, err := s.handoffDir(handoffID); err != nil {
		return nil, err
	}
	root, err := s.openRoot(create)
	if err != nil {
		return nil, err
	}
	defer func() { _ = root.Close() }()
	if create {
		if err := unix.Mkdirat(int(root.Fd()), handoffID, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create handoff directory: %w", err)
		}
	}
	return openHandoffAtRoot(root, handoffID)
}

func openHandoffAtRoot(root *os.File, handoffID string) (*os.File, error) {
	if root == nil {
		return nil, fmt.Errorf("open handoff directory: exact Store root is required")
	}
	if err := validateID("handoff_id", handoffID); err != nil {
		return nil, err
	}
	handoffFD, err := unix.Openat(int(root.Fd()), handoffID, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open handoff directory: %w", err)
	}
	handoff := os.NewFile(uintptr(handoffFD), "wb-session-store-handoff")
	if handoff == nil {
		_ = unix.Close(handoffFD)
		return nil, fmt.Errorf("wrap handoff directory")
	}
	return handoff, nil
}

func (s Store) openRoot(create bool) (*os.File, error) {
	if strings.TrimSpace(s.Root) == "" || s.Root != strings.TrimSpace(s.Root) {
		return nil, fmt.Errorf("handoff store root is required")
	}
	rootPath, err := filepath.Abs(s.Root)
	if err != nil {
		return nil, fmt.Errorf("resolve handoff store root: %w", err)
	}
	rootPath = filepath.Clean(rootPath)
	if create {
		if err := os.MkdirAll(rootPath, 0o700); err != nil {
			return nil, fmt.Errorf("create handoff store root: %w", err)
		}
	}
	rootFD, err := unix.Open(rootPath, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open handoff store root: %w", err)
	}
	root := os.NewFile(uintptr(rootFD), "wb-session-store-root")
	if root == nil {
		_ = unix.Close(rootFD)
		return nil, fmt.Errorf("wrap handoff store root")
	}
	return root, nil
}

func (s Store) retainHandoffUnderLock(lock *ExecutionLock, handoffID string, digest Digest) (Request, *os.File, error) {
	if lock == nil {
		return Request{}, nil, fmt.Errorf("execution lock is required")
	}
	lock.mu.Lock()
	request := lock.request
	lock.mu.Unlock()
	if request.HandoffID != handoffID {
		return Request{}, nil, fmt.Errorf("execution lock is for handoff %q, not %q", request.HandoffID, handoffID)
	}
	handoff, err := lock.RetainHandoffForStore(s.Root, request, digest)
	if err != nil {
		return Request{}, nil, err
	}
	admitted, storedDigest, _, err := loadRequestAt(handoff, handoffID)
	if err != nil {
		_ = handoff.Close()
		return Request{}, nil, err
	}
	if admitted != request || storedDigest != digest {
		_ = handoff.Close()
		return Request{}, nil, fmt.Errorf("%w: retained handoff request does not match execution authority", ErrHandoffConflict)
	}
	return request, handoff, nil
}

func loadRequestAt(handoff *os.File, handoffID string) (Request, Digest, []byte, error) {
	raw, err := readImmutableAt(handoff, requestFileName, maxExecutionLockRequestBytes, "handoff request")
	if err != nil {
		return Request{}, "", nil, err
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		return Request{}, "", nil, fmt.Errorf("decode durable handoff request: %w", err)
	}
	if request.HandoffID != handoffID {
		return Request{}, "", nil, fmt.Errorf("%w: directory %s contains request %s", ErrHandoffConflict, handoffID, request.HandoffID)
	}
	return request, DigestBytes(raw), raw, nil
}

func loadReceiptAt(handoff *os.File, request Request, digest Digest) (*Receipt, []byte, error) {
	raw, err := readImmutableAt(handoff, receiptFileName, maxReceiptBytes, "handoff receipt")
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, nil
	}
	if err != nil {
		return nil, nil, err
	}
	receipt, err := DecodeReceipt(raw)
	if err != nil {
		return nil, nil, fmt.Errorf("decode durable handoff receipt: %w", err)
	}
	if err := ValidateReceiptForRequest(receipt, request, digest); err != nil {
		return nil, nil, err
	}
	return &receipt, raw, nil
}

// ValidateReceiptForRequest binds every deterministic receipt field to the
// exact admitted request identity and Task 4's harness-selection policy.
func ValidateReceiptForRequest(receipt Receipt, request Request, digest Digest) error {
	if err := receipt.validate(); err != nil {
		return err
	}
	expectedReference, err := ExpectedTargetWorkLogReference(request, digest)
	if err != nil {
		return fmt.Errorf("derive target Work Log reference for handoff %s: %w", request.HandoffID, err)
	}
	expectedRuntime := strings.TrimSpace(request.RequestedHarness)
	if expectedRuntime == "" {
		expectedRuntime = strings.TrimSpace(request.SourceRuntime)
	}
	expectedModel := ""
	if expectedRuntime == strings.TrimSpace(request.SourceRuntime) {
		expectedModel = strings.TrimSpace(request.SourceModel)
	}
	want := map[string][2]string{
		"handoff_id":                {receipt.HandoffID, request.HandoffID},
		"request_digest":            {string(receipt.RequestDigest), string(digest)},
		"successor_wb_session_id":   {receipt.SuccessorWBSessionID, request.SuccessorWBSessionID},
		"predecessor_wb_session_id": {receipt.PredecessorWBSessionID, request.PredecessorWBSessionID},
		"target_machine":            {receipt.TargetMachine, request.TargetMachine},
		"pinned_commit":             {receipt.PinnedCommit, request.BundleCommit},
		"tmux_name":                 {receipt.TmuxName, "wb-session-" + request.SuccessorWBSessionID},
		"runtime":                   {receipt.Runtime, expectedRuntime},
		"model":                     {receipt.Model, expectedModel},
		"target_work_log_reference": {receipt.TargetWorkLogReference, expectedReference.String()},
	}
	for field, values := range want {
		if values[0] != values[1] {
			return fmt.Errorf("%w: receipt %s %q does not match request value %q for handoff %s", ErrHandoffConflict, field, values[0], values[1], request.HandoffID)
		}
	}
	return nil
}

func openEventsAt(handoff *os.File, create bool) (*os.File, error) {
	if handoff == nil {
		return nil, fmt.Errorf("open handoff events: handoff authority is required")
	}
	if create {
		if err := unix.Mkdirat(int(handoff.Fd()), eventsDirName, 0o700); err != nil && !errors.Is(err, unix.EEXIST) {
			return nil, fmt.Errorf("create handoff events directory: %w", err)
		}
	}
	fd, err := unix.Openat(int(handoff.Fd()), eventsDirName, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open handoff events directory: %w", err)
	}
	events := os.NewFile(uintptr(fd), "wb-session-handoff-events")
	if events == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap handoff events directory")
	}
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR || stat.Mode&0o777 != 0o700 {
		_ = events.Close()
		if err != nil {
			return nil, fmt.Errorf("inspect handoff events directory: %w", err)
		}
		return nil, fmt.Errorf("handoff events directory is not mode 0700")
	}
	return events, nil
}

func loadEventsAt(handoff *os.File, handoffID string, digest Digest) ([]HandoffEvent, error) {
	eventsDirectory, err := openEventsAt(handoff, false)
	if errors.Is(err, os.ErrNotExist) {
		return []HandoffEvent{}, nil
	}
	if err != nil {
		return nil, err
	}
	defer func() { _ = eventsDirectory.Close() }()
	entries, err := readEventEntries(eventsDirectory)
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".json") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	events := make([]HandoffEvent, 0, len(names))
	for index, name := range names {
		raw, err := readImmutableAt(eventsDirectory, name, maxEventBytes, "handoff event "+name)
		if err != nil {
			return nil, fmt.Errorf("read handoff event %s: %w", name, err)
		}
		var event HandoffEvent
		if err := decodeJSON(raw, &event); err != nil {
			return nil, fmt.Errorf("parse handoff event %s: %w", name, err)
		}
		wantSequence := uint64(index + 1)
		if event.SchemaVersion != EventSchemaVersion || event.Sequence != wantSequence ||
			event.HandoffID != handoffID || event.RequestDigest != digest || !validPhase(event.Phase) || event.At.IsZero() ||
			name != eventFileName(wantSequence) {
			return nil, fmt.Errorf("handoff event %s is inconsistent with aggregate %s", name, handoffID)
		}
		events = append(events, event)
	}
	return events, nil
}

func readEventEntries(directory *os.File) ([]os.DirEntry, error) {
	if _, err := directory.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind handoff events directory: %w", err)
	}
	entries, err := directory.ReadDir(-1)
	if err != nil {
		return nil, fmt.Errorf("read handoff events: %w", err)
	}
	return entries, nil
}

func nextEventSequenceAt(directory *os.File) (uint64, error) {
	entries, err := readEventEntries(directory)
	if err != nil {
		return 0, err
	}
	var maximum uint64
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		value := strings.TrimSuffix(entry.Name(), ".json")
		sequence, err := strconv.ParseUint(value, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("invalid handoff event name %q", entry.Name())
		}
		if eventFileName(sequence) != entry.Name() {
			return 0, fmt.Errorf("noncanonical handoff event name %q", entry.Name())
		}
		if sequence > maximum {
			maximum = sequence
		}
	}
	return maximum + 1, nil
}

func eventFileName(sequence uint64) string {
	return fmt.Sprintf("%020d.json", sequence)
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhaseOffered, PhaseReceived, PhaseWorktreeReady, PhaseSuccessorStarted, PhaseCompleted, PhaseFailed, PhaseCancelled:
		return true
	default:
		return false
	}
}

func readImmutableAt(directory *os.File, name string, limit int64, label string) ([]byte, error) {
	if directory == nil {
		return nil, fmt.Errorf("open %s: directory authority is required", label)
	}
	if err := repairPendingLinkAt(directory, name); err != nil {
		return nil, fmt.Errorf("repair interrupted %s publication: %w", label, err)
	}
	fd, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", label, err)
	}
	file := os.NewFile(uintptr(fd), "wb-session-"+strings.ReplaceAll(label, " ", "-"))
	if file == nil {
		_ = unix.Close(fd)
		return nil, fmt.Errorf("wrap %s", label)
	}
	defer func() { _ = file.Close() }()
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return nil, fmt.Errorf("inspect %s: %w", label, err)
	}
	if stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o777 != 0o600 || stat.Nlink != 1 || stat.Size < 0 || stat.Size > limit {
		return nil, fmt.Errorf("%s is not one single-link bounded regular mode 0600 file", label)
	}
	raw, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", label, err)
	}
	if int64(len(raw)) > limit {
		return nil, fmt.Errorf("%s exceeds %d bytes", label, limit)
	}
	if int64(len(raw)) != stat.Size {
		return nil, fmt.Errorf("%s size changed while it was read", label)
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind %s: %w", label, err)
	}
	verification, err := io.ReadAll(io.LimitReader(file, limit+1))
	if err != nil {
		return nil, fmt.Errorf("verify %s: %w", label, err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(fd, &after); err != nil {
		return nil, fmt.Errorf("reinspect %s: %w", label, err)
	}
	if after.Dev != stat.Dev || after.Ino != stat.Ino || after.Mode != stat.Mode || after.Nlink != stat.Nlink ||
		after.Size != stat.Size || int64(len(verification)) != stat.Size || !bytes.Equal(verification, raw) {
		return nil, fmt.Errorf("%s changed while it was verified", label)
	}
	return raw, nil
}

func publishImmutableAt(directory *os.File, name string, raw []byte, mode os.FileMode) (bool, error) {
	if directory == nil {
		return false, fmt.Errorf("immutable publication directory authority is required")
	}
	if filepath.Base(name) != name || name == "." || name == ".." {
		return false, fmt.Errorf("immutable publication name %q must be one base name", name)
	}
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil {
		return false, fmt.Errorf("generate immutable temporary name: %w", err)
	}
	temporaryName := ".pending-" + hex.EncodeToString(random[:])
	fd, err := unix.Openat(int(directory.Fd()), temporaryName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_NOFOLLOW|unix.O_CLOEXEC, uint32(mode.Perm()))
	if err != nil {
		return false, fmt.Errorf("create immutable temporary file: %w", err)
	}
	temporary := os.NewFile(uintptr(fd), "wb-session-immutable-temporary")
	if temporary == nil {
		_ = unix.Close(fd)
		_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		return false, fmt.Errorf("wrap immutable temporary file")
	}
	removeTemporary := true
	defer func() {
		_ = temporary.Close()
		if removeTemporary {
			_ = unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
		}
	}()
	if err := unix.Fchmod(fd, uint32(mode.Perm())); err != nil {
		return false, fmt.Errorf("secure immutable temporary file: %w", err)
	}
	if _, err := temporary.Write(raw); err != nil {
		return false, fmt.Errorf("write immutable temporary file: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return false, fmt.Errorf("sync immutable temporary file: %w", err)
	}
	created := true
	if err := unix.Linkat(int(directory.Fd()), temporaryName, int(directory.Fd()), name, 0); err != nil {
		if !errors.Is(err, unix.EEXIST) {
			return false, fmt.Errorf("publish immutable file: %w", err)
		}
		created = false
	}
	if err := unix.Unlinkat(int(directory.Fd()), temporaryName, 0); err != nil && !errors.Is(err, unix.ENOENT) {
		return false, fmt.Errorf("remove immutable temporary name: %w", err)
	}
	removeTemporary = false
	if created {
		publishedFD, err := unix.Openat(int(directory.Fd()), name, unix.O_RDONLY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return false, fmt.Errorf("open published immutable file: %w", err)
		}
		published := os.NewFile(uintptr(publishedFD), "wb-session-published-immutable")
		if published == nil {
			_ = unix.Close(publishedFD)
			return false, fmt.Errorf("wrap published immutable file")
		}
		var publishedStat unix.Stat_t
		statErr := unix.Fstat(publishedFD, &publishedStat)
		same := sameFile(temporary, published)
		closeErr := published.Close()
		if statErr != nil || closeErr != nil || !same || publishedStat.Mode&unix.S_IFMT != unix.S_IFREG ||
			uint32(publishedStat.Mode&0o777) != uint32(mode.Perm()) || publishedStat.Nlink != 1 {
			if statErr != nil {
				return false, fmt.Errorf("inspect published immutable file: %w", statErr)
			}
			if closeErr != nil {
				return false, fmt.Errorf("close published immutable file: %w", closeErr)
			}
			return false, fmt.Errorf("published immutable file does not retain the exact prepared inode")
		}
	}
	if err := unix.Fsync(int(directory.Fd())); err != nil {
		return false, fmt.Errorf("sync immutable publication directory: %w", err)
	}
	return created, nil
}

func repairPendingLinkAt(directory *os.File, finalName string) error {
	finalFD, err := unix.Openat(int(directory.Fd()), finalName, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	final := os.NewFile(uintptr(finalFD), "wb-session-interrupted-publication-final")
	if final == nil {
		_ = unix.Close(finalFD)
		return fmt.Errorf("wrap final immutable file")
	}
	defer func() { _ = final.Close() }()
	var before unix.Stat_t
	if err := unix.Fstat(finalFD, &before); err != nil {
		return err
	}
	if before.Nlink <= 1 {
		return nil
	}
	scanFD, err := unix.Openat(int(directory.Fd()), ".", unix.O_RDONLY|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	scan := os.NewFile(uintptr(scanFD), "wb-session-interrupted-publication-scan")
	if scan == nil {
		_ = unix.Close(scanFD)
		return fmt.Errorf("wrap immutable publication directory scan")
	}
	entries, readErr := scan.ReadDir(-1)
	_ = scan.Close()
	if readErr != nil {
		return readErr
	}
	repaired := false
	for _, entry := range entries {
		if !isPendingPublicationName(entry.Name()) {
			continue
		}
		pendingFD, err := unix.Openat(int(directory.Fd()), entry.Name(), unix.O_RDONLY|unix.O_NONBLOCK|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			if errors.Is(err, unix.ENOENT) {
				continue
			}
			return err
		}
		pending := os.NewFile(uintptr(pendingFD), "wb-session-interrupted-publication-pending")
		if pending == nil {
			_ = unix.Close(pendingFD)
			return fmt.Errorf("wrap immutable pending file")
		}
		same := sameFile(final, pending)
		_ = pending.Close()
		if !same {
			continue
		}
		if err := unix.Unlinkat(int(directory.Fd()), entry.Name(), 0); err != nil && !errors.Is(err, unix.ENOENT) {
			return err
		}
		repaired = true
	}
	if repaired {
		if err := unix.Fsync(int(directory.Fd())); err != nil {
			return err
		}
	}
	var after unix.Stat_t
	if err := unix.Fstat(finalFD, &after); err != nil {
		return err
	}
	if after.Dev != before.Dev || after.Ino != before.Ino || after.Nlink != 1 {
		return fmt.Errorf("final immutable file is not single-link after pending-link repair: %d links remain", after.Nlink)
	}
	return nil
}

func isPendingPublicationName(name string) bool {
	const prefix = ".pending-"
	if !strings.HasPrefix(name, prefix) || len(name) != len(prefix)+32 {
		return false
	}
	encoded := strings.TrimPrefix(name, prefix)
	decoded, err := hex.DecodeString(encoded)
	return err == nil && hex.EncodeToString(decoded) == encoded
}
