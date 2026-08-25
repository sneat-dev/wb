package main

import (
	"bytes"
	"strings"
	"testing"

	"github.com/sneat-dev/wb/internal/fleetsync"
)

func TestPrintSyncSummaryReportsFreshRemoteUpdates(t *testing.T) {
	var out bytes.Buffer
	printSyncSummary(&out, []fleetsync.Result{
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullAttempted: true, PullSucceeded: true},
		{Status: fleetsync.Unpushed, PullAttempted: true, PullSucceeded: true, Updated: true},
		{Status: fleetsync.Pulled, PullPlanned: true},
	})
	for _, want := range []string{
		"Pulled              3",
		"Pull planned        1",
		"Pull attempted      3",
		"Pull succeeded      3",
		"Updated from remote 2",
		"Already current     1",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary %q does not contain %q", out.String(), want)
		}
	}
}
