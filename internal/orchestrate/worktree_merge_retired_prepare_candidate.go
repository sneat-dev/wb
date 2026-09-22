package orchestrate

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/retiredcandidateack"
)

type WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions struct {
	ProjectsRoot, Receipt, Actor, Reason string
	Apply                                bool
}

// AcknowledgeRetiredPrepareCandidate authorizes only retirement of the
// receipted candidate. In particular it neither proves nor records that a
// source landed, and it deliberately leaves the old merge lane untouched.
func AcknowledgeRetiredPrepareCandidate(ctx context.Context, options WorktreeMergeRetiredPrepareCandidateAcknowledgementOptions) (retiredcandidateack.Acknowledgement, error) {
	path, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	receipt, err := readWorktreeMergeReceipt(path)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	if err := validateRetiredPrepareCandidateReceipt(receipt, path); err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return retiredcandidateack.Acknowledgement{}, errors.New("--actor and --reason are required with --apply")
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	defer func() { _ = lock.Release() }()
	receipt, err = readWorktreeMergeReceipt(path)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	if err := validateRetiredPrepareCandidateReceipt(receipt, path); err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	ack, err := proveRetiredPrepareCandidate(ctx, path, receipt, options.Actor, options.Reason)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	// Re-observe every mutable ref/worktree fact immediately before a write.
	ack, err = proveRetiredPrepareCandidate(ctx, path, receipt, options.Actor, options.Reason)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	ackPath := retiredcandidateack.Path(path)
	if !options.Apply {
		return ack, nil
	}
	if existing, loadErr := retiredcandidateack.Load(ackPath, retiredPrepareCandidateIdentity(receipt, path)); loadErr == nil {
		return existing, nil
	} else if !errors.Is(loadErr, os.ErrNotExist) {
		return retiredcandidateack.Acknowledgement{}, loadErr
	}
	if err := retiredcandidateack.Persist(ackPath, ack); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return retiredcandidateack.Acknowledgement{}, err
		}
		return retiredcandidateack.Load(ackPath, retiredPrepareCandidateIdentity(receipt, path))
	}
	return ack, nil
}

func validateRetiredPrepareCandidateReceipt(r WorktreeMergeReceipt, path string) error {
	if r.ReceiptPath != path || r.ID == "" || r.Lane == "" || r.Lane != worktreeMergeLaneID(r.Repository, r.Target) {
		return errors.New("receipt has inconsistent immutable receipt identity")
	}
	if r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergeConflict || r.LandingSHA != "" || r.PullRequest != "" || r.PublishedCandidateSHA != "" {
		return errors.New("receipt is not an unpublished failed prepare conflict")
	}
	if r.Repository == "" || r.Target == "" || r.TargetSHA == "" || r.Candidate.SHA != "" {
		return errors.New("receipt is not the legacy empty-candidate prepare shape")
	}
	if !retiredcandidateack.DistinctCandidate(retiredcandidateack.Source{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: r.TargetSHA}, retiredPrepareSources(r)) {
		return errors.New("receipt has incomplete or candidate-confused source identity")
	}
	return nil
}

func retiredPrepareSources(r WorktreeMergeReceipt) []retiredcandidateack.Source {
	out := make([]retiredcandidateack.Source, 0, len(r.Sources))
	for _, s := range r.Sources {
		out = append(out, retiredcandidateack.Source{Task: s.Task, Worktree: s.Worktree, Branch: s.Branch, SHA: s.SHA})
	}
	return out
}
func retiredPrepareCandidateIdentity(r WorktreeMergeReceipt, path string) retiredcandidateack.ReceiptIdentity {
	return retiredcandidateack.ReceiptIdentity{Path: path, ID: r.ID, Phase: string(r.Phase), Status: string(r.Status), Lane: r.Lane, Repository: r.Repository, Target: r.Target, TargetSHA: r.TargetSHA, Candidate: retiredcandidateack.Source{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: r.TargetSHA}, Sources: retiredPrepareSources(r)}
}

func proveRetiredPrepareCandidate(ctx context.Context, path string, r WorktreeMergeReceipt, actor, reason string) (retiredcandidateack.Acknowledgement, error) {
	branch, head, err := retiredPrepareCandidateState(ctx, r)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	if branch != r.Candidate.Branch || head != r.TargetSHA {
		return retiredcandidateack.Acknowledgement{}, errors.New("candidate branch or HEAD no longer matches the immutable receipt target")
	}
	if err := requireAbsorbedConflictCandidateUnpublished(ctx, r.Candidate.Worktree, r); err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	absent, err := retiredPrepareRefAbsent(ctx, r.Candidate.Worktree, r.Target)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	if !absent {
		return retiredcandidateack.Acknowledgement{}, fmt.Errorf("recorded target branch %s still exists", r.Target)
	}
	defaultBranch, err := retiredPrepareDefaultBranch(ctx, r.Candidate.Worktree)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	defaultSHA, err := fetchExactMergeTarget(ctx, r.Candidate.Worktree, defaultBranch)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	contained, err := isMergeAncestor(ctx, r.Candidate.Worktree, head, defaultSHA)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	if !contained {
		return retiredcandidateack.Acknowledgement{}, fmt.Errorf("candidate %s is not contained in freshly fetched origin/%s", head, defaultBranch)
	}
	hash, err := retiredcandidateack.FileSHA256(path)
	if err != nil {
		return retiredcandidateack.Acknowledgement{}, err
	}
	ack := retiredcandidateack.Acknowledgement{SchemaVersion: retiredcandidateack.SchemaVersion, Status: retiredcandidateack.Status, ReceiptPath: path, ReceiptSHA256: hash, ReceiptID: r.ID, ReceiptPhase: string(r.Phase), ReceiptStatus: string(r.Status), Lane: r.Lane, Repository: r.Repository, Target: r.Target, TargetSHA: r.TargetSHA, Candidate: retiredcandidateack.Source{Task: r.Candidate.Task, Worktree: r.Candidate.Worktree, Branch: r.Candidate.Branch, SHA: head}, Sources: retiredPrepareSources(r), DefaultBranch: defaultBranch, DefaultSHA: defaultSHA, Actor: actor, Reason: reason, RecordedAt: time.Now().UTC()}
	ack.ID = retiredcandidateack.ComputeID(ack)
	return ack, nil
}

func retiredPrepareCandidateState(ctx context.Context, r WorktreeMergeReceipt) (string, string, error) {
	status, _, err := runCommand(ctx, 0, 0, r.Candidate.Worktree, "git", "status", "--porcelain")
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(status) != "" {
		return "", "", errors.New("candidate worktree has local changes")
	}
	branch, _, err := runCommand(ctx, 0, 0, r.Candidate.Worktree, "git", "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", "", fmt.Errorf("read candidate branch: %w", err)
	}
	head, err := mergeRevision(ctx, r.Candidate.Worktree, "HEAD")
	if err != nil {
		return "", "", err
	}
	return strings.TrimSpace(branch), head, nil
}
func retiredPrepareRefAbsent(ctx context.Context, root, branch string) (bool, error) {
	out, _, err := runCommand(ctx, 0, 0, root, "git", "ls-remote", "origin", "refs/heads/"+branch)
	return strings.TrimSpace(out) == "", err
}
func retiredPrepareDefaultBranch(ctx context.Context, root string) (string, error) {
	out, _, err := runCommand(ctx, 0, 0, root, "git", "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		f := strings.Fields(line)
		if len(f) >= 2 && f[1] == "HEAD" && strings.HasPrefix(f[0], "refs/heads/") {
			return strings.TrimPrefix(f[0], "refs/heads/"), nil
		}
	}
	return "", errors.New("origin has no default branch")
}
