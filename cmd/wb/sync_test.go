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
		{Status: fleetsync.Pulled, Updated: true},
		{Status: fleetsync.Pulled},
		{Status: fleetsync.Unpushed, Updated: true},
	})
	for _, want := range []string{"Pulled              2", "Updated from remote 2"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("summary %q does not contain %q", out.String(), want)
		}
	}
}
