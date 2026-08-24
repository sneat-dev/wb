package worktrees

import (
	"os"
	"testing"
)

func viewFor(pid int) OwnerView {
	return OwnerView{
		OwnerRegistration: OwnerRegistration{PID: pid},
		PIDStatus:         ownerPIDStatus(pid),
	}
}

// Silence is not evidence of abandonment. Reporting it as orphaned is what
// made `wb worktree list` contradict `wb worktree orphans`.
func TestWorktreeOwnerStateReportsSilenceAsUnknown(t *testing.T) {
	if got := worktreeOwnerState(nil); got != "unknown" {
		t.Fatalf("worktreeOwnerState(no records) = %q, want unknown", got)
	}
}

// An entry WB wrote without a declaration is provenance, not a dead session.
func TestWorktreeOwnerStateTreatsProvenanceOnlyAsUnknown(t *testing.T) {
	if got := worktreeOwnerState([]OwnerView{viewFor(0)}); got != "unknown" {
		t.Fatalf("worktreeOwnerState(provenance only) = %q, want unknown", got)
	}
}

func TestWorktreeOwnerStateReportsALiveSession(t *testing.T) {
	if got := worktreeOwnerState([]OwnerView{viewFor(os.Getpid())}); got != "active" {
		t.Fatalf("worktreeOwnerState(live) = %q, want active", got)
	}
}

func TestWorktreeOwnerStateReportsAnExitedSession(t *testing.T) {
	if got := worktreeOwnerState([]OwnerView{viewFor(424242)}); got != "orphaned" {
		t.Fatalf("worktreeOwnerState(exited) = %q, want orphaned", got)
	}
}

// A dead session must not mask a live one, whichever order they were recorded.
func TestWorktreeOwnerStateFindsALiveSessionAnywhereInTheChain(t *testing.T) {
	cases := map[string][]OwnerView{
		"live last":  {viewFor(424242), viewFor(os.Getpid())},
		"live first": {viewFor(os.Getpid()), viewFor(424242)},
		"among provenance": {
			viewFor(0), viewFor(424242), viewFor(os.Getpid()), viewFor(0),
		},
	}
	for name, owners := range cases {
		t.Run(name, func(t *testing.T) {
			if got := worktreeOwnerState(owners); got != "active" {
				t.Fatalf("worktreeOwnerState = %q, want active", got)
			}
		})
	}
}

// The two commands answer the same question and must never disagree about the
// same worktree. This is the regression guard for the contradiction itself.
func TestListAndOrphansAgreeOnOwnerState(t *testing.T) {
	equivalent := map[string]string{
		OwnerLive:     "active",
		OwnerGone:     "orphaned",
		OwnerUnstated: "unknown",
	}
	cases := map[string][]OwnerView{
		"nothing recorded": nil,
		"provenance only":  {viewFor(0)},
		"live session":     {viewFor(os.Getpid())},
		"exited session":   {viewFor(424242)},
	}
	for name, owners := range cases {
		t.Run(name, func(t *testing.T) {
			listState := worktreeOwnerState(owners)

			// Mirror DeclaredOwner's rules over the same records.
			triageState := OwnerUnstated
			for _, owner := range owners {
				if owner.PID <= 0 {
					continue
				}
				if owner.PIDStatus == "active" {
					triageState = OwnerLive
					break
				}
				if owner.PIDStatus == "orphaned" {
					triageState = OwnerGone
				}
			}

			if want := equivalent[triageState]; listState != want {
				t.Fatalf("list says %q but triage says %q (want %q); the two commands disagree",
					listState, triageState, want)
			}
		})
	}
}
