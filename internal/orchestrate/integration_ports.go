package orchestrate

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sneat-dev/wb/internal/runner"
)

// CommitComment is one GitHub commit comment.
type CommitComment struct {
	Body     string
	CommitID string
}

// IntegrationGitHub is every GitHub read the integration verbs make.
type IntegrationGitHub interface {
	// CommitComment reads one commit comment and the commit it is on.
	CommitComment(ctx context.Context, repository string, id int64) (CommitComment, error)
}

// integrationGHCall runs one `gh` invocation and returns its stdout.
type integrationGHCall func(ctx context.Context, args ...string) ([]byte, error)

func integrationGH(ctx context.Context, args ...string) ([]byte, error) {
	response := githubExecute(ctx, "", args...)
	return response.Stdout, response.Err
}

// githubIntegration is the production IntegrationGitHub over `gh api`.
type githubIntegration struct{ gh integrationGHCall }

// DefaultIntegrationGitHub returns the production adapter.
func DefaultIntegrationGitHub() IntegrationGitHub {
	return githubIntegration{gh: integrationGH}
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
