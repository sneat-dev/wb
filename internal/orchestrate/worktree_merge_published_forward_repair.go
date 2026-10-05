package orchestrate

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

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/runner"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// WorktreeMergePublishedForwardRepair is a deliberately unprepared candidate
// for correcting one historical self-supersession. It has no merge receipt:
// the later correction is the only append-only transition that can consume it.
type WorktreeMergePublishedForwardRepair struct {
	Status               string                                   `json:"status"`
	ReceiptPath          string                                   `json:"receipt_path"`
	ReceiptSHA256        string                                   `json:"receipt_sha256"`
	ImmutableClaimSHA256 string                                   `json:"immutable_claim_sha256"`
	SupersessionPath     string                                   `json:"supersession_path"`
	SupersessionSHA256   string                                   `json:"supersession_sha256"`
	CurrentTargetSHA     string                                   `json:"current_target_sha"`
	Sources              []WorktreeMergeSource                    `json:"sources"`
	RequiredRoots        []WorktreeMergeValidationFailureSealRoot `json:"required_roots"`
	Candidate            WorktreeMergeCandidate                   `json:"candidate"`
	Actor                string                                   `json:"actor"`
	Reason               string                                   `json:"reason"`
}

// WorktreeMergePublishedForwardRepairOptions pins every mutable historical
// input needed to create a distinct candidate for one known self-supersession.
// ExpectedSourceSHAs are positional with Sources, so a caller cannot silently
// replace a requested current repair source with another clean worktree.
type WorktreeMergePublishedForwardRepairOptions struct {
	ProjectsRoot, Receipt, ExpectedReceiptSHA256, ExpectedImmutableClaimSHA256 string
	ExpectedSupersessionSHA256, ExpectedCurrentTargetSHA                       string
	Sources, ExpectedSourceSHAs                                                []string
	Apply                                                                      bool
	Actor, Reason                                                              string
	Model, AgentRuntime, AgentID, Initiator, CLI, Provider                     string
	SessionRequired                                                            bool
	Timeout                                                                    time.Duration
	Retry                                                                      int
}

// RepairTask is deterministic solely from caller-pinned immutable evidence,
// allowing apply retries to resume the same WB-managed candidate without
// guessing a branch from mutable filesystem state.
func (options WorktreeMergePublishedForwardRepairOptions) RepairTask() string {
	hash := sha256.New()
	for _, value := range append([]string{strings.TrimSpace(options.ExpectedReceiptSHA256), strings.TrimSpace(options.ExpectedSupersessionSHA256), strings.TrimSpace(options.ExpectedCurrentTargetSHA)}, options.ExpectedSourceSHAs...) {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return "published-forward-repair-" + hex.EncodeToString(hash.Sum(nil)[:8])
}

// PreparePublishedValidationFailureForwardRepair is the explicit cycle breaker
// for an already-published historical self-supersession. Ordinary prepare stays
// fail-closed. This path writes no historical artifact and creates no merge
// receipt; on apply it creates only one new WB-managed clean candidate whose
// DAG contains every immutable historical root and every pinned current source.
func PreparePublishedValidationFailureForwardRepair(ctx context.Context, options WorktreeMergePublishedForwardRepairOptions) (WorktreeMergePublishedForwardRepair, error) {
	return preparePublishedValidationFailureForwardRepair(ctx, options, defaultRunner, worktreeMergeReceiptSHA256, os.ReadFile, nil, nil)
}

// The per-invocation callbacks observe the two original temporal boundaries.
// Production supplies nil and every custody check remains native.
func preparePublishedValidationFailureForwardRepair(ctx context.Context, options WorktreeMergePublishedForwardRepairOptions, run runner.Runner, hash func(string) (string, error), readFile func(string) ([]byte, error), beforeCreate, beforeFinalRevalidation func()) (result WorktreeMergePublishedForwardRepair, retErr error) {
	if err := requirePublishedForwardRepairExpectations(options); err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergePublishedForwardRepair{}, errors.New("--actor and --reason are required with --apply")
	}
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	if err := validateValidationFailedSupersessionReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	defer func() { _ = lock.Release() }()

	evidence, err := inspectPublishedForwardRepairEvidence(ctx, options, receiptPath, run, hash, readFile)
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	receipt, receiptHash, claimHash := evidence.receipt, evidence.receiptHash, evidence.claimHash
	supersession, supersessionPath, supersessionHash := evidence.supersession, evidence.supersessionPath, evidence.supersessionHash
	correctionHash, sources := evidence.correctionHash, evidence.sources
	repository, canonical, currentTarget, roots := evidence.repository, evidence.canonical, evidence.currentTarget, evidence.roots
	revalidate := func() error {
		return revalidatePublishedForwardRepairEvidence(ctx, options, run, hash, readFile, receiptPath, receiptHash, claimHash, supersessionHash, correctionHash, sources, repository, canonical, currentTarget)
	}
	task := options.RepairTask()
	branch := "wb/recovery/" + receipt.Target + "/" + mergeOperationSuffix(task) + "-published-forward-repair"
	result = WorktreeMergePublishedForwardRepair{
		Status: "published_forward_repair_planned", ReceiptPath: receiptPath, ReceiptSHA256: receiptHash,
		ImmutableClaimSHA256: claimHash, SupersessionPath: supersessionPath, SupersessionSHA256: supersessionHash,
		CurrentTargetSHA: currentTarget, Sources: append([]WorktreeMergeSource(nil), sources...), RequiredRoots: roots,
		Candidate: WorktreeMergeCandidate{Task: task, Branch: branch}, Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason),
	}
	if !options.Apply {
		return result, nil
	}
	if beforeCreate != nil {
		beforeCreate()
	}
	if err := revalidate(); err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	listed, err := worktrees.List(ctx, worktrees.ListOptions{ProjectsRoot: options.ProjectsRoot, Task: task, Base: receipt.Target, Workers: 1})
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, fmt.Errorf("inspect published forward-repair worktree: %w", err)
	}
	if len(listed) > 1 {
		return WorktreeMergePublishedForwardRepair{}, fmt.Errorf("published forward-repair task %s resolves to %d worktrees, want at most one", task, len(listed))
	}
	prompt, err := writePublishedForwardRepairPrompt(receipt, supersession, currentTarget, sources, roots, result.Actor, result.Reason)
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	defer func() { _ = os.Remove(prompt) }()
	model := strings.TrimSpace(options.Model)
	if model == "" {
		model = "unknown"
	}
	created, err := worktrees.Create(ctx, []string{receipt.Repository}, worktrees.CreateOptions{
		ProjectsRoot: options.ProjectsRoot, Operation: task, Branch: branch, BranchChosen: true, Base: receipt.Target,
		Resume: len(listed) == 1, SessionRequired: options.SessionRequired,
		WorkLog: worktrees.WorkLogOptions{EffortID: task, RunID: task, Initiator: options.Initiator, AgentID: options.AgentID, AgentRuntime: options.AgentRuntime, Model: model, CLI: options.CLI, Provider: options.Provider, OriginalPrompt: prompt, RequireOriginalPrompt: true},
	})
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, fmt.Errorf("create published forward-repair worktree: %w", err)
	}
	createdNew := len(listed) == 0
	completed := false
	defer func() {
		if !createdNew || completed {
			return
		}
		_, abortErr := worktrees.Abort(ctx, worktrees.AbortOptions{
			ProjectsRoot: options.ProjectsRoot, Task: task, Base: receipt.Target, All: true,
			Disposition: worktrees.AbortDiscarded, DeleteRemote: true, Apply: true,
		})
		if abortErr != nil {
			retErr = fmt.Errorf("%w; retire invalid published forward-repair candidate: %v", retErr, abortErr)
		}
	}()
	if created[0].BaseSHA != currentTarget || filepath.Clean(created[0].CanonicalDir) != filepath.Clean(canonical) {
		return WorktreeMergePublishedForwardRepair{}, errors.New("target or canonical identity drifted while creating published forward-repair candidate")
	}
	candidate, replacementClaim, err := validateValidationFailureReplacementWithRunner(ctx, run, options.ProjectsRoot, receipt, created[0].WorktreeDir)
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	if candidate.Task != task || candidate.Branch != branch || replacementClaim.BaseSHA != currentTarget || candidate.SHA == receipt.Candidate.SHA {
		return WorktreeMergePublishedForwardRepair{}, errors.New("published forward-repair candidate has an unexpected identity or self replacement")
	}
	if err := mergePublishedForwardRepairRootsWithRunner(ctx, run, candidate.Worktree, roots, options.Timeout, options.Retry); err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	candidate.SHA, err = mergeRevision(ctx, run, candidate.Worktree, "HEAD")
	if err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	if err := requireCleanMergeWorktreeWithRunner(ctx, run, candidate.Worktree); err != nil {
		return WorktreeMergePublishedForwardRepair{}, fmt.Errorf("published forward-repair candidate is not clean: %w", err)
	}
	// Re-read every pre-existing boundary after construction. No new merge
	// receipt is ever persisted, so a race cannot rewrite historic evidence.
	if beforeFinalRevalidation != nil {
		beforeFinalRevalidation()
	}
	if err := revalidate(); err != nil {
		return WorktreeMergePublishedForwardRepair{}, err
	}
	for _, root := range roots {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, candidate.Worktree, root.SHA, candidate.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("published forward-repair candidate %s does not contain required %s root %s", candidate.SHA, root.Kind, root.SHA)
			}
			return WorktreeMergePublishedForwardRepair{}, ancestorErr
		}
	}
	result.Status, result.Candidate = "published_forward_repair_prepared", candidate
	completed = true
	return result, nil
}

// publishedForwardRepairEvidence contains only observations captured while the
// real lane lock is held. Revalidation consumes the same pinned snapshot before
// and after native construction; it never substitutes a custody decision.
type publishedForwardRepairEvidence struct {
	receipt                                            WorktreeMergeReceipt
	receiptHash, claimHash                             string
	supersession                                       WorktreeMergeValidationFailureSupersession
	supersessionPath, supersessionHash, correctionHash string
	sources                                            []WorktreeMergeSource
	repository, canonical, currentTarget               string
	roots                                              []WorktreeMergeValidationFailureSealRoot
}

func inspectPublishedForwardRepairEvidence(ctx context.Context, options WorktreeMergePublishedForwardRepairOptions, receiptPath string, run runner.Runner, hash func(string) (string, error), readFile func(string) ([]byte, error)) (publishedForwardRepairEvidence, error) {
	// All mutable evidence is re-read after the lane lock. In particular, this
	// replays the corrupt acknowledgement rather than trusting an earlier read.
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil {
		return publishedForwardRepairEvidence{}, err
	}
	if err := validateValidationFailedSupersessionReceipt(receipt, receiptPath); err != nil {
		return publishedForwardRepairEvidence{}, err
	}
	receiptHash, err := hash(receiptPath)
	if err != nil || receiptHash != options.ExpectedReceiptSHA256 {
		if err == nil {
			err = fmt.Errorf("receipt SHA256 %s does not match expected %s", receiptHash, options.ExpectedReceiptSHA256)
		}
		return publishedForwardRepairEvidence{}, err
	}
	originalClaim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return publishedForwardRepairEvidence{}, fmt.Errorf("validate failed candidate: %w", err)
	}
	claimBytes, err := readFile(originalClaim.ClaimPath)
	if err != nil {
		return publishedForwardRepairEvidence{}, fmt.Errorf("read immutable failed candidate claim: %w", err)
	}
	claimDigest := sha256.Sum256(claimBytes)
	claimHash := hex.EncodeToString(claimDigest[:])
	if claimHash != options.ExpectedImmutableClaimSHA256 {
		return publishedForwardRepairEvidence{}, fmt.Errorf("immutable claim SHA256 %s does not match expected %s", claimHash, options.ExpectedImmutableClaimSHA256)
	}
	supersessionPath := validationFailureSupersessionPath(receiptPath)
	supersession, err := readValidationFailureSupersession(supersessionPath, receipt)
	if err != nil {
		return publishedForwardRepairEvidence{}, fmt.Errorf("read existing self-supersession: %w", err)
	}
	if supersession.Replacement != supersession.OriginalCandidate || supersession.OriginalCandidate != receipt.Candidate || supersession.ReplacementClaimBaseSHA != supersession.OriginalClaimBaseSHA || supersession.OriginalClaimBaseSHA != originalClaim.BaseSHA {
		return publishedForwardRepairEvidence{}, errors.New("existing supersession is not the exact self-supersession repair shape")
	}
	supersessionHash, err := hash(supersessionPath)
	if err != nil || supersessionHash != options.ExpectedSupersessionSHA256 {
		if err == nil {
			err = fmt.Errorf("supersession SHA256 %s does not match expected %s", supersessionHash, options.ExpectedSupersessionSHA256)
		}
		return publishedForwardRepairEvidence{}, err
	}
	correctionPath := selfSupersessionCorrectionPath(receiptPath)
	correction, correctionErr := readSelfSupersessionCorrection(correctionPath, receipt, supersession)
	correctionHash := ""
	if correctionErr == nil {
		if err := validatePublishedForwardRepairCorrectionBinding(correction, receipt, supersession, receiptHash, claimHash, supersessionHash); err != nil {
			return publishedForwardRepairEvidence{}, fmt.Errorf("existing self-supersession correction is invalid: %w", err)
		}
		correctionHash, err = hash(correctionPath)
		if err != nil {
			return publishedForwardRepairEvidence{}, err
		}
	} else if !errors.Is(correctionErr, os.ErrNotExist) {
		return publishedForwardRepairEvidence{}, fmt.Errorf("existing self-supersession correction is invalid: %w", correctionErr)
	}

	sources, repository, canonical, err := inspectPublishedForwardRepairSourcesWithRunner(ctx, run, options.ProjectsRoot, options.Sources, receipt.Target)
	if err != nil {
		return publishedForwardRepairEvidence{}, err
	}
	if repository != receipt.Repository {
		return publishedForwardRepairEvidence{}, fmt.Errorf("repair sources belong to %s, want failed receipt repository %s", repository, receipt.Repository)
	}
	if err := requireExpectedPublishedForwardRepairSources(sources, options.ExpectedSourceSHAs); err != nil {
		return publishedForwardRepairEvidence{}, err
	}
	if err := requireImmutableHistoricalWorktreeMergeSourcesWithRunner(ctx, run, canonical, receipt); err != nil {
		return publishedForwardRepairEvidence{}, err
	}
	currentTarget, err := fetchExactMergeTargetWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.Target)
	if err != nil {
		return publishedForwardRepairEvidence{}, err
	}
	if currentTarget != options.ExpectedCurrentTargetSHA {
		return publishedForwardRepairEvidence{}, fmt.Errorf("current target %s does not match pinned repair and self-supersession target evidence", currentTarget)
	}
	if currentTarget != supersession.CurrentTargetSHA {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, supersession.CurrentTargetSHA, currentTarget)
		if ancestorErr != nil || !contains {
			return publishedForwardRepairEvidence{}, fmt.Errorf("current target %s does not descend from self-supersession target %s", currentTarget, supersession.CurrentTargetSHA)
		}
	}
	roots := publishedForwardRepairRoots(originalClaim.BaseSHA, receipt, supersession, currentTarget, sources)
	if correctionHash != "" {
		roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "corrected_replacement", SHA: correction.CorrectedReplacement.SHA})
		for _, source := range correction.Sources {
			roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "corrected_source:" + source.Task, SHA: source.SHA})
		}
	}
	return publishedForwardRepairEvidence{
		receipt: receipt, receiptHash: receiptHash, claimHash: claimHash,
		supersession: supersession, supersessionPath: supersessionPath, supersessionHash: supersessionHash, correctionHash: correctionHash,
		sources: sources, repository: repository, canonical: canonical, currentTarget: currentTarget, roots: roots,
	}, nil
}

func requirePublishedForwardRepairExpectations(options WorktreeMergePublishedForwardRepairOptions) error {
	if strings.TrimSpace(options.ProjectsRoot) == "" || strings.TrimSpace(options.Receipt) == "" || strings.TrimSpace(options.ExpectedReceiptSHA256) == "" || strings.TrimSpace(options.ExpectedImmutableClaimSHA256) == "" || strings.TrimSpace(options.ExpectedSupersessionSHA256) == "" || strings.TrimSpace(options.ExpectedCurrentTargetSHA) == "" || len(options.Sources) == 0 || len(options.Sources) != len(options.ExpectedSourceSHAs) {
		return errors.New("receipt, projects root, expected receipt, immutable claim, self-supersession, current target, and one expected SHA per source are required")
	}
	for _, sha := range options.ExpectedSourceSHAs {
		if strings.TrimSpace(sha) == "" {
			return errors.New("each repair source must have an expected SHA")
		}
	}
	return nil
}

func inspectPublishedForwardRepairSources(ctx context.Context, projectsRoot string, paths []string, target string) ([]WorktreeMergeSource, string, string, error) {
	return inspectPublishedForwardRepairSourcesWithRunner(ctx, defaultRunner, projectsRoot, paths, target)
}

func inspectPublishedForwardRepairSourcesWithRunner(ctx context.Context, run runner.Runner, projectsRoot string, paths []string, target string) ([]WorktreeMergeSource, string, string, error) {
	sources, repository, canonical, err := inspectWorktreeMergeSourcesWithRunner(ctx, run, projectsRoot, paths, target)
	if err != nil {
		return nil, "", "", err
	}
	for _, source := range sources {
		view, viewErr := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: projectsRoot, Worktree: source.Worktree})
		if viewErr != nil || view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != repository || view.Claim.Task != source.Task || view.Claim.Branch != source.Branch || view.Claim.Base != target || view.Claim.BaseSHA == "" {
			return nil, "", "", fmt.Errorf("repair source %s has no exact active Work Log claim", source.Worktree)
		}
	}
	return sources, repository, canonical, nil
}

func requireExpectedPublishedForwardRepairSources(sources []WorktreeMergeSource, expected []string) error {
	if len(sources) != len(expected) {
		return errors.New("expected repair source SHA count does not match sources")
	}
	for i, source := range sources {
		if source.SHA != expected[i] {
			return fmt.Errorf("repair source %s SHA %s does not match expected %s", source.Worktree, source.SHA, expected[i])
		}
	}
	return nil
}

// requireImmutableHistoricalWorktreeMergeSources treats the pinned failed
// receipt and its source-refresh chain as immutable roots of the failed
// candidate DAG, not live worktree requirements. Current sources are supplied
// separately and receive the full managed-worktree and active-claim checks
// above.
func requireImmutableHistoricalWorktreeMergeSources(ctx context.Context, repositoryDir string, receipt WorktreeMergeReceipt) error {
	return requireImmutableHistoricalWorktreeMergeSourcesWithRunner(ctx, defaultRunner, repositoryDir, receipt)
}

func requireImmutableHistoricalWorktreeMergeSourcesWithRunner(ctx context.Context, run runner.Runner, repositoryDir string, receipt WorktreeMergeReceipt) error {
	if receipt.Candidate.SHA == "" {
		return errors.New("failed receipt has no immutable candidate SHA for historical source provenance")
	}
	candidateSHA, err := mergeRevision(ctx, run, repositoryDir, receipt.Candidate.SHA)
	if err != nil || candidateSHA != receipt.Candidate.SHA {
		if err == nil {
			err = fmt.Errorf("resolved %s", candidateSHA)
		}
		return fmt.Errorf("immutable failed candidate %s is not an available exact commit: %w", receipt.Candidate.SHA, err)
	}
	for _, source := range immutableHistoricalWorktreeMergeSources(receipt) {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return errors.New("failed receipt contains an incomplete immutable historical source identity")
		}
		revision, err := mergeRevision(ctx, run, repositoryDir, source.SHA)
		if err != nil || revision != source.SHA {
			if err == nil {
				err = fmt.Errorf("resolved %s", revision)
			}
			return fmt.Errorf("immutable historical source %s@%s is not an available exact commit: %w", source.Branch, source.SHA, err)
		}
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, repositoryDir, source.SHA, receipt.Candidate.SHA)
		if ancestorErr != nil || !contains {
			if ancestorErr == nil {
				ancestorErr = fmt.Errorf("immutable historical source %s@%s is not an ancestor of failed candidate %s", source.Branch, source.SHA, receipt.Candidate.SHA)
			}
			return ancestorErr
		}
	}
	return nil
}

func immutableHistoricalWorktreeMergeSources(receipt WorktreeMergeReceipt) []WorktreeMergeSource {
	sources := append([]WorktreeMergeSource(nil), receipt.Sources...)
	for _, refresh := range receipt.SourceRefreshes {
		sources = append(sources, refresh.Sources...)
	}
	return sources
}

func publishedForwardRepairRoots(claimBase string, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession, currentTarget string, sources []WorktreeMergeSource) []WorktreeMergeValidationFailureSealRoot {
	roots := make([]WorktreeMergeValidationFailureSealRoot, 0, len(sources)+4+len(receipt.Sources))
	// Merge complete current repair sources first. They commonly already retain
	// the historical roots, avoiding content merges from obsolete side trees.
	for _, source := range sources {
		roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "current_repair_source:" + source.Task, SHA: source.SHA})
	}
	roots = append(roots, []WorktreeMergeValidationFailureSealRoot{{Kind: "failed_candidate_claim_base", SHA: claimBase}, {Kind: "receipt_target", SHA: receipt.TargetSHA}, {Kind: "self_supersession_current_target", SHA: supersession.CurrentTargetSHA}, {Kind: "current_remote_target", SHA: currentTarget}}...)
	for _, source := range receipt.Sources {
		roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "receipted_source:" + source.Task, SHA: source.SHA})
	}
	for _, refresh := range receipt.SourceRefreshes {
		for _, source := range refresh.Sources {
			roots = append(roots, WorktreeMergeValidationFailureSealRoot{Kind: "receipted_refresh_source:" + source.Task, SHA: source.SHA})
		}
	}
	return roots
}

func mergePublishedForwardRepairRoots(ctx context.Context, worktree string, roots []WorktreeMergeValidationFailureSealRoot, timeout time.Duration, retry int) error {
	return mergePublishedForwardRepairRootsWithRunner(ctx, defaultRunner, worktree, roots, timeout, retry)
}

func mergePublishedForwardRepairRootsWithRunner(ctx context.Context, run runner.Runner, worktree string, roots []WorktreeMergeValidationFailureSealRoot, timeout time.Duration, retry int) error {
	seen := map[string]bool{}
	for _, root := range roots {
		if root.SHA == "" || seen[root.SHA] {
			continue
		}
		seen[root.SHA] = true
		head, err := mergeRevision(ctx, run, worktree, "HEAD")
		if err != nil {
			return err
		}
		contains, err := isMergeAncestorWithRunner(ctx, run, worktree, root.SHA, head)
		if err != nil {
			return err
		}
		if contains {
			continue
		}
		if _, _, err := runCommand(ctx, run, timeout, retry, worktree, "git", "merge", "--no-edit", root.SHA); err != nil {
			_, _, _ = runCommand(ctx, run, timeout, 0, worktree, "git", "merge", "--abort")
			return fmt.Errorf("merge required %s root %s: %w", root.Kind, root.SHA, err)
		}
	}
	return nil
}

func revalidatePublishedForwardRepairEvidence(ctx context.Context, options WorktreeMergePublishedForwardRepairOptions, run runner.Runner, hash func(string) (string, error), readFile func(string) ([]byte, error), receiptPath, receiptHash, claimHash, supersessionHash, correctionHash string, sources []WorktreeMergeSource, repository, canonical, currentTarget string) error {
	receipt, err := readWorktreeMergeReceipt(receiptPath)
	if err != nil || validateValidationFailedSupersessionReceipt(receipt, receiptPath) != nil {
		return errors.New("failed receipt changed during published forward-repair construction")
	}
	if current, hashErr := hash(receiptPath); hashErr != nil || current != receiptHash || current != options.ExpectedReceiptSHA256 {
		return errors.New("failed receipt SHA256 changed during published forward-repair construction")
	}
	claim, err := validateMergeAcknowledgementCandidateWithRunner(ctx, run, options.ProjectsRoot, receipt, receipt.Candidate)
	if err != nil {
		return fmt.Errorf("failed candidate changed during published forward-repair construction: %w", err)
	}
	contents, err := readFile(claim.ClaimPath)
	claimDigest := sha256.Sum256(contents)
	if err != nil || hex.EncodeToString(claimDigest[:]) != claimHash || claimHash != options.ExpectedImmutableClaimSHA256 {
		return errors.New("immutable failed candidate claim changed during published forward-repair construction")
	}
	supersession, err := readValidationFailureSupersession(validationFailureSupersessionPath(receiptPath), receipt)
	if err != nil {
		return err
	}
	if current, hashErr := hash(supersession.AcknowledgementPath); hashErr != nil || current != supersessionHash || current != options.ExpectedSupersessionSHA256 {
		return errors.New("self-supersession acknowledgement changed during published forward-repair construction")
	}
	if supersession.CurrentTargetSHA != currentTarget {
		contains, ancestorErr := isMergeAncestorWithRunner(ctx, run, receipt.Candidate.Worktree, supersession.CurrentTargetSHA, currentTarget)
		if ancestorErr != nil || !contains {
			return errors.New("current target no longer descends from the self-supersession target during published forward-repair construction")
		}
	}
	if currentCorrection, correctionErr := readSelfSupersessionCorrection(selfSupersessionCorrectionPath(receiptPath), receipt, supersession); correctionErr == nil {
		if correctionHash == "" {
			return errors.New("self-supersession was corrected during published forward-repair construction")
		}
		if current, hashErr := hash(selfSupersessionCorrectionPath(receiptPath)); hashErr != nil || current != correctionHash {
			return errors.New("self-supersession correction changed during published forward-repair construction")
		}
		if err := validatePublishedForwardRepairCorrectionBinding(currentCorrection, receipt, supersession, receiptHash, claimHash, supersessionHash); err != nil {
			return err
		}
		_ = currentCorrection
	} else if !errors.Is(correctionErr, os.ErrNotExist) {
		return fmt.Errorf("self-supersession correction changed during published forward-repair construction: %w", correctionErr)
	} else if correctionHash != "" {
		return errors.New("self-supersession correction disappeared during published forward-repair construction")
	}
	refreshedSources, refreshedRepository, refreshedCanonical, err := inspectPublishedForwardRepairSourcesWithRunner(ctx, run, options.ProjectsRoot, options.Sources, receipt.Target)
	if err != nil {
		return fmt.Errorf("revalidate current repair source identity: %w", err)
	}
	if refreshedRepository != repository || filepath.Clean(refreshedCanonical) != filepath.Clean(canonical) || !sameWorktreeMergeSources(refreshedSources, sources) {
		return errors.New("current repair source slice, repository, or canonical identity changed during published forward-repair construction")
	}
	if err := requireExpectedPublishedForwardRepairSources(refreshedSources, options.ExpectedSourceSHAs); err != nil {
		return fmt.Errorf("revalidate current repair source evidence: %w", err)
	}
	if err := requireImmutableHistoricalWorktreeMergeSourcesWithRunner(ctx, run, refreshedCanonical, receipt); err != nil {
		return fmt.Errorf("revalidate immutable historical source evidence: %w", err)
	}
	fetched, err := fetchExactMergeTargetWithRunner(ctx, run, receipt.Candidate.Worktree, receipt.Target)
	if err != nil || fetched != currentTarget || fetched != options.ExpectedCurrentTargetSHA {
		return errors.New("remote target drifted during published forward-repair construction")
	}
	return nil
}

func validatePublishedForwardRepairCorrectionBinding(correction WorktreeMergeSelfSupersessionCorrection, receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession, receiptHash, claimHash, supersessionHash string) error {
	// The corrected replacement has its own recorded claim base. It may be
	// based on the current target, after the failed candidate's original base.
	if correction.ReceiptPath != receipt.ReceiptPath || correction.ReceiptSHA256 != receiptHash || correction.ImmutableClaimSHA256 != claimHash ||
		correction.SupersessionPath != supersession.AcknowledgementPath || correction.SupersessionSHA256 != supersessionHash ||
		correction.OriginalCandidate != receipt.Candidate || correction.OriginalClaimBaseSHA != supersession.OriginalClaimBaseSHA ||
		correction.CurrentTargetSHA != supersession.CurrentTargetSHA {
		return errors.New("correction does not retain immutable receipt, claim, supersession, candidate, base, or target evidence")
	}
	if correction.CorrectedReplacement.SHA == "" || correction.CorrectedReplacement.Task == "" || correction.CorrectedReplacement.Worktree == "" || correction.CorrectedReplacement.Branch == "" {
		return errors.New("correction has incomplete recorded replacement identity")
	}
	return nil
}

func writePublishedForwardRepairPrompt(receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession, currentTarget string, sources []WorktreeMergeSource, roots []WorktreeMergeValidationFailureSealRoot, actor, reason string) (string, error) {
	return writePublishedForwardRepairPromptInjected(receipt, supersession, currentTarget, sources, roots, actor, reason, nil)
}

// writePublishedForwardRepairPromptInjected is
// writePublishedForwardRepairPrompt's test seam (task-9 PR-9): every
// production call site reaches it only through
// writePublishedForwardRepairPrompt, which always passes a nil
// *filewrite.Injector, so production behaviour is unchanged. A test passes
// its own Injector to reach the scratch prompt file's create/chmod/write/
// close failure branches deterministically.
func writePublishedForwardRepairPromptInjected(receipt WorktreeMergeReceipt, supersession WorktreeMergeValidationFailureSupersession, currentTarget string, sources []WorktreeMergeSource, roots []WorktreeMergeValidationFailureSealRoot, actor, reason string, inj *filewrite.Injector) (string, error) {
	var body strings.Builder
	fmt.Fprintf(&body, "WB prepares one explicit published forward-repair candidate for failed receipt %s.\n", receipt.ReceiptPath)
	fmt.Fprintf(&body, "Target: %s@%s; historical self-supersession: %s.\n", receipt.Target, currentTarget, supersession.AcknowledgementPath)
	for _, source := range sources {
		fmt.Fprintf(&body, "- current source %s %s\n", source.Branch, source.SHA)
	}
	for _, root := range roots {
		fmt.Fprintf(&body, "- immutable root %s %s\n", root.Kind, root.SHA)
	}
	fmt.Fprintf(&body, "Actor: %s\nReason: %s\n", actor, reason)
	path, err := writeWorktreeMergeScratchPromptInjected("wb-published-forward-repair-prompt-*.txt", body.String(), inj)
	if err != nil {
		return "", err
	}
	return filepath.Clean(path), nil
}
