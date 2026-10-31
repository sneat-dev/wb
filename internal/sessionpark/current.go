package sessionpark

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/session"
	"github.com/sneat-dev/wb/internal/sessionmove"
)

const currentPreparationName = "current-resume.json"
const currentAcknowledgmentName = "current-resume-ack.json"

// CurrentPreparation is immutable authority for continuing in a real, already
// running harness. It neither launches a process nor changes member custody.
type CurrentPreparation struct {
	SchemaVersion      int                         `json:"schema_version"`
	ParkedSessionID    string                      `json:"parked_session_id"`
	BundleDigest       sessionmove.Digest          `json:"bundle_digest"`
	Caller             session.Record              `json:"caller"`
	Successor          session.Record              `json:"successor"`
	ContinuationDigest sessionmove.Digest          `json:"continuation_digest"`
	PreparedAt         time.Time                   `json:"prepared_at"`
	ParentDigest       sessionmove.Digest          `json:"parent_digest,omitempty"`
	RecoveryIndex      uint64                      `json:"recovery_index,omitempty"`
	UnstartedLaunch    *UnstartedLaunchAttestation `json:"unstarted_launch_attestation,omitempty"`
}

type CurrentAcknowledgment struct {
	SchemaVersion      int                `json:"schema_version"`
	ParkedSessionID    string             `json:"parked_session_id"`
	PreparationDigest  sessionmove.Digest `json:"preparation_digest"`
	ContinuationDigest sessionmove.Digest `json:"continuation_digest"`
}

// CurrentCallerMatches admits an identical retry after the PID registration
// became this preparation's successor. An unrelated re-registration, recycled
// PID, changed harness identity, or different WB session remains a conflict.
func (p CurrentPreparation) CurrentCallerMatches(caller session.Record) bool {
	return caller.PID == p.Caller.PID && caller.ProcessStartedAt.Equal(p.Caller.ProcessStartedAt) &&
		caller.NativeHarnessID == p.Caller.NativeHarnessID && caller.Runtime == p.Caller.Runtime && caller.Machine == p.Caller.Machine && caller.Scope == p.Caller.Scope &&
		(caller.WBSessionID == p.Caller.WBSessionID || caller.WBSessionID == p.Successor.WBSessionID)
}

func (p CurrentPreparation) Digest() sessionmove.Digest {
	raw, _ := json.Marshal(p)
	return sessionmove.DigestBytes(raw)
}

// PrepareCurrentUnderLock is called only while the caller also holds the
// complete member custody barrier. The service authenticates caller against
// native process evidence before invoking this method.
func (s Store) PrepareCurrentUnderLock(lock *SourceLock, caller session.Record, resolved map[string]string, now time.Time) (CurrentPreparation, string, error) {
	return s.PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock, caller, resolved, now, "")
}

// PrepareCurrentWithUnstartedAcknowledgmentUnderLock accepts only a registered
// operator's exact historical-preflight assertion for legacy empty bookkeeping.
func (s Store) PrepareCurrentWithUnstartedAcknowledgmentUnderLock(lock *SourceLock, caller session.Record, resolved map[string]string, now time.Time, acknowledgment sessionmove.Digest) (CurrentPreparation, string, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return CurrentPreparation{}, "", fmt.Errorf("current pickup requires retained aggregate authority")
	}
	if caller.PID <= 0 || caller.WBSessionID == "" || caller.NativeHarnessID == "" || caller.ProcessStartedAt.IsZero() || caller.StartedAt.IsZero() || caller.Machine == "" || caller.Machine != lock.bundle.Source.Machine ||
		(caller.Runtime != "codex" && caller.Runtime != "claude-code") {
		return CurrentPreparation{}, "", fmt.Errorf("current pickup requires an identifiable registered live harness")
	}
	attestation, err := s.unstartedAttestationForCallerUnderLock(lock, caller, acknowledgment)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if _, _, err := s.claimResumeRouteUnderLock(lock, ResumeRouteLocal, "", "", sessionmove.SSHConfig{}, now); err != nil {
		return CurrentPreparation{}, "", err
	}
	path, continuation, err := s.EnsureLocalSuccessorContextUnderLock(lock, resolved)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if existing, found, err := s.CurrentPreparationUnderLock(lock); err != nil {
		return CurrentPreparation{}, "", err
	} else if found {
		if !existing.CurrentCallerMatches(caller) || existing.ContinuationDigest != sessionmove.DigestBytes(continuation) {
			return CurrentPreparation{}, "", fmt.Errorf("parked session already has a different current-harness pickup")
		}
		return existing, path, nil
	}
	bundleRaw, err := EncodeBundle(lock.bundle)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	authority, err := LocalLaunchAuthority(lock.bundle, sessionmove.DigestBytes(bundleRaw), path, continuation)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if now.IsZero() {
		return CurrentPreparation{}, "", fmt.Errorf("current pickup preparation requires a timestamp")
	}
	successor := caller
	successor.WBSessionID = authority.SuccessorWBSessionID
	successor.PredecessorWBSessionID = lock.bundle.Source.WBSessionID
	successor.HandoffID = lock.parkID
	successor.StartedAt = now.UTC()
	successor.Lifecycle, successor.ParkedSessionID, successor.TmuxName = "", "", ""
	prepared := CurrentPreparation{SchemaVersion: SchemaVersion, ParkedSessionID: lock.parkID, BundleDigest: sessionmove.DigestBytes(bundleRaw),
		Caller: caller, Successor: successor, ContinuationDigest: sessionmove.DigestBytes(continuation), PreparedAt: now.UTC(), UnstartedLaunch: attestation}
	if attestation != nil {
		if err := s.validateUnstartedAttestationUnderLock(lock, caller, attestation); err != nil {
			return CurrentPreparation{}, "", err
		}
	}
	raw, err := json.Marshal(prepared)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if err := writeExactPrivateAt(lock.aggregate, currentPreparationName, raw); err != nil {
		return CurrentPreparation{}, "", err
	}
	return prepared, path, nil
}

func (s Store) currentPreparationFileUnderLock(lock *SourceLock, name string) (CurrentPreparation, bool, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return CurrentPreparation{}, false, fmt.Errorf("read current pickup requires retained aggregate authority")
	}
	raw, err := readPrivateRegularAt(lock.aggregate, name, 32<<10)
	if errors.Is(err, os.ErrNotExist) {
		return CurrentPreparation{}, false, nil
	}
	if err != nil {
		return CurrentPreparation{}, false, err
	}
	var p CurrentPreparation
	if err := strictDecode(raw, &p); err != nil {
		return CurrentPreparation{}, false, err
	}
	if p.UnstartedLaunch != nil {
		if p.RecoveryIndex != 0 || p.ParentDigest != "" {
			return CurrentPreparation{}, false, fmt.Errorf("recovery link cannot replace the original unstarted launch attestation")
		}
		if err := s.validateUnstartedAttestationUnderLock(lock, p.Caller, p.UnstartedLaunch); err != nil {
			return CurrentPreparation{}, false, err
		}
	}
	if err := p.Caller.ValidateScope(); err != nil {
		return CurrentPreparation{}, false, err
	}
	if err := p.Successor.ValidateScope(); err != nil {
		return CurrentPreparation{}, false, err
	}
	bundleRaw, err := EncodeBundle(lock.bundle)
	if err != nil {
		return CurrentPreparation{}, false, err
	}
	if p.SchemaVersion != SchemaVersion || p.ParkedSessionID != lock.parkID || p.BundleDigest != sessionmove.DigestBytes(bundleRaw) ||
		!validDigest(p.ContinuationDigest) || p.PreparedAt.IsZero() || p.Successor.PID != p.Caller.PID ||
		p.Successor.WBSessionID == p.Caller.WBSessionID || p.Successor.PredecessorWBSessionID != lock.bundle.Source.WBSessionID ||
		p.Successor.HandoffID != lock.parkID || p.Caller.ProcessStartedAt.IsZero() || p.Caller.NativeHarnessID == "" {
		return CurrentPreparation{}, false, fmt.Errorf("current pickup preparation contradicts the retained bundle")
	}
	path, continuation, found, err := s.LoadLocalSuccessorContextUnderLock(lock)
	if err != nil {
		return CurrentPreparation{}, false, err
	}
	if !found || sessionmove.DigestBytes(continuation) != p.ContinuationDigest {
		return CurrentPreparation{}, false, fmt.Errorf("current pickup context does not match preparation")
	}
	authority, err := LocalLaunchAuthority(lock.bundle, p.BundleDigest, path, continuation)
	if err != nil {
		return CurrentPreparation{}, false, err
	}
	expected := p.Caller
	expected.WBSessionID, expected.PredecessorWBSessionID, expected.HandoffID = authority.SuccessorWBSessionID, lock.bundle.Source.WBSessionID, lock.parkID
	expected.StartedAt = p.PreparedAt
	expected.Lifecycle, expected.ParkedSessionID, expected.TmuxName = "", "", ""
	if !reflect.DeepEqual(expected, p.Successor) {
		return CurrentPreparation{}, false, fmt.Errorf("current pickup successor differs from its prepared caller")
	}
	return p, true, nil
}

// AcknowledgeCurrentUnderLock durably binds the digest read by the existing
// harness before registration or attachment may occur. It contains no prompt.
func (s Store) AcknowledgeCurrentUnderLock(lock *SourceLock, caller session.Record, acknowledged sessionmove.Digest) (CurrentPreparation, error) {
	p, found, err := s.CurrentPreparationUnderLock(lock)
	if err != nil {
		return CurrentPreparation{}, err
	}
	if !found || !p.CurrentCallerMatches(caller) || acknowledged != p.ContinuationDigest {
		return CurrentPreparation{}, fmt.Errorf("current pickup requires acknowledgment of its exact continuation by its prepared caller")
	}
	_, continuation, exists, err := s.LoadLocalSuccessorContextUnderLock(lock)
	if err != nil {
		return CurrentPreparation{}, err
	}
	if !exists || sessionmove.DigestBytes(continuation) != acknowledged {
		return CurrentPreparation{}, fmt.Errorf("current pickup continuation changed before acknowledgment")
	}
	ack := CurrentAcknowledgment{SchemaVersion: SchemaVersion, ParkedSessionID: lock.parkID, PreparationDigest: p.Digest(), ContinuationDigest: acknowledged}
	raw, err := json.Marshal(ack)
	if err != nil {
		return CurrentPreparation{}, err
	}
	if err := writeExactPrivateAt(lock.aggregate, currentAckName(p), raw); err != nil {
		return CurrentPreparation{}, err
	}
	return p, nil
}

const maxCurrentRecoveries = 16

func currentRecoveryName(index uint64) string {
	return fmt.Sprintf("current-recovery-%03d.json", index)
}
func currentAckName(p CurrentPreparation) string {
	if p.RecoveryIndex == 0 {
		return currentAcknowledgmentName
	}
	return fmt.Sprintf("current-recovery-%03d-ack.json", p.RecoveryIndex)
}

// CurrentPreparationsUnderLock validates every immutable link, including gaps,
// duplicates and cycles, before any of their owner attempts may be replayed.
func (s Store) CurrentPreparationsUnderLock(lock *SourceLock) ([]CurrentPreparation, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return nil, fmt.Errorf("current recovery requires retained aggregate authority")
	}
	listing, err := openPrivateDirectoryAt(lock.aggregate, ".")
	if err != nil {
		return nil, err
	}
	entries, readErr := listing.ReadDir(256)
	_ = listing.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) {
		return nil, readErr
	}
	if len(entries) == 256 {
		return nil, fmt.Errorf("current recovery directory exceeds bounded entry count")
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "current-recovery-") {
			indexText := strings.TrimSuffix(strings.TrimSuffix(strings.TrimPrefix(entry.Name(), "current-recovery-"), ".json"), "-ack")
			index, err := strconv.ParseUint(indexText, 10, 64)
			if err != nil || index == 0 || index > maxCurrentRecoveries || entry.Name() != currentRecoveryName(index) && entry.Name() != fmt.Sprintf("current-recovery-%03d-ack.json", index) {
				return nil, fmt.Errorf("current recovery directory contains an invalid or out-of-range link")
			}
		}
	}
	first, found, err := s.currentPreparationFileUnderLock(lock, currentPreparationName)
	if err != nil {
		return nil, err
	}
	chain := []CurrentPreparation{}
	if found {
		if first.RecoveryIndex != 0 || first.ParentDigest != "" {
			return nil, fmt.Errorf("initial current pickup has recovery metadata")
		}
		chain = append(chain, first)
	}
	gap := !found
	for index := uint64(1); index <= maxCurrentRecoveries+1; index++ {
		node, exists, err := s.currentPreparationFileUnderLock(lock, currentRecoveryName(index))
		if err != nil {
			return nil, err
		}
		if !exists {
			gap = true
			continue
		}
		if gap || index > maxCurrentRecoveries || len(chain) == 0 {
			return nil, fmt.Errorf("current recovery chain has a gap or exceeds its bound")
		}
		previous := chain[len(chain)-1]
		if node.RecoveryIndex != index || node.ParentDigest != previous.Digest() || node.Successor.WBSessionID != first.Successor.WBSessionID || !node.PreparedAt.After(previous.PreparedAt) || node.ContinuationDigest != first.ContinuationDigest {
			return nil, fmt.Errorf("current recovery link contradicts its immutable parent")
		}
		for _, ancestor := range chain {
			if node.Caller.PID == ancestor.Caller.PID && node.Caller.ProcessStartedAt.Equal(ancestor.Caller.ProcessStartedAt) {
				return nil, fmt.Errorf("current recovery repeats an actor generation")
			}
		}
		chain = append(chain, node)
	}
	return chain, nil
}

func (s Store) CurrentPreparationUnderLock(lock *SourceLock) (CurrentPreparation, bool, error) {
	chain, err := s.CurrentPreparationsUnderLock(lock)
	if err != nil {
		return CurrentPreparation{}, false, err
	}
	if len(chain) == 0 {
		return CurrentPreparation{}, false, nil
	}
	return chain[len(chain)-1], true, nil
}

// PrepareCurrentRecoveryUnderLock appends a new actor without changing the
// original logical winner, preparation, acknowledgment or completed receipt.
// The caller already holds all member journals and proves the old generation
// gone; unknown liveness is a refusal, including a PID whose start is unreadable.
func (s Store) PrepareCurrentRecoveryUnderLock(lock *SourceLock, caller session.Record, resolved map[string]string, now time.Time, proveGone func(session.Record) error) (CurrentPreparation, string, error) {
	if lock == nil || !lock.held(s.Root, lock.parkID) {
		return CurrentPreparation{}, "", fmt.Errorf("current recovery requires retained aggregate authority")
	}
	if err := s.requireUnstartedLauncherUnderLock(lock); err != nil {
		return CurrentPreparation{}, "", err
	}
	chain, err := s.CurrentPreparationsUnderLock(lock)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if len(chain) == 0 {
		return s.PrepareCurrentUnderLock(lock, caller, resolved, now)
	}
	previous := chain[len(chain)-1]
	if previous.CurrentCallerMatches(caller) {
		return s.PrepareCurrentUnderLock(lock, caller, resolved, now)
	}
	if len(chain) > maxCurrentRecoveries {
		return CurrentPreparation{}, "", fmt.Errorf("current recovery chain reached its bounded limit")
	}
	if proveGone == nil {
		return CurrentPreparation{}, "", fmt.Errorf("current recovery has no native dead-generation proof")
	}
	if err := proveGone(previous.Caller); err != nil {
		return CurrentPreparation{}, "", fmt.Errorf("current recovery refuses a live or ambiguous previous actor: %w", err)
	}
	if caller.PID <= 0 || caller.WBSessionID == "" || caller.WBSessionID == previous.Successor.WBSessionID || caller.NativeHarnessID == "" || caller.ProcessStartedAt.IsZero() || caller.StartedAt.IsZero() || caller.Machine != lock.bundle.Source.Machine || (caller.Runtime != "codex" && caller.Runtime != "claude-code") || !now.After(previous.PreparedAt) {
		return CurrentPreparation{}, "", fmt.Errorf("current recovery requires an identifiable fresh live actor")
	}
	path, continuation, err := s.EnsureLocalSuccessorContextUnderLock(lock, resolved)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if sessionmove.DigestBytes(continuation) != previous.ContinuationDigest {
		return CurrentPreparation{}, "", fmt.Errorf("current recovery context differs from original")
	}
	successor := caller
	successor.WBSessionID = previous.Successor.WBSessionID
	successor.PredecessorWBSessionID = lock.bundle.Source.WBSessionID
	successor.HandoffID = lock.parkID
	successor.StartedAt = now.UTC()
	successor.Lifecycle, successor.ParkedSessionID, successor.TmuxName = "", "", ""
	node := CurrentPreparation{SchemaVersion: SchemaVersion, ParkedSessionID: lock.parkID, BundleDigest: previous.BundleDigest, Caller: caller, Successor: successor, ContinuationDigest: previous.ContinuationDigest, PreparedAt: now.UTC(), ParentDigest: previous.Digest(), RecoveryIndex: uint64(len(chain))}
	raw, err := json.Marshal(node)
	if err != nil {
		return CurrentPreparation{}, "", err
	}
	if err := writeExactPrivateAt(lock.aggregate, currentRecoveryName(node.RecoveryIndex), raw); err != nil {
		return CurrentPreparation{}, "", err
	}
	return node, path, nil
}

func (s Store) CompleteCurrentUnderLock(lock *SourceLock, p CurrentPreparation) error {
	state, err := s.LoadUnderLock(lock)
	if err != nil {
		return err
	}
	if state.Status != StatusResumed || state.Successor == nil || state.Successor.WBSessionID != p.Successor.WBSessionID {
		return fmt.Errorf("current completion requires the retained logical successor receipt")
	}
	latest, found, err := s.CurrentPreparationUnderLock(lock)
	if err != nil {
		return err
	}
	if !found || latest.Digest() != p.Digest() {
		return fmt.Errorf("current completion differs from retained actor")
	}
	ack, err := readPrivateRegularAt(lock.aggregate, currentAckName(p), 32<<10)
	if err != nil {
		return err
	}
	var acknowledged CurrentAcknowledgment
	if err := strictDecode(ack, &acknowledged); err != nil {
		return err
	}
	if acknowledged.PreparationDigest != p.Digest() || acknowledged.ContinuationDigest != p.ContinuationDigest {
		return fmt.Errorf("current completion has no exact actor acknowledgment")
	}
	raw, err := json.Marshal(struct {
		SchemaVersion     int                `json:"schema_version"`
		ParkedSessionID   string             `json:"parked_session_id"`
		PreparationDigest sessionmove.Digest `json:"preparation_digest"`
		Actor             session.Record     `json:"actor"`
	}{SchemaVersion, lock.parkID, p.Digest(), p.Successor})
	if err != nil {
		return err
	}
	return writeExactPrivateAt(lock.aggregate, fmt.Sprintf("current-completed-%03d.json", p.RecoveryIndex), raw)
}
