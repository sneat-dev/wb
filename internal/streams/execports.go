package streams

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/console"
)

// defaultCommandTimeout bounds every child process a stream verb starts.
// `every-verification-run-is-bounded` applies to the small reads too: a `gh`
// call that waits forever on a credential prompt is a hang, not a failure, and
// a hang is what strands a lane.
const defaultCommandTimeout = 2 * time.Minute

// ExecGit runs real Git.
type ExecGit struct {
	// Timeout bounds each child. Zero uses defaultCommandTimeout.
	Timeout time.Duration
}

func (git ExecGit) run(ctx context.Context, dir string, args ...string) (string, error) {
	return runBounded(ctx, git.Timeout, dir, "git", args...)
}

// CurrentBranch implements Git.
func (git ExecGit) CurrentBranch(ctx context.Context, dir string) (string, error) {
	out, err := git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read current branch in %s: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// DefaultBranch implements Git using only local state.
func (git ExecGit) DefaultBranch(ctx context.Context, dir string) (string, error) {
	if out, err := git.run(ctx, dir, "symbolic-ref", "--quiet", "--short", "refs/remotes/origin/HEAD"); err == nil {
		if name := strings.TrimPrefix(strings.TrimSpace(out), "origin/"); name != "" {
			return name, nil
		}
	}
	for _, candidate := range []string{"main", "master"} {
		if _, err := git.run(ctx, dir, "show-ref", "--verify", "--quiet", "refs/remotes/origin/"+candidate); err == nil {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("determine default branch of %s from local Git state", dir)
}

// Fetch implements Git.
func (git ExecGit) Fetch(ctx context.Context, dir string) error {
	_, err := git.run(ctx, dir, "fetch", "--quiet", "origin")
	return err
}

// PushBranch implements Git and verifies the ref it pushed.
//
// `push-verifies-the-ref-it-pushed`: the push exit code is not evidence the
// intended commit landed, so the local and remote SHAs are compared after the
// push and a divergence is an error.
func (git ExecGit) PushBranch(ctx context.Context, dir, branch string) (string, error) {
	local, err := git.LocalHead(ctx, dir)
	if err != nil {
		return "", err
	}
	if _, err := git.run(ctx, dir, "push", "--set-upstream", "origin", branch); err != nil {
		return "", fmt.Errorf("push %s from %s: %w", branch, dir, err)
	}
	if _, err := git.run(ctx, dir, "fetch", "--quiet", "origin", branch); err != nil {
		return "", fmt.Errorf("re-read origin/%s after pushing: %w", branch, err)
	}
	remote, ok, err := git.RemoteHead(ctx, dir, branch)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", fmt.Errorf("pushed %s but origin/%s does not resolve; the push did not land", branch, branch)
	}
	if remote != local {
		return "", fmt.Errorf("pushed %s at %s but origin/%s is %s; the push did not land the intended commit", branch, local, branch, remote)
	}
	return remote, nil
}

// RemoteHead implements Git.
func (git ExecGit) RemoteHead(ctx context.Context, dir, branch string) (string, bool, error) {
	out, err := git.run(ctx, dir, "rev-parse", "--verify", "--quiet", "refs/remotes/origin/"+branch)
	if err != nil {
		return "", false, nil
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return "", false, nil
	}
	return sha, true, nil
}

// LocalHead implements Git.
func (git ExecGit) LocalHead(ctx context.Context, dir string) (string, error) {
	out, err := git.run(ctx, dir, "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("read HEAD in %s: %w", dir, err)
	}
	return strings.TrimSpace(out), nil
}

// CommitsNotIn implements Git through `git cherry`, which compares by patch
// identity. Lines beginning "+" are the patches base does not already carry.
func (git ExecGit) CommitsNotIn(ctx context.Context, dir, branch, base string) ([]string, error) {
	out, err := git.run(ctx, dir, "cherry", "-v", base, branch)
	if err != nil {
		return nil, fmt.Errorf("compare %s against %s in %s: %w", branch, base, dir, err)
	}
	var unabsorbed []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+ ") {
			continue
		}
		unabsorbed = append(unabsorbed, strings.TrimSpace(strings.TrimPrefix(line, "+ ")))
	}
	return unabsorbed, nil
}

// DirtyPaths implements Git.
func (git ExecGit) DirtyPaths(ctx context.Context, dir string) ([]string, error) {
	out, err := git.run(ctx, dir, "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("read status in %s: %w", dir, err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if len(line) > 3 {
			paths = append(paths, strings.TrimSpace(line[3:]))
		}
	}
	return paths, nil
}

// Tags implements Git. `--sort=-v:refname` puts the newest version first
// under Git's own version ordering, which understands the `backend/v1.2.3`
// module-tag spelling as well as a bare `v1.2.3`.
func (git ExecGit) Tags(ctx context.Context, dir, pattern string) ([]string, error) {
	args := []string{"tag", "--sort=-v:refname"}
	if strings.TrimSpace(pattern) != "" {
		args = append(args, "--list", pattern)
	}
	out, err := git.run(ctx, dir, args...)
	if err != nil {
		return nil, fmt.Errorf("list tags in %s: %w", dir, err)
	}
	var tags []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			tags = append(tags, trimmed)
		}
	}
	return tags, nil
}

// LogSubjects implements Git.
func (git ExecGit) LogSubjects(ctx context.Context, dir, from, to string) ([]string, error) {
	revisions := to
	if strings.TrimSpace(from) != "" {
		revisions = from + ".." + to
	}
	out, err := git.run(ctx, dir, "log", "--no-merges", "--format=%s", revisions)
	if err != nil {
		return nil, fmt.Errorf("read %s log in %s: %w", revisions, dir, err)
	}
	var subjects []string
	for _, line := range strings.Split(out, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			subjects = append(subjects, trimmed)
		}
	}
	return subjects, nil
}

// ExecGitHub runs the installed `gh`.
//
// Every call uses `gh api` or a `--json` read rather than parsing human
// output: `land-verbs-work-with-the-installed-gh` records what happens when a
// verb depends on the presentation of a specific gh release.
type ExecGitHub struct {
	Timeout time.Duration
}

func (hub ExecGitHub) run(ctx context.Context, dir string, args ...string) (string, error) {
	return runBounded(ctx, hub.Timeout, dir, "gh", args...)
}

// CreateDraftPullRequest implements GitHub.
func (hub ExecGitHub) CreateDraftPullRequest(ctx context.Context, dir, base, head, title, body string) (PullRequest, error) {
	if _, err := hub.run(ctx, dir, "pr", "create", "--draft", "--base", base, "--head", head, "--title", title, "--body", body); err != nil {
		return PullRequest{}, fmt.Errorf("open draft pull request for %s: %w", head, err)
	}
	pullRequest, found, err := hub.PullRequestForBranch(ctx, dir, head)
	if err != nil {
		return PullRequest{}, err
	}
	if !found {
		return PullRequest{}, fmt.Errorf("opened a draft pull request for %s but it does not resolve; treat the pull request as not created", head)
	}
	return pullRequest, nil
}

type pullRequestJSON struct {
	Number      int    `json:"number"`
	URL         string `json:"url"`
	Title       string `json:"title"`
	IsDraft     bool   `json:"isDraft"`
	State       string `json:"state"`
	HeadRefName string `json:"headRefName"`
	BaseRefName string `json:"baseRefName"`
}

func (raw pullRequestJSON) toPullRequest() PullRequest {
	return PullRequest{
		Number: raw.Number,
		URL:    raw.URL,
		Title:  raw.Title,
		Head:   raw.HeadRefName,
		Base:   raw.BaseRefName,
		Draft:  raw.IsDraft,
		State:  raw.State,
	}
}

const pullRequestFields = "number,url,title,isDraft,state,headRefName,baseRefName"

// PullRequestForBranch implements GitHub.
func (hub ExecGitHub) PullRequestForBranch(ctx context.Context, dir, branch string) (PullRequest, bool, error) {
	out, err := hub.run(ctx, dir, "pr", "list", "--head", branch, "--state", "open", "--json", pullRequestFields)
	if err != nil {
		return PullRequest{}, false, fmt.Errorf("list pull requests for %s: %w", branch, err)
	}
	var raw []pullRequestJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return PullRequest{}, false, fmt.Errorf("parse pull requests for %s: %w", branch, err)
	}
	if len(raw) == 0 {
		return PullRequest{}, false, nil
	}
	return raw[0].toPullRequest(), true, nil
}

// OpenPullRequestsTargeting implements GitHub.
func (hub ExecGitHub) OpenPullRequestsTargeting(ctx context.Context, dir, base string) ([]PullRequest, error) {
	out, err := hub.run(ctx, dir, "pr", "list", "--base", base, "--state", "open", "--json", pullRequestFields)
	if err != nil {
		return nil, fmt.Errorf("list pull requests targeting %s: %w", base, err)
	}
	var raw []pullRequestJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return nil, fmt.Errorf("parse pull requests targeting %s: %w", base, err)
	}
	pullRequests := make([]PullRequest, 0, len(raw))
	for _, entry := range raw {
		pullRequests = append(pullRequests, entry.toPullRequest())
	}
	return pullRequests, nil
}

// ClosePullRequest implements GitHub.
func (hub ExecGitHub) ClosePullRequest(ctx context.Context, dir string, number int, comment string) error {
	_, err := hub.run(ctx, dir, "pr", "close", strconv.Itoa(number), "--comment", comment)
	return err
}

// RetargetPullRequest implements GitHub.
func (hub ExecGitHub) RetargetPullRequest(ctx context.Context, dir string, number int, base string) error {
	_, err := hub.run(ctx, dir, "pr", "edit", strconv.Itoa(number), "--base", base)
	return err
}

// DefaultBranchStatus implements GitHub. An unresolvable status is reported as
// an empty conclusion rather than an error: a red-`main` check that cannot run
// must be reported as unknown, never assumed green.
func (hub ExecGitHub) DefaultBranchStatus(ctx context.Context, dir, branch string) (string, error) {
	out, err := hub.run(ctx, dir, "run", "list", "--branch", branch, "--status", "completed", "--limit", "1", "--json", "conclusion")
	if err != nil {
		return "", err
	}
	var raw []struct {
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return "", fmt.Errorf("parse default-branch run status: %w", err)
	}
	if len(raw) == 0 {
		return "", nil
	}
	return raw[0].Conclusion, nil
}

func runBounded(ctx context.Context, timeout time.Duration, dir, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(bounded, name, args...)
	command.Dir = dir
	command.Env = console.Env()
	output, err := command.CombinedOutput()
	if err != nil {
		if bounded.Err() != nil && ctx.Err() == nil {
			return string(output), fmt.Errorf("%s %s timed out after %s: %s", name, strings.Join(args, " "), timeout, strings.TrimSpace(string(output)))
		}
		return string(output), fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(output)))
	}
	return string(output), nil
}
