package worktrees

import "testing"

func TestTerminalLockOperationNameBindsMergeReceipt(t *testing.T) {
	const receipt = "merge-sneat-co-bookius-main-c97cdfb3334d-1d2d9879e050"
	for _, task := range []string{
		"merge-sneat-co-bookius-main-c97cdfb3334d",
		"merge-sneat-co-bookius-main-c97cdfb3334d-a4381f8449a1",
	} {
		if !isTerminalLockOperationName(task, receipt) {
			t.Fatalf("expected %s to bind %s", task, receipt)
		}
	}
	for _, task := range []string{
		"merge-sneat-co-bookius-main-c97cdfb3334e",
		"merge-sneat-co-bookius-main-c97cdfb3334d-feature",
		"other-task",
	} {
		if isTerminalLockOperationName(task, receipt) {
			t.Fatalf("unrelated task %s bound to %s", task, receipt)
		}
	}
	if isTerminalLockOperationName("merge-sneat-co-bookius-main-c97cdfb3334d", "invalid-receipt") {
		t.Fatal("malformed receipt identity was accepted")
	}
}
