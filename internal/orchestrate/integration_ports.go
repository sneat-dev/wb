package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
)

// CommitComment is one GitHub commit comment.
type CommitComment struct {
	Body     string
	CommitID string
}

// commitCommentReader reads one commit comment and the commit it is on.
type commitCommentReader interface {
	CommitComment(ctx context.Context, repository string, id int64) (CommitComment, error)
}

// IntegrationGitHub is every GitHub read the integration verbs make.
type IntegrationGitHub interface {
	commitCommentReader
	DefaultBranch(ctx context.Context, repository string) (string, error)
	// PromotionMergeMethod is how a promotion into target will land: the
	// merge queue's method when target has one, else the repository's
	// preferred enabled method (a merge commit when allowed).
	PromotionMergeMethod(ctx context.Context, repository, target string) (string, error)
}

// integrationGHCall runs one `gh` invocation and returns its stdout.
type integrationGHCall func(ctx context.Context, args ...string) ([]byte, error)

func integrationGH(ctx context.Context, args ...string) ([]byte, error) {
	response := githubExecute(ctx, "", args...)
	return response.Stdout, response.Err
}

// githubIntegration is the production IntegrationGitHub over `gh api`.
type githubIntegration struct {
	gh         integrationGHCall
	mergeQueue func(ctx context.Context, repository, target string) (*githubchecks.MergeQueuePolicy, error)
	repoMethod func(ctx context.Context, repository string) (string, error)
}

// DefaultIntegrationGitHub returns the production adapter.
func DefaultIntegrationGitHub() IntegrationGitHub {
	return newGitHubIntegration()
}

func newGitHubIntegration() githubIntegration {
	return githubIntegration{gh: integrationGH, mergeQueue: githubchecks.TargetMergeQueue, repoMethod: repositoryPullRequestMergeMethod}
}

func (adapter githubIntegration) PromotionMergeMethod(ctx context.Context, repository, target string) (string, error) {
	queue, err := adapter.mergeQueue(ctx, repository, target)
	if err != nil {
		return "", fmt.Errorf("read the merge queue of %s: %w", target, err)
	}
	if queue != nil {
		return queue.MergeMethod, nil
	}
	return adapter.repoMethod(ctx, repository)
}

func (adapter githubIntegration) CommitComment(ctx context.Context, repository string, id int64) (CommitComment, error) {
	var comment struct {
		Body     string `json:"body"`
		CommitID string `json:"commit_id"`
	}
	raw, err := adapter.gh(ctx, "api", "repos/"+repository+"/comments/"+strconv.FormatInt(id, 10))
	if err == nil {
		err = json.Unmarshal(raw, &comment)
	}
	if err != nil {
		return CommitComment{}, fmt.Errorf("read review comment %d: %w", id, err)
	}
	return CommitComment{Body: comment.Body, CommitID: comment.CommitID}, nil
}

// batchGit runs git in one checkout with separated streams, so stdout parses
// cleanly even when git also writes progress to stderr.
type batchGit struct {
	run runner.Runner
	dir string
}

func (git batchGit) exec(ctx context.Context, args ...string) (runner.Result, error) {
	return git.run.Run(ctx, git.dir, "git", args...)
}

// out returns trimmed stdout, or an error naming the command and stderr.
func (git batchGit) out(ctx context.Context, args ...string) (string, error) {
	result, err := git.exec(ctx, args...)
	if err != nil {
		return "", fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(result.Stderr+result.Stdout))
	}
	return strings.TrimSpace(result.Stdout), nil
}

// test answers a yes/no git question: exit 0 is yes, exit 1 is no.
func (git batchGit) test(ctx context.Context, args ...string) (bool, error) {
	result, err := git.exec(ctx, args...)
	switch {
	case err == nil:
		return true, nil
	case result.ExitCode == 1:
		return false, nil
	default:
		return false, fmt.Errorf("git %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(result.Stderr))
	}
}

// configArgs prefixes args with fixed git config options.
func configArgs(options []string, args ...string) []string {
	prefixed := make([]string, 0, 2*len(options)+len(args))
	for _, option := range options {
		prefixed = append(prefixed, "-c", option)
	}
	return append(prefixed, args...)
}

// githubPromote is the production PromoteGitHub over `gh api`.
type githubPromote struct{ githubIntegration }

// DefaultPromoteGitHub returns the production adapter.
func DefaultPromoteGitHub() PromoteGitHub {
	return githubPromote{newGitHubIntegration()}
}

func (adapter githubIntegration) DefaultBranch(ctx context.Context, repository string) (string, error) {
	var repo struct {
		DefaultBranch string `json:"default_branch"`
	}
	raw, err := adapter.gh(ctx, "api", "repos/"+repository)
	if err == nil {
		err = json.Unmarshal(raw, &repo)
	}
	if err == nil && repo.DefaultBranch == "" {
		err = fmt.Errorf("no default branch reported")
	}
	if err != nil {
		return "", fmt.Errorf("read the default branch of %s: %w", repository, err)
	}
	return repo.DefaultBranch, nil
}

func (adapter githubPromote) OpenPullRequest(ctx context.Context, repository, head, base string) (int, string, error) {
	owner, _, _ := strings.Cut(repository, "/")
	var pulls []struct {
		Number int    `json:"number"`
		Body   string `json:"body"`
	}
	raw, err := adapter.gh(ctx, "api", "repos/"+repository+"/pulls?state=open&head="+owner+":"+head+"&base="+base)
	if err == nil {
		err = json.Unmarshal(raw, &pulls)
	}
	if err != nil {
		return 0, "", fmt.Errorf("find the pull request from %s to %s: %w", head, base, err)
	}
	if len(pulls) == 0 {
		return 0, "", nil
	}
	return pulls[0].Number, pulls[0].Body, nil
}

func (adapter githubPromote) CreatePullRequest(ctx context.Context, repository, head, base, title, body string) (int, error) {
	var created struct {
		Number int `json:"number"`
	}
	raw, err := adapter.gh(ctx, "api", "-X", "POST", "repos/"+repository+"/pulls", "-f", "head="+head, "-f", "base="+base, "-f", "title="+title, "-f", "body="+body)
	if err == nil {
		err = json.Unmarshal(raw, &created)
	}
	if err != nil {
		return 0, fmt.Errorf("open the pull request from %s to %s: %w", head, base, err)
	}
	return created.Number, nil
}

func (adapter githubPromote) UpdatePullRequestBody(ctx context.Context, repository string, number int, body string) error {
	if _, err := adapter.gh(ctx, "api", "-X", "PATCH", "repos/"+repository+"/pulls/"+strconv.Itoa(number), "-f", "body="+body); err != nil {
		return fmt.Errorf("update %s#%d: %w", repository, number, err)
	}
	return nil
}
