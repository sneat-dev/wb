package worktrees

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreeretire"
)

// RetiredCheckoutEvidence is the read-only proof that one merge candidate's
// checkout was removed by the supported preserved-retirement transaction. It
// carries only immutable identity and digests; the retirement receipt stays
// the durable record.
type RetiredCheckoutEvidence struct {
	Retirement    worktreeretire.Transaction `json:"retirement"`
	ReceiptPath   string                     `json:"receipt_path"`
	ReceiptSHA256 string                     `json:"receipt_sha256"`
	ProofSHA256   string                     `json:"proof_sha256"`
	Canonical     string                     `json:"canonical"`
}

// RetiredCheckoutProof is the held result of one supported preserved-retirement
// proof: the immutable evidence plus the exact corroborated canonical
// repository's root and .git descriptors. Every later Git read and the final
// acknowledgement authorization must run through this session rather than
// through the mutable canonical path, so a root or .git replacement after the
// initial identity check refuses instead of silently resolving elsewhere.
type RetiredCheckoutProof struct {
	evidence     RetiredCheckoutEvidence
	receiptBytes []byte
	canonical    *canonicalRepository
	git          func(context.Context, *canonicalRepository, ...string) (string, error)
	readSnapshot func(string) (worktreeretire.ReportSnapshot, error)
}

// Evidence returns the immutable proof bound into a durable acknowledgement.
func (proof *RetiredCheckoutProof) Evidence() RetiredCheckoutEvidence {
	if proof == nil {
		return RetiredCheckoutEvidence{}
	}
	return proof.evidence
}

// Validate re-authorizes the held canonical repository and requires the exact
// retirement receipt bytes to still be the snapshot that was proven. It is the
// final fence before an acknowledgement is published.
func (proof *RetiredCheckoutProof) Validate() error {
	if proof == nil || proof.canonical == nil {
		return fmt.Errorf("retired checkout proof session is unavailable")
	}
	if err := proof.canonical.authorizeForGit(); err != nil {
		return err
	}
	snapshot, err := proof.readSnapshot(proof.evidence.ReceiptPath)
	if err != nil {
		return fmt.Errorf("re-read retirement receipt %s: %w", proof.evidence.ReceiptPath, err)
	}
	if snapshot.SHA256 != proof.evidence.ReceiptSHA256 || !bytes.Equal(snapshot.Bytes, proof.receiptBytes) {
		return fmt.Errorf("retirement receipt %s changed after it was proven", proof.evidence.ReceiptPath)
	}
	return nil
}

// Git runs one Git query against the held canonical repository. The retained
// descriptors authorize the child process, and both the path and the Git
// namespace are revalidated before and after the boundary.
func (proof *RetiredCheckoutProof) Git(ctx context.Context, args ...string) (string, error) {
	if proof == nil || proof.canonical == nil || proof.git == nil {
		return "", fmt.Errorf("retired checkout proof session is unavailable")
	}
	if err := proof.canonical.authorizeForGit(); err != nil {
		return "", err
	}
	output, err := proof.git(ctx, proof.canonical, args...)
	if validationErr := proof.canonical.validate(); validationErr != nil {
		return "", errors.Join(err, validationErr)
	}
	return output, err
}

// Close releases the held canonical descriptors. It is safe to call once the
// caller owns a successful proof; a failed proof closes its own descriptors.
func (proof *RetiredCheckoutProof) Close() error {
	if proof == nil || proof.canonical == nil {
		return nil
	}
	proof.canonical.close()
	proof.canonical = nil
	return nil
}

func (proof *RetiredCheckoutProof) remoteSHA(ctx context.Context, remote, ref string) (string, error) {
	output, err := proof.Git(ctx, "ls-remote", remote, ref)
	if err != nil {
		return "", fmt.Errorf("inspect remote ref %s: %w", ref, err)
	}
	return parseRetiredRemoteSHA(output, ref)
}

// RetiredCheckoutProofOptions identifies the exact receipted merge candidate
// whose removed checkout must be proven retired. ArchiveRemote and Inspect are
// replaceable only so bare-remote integration tests never contact GitHub.
type RetiredCheckoutProofOptions struct {
	ProjectsRoot          string
	Task                  string
	Repository            string
	CandidateWorktree     string
	CandidateBranch       string
	CandidateSHA          string
	PublishedCandidateSHA string
	ArchiveRemote         string
	Inspect               RetiredArchiveInspector
}

type retiredCheckoutProofPorts struct {
	home               func(string) (string, error)
	canonical          func(string, string) (string, error)
	readSnapshot       func(string) (worktreeretire.ReportSnapshot, error)
	openCanonical      func(string) (*canonicalRepository, error)
	afterCanonicalOpen func()
	archivePlan        func(context.Context, string, RetiredArchiveInspector) (RetiredArchivePlan, error)
	verifyRemoved      func(context.Context, worktreeretire.Operation, worktreeretire.Transaction) error
	git                func(context.Context, *canonicalRepository, ...string) (string, error)
	lstat              func(string) (os.FileInfo, error)
}

func productionRetiredCheckoutProofPorts() retiredCheckoutProofPorts {
	return retiredCheckoutProofPorts{
		home:          wbhome.Root,
		canonical:     CanonicalRepositoryPath,
		readSnapshot:  worktreeretire.ReadReportSnapshot,
		openCanonical: openCanonicalRepository,
		archivePlan:   PlanRetiredArchivePreflight,
		verifyRemoved: func(ctx context.Context, operation worktreeretire.Operation, result worktreeretire.Transaction) error {
			return operation.VerifyRemovedRemote(ctx, result)
		},
		git:   gitCanonical,
		lstat: os.Lstat,
	}
}

// ProveRetiredCheckoutHeld proves, from the deterministic retirement receipt
// for the receipted candidate task/repository, that a supported preserved
// retirement completed, and keeps the exact descriptor-bound canonical
// identity open through the caller's later Git reads and final
// acknowledgement publication.
func ProveRetiredCheckoutHeld(ctx context.Context, options RetiredCheckoutProofOptions) (*RetiredCheckoutProof, error) {
	return proveRetiredCheckoutHeldWith(ctx, options, productionRetiredCheckoutProofPorts())
}

func proveRetiredCheckoutHeldWith(ctx context.Context, options RetiredCheckoutProofOptions, ports retiredCheckoutProofPorts) (*RetiredCheckoutProof, error) {
	if options.Task == "" || options.Repository == "" || options.CandidateWorktree == "" || options.CandidateBranch == "" || options.CandidateSHA == "" {
		return nil, fmt.Errorf("retired checkout proof requires the exact candidate task, repository, worktree, branch, and source SHA")
	}
	if !filepath.IsAbs(options.CandidateWorktree) || filepath.Clean(options.CandidateWorktree) != options.CandidateWorktree {
		return nil, fmt.Errorf("retired candidate worktree must be one clean absolute path")
	}
	if !isGitObjectID(options.CandidateSHA) {
		return nil, fmt.Errorf("retired checkout proof requires one exact candidate commit object")
	}
	// The published head and the preserved candidate may legitimately differ
	// when the candidate advanced after publication: the acknowledgement's own
	// pull-request proof still requires the exact published head, and both
	// commit objects are verified below.
	if options.PublishedCandidateSHA != "" && !isGitObjectID(options.PublishedCandidateSHA) {
		return nil, fmt.Errorf("retired checkout proof requires one exact published candidate commit object")
	}
	if info, err := ports.lstat(options.CandidateWorktree); err == nil {
		return nil, fmt.Errorf("retired candidate checkout %s still exists as %s", options.CandidateWorktree, info.Mode())
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("inspect retired candidate checkout: %w", err)
	}
	home, err := ports.home(options.ProjectsRoot)
	if err != nil {
		return nil, err
	}
	canonical, err := ports.canonical(options.ProjectsRoot, options.Repository)
	if err != nil {
		return nil, err
	}
	path := worktreeretire.ReportPath(home, worktreeretire.Transaction{Task: options.Task, Repository: options.Repository})
	// One securely read, bounded snapshot supplies both the decoded receipt and
	// the digest that binds it. Hashing the path separately would let a
	// replacement between decode and hash label unvalidated bytes as proven.
	snapshot, err := ports.readSnapshot(path)
	if err != nil {
		return nil, fmt.Errorf("read retirement receipt %s: %w", path, err)
	}
	retirement := snapshot.Transaction
	initialReceiptSHA := snapshot.SHA256
	if err := worktreeretire.ValidateRemovedReceipt(retirement, path, options.Task, options.Repository); err != nil {
		return nil, err
	}
	if retirement.Phase != "complete" {
		return nil, fmt.Errorf("retirement receipt %s is phase %s, want complete", path, retirement.Phase)
	}
	if retirement.Task != options.Task || retirement.Repository != options.Repository ||
		retirement.Worktree != options.CandidateWorktree || retirement.Branch != options.CandidateBranch {
		return nil, fmt.Errorf("retirement receipt %s does not bind the exact receipted candidate identity", path)
	}
	if filepath.Clean(retirement.Canonical) != filepath.Clean(canonical) {
		return nil, fmt.Errorf("retirement receipt %s names canonical clone %s, want %s", path, retirement.Canonical, canonical)
	}
	if retirement.ArchiveSHA == "" || !isGitObjectID(retirement.ArchiveSHA) {
		return nil, fmt.Errorf("retirement receipt %s has no verified private archive object", path)
	}
	if retirement.ArchiveRepository == "" || !strings.HasPrefix(retirement.RetiredRef, "retired/") || retirement.ArchiveRef == "" {
		return nil, fmt.Errorf("retirement receipt %s has no usable preserved-source or private-archive identity", path)
	}
	if retirement.OriginalRemoteSHA != "" && retirement.DeleteIntentSHA != retirement.OriginalRemoteSHA {
		return nil, fmt.Errorf("retirement receipt %s has no exact original-branch deletion intent", path)
	}
	opened, err := ports.openCanonical(canonical)
	if err != nil {
		return nil, fmt.Errorf("open canonical clone %s: %w", canonical, err)
	}
	proof := &RetiredCheckoutProof{
		receiptBytes: snapshot.Bytes,
		canonical:    opened,
		git:          ports.git,
		readSnapshot: ports.readSnapshot,
	}
	success := false
	defer func() {
		if !success {
			_ = proof.Close()
		}
	}()
	// The canonical identity is validated immediately after admission and
	// again around every Git boundary so a test-observable root/.git swap is
	// refused by the same descriptors that proved the retirement.
	if err := proof.canonical.validate(); err != nil {
		return nil, fmt.Errorf("authorize canonical clone %s: %w", canonical, err)
	}
	if ports.afterCanonicalOpen != nil {
		ports.afterCanonicalOpen()
	}
	if err := proof.canonical.validate(); err != nil {
		return nil, fmt.Errorf("authorize canonical clone %s: %w", canonical, err)
	}

	plan, err := ports.archivePlan(ctx, retirement.Repository, options.Inspect)
	if err != nil {
		return nil, err
	}
	if plan.Outcome != "planned" || plan.ArchiveRepository != retirement.ArchiveRepository {
		return nil, fmt.Errorf("retirement private archive preflight no longer holds for %s", retirement.ArchiveRepository)
	}
	archiveRemote := options.ArchiveRemote
	if archiveRemote == "" {
		archiveRemote = "git@github.com:" + retirement.ArchiveRepository + ".git"
	}
	if _, err := gitremote.Parse(archiveRemote); err != nil {
		return nil, fmt.Errorf("retirement archive remote: %w", err)
	}
	// The preserved-source/private-archive read is one of the path-based Git
	// boundaries this session owns: bind its remote observations to the held
	// canonical descriptors instead of resolving the canonical path again.
	operation := worktreeretire.Operation{
		ArchiveRemote: archiveRemote,
		Remote: worktreeretire.TransactionPorts{
			RemoteSHA: func(ctx context.Context, _ string, remote, ref string) (string, error) {
				return proof.remoteSHA(ctx, remote, ref)
			},
		},
	}
	if err := ports.verifyRemoved(ctx, operation, retirement); err != nil {
		return nil, fmt.Errorf("retirement preserved-source proof: %w", err)
	}
	registered, err := proof.Git(ctx, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	if strings.Contains(registered, "worktree "+retirement.Worktree+"\n") {
		return nil, fmt.Errorf("retired checkout %s is still registered", retirement.Worktree)
	}
	branches, err := proof.Git(ctx, "for-each-ref", "--format=%(objectname)", "refs/heads/"+retirement.Branch)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(branches) != "" {
		return nil, fmt.Errorf("retired local branch %s still exists", retirement.Branch)
	}
	sourceRef := worktreeretire.SourceRef(retirement)
	if _, err := proof.Git(ctx, "fetch", "--no-tags", "origin", sourceRef); err != nil {
		return nil, fmt.Errorf("fetch retired source ref %s: %w", sourceRef, err)
	}
	fetched, err := proof.Git(ctx, "rev-parse", "FETCH_HEAD")
	if err != nil || fetched != retirement.SourceSHA {
		return nil, fmt.Errorf("fetched retired source %s does not bind source SHA %s: %w", sourceRef, retirement.SourceSHA, err)
	}
	if kind, err := proof.Git(ctx, "cat-file", "-t", retirement.SourceSHA); err != nil || kind != "commit" {
		return nil, fmt.Errorf("preserved retired source object %s is missing: %w", retirement.SourceSHA, err)
	}
	if kind, err := proof.Git(ctx, "cat-file", "-t", options.CandidateSHA); err != nil || kind != "commit" {
		return nil, fmt.Errorf("published candidate object %s is missing: %w", options.CandidateSHA, err)
	}
	if options.PublishedCandidateSHA != "" && options.PublishedCandidateSHA != options.CandidateSHA {
		if kind, err := proof.Git(ctx, "cat-file", "-t", options.PublishedCandidateSHA); err != nil || kind != "commit" {
			return nil, fmt.Errorf("published pull-request head object %s is missing: %w", options.PublishedCandidateSHA, err)
		}
	}
	if err := proveRetiredSourceBindsCandidate(ctx, proof, retirement, options.CandidateSHA); err != nil {
		return nil, err
	}
	finalSnapshot, err := ports.readSnapshot(path)
	if err != nil {
		return nil, fmt.Errorf("re-read retirement receipt %s: %w", path, err)
	}
	if finalSnapshot.SHA256 != initialReceiptSHA || !bytes.Equal(finalSnapshot.Bytes, snapshot.Bytes) {
		return nil, fmt.Errorf("retirement receipt %s changed while it was being proven", path)
	}
	evidence := RetiredCheckoutEvidence{Retirement: retirement, ReceiptPath: path, ReceiptSHA256: initialReceiptSHA, Canonical: canonical}
	evidence.ProofSHA256 = retiredCheckoutProofDigest(evidence, options.CandidateSHA)
	proof.evidence = evidence
	success = true
	return proof, nil
}

// proveRetiredSourceBindsCandidate requires the preserved source to BE the
// receipted candidate or to be WB's exact durable retirement-intent commit on
// top of it. Arbitrary ancestry or "same work" is never accepted.
func proveRetiredSourceBindsCandidate(ctx context.Context, proof *RetiredCheckoutProof, retirement worktreeretire.Transaction, candidateSHA string) error {
	if retirement.SourceSHA == candidateSHA {
		return nil
	}
	if retirement.IntentParentSHA != candidateSHA || retirement.IntentTreeSHA == "" || retirement.IntentMessage == "" || retirement.IntentAt.IsZero() {
		return fmt.Errorf("preserved source %s does not bind receipted candidate %s", retirement.SourceSHA, candidateSHA)
	}
	parent, err := proof.Git(ctx, "rev-parse", retirement.SourceSHA+"^")
	if err != nil || parent != candidateSHA {
		return fmt.Errorf("retirement intent parent does not match receipted candidate %s: %w", candidateSHA, err)
	}
	tree, err := proof.Git(ctx, "rev-parse", retirement.SourceSHA+"^{tree}")
	if err != nil || tree != retirement.IntentTreeSHA {
		return fmt.Errorf("retirement intent tree does not match durable intent: %w", err)
	}
	message, err := proof.Git(ctx, "log", "-1", "--format=%B", retirement.SourceSHA)
	if err != nil || strings.TrimSpace(message) != retirement.IntentMessage {
		return fmt.Errorf("retirement intent message does not match durable intent: %w", err)
	}
	return nil
}

func retiredCheckoutProofDigest(evidence RetiredCheckoutEvidence, candidateSHA string) string {
	retirement := evidence.Retirement
	hash := sha256.New()
	for _, value := range []string{
		retirement.Task, retirement.Repository, retirement.ArchiveRepository, retirement.Worktree, retirement.Canonical,
		retirement.Branch, retirement.Preserve, retirement.OriginalRemoteSHA, retirement.DeleteIntentSHA, retirement.SourceSHA,
		retirement.IntentParentSHA, retirement.IntentTreeSHA, retirement.IntentMessage, retirement.RetiredRef, retirement.ArchiveRef,
		retirement.ArchiveSHA, retirement.ClaimID, retirement.EffortID, retirement.RunID, retirement.Phase,
		candidateSHA, evidence.ReceiptPath, evidence.ReceiptSHA256, evidence.Canonical,
	} {
		_, _ = hash.Write([]byte(value))
		_, _ = hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
