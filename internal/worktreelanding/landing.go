// Package worktreelanding owns target-head and residual landing policy.
package worktreelanding

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/sneat-dev/wb/internal/worktreeproof"
)

const DefaultResidueDepth = 10

type ResidualCommit struct {
	SHA     string `json:"sha"`
	Subject string `json:"subject"`
}

type LandingEvidence struct {
	LandedSHA   string                     `json:"landed_sha"`
	LandingSHA  string                     `json:"landing_sha"`
	PullRequest *worktreeproof.PullRequest `json:"pull_request,omitempty"`
	Residue     []ResidualCommit           `json:"residue,omitempty"`
	Truncated   bool                       `json:"truncated,omitempty"`
}

// VerifiedCandidate is returned only after exact source and target corroboration.
type VerifiedCandidate struct {
	LandingSHA  string
	PullRequest *worktreeproof.PullRequest
}

type CandidateVerifier func(context.Context, string, string, string, string, string, string) (*VerifiedCandidate, error)

func LandingEvidenceFor(ctx context.Context, worktree, repository, slug, head, base, target string, depth int,
	git worktreeproof.GitQuery, verify CandidateVerifier) (*LandingEvidence, error) {
	if depth == 0 {
		depth = DefaultResidueDepth
	}
	if depth < 0 || target == "" || head == "" {
		return nil, nil
	}
	candidates, truncated, err := CommitsNotIn(ctx, repository, target, head, depth, git)
	if err != nil || len(candidates) == 0 {
		return nil, err
	}
	for _, candidate := range candidates {
		if candidate == head {
			continue
		}
		receipt, err := verify(ctx, worktree, repository, slug, candidate, base, target)
		if err != nil {
			return nil, err
		}
		if receipt == nil {
			continue
		}
		residue, err := ResidualCommits(ctx, repository, target, head, candidate, depth, git)
		if err != nil {
			return nil, err
		}
		return &LandingEvidence{LandedSHA: candidate, LandingSHA: receipt.LandingSHA, PullRequest: receipt.PullRequest, Residue: residue}, nil
	}
	if truncated {
		return &LandingEvidence{Truncated: true}, nil
	}
	return nil, nil
}

func CommitsNotIn(ctx context.Context, repository, target, head string, limit int, git worktreeproof.GitQuery) ([]string, bool, error) {
	output, err := git(ctx, repository, "rev-list", fmt.Sprintf("--max-count=%d", limit+1), head, "--not", target)
	if err != nil {
		return nil, false, fmt.Errorf("list commits of %s not in %s: %w", head, target, err)
	}
	commits := SplitNonEmptyLines(output)
	if len(commits) > limit {
		return commits[:limit], true, nil
	}
	return commits, false, nil
}

func ResidualCommits(ctx context.Context, repository, target, head, landed string, limit int, git worktreeproof.GitQuery) ([]ResidualCommit, error) {
	output, err := git(ctx, repository, "log", fmt.Sprintf("--max-count=%d", limit), "--format=%H %s", head, "--not", target, landed)
	if err != nil {
		return nil, fmt.Errorf("list residual commits of %s past %s: %w", head, landed, err)
	}
	lines := SplitNonEmptyLines(output)
	residue := make([]ResidualCommit, 0, len(lines))
	for _, line := range lines {
		sha, subject, _ := strings.Cut(line, " ")
		residue = append(residue, ResidualCommit{SHA: sha, Subject: strings.TrimSpace(subject)})
	}
	return residue, nil
}

func SplitNonEmptyLines(value string) []string {
	lines := make([]string, 0, 8)
	for _, line := range strings.Split(value, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

func DetachedRefusal(headSHA, base string, unknownToRemote bool) string {
	if unknownToRemote {
		return "detached HEAD " + shortSHA(headSHA) + " was never pushed: GitHub's commit index has never seen it, so nothing can prove what it holds"
	}
	return "detached HEAD " + shortSHA(headSHA) + " is on origin but no merged pull request into " + base + " is associated with it"
}

func LandedWithResidue(landing *LandingEvidence) bool {
	return landing != nil && landing.LandedSHA != "" && len(landing.Residue) > 0
}

// ResidueReason explains why a checkout whose work landed is still held, and
// names the verb that retires it: `wb worktree gc <task> --allow-residue
// --apply`, the only verb with that flag (sneat-dev/wb#814: the finding used to
// say "rerun with --allow-residue", which `wb worktree cleanup`, the verb that
// printed it, rejects).
func ResidueReason(landing *LandingEvidence, task string) string {
	if landing == nil {
		return ""
	}
	text := "landed + residue: " + shortSHA(landing.LandedSHA) + " landed at " + shortSHA(landing.LandingSHA)
	if landing.PullRequest != nil {
		text += " via " + landing.PullRequest.URL
	}
	return text + "; " + PluralCommits(len(landing.Residue)) + " not in the target: " + landing.ResidueSummary() + "; retire it and discard them with: wb worktree gc " + task + " --allow-residue --apply"
}

func PluralCommits(count int) string {
	if count == 1 {
		return "1 residual commit"
	}
	return fmt.Sprintf("%d residual commits", count)
}

func (evidence *LandingEvidence) ResidueSummary() string {
	if evidence == nil || len(evidence.Residue) == 0 {
		return ""
	}
	parts := make([]string, 0, len(evidence.Residue))
	for _, commit := range evidence.Residue {
		parts = append(parts, shortSHA(commit.SHA)+" "+commit.Subject)
	}
	return strings.Join(parts, "; ")
}

func shortSHA(sha string) string {
	if len(sha) > 12 {
		return sha[:12]
	}
	return sha
}

// TargetHeadCache is scoped to one inventory context, including failed reads.
type TargetHeadCache struct {
	mu      sync.Mutex
	entries map[string]*targetHeadEntry
}
type targetHeadEntry struct {
	once sync.Once
	sha  string
	err  error
}
type targetHeadCacheKey struct{}

func NewTargetHeadCache() *TargetHeadCache {
	return &TargetHeadCache{entries: make(map[string]*targetHeadEntry)}
}
func WithTargetHeadCache(ctx context.Context) context.Context {
	return context.WithValue(ctx, targetHeadCacheKey{}, NewTargetHeadCache())
}
func TargetHeadCacheFrom(ctx context.Context) *TargetHeadCache {
	cache, _ := ctx.Value(targetHeadCacheKey{}).(*TargetHeadCache)
	return cache
}

func (c *TargetHeadCache) Resolve(repository, branch string, fetch func() (string, error)) (string, error) {
	key := repository + "\x00" + branch
	c.mu.Lock()
	entry, ok := c.entries[key]
	if !ok {
		entry = &targetHeadEntry{}
		c.entries[key] = entry
	}
	c.mu.Unlock()
	entry.once.Do(func() { entry.sha, entry.err = fetch() })
	return entry.sha, entry.err
}

func RemoteBranchHead(ctx context.Context, repository, branch string, git worktreeproof.GitQuery) (string, error) {
	output, err := git(ctx, repository, "ls-remote", "--heads", "origin", "refs/heads/"+branch)
	if err != nil {
		return "", err
	}
	fields := strings.Fields(output)
	if len(fields) == 0 {
		return "", nil
	}
	if len(fields) != 2 {
		return "", fmt.Errorf("unexpected remote branch response for %s: %q", branch, output)
	}
	return fields[0], nil
}

func FetchRemoteTargetHead(ctx context.Context, repository, branch string, timeout time.Duration,
	fetch func(context.Context, string, string) (string, error)) (string, error) {
	if cache := TargetHeadCacheFrom(ctx); cache != nil {
		return cache.Resolve(repository, branch, func() (string, error) { return FetchRemoteTargetHeadUncached(ctx, repository, branch, timeout, fetch) })
	}
	return FetchRemoteTargetHeadUncached(ctx, repository, branch, timeout, fetch)
}

func IsMissingRemoteTargetError(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	for _, marker := range []string{"couldn't find remote ref", "could not find remote ref", "remote ref does not exist", "no such ref"} {
		if strings.Contains(message, marker) {
			return true
		}
	}
	return false
}

func FetchRemoteTargetHeadUncached(ctx context.Context, repository, branch string, timeout time.Duration,
	fetch func(context.Context, string, string) (string, error)) (string, error) {
	fetchCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	head, err := fetch(fetchCtx, repository, branch)
	if err != nil {
		if errors.Is(fetchCtx.Err(), context.DeadlineExceeded) && ctx.Err() == nil {
			return "", fmt.Errorf("fetch exact origin/%s target: remote did not answer within %s", branch, timeout)
		}
		return "", fmt.Errorf("fetch exact origin/%s target: %w", branch, err)
	}
	return head, nil
}

func RemoteDefaultBranch(ctx context.Context, repository string, git worktreeproof.GitQuery, valid func(string) bool) (string, error) {
	output, err := git(ctx, repository, "ls-remote", "--symref", "origin", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read origin default branch: %w", err)
	}
	return worktreeproof.ParseRemoteDefaultBranch(output, valid)
}
