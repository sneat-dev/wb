package cmdpr

import (
	"context"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/orchestrate"
)

// A landing whose merge succeeded but whose cleanup failed exits with its own
// status and names the exact resume command (sneat-dev/wb#824): exit 1 reads as
// "not landed", which sends a caller back to re-land finished work.
func TestPullRequestLandExitsDistinctlyWhenTheMergeLandedButTheTailDidNot(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	result := orchestrate.PullRequestLandResult{
		Outcome: orchestrate.LandLandedIncomplete, RefusalCode: "branch-retirement-failed",
		Reason: "landed on main (abc) but the follow-up did not finish: boom", ResumeCommand: "wb pr land acme/app#7",
	}
	if result.ExitCode() != exitLandedIncomplete {
		t.Fatalf("exit = %d, want %d", result.ExitCode(), exitLandedIncomplete)
	}
	command, out := newOutputCapturingCommand()
	if err := printPullRequestLand(command, result); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"landed-incomplete: landed on main", "refusal: branch-retirement-failed", "resume with: wb pr land acme/app#7"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out.String())
		}
	}
}

func TestPullRequestLandAndCreateLandMapALandedIncompleteResultToExitThree(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	landed := orchestrate.PullRequestLandResult{
		Repository: "acme/app", PullRequest: 9, Outcome: orchestrate.LandLandedIncomplete,
		RefusalCode: "branch-retirement-failed", Reason: "landed on main but the follow-up did not finish: boom",
		ResumeCommand: "wb pr land acme/app#9",
	}
	deps.Land = func(context.Context, orchestrate.PullRequestLandOptions) (orchestrate.PullRequestLandResult, error) {
		return landed, nil
	}
	deps.Create = func(context.Context, orchestrate.PullRequestCreateOptions) (orchestrate.PullRequestCreateResult, error) {
		return orchestrate.PullRequestCreateResult{Outcome: orchestrate.CreateLandedIncomplete, LandResult: &landed, Reason: landed.Reason}, nil
	}
	projects := "/projects"
	for _, test := range []struct {
		name string
		args []string
	}{
		{"pr land text", []string{"pr", "land", "acme/app#9", "--non-interactive"}},
		{"pr land json", []string{"pr", "land", "acme/app#9", "--non-interactive", "--format", "json"}},
		{"pr create --land", []string{"pr", "create", "--land"}},
	} {
		var stdout, stderr strings.Builder
		_ = projects
		code := executeTest(New(testRuntime(), deps), test.args[1:], &stdout, &stderr)
		if code != exitLandedIncomplete {
			t.Errorf("%s: exit = %d, want %d\nstdout=%s\nstderr=%s", test.name, code, exitLandedIncomplete, stdout.String(), stderr.String())
		}
		if !strings.Contains(stderr.String(), "resume with: wb pr land acme/app#9") {
			t.Errorf("%s: the resume command is not on stderr: %q", test.name, stderr.String())
		}
	}
}

func TestPrintPullRequestLandSurfacesAFailedWriteOfTheResumeLine(t *testing.T) {
	t.Parallel()
	deps := testDependencies()
	_ = deps
	result := orchestrate.PullRequestLandResult{
		Outcome: orchestrate.LandLandedIncomplete, RefusalCode: "branch-retirement-failed", Reason: "boom",
		SanctionedCommand: "wb worktree guard .", ResumeCommand: "wb pr land acme/app#9",
	}
	for failAt := 1; failAt <= 5; failAt++ {
		if err := printPullRequestLand(newFailingCommand(failAt), result); err == nil {
			t.Errorf("a write failing at call %d was swallowed", failAt)
		}
	}
	if err := printPullRequestLand(newFailingCommand(6), result); err != nil {
		t.Errorf("only five lines are written: %v", err)
	}
}
