package worktreeclaims

import (
	"github.com/sneat-dev/wb/internal/worktreejournal"
	"testing"
	"time"
)

func TestRegisteredOwnerEventsBindLogicalWBIDWhilePreservingNativeActor(t *testing.T) {
	t.Parallel()
	identity := AgentIdentity{PID: 123, Runtime: "codex", AgentID: "native-chat", WBSessionID: "wbs-current", Registered: true, Model: "known"}
	var events []worktreejournal.LocalWorkLogEvent
	ports := OwnerPorts{Version: func() string { return "fixture" }, Now: func() time.Time { return time.Unix(10, 0) }, MutationInitiator: func() string { return "fixture" }, CurrentIdentity: func() AgentIdentity { return identity }, ReadEvents: func(string) ([]worktreejournal.LocalWorkLogEvent, error) { return events, nil }, AppendEvent: func(_ string, event worktreejournal.LocalWorkLogEvent) error {
		events = append(events, event)
		return nil
	}, ProcessStatus: func(int) error { return nil }}
	owner, err := ports.RecordOwner("owned-worktree", "effort", "codex", "known", identity.PID)
	if err != nil || owner.Agent != "codex/wbs-current" || len(events) != 1 || ExtraString(events[0].Extra, "owner_wb_session_id") != identity.WBSessionID {
		t.Fatalf("registered creation binding: %#v %#v %v", owner, events, err)
	}
	if err := ports.RecordCustody("owned-worktree", "effort", "ordinary command", identity); err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 {
		t.Fatal("unchanged registered custody created duplicate owner")
	}
	identity.Model = "enriched"
	if err := ports.RecordCustody("owned-worktree", "effort", "ordinary command", identity); err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 || events[1].Owner.Agent != "codex/wbs-current" || ExtraString(events[1].Extra, "owner_wb_session_id") != identity.WBSessionID {
		t.Fatal("ordinary command lost explicit logical binding")
	}
	if identity.Agent() != "codex/native-chat" {
		t.Fatal("immutable native actor attribution was rewritten")
	}
	// An explicit foreign owner declaration must not inherit this invocation's
	// registered identity merely because a process ID is present.
	foreign, err := ports.RecordOwner("owned-worktree", "effort", "codex/foreign", "known", identity.PID)
	if err != nil || foreign.Agent != "codex/foreign" || events[len(events)-1].Extra != nil {
		t.Fatalf("foreign explicit declaration acquired current binding: %#v %v", foreign, err)
	}
}
