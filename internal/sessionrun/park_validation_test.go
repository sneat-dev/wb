package sessionrun

import (
	"github.com/sneat-dev/wb/internal/sessionpark"
	"strings"
	"testing"
)

func TestValidateParkedRemoteBundleRejectsEmptyWorktrees(t *testing.T) {
	t.Parallel()
	err := validateParkedRemoteBundle(sessionpark.Bundle{ParkedSessionID: "p1"}, "vm-2")
	if err == nil || !strings.Contains(err.Error(), "between 1 and") {
		t.Fatalf("want empty-worktrees refusal, got %v", err)
	}
}
func TestValidateParkedRemoteBundleRejectsDirtyWorktree(t *testing.T) {
	t.Parallel()
	err := validateParkedRemoteBundle(sessionpark.Bundle{
		ParkedSessionID: "p1",
		Worktrees: []sessionpark.Worktree{{
			Repository: "acme/app", WorktreeDir: "/wt/app", Head: "aaa", RemoteHead: "aaa",
			WorkLogReference: "wl-1", OwnerEventID: "ev-1", Dirty: true,
		}},
	}, "vm-2")
	if err == nil || !strings.Contains(err.Error(), "not remotely reconstructable") {
		t.Fatalf("want dirty-worktree refusal, got %v", err)
	}
}
func TestParkResumeDiagnosticDirEmptyInputsYieldEmptyPath(t *testing.T) {
	t.Parallel()
	if got := parkResumeDiagnosticDir("", "p1"); got != "" {
		t.Fatalf("want empty diagnostic dir for empty home, got %q", got)
	}
	if got := parkResumeDiagnosticDir("/home/x", ""); got != "" {
		t.Fatalf("want empty diagnostic dir for empty session id, got %q", got)
	}
}
func TestParkResumeDiagnosticDirJoinsHomeAndSession(t *testing.T) {
	t.Parallel()
	got := parkResumeDiagnosticDir("/home/x", "p1")
	if !strings.Contains(got, "p1") || !strings.Contains(got, "worklogs") {
		t.Fatalf("want a worklogs diagnostic path naming the session, got %q", got)
	}
}
func TestParkedRemoteSSHConfigRejectsMismatchedTarget(t *testing.T) {
	t.Parallel()
	route := &sessionpark.ResumeRoute{Mode: sessionpark.ResumeRouteLocal, TargetMachine: "vm-1"}
	_, err := parkedRemoteSSHConfig(route, "vm-2", "", "")
	if err == nil || !strings.Contains(err.Error(), "already claimed") {
		t.Fatalf("want mismatched-target refusal, got %v", err)
	}
}
