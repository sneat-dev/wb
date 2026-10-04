package main

import (
	"bytes"
	"strings"
	"testing"
)

// The retired operations pages have no flag to open them: their routes answer
// 404 and the daemon serves Cockpit instead.
func TestDashboardHasNoFlagForTheRetiredPages(t *testing.T) {
	for _, flag := range []string{"metrics", "coverage"} {
		if newDashboardCmd(&invocation{}).Flags().Lookup(flag) != nil {
			t.Errorf("wb dashboard still has --%s", flag)
		}
		// Parsing fails before the command runs, so nothing is started.
		var stdout, stderr bytes.Buffer
		if got := run([]string{"dashboard", "--" + flag}, &stdout, &stderr); got != exitUsage || !strings.Contains(stderr.String(), "unknown flag") {
			t.Errorf("wb dashboard --%s exited %d with %q, want the usage exit %d and an unknown flag", flag, got, stderr.String(), exitUsage)
		}
	}
}

func TestNewDashboardCmd(t *testing.T) {
	t.Parallel()
	cmd := newDashboardCmd(&invocation{})
	if cmd == nil || cmd.Use != "dashboard" {
		t.Fatalf("expected dashboard command, got %v", cmd)
	}
}
