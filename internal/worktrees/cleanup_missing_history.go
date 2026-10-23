package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeclaims"
	"gopkg.in/yaml.v3"
)

// cleanupMissingHistoryReceipt attests retirement, never an original claim or
// its terminal. The original private history is explicitly unavailable. The
// immutable receipt survives the checkout and ordinary cleanup's phase backlog
// continues to own interrupted destructive operations.
type cleanupMissingHistoryReceipt struct {
	Version         int                           `json:"version"`
	Type            string                        `json:"type"`
	RecordedAt      time.Time                     `json:"recorded_at"`
	MissingHistory  string                        `json:"missing_history"`
	Projection      workLogProjection             `json:"projection"`
	ManifestSHA256  string                        `json:"manifest_sha256"`
	Task            string                        `json:"task"`
	Repository      string                        `json:"repository"`
	Worktree        string                        `json:"worktree"`
	Branch          string                        `json:"branch"`
	BaseSHA         string                        `json:"base_sha"`
	HeadSHA         string                        `json:"head_sha"`
	RemoteTargetSHA string                        `json:"remote_target_sha"`
	Landed          worktreeclaims.LandedEvidence `json:"landed"`
}

// cleanupHistoryAbsent recognizes absence only at the historical run/claim
// boundary. Malformed files, links and permissions never authorize recovery.
func cleanupHistoryAbsent(home string, projection workLogProjection) (bool, error) {
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return errors.Is(err, os.ErrNotExist), errUnlessAbsent(err)
	}
	defer func() { _ = run.Close() }()
	if _, err := readWorkLogClaimAt(run, projection.ClaimID); err == nil {
		return false, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	if _, err := readWorkLogTerminalAt(run, projection.ClaimID); err == nil {
		return false, fmt.Errorf("historical terminal exists without its immutable claim")
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	return true, nil
}

func errUnlessAbsent(err error) error {
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}

func planCleanupMissingHistory(ctx context.Context, home string, entry ListResult) (cleanupMissingHistoryReceipt, bool, error) {
	var receipt cleanupMissingHistoryReceipt
	projection, err := readWorkLogProjectionForReadOnlyClaim(entry.WorktreeDir)
	if errors.Is(err, errWorkLogProjectionNotFound) {
		return receipt, false, nil
	}
	if err != nil {
		return receipt, false, err
	}
	absent, err := cleanupHistoryAbsent(home, projection)
	if err != nil || !absent {
		return receipt, false, err
	}
	if projection.Lifecycle != "active" || !entry.Local || entry.External || entry.Detached || !entry.Clean ||
		entry.OpenPullRequest != nil || !entry.IntegratedAtOrigin || !isGitObjectID(entry.HeadSHA) || !isGitObjectID(entry.RemoteTargetSHA) {
		return receipt, false, fmt.Errorf("missing historical claim requires an active, clean repository-local checkout with no open PR and exact contained landing")
	}
	journal, err := openJournalDirectory(entry.WorktreeDir, false)
	if err != nil {
		return receipt, false, err
	}
	content, err := readBytesAt(journal, manifestName)
	_ = journal.Close()
	if err != nil {
		return receipt, false, err
	}
	var manifest Manifest
	if err := yaml.Unmarshal(content, &manifest); err != nil {
		return receipt, false, err
	}
	if err := worktreeclaims.ValidateManifest(manifest); err != nil {
		return receipt, false, err
	}
	if manifest.Provenance != ProvenanceCreated || manifest.EffortID != entry.Task || manifest.EffortID != projection.EffortID ||
		manifest.RunID != projection.RunID || manifest.ClaimID != projection.ClaimID || manifest.Repository != entry.Repository ||
		manifest.Worktree != filepath.Clean(entry.WorktreeDir) || manifest.Branch != entry.Branch || manifest.Base != entry.Base ||
		!isGitObjectID(manifest.BaseSHA) || manifest.CreatedAt.IsZero() {
		return receipt, false, fmt.Errorf("immutable manifest, projection and cleanup identity do not match")
	}
	if manifest.ClaimID != workLogClaimID(manifest.EffortID, CreateResult{Repository: manifest.Repository, WorktreeDir: manifest.Worktree,
		Branch: manifest.Branch, Base: manifest.Base, BaseSHA: manifest.BaseSHA}) {
		return receipt, false, fmt.Errorf("historical claim ID does not match immutable manifest identity")
	}
	head, headErr := git(ctx, entry.WorktreeDir, "rev-parse", "HEAD")
	branch, branchErr := git(ctx, entry.WorktreeDir, "symbolic-ref", "--short", "HEAD")
	status, statusErr := git(ctx, entry.WorktreeDir, "status", "--porcelain")
	if headErr != nil || branchErr != nil || statusErr != nil || head != entry.HeadSHA || branch != entry.Branch || status != "" {
		return receipt, false, fmt.Errorf("missing-history checkout changed or cannot be verified")
	}
	for _, pair := range [][2]string{{manifest.BaseSHA, entry.HeadSHA}, {entry.HeadSHA, entry.RemoteTargetSHA}} {
		contained, err := isAncestor(ctx, entry.CanonicalDir, pair[0], pair[1])
		if err != nil || !contained {
			return receipt, false, fmt.Errorf("missing-history exact base/head/target ancestry cannot be verified")
		}
	}
	digest := sha256.Sum256(content)
	receipt = cleanupMissingHistoryReceipt{Version: 1, Type: "legacy_missing_history_retirement", MissingHistory: "original private claim unavailable; original history not reconstructed",
		Projection: projection, ManifestSHA256: hex.EncodeToString(digest[:]), Task: entry.Task, Repository: entry.Repository,
		Worktree: entry.WorktreeDir, Branch: entry.Branch, BaseSHA: manifest.BaseSHA, HeadSHA: entry.HeadSHA, RemoteTargetSHA: entry.RemoteTargetSHA,
		Landed: worktreeclaims.LandedEvidence{Target: entry.Base, LandedSHA: entry.HeadSHA, Proof: worktreeclaims.LandedProofContained}}
	return receipt, true, nil
}

// retireCleanupMissingHistory returns the held claim fence to its caller, which
// retains it through destruction together with the task and repository locks.
// Creating an absent run on apply provides only the lock namespace; no claim,
// terminal, prompt archive or historical journal is synthesized.
func retireCleanupMissingHistory(ctx context.Context, home string, entry ListResult) (bool, func(), error) {
	return retireCleanupMissingHistoryObserved(ctx, home, entry, nil)
}

func retireCleanupMissingHistoryObserved(ctx context.Context, home string, entry ListResult, afterLock func()) (bool, func(), error) {
	receipt, missing, err := planCleanupMissingHistory(ctx, home, entry)
	if err != nil || !missing {
		return false, func() {}, err
	}
	locked, err := openLockedWorkLogRun(home, receipt.Projection.EffortID, receipt.Projection.RunID, receipt.Projection.ClaimID, true)
	if err != nil {
		return false, func() {}, err
	}
	success := false
	defer func() {
		if !success {
			locked.close()
		}
	}()
	if afterLock != nil {
		afterLock()
	}
	rechecked, missing, err := planCleanupMissingHistory(ctx, home, entry)
	if err != nil {
		return false, func() {}, err
	}
	if !missing || !reflect.DeepEqual(receipt, rechecked) {
		return false, func() {}, fmt.Errorf("missing historical claim evidence changed while acquiring claim fence")
	}
	backlog, err := openLifecycleBacklogDirectory(home, true)
	if err != nil {
		return false, func() {}, err
	}
	defer func() { _ = backlog.Close() }()
	directory, err := openPrivateChild(backlog, "missing-history", true)
	if err != nil {
		return false, func() {}, err
	}
	defer func() { _ = directory.Close() }()
	name := lifecycleBacklogID(entry, "removed") + ".json"
	var existing cleanupMissingHistoryReceipt
	if err := readJSONAt(directory, name, &existing); err == nil {
		// Fresh containment is always required, but a retry may observe a target
		// that has advanced. Preserve the original exact landing snapshot.
		receipt.RecordedAt, receipt.RemoteTargetSHA = existing.RecordedAt, existing.RemoteTargetSHA
		contained, ancestryErr := isAncestor(ctx, entry.CanonicalDir, existing.RemoteTargetSHA, entry.RemoteTargetSHA)
		if existing.RecordedAt.IsZero() || !isGitObjectID(existing.RemoteTargetSHA) || !reflect.DeepEqual(existing, receipt) || ancestryErr != nil || !contained {
			return false, func() {}, fmt.Errorf("immutable missing-history retirement receipt conflicts with current evidence")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, func() {}, err
	} else {
		receipt.RecordedAt = time.Now().UTC()
		if err := writeJSONImmutableAt(directory, name, receipt, false); err != nil {
			return false, func() {}, err
		}
	}
	success = true
	return true, locked.close, nil
}
