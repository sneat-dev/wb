package main

import (
	"bytes"
	"testing"
)

func TestRemoteReleaseAdaptersPreserveAdvisoriesAndLeakStatus(t *testing.T) {
	t.Parallel()
	var out bytes.Buffer
	skipped := skippedAutoRelease(&out, "no eligible checkout")
	if skipped.Outcome != "skipped" || skipped.Detail != "no eligible checkout" || skipped.Leaked() {
		t.Fatalf("skipped root advisory = %+v", skipped)
	}
	if got := out.String(); got != "remote claim release skipped: no eligible checkout\n" {
		t.Fatalf("skipped root diagnostic = %q", got)
	}
	out.Reset()
	failed := failedAutoRelease(&out, "task-a", "store unavailable")
	if failed.Outcome != "failed" || failed.Detail != "store unavailable" || !failed.Leaked() {
		t.Fatalf("failed root advisory = %+v", failed)
	}
	if got := out.String(); got != "remote claim release FAILED: task-a claim was not released (its worktree is already gone): store unavailable\n" {
		t.Fatalf("failed root diagnostic = %q", got)
	}
}
