package orchestrate_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/sneat-dev/wb/internal/orchestrate"
	"github.com/sneat-dev/wb/internal/testenv"
	"github.com/spf13/pflag"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

const (
	ciWaitHead       = "0123456789012345678901234567890123456789"
	ciWaitTargetHead = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	// ciWaitSliceBudget outlasts every observation these tests expect by orders
	// of magnitude. A slice deadline cancels the in-flight gh command, so a
	// budget a loaded runner can reach silently drops observations the receipts
	// below count.
	ciWaitSliceBudget = 5 * time.Minute
	// ciWaitSingleObservationInterval leaves WB no room for a second observation
	// within the budget: the poll-budget guard refuses to start one whose
	// interval would overrun the slice, so the slice ends on the observation
	// contract rather than on the clock and observes exactly once on any runner.
	ciWaitSingleObservationInterval = ciWaitSliceBudget - time.Millisecond
	// ciWaitRereadInterval keeps a terminal observation and its stable reread
	// back to back inside one slice.
	ciWaitRereadInterval = 100 * time.Millisecond
)

// ciWaitObservations reads the fake gh receipt counter, which records one entry
// per observation of the exact head's checks.
func ciWaitObservations(t *testing.T, state string) int {
	t.Helper()
	contents, err := os.ReadFile(state)
	if errors.Is(err, os.ErrNotExist) {
		return 0
	}
	if err != nil {
		t.Fatalf("read observation counter %s: %v", state, err)
	}
	count, err := strconv.Atoi(strings.TrimSpace(string(contents)))
	if err != nil {
		t.Fatalf("parse observation counter %q: %v", contents, err)
	}
	return count
}

const (
	exitOK       = 0
	exitFindings = 1
)

// observeForTest preserves the existing fixture request vectors while exercising
// the actual observer operation directly, without a CLI command or fake waiter.
func observeForTest(t *testing.T, args []string, out, diagnostic *bytes.Buffer) int {
	t.Helper()
	options := orchestrate.PullRequestWaitOptions{}
	flags := pflag.NewFlagSet("observer-fixture", pflag.ContinueOnError)
	flags.SetOutput(diagnostic)
	flags.StringVar(&options.Repository, "repo", "", "")
	flags.StringVar(&options.PullRequest, "pr", "", "")
	flags.StringVar(&options.Target, "target", "", "")
	flags.StringVar(&options.Head, "head", "", "")
	flags.DurationVar(&options.Slice, "slice", 8*time.Minute, "")
	flags.DurationVar(&options.CheckPollInterval, "interval", orchestrate.DefaultCheckPollInterval, "")
	flags.Bool("json", false, "")
	flags.String("format", "text", "")
	if err := flags.Parse(args[2:]); err != nil {
		t.Fatal(err)
	}
	result, err := orchestrate.WaitForCommitChecks(context.Background(), options)
	if err != nil {
		fmt.Fprintln(diagnostic, err)
		return exitFindings
	}
	if err := json.NewEncoder(out).Encode(result); err != nil {
		t.Fatal(err)
	}
	if result.Status == orchestrate.PullRequestWaitPassed {
		return exitOK
	}
	return exitFindings
}

func writeCIWaitExecutable(t *testing.T, path, contents string) {
	t.Helper()
	if !strings.Contains(contents, "/actions/runs?head_sha=") {
		const response = `if [ "$1" = api ] && echo "$2" | grep -q '/actions/runs?head_sha='; then
  echo '{"total_count":0,"workflow_runs":[]}'
  exit 0
fi
`
		contents = strings.Replace(contents, "#!/bin/sh\n", "#!/bin/sh\n"+response, 1)
	}
	// Every fake GitHub process must observe only the responses prepared by
	// this test. Reusing the real per-user observer cache lets another test or
	// WB process supply a fresh cached response for the same acme/app fixture.
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	// Sharded package tests are separate processes. Isolate WB's private state
	// too, so a concurrent shard cannot supply or replace observer evidence.
	t.Setenv("WB_PROJECTS_ROOT", t.TempDir())
	if err := testenv.WriteExecutableFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}
