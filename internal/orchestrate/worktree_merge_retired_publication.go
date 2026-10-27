package orchestrate

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/runner"
	unix "github.com/sneat-dev/wb/internal/unixcompat"
	"github.com/sneat-dev/wb/internal/worktreeproof"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const (
	// One persisted schema covers both historical acknowledgements written
	// before retirement binding existed and current ones: the retirement
	// adjunct is optional evidence inside the same record, never a schema
	// version, so an old record keeps its exact bytes, ID and digest input.
	worktreeMergeRetiredPublicationAcknowledgementSchemaVersion = 1
	worktreeMergeRetiredPublicationAcknowledgementSuffix        = ".retired-publication.ack.json"
)

// WorktreeMergeRetiredPublicationAcknowledgement is a separate, append-only
// acknowledgement for a prepare- or land-phase conflict/validation_failed/
// checks_failed receipt whose exact published pull request was later retired
// -- closed without ever merging, and its remote candidate branch deleted --
// after the receipted target advanced past the candidate, leaving WB's own
// "refusing to rewrite the published branch without force-push" refusal (or
// an equivalent validation/checks failure) as a dead end: resume repeats the
// same refusal forever, and every other conflict-recovery verb requires an
// unpublished receipt. It proves, using a fresh read of GitHub's own PR state
// and a freshly fetched current remote target, that the publication is
// genuinely gone and nothing from it landed, then records a separate audited
// acknowledgement so a fresh candidate can own the lane. It never rewrites
// the historical receipt or any Work Log, and never deletes the preserved
// candidate worktree.
type WorktreeMergeRetiredPublicationAcknowledgement struct {
	SchemaVersion         int                   `json:"schema_version"`
	ID                    string                `json:"id"`
	Status                string                `json:"status"`
	ReceiptPath           string                `json:"receipt_path"`
	AcknowledgementPath   string                `json:"acknowledgement_path"`
	ReceiptID             string                `json:"receipt_id"`
	ReceiptSHA256         string                `json:"receipt_sha256"`
	ReceiptPhase          WorktreeMergePhase    `json:"receipt_phase"`
	ReceiptStatus         WorktreeMergeStatus   `json:"receipt_status"`
	Lane                  string                `json:"lane"`
	Repository            string                `json:"repository"`
	Target                string                `json:"target"`
	ReceiptTargetSHA      string                `json:"receipt_target_sha"`
	CurrentTargetSHA      string                `json:"current_target_sha"`
	CandidateTask         string                `json:"candidate_task"`
	CandidateWorktree     string                `json:"candidate_worktree"`
	CandidateBranch       string                `json:"candidate_branch"`
	CandidateSHA          string                `json:"candidate_sha,omitempty"`
	PublishedCandidateSHA string                `json:"published_candidate_sha,omitempty"`
	PullRequest           string                `json:"pull_request"`
	PullRequestState      string                `json:"pull_request_state"`
	PullRequestClosedAt   string                `json:"pull_request_closed_at,omitempty"`
	PullRequestHeadSHA    string                `json:"pull_request_head_sha"`
	Sources               []WorktreeMergeSource `json:"sources"`
	// Retirement evidence is present only when the candidate checkout had
	// already been removed by the supported preserved-retirement transaction.
	// It binds the acknowledgement to that exact retirement receipt and proof;
	// older acknowledgements omit every field and keep their original ID.
	RetirementReceiptPath   string    `json:"retirement_receipt_path,omitempty"`
	RetirementReceiptSHA256 string    `json:"retirement_receipt_sha256,omitempty"`
	RetirementProofSHA256   string    `json:"retirement_proof_sha256,omitempty"`
	RetirementCanonical     string    `json:"retirement_canonical,omitempty"`
	RetirementSourceSHA     string    `json:"retirement_source_sha,omitempty"`
	RetirementRetiredRef    string    `json:"retirement_retired_ref,omitempty"`
	RetirementArchiveRef    string    `json:"retirement_archive_ref,omitempty"`
	RetirementArchiveSHA    string    `json:"retirement_archive_sha,omitempty"`
	RetirementPhase         string    `json:"retirement_phase,omitempty"`
	Actor                   string    `json:"actor"`
	Reason                  string    `json:"reason"`
	RecordedAt              time.Time `json:"recorded_at"`
}

// WorktreeMergeRetiredPublicationAcknowledgementOptions configures
// AcknowledgeRetiredPublication.
type WorktreeMergeRetiredPublicationAcknowledgementOptions struct {
	ProjectsRoot string
	Receipt      string
	Apply        bool
	Actor        string
	Reason       string
}

// AcknowledgeRetiredPublication proves, from a fresh read of GitHub's own
// pull-request state and a freshly fetched current remote target, that a
// conflict/validation_failed/checks_failed receipt's exact published pull
// request was closed without ever merging, that its candidate branch carries
// no remote ref any more, and that neither its published candidate nor its
// preserved candidate landed on the target -- then records a separate
// audited acknowledgement so a fresh candidate can own the merger lane. It
// never rewrites the historical receipt or any Work Log, and never deletes
// the preserved candidate worktree. A candidate checkout that still exists is
// resolved against its own remote; one that the supported preserved-retirement
// transaction already removed is accepted only through that transaction's
// exact retirement receipt, freshly verified preserved source, private
// archive, and original-branch deletion proof, and then resolved against the
// corroborated canonical clone. This is a dry-run by default; --apply requires
// --actor and --reason and writes only the new acknowledgement artifact.
func AcknowledgeRetiredPublication(ctx context.Context, options WorktreeMergeRetiredPublicationAcknowledgementOptions) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	return acknowledgeRetiredPublication(ctx, options, productionRetiredPublicationPorts())
}

// heldRetiredCheckout is the narrow capability returned by the held
// preserved-retirement proof: the immutable evidence plus the canonical
// repository identity every later Git read and the final acknowledgement
// authorization must run against. Production binds
// worktrees.ProveRetiredCheckoutHeld; no arbitrary path fallback is exposed.
type heldRetiredCheckout interface {
	Evidence() worktrees.RetiredCheckoutEvidence
	Git(context.Context, ...string) (string, error)
	Validate() error
	Close() error
}

// retiredPublicationPorts are one acknowledgement's durable boundaries,
// scoped so the exact retirement proof, pull-request reader, receipt snapshot
// digest and persistence can be exercised without package globals.
type retiredPublicationPorts struct {
	run             runner.Runner
	readReceipt     func(string) (WorktreeMergeReceipt, string, error)
	persist         func(*publicationNamespace, string, WorktreeMergeRetiredPublicationAcknowledgement) error
	readPR          func(context.Context, string, ...string) (string, error)
	proveRetirement func(context.Context, worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error)
	archiveRemote   string
	inspect         worktrees.RetiredArchiveInspector
	beforePersist   func() error
}

func productionRetiredPublicationPorts() retiredPublicationPorts {
	return retiredPublicationPorts{
		run: defaultRunner, readReceipt: readWorktreeMergeReceiptSnapshot,
		persist: persistRetiredPublicationAcknowledgementBound, readPR: githubRead,
		proveRetirement: func(ctx context.Context, options worktrees.RetiredCheckoutProofOptions) (heldRetiredCheckout, error) {
			return worktrees.ProveRetiredCheckoutHeld(ctx, options)
		},
	}
}

type candidateCheckoutState int

const (
	candidateCheckoutPresent candidateCheckoutState = iota
	candidateCheckoutAbsent
	candidateCheckoutInvalid
)

// observeCandidateCheckout distinguishes a real linked checkout directory from
// an absent path, a symlink, or any other replacement. Only an exactly absent
// path may enter the preserved-retirement proof.
func observeCandidateCheckout(path string) (candidateCheckoutState, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return candidateCheckoutAbsent, nil
	}
	if err != nil {
		return candidateCheckoutInvalid, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return candidateCheckoutInvalid, nil
	}
	return candidateCheckoutPresent, nil
}

func acknowledgeRetiredPublication(ctx context.Context, options WorktreeMergeRetiredPublicationAcknowledgementOptions, ports retiredPublicationPorts) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	receiptPath, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	receipt, _, err := ports.readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if err := validateRetiredPublicationReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lockID := receipt.Lane
	lock, err := AcquireOperationLock(options.ProjectsRoot, lockID, true)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()

	// Re-read and re-validate beneath the lane lock: every proof below must
	// run against evidence observed while this acknowledgement exclusively
	// owns the lane, never against values captured before it. The digest comes
	// from the same bounded snapshot that was decoded and validated.
	receipt, receiptHash, err := ports.readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if err := validateRetiredPublicationReceipt(receipt, receiptPath); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	// Admit the acknowledgement's parent namespace once, beneath the lane
	// lock, and keep that descriptor authoritative through every proof, the
	// final receipt fence, publication/link/sync and any collision reread.
	// A parent or ancestor swap after this point refuses instead of being
	// accepted as a detached success.
	ackPath := retiredPublicationAcknowledgementPath(receiptPath)
	namespace, err := openPublicationNamespace(ackPath, false)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	defer func() { _ = namespace.Close() }()
	candidateState, err := observeCandidateCheckout(receipt.Candidate.Worktree)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("inspect candidate worktree %s: %w", receipt.Candidate.Worktree, err)
	}
	if candidateState == candidateCheckoutInvalid {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate worktree %s is required for read-only git object resolution and is not a real directory", receipt.Candidate.Worktree)
	}
	gitRoot := receipt.Candidate.Worktree
	var retirement heldRetiredCheckout
	var retiredEvidence worktrees.RetiredCheckoutEvidence
	if candidateState == candidateCheckoutAbsent {
		proof, proofErr := ports.proveRetirement(ctx, worktrees.RetiredCheckoutProofOptions{
			ProjectsRoot: options.ProjectsRoot, Task: receipt.Candidate.Task, Repository: receipt.Repository,
			CandidateWorktree: receipt.Candidate.Worktree, CandidateBranch: receipt.Candidate.Branch,
			CandidateSHA: receipt.Candidate.SHA, PublishedCandidateSHA: receipt.PublishedCandidateSHA,
			ArchiveRemote: ports.archiveRemote, Inspect: ports.inspect,
		})
		if proofErr != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate worktree %s is absent without a supported preserved retirement: %w", receipt.Candidate.Worktree, proofErr)
		}
		retirement = proof
		defer func() { _ = retirement.Close() }()
		retiredEvidence = proof.Evidence()
		gitRoot = retiredEvidence.Canonical
	}

	view, err := proveRetiredPullRequest(ctx, receipt, ports.readPR)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	remote, err := retiredPublicationGit(ctx, ports.run, retirement, gitRoot, "ls-remote", "--heads", "origin", "refs/heads/"+receipt.Candidate.Branch)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("inspect candidate publication state: %w", err)
	}
	if strings.TrimSpace(remote) != "" {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate branch %s still carries a remote ref; this recovery requires it to be gone", receipt.Candidate.Branch)
	}
	currentTarget, err := fetchRetiredMergeTarget(ctx, ports.run, retirement, gitRoot, receipt.Target)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	candidates := []string{receipt.PublishedCandidateSHA, receipt.Candidate.SHA}
	if retirement != nil {
		candidates = append(candidates, retiredEvidence.Retirement.SourceSHA)
	}
	for _, sha := range candidates {
		if sha == "" {
			continue
		}
		landed, ancestorErr := retiredMergeAncestor(ctx, ports.run, retirement, gitRoot, sha, currentTarget)
		if ancestorErr != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("verify candidate %s against current target %s: %w", sha, currentTarget, ancestorErr)
		}
		if landed {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate %s is already reachable from current target %s; the retired publication still landed, use acknowledge-stranded-landing or resume instead", sha, currentTarget)
		}
	}

	ack := WorktreeMergeRetiredPublicationAcknowledgement{
		SchemaVersion: worktreeMergeRetiredPublicationAcknowledgementSchemaVersion, Status: "retired_publication_acknowledged",
		ReceiptPath: receiptPath, AcknowledgementPath: ackPath,
		ReceiptID: receipt.ID, ReceiptSHA256: receiptHash, ReceiptPhase: receipt.Phase, ReceiptStatus: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, ReceiptTargetSHA: receipt.TargetSHA, CurrentTargetSHA: currentTarget,
		CandidateTask: receipt.Candidate.Task, CandidateWorktree: receipt.Candidate.Worktree, CandidateBranch: receipt.Candidate.Branch,
		CandidateSHA: receipt.Candidate.SHA, PublishedCandidateSHA: receipt.PublishedCandidateSHA,
		PullRequest: receipt.PullRequest, PullRequestState: view.State, PullRequestClosedAt: view.ClosedAt, PullRequestHeadSHA: view.HeadRefOID,
		Sources: append([]WorktreeMergeSource(nil), receipt.Sources...),
		Actor:   strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC(),
	}
	if retirement != nil {
		ack.RetirementReceiptPath = retiredEvidence.ReceiptPath
		ack.RetirementReceiptSHA256 = retiredEvidence.ReceiptSHA256
		ack.RetirementProofSHA256 = retiredEvidence.ProofSHA256
		ack.RetirementCanonical = retiredEvidence.Canonical
		ack.RetirementSourceSHA = retiredEvidence.Retirement.SourceSHA
		ack.RetirementRetiredRef = retiredEvidence.Retirement.RetiredRef
		ack.RetirementArchiveRef = retiredEvidence.Retirement.ArchiveRef
		ack.RetirementArchiveSHA = retiredEvidence.Retirement.ArchiveSHA
		ack.RetirementPhase = retiredEvidence.Retirement.Phase
	}
	ack.ID = retiredPublicationAcknowledgementID(ack)
	if existing, readErr := readRetiredPublicationAcknowledgementInNamespace(ackPath, receipt, receiptHash, namespace); readErr == nil {
		return resolveExistingRetiredPublicationAcknowledgement(existing, ack, ackPath)
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if ports.beforePersist != nil {
		if err := ports.beforePersist(); err != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, err
		}
	}
	finalState, err := observeCandidateCheckout(receipt.Candidate.Worktree)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("recheck candidate worktree %s: %w", receipt.Candidate.Worktree, err)
	}
	if finalState != candidateState {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("candidate worktree %s changed while proving retirement", receipt.Candidate.Worktree)
	}
	// Final exact-receipt fence: the merger receipt must still be the very
	// snapshot whose digest the acknowledgement binds, and the held retirement
	// session must still carry the proven canonical identity and retirement
	// receipt bytes. A detached path hash or a later re-open is not authority.
	finalReceipt, finalHash, err := ports.readReceipt(receiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if finalHash != receiptHash {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("merge receipt %s changed while proving retired publication", receiptPath)
	}
	if err := validateRetiredPublicationReceipt(finalReceipt, receiptPath); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if retirement != nil {
		if err := retirement.Validate(); err != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retirement evidence changed while proving retired publication: %w", err)
		}
	}
	if err := namespace.verify(); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("acknowledgement parent changed before publication: %w", err)
	}
	if err := ports.persist(namespace, ackPath, ack); err != nil {
		if !errors.Is(err, errRetiredPublicationAcknowledgementExists) {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, err
		}
		// Another publisher won the no-replace link. Reread the exact immutable
		// evidence through the same admitted namespace: identical valid evidence
		// is idempotent, anything conflicting or malformed refuses without
		// touching the existing bytes.
		existing, readErr := readRetiredPublicationAcknowledgementInNamespace(ackPath, receipt, receiptHash, namespace)
		if readErr != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, readErr
		}
		return resolveExistingRetiredPublicationAcknowledgement(existing, ack, ackPath)
	}
	return ack, nil
}

// resolveExistingRetiredPublicationAcknowledgement keeps the one persisted
// schema idempotent. The historical writer recorded the same immutable proof
// without a retirement adjunct; a current absent-candidate proof carries that
// adjunct. Compatibility therefore compares a newly proved record with only
// its own retirement adjunct omitted, and only when the existing bytes carry
// no adjunct at all. Every older immutable field — receipt, lane, repository,
// target, candidate, pull request, sources and the current target that the
// original writer hashed — must still match exactly, so an absent field is
// never permission for arbitrary changed proof and a still-present candidate
// record stays bound to its own immutable proof. The new record is compared,
// never rewritten: the caller still persists or returns the existing bytes.
func resolveExistingRetiredPublicationAcknowledgement(existing, ack WorktreeMergeRetiredPublicationAcknowledgement, ackPath string) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	if !retiredPublicationHasRetirementEvidence(existing) {
		comparable := ack
		clearRetiredPublicationRetirementEvidence(&comparable)
		if !sameRetiredPublicationAcknowledgement(existing, comparable) {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retired-publication acknowledgement %s binds different immutable evidence", ackPath)
		}
		return existing, nil
	}
	if !sameRetiredPublicationAcknowledgement(existing, ack) {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retired-publication acknowledgement %s binds different immutable evidence", ackPath)
	}
	return existing, nil
}

// clearRetiredPublicationRetirementEvidence blanks only the retirement-bound
// adjunct on a copy used for comparison. Every older immutable field stays
// available, and no persisted record is ever stripped of its own proof: a
// newly written absent-candidate acknowledgement always keeps and hashes the
// complete adjunct.
func clearRetiredPublicationRetirementEvidence(ack *WorktreeMergeRetiredPublicationAcknowledgement) {
	ack.RetirementReceiptPath = ""
	ack.RetirementReceiptSHA256 = ""
	ack.RetirementProofSHA256 = ""
	ack.RetirementCanonical = ""
	ack.RetirementSourceSHA = ""
	ack.RetirementRetiredRef = ""
	ack.RetirementArchiveRef = ""
	ack.RetirementArchiveSHA = ""
	ack.RetirementPhase = ""
}

// retiredPublicationGit runs one Git query either through the held retirement
// session's descriptor-bound canonical identity or, for a still-present
// candidate, through the existing runner at that checkout path.
func retiredPublicationGit(ctx context.Context, run runner.Runner, held heldRetiredCheckout, path string, args ...string) (string, error) {
	if held != nil {
		return held.Git(ctx, args...)
	}
	output, _, err := runCommand(ctx, run, 0, 0, path, "git", args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func fetchRetiredMergeTarget(ctx context.Context, run runner.Runner, held heldRetiredCheckout, path, target string) (string, error) {
	refspec := "+refs/heads/" + target + ":refs/remotes/origin/" + target
	if _, err := retiredPublicationGit(ctx, run, held, path, "fetch", "--no-tags", "origin", refspec); err != nil {
		return "", fmt.Errorf("fetch exact remote target %s: %w", target, err)
	}
	output, err := retiredPublicationGit(ctx, run, held, path, "rev-parse", "--verify", "refs/remotes/origin/"+target+"^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(output), nil
}

func retiredMergeAncestor(ctx context.Context, run runner.Runner, held heldRetiredCheckout, path, ancestor, descendant string) (bool, error) {
	output, err := retiredPublicationGit(ctx, run, held, path, "merge-base", ancestor, descendant)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(output) == ancestor, nil
}

// validateRetiredPublicationReceipt narrowly scopes eligibility to a receipt
// that once published a candidate and then failed non-terminally without
// ever landing: prepare or land phase, conflict/validation_failed/
// checks_failed status, no recorded landing SHA, and an exact published pull
// request. A receipt that never published anything is
// acknowledge-absorbed-conflict's or supersede-validation-failed's territory
// instead; one that recorded a landing SHA belongs to
// acknowledge-landed-failed.
func validateRetiredPublicationReceipt(receipt WorktreeMergeReceipt, receiptPath string) error {
	if receipt.ReceiptPath != receiptPath || receipt.ID == "" || receipt.Lane == "" || receipt.Lane != worktreeMergeLaneID(receipt.Repository, receipt.Target) {
		return fmt.Errorf("receipt %s has inconsistent immutable receipt identity", receiptPath)
	}
	if receipt.Phase != WorktreeMergePhasePrepare && receipt.Phase != WorktreeMergePhaseLand {
		return fmt.Errorf("receipt %s is phase %s, want prepare or land", receiptPath, receipt.Phase)
	}
	switch receipt.Status {
	case WorktreeMergeConflict, WorktreeMergeValidationFailed, WorktreeMergeChecksFailed:
	default:
		return fmt.Errorf("receipt %s is %s, want conflict, validation_failed, or checks_failed", receiptPath, receipt.Status)
	}
	if receipt.LandingSHA != "" {
		return fmt.Errorf("receipt %s already recorded a landing SHA %s; use acknowledge-landed-failed instead", receiptPath, receipt.LandingSHA)
	}
	if receipt.PullRequest == "" {
		return fmt.Errorf("receipt %s has no published pull request to prove retired", receiptPath)
	}
	if receipt.Repository == "" || receipt.Target == "" || receipt.TargetSHA == "" {
		return fmt.Errorf("receipt %s lacks complete immutable repository or target identity", receiptPath)
	}
	if receipt.Candidate.Task == "" || receipt.Candidate.Worktree == "" || receipt.Candidate.Branch == "" {
		return fmt.Errorf("receipt %s lacks complete immutable candidate identity", receiptPath)
	}
	if receipt.PublishedCandidateSHA == "" && receipt.Candidate.SHA == "" {
		return fmt.Errorf("receipt %s has no published or preserved candidate SHA to prove unlanded", receiptPath)
	}
	if len(receipt.Sources) == 0 {
		return fmt.Errorf("receipt %s has no receipted sources", receiptPath)
	}
	for _, source := range receipt.Sources {
		if source.Task == "" || source.Worktree == "" || source.Branch == "" || source.SHA == "" {
			return fmt.Errorf("receipt %s has an incomplete immutable source identity", receiptPath)
		}
	}
	return nil
}

type retiredPullRequestView struct {
	State       string `json:"state"`
	ClosedAt    string `json:"closedAt"`
	MergedAt    string `json:"mergedAt"`
	HeadRefName string `json:"headRefName"`
	HeadRefOID  string `json:"headRefOid"`
	MergeCommit struct {
		OID string `json:"oid"`
	} `json:"mergeCommit"`
}

// proveRetiredPullRequest reads the receipted pull request fresh from GitHub
// and proves it was retired: closed without ever merging. A pull request
// that reports MERGED points the caller at acknowledge-stranded-landing
// instead; one that is still OPEN, or any other non-CLOSED state, refuses.
func proveRetiredPullRequest(ctx context.Context, receipt WorktreeMergeReceipt, readPR func(context.Context, string, ...string) (string, error)) (retiredPullRequestView, error) {
	output, err := readPR(ctx, "", "pr", "view", receipt.PullRequest, "--repo", receipt.Repository,
		"--json", "state,closedAt,mergedAt,mergeCommit,headRefName,headRefOid")
	if err != nil {
		return retiredPullRequestView{}, fmt.Errorf("read pull-request retirement state: %w", err)
	}
	var view retiredPullRequestView
	if jsonErr := json.Unmarshal([]byte(output), &view); jsonErr != nil {
		return retiredPullRequestView{}, fmt.Errorf("decode pull-request retirement state: %w", jsonErr)
	}
	if view.State == "MERGED" || view.MergeCommit.OID != "" || view.MergedAt != "" {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s is MERGED; use acknowledge-stranded-landing instead", receipt.PullRequest)
	}
	if view.State != "CLOSED" {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s is %s, want CLOSED without a merge commit", receipt.PullRequest, view.State)
	}
	if view.HeadRefName != receipt.Candidate.Branch {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s head branch %s does not match exact receipted candidate branch %s", receipt.PullRequest, view.HeadRefName, receipt.Candidate.Branch)
	}
	expectedHead := receipt.PublishedCandidateSHA
	if expectedHead == "" {
		expectedHead = receipt.Candidate.SHA
	}
	if view.HeadRefOID == "" || view.HeadRefOID != expectedHead {
		return retiredPullRequestView{}, fmt.Errorf("pull request %s head %s does not match exact receipted candidate SHA %s", receipt.PullRequest, view.HeadRefOID, expectedHead)
	}
	return view, nil
}

func retiredPublicationAcknowledgementPath(receiptPath string) string {
	return receiptPath + worktreeMergeRetiredPublicationAcknowledgementSuffix
}

func retiredPublicationAcknowledgementID(ack WorktreeMergeRetiredPublicationAcknowledgement) string {
	hash := sha256.New()
	for _, value := range []string{
		ack.ReceiptID, ack.ReceiptPath, ack.ReceiptSHA256, string(ack.ReceiptPhase), string(ack.ReceiptStatus), ack.Lane, ack.Repository, ack.Target,
		ack.ReceiptTargetSHA, ack.CurrentTargetSHA, ack.CandidateTask, ack.CandidateWorktree, ack.CandidateBranch, ack.CandidateSHA, ack.PublishedCandidateSHA,
		ack.PullRequest, ack.PullRequestState, ack.PullRequestClosedAt, ack.PullRequestHeadSHA,
		ack.Actor, ack.Reason,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	for _, source := range ack.Sources {
		for _, value := range []string{source.Task, source.Worktree, source.Branch, source.SHA} {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	if retiredPublicationHasRetirementEvidence(ack) {
		for _, value := range retiredPublicationRetirementEvidence(ack) {
			_, _ = hash.Write([]byte(value))
			_, _ = hash.Write([]byte{0})
		}
	}
	return hex.EncodeToString(hash.Sum(nil))
}

func retiredPublicationRetirementEvidence(ack WorktreeMergeRetiredPublicationAcknowledgement) []string {
	return []string{
		ack.RetirementReceiptPath, ack.RetirementReceiptSHA256, ack.RetirementProofSHA256, ack.RetirementCanonical,
		ack.RetirementSourceSHA, ack.RetirementRetiredRef, ack.RetirementArchiveRef, ack.RetirementArchiveSHA, ack.RetirementPhase,
	}
}

func retiredPublicationHasRetirementEvidence(ack WorktreeMergeRetiredPublicationAcknowledgement) bool {
	for _, value := range retiredPublicationRetirementEvidence(ack) {
		if value != "" {
			return true
		}
	}
	return false
}

// sameRetiredPublicationAcknowledgement compares only the immutable proof
// evidence, deliberately excluding Actor/Reason/ID/RecordedAt: a retry that
// repeats the same receipt, target, and pull-request proof is idempotent even
// when the operator supplies a different actor or reason on the replay.
func sameRetiredPublicationAcknowledgement(left, right WorktreeMergeRetiredPublicationAcknowledgement) bool {
	return left.ReceiptPath == right.ReceiptPath && left.AcknowledgementPath == right.AcknowledgementPath &&
		left.ReceiptID == right.ReceiptID && left.ReceiptSHA256 == right.ReceiptSHA256 &&
		left.ReceiptPhase == right.ReceiptPhase && left.ReceiptStatus == right.ReceiptStatus &&
		left.Lane == right.Lane && left.Repository == right.Repository && left.Target == right.Target &&
		left.ReceiptTargetSHA == right.ReceiptTargetSHA && left.CurrentTargetSHA == right.CurrentTargetSHA &&
		left.CandidateTask == right.CandidateTask && left.CandidateWorktree == right.CandidateWorktree &&
		left.CandidateBranch == right.CandidateBranch && left.CandidateSHA == right.CandidateSHA &&
		left.PublishedCandidateSHA == right.PublishedCandidateSHA &&
		left.PullRequest == right.PullRequest && left.PullRequestState == right.PullRequestState &&
		left.PullRequestClosedAt == right.PullRequestClosedAt && left.PullRequestHeadSHA == right.PullRequestHeadSHA &&
		sameRetiredPublicationRetirementEvidence(left, right) &&
		sameWorktreeMergeSources(left.Sources, right.Sources)
}

func sameRetiredPublicationRetirementEvidence(left, right WorktreeMergeRetiredPublicationAcknowledgement) bool {
	leftEvidence, rightEvidence := retiredPublicationRetirementEvidence(left), retiredPublicationRetirementEvidence(right)
	for index := range leftEvidence {
		if leftEvidence[index] != rightEvidence[index] {
			return false
		}
	}
	return true
}

// errRetiredPublicationAcknowledgementExists reports that an immutable
// acknowledgement already occupies the destination name. The caller must
// reread and authenticate those exact bytes instead of overwriting them.
var errRetiredPublicationAcknowledgementExists = fmt.Errorf("retired-publication acknowledgement already exists: %w", os.ErrExist)

func persistRetiredPublicationAcknowledgement(path string, ack WorktreeMergeRetiredPublicationAcknowledgement) error {
	return persistRetiredPublicationAcknowledgementInjected(path, ack, nil)
}

// persistRetiredPublicationAcknowledgementInjected stages the complete audit
// bytes under a held parent directory and publishes them with an atomic
// no-replace hard link. An acknowledgement appearing after the caller's own
// existence check is never overwritten: the link fails and the caller rereads
// the exact existing evidence. The parent descriptor is held across staging,
// publication and the directory sync, and the lexical parent path is
// re-corroborated at each of those boundaries, so a parent-path swap refuses
// instead of publishing only into a detached renamed directory.
func persistRetiredPublicationAcknowledgementInjected(path string, ack WorktreeMergeRetiredPublicationAcknowledgement, inj *filewrite.Injector) error {
	contents, err := marshalRetiredPublicationAcknowledgement(ack)
	if err != nil {
		return err
	}
	namespace, err := openPublicationNamespace(path, true)
	if err != nil {
		return err
	}
	defer func() { _ = namespace.Close() }()
	return persistRetiredPublicationAcknowledgementBytes(namespace, path, contents, inj)
}

// persistRetiredPublicationAcknowledgementBound is the production port: it
// publishes only through the namespace the acknowledgement flow admitted from
// the receipt evidence.
func persistRetiredPublicationAcknowledgementBound(namespace *publicationNamespace, path string, ack WorktreeMergeRetiredPublicationAcknowledgement) error {
	contents, err := marshalRetiredPublicationAcknowledgement(ack)
	if err != nil {
		return err
	}
	if namespace == nil {
		// A caller without an already-admitted namespace still goes through the
		// same verified open; this is never an unchecked path write or a
		// detached-namespace substitute for the acknowledgement flow.
		return persistRetiredPublicationAcknowledgement(path, ack)
	}
	return persistRetiredPublicationAcknowledgementBytes(namespace, path, contents, nil)
}

func marshalRetiredPublicationAcknowledgement(ack WorktreeMergeRetiredPublicationAcknowledgement) ([]byte, error) {
	contents, err := json.MarshalIndent(ack, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(contents, '\n'), nil
}

func persistRetiredPublicationAcknowledgementBytes(namespace *publicationNamespace, path string, contents []byte, inj *filewrite.Injector) error {
	if namespace == nil {
		return fmt.Errorf("acknowledgement parent namespace is unavailable")
	}
	if filepath.Clean(namespace.path) != filepath.Dir(path) {
		return fmt.Errorf("acknowledgement path %s is outside its admitted namespace %s", path, namespace.path)
	}
	if err := namespace.verify(); err != nil {
		return err
	}
	name := ".retired-publication-ack-" + rand.Text() + ".tmp"
	fd, err := filewrite.CreateExclusive(int(namespace.directory.Fd()), name, 0o600, inj)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(fd), name)
	removeTemporary := func() { _ = unix.Unlinkat(int(namespace.directory.Fd()), name, 0) }
	defer removeTemporary()
	if err := filewrite.ChmodFile(file, 0o600, name, inj); err != nil {
		_ = file.Close()
		return err
	}
	if err := filewrite.Write(file, contents, name, inj); err != nil {
		_ = file.Close()
		return err
	}
	if err := filewrite.Sync(file, name, inj); err != nil {
		_ = file.Close()
		return err
	}
	if err := filewrite.Close(file, name, inj); err != nil {
		return err
	}
	if err := namespace.verify(); err != nil {
		return err
	}
	if err := filewrite.LinkNoReplace(int(namespace.directory.Fd()), name, filepath.Base(path), inj); err != nil {
		if errors.Is(err, unix.EEXIST) {
			return errRetiredPublicationAcknowledgementExists
		}
		return err
	}
	// The link may have captured a directory that was renamed away while the
	// syscall ran. Verify the bound path before touching the temp name again
	// so a detached namespace is refused, never acknowledged as success.
	if err := namespace.verify(); err != nil {
		return fmt.Errorf("acknowledgement published outside its bound namespace: %w", err)
	}
	removeTemporary()
	if err := filewrite.SyncDir(namespace.directory, inj); err != nil {
		return err
	}
	if err := namespace.verify(); err != nil {
		return fmt.Errorf("acknowledgement parent changed during directory sync: %w", err)
	}
	return nil
}

func readRetiredPublicationAcknowledgement(path string, receipt WorktreeMergeReceipt) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	// Refuse malformed sidecar bytes before requiring the historical receipt
	// digest: a mutated or truncated acknowledgement must keep the lane
	// blocked even when the receipt itself is gone or unreadable, rather than
	// being downgraded to "no acknowledgement" by a missing-file error.
	contents, err := readSecureBoundedRegularFile(path, maxRetiredPublicationEvidenceBytes)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if err := decodeMergeAcknowledgement(contents, path, "retired-publication acknowledgement", new(WorktreeMergeRetiredPublicationAcknowledgement)); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	receiptHash, err := worktreeMergeReceiptSHA256(receipt.ReceiptPath)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	return readRetiredPublicationAcknowledgementWithReceiptHash(path, receipt, receiptHash)
}

// readRetiredPublicationAcknowledgementWithReceiptHash authenticates one
// acknowledgement through the same secure bounded byte read used for
// publication, and binds it to the receipt digest produced by the caller's own
// validated receipt snapshot rather than to a separate path hash.
func readRetiredPublicationAcknowledgementWithReceiptHash(path string, receipt WorktreeMergeReceipt, receiptHash string) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	namespace, err := openPublicationNamespace(path, false)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	defer func() { _ = namespace.Close() }()
	return readRetiredPublicationAcknowledgementInNamespace(path, receipt, receiptHash, namespace)
}

// readRetiredPublicationAcknowledgementInNamespace authenticates one
// acknowledgement through the namespace admitted from the receipt evidence,
// refusing when that namespace no longer binds the acknowledgement path.
func readRetiredPublicationAcknowledgementInNamespace(path string, receipt WorktreeMergeReceipt, receiptHash string, namespace *publicationNamespace) (WorktreeMergeRetiredPublicationAcknowledgement, error) {
	var ack WorktreeMergeRetiredPublicationAcknowledgement
	contents, err := readSecureBoundedRegularFileInNamespace(path, maxRetiredPublicationEvidenceBytes, namespace)
	if err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if err := decodeMergeAcknowledgement(contents, path, "retired-publication acknowledgement", &ack); err != nil {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, err
	}
	if ack.SchemaVersion != worktreeMergeRetiredPublicationAcknowledgementSchemaVersion || ack.Status != "retired_publication_acknowledged" ||
		ack.AcknowledgementPath != path || ack.ReceiptPath != receipt.ReceiptPath || ack.ReceiptID != receipt.ID || ack.ReceiptSHA256 != receiptHash ||
		ack.ReceiptPhase != receipt.Phase || ack.ReceiptStatus != receipt.Status || ack.Lane != receipt.Lane || ack.Repository != receipt.Repository || ack.Target != receipt.Target ||
		ack.ReceiptTargetSHA != receipt.TargetSHA || ack.CandidateTask != receipt.Candidate.Task || ack.CandidateWorktree != receipt.Candidate.Worktree ||
		ack.CandidateBranch != receipt.Candidate.Branch || ack.CandidateSHA != receipt.Candidate.SHA || ack.PublishedCandidateSHA != receipt.PublishedCandidateSHA ||
		ack.PullRequest != receipt.PullRequest || !sameWorktreeMergeSources(ack.Sources, receipt.Sources) ||
		ack.PullRequestState == "" || ack.CurrentTargetSHA == "" ||
		ack.Actor == "" || ack.Reason == "" || ack.RecordedAt.IsZero() || ack.ID != retiredPublicationAcknowledgementID(ack) {
		return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retired-publication acknowledgement %s has invalid immutable identity", path)
	}
	if retiredPublicationHasRetirementEvidence(ack) {
		if err := validateRetiredPublicationRetirementEvidence(ack); err != nil {
			return WorktreeMergeRetiredPublicationAcknowledgement{}, fmt.Errorf("retired-publication acknowledgement %s has invalid retirement evidence: %w", path, err)
		}
	}
	return ack, nil
}

func validateRetiredPublicationRetirementEvidence(ack WorktreeMergeRetiredPublicationAcknowledgement) error {
	for _, value := range retiredPublicationRetirementEvidence(ack) {
		if value == "" {
			return fmt.Errorf("retirement evidence is incomplete")
		}
	}
	if !filepath.IsAbs(ack.RetirementReceiptPath) || filepath.Clean(ack.RetirementReceiptPath) != ack.RetirementReceiptPath {
		return fmt.Errorf("retirement receipt path must be one clean absolute path")
	}
	if !filepath.IsAbs(ack.RetirementCanonical) || filepath.Clean(ack.RetirementCanonical) != ack.RetirementCanonical {
		return fmt.Errorf("retirement canonical clone must be one clean absolute path")
	}
	if !validRetirementDigest(ack.RetirementReceiptSHA256) || !validRetirementDigest(ack.RetirementProofSHA256) {
		return fmt.Errorf("retirement digests are invalid")
	}
	if !worktreeproof.IsGitObjectID(ack.RetirementSourceSHA) || !worktreeproof.IsGitObjectID(ack.RetirementArchiveSHA) {
		return fmt.Errorf("retirement source or archive object is invalid")
	}
	if !strings.HasPrefix(ack.RetirementRetiredRef, "retired/") || ack.RetirementArchiveRef == "" {
		return fmt.Errorf("retirement refs are invalid")
	}
	if ack.RetirementPhase != "complete" {
		return fmt.Errorf("retirement phase is %s, want complete", ack.RetirementPhase)
	}
	return nil
}

func validRetirementDigest(value string) bool {
	return len(value) == 64 && worktreeproof.HasOnlyLowerHexCharacters(value)
}

func hasRetiredPublicationAcknowledgement(receipt WorktreeMergeReceipt) (bool, error) {
	_, err := readRetiredPublicationAcknowledgement(retiredPublicationAcknowledgementPath(receipt.ReceiptPath), receipt)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// effectiveUnpublishedConflict reports whether receipt should be treated, for
// conflict-recovery eligibility purposes, as never having published a
// candidate: either its own PullRequest and PublishedCandidateSHA fields are
// genuinely both empty, or a valid retired-publication acknowledgement proves
// the sole publication this receipt ever recorded was later retired -- its
// pull request closed without merging and its remote candidate branch
// deleted -- with nothing from it landed on the current target. A malformed
// or absent acknowledgement never widens eligibility: this fails closed to
// "still published" whenever the sidecar cannot be authenticated.
func effectiveUnpublishedConflict(receipt WorktreeMergeReceipt) (bool, error) {
	if receipt.PullRequest == "" && receipt.PublishedCandidateSHA == "" {
		return true, nil
	}
	return hasRetiredPublicationAcknowledgement(receipt)
}
