package orchestrate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/runner"
)

// BatchGitHub is every GitHub read and write a batch verb makes.
type BatchGitHub interface {
	PullRequest(ctx context.Context, repository string, number int) (githubchecks.PullRequestView, error)
	IssueComment(ctx context.Context, repository string, id int64) (string, error)
	AllowsMergeCommits(ctx context.Context, repository string) (bool, error)
	UsesMergeQueue(ctx context.Context, repository, target string) (bool, error)
	// BatchPullRequest finds the pull request whose head is branch, in any
	// state; number 0 means there is none.
	BatchPullRequest(ctx context.Context, repository, branch string) (githubchecks.PullRequestView, error)
	CreatePullRequest(ctx context.Context, repository, branch, base, title, body string) (int, error)
	UpdatePullRequestBody(ctx context.Context, repository string, number int, body string) error
	CommitChecks(ctx context.Context, repository, sha string) ([]githubchecks.RemoteCheck, error)
}

// batchGHCall runs one `gh` invocation and returns its stdout.
type batchGHCall func(ctx context.Context, args ...string) ([]byte, error)

func batchGH(ctx context.Context, args ...string) ([]byte, error) {
	response := githubExecute(ctx, "", args...)
	return response.Stdout, response.Err
}

// githubBatch is the production BatchGitHub over `gh api`.
type githubBatch struct {
	gh         batchGHCall
	mergeQueue func(context.Context, string, string) (*githubchecks.MergeQueuePolicy, error)
	inspector  ChecksPolicyInspector
}

// DefaultBatchGitHub returns the production adapter.
func DefaultBatchGitHub() BatchGitHub {
	return githubBatch{gh: batchGH, mergeQueue: githubchecks.TargetMergeQueue, inspector: defaultChecksPolicyInspectorInstance}
}

func (adapter githubBatch) PullRequest(ctx context.Context, repository string, number int) (githubchecks.PullRequestView, error) {
	var view githubchecks.PullRequestView
	raw, err := adapter.gh(ctx, "api", "repos/"+repository+"/pulls/"+strconv.Itoa(number))
	if err == nil {
		err = json.Unmarshal(raw, &view)
	}
	if err != nil {
		return view, fmt.Errorf("read %s#%d: %w", repository, number, err)
	}
	return view, nil
}

func (adapter githubBatch) IssueComment(ctx context.Context, repository string, id int64) (string, error) {
	var comment issueCommentBodyResponse
	raw, err := adapter.gh(ctx, "api", "repos/"+repository+"/issues/comments/"+strconv.FormatInt(id, 10))
	if err == nil {
		err = json.Unmarshal(raw, &comment)
	}
	if err != nil {
		return "", fmt.Errorf("read review comment %d: %w", id, err)
	}
	return comment.Body, nil
}

func (adapter githubBatch) AllowsMergeCommits(ctx context.Context, repository string) (bool, error) {
	var repo struct {
		AllowMergeCommit bool `json:"allow_merge_commit"`
	}
	raw, err := adapter.gh(ctx, "api", "repos/"+repository)
	if err == nil {
		err = json.Unmarshal(raw, &repo)
	}
	if err != nil {
		return false, fmt.Errorf("read %s settings: %w", repository, err)
	}
	return repo.AllowMergeCommit, nil
}

func (adapter githubBatch) UsesMergeQueue(ctx context.Context, repository, target string) (bool, error) {
	policy, err := adapter.mergeQueue(ctx, repository, target)
	return policy != nil, err
}

func (adapter githubBatch) BatchPullRequest(ctx context.Context, repository, branch string) (githubchecks.PullRequestView, error) {
	owner, _, _ := strings.Cut(repository, "/")
	var views []githubchecks.PullRequestView
	raw, err := adapter.gh(ctx, "api", "repos/"+repository+"/pulls?state=all&head="+owner+":"+branch)
	if err == nil {
		err = json.Unmarshal(raw, &views)
	}
	if err != nil {
		return githubchecks.PullRequestView{}, fmt.Errorf("find the pull request for %s: %w", branch, err)
	}
	if len(views) == 0 {
		return githubchecks.PullRequestView{}, nil
	}
	// The list endpoint omits merged; one exact read answers it.
	return adapter.PullRequest(ctx, repository, views[0].Number)
}

func (adapter githubBatch) CreatePullRequest(ctx context.Context, repository, branch, base, title, body string) (int, error) {
	var created struct {
		Number int `json:"number"`
	}
	raw, err := adapter.gh(ctx, "api", "--method", "POST", "repos/"+repository+"/pulls",
		"-f", "head="+branch, "-f", "base="+base, "-f", "title="+title, "-f", "body="+body)
	if err == nil {
		err = json.Unmarshal(raw, &created)
	}
	if err == nil && created.Number == 0 {
		err = errors.New("GitHub returned no pull request number")
	}
	if err != nil {
		return 0, fmt.Errorf("open the batch pull request: %w", err)
	}
	return created.Number, nil
}

func (adapter githubBatch) UpdatePullRequestBody(ctx context.Context, repository string, number int, body string) error {
	if _, err := adapter.gh(ctx, "api", "--method", "PATCH", "repos/"+repository+"/pulls/"+strconv.Itoa(number), "-f", "body="+body); err != nil {
		return fmt.Errorf("update %s#%d body: %w", repository, number, err)
	}
	return nil
}

func (adapter githubBatch) CommitChecks(ctx context.Context, repository, sha string) ([]githubchecks.RemoteCheck, error) {
	checks, _, reason := adapter.inspector.CommitChecks(ctx, githubchecks.PullRequestWaitOptions{Repository: repository, Head: sha})
	if reason != "" {
		return nil, fmt.Errorf("read checks for %s: %s", shortMergeRevision(sha), reason)
	}
	return checks, nil
}

// batchGit runs git in one checkout with separated streams, so stdout parses
// cleanly even when git also writes progress to stderr.
type batchGit struct {
	run     runner.Runner
	dir     string
	timeout time.Duration
}

func (git batchGit) exec(ctx context.Context, args ...string) (runner.Result, error) {
	if git.timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, git.timeout)
		defer cancel()
	}
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

// configArgs prefixes args with the batch's fixed merge options.
func configArgs(options []string, args ...string) []string {
	prefixed := make([]string, 0, 2*len(options)+len(args))
	for _, option := range options {
		prefixed = append(prefixed, "-c", option)
	}
	return append(prefixed, args...)
}
