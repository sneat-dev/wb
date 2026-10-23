//go:build e2e

package orchestrate

import (
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/gitcli"
	"github.com/sneat-dev/wb/internal/githubchecks"
	"github.com/sneat-dev/wb/internal/worktrees"
)

func TestE2ELandKeepCommitsRefusesFailedRewrittenHeadChecks(t *testing.T) { //nolint:paralleltest // newLandFixture owns provider environment with t.Setenv.
	fixture := newLandFixture(t, "feature/keep-failed-checks", "go.mod", "go.sum")
	if _, err := worktrees.Create(context.Background(), []string{"acme/app"}, worktrees.CreateOptions{
		ProjectsRoot: fixture.projects, Operation: "keep-failed-checks", Branch: "feature/keep-failed-checks", BranchChosen: true, Resume: true,
		WorkLog: worktrees.WorkLogOptions{Model: "test-model"},
	}); err != nil {
		t.Fatal(err)
	}
	canonicalHead := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD"))
	fixture.writeState(t, "head-from-remote", "true")
	options := landOptions(fixture)
	options.MergeMethod = "squash"
	options.MergeMethodExplicit = true
	options.NoAutoMerge = true
	options.KeepCommits = []string{fixture.commitSHAs[0]}
	options.Reason = "retain the independently bisectable first commit"
	options.BuildCommand = []string{"sh", "-c", "exit 0"}
	pushes := 0
	observed := &engineStageObservedRunner{Runner: defaultRunner, after: func(_ string, name string, args []string) error {
		want := []string{"push", "--force-with-lease=refs/heads/feature/keep-failed-checks:" + fixture.headSHA, "origin", "HEAD:refs/heads/feature/keep-failed-checks"}
		if name == "git" && reflect.DeepEqual(args, want) {
			pushes++
			fixture.writeState(t, "check-conclusion", "failure")
		}
		return nil
	}}
	options.git = gitcli.New(observed)
	result, err := LandPullRequest(context.Background(), options)
	if err != nil {
		t.Fatal(err)
	}
	if pushes != 1 || result.Outcome != LandFindings || result.RefusalCode != LandRefusalChecksFailed || result.HeadSHA == fixture.headSHA || result.BranchDeleted || result.AutoMergeArmed {
		t.Fatalf("pushes=%d result=%+v", pushes, result)
	}
	if result.Checks == nil || result.Checks.Status != githubchecks.PullRequestWaitFailed || result.Checks.Head != result.HeadSHA || !strings.HasPrefix(result.Reason, "the rewritten branch's own checks are not green: ") {
		t.Fatalf("wrong rewritten-head failure: %+v", result)
	}
	if !strings.Contains(result.SanctionedCommand, "--keep-commits") || !strings.Contains(result.SanctionedCommand, "--reason") {
		t.Fatalf("retry command=%q", result.SanctionedCommand)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "ls-remote", "origin", "refs/heads/feature/keep-failed-checks")); !strings.HasPrefix(got, result.HeadSHA+"\t") {
		t.Fatalf("rewritten branch not retained: %q", got)
	}
	if got := strings.TrimSpace(runEngineGit(t, fixture.canonical, "rev-parse", "HEAD")); got != canonicalHead {
		t.Fatalf("canonical checkout changed: %q", got)
	}
	if log := fixture.ghLog(t); strings.Contains(log, "--method PUT") && strings.Contains(log, "/merge") {
		t.Fatalf("failed rewritten checks attempted merge: %s", log)
	}
}
