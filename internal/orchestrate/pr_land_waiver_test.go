package orchestrate

import (
	"strings"
	"testing"
	"time"
)

func TestLandResumeCommandCarriesWaiver(t *testing.T) {
	t.Parallel()
	opts := PullRequestLandOptions{
		Repository:  "sneat-dev/wb",
		PullRequest: "844",
		WaiveChecks: []string{"Workers Builds: specscore-md", "optional-check"},
		WaiveReason: "known broken on main",
		MergeMethod: "merge",
		Slice:       50 * time.Minute,
	}

	cmd := pullRequestLandResumeCommand(opts, "844", "")
	if !strings.Contains(cmd, `--waive-check 'Workers Builds: specscore-md'`) {
		t.Errorf("resume command %q missing first waive-check", cmd)
	}
	if !strings.Contains(cmd, `--waive-check 'optional-check'`) {
		t.Errorf("resume command %q missing second waive-check", cmd)
	}
	if !strings.Contains(cmd, `--waive-reason 'known broken on main'`) {
		t.Errorf("resume command %q missing waive-reason", cmd)
	}
}

func TestPullRequestLandWaiveCommandDeduplicatesExisting(t *testing.T) {
	t.Parallel()
	opts := PullRequestLandOptions{
		Repository:  "acme/app",
		WaiveChecks: []string{"Workers Builds: specscore-md"},
	}
	cmd := pullRequestLandWaiveCommand(opts, "7", []string{"Workers Builds: specscore-md"}, "main", "1234567890abcdef")
	count := strings.Count(cmd, "Workers Builds: specscore-md")
	if count != 1 {
		t.Errorf("waive command duplicated existing waive-check: count=%d, cmd=%s", count, cmd)
	}
}
