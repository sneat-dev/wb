package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/filewrite"
	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktrees"
)

// PullRequestUpdateOptions selects an update of a PR's head from its base.
// This operation never waits for checks, arms auto-merge, or lands the PR.
type PullRequestUpdateOptions struct {
	Repository   string
	PullRequest  string
	ProjectsRoot string
}

// PullRequestUpdateResult is the durable exact-identity record of one attempt.
// An accepted server update is recorded before any optional local checkout sync.
type PullRequestUpdateResult struct {
	SchemaVersion    int       `json:"schema_version"`
	Repository       string    `json:"repository"`
	PullRequest      string    `json:"pull_request"`
	Target           string    `json:"target"`
	Branch           string    `json:"branch"`
	BeforeSHA        string    `json:"before_sha"`
	TargetBeforeSHA  string    `json:"target_before_sha"`
	AfterSHA         string    `json:"after_sha,omitempty"`
	TargetParentSHA  string    `json:"target_parent_sha,omitempty"`
	TargetCurrentSHA string    `json:"target_current_sha,omitempty"`
	Status           string    `json:"status"` // requested, updated, *_partial, unchanged, or unverified
	LocalSync        string    `json:"local_sync,omitempty"`
	Reason           string    `json:"reason,omitempty"`
	ReceiptPath      string    `json:"receipt_path"`
	RecordedAt       time.Time `json:"recorded_at"`
}

type pullRequestUpdateOps struct {
	read       func(context.Context, string, string) (PullRequestView, error)
	target     func(context.Context, string, string) (string, string)
	contains   func(context.Context, string, string, string) (bool, string)
	update     func(context.Context, string, string, string) (string, string)
	parents    func(context.Context, string, string) ([]string, error)
	proveTree  func(context.Context, PullRequestUpdateOptions, string, string, string, string, string) (bool, error)
	syncLocal  func(context.Context, PullRequestUpdateOptions, string, string) string
	newReceipt func(PullRequestUpdateOptions) (string, error)
	persist    func(PullRequestUpdateResult) error
}

func productionPullRequestUpdateOps() pullRequestUpdateOps {
	return pullRequestUpdateOps{
		read: ReadPullRequest, target: targetHead, contains: candidateContainsTarget,
		update: func(ctx context.Context, repo, number, head string) (string, string) {
			return updatePullRequestBranch(ctx, repo, number, head, nil)
		},
		parents: pullRequestCommitParents,
		proveTree: func(ctx context.Context, options PullRequestUpdateOptions, target, branch, before, targetParent, updated string) (bool, error) {
			canonical, err := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, options.Repository)
			if err != nil {
				return false, err
			}
			return verifyUpdateBranchMergeProof(ctx, canonical, branch, target, options.Repository, before, targetParent, updated)
		},
		syncLocal:  syncOwnedPullRequestUpdateWorktree,
		newReceipt: newPullRequestUpdateReceiptPath,
		persist:    persistPullRequestUpdateReceipt,
	}
}

// UpdatePullRequest brings one PR head up to its observed base by GitHub's
// expected-head compare-and-swap. It records the exact result without landing.
func UpdatePullRequest(ctx context.Context, options PullRequestUpdateOptions) (PullRequestUpdateResult, error) {
	return updatePullRequestWith(ctx, options, productionPullRequestUpdateOps())
}

func updatePullRequestWith(ctx context.Context, options PullRequestUpdateOptions, ops pullRequestUpdateOps) (result PullRequestUpdateResult, err error) {
	repo, number := strings.TrimSpace(options.Repository), strings.TrimSpace(options.PullRequest)
	owner, name, separated := strings.Cut(repo, "/")
	if !separated || !validPullRequestUpdateRepositorySegment(owner) || !validPullRequestUpdateRepositorySegment(name) {
		return result, fmt.Errorf("repository must be owner/repository")
	}
	if parsed, parseErr := strconv.Atoi(number); parseErr != nil || parsed <= 0 || strconv.Itoa(parsed) != number {
		return result, fmt.Errorf("pull request number must be a positive decimal integer")
	}
	view, err := ops.read(ctx, repo, number)
	if err != nil {
		return result, err
	}
	if err := validatePullRequestUpdateView(view, repo, number, "", ""); err != nil {
		return result, err
	}
	before, branch, target := view.Head.SHA, view.Head.Ref, view.Base.Ref
	targetSHA, reason := ops.target(ctx, repo, target)
	if reason != "" {
		return result, fmt.Errorf("read target head: %s", reason)
	}
	if targetSHA == "" {
		return result, fmt.Errorf("target %s returned no SHA", target)
	}
	contains, reason := ops.contains(ctx, repo, targetSHA, before)
	if reason != "" {
		return result, fmt.Errorf("compare PR head with target: %s", reason)
	}
	path, err := ops.newReceipt(options)
	if err != nil {
		return result, err
	}
	result = PullRequestUpdateResult{
		SchemaVersion: 1, Repository: repo, PullRequest: number,
		Target: target, Branch: branch, BeforeSHA: before,
		TargetBeforeSHA: targetSHA, ReceiptPath: path, RecordedAt: time.Now().UTC(),
	}
	if contains {
		result.Status, result.AfterSHA = "unchanged", before
		currentPR, currentErr := ops.read(ctx, repo, number)
		if currentErr != nil {
			result.Status, result.Reason = "unverified", currentErr.Error()
		} else if identityErr := validatePullRequestUpdateView(currentPR, repo, number, target, branch); identityErr != nil || currentPR.Head.SHA != before {
			result.Status, result.Reason = "unchanged_partial", "PR head or identity moved during no-op check; rerun update"
		}
		currentTarget, currentReason := ops.target(ctx, repo, target)
		if currentReason != "" {
			result.Status, result.Reason = "unverified", currentReason
		} else {
			result.TargetCurrentSHA = currentTarget
			if result.Status == "unchanged" && currentTarget != targetSHA {
				result.Status, result.Reason = "unchanged_partial", "target advanced during no-op check; rerun update"
			} else if result.Status == "unchanged" {
				result.LocalSync = ops.syncLocal(ctx, options, branch, before)
				if pullRequestUpdateLocalSyncIncomplete(result.LocalSync) {
					result.Status = "unchanged_partial"
				}
			}
		}
		if err := ops.persist(result); err != nil {
			return result, err
		}
		return result, nil
	}
	result.Status = "requested"
	defer func() {
		if err != nil && result.Status == "requested" {
			result.Status, result.Reason = "unverified", err.Error()
			if persistErr := ops.persist(result); persistErr != nil {
				err = fmt.Errorf("%w; also failed to persist unverified receipt: %v", err, persistErr)
			}
		}
	}()
	if err := ops.persist(result); err != nil {
		return result, fmt.Errorf("record PR update intent: %w", err)
	}
	updated, reason := ops.update(ctx, repo, number, before)
	if reason != "" {
		return result, fmt.Errorf("update PR branch: %s", reason)
	}
	if updated == "" || updated == before {
		return result, fmt.Errorf("update did not produce a distinct exact head (receipt %s)", path)
	}
	result.AfterSHA = updated
	if err := ops.persist(result); err != nil {
		return result, fmt.Errorf("record accepted PR update: %w", err)
	}
	after, err := ops.read(ctx, repo, number)
	if err != nil {
		return result, fmt.Errorf("re-read updated PR (receipt %s): %w", path, err)
	}
	if err := validatePullRequestUpdateView(after, repo, number, target, branch); err != nil || after.Head.SHA != updated {
		return result, fmt.Errorf("updated PR identity/head changed (receipt %s): %v; observed %s, expected %s", path, err, after.Head.SHA, updated)
	}
	parents, err := ops.parents(ctx, repo, updated)
	if err != nil || len(parents) != 2 || parents[0] != before || parents[1] == "" {
		return result, fmt.Errorf("updated head merge parents are unverified (receipt %s): %v", path, err)
	}
	result.TargetParentSHA = parents[1]
	ancestor, reason := ops.contains(ctx, repo, targetSHA, parents[1])
	if reason != "" || !ancestor {
		return result, fmt.Errorf("updated target parent does not contain observed target %s (receipt %s): %s", targetSHA, path, reason)
	}
	proved, err := ops.proveTree(ctx, options, target, branch, before, parents[1], updated)
	if err != nil || !proved {
		return result, fmt.Errorf("updated merge tree could not be proved (receipt %s): %v", path, err)
	}
	currentTarget, reason := ops.target(ctx, repo, target)
	if reason != "" {
		return result, fmt.Errorf("re-read current target (receipt %s): %s", path, reason)
	}
	if currentTarget == "" {
		return result, fmt.Errorf("current target returned no SHA (receipt %s)", path)
	}
	result.TargetCurrentSHA = currentTarget
	result.Status = "updated"
	if err := ops.persist(result); err != nil {
		return result, fmt.Errorf("record verified PR update: %w", err)
	}
	result.LocalSync = ops.syncLocal(ctx, options, branch, updated)
	if currentTarget != parents[1] || pullRequestUpdateLocalSyncIncomplete(result.LocalSync) {
		result.Status = "updated_partial"
	}
	if err := ops.persist(result); err != nil {
		return result, fmt.Errorf("record local sync result: %w", err)
	}
	return result, nil
}

func pullRequestUpdateLocalSyncIncomplete(note string) bool {
	return strings.HasPrefix(note, "local sync skipped:") || strings.HasPrefix(note, "local worktree not fast-forwarded:")
}

func validPullRequestUpdateRepositorySegment(value string) bool {
	if value == "" || value == "." || value == ".." || strings.Contains(value, "..") {
		return false
	}
	for _, character := range value {
		if character >= 'a' && character <= 'z' || character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9' || character == '-' || character == '_' || character == '.' {
			continue
		}
		return false
	}
	return true
}

func validatePullRequestUpdateView(view PullRequestView, repository, number, target, branch string) error {
	if view.State != "open" || view.Merged || view.Locked || fmt.Sprint(view.Number) != number {
		return fmt.Errorf("pull request %s#%s is not the expected open, unlocked PR", repository, number)
	}
	if view.Head.Repo == nil || view.Base.Repo == nil || view.Head.Repo.FullName != repository || view.Base.Repo.FullName != repository {
		return fmt.Errorf("pull request head and base must belong to %s", repository)
	}
	if view.Head.SHA == "" || view.Head.Ref == "" || view.Base.Ref == "" || view.Base.SHA == "" || view.Head.Ref == view.Base.Ref {
		return fmt.Errorf("pull request omitted distinct head/base identities")
	}
	if target != "" && (view.Base.Ref != target || view.Head.Ref != branch) {
		return fmt.Errorf("pull request branch identities changed")
	}
	return nil
}

func newPullRequestUpdateReceiptPath(options PullRequestUpdateOptions) (string, error) {
	home, err := wbhome.Root(options.ProjectsRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Join(home, "reports", "pr-update", options.Repository, options.PullRequest)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	file, err := filewrite.CreateTemp(dir, "update-*.json", nil)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return file.Name(), nil
}

func persistPullRequestUpdateReceipt(result PullRequestUpdateResult) error {
	if result.ReceiptPath == "" {
		return fmt.Errorf("PR update receipt path is required")
	}
	data, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	file, err := filewrite.CreateTemp(filepath.Dir(result.ReceiptPath), ".update-*.tmp", nil)
	if err != nil {
		return err
	}
	defer func() { _ = os.Remove(file.Name()) }()
	if err = filewrite.Write(file, append(data, '\n'), file.Name(), nil); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Sync(); err != nil {
		_ = file.Close()
		return err
	}
	if err = file.Close(); err != nil {
		return err
	}
	return filewrite.Rename(file.Name(), result.ReceiptPath, nil)
}

func syncOwnedPullRequestUpdateWorktree(ctx context.Context, options PullRequestUpdateOptions, branch, head string) string {
	canonical, err := worktrees.CanonicalRepositoryPath(options.ProjectsRoot, options.Repository)
	if err != nil {
		return "local sync skipped: canonical checkout lookup failed: " + err.Error()
	}
	path, err := registeredWorktreeForBranch(ctx, canonical, branch, Options{})
	if err != nil {
		return "local sync skipped: worktree lookup failed: " + err.Error()
	}
	if path == "" {
		return "local sync not applicable: no linked worktree holds " + branch
	}
	view, err := worktrees.LoadWorkLogView(ctx, worktrees.LoadWorkLogOptions{ProjectsRoot: options.ProjectsRoot, Worktree: path})
	if err != nil || view.Claim == nil || view.Terminal != nil || view.Claim.Lifecycle != "active" || view.Claim.Repository != options.Repository || view.Claim.Branch != branch || view.Claim.Worktree != path {
		return "local sync skipped: checkout has no matching active WB claim"
	}
	guard, err := worktrees.Guard(ctx, path, worktrees.GuardOptions{ProjectsRoot: options.ProjectsRoot, Base: view.Claim.Base})
	if err != nil || guard.Kind != "linked" || guard.Branch != branch {
		return "local sync skipped: checkout did not pass WB guard"
	}
	return fastForwardWorktreeToUpdatedHead(ctx, path, branch, head)
}
