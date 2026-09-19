package herdr

import "testing"

func TestAgentSessionHarnessSessionID(t *testing.T) {
	var nilSession *AgentSession
	if got := nilSession.HarnessSessionID(); got != "" {
		t.Fatalf("nil AgentSession.HarnessSessionID() = %q, want empty", got)
	}

	byName := &AgentSession{Agent: "claude", Kind: "name", Source: "herdr:claude", Value: "reviewer"}
	if got := byName.HarnessSessionID(); got != "" {
		t.Fatalf("AgentSession{Kind: name}.HarnessSessionID() = %q, want empty", got)
	}

	byID := &AgentSession{Agent: "claude", Kind: "id", Source: "herdr:claude", Value: "00000000-0000-4000-8000-000000000001"}
	if got := byID.HarnessSessionID(); got != "00000000-0000-4000-8000-000000000001" {
		t.Fatalf("AgentSession{Kind: id}.HarnessSessionID() = %q", got)
	}
}

func TestAgentHarnessSessionIDWithNilSession(t *testing.T) {
	agent := Agent{PaneID: "w1:p3"} // no Session, as an unrecognized-agent pane reports
	if got := agent.HarnessSessionID(); got != "" {
		t.Fatalf("Agent{Session: nil}.HarnessSessionID() = %q, want empty", got)
	}
}

func TestRawPaneOrAgentValid(t *testing.T) {
	if (rawPaneOrAgent{}).valid() {
		t.Fatal("zero-value rawPaneOrAgent reported valid")
	}
	if !(rawPaneOrAgent{PaneID: "w1:p1"}).valid() {
		t.Fatal("rawPaneOrAgent with a pane id reported invalid")
	}
}
