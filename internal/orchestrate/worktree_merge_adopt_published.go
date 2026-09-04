package orchestrate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/gitremote"
	"github.com/sneat-dev/wb/internal/worktrees"
)

const worktreeMergePublishedCandidateAdoptionSuffix = ".published-candidate.adopted.ack.json"

// linkPublishedCandidateAdoption is a narrow durable-publication seam: an
// acknowledgement becomes visible only through this create-if-absent link.
var linkPublishedCandidateAdoption = os.Link

// WorktreeMergePublishedCandidateAdoption records remote publication that
// completed outside WB after prepare persisted but before it could record the
// PR. It is deliberately separate from the immutable merge receipt.
type WorktreeMergePublishedCandidateAdoption struct {
	SchemaVersion       int                    `json:"schema_version"`
	ID                  string                 `json:"id"`
	Status              string                 `json:"status"`
	ReceiptPath         string                 `json:"receipt_path"`
	AcknowledgementPath string                 `json:"acknowledgement_path"`
	ReceiptSHA256       string                 `json:"receipt_sha256"`
	ReceiptID           string                 `json:"receipt_id"`
	Lane                string                 `json:"lane"`
	Repository          string                 `json:"repository"`
	Target              string                 `json:"target"`
	Candidate           WorktreeMergeCandidate `json:"candidate"`
	PullRequest         string                 `json:"pull_request"`
	Actor               string                 `json:"actor"`
	Reason              string                 `json:"reason"`
	RecordedAt          time.Time              `json:"recorded_at"`
}

type WorktreeMergePublishedCandidateAdoptionOptions struct {
	ProjectsRoot, Receipt, PullRequest string
	Apply                              bool
	Actor, Reason                      string
}

func AdoptPublishedWorktreeMergeCandidate(ctx context.Context, options WorktreeMergePublishedCandidateAdoptionOptions) (WorktreeMergePublishedCandidateAdoption, error) {
	if strings.TrimSpace(options.PullRequest) == "" {
		return WorktreeMergePublishedCandidateAdoption{}, errors.New("pull request is required")
	}
	if options.Apply && (strings.TrimSpace(options.Actor) == "" || strings.TrimSpace(options.Reason) == "") {
		return WorktreeMergePublishedCandidateAdoption{}, errors.New("--actor and --reason are required with --apply")
	}
	path, err := resolveWorktreeMergeReceiptPath(options.ProjectsRoot, options.Receipt)
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	receipt, err := readWorktreeMergeReceipt(path)
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	lock, err := AcquireOperationLock(options.ProjectsRoot, receipt.Lane, true)
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	defer func() { _ = lock.Release() }()
	// Every mutable fact is re-read while the lane is exclusively held.
	receipt, err = readWorktreeMergeReceipt(path)
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	if err := validatePublishedCandidateAdoptionReceipt(receipt, path); err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	if _, err := validateMergeAcknowledgementCandidate(ctx, options.ProjectsRoot, receipt, receipt.Candidate); err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, fmt.Errorf("validate candidate: %w", err)
	}
	if err := validatePublishedCandidateAdoptionSources(ctx, receipt); err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, fmt.Errorf("re-read sources: %w", err)
	}
	if err := provePublishedCandidatePullRequest(ctx, receipt, options.PullRequest); err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	digest, err := worktreeMergeReceiptSHA256(path)
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	ack := WorktreeMergePublishedCandidateAdoption{SchemaVersion: 1, Status: "published_candidate_adopted", ReceiptPath: path, AcknowledgementPath: publishedCandidateAdoptionPath(path), ReceiptSHA256: digest, ReceiptID: receipt.ID, Lane: receipt.Lane, Repository: receipt.Repository, Target: receipt.Target, Candidate: receipt.Candidate, PullRequest: strings.TrimSpace(options.PullRequest), Actor: strings.TrimSpace(options.Actor), Reason: strings.TrimSpace(options.Reason), RecordedAt: time.Now().UTC()}
	ack.ID = publishedCandidateAdoptionID(ack)
	if existing, readErr := readPublishedCandidateAdoption(ack.AcknowledgementPath, receipt); readErr == nil {
		if existing.PullRequest != ack.PullRequest || existing.Candidate != ack.Candidate {
			return WorktreeMergePublishedCandidateAdoption{}, fmt.Errorf("published-candidate adoption %s binds different evidence", ack.AcknowledgementPath)
		}
		return existing, nil
	} else if !errors.Is(readErr, os.ErrNotExist) {
		return WorktreeMergePublishedCandidateAdoption{}, readErr
	}
	if !options.Apply {
		return ack, nil
	}
	if err := persistPublishedCandidateAdoption(ack.AcknowledgementPath, ack); err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	return ack, nil
}

func validatePublishedCandidateAdoptionReceipt(r WorktreeMergeReceipt, path string) error {
	if r.ReceiptPath != path || r.Phase != WorktreeMergePhasePrepare || r.Status != WorktreeMergeConflict || r.ID == "" || r.Lane != worktreeMergeLaneID(r.Repository, r.Target) || r.Repository == "" || r.Target == "" || r.Candidate.Task == "" || r.Candidate.Worktree == "" || r.Candidate.Branch == "" || r.Candidate.SHA == "" || len(r.Sources) == 0 || r.PullRequest != "" || r.PublishedCandidateSHA != "" || r.LandingSHA != "" {
		return errors.New("receipt is not an exact unlanded prepare/conflict candidate awaiting publication adoption")
	}
	return nil
}

func provePublishedCandidatePullRequest(ctx context.Context, r WorktreeMergeReceipt, selector string) error {
	hostedRepository, err := hostedRepositoryForCandidate(ctx, r)
	if err != nil {
		return err
	}
	v, err := ReadPullRequest(ctx, hostedRepository, selector)
	if err != nil {
		return err
	}
	if !strings.EqualFold(v.State, "open") {
		return fmt.Errorf("pull request %s is %s, not open", selector, v.State)
	}
	if v.Base.Ref != r.Target || v.Head.Ref != r.Candidate.Branch || v.Head.SHA != r.Candidate.SHA || v.Head.Repo == nil || v.Head.Repo.FullName != hostedRepository || v.Base.Repo == nil || v.Base.Repo.FullName != hostedRepository {
		return fmt.Errorf("pull request %s does not match exact repository, target, candidate branch, and candidate SHA", selector)
	}
	remote, _, err := runCommand(ctx, 0, 0, r.Candidate.Worktree, "git", "ls-remote", "--heads", "origin", "refs/heads/"+r.Candidate.Branch)
	if err != nil {
		return fmt.Errorf("read candidate remote ref: %w", err)
	}
	if !strings.HasPrefix(strings.TrimSpace(remote), r.Candidate.SHA+"\t") {
		return fmt.Errorf("candidate remote branch %s does not match receipted candidate %s", r.Candidate.Branch, r.Candidate.SHA)
	}
	return nil
}

// hostedRepositoryForCandidate takes GitHub identity from the candidate's
// authenticated origin, never from the historical Work Log repository name.
// Renames preserve the latter for WB lifecycle identity while GitHub PR reads
// must address the repository that currently hosts the branch.
func hostedRepositoryForCandidate(ctx context.Context, r WorktreeMergeReceipt) (string, error) {
	origin, _, err := runCommand(ctx, 0, 0, r.Candidate.Worktree, "git", "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("read candidate origin: %w", err)
	}
	remote, err := gitremote.Parse(strings.TrimSpace(origin))
	if err != nil {
		return "", fmt.Errorf("parse candidate origin: %w", err)
	}
	if remote.Identity.Host() == "github.com" {
		return remote.Identity.Repository, nil
	}
	// Test fixtures intentionally use local bare remotes. Production hosted
	// candidates must present github.com; never derive a hosted identity from a
	// filesystem path.
	if remote.Identity.Host() == "" && r.Repository != "" {
		return r.Repository, nil
	}
	return "", errors.New("candidate origin is not a github.com repository")
}

func publishedCandidateAdoptionPath(path string) string {
	return path + worktreeMergePublishedCandidateAdoptionSuffix
}
func publishedCandidateAdoptionID(a WorktreeMergePublishedCandidateAdoption) string {
	h := sha256.New()
	for _, v := range []string{a.ReceiptPath, a.ReceiptSHA256, a.ReceiptID, a.Lane, a.Repository, a.Target, a.Candidate.Task, a.Candidate.Worktree, a.Candidate.Branch, a.Candidate.SHA, a.PullRequest, a.Actor, a.Reason} {
		_, _ = h.Write([]byte(v))
		_, _ = h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}
func persistPublishedCandidateAdoption(path string, a WorktreeMergePublishedCandidateAdoption) error {
	b, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".published-candidate-adoption-*.tmp")
	if err != nil {
		return err
	}
	temporary := f.Name()
	defer func() { _ = os.Remove(temporary) }()
	if err := f.Chmod(0600); err != nil {
		_ = f.Close()
		return err
	}
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := linkPublishedCandidateAdoption(temporary, path); err != nil {
		return err
	}
	directory, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer func() { _ = directory.Close() }()
	return directory.Sync()
}

func validatePublishedCandidateAdoptionSources(ctx context.Context, receipt WorktreeMergeReceipt) error {
	for _, source := range receipt.Sources {
		if err := requireCleanMergeWorktree(ctx, source.Worktree); err != nil {
			return fmt.Errorf("source %s is not clean: %w", source.Branch, err)
		}
		branch, _, err := runCommand(ctx, 0, 0, source.Worktree, "git", "branch", "--show-current")
		if err != nil {
			return err
		}
		branch = strings.TrimSpace(branch)
		if branch != source.Branch {
			return fmt.Errorf("source branch %s no longer matches receipted branch %s", branch, source.Branch)
		}
		head, err := mergeRevision(ctx, source.Worktree, "HEAD")
		if err != nil {
			return err
		}
		contains, err := isMergeAncestor(ctx, source.Worktree, source.SHA, head)
		if err != nil || !contains {
			if err == nil {
				err = fmt.Errorf("source %s head %s is not a descendant of receipted %s", source.Branch, head, source.SHA)
			}
			return err
		}
		view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{Worktree: source.Worktree})
		if err != nil {
			return fmt.Errorf("load source Work Log: %w", err)
		}
		if view.Claim == nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != receipt.Repository || view.Claim.Task != source.Task || view.Claim.Branch != source.Branch || filepath.Clean(view.Claim.Worktree) != filepath.Clean(source.Worktree) {
			return fmt.Errorf("source %s has no matching active Work Log claim", source.Branch)
		}
	}
	return nil
}
func readPublishedCandidateAdoption(path string, r WorktreeMergeReceipt) (WorktreeMergePublishedCandidateAdoption, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, err
	}
	var a WorktreeMergePublishedCandidateAdoption
	if err = json.Unmarshal(b, &a); err != nil {
		return a, err
	}
	digest, err := worktreeMergeReceiptSHA256(r.ReceiptPath)
	if err != nil {
		return a, err
	}
	materialized := r.PullRequest == a.PullRequest && r.PublishedCandidateSHA == a.Candidate.SHA && r.Candidate.SHA != a.Candidate.SHA
	if a.SchemaVersion != 1 || a.Status != "published_candidate_adopted" || a.AcknowledgementPath != path || a.ReceiptPath != r.ReceiptPath || (!materialized && a.ReceiptSHA256 != digest) || a.ReceiptID != r.ID || a.Lane != r.Lane || a.Repository != r.Repository || a.Target != r.Target || (!materialized && a.Candidate != r.Candidate) || a.PullRequest == "" || a.Actor == "" || a.Reason == "" || a.RecordedAt.IsZero() || a.ID != publishedCandidateAdoptionID(a) {
		return a, fmt.Errorf("published-candidate adoption %s has invalid immutable identity", path)
	}
	return a, nil
}
func adoptedPublishedCandidate(ctx context.Context, r WorktreeMergeReceipt) (WorktreeMergePublishedCandidateAdoption, bool, error) {
	a, err := readPublishedCandidateAdoption(publishedCandidateAdoptionPath(r.ReceiptPath), r)
	if errors.Is(err, os.ErrNotExist) {
		return WorktreeMergePublishedCandidateAdoption{}, false, nil
	}
	if err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, false, err
	}
	if r.PullRequest == a.PullRequest && r.PublishedCandidateSHA == a.Candidate.SHA && r.Candidate.SHA != a.Candidate.SHA {
		if r.Candidate.SHA == "" {
			return a, false, nil
		}
		contains, err := isMergeAncestor(ctx, r.Candidate.Worktree, a.Candidate.SHA, r.Candidate.SHA)
		if err != nil || !contains {
			if err == nil {
				err = fmt.Errorf("materialized candidate %s does not retain adopted predecessor %s", r.Candidate.SHA, a.Candidate.SHA)
			}
			return WorktreeMergePublishedCandidateAdoption{}, false, err
		}
		return a, false, nil
	}
	if err := provePublishedCandidatePullRequest(ctx, r, a.PullRequest); err != nil {
		return WorktreeMergePublishedCandidateAdoption{}, false, err
	}
	return a, true, nil
}
