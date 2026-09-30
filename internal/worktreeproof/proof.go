// Package worktreeproof owns neutral commit and receipt identity checks.
package worktreeproof

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/mergeack"
	"github.com/sneat-dev/wb/internal/retiredcandidateack"
)

// GitQuery is a read-only, instance-scoped Git query supplied by the caller.
type GitQuery func(context.Context, string, ...string) (string, error)

// PullRequest is immutable evidence used to corroborate a landing.
type PullRequest struct {
	Number     int        `json:"number"`
	URL        string     `json:"url"`
	Repository string     `json:"repository,omitempty"`
	State      string     `json:"state"`
	Base       string     `json:"base"`
	BaseSHA    string     `json:"base_sha,omitempty"`
	HeadSHA    string     `json:"head_sha"`
	MergeSHA   string     `json:"merge_sha,omitempty"`
	Merged     *time.Time `json:"merged_at,omitempty"`
}

func IsGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	return HasOnlyLowerHexCharacters(value)
}

func IsGitRevisionID(value string) bool {
	if len(value) < 4 || len(value) > 64 {
		return false
	}
	return HasOnlyLowerHexCharacters(value)
}

func HasOnlyLowerHexCharacters(value string) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func ParseRemoteDefaultBranch(output string, valid func(string) bool) (string, error) {
	for _, line := range strings.Split(output, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "ref: ") {
			continue
		}
		fields := strings.Fields(strings.TrimPrefix(line, "ref: "))
		if len(fields) != 2 || fields[1] != "HEAD" || !strings.HasPrefix(fields[0], "refs/heads/") {
			continue
		}
		branch := strings.TrimPrefix(fields[0], "refs/heads/")
		if !valid(branch) {
			return "", fmt.Errorf("origin default branch is invalid: %q", branch)
		}
		return branch, nil
	}
	return "", fmt.Errorf("origin did not resolve a default branch")
}

func ParseCommitFirstParent(revision, parents string) (string, error) {
	fields := strings.Fields(parents)
	if len(fields) < 2 {
		return "", nil
	}
	if !IsGitObjectID(fields[1]) {
		return "", fmt.Errorf("commit %s resolved to invalid first parent %q", revision, fields[1])
	}
	return fields[1], nil
}

func ParseCommitTree(revision, tree string) (string, error) {
	if !IsGitObjectID(tree) {
		return "", fmt.Errorf("revision %s resolved to invalid tree SHA %q", revision, tree)
	}
	return tree, nil
}

func CommitFirstParent(ctx context.Context, repository, revision string, git GitQuery) (string, error) {
	parents, err := git(ctx, repository, "rev-list", "--parents", "-n", "1", "--end-of-options", revision)
	if err != nil {
		return "", fmt.Errorf("resolve parents of %s: %w", revision, err)
	}
	return ParseCommitFirstParent(revision, parents)
}

func CommitTree(ctx context.Context, repository, revision string, git GitQuery) (string, error) {
	tree, err := git(ctx, repository, "rev-parse", revision+"^{tree}")
	if err != nil {
		return "", fmt.Errorf("resolve tree for %s: %w", revision, err)
	}
	return ParseCommitTree(revision, tree)
}

// IsAncestor keeps the self-comparison and memo policy independent of the Git runner.
func IsAncestor(ctx context.Context, repository, ancestor, descendant string,
	run func(context.Context, string, string, string) (int, error),
	lookup func(string) (bool, bool), store func(string, bool)) (bool, error) {
	if ancestor == descendant {
		return true, nil
	}
	key := repository + "\x00" + ancestor + "\x00" + descendant
	if lookup != nil {
		if verdict, ok := lookup(key); ok {
			return verdict, nil
		}
	}
	exitCode, err := run(ctx, repository, ancestor, descendant)
	if err == nil {
		if store != nil && ctx.Err() == nil {
			store(key, true)
		}
		return true, nil
	}
	if exitCode == 1 && ctx.Err() == nil {
		if store != nil {
			store(key, false)
		}
		return false, nil
	}
	return false, fmt.Errorf("check whether %s is merged into %s: %w", ancestor, descendant, err)
}

// AgeFields holds the reporting fields changed by ApplyWorktreeAge.
type AgeFields struct {
	Owner      string
	CreatedAt  time.Time
	AgeSeconds int64
	TTLSeconds int64
	Expired    bool
}

func WorktreeOwnerName(owners []string, state string) string {
	for index := len(owners) - 1; index >= 0; index-- {
		if agent := strings.TrimSpace(owners[index]); agent != "" {
			return agent
		}
	}
	return state
}

// ApplyWorktreeAge uses caller-owned readers, preserving manifest precedence.
func ApplyWorktreeAge(result *AgeFields, worktree string, owners []string, state string, ttl time.Duration, now func() time.Time,
	readCreated func(string) (time.Time, error), statModified func(string) (time.Time, error)) {
	result.Owner = WorktreeOwnerName(owners, state)
	if created, err := readCreated(worktree); err == nil && !created.IsZero() {
		result.CreatedAt = created.UTC()
	} else if modified, statErr := statModified(worktree); statErr == nil {
		result.CreatedAt = modified.UTC()
	}
	if result.CreatedAt.IsZero() {
		return
	}
	current := now()
	if age := current.Sub(result.CreatedAt); age > 0 {
		result.AgeSeconds = int64(age / time.Second)
	}
	if ttl > 0 {
		result.TTLSeconds = int64(ttl / time.Second)
		result.Expired = current.Sub(result.CreatedAt) > ttl
	}
}

type AbsorbedConflictReceipt struct {
	ReceiptPath string `json:"receipt_path"`
	ID          string `json:"id"`
	Status      string `json:"status"`
	Lane        string `json:"lane"`
	Repository  string `json:"repository"`
	Target      string `json:"target"`
	TargetSHA   string `json:"target_sha"`
	Candidate   struct {
		Task     string `json:"task"`
		Worktree string `json:"worktree"`
		Branch   string `json:"branch"`
		SHA      string `json:"sha"`
	} `json:"candidate"`
	Sources []struct {
		Task     string `json:"task"`
		Worktree string `json:"worktree"`
		Branch   string `json:"branch"`
		SHA      string `json:"sha"`
	} `json:"sources"`
}

func (receipt AbsorbedConflictReceipt) Identity() mergeack.ReceiptIdentity {
	sources := make([]mergeack.Source, 0, len(receipt.Sources))
	for _, source := range receipt.Sources {
		sources = append(sources, mergeack.Source{Task: source.Task, Worktree: source.Worktree, Branch: source.Branch, SHA: source.SHA})
	}
	return mergeack.ReceiptIdentity{
		Path: receipt.ReceiptPath, ID: receipt.ID, Status: receipt.Status, Lane: receipt.Lane,
		Repository: receipt.Repository, Target: receipt.Target, TargetSHA: receipt.TargetSHA,
		Candidate: mergeack.Source{Task: receipt.Candidate.Task, Worktree: receipt.Candidate.Worktree, Branch: receipt.Candidate.Branch, SHA: receipt.Candidate.SHA},
		Sources:   sources,
	}
}

type RetiredPrepareCandidateReceipt struct {
	ReceiptPath           string                       `json:"receipt_path"`
	ID                    string                       `json:"id"`
	Phase                 string                       `json:"phase"`
	Status                string                       `json:"status"`
	Lane                  string                       `json:"lane"`
	Repository            string                       `json:"repository"`
	Target                string                       `json:"target"`
	TargetSHA             string                       `json:"target_sha"`
	LandingSHA            string                       `json:"landing_sha"`
	PullRequest           string                       `json:"pull_request"`
	PublishedCandidateSHA string                       `json:"published_candidate_sha"`
	Candidate             retiredcandidateack.Source   `json:"candidate"`
	Sources               []retiredcandidateack.Source `json:"sources"`
}

func (receipt RetiredPrepareCandidateReceipt) Identity() retiredcandidateack.ReceiptIdentity {
	candidate := receipt.Candidate
	candidate.SHA = receipt.TargetSHA
	return retiredcandidateack.ReceiptIdentity{
		Path: receipt.ReceiptPath, ID: receipt.ID, Phase: receipt.Phase,
		Status: receipt.Status, Lane: receipt.Lane, Repository: receipt.Repository,
		Target: receipt.Target, TargetSHA: receipt.TargetSHA, Candidate: candidate,
		Sources: receipt.Sources,
	}
}

func SameAbsorbedPullRequest(left, right *PullRequest) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Number == right.Number && left.Repository == right.Repository &&
		left.Base == right.Base && left.BaseSHA == right.BaseSHA &&
		left.HeadSHA == right.HeadSHA && left.MergeSHA == right.MergeSHA &&
		left.Merged != nil && right.Merged != nil && left.Merged.Equal(*right.Merged)
}
