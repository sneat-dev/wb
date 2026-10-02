//go:build e2e

package worktrees

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/wbhome"
	"github.com/sneat-dev/wb/internal/worktreelanding"
)

// closedDuplicate prepares the datatug/backstage shape: a pushed branch whose
// pull request was closed unmerged because a twin landed another way.
func closedDuplicate(t *testing.T, task string) (*gitFixture, CreateResult, githubPullRequest) {
	t.Helper()
	fixture := newGitFixture(t)
	created, err := Create(context.Background(), []string{"acme/app"}, CreateOptions{
		ProjectsRoot: fixture.projectsRoot, Operation: task, WorkLog: WorkLogOptions{Model: "unknown"},
	})
	if err != nil {
		t.Fatal(err)
	}
	worktree := created[0]
	if err := os.WriteFile(filepath.Join(worktree.WorktreeDir, "duplicate.txt"), []byte("landed elsewhere\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitTest(t, worktree.WorktreeDir, "add", "duplicate.txt")
	gitTest(t, worktree.WorktreeDir, "commit", "-m", "the duplicate")
	gitTest(t, worktree.WorktreeDir, "push", "-u", "origin", worktree.Branch)
	pull := githubPullRequest{
		Number: 6, URL: "https://github.com/acme/app/pull/6", State: "closed",
		Head: worktreelanding.GitHubRef{Ref: worktree.Branch, SHA: gitTestOutput(t, worktree.WorktreeDir, "rev-parse", "HEAD")},
		Base: worktreelanding.GitHubRef{Ref: "main"},
	}
	return fixture, worktree, pull
}

func closedPullRequestPorts(answers ...func() (*githubPullRequest, string, error)) abortPorts {
	ports := productionAbortPorts()
	call := 0
	ports.closedPullRequest = func(context.Context, string, string, string, string) (*githubPullRequest, string, error) {
		answer := answers[min(call, len(answers)-1)]
		call++
		return answer()
	}
	return ports
}

func answerWith(pull githubPullRequest) func() (*githubPullRequest, string, error) {
	return func() (*githubPullRequest, string, error) { return &pull, "", nil }
}

func closedAbortOptions(fixture *gitFixture, task string) AbortOptions {
	return AbortOptions{
		ProjectsRoot: fixture.projectsRoot, Task: task, Disposition: AbortDiscarded,
		ClosedPullRequest: "6", Reason: "duplicate of pull request 5, which squash-landed the same content",
	}
}

func TestE2EAbortDiscardedClosedPullRequestRetiresADuplicateWithAnAuditRecord(t *testing.T) {
	fixture, created, pull := closedDuplicate(t, "closed-dup")
	ports := closedPullRequestPorts(answerWith(pull))

	options := closedAbortOptions(fixture, "closed-dup")
	plan, err := abortWithPorts(context.Background(), options, ports)
	if err != nil || len(plan) != 1 || !plan[0].Eligible || plan[0].ClosedPullRequest == nil || plan[0].ClosedPullRequest.Number != 6 {
		t.Fatalf("dry run = %#v, %v", plan, err)
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("dry run removed the worktree: %v", statErr)
	}

	options.DeleteRemote, options.Apply = true, true
	results, err := abortWithPorts(context.Background(), options, ports)
	if err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 || !results[0].Applied || !results[0].WorktreeGone || !results[0].BranchDeleted || !results[0].RemoteDeleted {
		t.Fatalf("closed-duplicate discard = %#v", results)
	}
	if _, statErr := os.Stat(created.WorktreeDir); !os.IsNotExist(statErr) {
		t.Fatalf("worktree remains: %v", statErr)
	}
	if head, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created.Branch); remoteErr != nil || head != "" {
		t.Fatalf("remote branch remains at %q: %v", head, remoteErr)
	}
	evidence := results[0].ClosedPullRequest
	if evidence == nil || evidence.AuditPath == "" || evidence.HeadSHA != pull.Head.SHA {
		t.Fatalf("evidence = %#v", evidence)
	}
	home, err := wbhome.Resolve(fixture.projectsRoot)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Dir(evidence.AuditPath) != filepath.Join(home.Write.Home, "closed-pr-discards") {
		t.Fatalf("audit record %s is not under WB home %s", evidence.AuditPath, home.Write.Home)
	}
	raw, err := os.ReadFile(evidence.AuditPath)
	if err != nil {
		t.Fatal(err)
	}
	var audit closedPullRequestAudit
	if err := json.Unmarshal(raw, &audit); err != nil {
		t.Fatal(err)
	}
	if audit.Task != "closed-dup" || audit.Repository != "acme/app" || audit.HeadSHA != pull.Head.SHA ||
		audit.Evidence.Number != 6 || !strings.Contains(audit.Evidence.Reason, "duplicate of pull request 5") {
		t.Fatalf("audit = %#v", audit)
	}
}

func TestE2EAbortDiscardedClosedPullRequestRefusesUnprovenShapes(t *testing.T) {
	driftedHead := func(pull githubPullRequest) githubPullRequest {
		pull.Head.SHA = strings.Repeat("a", 40)
		return pull
	}
	otherBranch := func(pull githubPullRequest) githubPullRequest {
		pull.Head.Ref = "someone-elses-branch"
		return pull
	}
	for _, test := range []struct {
		name    string
		prepare func(t *testing.T, worktree CreateResult, pull githubPullRequest) func() (*githubPullRequest, string, error)
		want    string
	}{
		{"the pull request head is not the checkout head", func(t *testing.T, _ CreateResult, pull githubPullRequest) func() (*githubPullRequest, string, error) {
			return answerWith(driftedHead(pull))
		}, "commits the pull request never carried"},
		{"the pull request head branch is another branch", func(t *testing.T, _ CreateResult, pull githubPullRequest) func() (*githubPullRequest, string, error) {
			return answerWith(otherBranch(pull))
		}, "is not this checkout's branch"},
		{"the pull request was merged or is still open", func(*testing.T, CreateResult, githubPullRequest) func() (*githubPullRequest, string, error) {
			return func() (*githubPullRequest, string, error) {
				return nil, "--closed-pr pull request acme/app#6 is not closed", nil
			}
		}, "is not closed"},
		{"GitHub cannot be read", func(*testing.T, CreateResult, githubPullRequest) func() (*githubPullRequest, string, error) {
			return func() (*githubPullRequest, string, error) { return nil, "", errors.New("rate limited") }
		}, "rate limited"},
		{"the checkout has uncommitted bytes", func(t *testing.T, worktree CreateResult, pull githubPullRequest) func() (*githubPullRequest, string, error) {
			if err := os.WriteFile(filepath.Join(worktree.WorktreeDir, "wip.txt"), []byte("not in the pull request\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			return answerWith(pull)
		}, "requires a clean worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture, created, pull := closedDuplicate(t, "closed-refused")
			ports := closedPullRequestPorts(test.prepare(t, created, pull))
			options := closedAbortOptions(fixture, "closed-refused")
			options.DeleteRemote, options.Apply = true, true
			_, err := abortWithPorts(context.Background(), options, ports)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
			if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
				t.Fatalf("a refused discard removed the worktree: %v", statErr)
			}
			if head, remoteErr := remoteBranchHead(context.Background(), fixture.canonical, created.Branch); remoteErr != nil || head == "" {
				t.Fatalf("a refused discard changed the remote branch: %q %v", head, remoteErr)
			}
		})
	}
}

func TestE2EAbortDiscardedClosedPullRequestReprovesUnderTheTaskLock(t *testing.T) {
	fixture, created, pull := closedDuplicate(t, "closed-reprove")
	drifted := pull
	drifted.Head.SHA = strings.Repeat("b", 40)
	// The plan sees the proof, the re-proof before removal sees GitHub change.
	ports := closedPullRequestPorts(answerWith(pull), answerWith(drifted))
	options := closedAbortOptions(fixture, "closed-reprove")
	options.DeleteRemote, options.Apply = true, true
	_, err := abortWithPorts(context.Background(), options, ports)
	if err == nil || !strings.Contains(err.Error(), "no longer verifies") {
		t.Fatalf("error = %v", err)
	}
	if _, statErr := os.Stat(created.WorktreeDir); statErr != nil {
		t.Fatalf("worktree removed despite a failed re-proof: %v", statErr)
	}

	// A GitHub read failure at the re-proof refuses too.
	ports = closedPullRequestPorts(answerWith(pull), func() (*githubPullRequest, string, error) { return nil, "", errors.New("gone") })
	if _, err := abortWithPorts(context.Background(), options, ports); err == nil || !strings.Contains(err.Error(), "could not be re-read") {
		t.Fatalf("re-read error = %v", err)
	}
}
