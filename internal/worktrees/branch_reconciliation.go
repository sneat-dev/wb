package worktrees

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/wbhome"
	"golang.org/x/sys/unix"
)

const (
	branchReconciliationRecordName = "record.json"
	reconciliationStagePlanned     = "planned"
	reconciliationStageBundles     = "bundles_preserved"
	reconciliationStageRemote      = "remote_retired"
	reconciliationStageLocal       = "local_retired"
	reconciliationStageRebound     = "branch_rebound"
	reconciliationStageEvent       = "event_appended"
	reconciliationStageComplete    = "complete"
)

// branchReconciliationRecord is private, durable recovery evidence. It never
// rewrites the immutable claim: every apply re-reads and re-corroborates it.
type branchReconciliationRecord struct {
	Version      int       `json:"version"`
	EventID      string    `json:"event_id"`
	ClaimID      string    `json:"claim_id"`
	Worktree     string    `json:"worktree"`
	Repository   string    `json:"repository"`
	ClaimBranch  string    `json:"claim_branch"`
	LiveBranch   string    `json:"live_branch"`
	ExpectedHead string    `json:"expected_head"`
	LocalHead    string    `json:"local_claim_head"`
	RemoteHead   string    `json:"remote_claim_head"`
	TargetHead   string    `json:"target_head"`
	Actor        string    `json:"actor"`
	Reason       string    `json:"reason"`
	Stage        string    `json:"stage"`
	CreatedAt    time.Time `json:"created_at"`
}

type branchReconciliationEvidence struct {
	Version int    `json:"version"`
	Head    string `json:"head"`
	Ref     string `json:"ref"`
	Bundle  string `json:"bundle"`
	SHA256  string `json:"sha256"`
}

func reconcileClaimBranch(ctx context.Context, options LogRecoverOptions) (LogVerbResult, error) {
	if err := validateBranchReconciliationOptions(options); err != nil {
		return LogVerbResult{}, err
	}
	root, err := resolveWorktreeRoot(ctx, options.Worktree)
	if err != nil {
		return LogVerbResult{}, err
	}
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return LogVerbResult{}, err
	}
	projection, claim, err := reconciliationClaim(home, root)
	if err != nil {
		return LogVerbResult{}, err
	}
	if claim.Branch == options.ReconcileBranch {
		return LogVerbResult{}, fmt.Errorf("--reconcile-branch must name a live branch different from immutable claim %q", claim.Branch)
	}
	if options.Apply {
		unlock, lockErr := lockBranchReconciliationClaim(home, claim)
		if lockErr != nil {
			return LogVerbResult{}, lockErr
		}
		defer unlock()
	}
	entry, err := reconciliationLifecycleEvidence(ctx, options.ProjectsRoot, root, claim)
	if err != nil {
		return LogVerbResult{}, err
	}
	if err := validateReconciliationLifecycleEvidence(ctx, entry, claim, options); err != nil {
		return LogVerbResult{}, err
	}

	record, recordDir, recordErr := readBranchReconciliationRecord(home, claim, options.EventID)
	if recordErr != nil && !errors.Is(recordErr, os.ErrNotExist) {
		return LogVerbResult{}, recordErr
	}
	if errors.Is(recordErr, os.ErrNotExist) {
		if entry.Branch != options.ReconcileBranch {
			return LogVerbResult{}, fmt.Errorf("live branch %q does not match --reconcile-branch %q", entry.Branch, options.ReconcileBranch)
		}
		canonical, openErr := openCanonicalRepository(entry.CanonicalDir)
		if openErr != nil {
			return LogVerbResult{}, openErr
		}
		defer canonical.close()
		if err := canonical.validate(); err != nil {
			return LogVerbResult{}, err
		}
		localHead, localErr := gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+claim.Branch)
		if localErr != nil || !isGitObjectID(localHead) {
			return LogVerbResult{}, fmt.Errorf("resolve exact local immutable-claim branch %q: %w", claim.Branch, localErr)
		}
		remoteHead, remoteErr := remoteBranchHead(ctx, entry.CanonicalDir, claim.Branch)
		if remoteErr != nil || !isGitObjectID(remoteHead) {
			return LogVerbResult{}, fmt.Errorf("resolve exact remote immutable-claim branch %q: %w", claim.Branch, remoteErr)
		}
		record = branchReconciliationRecord{
			Version: 1, EventID: options.EventID, ClaimID: claim.ClaimID, Worktree: root,
			Repository: entry.Repository, ClaimBranch: claim.Branch, LiveBranch: options.ReconcileBranch,
			ExpectedHead: options.ExpectedHead, LocalHead: localHead, RemoteHead: remoteHead,
			TargetHead: entry.RemoteTargetSHA, Actor: options.Actor, Reason: options.Reason,
			Stage: reconciliationStagePlanned, CreatedAt: time.Now().UTC(),
		}
		if !options.Apply {
			return LogVerbResult{Worktree: root, Verb: "recover", Projection: localProjectionForReconciliation(projection),
				Diagnosis: []string{"branch reconciliation plan is read-only", "local and remote claim heads will be preserved before retirement"},
				Notes:     []string{"pass --apply to reconcile the live branch to the immutable Work Log claim"}}, nil
		}
		recordDir, err = createBranchReconciliationRecord(home, claim, record)
		if err != nil {
			return LogVerbResult{}, err
		}
		defer func() { _ = recordDir.Close() }()
	} else {
		defer func() { _ = recordDir.Close() }()
		if err := corroborateReconciliationRecord(record, claim, root, options); err != nil {
			return LogVerbResult{}, err
		}
		if !options.Apply {
			return LogVerbResult{Worktree: root, Verb: "recover", Projection: localProjectionForReconciliation(projection),
				Diagnosis: []string{"branch reconciliation recovery stage " + record.Stage},
				Notes:     []string{"dry-run only; recorded reconciliation is resumable with --apply"}}, nil
		}
	}

	if entry.Branch != record.LiveBranch && entry.Branch != record.ClaimBranch {
		return LogVerbResult{}, fmt.Errorf("live branch %q is neither recorded recovery branch %q nor immutable claim %q", entry.Branch, record.LiveBranch, record.ClaimBranch)
	}
	canonical, err := openCanonicalRepository(entry.CanonicalDir)
	if err != nil {
		return LogVerbResult{}, err
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return LogVerbResult{}, err
	}

	if record.Stage == reconciliationStagePlanned {
		if err := preserveReconciliationBundles(ctx, canonical, recordDir, record, options); err != nil {
			return LogVerbResult{}, err
		}
		record.Stage = reconciliationStageBundles
		if err := writeBranchReconciliationRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
	}
	if record.Stage == reconciliationStageBundles {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record); err != nil {
			return LogVerbResult{}, err
		}
		if err := requireRemoteClaimHead(ctx, entry.CanonicalDir, record.ClaimBranch, record.RemoteHead); err != nil {
			return LogVerbResult{}, err
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", "--force-with-lease=refs/heads/"+record.ClaimBranch+":"+record.RemoteHead, "origin", ":refs/heads/"+record.ClaimBranch); err != nil {
			return LogVerbResult{}, fmt.Errorf("retire exact remote immutable-claim branch: %w", err)
		}
		record.Stage = reconciliationStageRemote
		if err := writeBranchReconciliationRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "remote" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after remote retirement")
		}
	}
	if record.Stage == reconciliationStageRemote {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record); err != nil {
			return LogVerbResult{}, err
		}
		if err := requireRemoteClaimAbsent(ctx, entry.CanonicalDir, record.ClaimBranch); err != nil {
			return LogVerbResult{}, err
		}
		if err := requireLocalClaimHead(ctx, canonical, record.ClaimBranch, record.LocalHead); err != nil {
			return LogVerbResult{}, err
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+record.ClaimBranch, record.LocalHead); err != nil {
			return LogVerbResult{}, fmt.Errorf("retire exact local immutable-claim branch: %w", err)
		}
		record.Stage = reconciliationStageLocal
		if err := writeBranchReconciliationRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "local" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after local retirement")
		}
	}
	if record.Stage == reconciliationStageLocal {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record); err != nil {
			return LogVerbResult{}, err
		}
		if err := requireLocalClaimAbsent(ctx, canonical, record.ClaimBranch); err != nil {
			return LogVerbResult{}, err
		}
		if err := requireLocalClaimHead(ctx, canonical, record.LiveBranch, record.ExpectedHead); err != nil {
			return LogVerbResult{}, err
		}
		if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "branch", "-m", record.LiveBranch, record.ClaimBranch); err != nil {
			return LogVerbResult{}, fmt.Errorf("rebind live branch to immutable claim branch: %w", err)
		}
		record.Stage = reconciliationStageRebound
		if err := writeBranchReconciliationRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "rebound" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after branch rebind")
		}
	}
	if record.Stage == reconciliationStageRebound {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record); err != nil {
			return LogVerbResult{}, err
		}
		if err := corroborateProjectionWithPrivateClaim(home, root, projection); err != nil {
			return LogVerbResult{}, fmt.Errorf("re-corroborate immutable Work Log claim after branch rebind: %w", err)
		}
		event, updated, appendErr := appendLocalEvent(root, LocalWorkLogEvent{ID: record.EventID, Type: LocalEventBranchReconciled,
			Message: record.Reason, Git: ptrLocalGit(observeLocalGit(ctx, root)), Extra: map[string]any{"actor": record.Actor, "live_branch": record.LiveBranch, "claim_branch": record.ClaimBranch, "local_head": record.LocalHead, "remote_head": record.RemoteHead}})
		if appendErr != nil {
			return LogVerbResult{}, appendErr
		}
		record.Stage = reconciliationStageEvent
		if err := writeBranchReconciliationRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "event" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after branch-reconciled event")
		}
		return finishBranchReconciliation(recordDir, record, root, event, updated)
	}
	if record.Stage == reconciliationStageEvent {
		event, updated, appendErr := appendLocalEvent(root, LocalWorkLogEvent{ID: record.EventID, Type: LocalEventBranchReconciled, Message: record.Reason})
		if appendErr != nil {
			return LogVerbResult{}, appendErr
		}
		return finishBranchReconciliation(recordDir, record, root, event, updated)
	}
	if record.Stage == reconciliationStageComplete {
		event, updated, appendErr := appendLocalEvent(root, LocalWorkLogEvent{ID: record.EventID, Type: LocalEventBranchReconciled, Message: record.Reason})
		if appendErr != nil {
			return LogVerbResult{}, appendErr
		}
		return LogVerbResult{Worktree: root, Verb: "recover", Event: &event, Projection: &updated, Applied: true, ReadyForNormalCleanup: true,
			Notes: []string{"immutable Work Log claim re-corroborated; ready for normal cleanup"}}, nil
	}
	return LogVerbResult{}, fmt.Errorf("unknown branch reconciliation stage %q", record.Stage)
}

func finishBranchReconciliation(directory *os.File, record branchReconciliationRecord, root string, event LocalWorkLogEvent, projection LocalWorkLogProjection) (LogVerbResult, error) {
	record.Stage = reconciliationStageComplete
	if err := writeBranchReconciliationRecord(directory, record); err != nil {
		return LogVerbResult{}, err
	}
	return LogVerbResult{Worktree: root, Verb: "recover", Event: &event, Projection: &projection, Applied: true, ReadyForNormalCleanup: true,
		Notes: []string{"immutable Work Log claim re-corroborated; ready for normal cleanup"}}, nil
}

func validateBranchReconciliationOptions(options LogRecoverOptions) error {
	if strings.TrimSpace(options.Worktree) == "" || strings.TrimSpace(options.ReconcileBranch) == "" || strings.TrimSpace(options.ExpectedHead) == "" || strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "" || strings.TrimSpace(options.EventID) == "" {
		return fmt.Errorf("--reconcile-branch requires worktree, --expected-head, --remote, --actor, --reason, and --event-id")
	}
	if !options.Remote {
		return fmt.Errorf("--remote is required with --reconcile-branch")
	}
	if options.Takeover {
		return fmt.Errorf("--takeover cannot be combined with --reconcile-branch")
	}
	if !isGitObjectID(options.ExpectedHead) || !validSafeSegment(options.EventID) || !validBranch(context.Background(), options.ReconcileBranch) {
		return fmt.Errorf("invalid branch reconciliation input")
	}
	return nil
}

func lockBranchReconciliationClaim(home string, claim workLogClaim) (func(), error) {
	run, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return nil, err
	}
	unlock, err := lockClaim(run, claim.ClaimID)
	if err != nil {
		_ = run.Close()
		return nil, err
	}
	return func() { unlock(); _ = run.Close() }, nil
}

func reconciliationClaim(home, worktree string) (workLogProjection, workLogClaim, error) {
	projection, err := readWorkLogProjectionForReadOnlyClaim(worktree)
	if err != nil {
		return workLogProjection{}, workLogClaim{}, err
	}
	run, _, err := openWorkLogRun(home, projection.EffortID, projection.RunID, false)
	if err != nil {
		return workLogProjection{}, workLogClaim{}, err
	}
	defer func() { _ = run.Close() }()
	claims, err := openPrivateChild(run, "claims", false)
	if err != nil {
		return workLogProjection{}, workLogClaim{}, err
	}
	defer func() { _ = claims.Close() }()
	var claim workLogClaim
	if err := readJSONAt(claims, projection.ClaimID+".json", &claim); err != nil {
		return workLogProjection{}, workLogClaim{}, err
	}
	if err := corroborateReconciliationClaimShape(worktree, projection, claim); err != nil {
		return workLogProjection{}, workLogClaim{}, err
	}
	return projection, claim, nil
}

func corroborateReconciliationClaimShape(worktree string, projection workLogProjection, claim workLogClaim) error {
	if (claim.Version != 1 && claim.Version != 2) || claim.EffortID != projection.EffortID || claim.RunID != projection.RunID || claim.ClaimID != projection.ClaimID || claim.Lifecycle != "active" || filepath.Clean(claim.Worktree) != filepath.Clean(worktree) {
		return fmt.Errorf("work-log projection does not match immutable active claim")
	}
	if !validSafeSegment(claim.EffortID) || !validSafeSegment(claim.RunID) || !validSafeSegment(claim.Task) || !validClaimID(claim.ClaimID) || !isGitObjectID(claim.BaseSHA) {
		return fmt.Errorf("private work-log claim identity is invalid")
	}
	want := workLogClaimID(claim.EffortID, CreateResult{Repository: claim.Repository, WorktreeDir: claim.Worktree, Branch: claim.Branch, Base: claim.Base, BaseSHA: claim.BaseSHA})
	if claim.ParentClaimID != "" {
		if !validClaimID(claim.ParentClaimID) || claim.AgentID == "" || (claim.AcquiredVia != "handoff" && claim.AcquiredVia != "not_landed" && claim.AcquiredVia != "recycle_failed") {
			return fmt.Errorf("private successor claim metadata is invalid")
		}
		if claim.Version == 2 && claim.AcquiredVia != "recycle_failed" {
			want = declaredSuccessorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia, ClaimExecutionIdentity{Model: claim.Model, CLI: claim.CLI, Provider: claim.Provider})
		} else {
			want = successorWorkLogClaimID(claim.ParentClaimID, claim.AgentID, claim.AcquiredVia)
		}
	}
	if want != claim.ClaimID {
		return fmt.Errorf("private work-log claim digest mismatch")
	}
	return nil
}

func reconciliationLifecycleEvidence(ctx context.Context, projectsRoot, root string, claim workLogClaim) (ListResult, error) {
	listed, err := ListWithDiagnostics(ctx, ListOptions{ProjectsRoot: projectsRoot, Task: claim.Task, Base: claim.Base, GitHub: true, Workers: 1})
	if err != nil {
		return ListResult{}, err
	}
	for _, entry := range listed.Results {
		if filepath.Clean(entry.WorktreeDir) == filepath.Clean(root) {
			return entry, nil
		}
	}
	if len(listed.Diagnostics) > 0 {
		return ListResult{}, fmt.Errorf("verify live worktree and owner metadata: %s", listed.Diagnostics[0].Message)
	}
	return ListResult{}, fmt.Errorf("live worktree %s is not a readable managed member of task %q", root, claim.Task)
}

func validateReconciliationLifecycleEvidence(ctx context.Context, entry ListResult, claim workLogClaim, options LogRecoverOptions) error {
	if entry.HeadSHA != options.ExpectedHead {
		return fmt.Errorf("live HEAD %q does not match --expected-head %q", entry.HeadSHA, options.ExpectedHead)
	}
	if !entry.Clean {
		return fmt.Errorf("cannot reconcile a dirty worktree")
	}
	if entry.OpenPullRequest != nil {
		return fmt.Errorf("cannot reconcile while the live branch has an open pull request: %s", entry.OpenPullRequest.URL)
	}
	if entry.MergedPullRequest == nil {
		return fmt.Errorf("cannot reconcile without merged pull-request evidence for %s", entry.HeadSHA)
	}
	if !entry.IntegratedAtOrigin || entry.RemoteTargetSHA == "" {
		return fmt.Errorf("live branch %q is not integrated into the exact fetched origin/%s target", entry.Branch, claim.Base)
	}
	if entry.RemoteHeadSHA != "" && entry.RemoteHeadSHA != entry.HeadSHA {
		return fmt.Errorf("live branch remote head advanced from expected %s to %s", entry.HeadSHA, entry.RemoteHeadSHA)
	}
	merged, err := isAncestor(ctx, entry.CanonicalDir, claim.BaseSHA, entry.HeadSHA)
	if err != nil {
		return fmt.Errorf("verify claimed base against live head: %w", err)
	}
	if !merged {
		return fmt.Errorf("live HEAD is not descended from immutable claim base %s", claim.BaseSHA)
	}
	return nil
}

func revalidateReconciliationStage(ctx context.Context, options LogRecoverOptions, root string, claim workLogClaim, record branchReconciliationRecord) error {
	entry, err := reconciliationLifecycleEvidence(ctx, options.ProjectsRoot, root, claim)
	if err != nil {
		return err
	}
	if err := validateReconciliationLifecycleEvidence(ctx, entry, claim, options); err != nil {
		return err
	}
	if entry.Branch != record.LiveBranch && entry.Branch != record.ClaimBranch {
		return fmt.Errorf("live branch %q changed outside the recorded reconciliation", entry.Branch)
	}
	return nil
}

func createBranchReconciliationRecord(home string, claim workLogClaim, record branchReconciliationRecord) (*os.File, error) {
	run, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = run.Close() }()
	reconciliations, err := openPrivateChild(run, "branch-reconciliations", true)
	if err != nil {
		return nil, err
	}
	defer func() { _ = reconciliations.Close() }()
	directory, err := openPrivateChild(reconciliations, record.EventID, true)
	if err != nil {
		return nil, err
	}
	if err := writeBranchReconciliationRecord(directory, record); err != nil {
		_ = directory.Close()
		return nil, err
	}
	return directory, nil
}

func readBranchReconciliationRecord(home string, claim workLogClaim, eventID string) (branchReconciliationRecord, *os.File, error) {
	run, _, err := openWorkLogRun(home, claim.EffortID, claim.RunID, false)
	if err != nil {
		return branchReconciliationRecord{}, nil, err
	}
	defer func() { _ = run.Close() }()
	reconciliations, err := openPrivateChild(run, "branch-reconciliations", false)
	if err != nil {
		return branchReconciliationRecord{}, nil, err
	}
	defer func() { _ = reconciliations.Close() }()
	directory, err := openPrivateChild(reconciliations, eventID, false)
	if err != nil {
		return branchReconciliationRecord{}, nil, err
	}
	var record branchReconciliationRecord
	if err := readJSONAt(directory, branchReconciliationRecordName, &record); err != nil {
		_ = directory.Close()
		return branchReconciliationRecord{}, nil, err
	}
	return record, directory, nil
}

func writeBranchReconciliationRecord(directory *os.File, record branchReconciliationRecord) error {
	return writeJSONAtomicAt(directory, branchReconciliationRecordName, record, 0o600)
}

func corroborateReconciliationRecord(record branchReconciliationRecord, claim workLogClaim, root string, options LogRecoverOptions) error {
	if record.Version != 1 || record.EventID != options.EventID || record.ClaimID != claim.ClaimID || filepath.Clean(record.Worktree) != filepath.Clean(root) ||
		record.ClaimBranch != claim.Branch || record.LiveBranch != options.ReconcileBranch || record.ExpectedHead != options.ExpectedHead || record.Actor != options.Actor || record.Reason != options.Reason ||
		!isGitObjectID(record.LocalHead) || !isGitObjectID(record.RemoteHead) || !isGitObjectID(record.TargetHead) {
		return fmt.Errorf("branch reconciliation record does not match immutable claim and requested recovery")
	}
	return nil
}

func preserveReconciliationBundles(ctx context.Context, canonical *canonicalRepository, directory *os.File, record branchReconciliationRecord, options LogRecoverOptions) error {
	if options.testBeforeBundleCheck != nil {
		options.testBeforeBundleCheck()
	}
	if err := requireLocalClaimHead(ctx, canonical, record.ClaimBranch, record.LocalHead); err != nil {
		return err
	}
	if err := requireRemoteClaimHead(ctx, canonical.path, record.ClaimBranch, record.RemoteHead); err != nil {
		return err
	}
	if err := bundleClaimHead(ctx, canonical, directory, record.EventID, "local", "refs/heads/"+record.ClaimBranch, record.LocalHead); err != nil {
		return err
	}
	if options.testFailAfterBundle == "local" {
		return fmt.Errorf("injected bundle failure after local preservation")
	}
	remoteRef := "refs/remotes/origin/" + record.ClaimBranch
	if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "fetch", "--no-tags", "origin", "+refs/heads/"+record.ClaimBranch+":"+remoteRef); err != nil {
		return fmt.Errorf("fetch remote immutable-claim head for bundle preservation: %w", err)
	}
	fetched, err := gitCanonical(ctx, canonical, "rev-parse", remoteRef)
	if err != nil || fetched != record.RemoteHead {
		return fmt.Errorf("fetched remote immutable-claim head changed from %s to %s: %w", record.RemoteHead, fetched, err)
	}
	if err := bundleClaimHead(ctx, canonical, directory, record.EventID, "remote", remoteRef, record.RemoteHead); err != nil {
		return err
	}
	if options.testFailAfterBundle == "remote" {
		return fmt.Errorf("injected bundle failure after remote preservation")
	}
	return nil
}

func bundleClaimHead(ctx context.Context, canonical *canonicalRepository, directory *os.File, eventID, kind, reference, head string) error {
	name := "wb-reconcile-" + eventID + "-" + kind + ".bundle"
	path := filepath.Join(canonical.path, ".git", name)
	if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "bundle", "create", path, reference); err != nil {
		return fmt.Errorf("create %s recovery bundle for %s at %s: %w", kind, reference, head, err)
	}
	if err := runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "bundle", "verify", path); err != nil {
		return fmt.Errorf("verify %s recovery bundle for %s at %s: %w", kind, reference, head, err)
	}
	if err := requireBundleAdvertisesClaimRef(ctx, canonical, path, reference, head); err != nil {
		return fmt.Errorf("verify %s recovery bundle advertisement: %w", kind, err)
	}
	content, err := readBytesAt(canonical.common, name)
	if err != nil {
		return fmt.Errorf("read verified %s recovery bundle: %w", kind, err)
	}
	defer func() { _ = unix.Unlinkat(int(canonical.common.Fd()), name, 0) }()
	digest := sha256.Sum256(content)
	bundleName := kind + ".bundle"
	if err := writeBytesImmutableAt(directory, bundleName, content, 0o600, true); err != nil {
		return fmt.Errorf("preserve %s recovery bundle: %w", kind, err)
	}
	return writeJSONImmutableAt(directory, kind+".json", branchReconciliationEvidence{Version: 1, Head: head, Ref: reference, Bundle: bundleName, SHA256: hex.EncodeToString(digest[:])}, true)
}

func requireBundleAdvertisesClaimRef(ctx context.Context, canonical *canonicalRepository, path, reference, head string) error {
	output, err := gitCanonical(ctx, canonical, "bundle", "list-heads", path)
	if err != nil {
		return err
	}
	for _, line := range strings.Split(strings.TrimSpace(output), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == reference {
			if fields[0] != head {
				return fmt.Errorf("bundle advertises %s at %s, want %s", reference, fields[0], head)
			}
			return nil
		}
	}
	return fmt.Errorf("bundle does not advertise expected ref %s at %s", reference, head)
}

func requireRemoteClaimHead(ctx context.Context, repository, branch, want string) error {
	got, err := remoteBranchHead(ctx, repository, branch)
	if err != nil {
		return err
	}
	if got != want {
		return fmt.Errorf("remote immutable-claim branch %q moved from expected %s to %s", branch, want, got)
	}
	return nil
}

func requireRemoteClaimAbsent(ctx context.Context, repository, branch string) error {
	got, err := remoteBranchHead(ctx, repository, branch)
	if err != nil {
		return err
	}
	if got != "" {
		return fmt.Errorf("remote immutable-claim branch %q reappeared at %s", branch, got)
	}
	return nil
}

func requireLocalClaimHead(ctx context.Context, canonical *canonicalRepository, branch, want string) error {
	got, err := gitCanonical(ctx, canonical, "rev-parse", "refs/heads/"+branch)
	if err != nil || got != want {
		return fmt.Errorf("local branch %q moved from expected %s to %s: %w", branch, want, got, err)
	}
	return nil
}

func requireLocalClaimAbsent(ctx context.Context, canonical *canonicalRepository, branch string) error {
	got, err := gitCanonical(ctx, canonical, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
	if err != nil {
		return fmt.Errorf("verify absence of local immutable-claim branch %q: %w", branch, err)
	}
	if strings.TrimSpace(got) != "" {
		return fmt.Errorf("local immutable-claim branch %q still exists", branch)
	}
	return nil
}

func localProjectionForReconciliation(projection workLogProjection) *LocalWorkLogProjection {
	return &LocalWorkLogProjection{Version: 1, EffortID: projection.EffortID, RunID: projection.RunID, ClaimID: projection.ClaimID, Lifecycle: projection.Lifecycle}
}
