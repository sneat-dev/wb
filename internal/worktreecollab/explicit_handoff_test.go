package worktreecollab

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
)

func TestExplicitUnknownOwnerHandoffRequiresApprovalAndRetainsBinding(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{"approved", "no-approval", "live", "unreadable", "changed-revision", "reappeared-root", "becomes-live"} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			s, _, old, current, request := recoveryService(t)
			request.HandoffApproval = "User approved scoped recovery in originating chat"
			calls := 0
			s.Ports.OwnerStatus = func(string) (string, error) {
				calls++
				if scenario == "unreadable" {
					return "unknown", errors.New("registry unavailable")
				}
				if scenario == "live" || scenario == "becomes-live" && calls > 1 {
					return "live", nil
				}
				return "unknown", nil
			}
			switch scenario {
			case "no-approval":
				request.HandoffApproval = ""
			case "changed-revision":
				request.ExpectedRevision++
			case "reappeared-root":
				request.ExpectedRoot = current.Root
			}
			before := recoverySnapshotBytes(t, s, old)
			state, err := s.RecoverInactiveOwner(context.Background(), "worktree", request)
			if scenario != "approved" {
				if err == nil {
					t.Fatal("unsafe handoff accepted")
				}
				if after := recoverySnapshotBytes(t, s, old); !reflect.DeepEqual(before, after) {
					t.Fatal("rejected recovery changed state")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if state.Owner != "successor" || state.Checkout != current || state.OwnerEpoch != 2 || state.Revision != 2 || len(state.CheckoutRebinds) != 1 {
				t.Fatal("wrong handoff", state)
			}
			change := state.OwnerChanges[len(state.OwnerChanges)-1]
			var audit map[string]any
			if err = json.Unmarshal([]byte(change.Reason), &audit); err != nil || audit["handoff_approval"] != request.HandoffApproval || audit["owner_status"] != "unknown" || !change.Forced {
				t.Fatal("handoff authority/status missing from audit", change, err)
			}
		})
	}
}
