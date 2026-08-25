package sessionmove

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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
	directory, err := s.handoffDir(request.HandoffID)
	if err != nil {
		return Admission{}, err
	}
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return Admission{}, fmt.Errorf("create handoff directory: %w", err)
	}
	created, err := publishImmutable(filepath.Join(directory, requestFileName), raw, 0o600)
	if err != nil {
		return Admission{}, fmt.Errorf("persist handoff request: %w", err)
	}
	if !created {
		existingRaw, readErr := os.ReadFile(filepath.Join(directory, requestFileName))
		if readErr != nil {
			return Admission{}, fmt.Errorf("read durable handoff request: %w", readErr)
		}
		existingDigest := DigestBytes(existingRaw)
		if existingDigest != digest || !bytes.Equal(existingRaw, raw) {
			return Admission{}, fmt.Errorf("%w: handoff %s already has digest %s, received %s", ErrHandoffConflict, request.HandoffID, existingDigest, digest)
		}
		existing, decodeErr := DecodeRequest(existingRaw)
		if decodeErr != nil {
			return Admission{}, fmt.Errorf("decode durable handoff request: %w", decodeErr)
		}
		if existing.HandoffID != request.HandoffID {
			return Admission{}, fmt.Errorf("%w: directory %s contains request %s", ErrHandoffConflict, request.HandoffID, existing.HandoffID)
		}
	}

	receipt, err := loadReceipt(directory, request, digest)
	if err != nil {
		return Admission{}, err
	}
	return Admission{Request: request, Digest: digest, Replay: !created, Receipt: receipt}, nil
}

// SaveReceipt durably publishes the target receipt without replacement.
// Repeating the identical receipt is a successful replay; any change is a
// conflict because a handoff can have only one successor identity.
func (s Store) SaveReceipt(handoffID string, digest Digest, receipt Receipt) (Receipt, bool, error) {
	request, storedDigest, directory, err := s.loadRequest(handoffID)
	if err != nil {
		return Receipt{}, false, err
	}
	if digest != storedDigest {
		return Receipt{}, false, fmt.Errorf("%w: handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, storedDigest, digest)
	}
	if receipt.HandoffID != request.HandoffID || receipt.RequestDigest != storedDigest ||
		receipt.SuccessorWBSessionID != request.SuccessorWBSessionID ||
		receipt.PredecessorWBSessionID != request.PredecessorWBSessionID ||
		receipt.TargetMachine != request.TargetMachine || receipt.PinnedCommit != request.BundleCommit {
		return Receipt{}, false, fmt.Errorf("%w: receipt does not match request %s", ErrHandoffConflict, handoffID)
	}
	raw, err := EncodeReceipt(receipt)
	if err != nil {
		return Receipt{}, false, err
	}
	created, err := publishImmutable(filepath.Join(directory, receiptFileName), raw, 0o600)
	if err != nil {
		return Receipt{}, false, fmt.Errorf("persist handoff receipt: %w", err)
	}
	if created {
		return receipt, false, nil
	}
	existingRaw, err := os.ReadFile(filepath.Join(directory, receiptFileName))
	if err != nil {
		return Receipt{}, false, fmt.Errorf("read durable handoff receipt: %w", err)
	}
	if !bytes.Equal(existingRaw, raw) {
		return Receipt{}, false, fmt.Errorf("%w: handoff %s already has a different receipt", ErrHandoffConflict, handoffID)
	}
	existing, err := loadReceipt(directory, request, storedDigest)
	if err != nil {
		return Receipt{}, false, err
	}
	if existing == nil {
		return Receipt{}, false, fmt.Errorf("durable handoff receipt disappeared during replay")
	}
	return *existing, true, nil
}

// AppendEvent assigns the next sequence and creates a new immutable event
// file. It never rewrites a prior phase, including a prior failure.
func (s Store) AppendEvent(handoffID string, digest Digest, event HandoffEvent) (HandoffEvent, error) {
	_, storedDigest, directory, err := s.loadRequest(handoffID)
	if err != nil {
		return HandoffEvent{}, err
	}
	if digest != storedDigest {
		return HandoffEvent{}, fmt.Errorf("%w: handoff %s has digest %s, received %s", ErrHandoffConflict, handoffID, storedDigest, digest)
	}
	if !validPhase(event.Phase) {
		return HandoffEvent{}, fmt.Errorf("handoff phase %q is unsupported", event.Phase)
	}
	if event.At.IsZero() {
		return HandoffEvent{}, fmt.Errorf("handoff event time is required")
	}
	event.SchemaVersion = EventSchemaVersion
	event.HandoffID = handoffID
	event.RequestDigest = storedDigest
	event.Diagnostic = strings.TrimSpace(event.Diagnostic)
	eventsDirectory := filepath.Join(directory, eventsDirName)
	if err := os.MkdirAll(eventsDirectory, 0o700); err != nil {
		return HandoffEvent{}, fmt.Errorf("create handoff events directory: %w", err)
	}

	// Concurrent appenders may select the same next sequence. Immutable
	// publication lets one win; the other rescans and appends after it.
	for attempts := 0; attempts < 100; attempts++ {
		next, err := nextEventSequence(eventsDirectory)
		if err != nil {
			return HandoffEvent{}, err
		}
		event.Sequence = next
		raw, err := marshalJSON(event)
		if err != nil {
			return HandoffEvent{}, err
		}
		created, err := publishImmutable(eventPath(eventsDirectory, next), raw, 0o600)
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
	request, digest, directory, err := s.loadRequest(handoffID)
	if err != nil {
		return State{}, err
	}
	events, err := loadEvents(filepath.Join(directory, eventsDirName), request.HandoffID, digest)
	if err != nil {
		return State{}, err
	}
	receipt, err := loadReceipt(directory, request, digest)
	if err != nil {
		return State{}, err
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

func (s Store) loadRequest(handoffID string) (Request, Digest, string, error) {
	directory, err := s.handoffDir(handoffID)
	if err != nil {
		return Request{}, "", "", err
	}
	raw, err := os.ReadFile(filepath.Join(directory, requestFileName))
	if err != nil {
		return Request{}, "", "", fmt.Errorf("read handoff request: %w", err)
	}
	request, err := DecodeRequest(raw)
	if err != nil {
		return Request{}, "", "", fmt.Errorf("decode durable handoff request: %w", err)
	}
	if request.HandoffID != handoffID {
		return Request{}, "", "", fmt.Errorf("%w: directory %s contains request %s", ErrHandoffConflict, handoffID, request.HandoffID)
	}
	return request, DigestBytes(raw), directory, nil
}

func loadReceipt(directory string, request Request, digest Digest) (*Receipt, error) {
	raw, err := os.ReadFile(filepath.Join(directory, receiptFileName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read handoff receipt: %w", err)
	}
	receipt, err := DecodeReceipt(raw)
	if err != nil {
		return nil, fmt.Errorf("decode durable handoff receipt: %w", err)
	}
	if receipt.HandoffID != request.HandoffID || receipt.RequestDigest != digest ||
		receipt.SuccessorWBSessionID != request.SuccessorWBSessionID ||
		receipt.PredecessorWBSessionID != request.PredecessorWBSessionID ||
		receipt.TargetMachine != request.TargetMachine || receipt.PinnedCommit != request.BundleCommit {
		return nil, fmt.Errorf("%w: durable receipt does not match request %s", ErrHandoffConflict, request.HandoffID)
	}
	return &receipt, nil
}

func loadEvents(directory, handoffID string, digest Digest) ([]HandoffEvent, error) {
	entries, err := os.ReadDir(directory)
	if errors.Is(err, os.ErrNotExist) {
		return []HandoffEvent{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read handoff events: %w", err)
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
		raw, err := os.ReadFile(filepath.Join(directory, name))
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
			name != filepath.Base(eventPath(directory, wantSequence)) {
			return nil, fmt.Errorf("handoff event %s is inconsistent with aggregate %s", name, handoffID)
		}
		events = append(events, event)
	}
	return events, nil
}

func nextEventSequence(directory string) (uint64, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return 0, fmt.Errorf("read handoff events: %w", err)
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
		if sequence > maximum {
			maximum = sequence
		}
	}
	return maximum + 1, nil
}

func eventPath(directory string, sequence uint64) string {
	return filepath.Join(directory, fmt.Sprintf("%020d.json", sequence))
}

func validPhase(phase Phase) bool {
	switch phase {
	case PhaseOffered, PhaseReceived, PhaseWorktreeReady, PhaseSuccessorStarted, PhaseCompleted, PhaseFailed, PhaseCancelled:
		return true
	default:
		return false
	}
}

// publishImmutable exposes complete bytes atomically and never replaces an
// existing destination. A hard link turns a fully written sibling temp file
// into the visible record in one no-replace filesystem operation.
func publishImmutable(path string, raw []byte, mode os.FileMode) (bool, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return false, err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".pending-*")
	if err != nil {
		return false, err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if _, err := temporary.Write(raw); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return false, err
	}
	if err := temporary.Close(); err != nil {
		return false, err
	}
	if err := os.Link(temporaryPath, path); err != nil {
		if errors.Is(err, os.ErrExist) {
			return false, nil
		}
		return false, err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return false, err
	}
	if err := directory.Sync(); err != nil {
		_ = directory.Close()
		return false, err
	}
	if err := directory.Close(); err != nil {
		return false, err
	}
	return true, nil
}
