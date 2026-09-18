package provenance

import "testing"

// TestFromEnvReadsDeclaredFields proves every declared variable reaches the
// field the SDLC logging-gap analysis (wb#631) names for it.
func TestFromEnvReadsDeclaredFields(t *testing.T) {
	t.Setenv(EnvHarnessSessionID, "sess-abc123")
	t.Setenv(EnvHarness, "claude-code")
	t.Setenv(EnvEffortLevel, "high")
	t.Setenv(EnvAgentID, "agent-42")
	t.Setenv(EnvToolUseID, "toolu_01ABC")

	fields := FromEnv()
	if fields.HarnessSessionID != "sess-abc123" || fields.Harness != "claude-code" ||
		fields.EffortLevel != "high" || fields.AgentID != "agent-42" || fields.ToolUseID != "toolu_01ABC" {
		t.Fatalf("FromEnv did not read every declared field: %+v", fields)
	}
	if fields.WBVersion == "" {
		t.Fatal("WBVersion must never be empty")
	}
}

// TestFromEnvEmptyEnvironmentLeavesEveryDeclaredFieldEmpty pins the other
// half of the acceptance test: a claim built with nothing declared must not
// invent identity.
func TestFromEnvEmptyEnvironmentLeavesEveryDeclaredFieldEmpty(t *testing.T) {
	for _, name := range []string{EnvHarnessSessionID, EnvHarness, EnvEffortLevel, EnvAgentID, EnvToolUseID} {
		t.Setenv(name, "")
	}
	fields := FromEnv()
	if fields.HarnessSessionID != "" || fields.Harness != "" || fields.EffortLevel != "" ||
		fields.AgentID != "" || fields.ToolUseID != "" {
		t.Fatalf("FromEnv invented a field from an empty environment: %+v", fields)
	}
}

// TestSafeIDRejectsUnsafeValues proves an ID carrying shell metacharacters or
// whitespace is never treated as safe, whether read from the environment or
// checked directly by a caller such as the agent guard.
func TestSafeIDRejectsUnsafeValues(t *testing.T) {
	unsafe := []string{
		"", "agent 1", "agent;rm -rf /", "agent$(whoami)", "agent\n2", "agent'quote",
	}
	for _, value := range unsafe {
		if SafeID(value) {
			t.Fatalf("SafeID(%q) = true, want false", value)
		}
	}
	if !SafeID("agent-42_v2.1") {
		t.Fatal("SafeID rejected a compact token")
	}
}

// TestFromEnvDropsUnsafeDeclaredIDs proves an unsafe WB_AGENT_ID/
// WB_TOOL_USE_ID is omitted rather than carried through unescaped: a record
// is safer with a missing field than with one that failed this check.
func TestFromEnvDropsUnsafeDeclaredIDs(t *testing.T) {
	t.Setenv(EnvAgentID, "agent; rm -rf /")
	t.Setenv(EnvToolUseID, "tool one")
	t.Setenv(EnvHarnessSessionID, "sess one two")
	fields := FromEnv()
	if fields.AgentID != "" || fields.ToolUseID != "" || fields.HarnessSessionID != "" {
		t.Fatalf("FromEnv carried an unsafe ID through: %+v", fields)
	}
}
