package testenv

import (
	"os"
	"testing"
)

func TestIsolateClearsAgentVarsAndPinsGoworkOff(t *testing.T) {
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("WB_AGENT_PID", "999")
	t.Setenv("WB_AGENT_RUNTIME", "claude-code")
	t.Setenv("WB_AGENT_MODEL", "claude-sonnet-5")
	t.Setenv("GOWORK", "/ambient/go.work")

	Isolate(t)

	for _, name := range []string{"WB_AGENT_ID", "WB_AGENT_PID", "WB_AGENT_RUNTIME", "WB_AGENT_MODEL"} {
		if value := os.Getenv(name); value != "" {
			t.Fatalf("%s = %q after Isolate, want empty", name, value)
		}
	}
	if value := os.Getenv("GOWORK"); value != "off" {
		t.Fatalf("GOWORK = %q after Isolate, want %q", value, "off")
	}
}

func TestIsolateLetsATestSetItsOwnAgentVarAfterward(t *testing.T) {
	t.Setenv("WB_AGENT_ID", "outer-session")
	Isolate(t)
	// A test that intentionally exercises agent-mode behavior sets its own
	// value after Isolate, and that is the one the code under test observes.
	t.Setenv("WB_AGENT_ID", "intentional-session")
	if value := os.Getenv("WB_AGENT_ID"); value != "intentional-session" {
		t.Fatalf("WB_AGENT_ID = %q, want the value set after Isolate", value)
	}
}

func TestIsolateProcessClearsAgentVarsAndPinsGoworkOff(t *testing.T) {
	// IsolateProcess uses os.Setenv/Unsetenv directly (it is meant for
	// TestMain, which has no *testing.T to restore through), so this test
	// restores the environment itself via t.Setenv/t.Cleanup rather than
	// relying on automatic restoration.
	t.Setenv("WB_AGENT_ID", "outer-session")
	t.Setenv("GOWORK", "/ambient/go.work")

	IsolateProcess()

	if value := os.Getenv("WB_AGENT_ID"); value != "" {
		t.Fatalf("WB_AGENT_ID = %q after IsolateProcess, want empty", value)
	}
	if value := os.Getenv("GOWORK"); value != "off" {
		t.Fatalf("GOWORK = %q after IsolateProcess, want %q", value, "off")
	}
}
