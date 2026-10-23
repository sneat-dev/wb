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

	"github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeclaims"
)

const (
	reconciliationStagePlanned  = worktreeclaims.ReconciliationStagePlanned
	reconciliationStageBundles  = worktreeclaims.ReconciliationStageBundles
	reconciliationStageRemote   = worktreeclaims.ReconciliationStageRemote
	reconciliationStageLocal    = worktreeclaims.ReconciliationStageLocal
	reconciliationStageRebound  = worktreeclaims.ReconciliationStageRebound
	reconciliationStageEvent    = worktreeclaims.ReconciliationStageEvent
	reconciliationStageComplete = worktreeclaims.ReconciliationStageComplete
)

type branchReconciliationRecord = worktreeclaims.ReconciliationRecord
type branchReconciliationEvidence = worktreeclaims.ReconciliationEvidence

func reconcileClaimBranch(ctx context.Context, options LogRecoverOptions) (LogVerbResult, error) {
	return reconcileClaimBranchWithPorts(ctx, options, reconciliationPorts{})
}

type reconciliationPorts struct {
	writeRecord   func(*os.File, branchReconciliationRecord) error
	observeGit    func(context.Context, string) LocalGitEvidence
	readEvents    func(string) ([]LocalWorkLogEvent, error)
	appendEvent   func(string, LocalWorkLogEvent) (LocalWorkLogEvent, LocalWorkLogProjection, error)
	now           func() time.Time
	lifecycle     func(context.Context, string, string, workLogClaim) (ListResult, error)
	openCanonical func(string) (*canonicalRepository, error)
	createRecord  func(string, workLogClaim, branchReconciliationRecord) (*os.File, error)
	remoteHead    func(context.Context, string, string) (string, error)
	localHead     func(context.Context, *canonicalRepository, string) (string, error)
	retireRemote  func(context.Context, *canonicalRepository, branchReconciliationRecord) error
	retireLocal   func(context.Context, *canonicalRepository, branchReconciliationRecord) error
	rebind        func(context.Context, *canonicalRepository, branchReconciliationRecord) error
	requireHead   func(context.Context, *canonicalRepository, string, string) error
	requireAbsent func(context.Context, *canonicalRepository, string) error
	corroborate   func(string, string, workLogProjection) error
	bundle        reconciliationBundlePorts
}

// Bundle ports are scoped to one reconciliation attempt. Defaults retain the
// descriptor-anchored Git and no-replace storage operations below.
type reconciliationBundlePorts struct {
	secureGit  func(context.Context, *canonicalRepository, ...string) error
	advertise  func(context.Context, *canonicalRepository, string, string, string) error
	readBytes  func(*os.File, string) ([]byte, error)
	writeBytes func(*os.File, string, []byte, os.FileMode, bool) error
	fetched    func(context.Context, *canonicalRepository, string) (string, error)
}

func (p reconciliationBundlePorts) withDefaults() reconciliationBundlePorts {
	if p.secureGit == nil {
		p.secureGit = func(ctx context.Context, canonical *canonicalRepository, args ...string) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", args...)
		}
	}
	if p.advertise == nil {
		p.advertise = requireBundleAdvertisesClaimRef
	}
	if p.readBytes == nil {
		p.readBytes = readBytesAt
	}
	if p.writeBytes == nil {
		p.writeBytes = writeBytesImmutableAt
	}
	if p.fetched == nil {
		p.fetched = func(ctx context.Context, canonical *canonicalRepository, ref string) (string, error) {
			return gitCanonical(ctx, canonical, "rev-parse", ref)
		}
	}
	return p
}

func (ports reconciliationPorts) withDefaults() reconciliationPorts {
	if ports.writeRecord == nil {
		ports.writeRecord = reconciliationClaimPorts().WriteRecord
	}
	if ports.observeGit == nil {
		ports.observeGit = observeLocalGit
	}
	if ports.readEvents == nil {
		ports.readEvents = readLocalEvents
	}
	if ports.appendEvent == nil {
		ports.appendEvent = appendLocalEvent
	}
	if ports.now == nil {
		ports.now = func() time.Time { return time.Now().UTC() }
	}
	if ports.lifecycle == nil {
		ports.lifecycle = reconciliationLifecycleEvidence
	}
	if ports.openCanonical == nil {
		ports.openCanonical = openCanonicalRepository
	}
	if ports.createRecord == nil {
		ports.createRecord = reconciliationClaimPorts().CreateRecord
	}
	if ports.remoteHead == nil {
		ports.remoteHead = remoteBranchHead
	}
	if ports.localHead == nil {
		ports.localHead = localReconciliationBranchHead
	}
	if ports.retireRemote == nil {
		ports.retireRemote = func(ctx context.Context, canonical *canonicalRepository, record branchReconciliationRecord) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "push", "--force-with-lease=refs/heads/"+record.ClaimBranch+":"+record.RemoteHead, "origin", ":refs/heads/"+record.ClaimBranch)
		}
	}
	if ports.retireLocal == nil {
		ports.retireLocal = func(ctx context.Context, canonical *canonicalRepository, record branchReconciliationRecord) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "update-ref", "-d", "refs/heads/"+record.ClaimBranch, record.LocalHead)
		}
	}
	if ports.rebind == nil {
		ports.rebind = func(ctx context.Context, canonical *canonicalRepository, record branchReconciliationRecord) error {
			return runSecureCleanupGitHelper(ctx, canonical, nil, nil, "", "", "branch", "-m", record.LiveBranch, record.ClaimBranch)
		}
	}
	if ports.requireHead == nil {
		ports.requireHead = requireLocalClaimHead
	}
	if ports.requireAbsent == nil {
		ports.requireAbsent = requireLocalClaimAbsent
	}
	if ports.corroborate == nil {
		ports.corroborate = corroborateProjectionWithPrivateClaim
	}
	ports.bundle = ports.bundle.withDefaults()
	return ports
}

func reconcileClaimBranchWithPorts(ctx context.Context, options LogRecoverOptions, ports reconciliationPorts) (LogVerbResult, error) {
	ports = ports.withDefaults()
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
	projection, claim, err := reconciliationClaimPorts().ReadClaim(home, root)
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
	entry, err := ports.lifecycle(ctx, options.ProjectsRoot, root, claim)
	if err != nil {
		return LogVerbResult{}, err
	}
	if err := validateReconciliationLifecycleEvidence(ctx, entry, claim, options); err != nil {
		return LogVerbResult{}, err
	}

	claimPorts := reconciliationClaimPorts()
	record, recordDir, recordErr := claimPorts.ReadRecord(home, claim, options.EventID)
	if recordErr != nil && !errors.Is(recordErr, os.ErrNotExist) {
		return LogVerbResult{}, recordErr
	}
	if errors.Is(recordErr, os.ErrNotExist) {
		if entry.Branch != options.ReconcileBranch {
			return LogVerbResult{}, fmt.Errorf("live branch %q does not match --reconcile-branch %q", entry.Branch, options.ReconcileBranch)
		}
		canonical, openErr := ports.openCanonical(entry.CanonicalDir)
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
		remoteHead, remoteErr := ports.remoteHead(ctx, entry.CanonicalDir, claim.Branch)
		if remoteErr != nil || !isGitObjectID(remoteHead) {
			return LogVerbResult{}, fmt.Errorf("resolve exact remote immutable-claim branch %q: %w", claim.Branch, remoteErr)
		}
		record = branchReconciliationRecord{
			Version: 1, EventID: options.EventID, ClaimID: claim.ClaimID, Worktree: root,
			Repository: entry.Repository, ClaimBranch: claim.Branch, LiveBranch: options.ReconcileBranch,
			ExpectedHead: options.ExpectedHead, LocalHead: localHead, RemoteHead: remoteHead,
			TargetHead: entry.RemoteTargetSHA, Actor: options.Actor, Reason: options.Reason,
			Stage: reconciliationStagePlanned, CreatedAt: ports.now(),
		}
		if !options.Apply {
			return LogVerbResult{Worktree: root, Verb: "recover", Projection: localProjectionForReconciliation(projection),
				Diagnosis: []string{"branch reconciliation plan is read-only", "local and remote claim heads will be preserved before retirement"},
				Notes:     []string{"pass --apply to reconcile the live branch to the immutable Work Log claim"}}, nil
		}
		recordDir, err = ports.createRecord(home, claim, record)
		if err != nil {
			return LogVerbResult{}, err
		}
		defer func() { _ = recordDir.Close() }()
	} else {
		defer func() { _ = recordDir.Close() }()
		if err := worktreeclaims.CorroborateReconciliationRecord(record, claim, reconciliationRequest(root, options)); err != nil {
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
	canonical, err := ports.openCanonical(entry.CanonicalDir)
	if err != nil {
		return LogVerbResult{}, err
	}
	defer canonical.close()
	if err := canonical.validate(); err != nil {
		return LogVerbResult{}, err
	}

	if record.Stage == reconciliationStagePlanned {
		if err := preserveReconciliationBundles(ctx, canonical, recordDir, record, options, ports.bundle); err != nil {
			return LogVerbResult{}, err
		}
		record.Stage = reconciliationStageBundles
		if err := ports.writeRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
	}
	if record.Stage == reconciliationStageBundles {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record, canonical, ports); err != nil {
			return LogVerbResult{}, err
		}
		if err := verifyReconciliationBundles(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		remoteHead, err := ports.remoteHead(ctx, entry.CanonicalDir, record.ClaimBranch)
		if err != nil {
			return LogVerbResult{}, err
		}
		switch remoteHead {
		case record.RemoteHead:
			if err := ports.retireRemote(ctx, canonical, record); err != nil {
				return LogVerbResult{}, fmt.Errorf("retire exact remote immutable-claim branch: %w", err)
			}
		case "":
			// The delete may have succeeded before the next record write. The
			// exact stored bundles above, not mere absence, authorize replay.
		default:
			return LogVerbResult{}, fmt.Errorf("remote immutable-claim branch %q moved from expected %s to %s", record.ClaimBranch, record.RemoteHead, remoteHead)
		}
		record.Stage = reconciliationStageRemote
		if err := ports.writeRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "remote" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after remote retirement")
		}
	}
	if record.Stage == reconciliationStageRemote {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record, canonical, ports); err != nil {
			return LogVerbResult{}, err
		}
		if err := requireRemoteClaimAbsent(ctx, entry.CanonicalDir, record.ClaimBranch); err != nil {
			return LogVerbResult{}, err
		}
		if err := verifyReconciliationBundles(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		localHead, err := ports.localHead(ctx, canonical, record.ClaimBranch)
		if err != nil {
			return LogVerbResult{}, err
		}
		switch localHead {
		case record.LocalHead:
			if err := ports.retireLocal(ctx, canonical, record); err != nil {
				return LogVerbResult{}, fmt.Errorf("retire exact local immutable-claim branch: %w", err)
			}
		case "":
			// The exact expected ref was deleted before the stage write.
		default:
			return LogVerbResult{}, fmt.Errorf("local branch %q moved from expected %s to %s", record.ClaimBranch, record.LocalHead, localHead)
		}
		record.Stage = reconciliationStageLocal
		if err := ports.writeRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "local" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after local retirement")
		}
	}
	if record.Stage == reconciliationStageLocal {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record, canonical, ports); err != nil {
			return LogVerbResult{}, err
		}
		if err := verifyReconciliationBundles(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		claimHead, err := ports.localHead(ctx, canonical, record.ClaimBranch)
		if err != nil {
			return LogVerbResult{}, err
		}
		switch claimHead {
		case "":
			if err := ports.requireHead(ctx, canonical, record.LiveBranch, record.ExpectedHead); err != nil {
				return LogVerbResult{}, err
			}
			if err := ports.rebind(ctx, canonical, record); err != nil {
				return LogVerbResult{}, fmt.Errorf("rebind live branch to immutable claim branch: %w", err)
			}
		case record.ExpectedHead:
			if entry.Branch != record.ClaimBranch {
				return LogVerbResult{}, fmt.Errorf("rebound immutable claim branch is not checked out at the recorded worktree")
			}
			if err := ports.requireAbsent(ctx, canonical, record.LiveBranch); err != nil {
				return LogVerbResult{}, err
			}
		default:
			return LogVerbResult{}, fmt.Errorf("immutable claim branch reappeared at %s before exact rebind", claimHead)
		}
		record.Stage = reconciliationStageRebound
		if err := ports.writeRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "rebound" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after branch rebind")
		}
	}
	if record.Stage == reconciliationStageRebound {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record, canonical, ports); err != nil {
			return LogVerbResult{}, err
		}
		if err := ports.corroborate(home, root, projection); err != nil {
			return LogVerbResult{}, fmt.Errorf("re-corroborate immutable Work Log claim after branch rebind: %w", err)
		}
		event, updated, appendErr := appendReconciliationEvent(ctx, root, record, ports)
		if appendErr != nil {
			return LogVerbResult{}, appendErr
		}
		record.Stage = reconciliationStageEvent
		if err := ports.writeRecord(recordDir, record); err != nil {
			return LogVerbResult{}, err
		}
		if options.testStopAfterStage == "event" {
			return LogVerbResult{}, fmt.Errorf("injected interruption after branch-reconciled event")
		}
		return finishBranchReconciliation(recordDir, record, root, event, updated)
	}
	if record.Stage == reconciliationStageEvent || record.Stage == reconciliationStageComplete {
		if err := revalidateReconciliationStage(ctx, options, root, claim, record, canonical, ports); err != nil {
			return LogVerbResult{}, err
		}
		event, updated, appendErr := appendReconciliationEvent(ctx, root, record, ports)
		if appendErr != nil {
			return LogVerbResult{}, appendErr
		}
		if record.Stage == reconciliationStageEvent {
			return finishBranchReconciliation(recordDir, record, root, event, updated)
		}
		return completedBranchReconciliationResult(root, event, updated), nil
	}
	return LogVerbResult{}, fmt.Errorf("unknown branch reconciliation stage %q", record.Stage)
}

func reconciliationEvent(record branchReconciliationRecord, git LocalGitEvidence) LocalWorkLogEvent {
	return LocalWorkLogEvent{ID: record.EventID, Type: LocalEventBranchReconciled,
		Message: record.Reason, Git: ptrLocalGit(git),
		Extra: map[string]any{"actor": record.Actor, "live_branch": record.LiveBranch,
			"claim_branch": record.ClaimBranch, "local_head": record.LocalHead,
			"remote_head": record.RemoteHead}}
}

func appendReconciliationEvent(ctx context.Context, root string, record branchReconciliationRecord, ports reconciliationPorts) (LocalWorkLogEvent, LocalWorkLogProjection, error) {
	events, err := ports.readEvents(root)
	if err != nil {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, err
	}
	for _, prior := range events {
		if prior.ID != record.EventID {
			continue
		}
		if prior.Git == nil {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("recorded branch reconciliation event has no Git evidence")
		}
		if prior.Git.Branch != record.ClaimBranch || prior.Git.Head != record.ExpectedHead {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("recorded branch reconciliation event Git identity differs from exact recovery request")
		}
		expected := reconciliationEvent(record, *prior.Git)
		expected.Version, expected.Seq, expected.At = prior.Version, prior.Seq, prior.At
		if !sameLocalEvent(prior, expected) {
			return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("recorded branch reconciliation event %q differs from exact recovery request", record.EventID)
		}
		return ports.appendEvent(root, prior)
	}
	observed := ports.observeGit(ctx, root)
	if observed.Branch != record.ClaimBranch || observed.Head != record.ExpectedHead {
		return LocalWorkLogEvent{}, LocalWorkLogProjection{}, fmt.Errorf("observed branch reconciliation Git identity differs from exact recovery request")
	}
	return ports.appendEvent(root, reconciliationEvent(record, observed))
}

func localReconciliationBranchHead(ctx context.Context, canonical *canonicalRepository, branch string) (string, error) {
	return gitCanonical(ctx, canonical, "for-each-ref", "--format=%(objectname)", "refs/heads/"+branch)
}

func verifyReconciliationBundles(directory *os.File, record branchReconciliationRecord) error {
	for _, item := range []struct{ kind, reference, head string }{
		{"local", "refs/heads/" + record.ClaimBranch, record.LocalHead},
		{"remote", "refs/remotes/origin/" + record.ClaimBranch, record.RemoteHead},
	} {
		var evidence branchReconciliationEvidence
		if err := readJSONAt(directory, item.kind+".json", &evidence); err != nil {
			return fmt.Errorf("read durable %s reconciliation bundle evidence: %w", item.kind, err)
		}
		if evidence.Version != 1 || evidence.Head != item.head || evidence.Ref != item.reference || evidence.Bundle != item.kind+".bundle" {
			return fmt.Errorf("durable %s reconciliation bundle evidence differs from recorded authority", item.kind)
		}
		content, err := readBytesAt(directory, evidence.Bundle)
		if err != nil {
			return fmt.Errorf("read durable %s reconciliation bundle: %w", item.kind, err)
		}
		digest := sha256.Sum256(content)
		if hex.EncodeToString(digest[:]) != evidence.SHA256 {
			return fmt.Errorf("durable %s reconciliation bundle digest differs from recorded authority", item.kind)
		}
	}
	return nil
}

func finishBranchReconciliation(directory *os.File, record branchReconciliationRecord, root string, event LocalWorkLogEvent, projection LocalWorkLogProjection) (LogVerbResult, error) {
	record.Stage = reconciliationStageComplete
	if err := reconciliationClaimPorts().WriteRecord(directory, record); err != nil {
		return LogVerbResult{}, err
	}
	return completedBranchReconciliationResult(root, event, projection), nil
}

func completedBranchReconciliationResult(root string, event LocalWorkLogEvent, projection LocalWorkLogProjection) LogVerbResult {
	return LogVerbResult{Worktree: root, Verb: "recover", Event: &event, Projection: &projection, Applied: true, ReadyForNormalCleanup: true,
		Notes: []string{"immutable Work Log claim re-corroborated; ready for normal cleanup"}}
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
	locked, err := openLockedWorkLogRun(home, claim.EffortID, claim.RunID, claim.ClaimID, false)
	if err != nil {
		return nil, err
	}
	return locked.close, nil
}

func reconciliationClaimPorts() worktreeclaims.ReconciliationPorts {
	return worktreeclaims.ReconciliationPorts{ReadProjection: readWorkLogProjectionForReadOnlyClaim}
}

func reconciliationRequest(root string, options LogRecoverOptions) worktreeclaims.ReconciliationRequest {
	return worktreeclaims.ReconciliationRequest{
		Worktree: root, EventID: options.EventID, LiveBranch: options.ReconcileBranch,
		ExpectedHead: options.ExpectedHead, Actor: options.Actor, Reason: options.Reason,
	}
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

func revalidateReconciliationStage(ctx context.Context, options LogRecoverOptions, root string, claim workLogClaim, record branchReconciliationRecord, canonical *canonicalRepository, ports reconciliationPorts) error {
	entry, err := ports.lifecycle(ctx, options.ProjectsRoot, root, claim)
	if err != nil {
		return err
	}
	if err := validateReconciliationLifecycleEvidence(ctx, entry, claim, options); err != nil {
		return err
	}
	switch record.Stage {
	case reconciliationStageRebound, reconciliationStageEvent, reconciliationStageComplete:
		if entry.Branch != record.ClaimBranch {
			return fmt.Errorf("live branch %q is not the rebound immutable claim %q", entry.Branch, record.ClaimBranch)
		}
		if err := ports.requireHead(ctx, canonical, record.ClaimBranch, record.ExpectedHead); err != nil {
			return fmt.Errorf("verify rebound immutable claim head: %w", err)
		}
		if err := ports.requireAbsent(ctx, canonical, record.LiveBranch); err != nil {
			return fmt.Errorf("verify retired live branch absence: %w", err)
		}
	default:
		if entry.Branch != record.LiveBranch && entry.Branch != record.ClaimBranch {
			return fmt.Errorf("live branch %q changed outside the recorded reconciliation", entry.Branch)
		}
	}
	return nil
}

func preserveReconciliationBundles(ctx context.Context, canonical *canonicalRepository, directory *os.File, record branchReconciliationRecord, options LogRecoverOptions, ports reconciliationBundlePorts) error {
	if options.testBeforeBundleCheck != nil {
		options.testBeforeBundleCheck()
	}
	if err := requireLocalClaimHead(ctx, canonical, record.ClaimBranch, record.LocalHead); err != nil {
		return err
	}
	if err := requireRemoteClaimHead(ctx, canonical.path, record.ClaimBranch, record.RemoteHead); err != nil {
		return err
	}
	if err := bundleClaimHead(ctx, canonical, directory, record.EventID, "local", "refs/heads/"+record.ClaimBranch, record.LocalHead, ports); err != nil {
		return err
	}
	if options.testFailAfterBundle == "local" {
		return fmt.Errorf("injected bundle failure after local preservation")
	}
	remoteRef := "refs/remotes/origin/" + record.ClaimBranch
	if err := ports.secureGit(ctx, canonical, "fetch", "--no-tags", "origin", "+refs/heads/"+record.ClaimBranch+":"+remoteRef); err != nil {
		return fmt.Errorf("fetch remote immutable-claim head for bundle preservation: %w", err)
	}
	fetched, err := ports.fetched(ctx, canonical, remoteRef)
	if err != nil || fetched != record.RemoteHead {
		return fmt.Errorf("fetched remote immutable-claim head changed from %s to %s: %w", record.RemoteHead, fetched, err)
	}
	if err := bundleClaimHead(ctx, canonical, directory, record.EventID, "remote", remoteRef, record.RemoteHead, ports); err != nil {
		return err
	}
	if options.testFailAfterBundle == "remote" {
		return fmt.Errorf("injected bundle failure after remote preservation")
	}
	return nil
}

func bundleClaimHead(ctx context.Context, canonical *canonicalRepository, directory *os.File, eventID, kind, reference, head string, ports reconciliationBundlePorts) error {
	ports = ports.withDefaults()
	name := "wb-reconcile-" + eventID + "-" + kind + ".bundle"
	path := filepath.Join(canonical.path, ".git", name)
	if err := ports.secureGit(ctx, canonical, "bundle", "create", path, reference); err != nil {
		return fmt.Errorf("create %s recovery bundle for %s at %s: %w", kind, reference, head, err)
	}
	if err := ports.secureGit(ctx, canonical, "bundle", "verify", path); err != nil {
		return fmt.Errorf("verify %s recovery bundle for %s at %s: %w", kind, reference, head, err)
	}
	if err := ports.advertise(ctx, canonical, path, reference, head); err != nil {
		return fmt.Errorf("verify %s recovery bundle advertisement: %w", kind, err)
	}
	content, err := ports.readBytes(canonical.common, name)
	if err != nil {
		return fmt.Errorf("read verified %s recovery bundle: %w", kind, err)
	}
	defer func() { _ = unix.Unlinkat(int(canonical.common.Fd()), name, 0) }()
	digest := sha256.Sum256(content)
	bundleName := kind + ".bundle"
	if err := ports.writeBytes(directory, bundleName, content, 0o600, true); err != nil {
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
