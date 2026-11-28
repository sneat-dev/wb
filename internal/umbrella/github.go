package umbrella

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strconv"
	"strings"
)

// CommandResult captures stdout, stderr, and error from one gh invocation.
type CommandResult struct {
	Stdout []byte
	Stderr []byte
	Err    error
}

// GitHubExecutor runs `gh` with args. Nil defaults to exec.CommandContext("gh", ...).
type GitHubExecutor func(ctx context.Context, args ...string) CommandResult

func defaultGitHubExecutor(ctx context.Context, args ...string) CommandResult {
	cmd := exec.CommandContext(ctx, "gh", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	return CommandResult{Stdout: stdout.Bytes(), Stderr: stderr.Bytes(), Err: err}
}

// GitHubProvider implements Provider against GitHub Issues via `gh api`.
// Every call uses `gh api` so no newer gh subcommand flag is required.
type GitHubProvider struct {
	exec GitHubExecutor
}

// NewGitHubProvider builds a GitHub Issues umbrella provider.
func NewGitHubProvider(exec GitHubExecutor) *GitHubProvider {
	if exec == nil {
		exec = defaultGitHubExecutor
	}
	return &GitHubProvider{exec: exec}
}

// Registry returns "github".
func (p *GitHubProvider) Registry() string {
	return RegistryGitHub
}

// RenderLink returns GitHub's native cross-repo issue link "owner/repo#N".
func (p *GitHubProvider) RenderLink(ref Ref) (string, error) {
	owner, repo, number, err := parseGitHubRef(ref)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s/%s#%d", owner, repo, number), nil
}

type githubIssuePayload struct {
	State       string          `json:"state"`
	PullRequest json.RawMessage `json:"pull_request,omitempty"`
}

// Resolve fetches the issue state from GitHub without retaining title or body.
func (p *GitHubProvider) Resolve(ctx context.Context, ref Ref) (Info, error) {
	owner, repo, number, err := parseGitHubRef(ref)
	if err != nil {
		return Info{}, err
	}
	endpoint := fmt.Sprintf("repos/%s/%s/issues/%d", owner, repo, number)
	res := p.exec(ctx, "api", endpoint)
	if res.Err != nil {
		return Info{}, fmt.Errorf("resolve umbrella %s: %s", ref.String(), commandErrorDetail(res))
	}
	var payload githubIssuePayload
	if err := json.Unmarshal(res.Stdout, &payload); err != nil {
		return Info{}, fmt.Errorf("decode umbrella %s: %w", ref.String(), err)
	}
	if prRaw := strings.TrimSpace(string(payload.PullRequest)); prRaw != "" && prRaw != "null" {
		return Info{}, fmt.Errorf("umbrella %s is a pull request, not an issue", ref.String())
	}
	var state State
	switch strings.ToLower(strings.TrimSpace(payload.State)) {
	case "open":
		state = StateOpen
	case "closed":
		state = StateClosed
	default:
		return Info{}, fmt.Errorf("umbrella %s returned unexpected GitHub state %q", ref.String(), payload.State)
	}
	canonical := Ref{Registry: RegistryGitHub, ID: fmt.Sprintf("%s/%s#%d", owner, repo, number)}
	return Info{
		Ref:   canonical,
		State: state,
		Link:  canonical.ID,
	}, nil
}

// AppendStatus posts a single-line status comment on the GitHub issue.
func (p *GitHubProvider) AppendStatus(ctx context.Context, ref Ref, line string) error {
	owner, repo, number, err := parseGitHubRef(ref)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(line)
	if body == "" {
		return fmt.Errorf("status comment for umbrella %s must not be empty", ref.String())
	}
	endpoint := fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, number)
	res := p.exec(ctx, "api", "--method", "POST", endpoint, "-f", "body="+body)
	if res.Err != nil {
		return fmt.Errorf("post status to umbrella %s: %s", ref.String(), commandErrorDetail(res))
	}
	return nil
}

// Close posts the final summary comment and closes the GitHub issue.
func (p *GitHubProvider) Close(ctx context.Context, ref Ref, summary string) error {
	owner, repo, number, err := parseGitHubRef(ref)
	if err != nil {
		return err
	}
	body := strings.TrimSpace(summary)
	if body == "" {
		return fmt.Errorf("close summary for umbrella %s must not be empty", ref.String())
	}
	commentEndpoint := fmt.Sprintf("repos/%s/%s/issues/%d/comments", owner, repo, number)
	commentRes := p.exec(ctx, "api", "--method", "POST", commentEndpoint, "-f", "body="+body)
	if commentRes.Err != nil {
		return fmt.Errorf("post closing summary to umbrella %s: %s", ref.String(), commandErrorDetail(commentRes))
	}
	issueEndpoint := fmt.Sprintf("repos/%s/%s/issues/%s", owner, repo, strconv.Itoa(number))
	closeRes := p.exec(ctx, "api", "--method", "PATCH", issueEndpoint, "-f", "state=closed")
	if closeRes.Err != nil {
		return fmt.Errorf("close umbrella %s: %s", ref.String(), commandErrorDetail(closeRes))
	}
	return nil
}

func parseGitHubRef(ref Ref) (owner, repo string, number int, err error) {
	if strings.ToLower(strings.TrimSpace(ref.Registry)) != RegistryGitHub {
		return "", "", 0, fmt.Errorf("GitHub provider cannot handle registry %q", ref.Registry)
	}
	return ParseGitHubID(ref.ID)
}

func commandErrorDetail(res CommandResult) string {
	if msg := strings.TrimSpace(string(res.Stderr)); msg != "" {
		return msg
	}
	if msg := strings.TrimSpace(string(res.Stdout)); msg != "" {
		return msg
	}
	return res.Err.Error()
}
