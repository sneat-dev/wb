package worktrees

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"
)

const legacyMissingClaimRecoveryType = "legacy_missing_private_claim"

// legacyMissingClaimRecovery is the append-only authority record for the one
// historical shape where an internally-created worktree retained both its
// immutable manifest and claimed outbox event but lost the private claim file.
// It is written before the reconstructed claim, so a crash cannot leave an
// unaudited claim that ordinary cleanup would later accept.
type legacyMissingClaimRecovery struct {
	Version       int       `json:"version"`
	Type          string    `json:"type"`
	RecoveredAt   time.Time `json:"recovered_at"`
	ClaimedAt     time.Time `json:"claimed_at"`
	Task          string    `json:"task"`
	EffortID      string    `json:"effort_id"`
	RunID         string    `json:"run_id"`
	ClaimID       string    `json:"claim_id"`
	Repository    string    `json:"repository"`
	Worktree      string    `json:"worktree"`
	Branch        string    `json:"branch"`
	Base          string    `json:"base"`
	BaseSHA       string    `json:"base_sha"`
	HeadSHA       string    `json:"head_sha"`
	AbsorbedBy    string    `json:"absorbed_by"`
	AbsorbedBySHA string    `json:"absorbed_by_sha"`
}

type legacyMissingClaimPlan struct {
	claim    workLogClaim
	recovery legacyMissingClaimRecovery
}

// preflightAbortWorkLog makes dry-run and apply inspect the same Work Log
// evidence. Generic missing/corrupt claims still fail closed. Recovery is
// available only for a clean source whose exact landing was independently
// proved by --absorbed-by.
func preflightAbortWorkLog(home string, options AbortOptions, entry ListResult) (bool, error) {
	projection, projectionErr := readWorkLogProjectionForReadOnlyClaim(entry.WorktreeDir)
	if errors.Is(projectionErr, errWorkLogProjectionNotFound) {
		return false, nil
	}
	if projectionErr != nil {
		return false, projectionErr
	}
	ordinaryErr := corroborateWorkLogProjection(home, entry.WorktreeDir, entry.HeadSHA, projection)
	if ordinaryErr == nil {
		return false, nil
	}
	if !errors.Is(ordinaryErr, os.ErrNotExist) || options.Disposition != AbortDiscarded ||
		strings.TrimSpace(options.AbsorbedBy) == "" || !entry.Clean || !entry.AbsorbedAtOrigin {
		return false, ordinaryErr
	}
	if _, err := planLegacyMissingClaimRecovery(home, options, entry); err != nil {
		return false, fmt.Errorf("private claim is missing and legacy recovery evidence did not corroborate it: %w", err)
	}
	return true, nil
}

func planLegacyMissingClaimRecovery(home string, options AbortOptions, entry ListResult) (legacyMissingClaimPlan, error) {
	manifest, err := ReadManifest(entry.WorktreeDir)
	if err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("read immutable worktree manifest: %w", err)
	}
	if manifest.Provenance != ProvenanceCreated {
		return legacyMissingClaimPlan{}, fmt.Errorf("worktree manifest provenance is %q, not %q", manifest.Provenance, ProvenanceCreated)
	}
	projection, err := readWorkLogProjectionForReadOnlyClaim(entry.WorktreeDir)
	if err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("read local Work Log projection: %w", err)
	}
	if projection.Lifecycle != "active" || manifest.EffortID != options.Task ||
		manifest.EffortID != projection.EffortID || manifest.RunID != projection.RunID || manifest.ClaimID != projection.ClaimID ||
		manifest.Repository != entry.Repository || filepath.Clean(manifest.Worktree) != filepath.Clean(entry.WorktreeDir) ||
		manifest.Branch != entry.Branch || manifest.Base != entry.Base {
		return legacyMissingClaimPlan{}, fmt.Errorf("manifest, projection, task, and live checkout identity do not match exactly")
	}
	wantClaimID := workLogClaimID(manifest.EffortID, CreateResult{
		Repository: manifest.Repository, WorktreeDir: manifest.Worktree, Branch: manifest.Branch,
		Base: manifest.Base, BaseSHA: manifest.BaseSHA,
	})
	if manifest.ClaimID == "" || manifest.ClaimID != wantClaimID {
		return legacyMissingClaimPlan{}, fmt.Errorf("manifest claim ID does not match its deterministic checkout identity")
	}
	runDir, _, err := openWorkLogRun(home, manifest.EffortID, manifest.RunID, false)
	if err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("open historical Work Log run: %w", err)
	}
	promptArchive, promptDigest, err := recoveredOriginalPromptEvidence(runDir)
	_ = runDir.Close()
	if err != nil {
		return legacyMissingClaimPlan{}, err
	}
	outbox, err := openWorkLogOutbox(home, manifest.EffortID, false)
	if err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("open historical Work Log outbox: %w", err)
	}
	defer func() { _ = outbox.Close() }()
	var claimed workLogPublicEvent
	if err := readJSONAt(outbox, manifest.RunID+"-"+manifest.ClaimID+"-claimed.json", &claimed); err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("read immutable claimed outbox event: %w", err)
	}
	wantEvent := workLogPublicEvent{
		Version: 1, Type: "worktree.claimed", At: manifest.CreatedAt,
		EffortID: manifest.EffortID, RunID: manifest.RunID, ClaimID: manifest.ClaimID,
		Repository: manifest.Repository, Branch: manifest.Branch, Base: manifest.Base,
		BaseSHA: manifest.BaseSHA, Lifecycle: "active",
	}
	if !reflect.DeepEqual(claimed, wantEvent) {
		return legacyMissingClaimPlan{}, fmt.Errorf("immutable claimed outbox event does not corroborate the manifest")
	}
	model := strings.TrimSpace(manifest.Model)
	if model == "" {
		model = "unknown"
	}
	identity := ClaimExecutionIdentity{Model: model, CLI: manifest.CLI, Provider: manifest.Provider}
	if err := validateNewExecutionIdentity(identity); err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("manifest execution identity is invalid: %w", err)
	}
	modelProvenance := modelProvenanceCallerDeclared
	if model == "unknown" {
		modelProvenance = modelProvenanceUnknown
	}
	claim := workLogClaim{
		Version: 2, EffortID: manifest.EffortID, RunID: manifest.RunID, ClaimID: manifest.ClaimID,
		Task: options.Task, Repository: manifest.Repository, Worktree: manifest.Worktree,
		Branch: manifest.Branch, Base: manifest.Base, BaseSHA: manifest.BaseSHA,
		Lifecycle: "active", RecordedAt: claimed.At, Initiator: strings.TrimSpace(manifest.Initiator),
		AgentID: strings.TrimSpace(manifest.AgentID), AgentRuntime: strings.TrimSpace(manifest.AgentRuntime),
		Model: model, ModelProvenance: modelProvenance, ModelDeclaredBy: declaredBy(WorkLogOptions{Initiator: manifest.Initiator, AgentID: manifest.AgentID}),
		CLI: strings.TrimSpace(manifest.CLI), Provider: strings.TrimSpace(manifest.Provider),
		PromptArchive: promptArchive, PromptDigest: promptDigest,
		AcquiredVia: legacyMissingClaimRecoveryType,
	}
	if err := validateStaticWorkLogClaim(claim, projection.EffortID, projection.RunID); err != nil {
		return legacyMissingClaimPlan{}, fmt.Errorf("reconstructed claim is invalid: %w", err)
	}
	return legacyMissingClaimPlan{claim: claim, recovery: legacyMissingClaimRecovery{
		Version: 1, Type: legacyMissingClaimRecoveryType, ClaimedAt: claimed.At,
		Task: options.Task, EffortID: manifest.EffortID, RunID: manifest.RunID, ClaimID: manifest.ClaimID,
		Repository: entry.Repository, Worktree: entry.WorktreeDir, Branch: entry.Branch,
		Base: entry.Base, BaseSHA: manifest.BaseSHA, HeadSHA: entry.HeadSHA,
		AbsorbedBy: options.AbsorbedBy, AbsorbedBySHA: entry.AbsorbedBySHA,
	}}, nil
}

func recoveredOriginalPromptEvidence(runDir *os.File) (string, string, error) {
	contents, archiveErr := readBytesAt(runDir, "original-prompt.txt")
	var metadata workLogPromptMetadata
	metadataErr := readJSONAt(runDir, "original-prompt.json", &metadata)
	if errors.Is(archiveErr, os.ErrNotExist) && errors.Is(metadataErr, os.ErrNotExist) {
		return "", "", nil
	}
	if archiveErr != nil {
		return "", "", fmt.Errorf("read immutable original prompt archive: %w", archiveErr)
	}
	if metadataErr != nil {
		return "", "", fmt.Errorf("read immutable original prompt metadata: %w", metadataErr)
	}
	digest := sha256.Sum256(contents)
	wantDigest := hex.EncodeToString(digest[:])
	if metadata.Version != 1 || metadata.SHA256 != wantDigest {
		return "", "", fmt.Errorf("immutable original prompt metadata does not corroborate the archived bytes")
	}
	return "original-prompt.txt", wantDigest, nil
}

func recoverLegacyMissingClaimForAbort(home string, options AbortOptions, entry ListResult) error {
	plan, err := planLegacyMissingClaimRecovery(home, options, entry)
	if err != nil {
		return err
	}
	runDir, _, err := openWorkLogRun(home, plan.claim.EffortID, plan.claim.RunID, false)
	if err != nil {
		return err
	}
	defer func() { _ = runDir.Close() }()
	unlock, err := lockClaim(runDir, plan.claim.ClaimID)
	if err != nil {
		return err
	}
	defer unlock()

	claims, err := openPrivateChild(runDir, "claims", true)
	if err != nil {
		return err
	}
	defer func() { _ = claims.Close() }()
	var existingClaim workLogClaim
	claimExists := false
	if err := readJSONAt(claims, plan.claim.ClaimID+".json", &existingClaim); err == nil {
		if !reflect.DeepEqual(existingClaim, plan.claim) {
			return fmt.Errorf("private claim appeared with different immutable bytes")
		}
		claimExists = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect missing private claim: %w", err)
	}

	recoveries, err := openPrivateChild(runDir, "recoveries", !claimExists)
	if err != nil {
		if claimExists && errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("recovered private claim exists without its mandatory recovery receipt")
		}
		return fmt.Errorf("open missing-claim recovery receipts: %w", err)
	}
	defer func() { _ = recoveries.Close() }()
	recoveryName := plan.claim.ClaimID + "-missing-claim.json"
	var existing legacyMissingClaimRecovery
	if err := readJSONAt(recoveries, recoveryName, &existing); err == nil {
		planned := plan.recovery
		planned.RecoveredAt = existing.RecoveredAt
		if existing.RecoveredAt.IsZero() || !reflect.DeepEqual(existing, planned) {
			return fmt.Errorf("existing missing-claim recovery receipt does not match current exact evidence")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect missing-claim recovery receipt: %w", err)
	} else {
		if claimExists {
			return fmt.Errorf("recovered private claim exists without its mandatory recovery receipt")
		}
		plan.recovery.RecoveredAt = time.Now().UTC()
		if err := writeJSONImmutableAt(recoveries, recoveryName, plan.recovery, false); err != nil {
			return fmt.Errorf("write immutable missing-claim recovery receipt: %w", err)
		}
	}
	if claimExists {
		return preflightWorkLogSeal(home, entry.WorktreeDir, entry.HeadSHA)
	}
	if err := writeJSONImmutableAt(claims, plan.claim.ClaimID+".json", plan.claim, false); err != nil {
		return fmt.Errorf("publish recovered immutable claim: %w", err)
	}
	return preflightWorkLogSeal(home, entry.WorktreeDir, entry.HeadSHA)
}
