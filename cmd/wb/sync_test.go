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
		"Final outcomes",
		"Pulled              3",
		"Pull actions",
		"Pull planned        1",
		"Pull attempted      3",
		"Pull succeeded      3",
		"Updated from remote 2",
		"Already current     1",
		"Attention",
		"Failures",
	} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary %q does not contain %q", out.String(), want)
		}
	}
	sections := []string{"Final outcomes", "Pull actions", "Attention", "Failures"}
	previous := -1
	for _, section := range sections {
		index := strings.Index(out.String(), section)
		if index <= previous {
			t.Fatalf("summary sections are not ordered %v: %q", sections, out.String())
		}
		previous = index
	}
}
