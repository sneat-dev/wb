//go:build e2e

package orchestrate

import (
	"os"
	"testing"
)

func missingCleanupOwnerFixture(t *testing.T) (engineFixture, WorktreeMergeReceipt) {
	t.Helper()
	f, _, r, claims := landedTerminalCleanupFixture(t)
	intent := WorktreeMergeLandOptions{Route: WorktreeMergeRouteAuto, Cleanup: true, OnFailure: "stop"}
	retainWorktreeMergeLandIntent(&r, &intent)
	if e := persistWorktreeMergeReceipt(r); e != nil {
		t.Fatal(e)
	}
	externallyTerminalizeMergeCleanup(t, f, &r)
	if e := os.Remove(terminalWorkLogPath(claims[r.Sources[0].Task])); e != nil {
		t.Fatal(e)
	}
	return f, r
}
