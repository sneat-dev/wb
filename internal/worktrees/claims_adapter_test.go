package worktrees

import "testing"

func TestClaimsPreflightAdapter(t *testing.T) {
	t.Parallel()
	if err := PreflightWorkLogOptions("task", WorkLogOptions{Model: "model", RunID: "run"}); err != nil {
		t.Fatal(err)
	}
	if err := PreflightWorkLogOptions("task", WorkLogOptions{Model: "model", RunID: "bad/path"}); err == nil {
		t.Fatal("accepted unsafe run")
	}
}

func TestClaimsBindingAdapterRejectsInvalidRoot(t *testing.T) {
	t.Parallel()
	if _, err := claimBindingPorts().Homes("\x00"); err == nil {
		t.Fatal("accepted invalid projects root")
	}
}
