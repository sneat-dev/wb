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

// CommitsNotIn implements Git by patch identity.
//
// `git cherry` answers which commits base does not already carry *as patches*,
// which is the right question after a rebase-and-merge landing rewrites SHAs.
// It answers with SHAs; the patch ids come from `git patch-id --stable` over
// the same range, so two commits carrying one body of work on two branches are
// recognisably one patch even though their SHAs differ by construction.
//
// Subject text is carried as a label only. Keying on it would cluster two
// unrelated commits that happen to share a message, and would fail to cluster
// one change re-applied with an edited message.
func (git ExecGit) CommitsNotIn(ctx context.Context, dir, branch, base string) ([]Commit, error) {
	out, err := git.run(ctx, dir, "cherry", base, branch)
	if err != nil {
		return nil, fmt.Errorf("compare %s against %s in %s: %w", branch, base, dir, err)
	}
	var unabsorbed []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "+ ") {
			continue
		}
		if sha := strings.TrimSpace(strings.TrimPrefix(line, "+ ")); sha != "" {
			unabsorbed = append(unabsorbed, sha)
		}
	}
	if len(unabsorbed) == 0 {
		return nil, nil
	}
	subjects, err := git.commitSubjects(ctx, dir, unabsorbed)
	if err != nil {
		return nil, err
	}
	patchIDs, err := git.patchIDs(ctx, dir, base, branch)
	if err != nil {
		return nil, err
	}
	commits := make([]Commit, 0, len(unabsorbed))
	for _, sha := range unabsorbed {
		commits = append(commits, Commit{SHA: sha, Subject: subjects[sha], PatchID: patchIDs[sha]})
	}
	return commits, nil
}

func (git ExecGit) commitSubjects(ctx context.Context, dir string, shas []string) (map[string]string, error) {
	subjects := make(map[string]string, len(shas))
	for _, sha := range shas {
		out, err := git.run(ctx, dir, "log", "-1", "--format=%s", sha)
		if err != nil {
			return nil, fmt.Errorf("read the subject of %s in %s: %w", sha, dir, err)
		}
		subjects[sha] = strings.TrimSpace(out)
	}
	return subjects, nil
}

// patchIDs maps every commit in base..branch to its stable patch id.
//
// `git patch-id` reads a patch stream and prints "<patch-id> <commit-id>" per
// commit, so one `git log --patch` feeds one `git patch-id` and the whole range
// costs two child processes rather than two per commit. A commit Git declines
// to give a patch id (an empty commit) is simply absent from the map, and
// Commit.Identity falls back to its SHA rather than treating two unknowns as
// equal.
func (git ExecGit) patchIDs(ctx context.Context, dir, base, branch string) (map[string]string, error) {
	patch, err := git.run(ctx, dir, "log", "--patch", "--no-color", "--no-merges", "--format=commit %H", base+".."+branch)
	if err != nil {
		return nil, fmt.Errorf("read the patch range %s..%s in %s: %w", base, branch, dir, err)
	}
	if strings.TrimSpace(patch) == "" {
		return map[string]string{}, nil
	}
	out, err := git.runWithInput(ctx, dir, patch, "patch-id", "--stable")
	if err != nil {
		return nil, fmt.Errorf("compute patch identities in %s: %w", dir, err)
	}
	identities := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(strings.TrimSpace(line))
		if len(fields) != 2 {
			continue
		}
		identities[fields[1]] = fields[0]
	}
	return identities, nil
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

// DeleteRemoteBranch implements Git and asserts the effect: after the push
// that deletes the ref, origin must no longer resolve it.
func (git ExecGit) DeleteRemoteBranch(ctx context.Context, dir, branch string) error {
	if _, err := git.run(ctx, dir, "push", "origin", "--delete", branch); err != nil {
		// A branch that is already gone is the state the caller wanted; only
		// a still-present ref is a failure, which the check below decides.
		if _, present, headErr := git.RemoteHead(ctx, dir, branch); headErr == nil && !present {
			return nil
		}
		return fmt.Errorf("delete origin/%s from %s: %w", branch, dir, err)
	}
	if _, err := git.run(ctx, dir, "fetch", "--quiet", "--prune", "origin"); err != nil {
		return fmt.Errorf("re-read origin after deleting %s: %w", branch, err)
	}
	if _, present, err := git.RemoteHead(ctx, dir, branch); err != nil {
		return err
	} else if present {
		return fmt.Errorf("pushed a deletion of %s but origin/%s still resolves", branch, branch)
	}
	return nil
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

// PullRequest implements GitHub.
func (hub ExecGitHub) PullRequest(ctx context.Context, dir string, number int) (PullRequest, bool, error) {
	out, err := hub.run(ctx, dir, "pr", "view", strconv.Itoa(number), "--json", pullRequestFields)
	if err != nil {
		if strings.Contains(strings.ToLower(out), "could not resolve") || strings.Contains(strings.ToLower(out), "not found") {
			return PullRequest{}, false, nil
		}
		return PullRequest{}, false, fmt.Errorf("read pull request %d: %w", number, err)
	}
	var raw pullRequestJSON
	if err := json.Unmarshal([]byte(out), &raw); err != nil {
		return PullRequest{}, false, fmt.Errorf("parse pull request %d: %w", number, err)
	}
	return raw.toPullRequest(), true, nil
}

// ClosePullRequest implements GitHub and asserts the effect.
//
// `verbs-assert-effects-not-exit-codes` is a Principle-level MUST, and this is
// a destructive call whose result lands in a durable report: a `gh pr close`
// that exits 0 without closing — a permission edge, a race with a merge —
// must not be recorded as done.
func (hub ExecGitHub) ClosePullRequest(ctx context.Context, dir string, number int, comment string) error {
	if _, err := hub.run(ctx, dir, "pr", "close", strconv.Itoa(number), "--comment", comment); err != nil {
		return err
	}
	pullRequest, found, err := hub.PullRequest(ctx, dir, number)
	if err != nil {
		return fmt.Errorf("verify pull request %d closed: %w", number, err)
	}
	if !found {
		return fmt.Errorf("closed pull request %d but it no longer resolves; treat the close as unverified", number)
	}
	if strings.EqualFold(pullRequest.State, "OPEN") {
		return fmt.Errorf("`gh pr close` succeeded but pull request %d is still OPEN", number)
	}
	return nil
}

// RetargetPullRequest implements GitHub and asserts the effect.
func (hub ExecGitHub) RetargetPullRequest(ctx context.Context, dir string, number int, base string) error {
	if _, err := hub.run(ctx, dir, "pr", "edit", strconv.Itoa(number), "--base", base); err != nil {
		return err
	}
	pullRequest, found, err := hub.PullRequest(ctx, dir, number)
	if err != nil {
		return fmt.Errorf("verify pull request %d retargeted: %w", number, err)
	}
	if !found {
		return fmt.Errorf("retargeted pull request %d but it no longer resolves; treat the retarget as unverified", number)
	}
	if pullRequest.Base != base {
		return fmt.Errorf("`gh pr edit` succeeded but pull request %d still targets %q, not %q", number, pullRequest.Base, base)
	}
	return nil
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

func (git ExecGit) runWithInput(ctx context.Context, dir, input string, args ...string) (string, error) {
	return runBoundedWithInput(ctx, git.Timeout, dir, input, "git", args...)
}

func runBounded(ctx context.Context, timeout time.Duration, dir, name string, args ...string) (string, error) {
	return runBoundedWithInput(ctx, timeout, dir, "", name, args...)
}

func runBoundedWithInput(ctx context.Context, timeout time.Duration, dir, input, name string, args ...string) (string, error) {
	if timeout <= 0 {
		timeout = defaultCommandTimeout
	}
	bounded, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	command := exec.CommandContext(bounded, name, args...)
	command.Dir = dir
	command.Env = console.Env()
	if input != "" {
		command.Stdin = strings.NewReader(input)
	}
	output, err := command.CombinedOutput()
	// Child output routinely carries a remote URL with an embedded credential,
	// and this error string is persisted in stream.json and re-emitted in the
	// JSON envelope. Redaction therefore happens where the bytes are captured,
	// not only where an event is appended: a token that reaches the state file
	// has already leaked to whatever backs up the home directory.
	if err != nil {
		detail := RedactString(strings.TrimSpace(string(output)))
		if bounded.Err() != nil && ctx.Err() == nil {
			return detail, fmt.Errorf("%s %s timed out after %s: %s", name, strings.Join(args, " "), timeout, detail)
		}
		return detail, fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, detail)
	}
	return string(output), nil
}
